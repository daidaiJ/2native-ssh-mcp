//go:build integration

// Integration tests against a real SSH host. Run with:
//
//	go test -tags integration ./internal/manager/ -run BackgroundJob -v
//
// They load connection details from the repo-root config.json and expect a
// reachable "ubuntu" host. These tests are excluded from the default build.
package manager

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"2native-ssh-mcp/internal/config"
)

func loadTestManager(t *testing.T) *Manager {
	t.Helper()
	cfgPath := os.Getenv("SSH_MCP_TEST_CONFIG")
	if cfgPath == "" {
		wd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		cfgPath = filepath.Join(wd, "..", "..", "config.json")
	}
	opts, err := config.ParseArgs([]string{"--config-file", cfgPath})
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if _, ok := opts.Configs["ubuntu"]; !ok {
		// Same fallback as newSftpTestManager: re-register the first
		// configured connection under the name the tests reference.
		for _, conf := range opts.Configs {
			conf.Name = "ubuntu"
			opts.Configs["ubuntu"] = conf
			break
		}
	}
	if _, ok := opts.Configs["ubuntu"]; !ok {
		t.Fatal("config.json must define an 'ubuntu' connection for integration tests")
	}
	m, err := New(opts.Configs, opts.CommandLogDir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// remoteFileExists checks a remote path via a fresh exec command.
func remoteFileExists(t *testing.T, m *Manager, path string) bool {
	t.Helper()
	out, err := m.ExecuteCommand(context.Background(),
		"test -f "+shellQuote(path)+" && echo EXISTS || echo GONE", "", "ubuntu",
		RunOptions{Prevalidated: true, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("remote check %s: %v", path, err)
	}
	return strings.Contains(out.Stdout, "EXISTS")
}

// TestExecStabilityRawSession guards against the stdout/stderr shared-buffer
// data loss: runDetachedExec must capture output reliably across rapid execs.
func TestExecStabilityRawSession(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()
	client, err := m.EnsureConnected(context.Background(), "ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	for i := 0; i < 6; i++ {
		out, err := m.runDetachedExec(context.Background(), client, "echo B_"+string(rune('0'+i)), 5*time.Second)
		if err != nil {
			t.Fatalf("exec %d: %v", i, err)
		}
		if !strings.Contains(out, "B_") {
			t.Fatalf("exec %d returned no output: %q", i, out)
		}
	}
}

// TestDetachedSurvivesNoPTYExec verifies a setsid'd job survives the exec
// channel closing and a full connection drop.
func TestDetachedSurvivesNoPTYExec(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	client, err := m.EnsureConnected(context.Background(), "ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	script := "rm -f /tmp/detach.pid /tmp/detach.log; " +
		"setsid sh -c 'sleep 60; echo DONE' </dev/null >>/tmp/detach.log 2>&1 & " +
		"echo $! > /tmp/detach.pid; sleep 1; cat /tmp/detach.pid"
	if _, err := m.runDetachedExec(context.Background(), client, script, 15*time.Second); err != nil {
		t.Fatalf("start detached: %v", err)
	}

	m.Disconnect("ubuntu")
	client2, err := m.EnsureConnected(context.Background(), "ubuntu")
	if err != nil {
		t.Fatal(err)
	}
	check := "PID=$(cat /tmp/detach.pid 2>/dev/null); " +
		"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then echo ALIVE; else echo DEAD; fi"
	out, err := m.runDetachedExec(context.Background(), client2, check, 15*time.Second)
	if err != nil {
		t.Fatalf("check detached: %v", err)
	}
	if !strings.Contains(out, "ALIVE") {
		t.Fatalf("detached job did not survive no-PTY exec channel close: %s", out)
	}
}

// TestCommandResultIntegration verifies the structured result behavior on a
// real host: non-zero exit is a normal result, not an error.
func TestCommandResultIntegration(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	res, err := m.ExecuteCommand(context.Background(),
		"sh -c 'echo BUILD_RUNNING; exit 1'", "", "ubuntu",
		RunOptions{Prevalidated: true, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("non-zero exit must not be an error, got: %v", err)
	}
	if res.Status != StatusExited || res.ExitCode != 1 {
		t.Fatalf("expected exited/1, got %+v", res)
	}
	if !strings.Contains(res.Stdout, "BUILD_RUNNING") {
		t.Fatalf("stdout must contain BUILD_RUNNING, got %q", res.Stdout)
	}
	if !res.ReplaySafe {
		t.Fatalf("exited result must be replay-safe: %+v", res)
	}

	ok, err := m.ExecuteCommand(context.Background(), "echo OK", "", "ubuntu",
		RunOptions{Prevalidated: true, Timeout: 15 * time.Second})
	if err != nil {
		t.Fatalf("ok command: %v", err)
	}
	if ok.Status != StatusOK || ok.ExitCode != 0 || !strings.Contains(ok.Stdout, "OK") {
		t.Fatalf("expected ok/0 with OK output, got %+v", ok)
	}

	// Timeout must surface as an error with partial output, not replay-safe.
	timedOut, err := m.ExecuteCommand(context.Background(), "sleep 30", "", "ubuntu",
		RunOptions{Prevalidated: true, Timeout: 2 * time.Second})
	if err == nil {
		t.Fatalf("expected timeout error, got %+v", timedOut)
	}
	if timedOut.Status != StatusTimeout || timedOut.ReplaySafe {
		t.Fatalf("expected timeout status, not replay-safe, got %+v", timedOut)
	}
}

// TestSftpErrorIntegration verifies upload to a missing remote parent
// directory reports the parent, not a generic "file does not exist".
func TestSftpErrorIntegration(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	local := filepath.Join("upload-me.txt")
	if err := os.WriteFile(local, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(local)
	_, err := m.TransferFile(context.Background(), "upload", local, "/tmp/no/such/dir/upload-me.txt", "ubuntu", false, nil)
	if err == nil {
		t.Fatal("expected upload to missing parent to fail")
	}
	te := AsToolError(err)
	if !strings.Contains(te.Message, "Remote parent directory does not exist: /tmp/no/such/dir") {
		t.Fatalf("expected remote parent message, got: %s", te.Message)
	}
}

// TestBackgroundJobIntegration exercises the full background session
// lifecycle: start, read, survive a connection drop, and explicit close.
func TestBackgroundJobIntegration(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	// Clean up any leftover state from a previous failed run.
	_ = m.CloseSession(context.Background(), "bgtest")

	info, err := m.OpenSessionWithOptions(context.Background(), "bgtest", "ubuntu", SessionOpenOptions{
		Background: true,
		CmdString:  "sleep 60; echo DONE",
	})
	if err != nil {
		t.Fatalf("open background session: %v", err)
	}
	if !info.Background || !info.Running {
		t.Fatalf("expected background running session, got %+v", info)
	}

	// Read: job should be running and the log file present.
	out, err := m.ReadSessionOutput(context.Background(), "bgtest", 0, -1, 0)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !out.Running {
		t.Fatalf("expected job running on first read, got %+v", out)
	}
	if out.LogPath == "" {
		t.Fatal("expected LogPath in read output")
	}
	if !remoteFileExists(t, m, out.LogPath) {
		t.Fatalf("expected remote log file to exist at %s", out.LogPath)
	}

	// Simulate a connection drop: disconnect, then read again. The job must
	// survive and still report running.
	m.Disconnect("ubuntu")
	out, err = m.ReadSessionOutput(context.Background(), "bgtest", 0, -1, 0)
	if err != nil {
		t.Fatalf("read after disconnect: %v", err)
	}
	if !out.Running {
		t.Fatalf("expected job still running after disconnect, got %+v", out)
	}

	// Explicit close must kill the job and remove its pid/log files.
	if err := m.CloseSession(context.Background(), "bgtest"); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(3 * time.Second)
	if remoteFileExists(t, m, strings.TrimSuffix(out.LogPath, ".log")+".pid") {
		t.Fatal("expected pid file to be removed after close")
	}
}

// TestBackgroundReadTruncationIntegration verifies that a maxBytes-limited
// read tells the caller the chunk is a prefix: truncated=true carries how
// many bytes remain, and continuing from the returned offset yields the rest.
func TestBackgroundReadTruncationIntegration(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	name := "bgtrunc"
	_ = m.CloseSession(context.Background(), name)

	job := "for i in $(seq 1 200); do echo TRUNC_LINE_$i; done"
	if _, err := m.OpenSessionWithOptions(context.Background(), name, "ubuntu", SessionOpenOptions{
		Background: true,
		CmdString:  job,
	}); err != nil {
		t.Fatalf("open background session: %v", err)
	}
	defer m.CloseSession(context.Background(), name)

	// Wait for the job to finish so all bytes are already on disk.
	deadline := time.Now().Add(30 * time.Second)
	for {
		out, err := m.ReadSessionOutput(context.Background(), name, 0, -1, 1000)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !out.Running || time.Now().After(deadline) {
			break
		}
	}

	const capBytes = 256
	first, err := m.ReadSessionOutput(context.Background(), name, capBytes, 0, 0)
	if err != nil {
		t.Fatalf("capped read: %v", err)
	}
	if !first.Truncated || first.RemainingBytes <= 0 {
		t.Fatalf("a maxBytes-capped read must report truncation and the remainder, got %+v", first)
	}
	if len(first.Output) > capBytes {
		t.Fatalf("read returned %d bytes, cap is %d", len(first.Output), capBytes)
	}
	if first.Truncated != (first.RemainingBytes > 0) {
		t.Fatalf("truncated and remainingBytes disagree: %+v", first)
	}

	next, err := m.ReadSessionOutput(context.Background(), name, 0, first.Offset, 0)
	if err != nil {
		t.Fatalf("continue read: %v", err)
	}
	if next.Offset <= first.Offset {
		t.Fatalf("continuing from %d did not advance (next %d)", first.Offset, next.Offset)
	}
	if next.Truncated {
		t.Fatalf("the remaining output fits in one read, got %+v", next)
	}
	if next.RemainingBytes != 0 {
		t.Fatalf("a complete read must report no remainder, got %+v", next)
	}
}

// TestBackgroundLogCapIntegration verifies the bounded capture on a real
// host: a chatty job cannot grow its remote log past backgroundLogMaxBytes,
// the read reports how much was trimmed, and one answer carries the kept
// beginning, an omission marker and the newest bytes.
func TestBackgroundLogCapIntegration(t *testing.T) {
	m := loadTestManager(t)
	defer m.DisconnectAll()

	// Shrink the cap for this connection so the test stays quick.
	const capBytes = 64 * 1024
	m.mu.Lock()
	m.configs["ubuntu"].BackgroundLogMaxBytes = capBytes
	m.mu.Unlock()

	const name = "bgcap"
	_ = m.CloseSession(context.Background(), name)
	job := "echo HEAD_MARK; sleep 2; i=0; while [ $i -lt 4000 ]; do " +
		"echo \"line $i padding-padding-padding-padding\"; i=$((i+1)); done; sleep 2; echo TAIL_MARK"
	if _, err := m.OpenSessionWithOptions(context.Background(), name, "ubuntu", SessionOpenOptions{
		Background: true,
		CmdString:  job,
	}); err != nil {
		t.Fatalf("open background session: %v", err)
	}
	defer m.CloseSession(context.Background(), name)

	// Poll until the job finishes (or the budget runs out).
	deadline := time.Now().Add(60 * time.Second)
	var out *SessionOutput
	for {
		var err error
		out, err = m.ReadSessionOutput(context.Background(), name, 0, -1, 5000)
		if err != nil {
			t.Fatalf("read: %v", err)
		}
		if !out.Running || time.Now().After(deadline) {
			break
		}
	}
	if out.Running {
		t.Fatalf("job did not finish within the budget: %+v", out)
	}
	if out.DroppedBytes == 0 {
		t.Fatalf("a job this size must have been trimmed at a %d-byte cap: %+v", capBytes, out)
	}
	if out.LogMaxBytes != capBytes {
		t.Fatalf("read must report the cap in force, got %d", out.LogMaxBytes)
	}

	// Re-reading from the start answers with head, marker and tail in one go.
	fromStart, err := m.ReadSessionOutput(context.Background(), name, 0, 0, 0)
	if err != nil {
		t.Fatalf("read from offset 0: %v", err)
	}
	for _, want := range []string{"HEAD_MARK", "bytes of earlier output were dropped", "TAIL_MARK"} {
		if !strings.Contains(fromStart.Output, want) {
			t.Fatalf("a trimmed read must contain %q, got %d bytes: %q",
				want, len(fromStart.Output), fromStart.Output[:min(len(fromStart.Output), 200)])
		}
	}
	if fromStart.TotalBytes <= fromStart.DroppedBytes {
		t.Fatalf("totalBytes must cover the dropped bytes plus the retained log: %+v", fromStart)
	}
	if fromStart.TotalBytes <= capBytes {
		t.Fatalf("the job wrote more than the cap, totalBytes must say so: %+v", fromStart)
	}

	// The absolute offset never rewinds: continuing from the last position
	// yields nothing new and does not move backwards.
	after, err := m.ReadSessionOutput(context.Background(), name, 0, -1, 0)
	if err != nil {
		t.Fatalf("continue read: %v", err)
	}
	if after.Output != "" {
		t.Fatalf("a finished job must have nothing new to report, got %q", after.Output)
	}
	if after.Offset < fromStart.Offset {
		t.Fatalf("offset went backwards: %d -> %d", fromStart.Offset, after.Offset)
	}
}

// TestSessionReadWaitMs verifies action=read blocks for new output instead
// of returning empty (issue #3 Slice B: no idle spin-polling).
func TestSessionReadWaitMs(t *testing.T) {
	m := loadTestManager(t)
	name := "bgwait"
	_ = m.CloseSession(context.Background(), name)
	_, err := m.OpenSessionWithOptions(context.Background(), name, "ubuntu", SessionOpenOptions{
		Background: true,
		CmdString:  "sleep 2; echo WAITMS_DONE",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.CloseSession(context.Background(), name)

	start := time.Now()
	out, err := m.ReadSessionOutput(context.Background(), name, 0, -1, 8000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "WAITMS_DONE") {
		t.Fatalf("waitMs read must return the delayed output, got: %q", out.Output)
	}
	if time.Since(start) < 500*time.Millisecond {
		t.Fatalf("waitMs read returned before the job could produce output")
	}
}

// TestBackgroundPTY verifies the opt-in PTY wrapper for background jobs:
// the detached process sees a TTY on stdout (issue #3 Slice C). The knob is
// the connection's pty setting, with the manager-level override left for
// internal callers.
func TestBackgroundPTY(t *testing.T) {
	m := loadTestManager(t)
	name := "bgpty"
	_ = m.CloseSession(context.Background(), name)
	pty := true
	_, err := m.OpenSessionWithOptions(context.Background(), name, "ubuntu", SessionOpenOptions{
		Background: true,
		Pty:        &pty,
		CmdString:  "sleep 2; if [ -t 1 ]; then echo BG_PTY_YES; else echo BG_PTY_NO; fi",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer m.CloseSession(context.Background(), name)

	out, err := m.ReadSessionOutput(context.Background(), name, 0, -1, 8000)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.Output, "BG_PTY_YES") {
		t.Fatalf("background job with pty=true must see a TTY, got: %q", out.Output)
	}
}

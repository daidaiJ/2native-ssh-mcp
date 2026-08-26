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
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(wd, "..", "..", "config.json")
	opts, err := config.ParseArgs([]string{"--config-file", cfgPath})
	if err != nil {
		t.Fatalf("load config: %v", err)
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
	client, err := m.EnsureConnected("ubuntu")
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

	client, err := m.EnsureConnected("ubuntu")
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
	client2, err := m.EnsureConnected("ubuntu")
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
	_ = m.CloseSession("bgtest")

	info, err := m.OpenSessionWithOptions("bgtest", "ubuntu", SessionOpenOptions{
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
	out, err := m.ReadSessionOutput("bgtest", 0, -1)
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
	out, err = m.ReadSessionOutput("bgtest", 0, -1)
	if err != nil {
		t.Fatalf("read after disconnect: %v", err)
	}
	if !out.Running {
		t.Fatalf("expected job still running after disconnect, got %+v", out)
	}

	// Explicit close must kill the job and remove its pid/log files.
	if err := m.CloseSession("bgtest"); err != nil {
		t.Fatalf("close: %v", err)
	}
	time.Sleep(3 * time.Second)
	if remoteFileExists(t, m, strings.TrimSuffix(out.LogPath, ".log")+".pid") {
		t.Fatal("expected pid file to be removed after close")
	}
}

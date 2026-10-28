//go:build integration

// End-to-end verification of the escaping work against a real host. Run with:
//
//	SSH_MCP_TEST_CONFIG=/path/to/config.json go test -tags integration ./internal/manager/ -run Integration -v
//
// The config must define an "ubuntu" connection. Everything here runs over the
// real SSH transport: argv byte preservation, scriptFile staging and cleanup,
// the background exit code through the cleanup guard, lint wiring, the
// capability probe and the audit record's readable equivalent.
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

const integrationConn = "ubuntu"

func integrationTimeout() time.Duration { return 60 * time.Second }

// runIntegration runs one command on the test host and fails the test on a
// transport error.
func runIntegration(t *testing.T, m *Manager, cmdString string, opts RunOptions) CommandResult {
	t.Helper()
	if opts.Timeout == 0 {
		opts.Timeout = integrationTimeout()
	}
	res, err := m.ExecuteCommand(context.Background(), cmdString, "", integrationConn, opts)
	if err != nil {
		t.Fatalf("command %q failed: %v", cmdString, err)
	}
	return res
}

func TestIntegrationArgvModePreservesArgumentsByteForByte(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	args := []string{"a b", "it's", "$HOME", `back\slash`, "*", "-n", "line1\nline2", ""}
	execText, auditText, err := JoinArgv("printf", append([]string{"<%s>\\n"}, args...))
	if err != nil {
		t.Fatal(err)
	}
	res := runIntegration(t, m, execText, RunOptions{AuditCommand: auditText})

	want := []string{"<a b>", "<it's>", "<$HOME>", `<back\slash>`, "<*>", "<-n>", "<line1", "line2>", "<>"}
	got := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(got) != len(want) {
		t.Fatalf("argv mode must deliver %d arguments, got %d:\n%q", len(want), len(got), res.Stdout)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argument %d changed: got %q want %q (full output:\n%s)", i, got[i], want[i], res.Stdout)
		}
	}
}

// The naive join is what an agent hand-writes today: the contrast is the
// reason argv mode exists.
func TestIntegrationNaiveJoinStillSplitsAndExpands(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	res := runIntegration(t, m, "printf '<%s>\\n' $HOME *", RunOptions{})
	lines := strings.Split(strings.TrimRight(res.Stdout, "\n"), "\n")
	if len(lines) < 2 {
		t.Fatalf("unquoted $HOME and * must still expand into many arguments, got:\n%s", res.Stdout)
	}
	if lines[0] == "<$HOME>" || lines[0] == "<*>" {
		t.Fatalf("unquoted arguments must not survive verbatim, got:\n%s", res.Stdout)
	}
}

func TestIntegrationStagedScriptRunsAndCleansUp(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	local := filepath.Join(t.TempDir(), "probe script.sh")
	content := []byte("#!/bin/sh\necho staged-ok\nexit 7\n")
	if err := os.WriteFile(local, content, 0o600); err != nil {
		t.Fatal(err)
	}
	remotePath := scriptTempPath(local)
	if err := m.StageScript(context.Background(), integrationConn, remotePath, content); err != nil {
		t.Fatalf("staging failed: %v", err)
	}
	if !strings.HasPrefix(remotePath, "/tmp/.2native-ssh-mcp-") || strings.Contains(remotePath, " ") {
		t.Fatalf("unexpected staging path: %s", remotePath)
	}

	cmd := ScriptCommand(remotePath, content, "sh", true)
	res := runIntegration(t, m, cmd, RunOptions{AuditCommand: ScriptAuditText(local, remotePath, "sh", content, true)})
	if res.ExitCode != 7 {
		t.Fatalf("the script's exit code must survive, got %d (stdout=%q)", res.ExitCode, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "staged-ok") {
		t.Fatalf("script output missing: %q", res.Stdout)
	}
	// The shebang decides the interpreter, and the temp file must be gone.
	if check := runIntegration(t, m, "test -e "+shellQuote(remotePath)+" && echo present || echo gone", RunOptions{}); !strings.Contains(check.Stdout, "gone") {
		t.Fatalf("scriptCleanup=true must remove the staged file, got: %q", check.Stdout)
	}
}

func TestIntegrationStagedScriptKeptWhenCleanupOff(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	local := filepath.Join(t.TempDir(), "keep.sh")
	content := []byte("echo kept\n")
	remotePath := scriptTempPath(local)
	if err := m.StageScript(context.Background(), integrationConn, remotePath, content); err != nil {
		t.Fatal(err)
	}
	res := runIntegration(t, m, ScriptCommand(remotePath, content, "sh", false), RunOptions{})
	if res.ExitCode != 0 || !strings.Contains(res.Stdout, "kept") {
		t.Fatalf("script must run: rc=%d stdout=%q", res.ExitCode, res.Stdout)
	}
	check := runIntegration(t, m, "test -e "+shellQuote(remotePath)+" && echo present || echo gone", RunOptions{})
	if !strings.Contains(check.Stdout, "present") {
		t.Fatalf("scriptCleanup=false must leave the file, got: %q", check.Stdout)
	}
	runIntegration(t, m, "rm -f "+shellQuote(remotePath), RunOptions{})
}

func TestIntegrationBackgroundStagedScriptKeepsExitCode(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	local := filepath.Join(t.TempDir(), "bg.sh")
	content := []byte("sleep 2\nexit 5\n")
	remotePath := scriptTempPath(local)
	if err := m.StageScript(context.Background(), integrationConn, remotePath, content); err != nil {
		t.Fatal(err)
	}
	session := "bg-script-check"
	if _, err := m.OpenSessionWithOptions(context.Background(), session, integrationConn, SessionOpenOptions{
		Background:   true,
		CmdString:    ScriptCommand(remotePath, content, "sh", true),
		AuditCommand: ScriptAuditText(local, remotePath, "sh", content, true),
	}); err != nil {
		t.Fatalf("background start failed: %v", err)
	}
	defer m.CloseSession(context.Background(), session)

	code := waitForBackgroundExit(t, m, session)
	if code != 5 {
		t.Fatalf("the detached job's exit code must survive the cleanup guard, got %d", code)
	}
	check := runIntegration(t, m, "test -e "+shellQuote(remotePath)+" && echo present || echo gone", RunOptions{})
	if !strings.Contains(check.Stdout, "gone") {
		t.Fatalf("the detached run must clean up its staged file, got: %q", check.Stdout)
	}
}

// waitForBackgroundExit polls the background session until the job reports an
// exit code.
func waitForBackgroundExit(t *testing.T, m *Manager, session string) int {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		out, err := m.ReadSessionOutput(context.Background(), session, -1, 4096, 2000)
		if err != nil {
			t.Fatalf("read background output: %v", err)
		}
		if out != nil && out.ExitCode != nil {
			return *out.ExitCode
		}
	}
	t.Fatal("background job did not finish within 45s")
	return -1
}

func TestIntegrationLintWarnReportsAndRuns(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	// `$1` inside a double-quoted -c script is expanded to nothing by the
	// outer shell: the command still exits 0 with the wrong output.
	cmd := `bash -c "printf '%s\n' $1"`
	res := runIntegration(t, m, cmd, RunOptions{LintMode: config.LintWarn})
	if len(res.Lint) == 0 {
		t.Fatal("lintMode=warn must report the nested positional finding")
	}
	if !strings.Contains(strings.Join(res.Lint, "\n"), CodeLintNestedPositional) {
		t.Fatalf("unexpected findings: %v", res.Lint)
	}
	if !strings.Contains(res.Text(), "[lint]") {
		t.Fatalf("the text result must carry the findings:\n%s", res.Text())
	}
}

func TestIntegrationLintStrictRefusesBeforeTouchingTheHost(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	marker := "/tmp/.2native-ssh-mcp-lint-marker"
	runIntegration(t, m, "rm -f "+shellQuote(marker), RunOptions{})

	cmd := "touch " + marker + `; echo "unterminated`
	_, err := m.ExecuteCommand(context.Background(), cmd, "", integrationConn,
		RunOptions{LintMode: config.LintStrict, Timeout: integrationTimeout()})
	if err == nil {
		t.Fatal("lintMode=strict must refuse a command with an error-class finding")
	}
	if code := AsToolError(err).Code; code != CodeCommandLintFailed {
		t.Fatalf("expected %s, got %s (%v)", CodeCommandLintFailed, code, err)
	}
	check := runIntegration(t, m, "test -e "+shellQuote(marker)+" && echo present || echo gone", RunOptions{})
	if !strings.Contains(check.Stdout, "gone") {
		t.Fatalf("a refused command must never reach the host, got: %q", check.Stdout)
	}
}

func TestIntegrationAuditRecordHoldsTheReadableEquivalent(t *testing.T) {
	logDir := t.TempDir()
	m := newTestManager(t, func(cfg *config.SSHConfig) {
		size := 50
		cfg.CommandLogSize = &size
		cfg.CommandLogDir = logDir
		cfg.CommandLogOnlySuccess = false
	})
	execText, auditText, err := JoinArgv("printf", []string{"<%s>\\n", "a b", "$HOME", "*"})
	if err != nil {
		t.Fatal(err)
	}
	runIntegration(t, m, execText, RunOptions{AuditCommand: auditText})
	m.DisconnectAll() // flush the audit chain

	records, err := readAuditRecords(filepath.Join(logDir, integrationConn+".jsonl"))
	if err != nil {
		t.Fatalf("read audit log: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("no audit records written")
	}
	last := records[len(records)-1]
	if last.Command != auditText {
		t.Fatalf("the audit record must hold the readable equivalent:\n got: %s\nwant: %s", last.Command, auditText)
	}
	if last.Command == execText || strings.Contains(last.Command, "'printf'") {
		t.Fatalf("the audit record must not hold the quoted execution text: %s", last.Command)
	}
}

func TestIntegrationCapabilityProbe(t *testing.T) {
	m := newSftpTestManager(t)
	defer m.DisconnectAll()

	if err := m.Connect(context.Background(), integrationConn); err != nil {
		t.Fatalf("connect: %v", err)
	}
	caps := waitForCapabilities(t, m)
	if caps.Error != "" {
		t.Fatalf("probe failed: %s", caps.Error)
	}
	if !caps.Base64 {
		t.Fatalf("base64 must be detected on the test host: %+v", caps)
	}
	t.Logf("probed capabilities: base64=%t jq=%t", caps.Base64, caps.JQ)
}

func TestIntegrationCapabilityProbeDisabled(t *testing.T) {
	off := false
	m := newTestManager(t, func(cfg *config.SSHConfig) { cfg.AutoProbe = &off })
	defer m.DisconnectAll()

	if err := m.Connect(context.Background(), integrationConn); err != nil {
		t.Fatalf("connect: %v", err)
	}
	caps := m.Capabilities(integrationConn)
	if caps == nil || caps.Skipped != "autoProbe=false" {
		t.Fatalf("autoProbe=false must be reported instead of probed, got %+v", caps)
	}
	if caps.Base64 || caps.JQ {
		t.Fatalf("no capability may be claimed when the probe is off: %+v", caps)
	}
}

func waitForCapabilities(t *testing.T, m *Manager) *RemoteCapabilities {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if caps := m.Capabilities(integrationConn); caps != nil {
			return caps
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("capability probe did not finish within 30s")
	return nil
}

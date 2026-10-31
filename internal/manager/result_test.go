package manager

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestBuildCommandResultNormalizesCR(t *testing.T) {
	res := buildCommandResult("a\r\nb\rc\n", "err\r\nline", 0, StatusOK, nil)
	for name, out := range map[string]string{"stdout": res.Stdout, "stderr": res.Stderr} {
		if strings.Contains(out, "\r") {
			t.Fatalf("%s must not contain carriage returns, got %q", name, out)
		}
	}
	if res.Stdout != "a\nb\nc\n" {
		t.Fatalf("stdout = %q, want %q", res.Stdout, "a\nb\nc\n")
	}
}

func TestCommandResultText(t *testing.T) {
	res := CommandResult{
		Stdout:   "BUILD_RUNNING",
		ExitCode: 1,
		Status:   StatusExited,
	}
	text := res.Text()
	if !strings.Contains(text, "BUILD_RUNNING") || !strings.Contains(text, "[exit code] 1") {
		t.Fatalf("Text() must show stdout and exit code separately, got: %q", text)
	}
}

// An interrupted command must say what the remote side can be assumed to have
// done and what to do about it: "the call failed" and "the command did not
// run" are different facts.
func TestInterruptGuidance(t *testing.T) {
	cases := []struct {
		status string
		state  string
		want   string
	}{
		{StatusTimeout, ExecutionStarted, "may still be running"},
		{StatusOutputLimit, ExecutionStarted, "aborted"},
		{StatusCancelled, ExecutionStarted, "cancelled"},
		{StatusConnectionLost, ExecutionUnknown, "never have started"},
	}
	for _, tc := range cases {
		state, guidance := interruptGuidance(tc.status)
		if state != tc.state {
			t.Fatalf("%s: executionState = %q, want %q", tc.status, state, tc.state)
		}
		if !strings.Contains(guidance, tc.want) {
			t.Fatalf("%s: guidance must mention %q, got %q", tc.status, tc.want, guidance)
		}
		if !strings.Contains(guidance, "before") {
			t.Fatalf("%s: guidance must tell the caller to check the remote state first, got %q", tc.status, guidance)
		}
	}
	for _, status := range []string{StatusOK, StatusExited, ""} {
		if state, guidance := interruptGuidance(status); state != "" || guidance != "" {
			t.Fatalf("%q must not carry a recovery note, got %q/%q", status, state, guidance)
		}
	}
}

func TestBuildCommandResultCarriesRecoveryState(t *testing.T) {
	timedOut := buildCommandResult("partial", "", -1, StatusTimeout, nil)
	if timedOut.ExecutionState != ExecutionStarted || timedOut.Guidance == "" || timedOut.ReplaySafe {
		t.Fatalf("timeout result must be started/not replay-safe with guidance: %+v", timedOut)
	}
	lost := buildCommandResult("partial", "", -1, StatusConnectionLost, nil)
	if lost.ExecutionState != ExecutionUnknown {
		t.Fatalf("a dropped connection leaves the execution state unknown, got %q", lost.ExecutionState)
	}
	ok := buildCommandResult("done", "", 0, StatusOK, nil)
	if ok.ExecutionState != "" || ok.Guidance != "" {
		t.Fatalf("a completed command carries no recovery note: %+v", ok)
	}
	// The text rendering shows the recovery line for interrupted results.
	if !strings.Contains(timedOut.Text(), "[recovery]") {
		t.Fatalf("Text() must render the recovery guidance: %q", timedOut.Text())
	}
}

// Truncated output must be labelled as incomplete in the text rendering, not
// just in the JSON flag: an agent reading prose should never take a prefix
// for the whole output.
func TestCommandResultTextMarksTruncation(t *testing.T) {
	res := CommandResult{
		Stdout:       "partial output",
		ExitCode:     -1,
		Status:       StatusOutputLimit,
		Truncated:    true,
		ClippedBytes: 4096,
	}
	text := res.Text()
	for _, want := range []string{"[output truncated]", "incomplete", "4096 bytes were discarded"} {
		if !strings.Contains(text, want) {
			t.Fatalf("Text() must mention %q, got:\n%s", want, text)
		}
	}
}

func TestCommandResultTextWithStderr(t *testing.T) {
	res := CommandResult{
		Stdout:   "out",
		Stderr:   "err",
		ExitCode: 0,
		Status:   StatusOK,
	}
	text := res.Text()
	if !strings.Contains(text, "[stderr]") || !strings.Contains(text, "err") {
		t.Fatalf("Text() must include stderr section, got: %q", text)
	}
}

func TestBuildCommandResultReplaySafe(t *testing.T) {
	cfg := &config.SSHConfig{}
	ok := buildCommandResult("x", "", 0, StatusOK, cfg)
	if !ok.ReplaySafe || ok.Status != StatusOK {
		t.Fatalf("ok result must be replay-safe: %+v", ok)
	}
	exited := buildCommandResult("x", "", 1, StatusExited, cfg)
	if !exited.ReplaySafe || exited.Status != StatusExited || exited.ExitCode != 1 {
		t.Fatalf("exited result must be replay-safe with exit code: %+v", exited)
	}
	lost := buildCommandResult("partial", "", -1, StatusConnectionLost, cfg)
	if lost.ReplaySafe || !lost.Partial || lost.Status != StatusConnectionLost {
		t.Fatalf("connection_lost result must not be replay-safe: %+v", lost)
	}
	timeout := buildCommandResult("partial", "", -1, StatusTimeout, cfg)
	if timeout.ReplaySafe || !timeout.Partial {
		t.Fatalf("timeout result must not be replay-safe: %+v", timeout)
	}
}

func TestBuildCommandResultRedacts(t *testing.T) {
	// Redaction is opt-in (redactSecrets): by default secrets stay visible.
	cfg := &config.SSHConfig{}
	res := buildCommandResult("token=SECRET123 done", "", 0, StatusOK, cfg)
	if !strings.Contains(res.Stdout, "SECRET123") {
		t.Fatalf("redaction must be off by default, got: %q", res.Stdout)
	}

	yes := true
	res = buildCommandResult("token=SECRET123 done", "", 0, StatusOK, &config.SSHConfig{RedactSecrets: &yes})
	if strings.Contains(res.Stdout, "SECRET123") {
		t.Fatalf("redactSecrets=true must redact, got: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "[REDACTED]") {
		t.Fatalf("redactSecrets=true must show the placeholder, got: %q", res.Stdout)
	}
}

func TestBuildCommandResultStripsANSIByDefault(t *testing.T) {
	cfg := &config.SSHConfig{}
	res := buildCommandResult("\x1b[31mX\x1b[0m", "\x1b[32mY\x1b[0m", 0, StatusOK, cfg)
	if res.Stdout != "X" || res.Stderr != "Y" {
		t.Fatalf("ANSI must be stripped by default, got stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}
}

func TestBuildCommandResultKeepsANSIWhenDisabled(t *testing.T) {
	falseVal := false
	cfg := &config.SSHConfig{StripAnsi: &falseVal}
	res := buildCommandResult("\x1b[31mX\x1b[0m", "", 0, StatusOK, cfg)
	if res.Stdout != "\x1b[31mX\x1b[0m" {
		t.Fatalf("stripAnsi=false must keep CSI sequences, got: %q", res.Stdout)
	}
}
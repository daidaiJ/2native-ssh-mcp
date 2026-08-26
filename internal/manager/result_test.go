package manager

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

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
	cfg := &config.SSHConfig{}
	res := buildCommandResult("token=SECRET123 done", "", 0, StatusOK, cfg)
	if strings.Contains(res.Stdout, "SECRET123") {
		t.Fatalf("stdout must be redacted, got: %q", res.Stdout)
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
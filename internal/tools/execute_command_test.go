package tools

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

func TestCommandResultDefaultsToText(t *testing.T) {
	res := manager.CommandResult{
		Stdout:     "hello\nworld",
		ExitCode:   0,
		Status:     manager.StatusOK,
		ReplaySafe: true,
	}
	text := resultText(t, commandResult(res, nil))
	want := "hello\nworld\n[exit code] 0"
	if text != want {
		t.Fatalf("default format should be the sectioned text result:\n got: %q\nwant: %q", text, want)
	}
	if strings.Contains(text, "\"stdout\"") {
		t.Fatalf("text result must not embed the JSON envelope: %q", text)
	}
}

func TestCommandResultJSONOptIn(t *testing.T) {
	res := manager.CommandResult{
		Stdout:     "hello",
		ExitCode:   3,
		Status:     manager.StatusExited,
		ReplaySafe: true,
	}
	cfg := &config.SSHConfig{ResultFormat: "json"}
	text := resultText(t, commandResult(res, cfg))
	for _, want := range []string{`"exitCode": 3`, `"status": "exited"`, `"stdout": "hello"`} {
		if !strings.Contains(text, want) {
			t.Fatalf("json result missing %s:\n%s", want, text)
		}
	}
}

func TestCommandResultUnknownFormatFallsBackToText(t *testing.T) {
	res := manager.CommandResult{
		Stdout:     "x",
		ExitCode:   0,
		Status:     manager.StatusOK,
		ReplaySafe: true,
	}
	cfg := &config.SSHConfig{ResultFormat: "yaml"}
	text := resultText(t, commandResult(res, cfg))
	if strings.Contains(text, "\"stdout\"") {
		t.Fatalf("unknown resultFormat must fall back to text, got: %q", text)
	}
}

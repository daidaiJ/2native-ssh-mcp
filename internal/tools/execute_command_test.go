package tools

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

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

// A default-routed call (connectionName omitted) must surface which
// connection and host actually served it, in both result formats.
func TestCommandResultDefaultConnectionSummary(t *testing.T) {
	cfg := &config.SSHConfig{Host: "203.0.113.5", Port: 22, Aliases: []string{"web", "prod"}}
	res := manager.CommandResult{
		Stdout:     "ok",
		ExitCode:   0,
		Status:     manager.StatusOK,
		ReplaySafe: true,
		Connection: "default",
		Host:       cfg.Host,
		Port:       cfg.Port,
		Aliases:    cfg.Aliases,
	}

	text := resultText(t, commandResult(res, nil))
	want := "connection=default host=203.0.113.5 port=22 aliases=web,prod"
	if !strings.HasPrefix(text, want+"\n\n") {
		t.Fatalf("text result must start with the default-connection summary:\n%s", text)
	}

	jsonText := resultText(t, commandResult(res, &config.SSHConfig{ResultFormat: "json"}))
	for _, want := range []string{`"connection": "default"`, `"host": "203.0.113.5"`, `"port": 22`, `"aliases": [`} {
		if !strings.Contains(jsonText, want) {
			t.Fatalf("json result missing %s:\n%s", want, jsonText)
		}
	}

	// Explicit connectionName: no stamping, no summary line.
	plain := manager.CommandResult{Stdout: "ok", ExitCode: 0, Status: manager.StatusOK, ReplaySafe: true}
	if got := resultText(t, commandResult(plain, nil)); strings.Contains(got, "connection=default") {
		t.Fatalf("explicit-connection result must not carry the summary:\n%s", got)
	}
}

func TestConnectionSummary(t *testing.T) {
	if got, want := connectionSummary("default", "192.0.2.1", 2222, nil), "connection=default host=192.0.2.1 port=2222"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got, want := connectionSummary("default", "192.0.2.1", 2222, []string{"web"}), "connection=default host=192.0.2.1 port=2222 aliases=web"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// Error results from a default-routed call must carry the connection identity
// so a retrying agent knows which host it was talking to.
func TestErrorResultForCarriesConnectionIdentity(t *testing.T) {
	res := manager.CommandResult{
		Status:     manager.StatusConnectionLost,
		Partial:    true,
		ReplaySafe: false,
		Connection: "default",
		Host:       "203.0.113.5",
		Port:       22,
	}
	out := errorResultFor(manager.NewToolError(manager.CodeSSHConnectionLost, "lost", true), res)
	text := out.Content[0].(mcp.TextContent).Text
	for _, want := range []string{`"connection": "default"`, `"host": "203.0.113.5"`, `"port": 22`} {
		if !strings.Contains(text, want) {
			t.Fatalf("error result missing %s:\n%s", want, text)
		}
	}
}

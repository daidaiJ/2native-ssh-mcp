package tools

import (
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

func newTestManager(t *testing.T) *manager.Manager {
	t.Helper()
	m, err := manager.New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func resultText(t *testing.T, res *mcp.CallToolResult) string {
	t.Helper()
	if len(res.Content) == 0 {
		return ""
	}
	text, ok := res.Content[0].(mcp.TextContent)
	if !ok {
		t.Fatalf("expected TextContent, got %T", res.Content[0])
	}
	return text.Text
}

func TestSessionToolListWithoutSessionName(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "list"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("action=list must succeed without sessionName, got: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Active sessions") {
		t.Fatalf("list must render the session summary, got: %s", text)
	}
}

func TestSessionToolListFiltersByConnection(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "list", "connectionName": "dev"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("action=list with connectionName must succeed, got: %+v", res)
	}
}

func TestSessionToolInvalidAction(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("invalid action must be an error")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "Invalid action") || !strings.Contains(text, "must be open, read, close, or list") {
		t.Fatalf("invalid action must be reported before missing sessionName, got: %s", text)
	}
}

func TestSessionToolOpenRequiresSessionName(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "open"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("action=open without sessionName must be an error")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "COMMAND_VALIDATION_FAILED") ||
		!strings.Contains(text, "sessionName is required for action=open") {
		t.Fatalf("expected validation error, got: %s", text)
	}
}

func TestSessionToolReadRequiresSessionName(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "read"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("action=read without sessionName must be an error")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "sessionName is required for action=read") {
		t.Fatalf("expected validation error, got: %s", text)
	}
}

func TestSessionToolCloseRequiresSessionName(t *testing.T) {
	m := newTestManager(t)
	res, err := handleSessionTool(m, map[string]any{"action": "close"})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Fatal("action=close without sessionName must be an error")
	}
	text := resultText(t, res)
	if !strings.Contains(text, "sessionName is required for action=close") {
		t.Fatalf("expected validation error, got: %s", text)
	}
}

func TestSessionToolCloseIdempotent(t *testing.T) {
	m := newTestManager(t)
	// Closing a session that was never opened (or already closed by the
	// idle TTL) must succeed, not report COMMAND_VALIDATION_FAILED.
	res, err := handleSessionTool(m, map[string]any{"action": "close", "sessionName": "nope"})
	if err != nil {
		t.Fatal(err)
	}
	if res.IsError {
		t.Fatalf("close of a never-opened session must succeed, got: %+v", res)
	}
	text := resultText(t, res)
	if !strings.Contains(text, `Session "nope" closed`) {
		t.Fatalf("expected close confirmation, got: %s", text)
	}
}
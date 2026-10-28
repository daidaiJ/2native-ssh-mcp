package tools

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// requestManager builds a manager with one connection that never dials: the
// resolution tests below only exercise argument handling.
func requestManager(t *testing.T) *manager.Manager {
	t.Helper()
	m, err := manager.New(map[string]*config.SSHConfig{
		"default": {Name: "default", Host: "example.invalid", Port: 22, Username: "tester"},
	}, "")
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	return m
}

func TestResolveExecuteRequestShellMode(t *testing.T) {
	req, err := resolveExecuteRequest(requestManager(t),
		map[string]any{"cmdString": "ls -la"}, "", "")
	if err != nil {
		t.Fatalf("shell mode must resolve: %v", err)
	}
	if req.execCommand != "ls -la" || req.auditCommand != "" {
		t.Fatalf("shell mode must pass the command through unchanged: %+v", req)
	}
	if req.gateText() != "ls -la" {
		t.Fatalf("gate text must fall back to the command, got %q", req.gateText())
	}
	if req.lintMode != "" {
		t.Fatalf("no per-call lint mode must stay empty (connection default applies), got %q", req.lintMode)
	}
}

func TestResolveExecuteRequestArgvMode(t *testing.T) {
	args := map[string]any{
		"cmdString": "grep",
		"args":      []any{"-rn", "foo bar", "$HOME", "*"},
	}
	req, err := resolveExecuteRequest(requestManager(t), args, "", "")
	if err != nil {
		t.Fatalf("argv mode must resolve: %v", err)
	}
	want := `'grep' '-rn' 'foo bar' '$HOME' '*'`
	if req.execCommand != want {
		t.Fatalf("every argument must be quoted for execution:\n got: %s\nwant: %s", req.execCommand, want)
	}
	if req.auditCommand != `grep -rn 'foo bar' '$HOME' '*'` {
		t.Fatalf("audit text must stay readable, got: %s", req.auditCommand)
	}
	if req.gateText() != req.auditCommand {
		t.Fatalf("the gate must see the readable equivalent, got %q", req.gateText())
	}
	if req.lintMode != config.LintOff {
		t.Fatalf("argv mode must switch the lint off, got %q", req.lintMode)
	}
}

func TestResolveExecuteRequestAcceptsStringSliceArgs(t *testing.T) {
	req, err := resolveExecuteRequest(requestManager(t),
		map[string]any{"cmdString": "echo", "args": []string{"a b"}}, "", "")
	if err != nil {
		t.Fatalf("[]string args must be accepted: %v", err)
	}
	if req.execCommand != `'echo' 'a b'` {
		t.Fatalf("unexpected argv command: %s", req.execCommand)
	}
}

func TestResolveExecuteRequestRejectsConflictingArguments(t *testing.T) {
	m := requestManager(t)
	cases := []struct {
		name string
		args map[string]any
		want string
	}{
		{"neither", map[string]any{}, "cmdString (or scriptFile) must be a non-empty command"},
		{"blank cmd", map[string]any{"cmdString": "   "}, "cmdString (or scriptFile) must be a non-empty command"},
		{"cmd and script", map[string]any{"cmdString": "ls", "scriptFile": "/tmp/x.sh"}, "mutually exclusive"},
		{"script and args", map[string]any{"scriptFile": "/tmp/x.sh", "args": []any{"a"}}, "cannot be combined with scriptFile"},
		{"non-string arg", map[string]any{"cmdString": "ls", "args": []any{1}}, "args[0] must be a string"},
		{"args not an array", map[string]any{"cmdString": "ls", "args": "ls"}, "args must be an array of strings"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveExecuteRequest(m, tc.args, "", "")
			if err == nil {
				t.Fatalf("expected a validation error for %v", tc.args)
			}
			if code := manager.AsToolError(err).Code; code != manager.CodeCommandValidationFailed {
				t.Fatalf("validation errors must use %s, got %s", manager.CodeCommandValidationFailed, code)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q must mention %q", err.Error(), tc.want)
			}
		})
	}
}

func TestResolveExecuteRequestScriptFileReadsLocally(t *testing.T) {
	m := requestManager(t)
	dir := t.TempDir()
	script := filepath.Join(dir, "deploy.sh")
	if err := os.WriteFile(script, []byte("echo hi\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The path scope must allow the temp dir, exactly as a configured
	// connection with localPathMode=any would.
	if _, err := resolveExecuteRequest(m,
		map[string]any{"scriptFile": script}, "", ""); err == nil {
		t.Fatal("a script outside the allowed local paths must be rejected")
	}

	missing := filepath.Join(dir, "nope.sh")
	_, err := resolveExecuteRequest(m,
		map[string]any{"scriptFile": missing}, "", "")
	if err == nil {
		t.Fatal("a missing script must be reported before anything is staged")
	}
	if code := manager.AsToolError(err).Code; code != manager.CodeLocalPathNotAllowed && code != manager.CodeLocalFileReadFailed {
		t.Fatalf("unexpected error code for a missing script: %s", code)
	}
}

func TestResolveExecuteRequestScriptFileDoesNotUpload(t *testing.T) {
	m, err := manager.New(map[string]*config.SSHConfig{
		"default": {Name: "default", Host: "example.invalid", Port: 22, Username: "tester", LocalPathMode: config.LocalPathModeAny},
	}, "")
	if err != nil {
		t.Fatalf("manager: %v", err)
	}
	script := filepath.Join(t.TempDir(), "deploy.sh")
	content := []byte("#!/bin/sh\necho hi\n")
	if err := os.WriteFile(script, content, 0o600); err != nil {
		t.Fatal(err)
	}
	req, err := resolveExecuteRequest(m, map[string]any{"scriptFile": script}, "", "")
	if err != nil {
		t.Fatalf("a readable local script must resolve without connecting: %v", err)
	}
	if req.script == nil || req.script.RemotePath == "" || req.scriptRun == nil {
		t.Fatal("resolution must prepare the upload, not skip it")
	}
	if req.scriptRun.RemotePath != req.script.RemotePath {
		t.Fatalf("audit path and upload path must match: %s vs %s", req.scriptRun.RemotePath, req.script.RemotePath)
	}
	if !strings.Contains(req.execCommand, req.script.RemotePath) {
		t.Fatalf("exec command must name the staged path, got %s", req.execCommand)
	}
	if !strings.Contains(req.auditCommand, "echo hi") {
		t.Fatalf("audit text must carry the script body, got %s", req.auditCommand)
	}
	if req.lintMode != config.LintOff {
		t.Fatalf("scriptFile must switch the lint off, got %q", req.lintMode)
	}
	if req.scriptRun.Shebang != true {
		t.Fatal("a shebang script must be marked as such")
	}
}

package manager

import (
	"strings"
	"testing"
)

func TestBuildPIDWrapperScript(t *testing.T) {
	pidFile := "/tmp/.ssh-mcp-pid_ab12.pid"
	wrapped := buildPIDWrapperScript("echo hi", pidFile)
	for _, want := range []string{
		"echo $$ > '/tmp/.ssh-mcp-pid_ab12.pid'",
		"trap 'rm -f '/tmp/.ssh-mcp-pid_ab12.pid'' EXIT",
		"echo hi",
	} {
		if !strings.Contains(wrapped, want) {
			t.Fatalf("wrapper missing %q:\n%s", want, wrapped)
		}
	}
	if !strings.HasSuffix(wrapped, "echo hi") {
		t.Fatalf("user command must run last:\n%s", wrapped)
	}
}

func TestRemoteKillScript(t *testing.T) {
	script := remoteKillScript("TERM", "/tmp/.ssh-mcp-pid_x.pid")
	for _, want := range []string{
		`pid=$(cat '/tmp/.ssh-mcp-pid_x.pid' 2>/dev/null)`,
		`kill -TERM -- -"$pid"`,
		`kill -TERM "$pid"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("kill script missing %q:\n%s", want, script)
		}
	}
}

func TestRemoteKillScriptSkipsWhenPIDEmpty(t *testing.T) {
	// No pidfile content -> the guard short-circuits and the script exits 0.
	script := remoteKillScript("KILL", "/tmp/pf")
	if !strings.HasPrefix(script, "pid=$(cat") || !strings.Contains(script, `[ -n "$pid" ] && {`) {
		t.Fatalf("kill script must be guarded by a non-empty pid check:\n%s", script)
	}
	if !strings.Contains(script, "exit 0") {
		t.Fatalf("kill script must end with exit 0 so a missing pid is not an error:\n%s", script)
	}
}

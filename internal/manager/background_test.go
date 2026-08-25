package manager

import (
	"strings"
	"testing"
)

func TestParseBGReadOutput(t *testing.T) {
	running, total, chunk := parseBGReadOutput("__MCP_BG_HDR__running=1 size=42\nhello")
	if !running || total != 42 || chunk != "hello" {
		t.Fatalf("parseBGReadOutput: running=%v total=%d chunk=%q", running, total, chunk)
	}
}

func TestParseBGStartedPID(t *testing.T) {
	if pid := parseBGStartedPID("__MCP_BG_STARTED__ pid=123\n"); pid != 123 {
		t.Fatalf("parseBGStartedPID = %d, want 123", pid)
	}
	if pid := parseBGStartedPID("__MCP_BG_FAILED__\n"); pid != 0 {
		t.Fatalf("parseBGStartedPID on failure = %d, want 0", pid)
	}
	if pid := parseBGStartedPID(""); pid != 0 {
		t.Fatalf("parseBGStartedPID on empty = %d, want 0", pid)
	}
}

func TestBuildBGStarterScriptDetaches(t *testing.T) {
	script := buildBGStarterScript("/tmp/x.log", "/tmp/x.pid", "/tmp/x.exit", "sleep 60; echo DONE")
	for _, want := range []string{"setsid", "</dev/null", "__MCP_BG_STARTED__", "kill -0", "sleep 1"} {
		if !strings.Contains(script, want) {
			t.Fatalf("starter script missing %q:\n%s", want, script)
		}
	}
	if strings.Contains(script, "nohup") {
		t.Fatalf("starter must not rely on nohup:\n%s", script)
	}
	// The body must be passed as a single-quoted argument, not spliced raw.
	if !strings.Contains(script, "'sleep 60; echo DONE'") {
		t.Fatalf("starter must pass the body as a single-quoted argument:\n%s", script)
	}
}

func TestBuildBGStarterScriptQuotesBody(t *testing.T) {
	// A body with a single quote must survive quoting.
	script := buildBGStarterScript("/tmp/x.log", "/tmp/x.pid", "/tmp/x.exit", "echo it's")
	if !strings.Contains(script, `'echo it'\''s'`) {
		t.Fatalf("starter must shell-quote the body:\n%s", script)
	}
}

func TestBuildBGReadScript(t *testing.T) {
	script := buildBGReadScript("/tmp/x.log", "/tmp/x.pid", 10, 1024)
	for _, want := range []string{"__MCP_BG_HDR__", "kill -0", "tail -c +11", "head -c 1024"} {
		if !strings.Contains(script, want) {
			t.Fatalf("read script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildBGStopScript(t *testing.T) {
	script := buildBGStopScript("/tmp/x.pid", "/tmp/x.log")
	for _, want := range []string{"kill -TERM", "kill -KILL", "sleep 2", "rm -f"} {
		if !strings.Contains(script, want) {
			t.Fatalf("stop script missing %q:\n%s", want, script)
		}
	}
}

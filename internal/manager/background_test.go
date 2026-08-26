package manager

import (
	"regexp"
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestParseBGReadOutput(t *testing.T) {
	running, total, chunk, exit := parseBGReadOutput("__MCP_BG_HDR__running=1 size=42 exit=\nhello")
	if !running || total != 42 || chunk != "hello" {
		t.Fatalf("parseBGReadOutput: running=%v total=%d chunk=%q", running, total, chunk)
	}
	if exit != nil {
		t.Fatalf("parseBGReadOutput: exit must be nil while running, got %v", *exit)
	}
}

func TestParseBGReadOutputLegacyHeader(t *testing.T) {
	// Old read scripts had no exit= field; parsing must stay compatible.
	running, total, chunk, exit := parseBGReadOutput("__MCP_BG_HDR__running=0 size=7\nDONE\n")
	if running || total != 7 || chunk != "DONE\n" {
		t.Fatalf("parseBGReadOutput legacy: running=%v total=%d chunk=%q", running, total, chunk)
	}
	if exit != nil {
		t.Fatalf("parseBGReadOutput legacy: exit must be nil, got %v", *exit)
	}
}

func TestParseBGReadOutputExitCode(t *testing.T) {
	running, total, chunk, exit := parseBGReadOutput("__MCP_BG_HDR__running=0 size=5 exit=3\nDONE\n")
	if running || total != 5 || chunk != "DONE\n" {
		t.Fatalf("parseBGReadOutput: running=%v total=%d chunk=%q", running, total, chunk)
	}
	if exit == nil || *exit != 3 {
		t.Fatalf("parseBGReadOutput: exit = %v, want 3", exit)
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
	script := buildBGReadScript("/tmp/x.log", "/tmp/x.pid", "/tmp/x.exit", 10, 1024)
	for _, want := range []string{"__MCP_BG_HDR__", "kill -0", "tail -c +11", "head -c 1024", "EXITF='/tmp/x.exit'", "exit=%s"} {
		if !strings.Contains(script, want) {
			t.Fatalf("read script missing %q:\n%s", want, script)
		}
	}
}

func TestBuildBGStopScript(t *testing.T) {
	script := buildBGStopScript("/tmp/x.pid", "/tmp/x.log")
	for _, want := range []string{"kill -TERM", "kill -KILL", "sleep 2", "rm -f", bgStopFailedMarker} {
		if !strings.Contains(script, want) {
			t.Fatalf("stop script missing %q:\n%s", want, script)
		}
	}
}

func TestBGSessionPaths(t *testing.T) {
	logPath, pidPath, exitPath := bgPaths("my-session")
	re := regexp.MustCompile(`^/tmp/\.2native-ssh-mcp-my-session-([A-Za-z0-9_]+)\.(log|pid|exit)$`)
	m := re.FindStringSubmatch(logPath)
	if m == nil {
		t.Fatalf("log path does not match expected pattern: %s", logPath)
	}
	id := m[1]
	if id == "" {
		t.Fatal("bg path id must be non-empty")
	}
	for _, p := range []string{pidPath, exitPath} {
		m := re.FindStringSubmatch(p)
		if m == nil {
			t.Fatalf("path does not match expected pattern: %s", p)
		}
		if m[1] != id {
			t.Fatalf("paths must share the same id: log=%s pid=%s exit=%s", logPath, pidPath, exitPath)
		}
	}
}

// TestCloseSessionOrphanKeepsSession verifies that a close which cannot
// confirm the remote job stopped keeps the session in the map, marked
// orphaned, with background state intact.
func TestCloseSessionOrphanKeepsSession(t *testing.T) {
	cfg := &config.SSHConfig{
		Name:                "unreachable",
		Host:                "127.0.0.1",
		Port:                1, // nothing listens here: connection refused
		Username:            "testuser",
		ConnectionTimeoutMs: 500,
	}
	m, err := New(map[string]*config.SSHConfig{"unreachable": cfg}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.DisconnectAll()

	logPath, pidPath, exitPath := bgPaths("orphan-test")
	ns := &namedSession{
		name:          "orphan-test",
		connectionKey: "unreachable",
		background:    true,
		bgRunning:     true,
		bgLogPath:     logPath,
		bgPIDPath:     pidPath,
		bgExitPath:    exitPath,
	}
	m.mu.Lock()
	m.sessions["orphan-test"] = ns
	m.mu.Unlock()

	if err := m.CloseSession("orphan-test"); err == nil {
		t.Fatal("expected error when the stop cannot be confirmed")
	}

	m.mu.Lock()
	_, stillThere := m.sessions["orphan-test"]
	bg := ns.background
	orphaned := ns.orphaned
	m.mu.Unlock()
	if !stillThere {
		t.Fatal("session must stay in the map after an unconfirmed stop")
	}
	if !bg {
		t.Fatal("background must stay true after an unconfirmed stop")
	}
	if !orphaned {
		t.Fatal("session must be marked orphaned after an unconfirmed stop")
	}
}

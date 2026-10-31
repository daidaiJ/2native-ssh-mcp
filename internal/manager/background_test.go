package manager

import (
	"context"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

// bgTestSpec builds a starter spec over throwaway remote paths.
func bgTestSpec(body string, pty bool, logMaxBytes int) bgJobSpec {
	return bgJobSpec{
		logPath:     "/tmp/x.log",
		pidPath:     "/tmp/x.pid",
		exitPath:    "/tmp/x.exit",
		headPath:    "/tmp/x.head",
		dropPath:    "/tmp/x.drop",
		body:        body,
		pty:         pty,
		logMaxBytes: logMaxBytes,
	}
}

func TestParseBGReadOutput(t *testing.T) {
	read := parseBGReadOutput("__MCP_BG_HDR__running=1 size=42 exit= dropped=0 headlen=0\nhello")
	if !read.running || read.size != 42 || read.chunk != "hello" {
		t.Fatalf("parseBGReadOutput: %+v", read)
	}
	if read.exitCode != nil {
		t.Fatalf("parseBGReadOutput: exit must be nil while running, got %v", *read.exitCode)
	}
	if read.dropped != 0 || read.head != "" {
		t.Fatalf("untrimmed log must report no dropped bytes and no head: %+v", read)
	}
}

func TestParseBGReadOutputLegacyHeader(t *testing.T) {
	// Old read scripts had no exit=/dropped=/headlen= fields; parsing must
	// stay compatible.
	read := parseBGReadOutput("__MCP_BG_HDR__running=0 size=7\nDONE\n")
	if read.running || read.size != 7 || read.chunk != "DONE\n" {
		t.Fatalf("parseBGReadOutput legacy: %+v", read)
	}
	if read.exitCode != nil || read.dropped != 0 || read.head != "" {
		t.Fatalf("parseBGReadOutput legacy: %+v", read)
	}
}

func TestParseBGReadOutputExitCode(t *testing.T) {
	read := parseBGReadOutput("__MCP_BG_HDR__running=0 size=5 exit=3 dropped=0 headlen=0\nDONE\n")
	if read.running || read.size != 5 || read.chunk != "DONE\n" {
		t.Fatalf("parseBGReadOutput: %+v", read)
	}
	if read.exitCode == nil || *read.exitCode != 3 {
		t.Fatalf("parseBGReadOutput: exit = %v, want 3", read.exitCode)
	}
}

// A trimmed log answers with the kept beginning, the dropped-byte count and
// the tail; the head is split off by its byte length, not by a marker, so it
// survives output that itself contains the marker text.
func TestParseBGReadOutputHead(t *testing.T) {
	payload := "HEAD-LINE\n" + "__MCP_BG_HDR__ not a marker here\n" + "tail-bytes"
	head := "HEAD-LINE\n"
	out := "__MCP_BG_HDR__running=1 size=10 exit= dropped=4096 headlen=" +
		strconv.Itoa(len(head)) + "\n" + head + payload[len(head):]
	read := parseBGReadOutput(out)
	if read.dropped != 4096 {
		t.Fatalf("dropped = %d, want 4096", read.dropped)
	}
	if read.head != head {
		t.Fatalf("head = %q, want %q", read.head, head)
	}
	if want := payload[len(head):]; read.chunk != want {
		t.Fatalf("chunk = %q, want %q", read.chunk, want)
	}
}

// The head length is a byte count: a boundary that lands inside a multi-byte
// rune must move back to the rune's start instead of splitting it.
func TestParseBGReadOutputHeadRuneBoundary(t *testing.T) {
	head := "中" // three bytes
	out := "__MCP_BG_HDR__running=1 size=0 exit= dropped=7 headlen=1\n" + head + "x"
	read := parseBGReadOutput(out)
	if read.head != "" {
		t.Fatalf("a mid-rune boundary must move to the rune start, got head %q", read.head)
	}
	if read.chunk != head+"x" {
		t.Fatalf("chunk = %q, want the whole rune plus the tail", read.chunk)
	}
}

// A head that covers the whole payload is the common case once the log is
// trimmed to the cap, and the boundary must not index past the end.
func TestParseBGReadOutputHeadCoversPayload(t *testing.T) {
	body := strings.Repeat("x", 40)
	out := "__MCP_BG_HDR__running=0 size=40 exit=0 dropped=100 headlen=40\n" + body
	read := parseBGReadOutput(out)
	if read.head != body || read.chunk != "" {
		t.Fatalf("a head covering the payload must leave an empty chunk, got head=%q chunk=%q", read.head, read.chunk)
	}
	past := parseBGReadOutput("__MCP_BG_HDR__running=0 size=3 exit= dropped=9 headlen=99\nabc")
	if past.head != "abc" || past.chunk != "" {
		t.Fatalf("a head length past the payload must clamp: %+v", past)
	}
}

// When maxBytes cuts the read short, the header carries how much output is
// still waiting; the caller must be able to tell a prefix from the whole log.
func TestParseBGReadOutputRemaining(t *testing.T) {
	read := parseBGReadOutput("__MCP_BG_HDR__running=1 size=9000 exit= dropped=0 headlen=0 remain=6900\nchunk")
	if read.remaining != 6900 || read.size != 9000 || read.chunk != "chunk" {
		t.Fatalf("parseBGReadOutput: %+v", read)
	}
	// Headers written before remain= existed must parse as complete reads.
	legacy := parseBGReadOutput("__MCP_BG_HDR__running=0 size=5 exit=0 dropped=0 headlen=0\nDONE\n")
	if legacy.remaining != 0 {
		t.Fatalf("legacy header must report no remaining bytes, got %d", legacy.remaining)
	}
}

func TestBuildBGReadScriptReportsRemaining(t *testing.T) {
	script := buildBGReadScript("/tmp/x.log", "/tmp/x.pid", "/tmp/x.exit", "/tmp/x.head", "/tmp/x.drop", 0, 1024)
	for _, want := range []string{"AVAIL=", "REMAIN=", "remain=%s", "-gt 1024", "AVAIL - 1024"} {
		if !strings.Contains(script, want) {
			t.Fatalf("read script missing %q:\n%s", want, script)
		}
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
	script := buildBGStarterScript(bgTestSpec("sleep 60; echo DONE", false, 1024))
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

// A job that finishes within the starter's one-second liveness window must
// count as started: the exit file it writes is proof it ran, and reporting a
// spurious start failure for a fast job is worse than a slow success.
func TestBuildBGStarterScriptFastJobCountsAsStarted(t *testing.T) {
	script := buildBGStarterScript(bgTestSpec("echo hi", false, 1024))
	exitCheck := `if [ -f "$EXITF" ]; then`
	if !strings.Contains(script, exitCheck) {
		t.Fatalf("starter must treat a written exit file as a successful start:\n%s", script)
	}
	if strings.Index(script, exitCheck) > strings.Index(script, `kill -0 "$PID"`) {
		t.Fatalf("the exit-file check must come before the liveness check:\n%s", script)
	}
}

// The starter must detach the exec channel's stdout, otherwise the channel
// never reaches EOF and the start call blocks until its timeout.
func TestBuildBGStarterScriptDetachesChannelStdout(t *testing.T) {
	script := buildBGStarterScript(bgTestSpec("echo hi", false, 1024))
	if !strings.Contains(script, "exec </dev/null >/dev/null 2>&1") {
		t.Fatalf("starter must detach stdin and stdout/stderr:\n%s", script)
	}
}

// The inner script is passed as a single-quoted argument to sh -c, so a quote
// of its own would end that string early: the shell then re-parses the
// remainder as unquoted words, silently eating backslashes (a printf '%s\n'
// inside once arrived as %sn). Assert the inner block is quote-free.
func TestBuildBGStarterScriptInnerBlockIsQuoteFree(t *testing.T) {
	script := buildBGStarterScript(bgTestSpec("echo it's", false, 1024))
	start := strings.Index(script, "setsid sh -c '")
	if start < 0 {
		t.Fatalf("starter must launch the inner shell with a quoted script:\n%s", script)
	}
	inner := script[start+len("setsid sh -c '"):]
	end := strings.Index(inner, "' _ ")
	if end < 0 {
		t.Fatalf("starter must pass the inner script's arguments after the quoted block:\n%s", script)
	}
	if strings.Contains(inner[:end], "'") {
		t.Fatalf("the inner script must not contain a single quote:\n%s", inner[:end])
	}
}

func TestBuildBGStarterScriptQuotesBody(t *testing.T) {
	// A body with a single quote must survive quoting.
	script := buildBGStarterScript(bgTestSpec("echo it's", false, 1024))
	if !strings.Contains(script, `'echo it'\''s'`) {
		t.Fatalf("starter must shell-quote the body:\n%s", script)
	}
}

// The log cap and the kept-beginning size travel in the script text, and the
// head never exceeds the cap it is meant to survive.
func TestBuildBGStarterScriptBoundedCapture(t *testing.T) {
	script := buildBGStarterScript(bgTestSpec("echo hi", false, 1<<20))
	for _, want := range []string{
		"capture()", "dd bs=65536", "head -c", `cat "$CHUNK" >> "$LOG"`,
		"DROPPED=$((DROPPED + SIZE - CAP))", `echo "$DROPPED" > "$DROPF"`,
		"1048576", "TAKE=$((65536 - HEADLEN))",
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("bounded-capture starter missing %q:\n%s", want, script)
		}
	}

	// A cap below the head size must not ask for a head larger than the log
	// that will exist.
	small := buildBGStarterScript(bgTestSpec("echo hi", false, 4096))
	if !strings.Contains(small, "TAKE=$((4096 - HEADLEN))") || strings.Contains(small, "TAKE=$((65536 - HEADLEN))") {
		t.Fatalf("head keep must clamp to the cap:\n%s", small)
	}

	// An uncapped log (negative) still runs through the capture loop, which
	// then never trims.
	uncapped := buildBGStarterScript(bgTestSpec("echo hi", false, -1))
	if !strings.Contains(uncapped, "-1") || !strings.Contains(uncapped, "capture()") {
		t.Fatalf("uncapped starter must keep the capture loop with cap -1:\n%s", uncapped)
	}
}

func TestBuildBGReadScript(t *testing.T) {
	script := buildBGReadScript("/tmp/x.log", "/tmp/x.pid", "/tmp/x.exit", "/tmp/x.head", "/tmp/x.drop", 10, 1024)
	for _, want := range []string{
		"__MCP_BG_HDR__", "kill -0", "REL=10", "FOFF=$((REL - DROP))", "tail -c +$((FOFF+1))", "head -c 1024",
		"EXITF='/tmp/x.exit'", "HEADF='/tmp/x.head'", "DROPF='/tmp/x.drop'",
		"dropped=%s", "headlen=%s", `cat "$HEADF"`,
	} {
		if !strings.Contains(script, want) {
			t.Fatalf("read script missing %q:\n%s", want, script)
		}
	}
	// The kept head is only sent when the requested offset fell into the
	// trimmed region.
	if !strings.Contains(script, `if [ 10 -lt "$DROP" ]`) {
		t.Fatalf("read script must compare the absolute offset with the dropped count:\n%s", script)
	}
}

func TestBuildBGStopScript(t *testing.T) {
	script := buildBGStopScript("/tmp/x.pid", "/tmp/x.log", "/tmp/x.head", "/tmp/x.drop")
	for _, want := range []string{"kill -TERM", "kill -KILL", "sleep 2", "rm -f", bgStopFailedMarker, `"$HEADF"`, `"$DROPF"`} {
		if !strings.Contains(script, want) {
			t.Fatalf("stop script missing %q:\n%s", want, script)
		}
	}
}

func TestBGSessionPaths(t *testing.T) {
	logPath, pidPath, exitPath, headPath, dropPath := bgPaths("my-session")
	re := regexp.MustCompile(`^/tmp/\.2native-ssh-mcp-my-session-([A-Za-z0-9_]+)\.(log|pid|exit|head|drop)$`)
	m := re.FindStringSubmatch(logPath)
	if m == nil {
		t.Fatalf("log path does not match expected pattern: %s", logPath)
	}
	id := m[1]
	if id == "" {
		t.Fatal("bg path id must be non-empty")
	}
	for _, p := range []string{pidPath, exitPath, headPath, dropPath} {
		m := re.FindStringSubmatch(p)
		if m == nil {
			t.Fatalf("path does not match expected pattern: %s", p)
		}
		if m[1] != id {
			t.Fatalf("paths must share the same id: log=%s pid=%s exit=%s head=%s drop=%s",
				logPath, pidPath, exitPath, headPath, dropPath)
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

	logPath, pidPath, exitPath, headPath, dropPath := bgPaths("orphan-test")
	ns := &namedSession{
		name:          "orphan-test",
		connectionKey: "unreachable",
		background:    true,
		bgRunning:     true,
		bgLogPath:     logPath,
		bgPIDPath:     pidPath,
		bgExitPath:    exitPath,
		bgHeadPath:    headPath,
		bgDropPath:    dropPath,
	}
	m.mu.Lock()
	m.sessions["orphan-test"] = ns
	m.mu.Unlock()

	if err := m.CloseSession(context.Background(), "orphan-test"); err == nil {
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

func TestBGStarterScriptUnsetsBuffering(t *testing.T) {
	script := buildBGStarterScript(bgTestSpec("echo hi", false, 1024))
	if !strings.Contains(script, "export PYTHONUNBUFFERED=1") {
		t.Fatal("background jobs must disable Python block buffering so logs stream")
	}
}

func TestBGStarterScriptPTYFlag(t *testing.T) {
	plain := buildBGStarterScript(bgTestSpec("echo hi", false, 1024))
	// The script text carries both branches; the last argument selects one.
	if !strings.Contains(plain, " 0 &") {
		t.Fatalf("pty=false must pass flag 0: %s", plain)
	}
	pty := buildBGStarterScript(bgTestSpec("echo hi", true, 1024))
	for _, want := range []string{`script -qefc "$BODY" /dev/null`, `"$USE_PTY" = "1"`, "PYTHONUNBUFFERED=1", " 1 &"} {
		if !strings.Contains(pty, want) {
			t.Fatalf("pty=true starter missing %q:\n%s", want, pty)
		}
	}
	// The pty flag must degrade gracefully when script(1) is absent.
	if !strings.Contains(pty, `command -v script`) || !strings.Contains(pty, `eval "$BODY"`) {
		t.Fatal("pty=true starter must fall back to eval when script is missing")
	}
}

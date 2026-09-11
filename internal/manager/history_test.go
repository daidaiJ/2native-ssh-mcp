package manager

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestSanitizeInvalidUTF8(t *testing.T) {
	// Valid UTF-8 (including Chinese) passes through untouched, no flag.
	out, changed := sanitizeInvalidUTF8("ok: 系统 ready\n")
	if out != "ok: 系统 ready\n" || changed {
		t.Fatalf("valid UTF-8 must pass through unchanged, got %q changed=%v", out, changed)
	}

	// GBK-encoded 系统 (cf extended-B? no: GBK bytes D5 6C? use real GBK bytes)
	// Latin-1 byte 0xE9 and a GBK pair become explicit \xNN escapes.
	out, changed = sanitizeInvalidUTF8("caf\xe9 \xd6\xd0\xce\xc4\n")
	want := "caf\\xe9 \\xd6\\xd0\\xce\\xc4\n"
	if out != want || !changed {
		t.Fatalf("invalid sequences must become \\xNN escapes, got %q changed=%v", out, changed)
	}

	// A legitimately encoded U+FFFD already in the output must survive.
	out, changed = sanitizeInvalidUTF8("\uFFFD")
	if out != "\uFFFD" || changed {
		t.Fatalf("valid U+FFFD must survive, got %q changed=%v", out, changed)
	}
}

func TestBuildCommandResultSanitizeToggle(t *testing.T) {
	dirty := "bad \xff byte"
	on := buildCommandResult(dirty, "", 0, StatusOK, &config.SSHConfig{})
	if !on.NonUTF8 || !strings.Contains(on.Stdout, `\xff`) {
		t.Fatalf("default sanitize must escape invalid bytes, got %+v", on)
	}
	off := false
	cfg := &config.SSHConfig{Utf8Sanitize: &off}
	res := buildCommandResult(dirty, "", 0, StatusOK, cfg)
	if res.NonUTF8 || !strings.Contains(res.Stdout, "\xff") {
		t.Fatalf("utf8Sanitize=false must keep raw bytes and no flag, got %+v", res)
	}
}

func TestHistoryCmdPattern(t *testing.T) {
	yes := []string{"history", "  history  ", "history 50", "\thistory\t100\t"}
	no := []string{"history -c", "history | grep x", "cat history", "history; rm -rf /",
		"history 0", "history -50", "FC -l", "echo history"}
	for _, c := range yes {
		if !historyCmdPattern.MatchString(c) {
			t.Fatalf("%q must match as a history listing", c)
		}
	}
	for _, c := range no {
		if historyCmdPattern.MatchString(c) {
			t.Fatalf("%q must not match as a history listing", c)
		}
	}
}

func TestParseHistoryEntries(t *testing.T) {
	out := "  541  systemctl status nginx\n  542  vim app.conf\n543  history\n\n# comment\n"
	got := parseHistoryEntries(out)
	want := []string{"systemctl status nginx", "vim app.conf", "history"}
	if len(got) != len(want) {
		t.Fatalf("parsed %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("parsed %v, want %v", got, want)
		}
	}
}

func TestTailDedupe(t *testing.T) {
	remote := []string{"ls -la", "systemctl restart nginx"}
	log := []CommandLogEntry{
		{Command: "cd /opt"},         // kept
		{Command: " ls -la "},        // already visible remotely
		{Command: "history"},         // the current command
		{Command: "tail -f app.log"}, // superseded below (tail wins)
		{Command: "cd /opt"},         // duplicate, most recent wins
		{Command: "tail -f app.log"}, // kept, latest occurrence
		{Command: ""},                // empty, dropped
	}
	got := tailDedupe(remote, log, "history")
	want := []string{"cd /opt", "tail -f app.log"}
	if len(got) != len(want) {
		t.Fatalf("tailDedupe = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tailDedupe = %v, want %v", got, want)
		}
	}
}

func TestEnrichHistoryOutput(t *testing.T) {
	cfg := &config.SSHConfig{}
	on := true
	cfg.HistoryFromLog = &on
	m, err := New(map[string]*config.SSHConfig{"srv": cfg}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m.commandLogs["srv"], err = NewCommandLog(t.TempDir(), "srv", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	m.commandLogs["srv"].items = []CommandLogEntry{
		{Command: "echo one"}, {Command: "echo two"}, {Command: "echo one"},
	}

	// Sparse remote listing (2 entries < 10) gets the supplement appended.
	res := CommandResult{Status: StatusOK, Stdout: "  1  whoami\n  2  ls\n"}
	m.enrichHistoryOutput("srv", "history", &res)
	if res.HistorySupplemented != 2 { // echo one dedupes to its last occurrence
		t.Fatalf("expected 2 supplemented entries, got %d: %q", res.HistorySupplemented, res.Stdout)
	}
	if !strings.Contains(res.Stdout, "tail-deduped") ||
		!strings.Contains(res.Stdout, "  -  echo one\n") ||
		!strings.Contains(res.Stdout, "  -  echo two\n") {
		t.Fatalf("supplement block missing or wrong: %q", res.Stdout)
	}

	// A rich remote listing stays untouched.
	res = CommandResult{Status: StatusOK, Stdout: strings.Repeat("  1  ls\n", 15)}
	m.enrichHistoryOutput("srv", "history", &res)
	if res.HistorySupplemented != 0 {
		t.Fatalf("rich history must not be supplemented: %q", res.Stdout)
	}

	// A non-history command stays untouched even when output is sparse.
	res = CommandResult{Status: StatusOK, Stdout: "hello\n"}
	m.enrichHistoryOutput("srv", "echo hello", &res)
	if res.HistorySupplemented != 0 || res.Stdout != "hello\n" {
		t.Fatalf("non-history command must not be enriched: %+v", res)
	}

	// Disabled by default: the on/off gate lives in finalizeCommand.
	offCfg := &config.SSHConfig{}
	m2, err := New(map[string]*config.SSHConfig{"srv": offCfg}, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m2.commandLogs["srv"], err = NewCommandLog(t.TempDir(), "srv", 20, false)
	if err != nil {
		t.Fatal(err)
	}
	m2.commandLogs["srv"].items = []CommandLogEntry{{Command: "echo one"}}
	res = CommandResult{Status: StatusOK, Stdout: "  1  whoami\n"}
	res, _ = m2.finalizeCommand("srv", offCfg, "history", res, nil)
	if res.HistorySupplemented != 0 || res.Stdout != "  1  whoami\n" {
		t.Fatalf("historyFromLog must default to off: %+v", res)
	}
}

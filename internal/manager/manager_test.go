package manager

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCommandLogBoundedAndPersistent(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "cmd", ExitCode: 0, Success: true})
	}

	// Reload from disk: only the last 3 entries survive.
	reloaded, err := NewCommandLog(dir, "dev", 3, false)
	if err != nil {
		t.Fatal(err)
	}
	reloaded.mu.Lock()
	count := len(reloaded.items)
	reloaded.mu.Unlock()
	if count != 3 {
		t.Fatalf("expected 3 entries after reload, got %d", count)
	}
}

func TestCommandLogOnlySuccess(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, true)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(CommandLogEntry{Command: "ok", Success: true})
	log.Add(CommandLogEntry{Command: "fail", Success: false})
	log.Add(CommandLogEntry{Command: "ok2", Success: true})

	log.mu.Lock()
	defer log.mu.Unlock()
	if len(log.items) != 2 {
		t.Fatalf("expected 2 entries (failed skipped), got %d", len(log.items))
	}
	if log.items[0].Command != "ok" || log.items[1].Command != "ok2" {
		t.Fatalf("unexpected entries: %+v", log.items)
	}
}

func TestCommandLogFileFormat(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false)
	if err != nil {
		t.Fatal(err)
	}
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "ls -la", ExitCode: 0, Success: true})

	data, err := os.ReadFile(filepath.Join(dir, "dev.log"))
	if err != nil {
		t.Fatal(err)
	}
	var entry CommandLogEntry
	if err := json.Unmarshal(bytes.TrimSpace(data), &entry); err != nil {
		t.Fatalf("log file must contain JSON lines: %v", err)
	}
	if entry.Command != "ls -la" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

// writerAtBuffer implements io.WriterAt for copy tests.
type writerAtBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (w *writerAtBuffer) WriteAt(p []byte, off int64) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	end := int(off) + len(p)
	if end > w.buf.Len() {
		w.buf.Write(make([]byte, end-w.buf.Len()))
	}
	copy(w.buf.Bytes()[int(off):], p)
	return len(p), nil
}

func TestCopySequential(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	var dst writerAtBuffer
	done, err := copySequential(src, &dst, 0, 11, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != 11 || dst.buf.String() != "hello world" {
		t.Fatalf("unexpected copy result: done=%d dst=%q", done, dst.buf.String())
	}
}

func TestCopySequentialFromOffset(t *testing.T) {
	src := bytes.NewReader([]byte("hello world"))
	var dst writerAtBuffer
	done, err := copySequential(src, &dst, 6, -1, 4, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != 5 || dst.buf.String() != "\x00\x00\x00\x00\x00\x00world" {
		t.Fatalf("unexpected copy result: done=%d dst=%q", done, dst.buf.String())
	}
}

func TestCopyConcurrent(t *testing.T) {
	payload := bytes.Repeat([]byte("0123456789abcdef"), 4096) // 64KB
	src := bytes.NewReader(payload)
	var dst writerAtBuffer
	done, err := copyConcurrent(src, &dst, 0, int64(len(payload)), 8, 4096, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != int64(len(payload)) {
		t.Fatalf("expected %d bytes, got %d", len(payload), done)
	}
	if !bytes.Equal(dst.buf.Bytes(), payload) {
		t.Fatal("concurrent copy produced different content")
	}
}

func TestCopyConcurrentFromOffset(t *testing.T) {
	payload := bytes.Repeat([]byte("abcdefgh"), 2048) // 16KB
	src := bytes.NewReader(payload)
	var dst writerAtBuffer
	start := int64(4096)
	done, err := copyConcurrent(src, &dst, start, int64(len(payload)), 4, 1024, nil)
	if err != nil {
		t.Fatal(err)
	}
	if done != int64(len(payload))-start {
		t.Fatalf("expected %d bytes, got %d", int64(len(payload))-start, done)
	}
	if !bytes.Equal(dst.buf.Bytes()[start:], payload[start:]) {
		t.Fatal("offset concurrent copy produced different content")
	}
}

func TestCopyConcurrentProgress(t *testing.T) {
	payload := bytes.Repeat([]byte("x"), 10000)
	src := bytes.NewReader(payload)
	var dst writerAtBuffer
	var maxObserved atomic.Int64
	progress := func(done, total int64) {
		if total != 10000 {
			t.Fatalf("expected total 10000, got %d", total)
		}
		for {
			cur := maxObserved.Load()
			if done <= cur || maxObserved.CompareAndSwap(cur, done) {
				return
			}
		}
	}
	done, err := copyConcurrent(src, &dst, 0, 10000, 4, 1000, progress)
	if err != nil {
		t.Fatal(err)
	}
	if done != 10000 || maxObserved.Load() != 10000 {
		t.Fatalf("expected final progress 10000, got done=%d max=%d", done, maxObserved.Load())
	}
}

func TestShellQuote(t *testing.T) {
	cases := map[string]string{
		"ls -la":        "'ls -la'",
		"it's":          `'it'\''s'`,
		"":              "''",
	}
	for input, want := range cases {
		if got := shellQuote(input); got != want {
			t.Fatalf("shellQuote(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestApplyCommandTemplate(t *testing.T) {
	got := applyCommandTemplate("sudo -n <quotedCommand>", "ls 'a'")
	want := "sudo -n 'ls '\\''a'\\'''"
	if got != want {
		t.Fatalf("applyCommandTemplate = %q, want %q", got, want)
	}
}

func TestBuildShellScript(t *testing.T) {
	script := buildShellScript("cmd_1", "echo hi", "", "")
	if !bytes.Contains([]byte(script), []byte("__MCP_BEGIN__cmd_1__")) {
		t.Fatalf("script missing begin marker: %s", script)
	}
	if !bytes.Contains([]byte(script), []byte("__MCP_END__cmd_1__RC__%s__")) {
		t.Fatalf("script missing end marker: %s", script)
	}
	if !bytes.Contains([]byte(script), []byte("__mcp_rc=$?")) {
		t.Fatalf("script missing exit code capture: %s", script)
	}
}

func TestCleanShellOutput(t *testing.T) {
	input := "\x1b[32mgreen\x1b[0m\r\n\x1b]0;title\x07next\r"
	want := "green\nnext\n"
	if got := cleanShellOutput(input); got != want {
		t.Fatalf("cleanShellOutput = %q, want %q", got, want)
	}
}

func TestParseStatusOutput(t *testing.T) {
	marker := "__MCP_FIELD_abc_"
	output := "\n__MCP_FIELD_abc_hostname\nmyhost\n__MCP_FIELD_abc_osName\nLinux\n"
	values := parseStatusOutput(output, marker)
	if values["hostname"] != "myhost" || values["osName"] != "Linux" {
		t.Fatalf("unexpected values: %+v", values)
	}
}
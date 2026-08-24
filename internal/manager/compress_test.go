package manager

import (
	"strconv"
	"strings"
	"testing"
)

func TestCompressCommandOutputHeadTail(t *testing.T) {
	opts := DefaultCompressOptions()
	opts.Threshold = 500
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "line-"+strconv.Itoa(i)+"-padding-to-make-output-longer")
	}
	in := strings.Join(lines, "\n")
	out := CompressCommandOutput(in, opts)
	if len(out) >= len(in) {
		t.Fatalf("expected compression to shrink output: in=%d out=%d", len(in), len(out))
	}
	if !strings.Contains(out, "lines omitted") {
		t.Fatalf("missing omission marker:\n%s", out)
	}
}

func TestCompressSkipsSmallOutput(t *testing.T) {
	in := "hello\nworld\n"
	out := CompressCommandOutput(in, DefaultCompressOptions())
	if out != in {
		t.Fatalf("small output should not compress: %q", out)
	}
}

func TestCollapseDuplicateLines(t *testing.T) {
	lines := []string{"ERR timeout", "ERR timeout", "ERR timeout", "OK"}
	got := collapseConsecutiveDuplicateLines(lines)
	if len(got) != 2 {
		t.Fatalf("expected 2 lines, got %d: %v", len(got), got)
	}
	if !strings.Contains(got[0], "repeated 3") {
		t.Fatalf("expected repeat marker, got %q", got[0])
	}
}

func TestFinalizeCommandOutputRedactsAndCompresses(t *testing.T) {
	opts := DefaultCompressOptions()
	opts.Threshold = 500
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = "log line with enough padding to exceed threshold when joined"
	}
	lines = append(lines, "password=secret123")
	in := strings.Join(lines, "\n")
	out := FinalizeCommandOutput(in, opts)
	if strings.Contains(out, "secret123") {
		t.Fatal("password should be redacted")
	}
	if !strings.Contains(out, "lines omitted") {
		t.Fatal("expected compression")
	}
}

func TestCompressDisabled(t *testing.T) {
	opts := DefaultCompressOptions()
	opts.Enabled = false
	var lines []string
	for i := 0; i < 200; i++ {
		lines = append(lines, "x")
	}
	in := strings.Join(lines, "\n")
	if CompressCommandOutput(in, opts) != in {
		t.Fatal("disabled compression should passthrough")
	}
}

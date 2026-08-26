package manager

import (
	"strconv"
	"strings"
	"sync/atomic"
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

func TestFinalizeCommandOutputCompresses(t *testing.T) {
	opts := DefaultCompressOptions()
	opts.Threshold = 500
	lines := make([]string, 150)
	for i := range lines {
		lines[i] = "log line with enough padding to exceed threshold when joined"
	}
	in := strings.Join(lines, "\n")
	out := FinalizeCommandOutput(in, opts)
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

// TestLimitedBufferSharedBudget verifies stdout+stderr share one byte budget:
// stdout filling max-1 leaves room for only 1 more byte on stderr, and the
// next write trips the overflow flag.
func TestLimitedBufferSharedBudget(t *testing.T) {
	const max = 10
	var budget atomic.Int32
	budget.Store(max)
	exceeded := false
	stdout := &limitedBuffer{max: max, shared: &budget, onExceed: func() { exceeded = true }}
	stderr := &limitedBuffer{max: max, shared: &budget, onExceed: func() { exceeded = true }}

	if _, err := stdout.Write([]byte("123456789")); err != nil { // max-1
		t.Fatal(err)
	}
	if exceeded {
		t.Fatal("must not trip before the shared budget is exhausted")
	}
	if _, err := stderr.Write([]byte("ab")); err != nil { // 2 bytes, only 1 left
		t.Fatal(err)
	}
	if !exceeded {
		t.Fatal("expected overflow when combined output exceeds max")
	}
	if got := stdout.String() + stderr.String(); got != "123456789a" {
		t.Fatalf("combined capture = %q, want %q", got, "123456789a")
	}
}

// TestLimitedBufferSharedBudget verifies stdout+stderr share one byte budget:
// stdout filling max-1 leaves room for only 1 more byte on stderr, and the
// next write trips the overflow flag.

package manager

import (
	"sync"
	"unicode/utf8"
	"sync/atomic"
	"testing"
)

// TestLimitedBufferSharedBudgetConcurrent writes to both streams at once,
// mirroring how x/crypto/ssh copies stdout and stderr from separate
// goroutines. The shared budget must be race-free (caught by -race) and the
// combined output must stay within the shared budget plus at most one write
// chunk per stream (the atomic soft bound).
func TestLimitedBufferSharedBudgetConcurrent(t *testing.T) {
	const max = 4096
	var budget atomic.Int32
	budget.Store(max)
	exceeded := make(chan struct{}, 2)
	notify := func() {
		select {
		case exceeded <- struct{}{}:
		default:
		}
	}
	stdout := &limitedBuffer{max: max, shared: &budget, onExceed: notify}
	stderr := &limitedBuffer{max: max, shared: &budget, onExceed: notify}

	chunk := make([]byte, 512)
	var wg sync.WaitGroup
	for _, b := range []*limitedBuffer{stdout, stderr} {
		wg.Add(1)
		go func(b *limitedBuffer) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				if _, err := b.Write(chunk); err != nil {
					t.Errorf("write: %v", err)
					return
				}
			}
		}(b)
	}
	wg.Wait()

	combined := stdout.buf.Len() + stderr.buf.Len()
	if combined > max+4*len(chunk) {
		t.Fatalf("combined output %d exceeds shared budget %d (+4 chunks)", combined, max)
	}
}
// TestLimitedBufferUTF8SafeCut verifies that a truncation at the byte cap
// backs off to a rune boundary instead of splitting a multi-byte UTF-8
// sequence (issue #3 Slice C).
func TestLimitedBufferUTF8SafeCut(t *testing.T) {
	s := "abc字字字" // 3 + 3*3 = 12 bytes
	var budget atomic.Int32
	budget.Store(10)
	buf := &limitedBuffer{max: 10, shared: &budget}
	buf.Write([]byte(s))
	if buf.buf.Len() != 9 {
		t.Fatalf("cut must back off to the rune boundary (9 bytes), got %d", buf.buf.Len())
	}
	if !utf8.ValidString(buf.String()) {
		t.Fatalf("buffered output must stay valid UTF-8: %q", buf.String())
	}
	if !buf.exceeded {
		t.Fatal("buffer must be marked exceeded")
	}

	// Standalone buffer, cap landing exactly on a rune start.
	buf2 := &limitedBuffer{max: 6}
	buf2.Write([]byte(s))
	if buf2.buf.Len() != 6 || !utf8.ValidString(buf2.String()) {
		t.Fatalf("cap on a rune start must not split: len=%d %q", buf2.buf.Len(), buf2.String())
	}
}

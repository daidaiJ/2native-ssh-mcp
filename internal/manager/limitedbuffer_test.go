package manager

import (
	"sync"
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
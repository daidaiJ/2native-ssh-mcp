package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunDirectoryTransferAggregates(t *testing.T) {
	tasks := []transferTask{
		{rel: "a", size: 10},
		{rel: "b", size: 20},
		{rel: "c", size: 30},
		{rel: "d", size: 40},
	}
	// "b" resumes, "c" is skipped, "d" fails: the aggregate must count each
	// category once and still process the healthy files.
	var progressTotal, progressGrandTotal int64
	out := runDirectoryTransfer(context.Background(), tasks, 2, 100, func(done, total int64) {
		progressTotal, progressGrandTotal = done, total
	}, func(_ context.Context, task transferTask, progress ProgressFunc) (directoryTransferOutcome, error) {
		switch task.rel {
		case "a":
			progress(task.size, task.size)
			return directoryTransferOutcome{bytes: task.size}, nil
		case "b":
			// A resumed file reports progress from its resume offset; the
			// helper must still count the whole file as done.
			progress(task.size/2, task.size)
			progress(task.size, task.size)
			return directoryTransferOutcome{bytes: task.size / 2, resumed: true}, nil
		case "c":
			return directoryTransferOutcome{skipped: true}, nil
		default:
			return directoryTransferOutcome{}, errors.New("boom")
		}
	})

	if out.files != 3 || out.skipped != 1 || out.resumed != 1 {
		t.Fatalf("aggregate = %+v", out)
	}
	if out.transferred != 10+10 {
		t.Fatalf("transferred = %d, want 20", out.transferred)
	}
	if len(out.failed) != 1 || out.failed[0] != "d: boom" {
		t.Fatalf("failed = %v, want [d: boom]", out.failed)
	}
	// The failed file contributes no completed bytes, matching the
	// sequential behavior; a, b and the skipped c add up to 60.
	if out.done != 60 || progressTotal != 60 || progressGrandTotal != 100 {
		t.Fatalf("done = %d, progress = %d/%d, want 60/100", out.done, progressTotal, progressGrandTotal)
	}
}

func TestRunDirectoryTransferBoundsWorkers(t *testing.T) {
	const tasks = 8
	list := make([]transferTask, tasks)
	for i := range list {
		list[i] = transferTask{rel: fmt.Sprint(i), size: 1}
	}
	var active, peak atomic.Int32
	out := runDirectoryTransfer(context.Background(), list, 3, tasks, nil,
		func(context.Context, transferTask, ProgressFunc) (directoryTransferOutcome, error) {
			n := active.Add(1)
			for {
				p := peak.Load()
				if n <= p || peak.CompareAndSwap(p, n) {
					break
				}
			}
			active.Add(-1)
			return directoryTransferOutcome{bytes: 1}, nil
		})
	if out.files != tasks {
		t.Fatalf("files = %d, want %d", out.files, tasks)
	}
	if p := peak.Load(); p > 3 {
		t.Fatalf("workers exceeded the limit: peak %d", p)
	}
}

// countingReaderAt records how many ReadAt calls overlap, and slows each one
// down so the overlap is observable.
type countingReaderAt struct {
	r      io.ReaderAt
	active atomic.Int32
	peak   atomic.Int32
}

func (c *countingReaderAt) ReadAt(p []byte, off int64) (int, error) {
	n := c.active.Add(1)
	defer c.active.Add(-1)
	for {
		p := c.peak.Load()
		if n <= p || c.peak.CompareAndSwap(p, n) {
			break
		}
	}
	time.Sleep(2 * time.Millisecond)
	return c.r.ReadAt(p, off)
}

type memWriterAt struct {
	mu   sync.Mutex
	data []byte
}

func (m *memWriterAt) WriteAt(p []byte, off int64) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return copy(m.data[off:], p), nil
}

// A file with three chunks must overlap its round trips even though there
// are four workers: falling back to sequential below workers×chunk turns
// every small upload into one RTT per 32 KiB.
func TestCopyFilePipelinesSmallMultiChunkTransfer(t *testing.T) {
	const chunkSize = 1024
	data := bytes.Repeat([]byte("x"), 3*chunkSize)
	src := &countingReaderAt{r: bytes.NewReader(data)}
	dst := &memWriterAt{data: make([]byte, len(data))}

	n, err := copyFile(context.Background(), src, dst, 0, int64(len(data)), 4, chunkSize, nil)
	if err != nil {
		t.Fatal(err)
	}
	if n != int64(len(data)) {
		t.Fatalf("copied %d bytes, want %d", n, len(data))
	}
	if peak := src.peak.Load(); peak < 2 {
		t.Fatalf("small multi-chunk transfer stayed sequential (peak in-flight reads = %d)", peak)
	}
	if !bytes.Equal(dst.data, data) {
		t.Fatal("copied bytes do not match the source")
	}
}

func TestRunDirectoryTransferCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out := runDirectoryTransfer(ctx, []transferTask{{rel: "a", size: 1}, {rel: "b", size: 1}}, 2, 2, nil,
		func(context.Context, transferTask, ProgressFunc) (directoryTransferOutcome, error) {
			return directoryTransferOutcome{}, errors.New("must not run")
		})
	if out.files != 0 || len(out.failed) != 0 {
		t.Fatalf("a cancelled transfer must not record work: %+v", out)
	}
}

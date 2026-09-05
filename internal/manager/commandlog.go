package manager

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// CommandLogEntry is one recorded command execution (without output).
type CommandLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Command   string    `json:"command"`
	ExitCode  int       `json:"exitCode"`
	Success   bool      `json:"success"`
}

// CommandLog appends executed commands to a bounded log file, keeping only
// the most recent size entries. The file survives restarts.
type CommandLog struct {
	mu          sync.Mutex
	path        string
	size        int
	onlySuccess bool
	items       []CommandLogEntry
}

// NewCommandLog creates a log file at <dir>/<name>.log, loading any existing
// content (keeping the last size entries). When onlySuccess is set, failed
// commands are not recorded.
func NewCommandLog(dir, name string, size int, onlySuccess bool) (*CommandLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	cl := &CommandLog{path: filepath.Join(dir, name+".log"), size: size, onlySuccess: onlySuccess}
	cl.load()
	return cl, nil
}

// load reads the existing log file, keeping the last size entries.
func (cl *CommandLog) load() {
	f, err := os.Open(cl.path)
	if err != nil {
		return
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		var e CommandLogEntry
		if err := json.Unmarshal(scanner.Bytes(), &e); err != nil {
			continue
		}
		cl.items = append(cl.items, e)
	}
	if len(cl.items) > cl.size {
		cl.items = cl.items[len(cl.items)-cl.size:]
	}
}

// Add appends an entry and rewrites the file with the last size entries.
// Failed commands are skipped when onlySuccess is set.
func (cl *CommandLog) Add(e CommandLogEntry) {
	if cl.onlySuccess && !e.Success {
		return
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.items = append(cl.items, e)
	if len(cl.items) > cl.size {
		cl.items = cl.items[len(cl.items)-cl.size:]
	}
	cl.flush()
}

// Recent returns a copy of the last n entries (oldest first). Safe on a nil
// log (disabled) and concurrently with Add.
func (cl *CommandLog) Recent(n int) []CommandLogEntry {
	if cl == nil {
		return nil
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if n <= 0 || len(cl.items) == 0 {
		return nil
	}
	if n > len(cl.items) {
		n = len(cl.items)
	}
	out := make([]CommandLogEntry, n)
	copy(out, cl.items[len(cl.items)-n:])
	return out
}

// flush atomically rewrites the log file.
func (cl *CommandLog) flush() {
	tmp := cl.path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return
	}
	enc := json.NewEncoder(f)
	for _, e := range cl.items {
		_ = enc.Encode(e)
	}
	_ = f.Close()
	_ = os.Rename(tmp, cl.path)
}
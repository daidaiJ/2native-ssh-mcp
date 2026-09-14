package manager

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Audit format version and chain anchors.
const (
	// auditVersion is the v field of every audit record.
	auditVersion = 1
	// AuditTypeCommand is a regular command/transfer/session record.
	AuditTypeCommand = "command"
	// AuditTypeChainStart is the first line of a freshly rotated file; it
	// carries the previous file's tail hash so the chain never breaks.
	AuditTypeChainStart = "chain-start"
	// zeroHash anchors the first record of the very first file.
	zeroHash = "0000000000000000000000000000000000000000000000000000000000000000"
	// Approval outcomes recorded in the approval field.
	ApprovalNone     = "none"
	ApprovalAuto     = "auto"
	ApprovalAccepted = "accepted"
	ApprovalDeclined = "declined"
	ApprovalFailOpen = "fail-open"
	ApprovalTimeout  = "timeout"
)

// Rotation limits: whichever is hit first rotates the file. Vars so tests
// can shrink them; defaults per REQ-002.
var (
	auditMaxBytes = 16 * 1024 * 1024
	auditMaxLines = 10000
)

// Flush-batch limits for the buffered write path: whichever is hit first
// flushes the pending records, besides the configured time interval.
var (
	auditFlushRecords = 32
	auditFlushBytes   = 256 * 1024
)

// AuditRecord is one tamper-evident audit line on disk (JSONL). Hash is
// SHA-256 over the canonical JSON of the record without the hash field
// (field names sorted, no HTML escaping); PrevHash links to the previous
// line's hash, zeroHash starts a chain.
type AuditRecord struct {
	V          int    `json:"v"`
	Type       string `json:"type,omitempty"`
	TS         string `json:"ts"`
	Connection string `json:"connection"`
	Tool       string `json:"tool,omitempty"`
	Command    string `json:"command,omitempty"`
	Session    string `json:"session,omitempty"`
	Approval   string `json:"approval,omitempty"`
	ExitCode   int    `json:"exitCode,omitempty"`
	Success    bool   `json:"success"`
	ErrorCode  string `json:"errorCode,omitempty"`
	PrevHash   string `json:"prevHash,omitempty"`
	Hash       string `json:"hash,omitempty"`
}

// canonicalJSON renders the record with a fixed field set, sorted keys and
// no HTML escaping, excluding the hash field. Map keys are sorted by
// encoding/json and the int fields are small, so the encoding is stable
// across runs and platforms — pinned by commandlog_hash_test.go.
func (r AuditRecord) canonicalJSON() ([]byte, error) {
	r.Hash = ""
	m := map[string]any{
		"v":          r.V,
		"type":       r.Type,
		"ts":         r.TS,
		"connection": r.Connection,
		"tool":       r.Tool,
		"command":    r.Command,
		"session":    r.Session,
		"approval":   r.Approval,
		"exitCode":   r.ExitCode,
		"success":    r.Success,
		"errorCode":  r.ErrorCode,
		"prevHash":   r.PrevHash,
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

// seal computes and fills the record's hash from its current fields.
func (r *AuditRecord) seal() error {
	c, err := r.canonicalJSON()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(c)
	r.Hash = hex.EncodeToString(sum[:])
	return nil
}

// CommandLogEntry is one recorded command in the in-memory window (without
// output) surfaced by Recent() / list-servers / the history supplement.
type CommandLogEntry struct {
	Timestamp time.Time `json:"timestamp"`
	Command   string    `json:"command"`
	ExitCode  int       `json:"exitCode"`
	Success   bool      `json:"success"`
}

// CommandLog appends executed commands to a tamper-evident JSONL audit log
// (a SHA-256 hash chain) and keeps the most recent size entries in memory
// for Recent(). The file is append-only and rotates at auditMaxBytes or
// auditMaxLines; failed commands are always written to disk — onlySuccess
// filters the in-memory window only.
//
// Records are buffered and flushed in batches: when flushMs is 0 every
// record is written synchronously (write-through); otherwise a flush runs
// after flushMs, auditFlushRecords records or auditFlushBytes, whichever
// comes first. A crash can lose the unflushed tail (at most flushMs of
// records); the on-disk chain stays verifiable at all times because records
// are chained at flush time from the disk tail hash.
type CommandLog struct {
	mu          sync.Mutex
	dir         string
	name        string
	path        string
	size        int
	onlySuccess bool
	flushMs     int
	items       []CommandLogEntry
	lastHash    string
	fileBytes   int64
	fileLines   int64
	file        *os.File
	pending     []AuditRecord
	flushTimer  *time.Timer
}

// NewCommandLog opens (or creates) the audit log at <dir>/<name>.jsonl.
// flushMs is the flush policy: 0 = write-through, >0 = buffered with that
// maximum delay in milliseconds. A legacy <name>.log from the rewrite-based
// implementation is renamed to <name>.log.legacy once (with a stderr notice)
// instead of being spliced into the new chain.
func NewCommandLog(dir, name string, size int, onlySuccess bool, flushMs int) (*CommandLog, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	if flushMs < 0 {
		flushMs = 0
	}
	cl := &CommandLog{
		dir:         dir,
		name:        name,
		path:        filepath.Join(dir, name+".jsonl"),
		size:        size,
		onlySuccess: onlySuccess,
		flushMs:     flushMs,
	}
	if err := cl.archiveLegacy(); err != nil {
		return nil, err
	}
	cl.load()
	return cl, nil
}

// archiveLegacy moves a pre-chain <name>.log aside. A pre-existing
// .log.legacy is replaced (it is a stale copy of an even older .log).
func (cl *CommandLog) archiveLegacy() error {
	legacy := filepath.Join(cl.dir, cl.name+".log")
	if _, err := os.Stat(legacy); err != nil {
		return nil
	}
	target := legacy + ".legacy"
	_ = os.Remove(target)
	if err := os.Rename(legacy, target); err != nil {
		return fmt.Errorf("found legacy command log %s but could not rename it to %s: %w", legacy, target, err)
	}
	fmt.Fprintf(os.Stderr, "[2native-ssh-mcp] audit: legacy command log %s renamed to %s; a new hash-chained audit log starts at %s\n",
		legacy, target, cl.path)
	return nil
}

// load restores the in-memory window and chain state from the current file.
// Lines that do not parse as hash-chained records are skipped for chain
// purposes (audit-verify reports them); the file itself is never rewritten.
func (cl *CommandLog) load() {
	f, err := os.Open(cl.path)
	if err != nil {
		return
	}
	defer f.Close()
	info, err := f.Stat()
	if err == nil {
		cl.fileBytes = info.Size()
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
	for scanner.Scan() {
		cl.fileLines++
		var r AuditRecord
		if err := json.Unmarshal(scanner.Bytes(), &r); err != nil || r.Hash == "" {
			continue
		}
		if r.Type == AuditTypeChainStart {
			cl.lastHash = r.Hash
			continue
		}
		// Only execute-command records populate the recent-commands window;
		// file-transfer/session records live on disk only.
		if ts, err := time.Parse(time.RFC3339Nano, r.TS); err == nil && (r.Tool == "execute-command" || r.Tool == "") {
			cl.items = append(cl.items, CommandLogEntry{
				Timestamp: ts,
				Command:   r.Command,
				ExitCode:  r.ExitCode,
				Success:   r.Success,
			})
		}
		if len(cl.items) > cl.size {
			cl.items = cl.items[len(cl.items)-cl.size:]
		}
		cl.lastHash = r.Hash
	}
}

// Add appends one execute-command outcome to the audit chain and to the
// in-memory window. Failed commands are always written; onlySuccess filters
// the window only. A write failure is reported on stderr and never changes
// the fact the command already ran.
func (cl *CommandLog) Add(e CommandLogEntry) {
	cl.AddAudit(AuditRecord{
		V:        auditVersion,
		Type:     AuditTypeCommand,
		TS:       e.Timestamp.UTC().Format(time.RFC3339Nano),
		Tool:     "execute-command",
		Command:  e.Command,
		ExitCode: e.ExitCode,
		Success:  e.Success,
	}, true)
}

// AddAudit appends one record to the audit chain and, when toWindow is set,
// to the in-memory recent-commands window (execute-command outcomes only:
// file-transfer and session records stay disk-only so the window keeps its
// command-history semantics for list-servers and the history supplement).
func (cl *CommandLog) AddAudit(r AuditRecord, toWindow bool) {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	ts := time.Now()
	if r.TS != "" {
		if parsed, err := time.Parse(time.RFC3339Nano, r.TS); err == nil {
			ts = parsed
		}
	} else {
		r.TS = ts.UTC().Format(time.RFC3339Nano)
	}
	if toWindow {
		cl.items = append(cl.items, CommandLogEntry{
			Timestamp: ts,
			Command:   r.Command,
			ExitCode:  r.ExitCode,
			Success:   r.Success,
		})
		if len(cl.items) > cl.size {
			cl.items = cl.items[len(cl.items)-cl.size:]
		}
	}
	cl.pending = append(cl.pending, r)
	if cl.flushMs == 0 {
		// Write-through policy: strictest durability, every record hits the
		// disk before this call returns.
		cl.flushLocked()
		return
	}
	if len(cl.pending) >= auditFlushRecords || cl.pendingSize() >= auditFlushBytes {
		cl.flushLocked()
		return
	}
	if cl.flushTimer == nil {
		cl.flushTimer = time.AfterFunc(time.Duration(cl.flushMs)*time.Millisecond, cl.flushTimerFired)
	}
}

// pendingSize estimates the buffered bytes without marshalling (the record
// serialized size is dominated by the command text).
func (cl *CommandLog) pendingSize() int {
	n := 0
	for i := range cl.pending {
		n += len(cl.pending[i].Command) + 128
	}
	return n
}

func (cl *CommandLog) flushTimerFired() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.flushTimer = nil
	cl.flushLocked()
}

// Flush writes any buffered records to disk. Safe to call concurrently with
// Add; a no-op when nothing is buffered.
func (cl *CommandLog) Flush() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.flushLocked()
}

// Close flushes and releases the file handle (and any pending timer). The
// log stays usable: the handle is reopened lazily on the next flush.
func (cl *CommandLog) Close() {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	cl.flushLocked()
	if cl.flushTimer != nil {
		cl.flushTimer.Stop()
		cl.flushTimer = nil
	}
	if cl.file != nil {
		_ = cl.file.Close()
		cl.file = nil
	}
}

// flushLocked seals, links and writes every pending record as one batch.
// Callers hold cl.mu. The records chain from the current disk tail hash, so
// the on-disk chain verifies even though earlier records may still be
// buffered. On a write error the batch is dropped with a stderr report: the
// command already ran, and keeping the chain anchored to the disk tail keeps
// the file verifiable.
func (cl *CommandLog) flushLocked() {
	if len(cl.pending) == 0 {
		return
	}
	var buf bytes.Buffer
	prev := cl.lastHash
	lines := int64(0)

	// Rotate first when the current file has reached either limit; the
	// chain-start record joins the same batch so one Write carries both.
	if cl.fileLines >= int64(auditMaxLines) || (cl.fileBytes > 0 && cl.fileBytes >= int64(auditMaxBytes)) {
		if err := cl.rotateLocked(); err != nil {
			fmt.Fprintf(os.Stderr, "[2native-ssh-mcp] audit: rotation failed for %s: %v\n", cl.path, err)
		} else {
			start := AuditRecord{V: auditVersion, Type: AuditTypeChainStart}
			if sealed, err := sealRecord(start, cl.name, prev); err == nil {
				if line, err := json.Marshal(sealed); err == nil {
					buf.Write(line)
					buf.WriteByte('\n')
					prev = sealed.Hash
					lines++
				}
			}
		}
	}

	for i := range cl.pending {
		sealed, err := sealRecord(cl.pending[i], cl.name, prev)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[2native-ssh-mcp] audit: cannot seal record for %s: %v\n", cl.path, err)
			continue
		}
		line, err := json.Marshal(sealed)
		if err != nil {
			continue
		}
		buf.Write(line)
		buf.WriteByte('\n')
		prev = sealed.Hash
		lines++
	}
	cl.pending = cl.pending[:0]
	if lines == 0 {
		return
	}

	if err := cl.ensureFileLocked(); err != nil {
		fmt.Fprintf(os.Stderr, "[2native-ssh-mcp] audit: failed to open %s: %v\n", cl.path, err)
		return
	}
	n, err := cl.file.Write(buf.Bytes())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[2native-ssh-mcp] audit: failed to append %s: %v\n", cl.path, err)
		return
	}
	cl.fileBytes += int64(n)
	cl.fileLines += lines
	cl.lastHash = prev
}

// sealRecord fills the connection, timestamp and prev-hash fields and
// computes the record hash. prev is the previous record's hash; "" anchors
// the chain at zeroHash (first record of the first file).
func sealRecord(r AuditRecord, connection, prev string) (AuditRecord, error) {
	if r.TS == "" {
		r.TS = time.Now().UTC().Format(time.RFC3339Nano)
	}
	r.Connection = connection
	if prev == "" {
		r.PrevHash = zeroHash
	} else {
		r.PrevHash = prev
	}
	if err := r.seal(); err != nil {
		return r, err
	}
	return r, nil
}

// ensureFileLocked opens the log file for appending, keeping the handle
// open across flushes (one handle, O_APPEND writes; reopened lazily after
// Close or rotation). Callers hold cl.mu.
func (cl *CommandLog) ensureFileLocked() error {
	if cl.file != nil {
		return nil
	}
	f, err := os.OpenFile(cl.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cl.file = f
	return nil
}

// rotateLocked renames the current file to <name>.jsonl.<utc> so the next
// flush opens a fresh one. The caller writes the chain-start record linking
// the old tail hash. Callers hold cl.mu.
func (cl *CommandLog) rotateLocked() error {
	if cl.fileLines > 0 {
		// Windows refuses to rename an open file: release the handle first.
		if cl.file != nil {
			_ = cl.file.Close()
			cl.file = nil
		}
		// Nanosecond UTC suffix: rotations can happen twice within one wall
		// second, and the suffix must stay unique and lexicographically
		// chronological for audit-verify's file ordering.
		stamp := time.Now().UTC().Format("20060102T150405.000000000Z")
		rotated := cl.path + "." + stamp
		for i := 1; ; i++ {
			if _, err := os.Stat(rotated); err == nil {
				rotated = fmt.Sprintf("%s.%s.%d", cl.path, stamp, i)
				continue
			}
			break
		}
		if err := os.Rename(cl.path, rotated); err != nil {
			return err
		}
	}
	cl.fileBytes = 0
	cl.fileLines = 0
	return nil
}

// Recent returns a copy of the last n entries (oldest first). With
// onlySuccess set, failed entries are filtered out of this display window
// (they remain on disk). Safe on a nil log (disabled) and concurrently with
// Add.
func (cl *CommandLog) Recent(n int) []CommandLogEntry {
	if cl == nil {
		return nil
	}
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if n <= 0 {
		return nil
	}
	out := make([]CommandLogEntry, 0, min(n, len(cl.items)))
	for i := len(cl.items) - 1; i >= 0 && len(out) < n; i-- {
		if cl.onlySuccess && !cl.items[i].Success {
			continue
		}
		out = append(out, cl.items[i])
	}
	// Oldest first.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

// AuditVerifyIssue describes the first broken link of an audit chain.
type AuditVerifyIssue struct {
	Path         string
	Line         int    // 1-based; 0 = file-level problem
	Reason       string // "hash mismatch" / "prevHash mismatch" / "legacy record" / "unexpected end of file"
	ExpectedHash string
	ActualHash   string
}

func (e *AuditVerifyIssue) Error() string {
	if e.Line > 0 {
		return fmt.Sprintf("%s: line %d: %s (expected %s, got %s)",
			e.Path, e.Line, e.Reason, shortHash(e.ExpectedHash), shortHash(e.ActualHash))
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Reason)
}

func shortHash(h string) string {
	if len(h) > 12 {
		return h[:12]
	}
	return h
}

// VerifyAuditFiles verifies a sequence of audit files that form one chain in
// time order (rotated files first, the live file last): every line's prevHash
// must equal the previous line's hash across file boundaries, and every
// line's hash must match its canonical content. The first broken line is
// reported; nil means the whole chain is intact.
func VerifyAuditFiles(paths []string) error {
	prev := zeroHash
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(f)
		scanner.Buffer(make([]byte, 1024*1024), 1024*1024)
		lineNo := 0
		for scanner.Scan() {
			lineNo++
			raw := bytes.TrimSpace(scanner.Bytes())
			if len(raw) == 0 {
				continue
			}
			var r AuditRecord
			if err := json.Unmarshal(raw, &r); err != nil {
				f.Close()
				return &AuditVerifyIssue{Path: path, Line: lineNo, Reason: "unexpected end of file / malformed line"}
			}
			if r.Hash == "" {
				f.Close()
				return &AuditVerifyIssue{Path: path, Line: lineNo, Reason: "legacy record (no hash), not a hash chain"}
			}
			want := r.Hash
			if err := r.seal(); err != nil {
				f.Close()
				return &AuditVerifyIssue{Path: path, Line: lineNo, Reason: "canonical encoding failed"}
			}
			if r.Hash != want {
				f.Close()
				return &AuditVerifyIssue{Path: path, Line: lineNo, Reason: "hash mismatch", ExpectedHash: want, ActualHash: r.Hash}
			}
			if r.PrevHash != prev {
				f.Close()
				return &AuditVerifyIssue{Path: path, Line: lineNo, Reason: "prevHash mismatch", ExpectedHash: prev, ActualHash: r.PrevHash}
			}
			prev = r.Hash
		}
		if err := scanner.Err(); err != nil {
			f.Close()
			return &AuditVerifyIssue{Path: path, Line: lineNo + 1, Reason: "unexpected end of file"}
		}
		f.Close()
	}
	return nil
}

// AuditFilesForConnection returns the audit files for a connection in chain
// order: the live <name>.jsonl last, rotated <name>.jsonl.<ts> files first
// (their UTC suffixes sort chronologically).
func AuditFilesForConnection(dir, name string) []string {
	matches, _ := filepath.Glob(filepath.Join(dir, name+".jsonl*"))
	sort.Strings(matches)
	// The live file must come last even when a rotated suffix would sort
	// before it lexically (cannot happen with UTC timestamps, but be strict).
	live := filepath.Join(dir, name+".jsonl")
	out := make([]string, 0, len(matches))
	for _, p := range matches {
		if p != live {
			out = append(out, p)
		}
	}
	if _, err := os.Stat(live); err == nil {
		out = append(out, live)
	}
	return out
}

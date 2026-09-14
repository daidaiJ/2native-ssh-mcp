package manager

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"2native-ssh-mcp/internal/config"
)

var ten = 10

// readAuditRecords parses an audit JSONL file into records (test helper).
func readAuditRecords(path string) ([]AuditRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []AuditRecord
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var r AuditRecord
		if err := json.Unmarshal([]byte(line), &r); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out = append(out, r)
	}
	return out, scanner.Err()
}

// TestAuditCanonicalHashPinned pins the canonical JSON encoding and its hash
// for one fixed record: same struct, same 64-hex hash, forever. Changing the
// canonical form or the hash input breaks every existing audit chain, so it
// must be a deliberate, documented act.
func TestAuditCanonicalHashPinned(t *testing.T) {
	r := AuditRecord{
		V:          1,
		Type:       "command",
		TS:         "2026-01-02T03:04:05.123456789Z",
		Connection: "prod",
		Tool:       "execute-command",
		Command:    "systemctl restart nginx",
		Session:    "",
		Approval:   "accepted",
		ExitCode:   0,
		Success:    true,
		ErrorCode:  "",
		PrevHash:   "1111111111111111111111111111111111111111111111111111111111111111",
	}
	canonical := `{"approval":"accepted","command":"systemctl restart nginx","connection":"prod","errorCode":"","exitCode":0,"prevHash":"1111111111111111111111111111111111111111111111111111111111111111","session":"","success":true,"tool":"execute-command","ts":"2026-01-02T03:04:05.123456789Z","type":"command","v":1}`
	got, err := r.canonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != canonical {
		t.Fatalf("canonical JSON changed:\n old: %s\n new: %s", canonical, got)
	}
	const pinnedHash = "eb3d55a6a9000852d1db929da4d947080a42ee9b44348b3bf012cd5a272cd50c"
	if err := r.seal(); err != nil {
		t.Fatal(err)
	}
	if r.Hash != pinnedHash {
		t.Fatalf("hash changed for the pinned canonical form (existing chains would not verify against new records):\n want %s\n got  %s", pinnedHash, r.Hash)
	}
}

// TestAuditCanonicalDeterministic requires two encodings of the same record
// to produce the same bytes and hash.
func TestAuditCanonicalDeterministic(t *testing.T) {
	mk := func() AuditRecord {
		return AuditRecord{
			V: 1, Type: "command",
			TS:         "2026-05-06T07:08:09Z",
			Connection: "dev", Tool: "file-transfer",
			Command: "upload /srv/app.tar.gz", Approval: "none",
			ExitCode: 0, Success: true, PrevHash: zeroHash,
		}
	}
	a, b := mk(), mk()
	if err := a.seal(); err != nil {
		t.Fatal(err)
	}
	if err := b.seal(); err != nil {
		t.Fatal(err)
	}
	if a.Hash != b.Hash {
		t.Fatalf("same record must hash identically: %s vs %s", a.Hash, b.Hash)
	}
	ca, _ := a.canonicalJSON()
	cb, _ := b.canonicalJSON()
	if string(ca) != string(cb) {
		t.Fatalf("canonical encodings differ:\n%s\n%s", ca, cb)
	}
	if len(a.Hash) != 64 || strings.ToLower(a.Hash) != a.Hash {
		t.Fatalf("hash must be 64 lowercase hex chars, got %q", a.Hash)
	}
}

// TestAuditHashExcludesHashField checks that the sealed hash stays stable
// when the hash field is pre-populated (the canonical form must ignore it).
func TestAuditHashExcludesHashField(t *testing.T) {
	r := AuditRecord{V: 1, TS: "2026-01-01T00:00:00Z", Connection: "c", Command: "id", Success: true, PrevHash: zeroHash}
	if err := r.seal(); err != nil {
		t.Fatal(err)
	}
	withHash := r
	withHash.Hash = "deadbeef"
	got, _ := withHash.canonicalJSON()
	want, _ := r.canonicalJSON()
	if string(got) != string(want) {
		t.Fatalf("hash field must not affect the canonical form")
	}
}

func TestAuditRotatePreservesChain(t *testing.T) {
	oldLines, oldBytes := auditMaxLines, auditMaxBytes
	auditMaxLines = 5
	auditMaxBytes = 1 << 30
	defer func() { auditMaxLines, auditMaxBytes = oldLines, oldBytes }()

	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := 0; i < 12; i++ {
		log.Add(CommandLogEntry{Timestamp: time.Now(), Command: fmt.Sprintf("cmd-%d", i), Success: true})
	}

	entries, err := filepath.Glob(filepath.Join(dir, "dev.jsonl*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) < 2 {
		t.Fatalf("expected rotated files plus live file, got %v", entries)
	}
	var rotated []string
	var live string
	for _, p := range entries {
		if p == filepath.Join(dir, "dev.jsonl") {
			live = p
		} else {
			rotated = append(rotated, p)
		}
	}
	if live == "" || len(rotated) == 0 {
		t.Fatalf("expected one live file and rotated files, got %v", entries)
	}

	// The new file must start with a chain-start whose prevHash is the tail
	// hash of the chronologically previous file (the last rotated one).
	sort.Strings(rotated) // nanosecond UTC suffixes sort chronologically
	oldRecords, err := readAuditRecords(rotated[len(rotated)-1])
	if err != nil {
		t.Fatal(err)
	}
	newRecords, err := readAuditRecords(live)
	if err != nil {
		t.Fatal(err)
	}
	if len(newRecords) == 0 || newRecords[0].Type != AuditTypeChainStart {
		t.Fatalf("rotated-into file must start with a chain-start record, got %+v", newRecords)
	}
	if newRecords[0].PrevHash != oldRecords[len(oldRecords)-1].Hash {
		t.Fatalf("chain-start.prevHash must equal the old file tail hash: %s vs %s",
			newRecords[0].PrevHash, oldRecords[len(oldRecords)-1].Hash)
	}

	// The whole sequence verifies as one chain, oldest file first.
	if err := VerifyAuditFiles(AuditFilesForConnection(dir, "dev")); err != nil {
		t.Fatalf("cross-file chain must verify: %v", err)
	}
}

func TestAuditVerifyTamperedLine(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "echo one", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "echo two", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "echo three", Success: true})

	path := filepath.Join(dir, "dev.jsonl")
	if err := VerifyAuditFiles([]string{path}); err != nil {
		t.Fatalf("fresh chain must verify: %v", err)
	}

	// Tamper with the middle line's command.
	records, err := readAuditRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := make([]string, len(records))
	for i, r := range records {
		if i == 1 {
			r.Command = "echo EVIL"
		}
		b, _ := json.Marshal(r)
		lines[i] = string(b)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = VerifyAuditFiles([]string{path})
	issue, ok := err.(*AuditVerifyIssue)
	if !ok {
		t.Fatalf("expected AuditVerifyIssue, got %v", err)
	}
	if issue.Line != 2 {
		t.Fatalf("tampered line 2 must be reported, got line %d (%v)", issue.Line, err)
	}
	if issue.Reason != "hash mismatch" {
		t.Fatalf("expected hash mismatch, got %q", issue.Reason)
	}
}

func TestAuditVerifyPrevHashSplice(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "a", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "b", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "c", Success: true})

	// Replace the middle line entirely with a self-consistent record: its own
	// hash matches its content, but the chain link to the previous line is
	// broken. Verify must catch the prevHash mismatch.
	path := filepath.Join(dir, "dev.jsonl")
	records, err := readAuditRecords(path)
	if err != nil {
		t.Fatal(err)
	}
	forged := AuditRecord{
		V: auditVersion, Type: AuditTypeCommand,
		TS: "2026-01-01T00:00:00Z", Connection: "dev",
		Tool: "execute-command", Command: "injected",
		Success: true, PrevHash: zeroHash,
	}
	if err := forged.seal(); err != nil {
		t.Fatal(err)
	}
	out := []string{mustJSON(t, records[0]), mustJSON(t, forged), mustJSON(t, records[2])}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = VerifyAuditFiles([]string{path})
	issue, ok := err.(*AuditVerifyIssue)
	if !ok {
		t.Fatalf("expected AuditVerifyIssue, got %v", err)
	}
	if issue.Line != 2 || issue.Reason != "prevHash mismatch" {
		t.Fatalf("expected prevHash mismatch at line 2, got line %d reason %q", issue.Line, issue.Reason)
	}
}

func TestAuditVerifyTruncatedLine(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "a", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "b", Success: true})

	path := filepath.Join(dir, "dev.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a torn final write: cut the file mid-line.
	text := strings.TrimRight(string(data), "\n")
	cut := text[:len(text)-20]
	if err := os.WriteFile(path, []byte(cut), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyAuditFiles([]string{path}); err == nil {
		t.Fatal("truncated final line must fail verification")
	}
}

func TestAuditLegacyLogArchived(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"timestamp":"2026-01-01T00:00:00Z","command":"old-style","exitCode":0,"success":true}` + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dev.log"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}

	log, err := NewCommandLog(dir, "dev", 10, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "new-style", Success: true})

	if _, err := os.Stat(filepath.Join(dir, "dev.log")); !os.IsNotExist(err) {
		t.Fatalf("legacy file must be renamed away, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "dev.log.legacy")); err != nil {
		t.Fatalf("legacy copy must exist: %v", err)
	}
	records, err := readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].Command != "new-style" {
		t.Fatalf("legacy lines must not be spliced into the new chain, got %+v", records)
	}
	if err := VerifyAuditFiles([]string{filepath.Join(dir, "dev.jsonl")}); err != nil {
		t.Fatalf("new chain must verify: %v", err)
	}
}

func TestAuditRecentWindowAfterReload(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 3, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := 0; i < 5; i++ {
		log.Add(CommandLogEntry{Timestamp: time.Now(), Command: fmt.Sprintf("cmd-%d", i), Success: true})
	}
	reloaded, err := NewCommandLog(dir, "dev", 3, false, 0)
	if err != nil {
		t.Fatal(err)
	}
	recent := reloaded.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("expected window of 3 after reload, got %d", len(recent))
	}
	if recent[2].Command != "cmd-4" || recent[0].Command != "cmd-2" {
		t.Fatalf("window must keep the newest entries oldest-first: %+v", recent)
	}
	// The disk file keeps every line — nothing is rewritten away.
	records, err := readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("append-only log must keep all 5 records, got %d", len(records))
	}
}

// TestRejectedCommandAudited pins the approval-gate audit contract: a command
// refused by the gate never reaches the remote host but still lands in the
// jsonl with approval=declined and success=false (REQ-002 R3).
func TestRejectedCommandAudited(t *testing.T) {
	dir := t.TempDir()
	m, err := New(map[string]*config.SSHConfig{
		"prod": {Host: "192.0.2.1", Username: "u", Port: 22, CommandLogSize: &ten, CommandLogDir: dir},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	defer m.DisconnectAll()
	m.RecordRejectedCommand("", "prod", "rm -rf /tmp/build", ApprovalDeclined)
	m.DisconnectAll() // graceful shutdown: flush + close the audit log

	records, err := readAuditRecords(filepath.Join(dir, "prod.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 {
		t.Fatalf("expected exactly 1 audit record, got %d", len(records))
	}
	r := records[0]
	if r.Command != "rm -rf /tmp/build" || r.Approval != ApprovalDeclined || r.Success {
		t.Fatalf("declined command must be audited as declined/failed, got %+v", r)
	}
	if r.Tool != "execute-command" {
		t.Fatalf("expected execute-command tool, got %q", r.Tool)
	}
	if err := VerifyAuditFiles([]string{filepath.Join(dir, "prod.jsonl")}); err != nil {
		t.Fatalf("chain must verify: %v", err)
	}
	// Nothing ran, so the recent-commands window (what list-servers shows)
	// stays empty for this disk-only record.
	if got := m.RecentCommands("prod", 5); len(got) != 0 {
		t.Fatalf("declined record must not enter the recent window, got %+v", got)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestAuditBufferedFlush covers the batched write path: with flushMs > 0
// records may sit in the buffer (chain still open), and Flush/Close carry
// them to disk with the chain intact.
func TestAuditBufferedFlush(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 60000)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "buffered-a", Success: true})
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "buffered-b", Success: true})

	// Still buffered: the file must not exist yet (flush timer pending).
	if _, err := os.Stat(filepath.Join(dir, "dev.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("buffered records must not hit the disk before a flush, stat err=%v", err)
	}
	// The in-memory window already reflects them.
	if got := log.Recent(10); len(got) != 2 {
		t.Fatalf("window must show buffered records, got %+v", got)
	}

	log.Flush()
	records, err := readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 2 || records[1].Command != "buffered-b" {
		t.Fatalf("flush must write every buffered record in order, got %+v", records)
	}
	if records[0].PrevHash != zeroHash || records[1].PrevHash != records[0].Hash {
		t.Fatalf("flushed batch must form a chain, got %+v", records)
	}

	// More records, then Close: graceful shutdown flushes the tail.
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "buffered-c", Success: true})
	log.Close()
	if err := VerifyAuditFiles([]string{filepath.Join(dir, "dev.jsonl")}); err != nil {
		t.Fatalf("chain must verify after Close: %v", err)
	}
	records, _ = readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if len(records) != 3 {
		t.Fatalf("Close must flush the buffered tail, got %d records", len(records))
	}
}

// TestAuditFlushTimerFires checks the time-based trigger end to end.
func TestAuditFlushTimerFires(t *testing.T) {
	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 30)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	log.Add(CommandLogEntry{Timestamp: time.Now(), Command: "timed", Success: true})

	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(dir, "dev.jsonl")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("flush timer did not write the record within 2s")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := VerifyAuditFiles([]string{filepath.Join(dir, "dev.jsonl")}); err != nil {
		t.Fatalf("timer-flushed chain must verify: %v", err)
	}
}

// TestAuditBufferedBatchCap checks that the record-count cap flushes before
// the configured interval elapses.
func TestAuditBufferedBatchCap(t *testing.T) {
	old := auditFlushRecords
	auditFlushRecords = 4
	defer func() { auditFlushRecords = old }()

	dir := t.TempDir()
	log, err := NewCommandLog(dir, "dev", 10, false, 60000)
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	for i := 0; i < 6; i++ {
		log.Add(CommandLogEntry{Timestamp: time.Now(), Command: fmt.Sprintf("cmd-%d", i), Success: true})
		if i < 3 {
			// Below the cap nothing may be flushed yet.
			if _, err := os.Stat(filepath.Join(dir, "dev.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("records below the cap must stay buffered, flushed at %d", i)
			}
		}
	}
	// The cap flushed 4 records; the last 2 are still buffered.
	partial, err := readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(partial) != auditFlushRecords {
		t.Fatalf("cap must flush exactly %d records, got %d", auditFlushRecords, len(partial))
	}

	log.Close()
	records, err := readAuditRecords(filepath.Join(dir, "dev.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 6 {
		t.Fatalf("Close must flush the buffered tail, got %d of 6", len(records))
	}
	if err := VerifyAuditFiles([]string{filepath.Join(dir, "dev.jsonl")}); err != nil {
		t.Fatalf("chain must verify: %v", err)
	}
}

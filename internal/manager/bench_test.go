package manager

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"2native-ssh-mcp/internal/config"
)

// Micro-benchmarks for the output pipeline hot path. Every command result
// passes through stripANSI -> redact -> compress (or spill), so these show
// where the per-output CPU goes. Run with:
//
//	go test ./internal/manager/ -bench BenchmarkOutput -benchmem
//
// The sizes match the thresholds that matter: 4KiB compress threshold,
// 8KiB spill threshold and the 10MiB maxOutputBytes collection cap.

const benchOutputBytes = 1 << 20 // 1 MiB

func benchPlainOutput(n int) string {
	var b strings.Builder
	line := 0
	for b.Len() < n {
		fmt.Fprintf(&b, "2026-09-04 10:00:%02d INFO  worker=%d processed request id=%d status=ok latency=%dms\n", line%60, line%8, line, line%97)
		line++
	}
	return b.String()
}

func benchANSIOutput(n int) string {
	var b strings.Builder
	line := 0
	for b.Len() < n {
		fmt.Fprintf(&b, "\x1b[32m2026-09-04 10:00:%02d\x1b[0m \x1b[1mINFO\x1b[0m worker=%d processed request id=%d\n", line%60, line%8, line)
		line++
	}
	return b.String()
}

func BenchmarkOutputStripANSIPlain1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = stripANSI(s)
	}
}

func BenchmarkOutputStripANSIAnsi1MiB(b *testing.B) {
	s := benchANSIOutput(benchOutputBytes)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = stripANSI(s)
	}
}

func BenchmarkOutputRedactPlain1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes)
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = RedactSensitiveOutput(s)
	}
}

func BenchmarkOutputRedactWithSecrets1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes) + "\npassword=hunter2\ntoken=abc123\n"
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = RedactSensitiveOutput(s)
	}
}

func BenchmarkOutputCompress1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes)
	opts := DefaultCompressOptions()
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = CompressCommandOutput(s, opts)
	}
}

func BenchmarkOutputBuildCommandResultPlain1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes)
	cfg := &config.SSHConfig{OutputSpillThreshold: -1} // spill off: measure the compress path
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = buildCommandResult(s, "", 0, StatusOK, cfg)
	}
}

func BenchmarkOutputBuildCommandResultANSI1MiB(b *testing.B) {
	s := benchANSIOutput(benchOutputBytes)
	cfg := &config.SSHConfig{OutputSpillThreshold: -1}
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = buildCommandResult(s, "", 0, StatusOK, cfg)
	}
}

func BenchmarkOutputLimitedBufferWrite1MiB(b *testing.B) {
	chunk := []byte(benchPlainOutput(32 * 1024))
	b.SetBytes(int64(len(chunk) * b.N))
	for i := 0; i < b.N; i++ {
		b.StopTimer()
		var budget atomic.Int32
		budget.Store(int32(benchOutputBytes))
		buf := &limitedBuffer{max: benchOutputBytes, shared: &budget}
		b.StartTimer()
		for written := 0; written < benchOutputBytes; written += len(chunk) {
			buf.Write(chunk)
		}
	}
}

func BenchmarkOutputBuildCommandResultRedact1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes) + "\npassword=hunter2\ntoken=abc123\n"
	yes := true
	cfg := &config.SSHConfig{OutputSpillThreshold: -1, RedactSecrets: &yes}
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = buildCommandResult(s, "", 0, StatusOK, cfg)
	}
}

func BenchmarkOutputRedactKVScanner1MiB(b *testing.B) {
	s := benchPlainOutput(benchOutputBytes) + "\npassword=hunter2\ntoken=abc123\n"
	b.SetBytes(int64(len(s)))
	for i := 0; i < b.N; i++ {
		_ = redactKV(s)
	}
}

// Benchmarks for the per-command audit log. Every executed command pays one
// AddAudit; the question is whether the hash chain (canonical JSON + SHA-256
// + one O_APPEND write) costs more than the legacy model it replaced
// (buffer the last N records, rewrite the whole file, rename over the old).
// Run with:
//
//	go test ./internal/manager/ -bench BenchmarkCommandLog -benchmem

func benchCommandLogEntries(b *testing.B) []CommandLogEntry {
	entries := make([]CommandLogEntry, b.N)
	for i := range entries {
		entries[i] = CommandLogEntry{
			Timestamp: time.Now(),
			Command:   "systemctl restart nginx && journalctl -u nginx --since -5m | tail -n 50",
			ExitCode:  0,
			Success:   true,
		}
	}
	return entries
}

// BenchmarkCommandLogAppendChain measures the shipped model: hash-chained
// append-only JSONL.
func BenchmarkCommandLogAppendChain(b *testing.B) {
	dir := b.TempDir()
	log, err := NewCommandLog(dir, "bench", 20, false, 0)
	if err != nil {
		b.Fatal(err)
	}
	defer log.Close()
	entries := benchCommandLogEntries(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Add(entries[i])
	}
}

// BenchmarkCommandLogRewriteLegacy measures the pre-chain model this
// replaced: keep the last 20 entries and atomically rewrite the whole file
// on every record.
func BenchmarkCommandLogRewriteLegacy(b *testing.B) {
	dir := b.TempDir()
	path := filepath.Join(dir, "legacy.log")
	entries := benchCommandLogEntries(b)
	window := make([]CommandLogEntry, 0, 20)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		window = append(window, entries[i])
		if len(window) > 20 {
			window = window[len(window)-20:]
		}
		tmp := path + ".tmp"
		f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			b.Fatal(err)
		}
		enc := json.NewEncoder(f)
		for _, e := range window {
			_ = enc.Encode(e)
		}
		_ = f.Close()
		_ = os.Rename(tmp, path)
	}
}

// BenchmarkCommandLogAppendBuffered measures the default policy (5s flush
// interval, batch caps at 32 records): per-record cost amortizes to the
// window append plus a share of the hash and the batched write.
func BenchmarkCommandLogAppendBuffered(b *testing.B) {
	dir := b.TempDir()
	log, err := NewCommandLog(dir, "bench", 20, false, 5000)
	if err != nil {
		b.Fatal(err)
	}
	defer log.Close()
	entries := benchCommandLogEntries(b)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		log.Add(entries[i])
	}
}

// BenchmarkAuditCanonicalHash isolates the CPU cost of one canonical JSON
// encoding + SHA-256 (the per-record hashing overhead, no I/O).
func BenchmarkAuditCanonicalHash(b *testing.B) {
	r := AuditRecord{
		V: auditVersion, Type: AuditTypeCommand,
		TS: "2026-09-14T12:00:00.123456789Z", Connection: "bench",
		Tool: "execute-command",
		Command: "kubectl get pods -n prod -o wide | grep -v Completed | head -20",
		Approval: ApprovalAccepted, PrevHash: zeroHash,
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.seal(); err != nil {
			b.Fatal(err)
		}
	}
}

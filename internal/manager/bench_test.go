package manager

import (
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

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

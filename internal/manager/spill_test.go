package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"2native-ssh-mcp/internal/config"
)

func spillTestCfg(dir string, threshold int) *config.SSHConfig {
	yes := true
	return &config.SSHConfig{OutputSpillThreshold: threshold, OutputSpillDir: dir, RedactSecrets: &yes}
}

func TestSpillAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	cfg := spillTestCfg(dir, 100)
	body := strings.Repeat("line of output with token=SECRET123\n", 5) // 185 bytes
	res := buildCommandResult(body, "", 0, StatusOK, cfg)

	if res.OutputFile == "" {
		t.Fatalf("output above the spill threshold must be spilled: %+v", res)
	}
	if res.OutputFileBytes == 0 || res.OutputFileLines == 0 {
		t.Fatalf("spill info must record bytes and lines: %+v", res)
	}
	if !filepath.IsAbs(res.OutputFile) {
		t.Fatalf("spill path must be absolute, got: %q", res.OutputFile)
	}
	data, err := os.ReadFile(res.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.HasPrefix(content, "[stdout]\n") {
		t.Fatalf("spill file must have a [stdout] section, got: %q", head(content, 40))
	}
	if strings.Contains(content, "SECRET123") {
		t.Fatalf("spill file must contain redacted output only")
	}
	if !strings.Contains(content, "[REDACTED]") {
		t.Fatalf("spill file must contain the redacted placeholder")
	}
	// The result itself only carries a preview plus the notice.
	if len(res.Stdout) >= len(content) {
		t.Fatalf("result stdout must be a short preview, got %d bytes", len(res.Stdout))
	}
	if !strings.Contains(res.Stdout, "[REDACTED]") {
		t.Fatalf("preview must be redacted too")
	}
	if strings.Contains(res.Stdout, "output compressed") {
		t.Fatalf("spilled output must not also be compressed")
	}
	if !strings.Contains(res.Text(), "use local Read/Grep") {
		t.Fatalf("Text() must point the agent at the spill file: %q", res.Text())
	}
	if res.ExitCode != 0 || res.Status != StatusOK || !res.ReplaySafe {
		t.Fatalf("spill must not change the command outcome: %+v", res)
	}
}

func TestSpillIncludesStderrSection(t *testing.T) {
	dir := t.TempDir()
	cfg := spillTestCfg(dir, 50)
	res := buildCommandResult("out", strings.Repeat("e", 100), 0, StatusOK, cfg)
	if res.OutputFile == "" {
		t.Fatal("combined output above threshold must be spilled")
	}
	data, err := os.ReadFile(res.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\n[stderr]\n") {
		t.Fatalf("spill file must keep a [stderr] section")
	}
}

func TestSpillBelowThresholdKeepsCompression(t *testing.T) {
	dir := t.TempDir()
	cfg := spillTestCfg(dir, 1<<20) // 1 MiB: never reached
	cfg.OutputCompressThreshold = 100
	body := strings.Repeat("line\n", 100)
	res := buildCommandResult(body, "", 0, StatusOK, cfg)
	if res.OutputFile != "" {
		t.Fatalf("output below the spill threshold must not be spilled: %+v", res)
	}
	if !strings.Contains(res.Stdout, "output compressed") {
		t.Fatalf("compression must still apply below the spill threshold")
	}
}

func TestSpillDisabled(t *testing.T) {
	dir := t.TempDir()
	cfg := spillTestCfg(dir, -1)
	cfg.OutputCompressThreshold = 100
	body := strings.Repeat("line\n", 1000)
	res := buildCommandResult(body, "", 0, StatusOK, cfg)
	if res.OutputFile != "" {
		t.Fatalf("outputSpillThreshold=-1 must disable spilling: %+v", res)
	}
	if !strings.Contains(res.Stdout, "output compressed") {
		t.Fatalf("disabled spill must fall back to compression")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("no spill files may be created when spill is disabled")
	}
}

func TestSpillWriteFailureFallsBackToCompression(t *testing.T) {
	// A regular file where the directory should be makes MkdirAll fail.
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := spillTestCfg(blocked, 10)
	cfg.OutputCompressThreshold = 20
	body := strings.Repeat("line\n", 50)
	res := buildCommandResult(body, "", 0, StatusOK, cfg)
	if res.OutputFile != "" {
		t.Fatalf("spill failure must fall back to compression, got: %+v", res)
	}
	if !strings.Contains(res.Stdout, "output compressed") {
		t.Fatalf("fallback must use compression, got: %q", head(res.Stdout, 80))
	}
}

func TestSpillDirCapKeepsNewest(t *testing.T) {
	dir := t.TempDir()
	old := time.Now().Add(-time.Hour)
	for i := 0; i < spillDirMaxFiles+3; i++ {
		name := fmt.Sprintf("exec-20200101-%04d.log", i)
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(filepath.Join(dir, name), old, old.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, ok := spillCommandOutput(strings.Repeat("x", 100), "", spillTestCfg(dir, 10)); !ok {
		t.Fatal("spill must succeed")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != spillDirMaxFiles {
		t.Fatalf("spill dir must keep the newest %d files, got %d", spillDirMaxFiles, len(entries))
	}
	// The oldest file must have been removed.
	if _, err := os.Stat(filepath.Join(dir, "exec-20200101-0000.log")); !os.IsNotExist(err) {
		t.Fatalf("oldest spill file must be removed, got err: %v", err)
	}
}

func TestSpillPreviewBounds(t *testing.T) {
	stdout := strings.Repeat("line\n", 50)
	preview := spillPreview(stdout, "")
	if got := strings.Count(preview, "\n"); got != defaultOutputSpillPreviewLines {
		t.Fatalf("preview must keep %d lines plus the marker, got %d newlines", defaultOutputSpillPreviewLines, got)
	}
	if !strings.Contains(preview, "more lines in the spill file") {
		t.Fatalf("preview must note the omitted lines: %q", preview)
	}

	long := strings.Repeat("字", 5000) // one huge multi-byte line
	capped := spillPreview(long, "")
	if len(capped) > spillPreviewLineMax+4 {
		t.Fatalf("preview lines must be capped, got %d bytes", len(capped))
	}
	if !utf8.ValidString(strings.TrimSuffix(capped, "…")) {
		t.Fatalf("preview cap must not split a multi-byte rune")
	}

	if got := spillPreview("", ""); got != "" {
		t.Fatalf("empty output must yield an empty preview, got %q", got)
	}
}

func TestFinalizeShellOutputExtractsPWDBeforeTrim(t *testing.T) {
	// Regression: TrimSpace used to remove the trailing newline before the
	// PWD regex ran, so CWD tracking silently never worked and the __MCP_PWD__
	// line leaked into the agent-visible output.
	raw := "some output\n__MCP_PWD__/home/deploy/app__\n"
	res := finalizeShellOutput(raw, 0, StatusOK, &config.SSHConfig{})
	if res.CWD != "/home/deploy/app" {
		t.Fatalf("CWD must be extracted from raw output, got: %q", res.CWD)
	}
	if strings.Contains(res.Stdout, "__MCP_PWD__") {
		t.Fatalf("PWD line must be stripped from the result: %q", res.Stdout)
	}
	if !strings.Contains(res.Stdout, "some output") {
		t.Fatalf("real output must survive: %q", res.Stdout)
	}
}

func TestFinalizeShellOutputSpilledKeepsCWD(t *testing.T) {
	dir := t.TempDir()
	cfg := spillTestCfg(dir, 50)
	raw := strings.Repeat("log line\n", 30) + "__MCP_PWD__/var/log/app__\n"
	res := finalizeShellOutput(raw, 0, StatusOK, cfg)
	if res.CWD != "/var/log/app" {
		t.Fatalf("CWD must survive spilling, got: %q", res.CWD)
	}
	if res.OutputFile == "" {
		t.Fatal("large output must be spilled")
	}
	data, err := os.ReadFile(res.OutputFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "__MCP_PWD__") {
		t.Fatalf("the PWD marker must not reach the spill file")
	}
}

func head(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

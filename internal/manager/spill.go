package manager

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

const (
	// defaultOutputSpillPreviewLines bounds the preview that replaces the
	// full output in the MCP result once it has been spilled to a file.
	defaultOutputSpillPreviewLines = 12
	// spillPreviewLineMax caps each preview line so one pathological line
	// cannot defeat the purpose of the short notice.
	spillPreviewLineMax = 2000
	// spillDirMaxFiles caps how many spill files are kept; the oldest are
	// removed after each spill.
	spillDirMaxFiles = 32
	spillFilePrefix  = "exec-"
	spillFileExt     = ".log"
)

// spillMu serializes the directory trim so concurrent spills do not race on
// Remove.
var spillMu sync.Mutex

// spillInfo describes one spilled output file.
type spillInfo struct {
	path  string
	bytes int
	lines int
}

// spillCommandOutput writes the redacted stdout/stderr to a local file when
// their combined size reaches the spill threshold, so the agent can Read or
// Grep the full output locally instead of pulling it into the conversation.
// The MCP result then only carries a short notice plus a small preview.
//
// The content is already ANSI-stripped and redacted by the caller — nothing
// unredacted may reach the disk. Returns ok=false (the caller falls back to
// light compression, and the command itself is not failed) when spill is
// disabled, the output is below the threshold, or the write fails.
func spillCommandOutput(stdout, stderr string, cfg *config.SSHConfig) (spillInfo, bool) {
	var zero spillInfo
	if cfg == nil {
		return zero, false
	}
	threshold := cfg.OutputSpillThreshold
	if threshold == 0 {
		threshold = config.DefaultOutputSpillThreshold // un-normalized config
	}
	if threshold < 0 {
		return zero, false
	}
	if len(stdout)+len(stderr) < threshold {
		return zero, false
	}

	dir := cfg.OutputSpillDir
	if dir == "" {
		dir = config.DefaultOutputSpillDir
	}
	if abs, err := filepath.Abs(dir); err == nil {
		dir = abs
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		logger.Warn("output spill: mkdir %s failed (%v); falling back to compression", dir, err)
		return zero, false
	}

	content := "[stdout]\n" + stdout
	if stderr != "" {
		content += "\n[stderr]\n" + stderr
	}
	name := fmt.Sprintf("%s%s-%s%s", spillFilePrefix, time.Now().Format("20060102-150405"), randomID("i"), spillFileExt)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		logger.Warn("output spill: write %s failed (%v); falling back to compression", path, err)
		return zero, false
	}

	trimSpillDir(dir)
	logger.Info("output spill: %d bytes written to %s", len(content), path)
	return spillInfo{path: path, bytes: len(content), lines: strings.Count(content, "\n")}, true
}

// trimSpillDir keeps only the newest spillDirMaxFiles spill files in dir.
func trimSpillDir(dir string) {
	spillMu.Lock()
	defer spillMu.Unlock()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	type spillFile struct {
		name string
		mod  time.Time
	}
	var files []spillFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), spillFilePrefix) || !strings.HasSuffix(e.Name(), spillFileExt) {
			continue
		}
		var mod time.Time
		if info, err := e.Info(); err == nil {
			mod = info.ModTime()
		}
		files = append(files, spillFile{name: e.Name(), mod: mod})
	}
	if len(files) <= spillDirMaxFiles {
		return
	}
	sort.Slice(files, func(i, j int) bool { return files[i].mod.Before(files[j].mod) })
	for i := 0; i < len(files)-spillDirMaxFiles; i++ {
		_ = os.Remove(filepath.Join(dir, files[i].name))
	}
}

// spillPreview builds the short head-of-output preview that travels in the
// MCP result once the full output has been spilled to a file.
func spillPreview(stdout, stderr string) string {
	src := stdout
	if strings.TrimSpace(src) == "" {
		src = stderr
	}
	if src == "" {
		return ""
	}
	lines := strings.Split(src, "\n")
	total := len(lines)
	if total > defaultOutputSpillPreviewLines {
		lines = lines[:defaultOutputSpillPreviewLines]
	}
	for i, line := range lines {
		if len(line) > spillPreviewLineMax {
			lines[i] = utf8SafeTruncate(line, spillPreviewLineMax) + "…"
		}
	}
	out := strings.Join(lines, "\n")
	if total > defaultOutputSpillPreviewLines {
		out += fmt.Sprintf("\n... [%d more lines in the spill file] ...", total-defaultOutputSpillPreviewLines)
	}
	return out
}

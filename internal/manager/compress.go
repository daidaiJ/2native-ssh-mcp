package manager

import (
	"fmt"
	"strings"
)

const (
	defaultCompressThreshold = 4096
	defaultCompressHeadLines = 60
	defaultCompressTailLines = 20
)

// CompressOptions tunes lightweight output compression (lossy).
type CompressOptions struct {
	Enabled   bool
	Threshold int // bytes; compress when len(output) >= Threshold
	HeadLines int
	TailLines int
}

// DefaultCompressOptions returns the default light compression settings.
func DefaultCompressOptions() CompressOptions {
	return CompressOptions{
		Enabled:   true,
		Threshold: defaultCompressThreshold,
		HeadLines: defaultCompressHeadLines,
		TailLines: defaultCompressTailLines,
	}
}

// CompressOptionsFromConfig builds options from connection config (nil cfg → defaults).
func CompressOptionsFromConfig(enabled *bool, threshold int) CompressOptions {
	opts := DefaultCompressOptions()
	if enabled != nil {
		opts.Enabled = *enabled
	}
	if threshold > 0 {
		opts.Threshold = threshold
	}
	return opts
}

// CompressCommandOutput applies deterministic, lossy rules for large command output.
func CompressCommandOutput(s string, opts CompressOptions) string {
	if s == "" || !opts.Enabled || len(s) < opts.Threshold {
		return s
	}
	head, tail := opts.HeadLines, opts.TailLines
	if head <= 0 {
		head = defaultCompressHeadLines
	}
	if tail <= 0 {
		tail = defaultCompressTailLines
	}

	origBytes := len(s)
	lines := strings.Split(s, "\n")
	lines = collapseConsecutiveDuplicateLines(lines)
	lines = collapseExcessiveBlankLines(lines)

	var omitted int
	if len(lines) > head+tail+1 {
		omitted = len(lines) - head - tail
		middle := fmt.Sprintf("... [%d lines omitted; use head/tail/grep/wc for focused output] ...", omitted)
		lines = append(append(lines[:head], middle), lines[len(lines)-tail:]...)
	}

	out := strings.Join(lines, "\n")
	if len(out) >= origBytes {
		return s
	}
	out += fmt.Sprintf(
		"\n\n[output compressed: %d → %d bytes; %d lines omitted. Use head/tail/grep for focused output.]",
		origBytes, len(out), omitted,
	)
	return out
}

func collapseConsecutiveDuplicateLines(lines []string) []string {
	if len(lines) == 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	prev := lines[0]
	count := 1
	flush := func() {
		if count > 2 {
			out = append(out, fmt.Sprintf("%s  ... [repeated %d times]", prev, count))
		} else {
			for i := 0; i < count; i++ {
				out = append(out, prev)
			}
		}
	}
	for i := 1; i < len(lines); i++ {
		if lines[i] == prev {
			count++
			continue
		}
		flush()
		prev = lines[i]
		count = 1
	}
	flush()
	return out
}

func collapseExcessiveBlankLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	blanks := 0
	for _, line := range lines {
		if strings.TrimSpace(line) == "" {
			blanks++
			if blanks > 2 {
				continue
			}
		} else {
			blanks = 0
		}
		out = append(out, line)
	}
	return out
}

// FinalizeCommandOutput redacts secrets then optionally compresses for the agent.
func FinalizeCommandOutput(output string, opts CompressOptions) string {
	out := redactCombinedOutput(output)
	return CompressCommandOutput(out, opts)
}
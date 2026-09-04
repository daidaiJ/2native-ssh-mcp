package manager

import (
	"fmt"
	"strings"

	"2native-ssh-mcp/internal/config"
)

// Command status values. ok and exited are normal results (not MCP errors);
// the rest are surfaced as errors with their own codes.
const (
	StatusOK             = "ok"
	StatusExited         = "exited"
	StatusTimeout        = "timeout"
	StatusCancelled      = "cancelled"
	StatusConnectionLost = "connection_lost"
	StatusOutputLimit    = "output_limit"
)

// CommandResult is the structured outcome of a command execution. Stdout and
// Stderr are already redacted and light-compressed — or, when the output was
// spilled to a file, Stdout only carries a short preview and OutputFile
// points at the full copy on the local disk.
type CommandResult struct {
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr,omitempty"`
	ExitCode int    `json:"exitCode"` // -1 when unknown
	Signal   string `json:"signal,omitempty"`
	Status   string `json:"status"`
	// Partial is true when the command was interrupted (timeout, output
	// limit, connection loss) and Stdout/Stderr may be incomplete.
	Partial bool `json:"partial,omitempty"`
	// ReplaySafe is false when the command may have partially executed and
	// must not be blindly replayed.
	ReplaySafe bool `json:"replaySafe"`
	// CWD is the remote working directory captured from a named-session
	// shell after the command ran (empty for plain exec commands).
	CWD string `json:"cwd,omitempty"`
	// OutputFile is set when stdout+stderr reached outputSpillThreshold and
	// the full redacted output was written to this local file. The agent
	// should Read/Grep the file instead of re-running the command.
	OutputFile      string `json:"outputFile,omitempty"`
	OutputFileBytes int    `json:"outputFileBytes,omitempty"`
	OutputFileLines int    `json:"outputFileLines,omitempty"`
	// Truncated is true when output collection hit maxOutputBytes and the
	// command was aborted; ClippedBytes is how many bytes were discarded
	// after the cap (0/absent when unknown, e.g. shell-transport paths).
	Truncated    bool  `json:"truncated,omitempty"`
	ClippedBytes int64 `json:"clippedBytes,omitempty"`
}

// Text renders the human-readable body for a normal (ok/exited) result.
func (r CommandResult) Text() string {
	var sections []string
	if r.Stdout != "" {
		sections = append(sections, r.Stdout)
	}
	if r.Stderr != "" {
		sections = append(sections, "[stderr]\n"+r.Stderr)
	}
	if r.ExitCode >= 0 {
		sections = append(sections, fmt.Sprintf("[exit code] %d", r.ExitCode))
	}
	if r.Signal != "" {
		sections = append(sections, "[signal] "+r.Signal)
	}
	if r.OutputFile != "" {
		sections = append(sections, fmt.Sprintf(
			"[output] full output written to %s (%d bytes, %d lines) — use local Read/Grep on this file; do not re-run the command or cat it remotely",
			r.OutputFile, r.OutputFileBytes, r.OutputFileLines))
	}
	return strings.Join(sections, "\n")
}

// buildCommandResult strips ANSI escapes (unless disabled), redacts
// stdout/stderr and either spills them to a local file (at or above
// outputSpillThreshold) or light-compresses them, then wraps everything in a
// CommandResult. Interrupted statuses (timeout, output limit, connection
// lost, cancelled) are marked partial and not replay-safe.
func buildCommandResult(stdout, stderr string, exitCode int, status string, cfg *config.SSHConfig) CommandResult {
	if cfg == nil || cfg.GetStripAnsi() {
		stdout = stripANSI(stdout)
		stderr = stripANSI(stderr)
	}
	// Redaction is opt-in (redactSecrets): scanning secret-bearing output is
	// too expensive to run on every result by default.
	if cfg != nil && cfg.GetRedactSecrets() {
		stdout, stderr = redactCommandOutput(stdout, stderr)
	}
	replaySafe := status == StatusOK || status == StatusExited
	if info, spilled := spillCommandOutput(stdout, stderr, cfg); spilled {
		return CommandResult{
			Stdout:          spillPreview(stdout, stderr),
			ExitCode:        exitCode,
			Status:          status,
			Partial:         !replaySafe,
			ReplaySafe:      replaySafe,
			OutputFile:      info.path,
			OutputFileBytes: info.bytes,
			OutputFileLines: info.lines,
		}
	}
	return CommandResult{
		Stdout:     FinalizeCommandOutput(stdout, compressOpts(cfg)),
		Stderr:     FinalizeCommandOutput(stderr, compressOpts(cfg)),
		ExitCode:   exitCode,
		Status:     status,
		Partial:    !replaySafe,
		ReplaySafe: replaySafe,
	}
}

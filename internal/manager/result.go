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
// Stderr are already redacted and light-compressed.
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
	return strings.Join(sections, "\n")
}

// buildCommandResult redacts and light-compresses stdout/stderr and wraps
// them in a CommandResult. Interrupted statuses (timeout, output limit,
// connection lost, cancelled) are marked partial and not replay-safe.
func buildCommandResult(stdout, stderr string, exitCode int, status string, cfg *config.SSHConfig) CommandResult {
	stdout, stderr = redactCommandOutput(stdout, stderr)
	replaySafe := status == StatusOK || status == StatusExited
	return CommandResult{
		Stdout:     FinalizeCommandOutput(stdout, compressOpts(cfg)),
		Stderr:     FinalizeCommandOutput(stderr, compressOpts(cfg)),
		ExitCode:   exitCode,
		Status:     status,
		Partial:    !replaySafe,
		ReplaySafe: replaySafe,
	}
}

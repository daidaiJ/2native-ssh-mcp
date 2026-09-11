package manager

import (
	"fmt"
	"regexp"
	"strings"

	"2native-ssh-mcp/internal/config"
)

// historySparseEntries is the remote-entry count below which `history`
// output counts as too sparse and the command-log supplement kicks in.
const historySparseEntries = 10

// historySupplementEntries caps how many command-log entries the supplement
// considers.
const historySupplementEntries = 20

// historyCmdPattern matches a bare `history` or `history <n>` listing — the
// command whose output the supplement enriches. Flags (`-c`, `-w`, ...),
// pipes and other shell syntax never match: those change or consume history
// rather than display it.
var historyCmdPattern = regexp.MustCompile(`^\s*history(?:\s+[1-9]\d*)?\s*$`)

// historyNumberedLine strips the leading "  42  " sequence number that bash
// and zsh prefix to history listings (two or more spaces, matching the
// default format; HISTTIMEFORMAT timestamps defeat the match, in which case
// the full line counts as one entry — dedupe is best-effort by design).
var historyNumberedLine = regexp.MustCompile(`^\s*\d+\s{2,}(.*)$`)

// finalizeCommand records the executed command in the connection's command
// log and applies the history-log supplement when enabled. Shared tail of the
// exec (ExecuteCommand) and named-session (RunInSession) paths.
func (m *Manager) finalizeCommand(key string, cfg *config.SSHConfig, cmdString string, result CommandResult, err error) (CommandResult, error) {
	m.RecordCommand(key, cmdString, result.ExitCode, result.ExitCode == 0)
	if err == nil && cfg != nil && cfg.GetHistoryFromLog() {
		m.enrichHistoryOutput(key, cmdString, &result)
	}
	return result, err
}

// enrichHistoryOutput appends command-log entries to sparse remote `history`
// output (opt-in per connection via historyFromLog). Exec-mode commands run
// in a fresh non-interactive shell where the history builtin prints nothing,
// and small HISTSIZE settings truncate interactive sessions; the MCP command
// log covers both. Entries already visible remotely are not repeated, and
// within the supplement the most recent occurrence wins (tail-dedupe).
func (m *Manager) enrichHistoryOutput(key, cmdString string, result *CommandResult) {
	if !historyCmdPattern.MatchString(cmdString) {
		return
	}
	// A spilled, clipped or interrupted result no longer holds the raw
	// listing; mixing a supplement into it would mislead.
	if result.OutputFile != "" || result.Truncated || result.Partial {
		return
	}
	remote := parseHistoryEntries(result.Stdout)
	if len(remote) >= historySparseEntries {
		return
	}
	supplement := tailDedupe(remote, m.RecentCommands(key, historySupplementEntries), cmdString)
	if len(supplement) == 0 {
		return
	}
	var b strings.Builder
	b.WriteString(result.Stdout)
	if result.Stdout != "" && !strings.HasSuffix(result.Stdout, "\n") {
		b.WriteString("\n")
	}
	b.WriteString("# --- supplemented by 2native-ssh-mcp from its command log (tail-deduped) ---\n")
	for _, cmd := range supplement {
		fmt.Fprintf(&b, "  -  %s\n", cmd)
	}
	result.Stdout = b.String()
	result.HistorySupplemented = len(supplement)
}

// parseHistoryEntries extracts command texts from a `history` listing.
// Numbered lines contribute the command part; any other non-blank,
// non-comment line counts as one entry with its full text.
func parseHistoryEntries(out string) []string {
	var entries []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if m := historyNumberedLine.FindStringSubmatch(line); m != nil {
			entries = append(entries, strings.TrimSpace(m[1]))
		} else {
			entries = append(entries, line)
		}
	}
	return entries
}

// tailDedupe returns the command-log entries worth appending: not the current
// command, not already visible in the remote listing, and duplicates reduced
// to their most recent occurrence (the tail wins) while keeping the overall
// order oldest-first.
func tailDedupe(remoteCmds []string, logEntries []CommandLogEntry, currentCmd string) []string {
	remote := make(map[string]bool, len(remoteCmds))
	for _, c := range remoteCmds {
		remote[c] = true
	}
	filtered := make([]string, 0, len(logEntries))
	for _, e := range logEntries {
		c := strings.TrimSpace(e.Command)
		if c == "" || c == currentCmd || remote[c] {
			continue
		}
		filtered = append(filtered, c)
	}
	seen := make(map[string]bool, len(filtered))
	out := make([]string, 0, len(filtered))
	for i := len(filtered) - 1; i >= 0; i-- {
		if seen[filtered[i]] {
			continue
		}
		seen[filtered[i]] = true
		out = append(out, filtered[i])
	}
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out
}

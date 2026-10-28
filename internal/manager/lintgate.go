package manager

import (
	"fmt"
	"strings"

	"2native-ssh-mcp/internal/config"
)

// LintModeFor resolves the effective lint mode for one call: the per-call
// override wins, then the connection configuration, then the default (warn).
// An override that is not one of off/warn/strict is ignored (the tool schema
// only lets the three values through).
func LintModeFor(cfg *config.SSHConfig, override string) string {
	switch strings.ToLower(strings.TrimSpace(override)) {
	case config.LintOff, config.LintWarn, config.LintStrict:
		return strings.ToLower(strings.TrimSpace(override))
	}
	return cfg.GetLintMode()
}

// checkCommandLint runs the structural lint over a shell command before it is
// sent to the remote host.
//
// off: nothing runs. warn: findings are returned for the result and the
// command runs anyway. strict: error-class findings refuse the command with
// COMMAND_LINT_FAILED (warn-class findings are still returned and the command
// still runs).
//
// Only shell-mode commands are linted. argv mode is already byte-exact (every
// argument is quoted by the server, so no shell syntax is involved), and a
// staged scriptFile is parsed by the remote interpreter itself, which reports
// its own syntax errors with a line number — linting it would duplicate that
// less precisely.
func (m *Manager) checkCommandLint(cmdString, mode string) ([]string, error) {
	if mode == config.LintOff {
		return nil, nil
	}
	findings := LintShell(cmdString)
	if len(findings) == 0 {
		return nil, nil
	}
	if mode == config.LintStrict {
		if errs := LintErrors(findings); len(errs) > 0 {
			return nil, newToolError(CodeCommandLintFailed,
				fmt.Sprintf("Command lint failed (lintMode=strict): %s. Fix the command, or switch this call or this connection to lintMode=warn (report and run) or lintMode=off.",
					strings.ReplaceAll(FormatLint(errs), "\n", " | ")), false)
		}
	}
	return lintLines(findings), nil
}

// lintLines renders findings as the one-line-each strings carried in a
// CommandResult.
func lintLines(findings []LintFinding) []string {
	out := make([]string, 0, len(findings))
	for _, f := range findings {
		out = append(out, fmt.Sprintf("[%s] %s: %s", f.Kind, f.Code, f.Msg))
	}
	return out
}

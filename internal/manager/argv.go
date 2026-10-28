package manager

import "strings"

// auditTextFor picks the text that policy checks and the audit record use:
// the caller's readable equivalent when there is one, otherwise the executed
// command itself (already readable).
func auditTextFor(executed, equivalent string) string {
	if strings.TrimSpace(equivalent) != "" {
		return equivalent
	}
	return executed
}

// JoinArgv renders an argv-style invocation in two forms.
//
// execText is the shell command that runs remotely: every element is
// single-quoted verbatim, so nothing is re-split, glob-expanded or
// substituted by the remote shell.
//
// auditText is the equivalent command line a human reads: the same arguments
// joined with minimal quoting (an argument is quoted only where a shell would
// change it). It is what the whitelist/blacklist check, the destructive
// classifier, the approval prompt and the audit record see, so the audit trail
// holds a command a reviewer can re-run by hand instead of a wall of quotes.
func JoinArgv(program string, args []string) (execText, auditText string, err error) {
	argv := make([]string, 0, len(args)+1)
	argv = append(argv, program)
	argv = append(argv, args...)
	for _, a := range argv {
		if strings.ContainsRune(a, 0) {
			return "", "", newToolError(CodeCommandValidationFailed,
				"args must not contain null bytes", false)
		}
	}
	quoted := make([]string, len(argv))
	readable := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = shellQuote(a)
		readable[i] = shellQuoteMinimal(a)
	}
	return strings.Join(quoted, " "), strings.Join(readable, " "), nil
}

// shellQuoteMinimal quotes an argument only when the bare form would be
// changed by a shell (word splitting, globbing, expansion, redirection).
func shellQuoteMinimal(value string) string {
	if isPlainShellWord(value) {
		return value
	}
	return shellQuote(value)
}

// isPlainShellWord reports whether s survives a trip through a POSIX shell
// unchanged when left unquoted: non-empty and free of the characters that
// trigger word splitting, globbing, expansion or redirection. The point is to
// keep the audit line close to what the caller typed.
func isPlainShellWord(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case strings.IndexByte("-_@%+=:,./", c) >= 0:
		default:
			return false
		}
	}
	return true
}

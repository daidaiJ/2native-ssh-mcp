package manager

import (
	"regexp"
	"strings"
)

var (
	bearerTokenPattern = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\-._~+/]+=*`)
	pemBlockPattern    = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`)
	passwordKVPattern  = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key)\s*[:=]\s*\S+`)
)

const redactedPlaceholder = "[REDACTED]"

// RedactSensitiveOutput masks common secret patterns before returning command
// output to the MCP client.
func RedactSensitiveOutput(s string) string {
	if s == "" {
		return s
	}
	out := bearerTokenPattern.ReplaceAllString(s, "${1}"+redactedPlaceholder)
	out = pemBlockPattern.ReplaceAllString(out, "[REDACTED PEM BLOCK]")
	out = passwordKVPattern.ReplaceAllString(out, "${1}="+redactedPlaceholder)
	return out
}

// redactCommandOutput applies redaction to stdout/stderr sections.
func redactCommandOutput(stdout, stderr string) (string, string) {
	return RedactSensitiveOutput(stdout), RedactSensitiveOutput(stderr)
}

func redactCombinedOutput(output string) string {
	if !strings.Contains(output, "[stderr]\n") {
		return RedactSensitiveOutput(output)
	}
	parts := strings.SplitN(output, "[stderr]\n", 2)
	return RedactSensitiveOutput(parts[0]) + "[stderr]\n" + RedactSensitiveOutput(parts[1])
}

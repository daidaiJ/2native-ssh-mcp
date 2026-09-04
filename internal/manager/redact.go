package manager

import (
	"regexp"
	"strings"
)

var (
	bearerTokenPattern = regexp.MustCompile(`(?i)(Bearer\s+)[A-Za-z0-9\-._~+/]+=*`)
	pemBlockPattern    = regexp.MustCompile(`-----BEGIN [A-Z ]+-----[\s\S]*?-----END [A-Z ]+-----`)
	// passwordKVPattern is kept only as the semantic reference for the
	// hand-rolled redactKV scanner and its differential test.
	passwordKVPattern = regexp.MustCompile(`(?i)(password|passwd|pwd|secret|token|api[_-]?key)\s*[:=]\s*\S+`)
)

const redactedPlaceholder = "[REDACTED]"

// Redaction is opt-in (config redactSecrets) because scanning secret-bearing
// output is inherently expensive. When it is enabled, a cheap single-pass
// anchor pre-scan gates each regex so a pattern only runs when its own anchor
// occurred, and the hottest pattern (keyword=value) uses a byte scanner that
// is byte-for-byte equivalent to the regex but ~30x faster.

// Anchor classes. Any match of a redaction pattern must contain the class
// anchor (compared ASCII-case-insensitively — a superset of the case-sensitive
// PEM pattern), so output that trips no anchor cannot hold a secret.
const (
	anchorBearer = 1 << iota
	anchorPEM
	anchorKV
	anchorAny = anchorBearer | anchorPEM | anchorKV
)

var (
	bearerAnchors = []string{"bearer"}
	pemAnchors    = []string{"begin "}
	kvAnchors     = []string{"password", "passwd", "pwd", "secret", "token", "apikey", "api_key", "api-key"}
	// kvKeywords mirrors the passwordKVPattern alternation order; at any
	// position the keywords are mutually prefix-exclusive, so first match
	// wins exactly like the regex alternation.
	kvKeywords = []string{"password", "passwd", "pwd", "secret", "token", "apikey", "api_key", "api-key"}
)

var anchorFirstMask = func() [256]byte {
	var t [256]byte
	for _, a := range bearerAnchors {
		t[asciiFold(a[0])] |= anchorBearer
	}
	for _, a := range pemAnchors {
		t[asciiFold(a[0])] |= anchorPEM
	}
	for _, a := range kvAnchors {
		t[asciiFold(a[0])] |= anchorKV
	}
	return t
}()

func asciiFold(b byte) byte {
	if b >= 'A' && b <= 'Z' {
		return b | 0x20
	}
	return b
}

func asciiEqualFold(s, target string) bool {
	if len(s) != len(target) {
		return false
	}
	for i := 0; i < len(s); i++ {
		if asciiFold(s[i]) != asciiFold(target[i]) {
			return false
		}
	}
	return true
}

func asciiHasAnyFold(s string, i int, anchors []string) bool {
	for _, a := range anchors {
		if i+len(a) <= len(s) && asciiEqualFold(s[i:i+len(a)], a) {
			return true
		}
	}
	return false
}

// scanSecretAnchors reports which redaction anchor classes occur in s with a
// single linear pass. It is a superset check: a clear class guarantees its
// regex cannot match, so the expensive scan is skipped for that pattern.
func scanSecretAnchors(s string) int {
	mask := 0
	for i := 0; i < len(s); i++ {
		m := anchorFirstMask[asciiFold(s[i])]
		if m == 0 {
			continue
		}
		if m&anchorBearer != 0 && asciiHasAnyFold(s, i, bearerAnchors) {
			mask |= anchorBearer
		}
		if m&anchorPEM != 0 && asciiHasAnyFold(s, i, pemAnchors) {
			mask |= anchorPEM
		}
		if m&anchorKV != 0 && asciiHasAnyFold(s, i, kvAnchors) {
			mask |= anchorKV
		}
		if mask == anchorAny {
			return mask
		}
	}
	return mask
}

// outputMayContainSecrets is a superset check: false guarantees that no
// redaction regex could match.
func outputMayContainSecrets(s string) bool {
	return scanSecretAnchors(s) != 0
}

// RedactSensitiveOutput masks common secret patterns before returning command
// output to the MCP client.
func RedactSensitiveOutput(s string) string {
	if s == "" {
		return s
	}
	anchors := scanSecretAnchors(s)
	if anchors == 0 {
		return s
	}
	out := s
	if anchors&anchorBearer != 0 {
		out = bearerTokenPattern.ReplaceAllString(out, "${1}"+redactedPlaceholder)
	}
	if anchors&anchorPEM != 0 {
		out = pemBlockPattern.ReplaceAllString(out, "[REDACTED PEM BLOCK]")
	}
	if anchors&anchorKV != 0 {
		out = redactKV(out)
	}
	return out
}

// isRegexSpace matches the Go regexp \s class: [\t\n\f\r ] (ASCII only).
func isRegexSpace(c byte) bool {
	switch c {
	case '\t', '\n', '\f', '\r', ' ':
		return true
	}
	return false
}

// redactKV rewrites keyword<space>*[:=]<space>*value runs to
// "keyword=[REDACTED]", byte-for-byte equivalent to
// passwordKVPattern.ReplaceAllString(s, "${1}="+redactedPlaceholder) for
// ASCII input (guarded by a differential test). Accepted divergence: the
// regex's (?i) also folds a few non-ASCII letters (e.g. the Kelvin sign for
// 'k'), which the ASCII-only scanner does not match — irrelevant for real
// credentials.
func redactKV(s string) string {
	var b strings.Builder
	wrote := 0
	for i := 0; i < len(s); {
		if anchorFirstMask[asciiFold(s[i])]&anchorKV == 0 || !asciiHasAnyFold(s, i, kvAnchors) {
			i++
			continue
		}
		klen := 0
		rest := s[i:]
		for _, kw := range kvKeywords {
			if len(rest) >= len(kw) && asciiEqualFold(rest[:len(kw)], kw) {
				klen = len(kw)
				break
			}
		}
		if klen == 0 {
			i++
			continue
		}
		j := i + klen
		for j < len(s) && isRegexSpace(s[j]) {
			j++
		}
		if j >= len(s) || (s[j] != ':' && s[j] != '=') {
			i++
			continue
		}
		j++ // the [:=]
		for j < len(s) && isRegexSpace(s[j]) {
			j++
		}
		valueStart := j
		for j < len(s) && !isRegexSpace(s[j]) {
			j++
		}
		if j == valueStart {
			i++ // \S+ needs at least one byte; no value here
			continue
		}
		if wrote == 0 {
			b.Grow(len(s))
		}
		b.WriteString(s[wrote:i])
		b.WriteString(s[i : i+klen])
		b.WriteByte('=')
		b.WriteString(redactedPlaceholder)
		wrote = j
		i = j
	}
	if wrote == 0 {
		return s
	}
	b.WriteString(s[wrote:])
	return b.String()
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

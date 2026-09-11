package manager

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// sanitizeInvalidUTF8 replaces invalid UTF-8 sequences with \xNN escapes of
// the original bytes, keeping the output valid UTF-8 for JSON transport while
// preserving the raw bytes for later decoding. Remote output is never assumed
// to be UTF-8: GBK/Big5/latin-1 text arrives as invalid sequences and becomes
// visible escapes instead of silently degrading into U+FFFD when marshalled.
// Returns the string (unchanged when already valid) and whether any bytes
// were replaced.
func sanitizeInvalidUTF8(s string) (string, bool) {
	if utf8.ValidString(s) {
		return s, false
	}
	var b strings.Builder
	b.Grow(len(s) + 16)
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		if r == utf8.RuneError && size <= 1 {
			fmt.Fprintf(&b, "\\x%02x", s[i])
			i++
			continue
		}
		b.WriteString(s[i : i+size])
		i += size
	}
	return b.String(), true
}

// utf8SafeTruncate cuts s to at most max bytes without splitting a multi-byte
// rune at the boundary. Bytes that are not part of a valid sequence are left
// untouched (garbage in, garbage out — never made worse).
func utf8SafeTruncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	start := max - utf8.UTFMax
	if start < 0 {
		start = 0
	}
	for i := max - 1; i >= start; i-- {
		b := s[i]
		if b < 0x80 {
			break // ASCII boundary: nothing is split
		}
		if b >= 0xC0 {
			// Rune start byte: if the rune extends past cut, cut before it.
			if i+utf8RuneLen(b) > max {
				cut = i
			}
			break
		}
	}
	return s[:cut]
}

// utf8SafeCutLen reports the length to keep of a byte slice being truncated
// to max bytes so the boundary does not split a multi-byte rune.
func utf8SafeCutLen(p []byte, max int) int {
	if max >= len(p) {
		return len(p)
	}
	cut := max
	start := max - utf8.UTFMax
	if start < 0 {
		start = 0
	}
	for i := max - 1; i >= start; i-- {
		b := p[i]
		if b < 0x80 {
			break
		}
		if b >= 0xC0 {
			if i+utf8RuneLen(b) > max {
				cut = i
			}
			break
		}
	}
	return cut
}

func utf8RuneLen(b byte) int {
	switch {
	case b&0xE0 == 0xC0:
		return 2
	case b&0xF0 == 0xE0:
		return 3
	default:
		return 4
	}
}

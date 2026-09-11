package manager

import (
	"math/rand"
	"testing"
)

// ansiStripOracle is the regex pipeline stripANSI must stay byte-for-byte
// equivalent to (the ansiOSCPattern/ansiCSIPattern/ansiCharsetPattern vars
// remain defined as the semantic reference).
func ansiStripOracle(s string) string {
	s = ansiOSCPattern.ReplaceAllString(s, "")
	s = ansiCSIPattern.ReplaceAllString(s, "")
	return ansiCharsetPattern.ReplaceAllString(s, "")
}

// TestStripANSIDifferential fuzzes constructed and random byte strings —
// including stacked ESC bytes whose removal can splice a new sequence into
// existence across the boundary — against the regex oracle.
func TestStripANSIDifferential(t *testing.T) {
	pieces := []string{
		"\x1b", "\x1b[", "\x1b]0;title\x07", "\x1b[32m", "\x1b[0m", "\x1b(B", "\x1b(0",
		"\x1b[31;1m", "\x1b]8;;http://x\x1b\\", "[31m", "a", "0", "(", "]", ";", " ",
		"\x07", "\x1bQ", "\x1b[Q", "\x1b]x",
	}
	rng := rand.New(rand.NewSource(42))
	for iter := 0; iter < 20000; iter++ {
		var s string
		for i, n := 0, 1+rng.Intn(12); i < n; i++ {
			s += pieces[rng.Intn(len(pieces))]
		}
		if got, want := stripANSI(s), ansiStripOracle(s); got != want {
			t.Fatalf("stripANSI(%q) = %q, regex oracle = %q", s, got, want)
		}
	}
	for iter := 0; iter < 20000; iter++ {
		b := make([]byte, rng.Intn(24))
		for i := range b {
			switch rng.Intn(4) {
			case 0:
				b[i] = 0x1b
			case 1:
				b[i] = byte(rng.Intn(0x20))
			default:
				b[i] = byte(0x20 + rng.Intn(0x60))
			}
		}
		s := string(b)
		if got, want := stripANSI(s), ansiStripOracle(s); got != want {
			t.Fatalf("stripANSI(%q) = %q, regex oracle = %q", s, got, want)
		}
	}
}

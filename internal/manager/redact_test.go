package manager

import (
	"math/rand"
	"strings"
	"testing"
)

func TestRedactSensitiveOutput(t *testing.T) {
	tests := []struct {
		in, wantSub, wantNot string
	}{
		{
			in:      "Authorization: Bearer eyJhbGciOiJIUzI1NiJ9.abc123",
			wantSub: "Bearer [REDACTED]",
			wantNot: "abc123",
		},
		{
			in:      "password=SuperSecret123!",
			wantSub: "password=[REDACTED]",
			wantNot: "SuperSecret123",
		},
		{
			in: `-----BEGIN RSA PRIVATE KEY-----
MIIEpAIBAAKCAQEA...
-----END RSA PRIVATE KEY-----`,
			wantSub: "[REDACTED PEM BLOCK]",
			wantNot: "MIIEpAIBAAKCAQEA",
		},
		{
			in:      "plain output with no secrets",
			wantSub: "plain output with no secrets",
		},
	}
	for _, tt := range tests {
		got := RedactSensitiveOutput(tt.in)
		if tt.wantSub != "" && !strings.Contains(got, tt.wantSub) {
			t.Fatalf("RedactSensitiveOutput(%q) = %q, want substring %q", tt.in, got, tt.wantSub)
		}
		if tt.wantNot != "" && strings.Contains(got, tt.wantNot) {
			t.Fatalf("RedactSensitiveOutput(%q) = %q, must not contain %q", tt.in, got, tt.wantNot)
		}
	}
}

// TestRedactFastPathAnchors guards the anchor pre-scan: every output that the
// regexes would redact must trip the fast path too (no false negatives), and
// plain output must skip the regexes entirely.
func TestRedactFastPathAnchors(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantMatch bool   // the full regex path must run
		wantRedact string // substring expected after redaction ("" = unchanged)
	}{
		{"plain", "just regular log output\nwith lines", false, ""},
		{"lower kv", "password=hunter2", true, "password=[REDACTED]"},
		{"upper kv", "PASSWORD=hunter2", true, "PASSWORD=[REDACTED]"},
		{"mixed kv", "PaSsWoRd = hunter2", true, "PaSsWoRd=[REDACTED]"},
		{"passwd", "passwd: s3cret", true, "passwd=[REDACTED]"},
		{"pwd", "pwd=shhh", true, "pwd=[REDACTED]"},
		{"secret", "secret=value", true, "secret=[REDACTED]"},
		{"token", "token=abc", true, "token=[REDACTED]"},
		{"bearer", "Authorization: Bearer abc.def.ghi", true, "Bearer [REDACTED]"},
		{"lower bearer", "authorization: bearer abc", true, "bearer [REDACTED]"},
		{"api_key", "api_key=xyz", true, "api_key=[REDACTED]"},
		{"api-key upper", "API-KEY: xyz", true, "API-KEY=[REDACTED]"},
		{"apikey", "apikey=xyz", true, "apikey=[REDACTED]"},
		{"pem", "-----BEGIN RSA PRIVATE KEY-----\nabc\n-----END RSA PRIVATE KEY-----", true, "[REDACTED PEM BLOCK]"},
		{"keyword no separator", "the password is strong", true, ""},
		{"utf8 around", "日志内容 token=abc 中文输出", true, "token=[REDACTED]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := outputMayContainSecrets(tc.in); got != tc.wantMatch {
				t.Fatalf("outputMayContainSecrets=%v, want %v", got, tc.wantMatch)
			}
			got := RedactSensitiveOutput(tc.in)
			if tc.wantRedact == "" {
				if got != tc.in {
					t.Fatalf("output must be unchanged, got %q", got)
				}
				return
			}
			if !strings.Contains(got, tc.wantRedact) {
				t.Fatalf("expected redaction %q, got %q", tc.wantRedact, got)
			}
		})
	}
}

// TestRedactKVDifferential verifies the hand-rolled redactKV scanner is
// byte-for-byte equivalent to the reference regex on an adversarial corpus
// (fixed seed), in addition to the hand-picked cases above.
func TestRedactKVDifferential(t *testing.T) {
	ref := func(s string) string {
		return passwordKVPattern.ReplaceAllString(s, "${1}="+redactedPlaceholder)
	}
	keywords := []string{"password", "passwd", "pwd", "secret", "token", "apikey", "api_key", "api-key"}
	caseMutate := []func(string) string{
		func(s string) string { return s },
		strings.ToUpper,
		func(s string) string { return strings.ToUpper(s[:1]) + s[1:] },
		func(s string) string {
			if len(s) < 5 {
				return strings.ToUpper(s)
			}
			return s[:2] + strings.ToUpper(s[2:4]) + s[4:]
		},
	}
	seps := []string{"=", ":", " =", ":", "\t:", "\n=", " =\r\n", "= ", ":  "}
	values := []string{"abc", "x", "12345", "a=b", "v-w.x~y", "密", "p=[REDACTED]", ""}
	filler := []string{"log line\n", "plain text ", "\x1b[31mansi\x1b[0m", "---- ", "…", "p", "s", "t", "a"}

	rng := rand.New(rand.NewSource(20260904))
	for iter := 0; iter < 2000; iter++ {
		var sb strings.Builder
		for i := 0; i < 1+rng.Intn(12); i++ {
			switch rng.Intn(4) {
			case 0:
				kw := keywords[rng.Intn(len(keywords))]
				sb.WriteString(caseMutate[rng.Intn(len(caseMutate))](kw))
				sb.WriteString(seps[rng.Intn(len(seps))])
				sb.WriteString(values[rng.Intn(len(values))])
			case 1:
				sb.WriteString(filler[rng.Intn(len(filler))])
			case 2:
				sb.WriteString(caseMutate[rng.Intn(len(caseMutate))](keywords[rng.Intn(len(keywords))]))
			case 3:
				sb.WriteString(values[rng.Intn(len(values))])
				sb.WriteString(seps[rng.Intn(len(seps))])
			}
		}
		in := sb.String()
		got, want := redactKV(in), ref(in)
		if got != want {
			t.Fatalf("mismatch on %q:\n scanner: %q\n regex:   %q", in, got, want)
		}
	}
}

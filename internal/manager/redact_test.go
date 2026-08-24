package manager

import (
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

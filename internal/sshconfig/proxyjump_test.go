package sshconfig

import (
	"strings"
	"testing"
)

func TestLookupProxyJump(t *testing.T) {
	path := writeConfig(t, `
Host internal
    HostName 10.0.1.50
    User dbadmin
    ProxyJump bastion
`)
	entry, err := Lookup("internal", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected entry for internal")
	}
	if entry.ProxyJump != "bastion" {
		t.Fatalf("ProxyJump must resolve verbatim, got %q", entry.ProxyJump)
	}
	if entry.HostName != "10.0.1.50" || entry.User != "dbadmin" {
		t.Fatalf("unexpected entry: %+v", entry)
	}
}

// A Host block carrying only ProxyJump is still a meaningful entry.
func TestLookupProxyJumpOnlyBlock(t *testing.T) {
	path := writeConfig(t, `
Host internal
    ProxyJump bastion
`)
	entry, err := Lookup("internal", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.ProxyJump != "bastion" {
		t.Fatalf("expected ProxyJump-only entry, got %+v", entry)
	}
}

func TestLookupProxyJumpMultiHopRejected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   string
		wantErr string
	}{
		{"comma list", "bastion,bastion2", "multi-hop"},
		{"space list", "bastion bastion2", "multi-hop"},
		{"user@host", "deploy@bastion.example.com", "user@host"},
		{"host:port", "bastion.example.com:2222", "host:port"},
		{"none", "none", "none"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := writeConfig(t, `
Host internal
    HostName 10.0.1.50
    ProxyJump `+tc.value+`
`)
			entry, err := Lookup("internal", path)
			if err == nil {
				t.Fatalf("ProxyJump %q must be rejected, got %+v", tc.value, entry)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error should mention %q, got %v", tc.wantErr, err)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("error must carry the raw value, got %v", err)
			}
		})
	}
}

func TestValidateProxyJumpAcceptsPlainAlias(t *testing.T) {
	if err := validateProxyJump("bastion"); err != nil {
		t.Fatalf("plain alias must pass: %v", err)
	}
}

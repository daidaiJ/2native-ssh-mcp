package config

import (
	"strings"
	"testing"
)

func baseViaConfig(name, via string) *SSHConfig {
	return &SSHConfig{Name: name, Host: "h", Username: "u", Via: via}
}

func TestValidateViasAcceptsSingleHop(t *testing.T) {
	configs := map[string]*SSHConfig{
		"bastion":     baseViaConfig("bastion", ""),
		"internal-db": baseViaConfig("internal-db", "bastion"),
	}
	if err := ValidateVias(configs); err != nil {
		t.Fatalf("legal single hop must pass: %v", err)
	}
}

// The jump host itself may keep its proxy: the tunnel only replaces the
// target's TCP, the outer hop owns the proxy semantics.
func TestValidateViasJumpHostMayKeepProxy(t *testing.T) {
	bastion := baseViaConfig("bastion", "")
	bastion.Proxy = "socks5://127.0.0.1:1080"
	configs := map[string]*SSHConfig{
		"bastion":     bastion,
		"internal-db": baseViaConfig("internal-db", "bastion"),
	}
	if err := ValidateVias(configs); err != nil {
		t.Fatalf("jump host proxy must be allowed: %v", err)
	}
}

func TestValidateViasTable(t *testing.T) {
	cases := []struct {
		name    string
		configs map[string]*SSHConfig
		wantErr string
	}{
		{
			"unknown via",
			map[string]*SSHConfig{
				"bastion":     baseViaConfig("bastion", ""),
				"internal-db": baseViaConfig("internal-db", "gateway"),
			},
			"unknown connection 'gateway'",
		},
		{
			"self reference",
			map[string]*SSHConfig{"loop": baseViaConfig("loop", "loop")},
			"not itself",
		},
		{
			"two-node cycle",
			map[string]*SSHConfig{
				"a": baseViaConfig("a", "b"),
				"b": baseViaConfig("b", "a"),
			},
			"a -> b -> a",
		},
		{
			"three-node cycle",
			map[string]*SSHConfig{
				"a": baseViaConfig("a", "b"),
				"b": baseViaConfig("b", "c"),
				"c": baseViaConfig("c", "a"),
			},
			"a -> b -> c -> a",
		},
		{
			"multi-hop comma",
			map[string]*SSHConfig{
				"bastion": baseViaConfig("bastion", ""),
				"target":  baseViaConfig("target", "bastion,bastion2"),
			},
			"multi-hop",
		},
		{
			"multi-hop space",
			map[string]*SSHConfig{
				"bastion": baseViaConfig("bastion", ""),
				"target":  baseViaConfig("target", "bastion bastion2"),
			},
			"multi-hop",
		},
		{
			"via with proxy",
			map[string]*SSHConfig{
				"bastion": baseViaConfig("bastion", ""),
				"target":  withProxy(baseViaConfig("target", "bastion"), "proxy", "socks5://127.0.0.1:1080"),
			},
			"mutually exclusive",
		},
		{
			"via with legacy socksProxy",
			map[string]*SSHConfig{
				"bastion": baseViaConfig("bastion", ""),
				"target":  withProxy(baseViaConfig("target", "bastion"), "socksProxy", "socks5://127.0.0.1:1080"),
			},
			"mutually exclusive",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateVias(tc.configs)
			if err == nil {
				t.Fatal("expected validation failure")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error should mention %q, got %v", tc.wantErr, err)
			}
		})
	}
}

func withProxy(cfg *SSHConfig, field, value string) *SSHConfig {
	switch field {
	case "proxy":
		cfg.Proxy = value
	case "socksProxy":
		cfg.SocksProxy = value
	}
	return cfg
}

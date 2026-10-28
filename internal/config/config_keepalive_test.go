package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeKeepAliveConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(newConfigDir(t), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestKeepAliveDefault(t *testing.T) {
	path := writeKeepAliveConfig(t, `{
		"$global": {"allowInsecureConfigPerms": true},
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].GetKeepAliveMs(); got != DefaultKeepAliveMs {
		t.Fatalf("expected the default keep-alive window %d, got %d", DefaultKeepAliveMs, got)
	}
}

func TestKeepAliveGlobalDefaultAndOverride(t *testing.T) {
	path := writeKeepAliveConfig(t, `{
		"$global": {"allowInsecureConfigPerms": true, "keepAliveMs": 120000},
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"},
		"close": {"host": "10.0.0.2", "port": 22, "username": "root", "password": "x", "keepAliveMs": -1}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].GetKeepAliveMs(); got != 120000 {
		t.Fatalf("expected $global.keepAliveMs to apply to connections, got %d", got)
	}
	if got := opts.Configs["close"].GetKeepAliveMs(); got != -1 {
		t.Fatalf("expected connection-level keepAliveMs=-1 to win over $global, got %d", got)
	}
}

func TestKeepAliveValidation(t *testing.T) {
	cases := []struct {
		name    string
		value   string
		want    int
		wantErr string
	}{
		{"close after each command", `-1`, -1, ""},
		{"explicit window", `5000`, 5000, ""},
		{"below the disable sentinel", `-2`, 0, "keepAliveMs must be -1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeKeepAliveConfig(t, `{
				"$global": {"allowInsecureConfigPerms": true},
				"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x", "keepAliveMs": `+tc.value+`}
			}`)
			opts, err := ParseArgs([]string{"--config-file", path})
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("expected error containing %q, got %v", tc.wantErr, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseArgs failed: %v", err)
			}
			if got := opts.Configs["srv"].GetKeepAliveMs(); got != tc.want {
				t.Fatalf("expected keepAliveMs %d, got %d", tc.want, got)
			}
		})
	}
}

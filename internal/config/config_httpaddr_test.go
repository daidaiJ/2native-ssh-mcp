package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeHTTPAddrConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(newConfigDir(t), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestHTTPAddrFromConfigFile(t *testing.T) {
	path := writeHTTPAddrConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true, "httpAddr": "127.0.0.1:8339"},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opts.HTTPAddr != "127.0.0.1:8339" {
		t.Fatalf("expected $global.httpAddr to win over the default, got %q", opts.HTTPAddr)
	}
}

func TestHTTPAddrFallsBackToDefault(t *testing.T) {
	path := writeHTTPAddrConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opts.HTTPAddr != DefaultHTTPAddr {
		t.Fatalf("expected DefaultHTTPAddr %q when the config names no address, got %q", DefaultHTTPAddr, opts.HTTPAddr)
	}

	// No config file at all: same fallback.
	opts, err = ParseArgs([]string{"--ssh", `{"name":"srv","host":"10.0.0.1","port":22,"username":"root","password":"x"}`})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opts.HTTPAddr != DefaultHTTPAddr {
		t.Fatalf("expected DefaultHTTPAddr %q without a config file, got %q", DefaultHTTPAddr, opts.HTTPAddr)
	}
}

func TestHTTPAddrFlagWinsOverConfigFile(t *testing.T) {
	path := writeHTTPAddrConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true, "httpAddr": "127.0.0.1:8339"},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
}`)
	opts, err := ParseArgs([]string{"--config-file", path, "--http-addr", "0.0.0.0:9000"})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if opts.HTTPAddr != "0.0.0.0:9000" {
		t.Fatalf("expected --http-addr to win over $global.httpAddr, got %q", opts.HTTPAddr)
	}
}

func TestHTTPAddrValidation(t *testing.T) {
	cases := []struct {
		name  string
		value string
		want  string
	}{
		{"missing port", `"127.0.0.1"`, "$global.httpAddr"},
		{"non-numeric port", `"127.0.0.1:http"`, "is not a number"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			path := writeHTTPAddrConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true, "httpAddr": `+c.value+`},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
}`)
			_, err := ParseArgs([]string{"--config-file", path})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("expected an error mentioning %q, got %v", c.want, err)
			}
		})
	}
}

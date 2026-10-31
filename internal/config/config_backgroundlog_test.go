package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeBackgroundLogConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(newConfigDir(t), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestBackgroundLogCapDefault(t *testing.T) {
	path := writeBackgroundLogConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].GetBackgroundLogMaxBytes(); got != DefaultBackgroundLogMaxBytes {
		t.Fatalf("expected the default cap %d, got %d", DefaultBackgroundLogMaxBytes, got)
	}
}

func TestBackgroundLogCapGlobalDefaultAndOverride(t *testing.T) {
	path := writeBackgroundLogConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true, "backgroundLogMaxBytes": 262144},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"},
  "all": {"host": "10.0.0.2", "port": 22, "username": "root", "password": "x", "backgroundLogMaxBytes": -1}
}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].GetBackgroundLogMaxBytes(); got != 262144 {
		t.Fatalf("expected $global.backgroundLogMaxBytes to apply, got %d", got)
	}
	if got := opts.Configs["all"].GetBackgroundLogMaxBytes(); got != -1 {
		t.Fatalf("expected the connection value to win over $global, got %d", got)
	}
}

func TestBackgroundLogCapValidation(t *testing.T) {
	path := writeBackgroundLogConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x", "backgroundLogMaxBytes": -2}
}`)
	if _, err := ParseArgs([]string{"--config-file", path}); err == nil {
		t.Fatal("expected backgroundLogMaxBytes=-2 to be rejected")
	}
}

func TestResultFormatGlobalDefaultAndOverride(t *testing.T) {
	path := writeBackgroundLogConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true, "resultFormat": "json"},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"},
  "plain": {"host": "10.0.0.2", "port": 22, "username": "root", "password": "x", "resultFormat": "text"}
}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].GetResultFormat(); got != ResultFormatJSON {
		t.Fatalf("expected $global.resultFormat to apply, got %q", got)
	}
	if got := opts.Configs["plain"].GetResultFormat(); got != ResultFormatText {
		t.Fatalf("expected the connection value to win over $global, got %q", got)
	}
}

func TestResultFormatValidation(t *testing.T) {
	path := writeBackgroundLogConfig(t, `{
  "$global": {"allowInsecureConfigPerms": true},
  "srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x", "resultFormat": "yaml"}
}`)
	_, err := ParseArgs([]string{"--config-file", path})
	if err == nil {
		t.Fatal("expected an invalid resultFormat to be rejected")
	}
	if !strings.Contains(err.Error(), "resultFormat must be 'text' or 'json'") {
		t.Fatalf("error must name the field and its values, got: %v", err)
	}
}

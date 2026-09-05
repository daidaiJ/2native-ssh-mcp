package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeApprovalConfig writes a config file and returns its path. The temp
// directory is chmod 0700 so CheckConfigFilePermissions accepts it on Unix.
func writeApprovalConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "config.json")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApprovalModeGlobalDefaultApplies(t *testing.T) {
	path := writeApprovalConfig(t, `{
		"$global": {"approvalMode": "ask-destructive", "approvalPatterns": ["terraform\\s+destroy"]},
		"prod": {"host": "h1", "username": "u", "port": 22},
		"lab":  {"host": "h2", "username": "u", "port": 22, "approvalMode": "auto"}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatal(err)
	}
	if got := opts.Configs["prod"].ApprovalMode; got != ApprovalModeAskDestructive {
		t.Fatalf("prod should inherit $global approvalMode, got %q", got)
	}
	if len(opts.Configs["prod"].ApprovalPatterns) != 1 {
		t.Fatalf("prod should inherit $global approvalPatterns")
	}
	// Connection-level override wins over $global.
	if got := opts.Configs["lab"].ApprovalMode; got != ApprovalModeAuto {
		t.Fatalf("lab should keep its own approvalMode, got %q", got)
	}
}

func TestApprovalModeDefaultsToAuto(t *testing.T) {
	path := writeApprovalConfig(t, `[{"name": "dev", "host": "h", "username": "u", "port": 22}]`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatal(err)
	}
	if got := opts.Configs["dev"].ApprovalMode; got != ApprovalModeAuto {
		t.Fatalf("unset approvalMode must normalize to auto, got %q", got)
	}
}

func TestApprovalModeValidation(t *testing.T) {
	path := writeApprovalConfig(t, `{"dev": {"host": "h", "username": "u", "port": 22, "approvalMode": "yolo"}}`)
	_, err := ParseArgs([]string{"--config-file", path})
	if err == nil || !strings.Contains(err.Error(), "approvalMode") {
		t.Fatalf("expected approvalMode validation error, got %v", err)
	}
}

func TestApprovalPatternValidation(t *testing.T) {
	path := writeApprovalConfig(t, `{"dev": {"host": "h", "username": "u", "port": 22, "approvalPatterns": ["(["]}}`)
	_, err := ParseArgs([]string{"--config-file", path})
	if err == nil || !strings.Contains(err.Error(), "approvalPatterns") {
		t.Fatalf("expected invalid-regex error naming the field, got %v", err)
	}
}

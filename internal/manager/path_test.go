package manager

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestLocalPathDeniedMessageTraversal(t *testing.T) {
	msg := localPathDeniedMessage("foo/../../etc/passwd", "/resolved/etc/passwd", []string{"/cwd"})
	if !strings.Contains(msg, "Path traversal rejected") {
		t.Fatalf("traversal path must use the traversal wording, got: %s", msg)
	}
	if strings.Contains(msg, "not within the process cwd") {
		t.Fatalf("traversal path must not use the whitelist wording, got: %s", msg)
	}
}

func TestLocalPathDeniedMessageBackslashTraversal(t *testing.T) {
	// Windows-style separators count as traversal too.
	msg := localPathDeniedMessage(`foo\..\..\etc\passwd`, "/resolved/etc/passwd", []string{"/cwd"})
	if !strings.Contains(msg, "Path traversal rejected") {
		t.Fatalf("backslash traversal must use the traversal wording, got: %s", msg)
	}
}

func TestLocalPathDeniedMessageWhitelistMiss(t *testing.T) {
	msg := localPathDeniedMessage(`D:\other\a.tar`, `D:\other\a.tar`, []string{`D:\pro\2native-ssh-mcp`})
	if strings.Contains(msg, "Path traversal detected") || strings.Contains(msg, "Path traversal rejected") {
		t.Fatalf("plain whitelist miss must not be worded as traversal, got: %s", msg)
	}
	if !strings.Contains(msg, "not within the allowed local paths for this connection") {
		t.Fatalf("whitelist miss must use the scope wording, got: %s", msg)
	}
	if !strings.Contains(msg, "Allowed local paths") {
		t.Fatalf("message must list the allowed roots, got: %s", msg)
	}
}

func TestHasDotDotSegment(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"foo/../../etc/passwd", true},
		{`foo\..\..\etc\passwd`, true},
		{"../etc/passwd", true},
		{"foo..bar", false},
		{"D:\\other\\a.tar", false},
		{"C:\\pro\\x", false},
		{"plain/path", false},
	}
	for _, c := range cases {
		if got := hasDotDotSegment(c.path); got != c.want {
			t.Fatalf("hasDotDotSegment(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func newPathTestManager(t *testing.T, cfg *config.SSHConfig) *Manager {
	t.Helper()
	cfg.Name = "dev"
	m, err := New(map[string]*config.SSHConfig{"dev": cfg}, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestValidateLocalPathModeAny(t *testing.T) {
	m := newPathTestManager(t, &config.SSHConfig{
		Host: "h", Username: "u", LocalPathMode: config.LocalPathModeAny,
	})
	outside := filepath.Join(t.TempDir(), "anywhere.txt")
	got, err := m.validateLocalPath(outside, "dev", "read")
	if err != nil {
		t.Fatalf("any mode must allow any path, got: %v", err)
	}
	if got != outside {
		t.Fatalf("unexpected resolved path: %s", got)
	}
}

func TestValidateLocalPathModeList(t *testing.T) {
	allowed := t.TempDir()
	m := newPathTestManager(t, &config.SSHConfig{
		Host: "h", Username: "u", LocalPathMode: config.LocalPathModeList,
		AllowedLocalPaths: []string{allowed},
	})

	inside := filepath.Join(allowed, "in.txt")
	if _, err := m.validateLocalPath(inside, "dev", "read"); err != nil {
		t.Fatalf("path inside allowedLocalPaths must pass in list mode, got: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "out.txt")
	if _, err := m.validateLocalPath(outside, "dev", "read"); err == nil {
		t.Fatal("path outside allowedLocalPaths must fail in list mode")
	}
}

func TestValidateLocalPathModeCwd(t *testing.T) {
	m := newPathTestManager(t, &config.SSHConfig{
		Host: "h", Username: "u", LocalPathMode: config.LocalPathModeCwd,
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(cwd, "definitely-not-a-real-file.txt")
	if _, err := m.validateLocalPath(inside, "dev", "read"); err != nil {
		t.Fatalf("path inside cwd must pass in cwd mode, got: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "out.txt")
	if _, err := m.validateLocalPath(outside, "dev", "read"); err == nil {
		t.Fatal("path outside cwd must fail in cwd mode")
	}
}

func TestValidateLocalPathModeCwdIncludesList(t *testing.T) {
	// Backward compatibility: the default "cwd" mode keeps honoring
	// allowedLocalPaths as extra roots.
	allowed := t.TempDir()
	m := newPathTestManager(t, &config.SSHConfig{
		Host: "h", Username: "u", LocalPathMode: config.LocalPathModeCwd,
		AllowedLocalPaths: []string{allowed},
	})

	inside := filepath.Join(allowed, "in.txt")
	if _, err := m.validateLocalPath(inside, "dev", "read"); err != nil {
		t.Fatalf("allowedLocalPaths must still be honored in cwd mode, got: %v", err)
	}
}

func TestValidateLocalPathModeListEmptyDeniesAll(t *testing.T) {
	m := newPathTestManager(t, &config.SSHConfig{
		Host: "h", Username: "u", LocalPathMode: config.LocalPathModeList,
	})

	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.validateLocalPath(filepath.Join(cwd, "x.txt"), "dev", "read"); err == nil {
		t.Fatal("list mode with no allowedLocalPaths must deny every path")
	}
}

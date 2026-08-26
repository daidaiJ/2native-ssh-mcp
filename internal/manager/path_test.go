package manager

import (
	"strings"
	"testing"
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
	if !strings.Contains(msg, "not within the process cwd or configured allowedLocalPaths") {
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
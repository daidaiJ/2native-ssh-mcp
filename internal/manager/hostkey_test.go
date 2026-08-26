package manager

import (
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"2native-ssh-mcp/internal/config"
)

// testHostKey generates a fresh ed25519 host key.
func testHostKey(t *testing.T) ssh.PublicKey {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	return signer.PublicKey()
}

func testRemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("192.0.2.1"), Port: 22}
}

func writeKnownHosts(t *testing.T, path string, hostname string, key ssh.PublicKey) {
	t.Helper()
	line := knownhosts.Line([]string{hostname}, key) + "\n"
	if err := os.WriteFile(path, []byte(line), 0600); err != nil {
		t.Fatal(err)
	}
}

func readKnownHosts(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestHostKeyAcceptNewRecordsUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cfg := &config.SSHConfig{HostKeyCheck: "accept-new", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("known_hosts file not created: %v", err)
	}

	key := testHostKey(t)
	hostname := "192.0.2.1:22"
	if err := cb(hostname, testRemoteAddr(), key); err != nil {
		t.Fatalf("first callback should accept unknown host: %v", err)
	}
	content := readKnownHosts(t, path)
	if !strings.Contains(content, key.Type()) {
		t.Fatalf("known_hosts missing recorded key, got: %q", content)
	}

	// Same key again: accepted, no duplicate line appended.
	if err := cb(hostname, testRemoteAddr(), key); err != nil {
		t.Fatalf("second callback with same key should pass: %v", err)
	}
	if got := readKnownHosts(t, path); got != content {
		t.Fatalf("known_hosts changed on repeat check:\nbefore: %q\nafter:  %q", content, got)
	}
}

func TestHostKeyAcceptNewMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key1 := testHostKey(t)
	writeKnownHosts(t, path, "192.0.2.1:22", key1)

	cfg := &config.SSHConfig{HostKeyCheck: "accept-new", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	err = cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t))
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeSSHHostKeyMismatch {
		t.Fatalf("expected %s, got %s", CodeSSHHostKeyMismatch, te.Code)
	}
	if te.Retriable {
		t.Fatal("host key mismatch must not be retriable")
	}
}

func TestHostKeyStrictUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	// Empty file: host unknown.
	if err := os.WriteFile(path, nil, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := &config.SSHConfig{HostKeyCheck: "strict", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	err = cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t))
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeSSHHostKeyUnknown {
		t.Fatalf("expected %s, got %s", CodeSSHHostKeyUnknown, te.Code)
	}
	if te.Retriable {
		t.Fatal("unknown host key must not be retriable")
	}
	// strict must not create or modify the file.
	if got := readKnownHosts(t, path); got != "" {
		t.Fatalf("strict mode modified known_hosts: %q", got)
	}
}

func TestHostKeyStrictMissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts") // does not exist
	cfg := &config.SSHConfig{HostKeyCheck: "strict", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	err = cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t))
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeSSHHostKeyUnknown {
		t.Fatalf("expected %s, got %s", CodeSSHHostKeyUnknown, te.Code)
	}
}

func TestHostKeyStrictMismatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	key1 := testHostKey(t)
	writeKnownHosts(t, path, "192.0.2.1:22", key1)

	cfg := &config.SSHConfig{HostKeyCheck: "strict", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	err = cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t))
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("expected *ToolError, got %T: %v", err, err)
	}
	if te.Code != CodeSSHHostKeyMismatch {
		t.Fatalf("expected %s, got %s", CodeSSHHostKeyMismatch, te.Code)
	}
}

func TestHostKeyNoneIgnoresFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts") // does not exist
	cfg := &config.SSHConfig{HostKeyCheck: "none", KnownHostsFile: path}
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if err := cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t)); err != nil {
		t.Fatalf("none mode must accept any key: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("none mode must not touch known_hosts file, stat err: %v", err)
	}
}

func TestHostKeyDefaultIsAcceptNew(t *testing.T) {
	path := filepath.Join(t.TempDir(), "known_hosts")
	cfg := &config.SSHConfig{KnownHostsFile: path} // empty HostKeyCheck
	cb, err := buildHostKeyCallback(cfg)
	if err != nil {
		t.Fatalf("buildHostKeyCallback: %v", err)
	}
	if err := cb("192.0.2.1:22", testRemoteAddr(), testHostKey(t)); err != nil {
		t.Fatalf("default mode should accept and record unknown host: %v", err)
	}
	if got := readKnownHosts(t, path); !strings.Contains(got, "ssh-") {
		t.Fatalf("default mode did not record host, got: %q", got)
	}
}

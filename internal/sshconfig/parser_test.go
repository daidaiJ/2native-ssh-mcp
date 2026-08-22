package sshconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, content string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupBasic(t *testing.T) {
	path := writeConfig(t, `
Host myserver
    HostName 192.168.1.10
    User alice
    Port 2222
    IdentityFile ~/.ssh/id_rsa

Host *
    User defaultuser
`)
	entry, err := Lookup("myserver", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil {
		t.Fatal("expected entry for myserver")
	}
	if entry.HostName != "192.168.1.10" || entry.User != "alice" || entry.Port != 2222 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.IdentityFile == "~/.ssh/id_rsa" {
		t.Fatal("identity file should be tilde-expanded")
	}
}

func TestLookupWildcardAndNegation(t *testing.T) {
	path := writeConfig(t, `
Host *.example.com !bad.example.com
    User wildcard

Host bad.example.com
    User baduser
`)
	entry, err := Lookup("good.example.com", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.User != "wildcard" {
		t.Fatalf("expected wildcard match, got: %+v", entry)
	}

	entry, err = Lookup("bad.example.com", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.User != "baduser" {
		t.Fatalf("expected negation to fall through, got: %+v", entry)
	}
}

func TestLookupFirstMatchWins(t *testing.T) {
	path := writeConfig(t, `
Host myserver
    User first
    User second
`)
	entry, err := Lookup("myserver", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry.User != "first" {
		t.Fatalf("expected first-match-wins, got: %+v", entry)
	}
}

func TestLookupInclude(t *testing.T) {
	dir := t.TempDir()
	includePath := filepath.Join(dir, "extra.conf")
	if err := os.WriteFile(includePath, []byte("Host extra\n    User extrauser\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "config")
	content := "Include extra.conf\nHost main\n    User mainuser\n"
	if err := os.WriteFile(mainPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	entry, err := Lookup("extra", mainPath)
	if err != nil {
		t.Fatal(err)
	}
	if entry == nil || entry.User != "extrauser" {
		t.Fatalf("expected include to resolve, got: %+v", entry)
	}
}

func TestLookupMissing(t *testing.T) {
	path := writeConfig(t, "Host other\n    User x\n")
	entry, err := Lookup("nope", path)
	if err != nil {
		t.Fatal(err)
	}
	if entry != nil {
		t.Fatalf("expected nil for unknown host, got: %+v", entry)
	}
}

func TestLookupDefaultPathMissing(t *testing.T) {
	// No explicit path and no ~/.ssh/config: must return nil, not an error.
	entry, err := Lookup("anything", "")
	if err != nil {
		t.Fatalf("expected nil error for missing default config, got: %v", err)
	}
	if entry != nil {
		t.Fatalf("expected nil entry, got: %+v", entry)
	}
}

func TestLookupExplicitPathMissing(t *testing.T) {
	_, err := Lookup("anything", filepath.Join(t.TempDir(), "nope"))
	if err == nil {
		t.Fatal("expected error for explicitly missing config file")
	}
}
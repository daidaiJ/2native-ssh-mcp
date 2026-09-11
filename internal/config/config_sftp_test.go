package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSftpDedicatedConfig(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(newConfigDir(t), "config.json")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestSftpDedicatedConnDefaultOff(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["srv"]
	if conf.GetSftpDedicatedConn() {
		t.Fatal("expected sftpDedicatedConn to default to false")
	}
}

func TestSftpDedicatedConnGlobalDefault(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"$global": {"sftpDedicatedConn": true},
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"},
		"plain": {"host": "10.0.0.2", "port": 22, "username": "root", "password": "x", "sftpDedicatedConn": false}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if !opts.Configs["srv"].GetSftpDedicatedConn() {
		t.Fatal("expected $global.sftpDedicatedConn=true to apply to connections")
	}
	if opts.Configs["plain"].GetSftpDedicatedConn() {
		t.Fatal("expected connection-level sftpDedicatedConn=false to win over $global")
	}
}

func TestSftpDedicatedConnGlobalInvalid(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"$global": {"sftpDedicatedConn": "yes"},
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
	}`)
	if _, err := ParseArgs([]string{"--config-file", path}); err == nil {
		t.Fatal("expected an error for a non-boolean $global.sftpDedicatedConn")
	}
}

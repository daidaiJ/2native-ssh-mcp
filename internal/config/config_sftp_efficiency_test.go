package config

import "testing"

func TestSftpEfficiencyDefaults(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["srv"]
	if conf.SftpFileConcurrency != DefaultSftpFileConcurrency {
		t.Fatalf("sftpFileConcurrency = %d, want %d", conf.SftpFileConcurrency, DefaultSftpFileConcurrency)
	}
	if !conf.GetSftpVerifyChecksum() {
		t.Fatal("sftpVerifyChecksum must default to true")
	}
}

func TestSftpEfficiencyGlobalDefaultAndOverride(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"$global": {"sftpFileConcurrency": 8, "sftpVerifyChecksum": false},
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x"},
		"over": {"host": "10.0.0.2", "port": 22, "username": "root", "password": "x",
			"sftpFileConcurrency": 2, "sftpVerifyChecksum": true}
	}`)
	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if got := opts.Configs["srv"].SftpFileConcurrency; got != 8 {
		t.Fatalf("$global.sftpFileConcurrency must apply, got %d", got)
	}
	if opts.Configs["srv"].GetSftpVerifyChecksum() {
		t.Fatal("$global.sftpVerifyChecksum=false must apply")
	}
	if got := opts.Configs["over"].SftpFileConcurrency; got != 2 {
		t.Fatalf("connection-level sftpFileConcurrency must win, got %d", got)
	}
	if !opts.Configs["over"].GetSftpVerifyChecksum() {
		t.Fatal("connection-level sftpVerifyChecksum must win over $global")
	}
}

func TestSftpFileConcurrencyValidation(t *testing.T) {
	path := writeSftpDedicatedConfig(t, `{
		"srv": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x", "sftpFileConcurrency": -1}
	}`)
	if _, err := ParseArgs([]string{"--config-file", path}); err == nil {
		t.Fatal("expected an error for a negative sftpFileConcurrency")
	}
}

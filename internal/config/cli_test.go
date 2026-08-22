package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseArgsSingleHost(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--host", "192.168.1.1", "--port", "2222",
		"--username", "root", "--password", "secret",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf, ok := opts.Configs["default"]
	if !ok {
		t.Fatal("expected 'default' connection")
	}
	if conf.Host != "192.168.1.1" || conf.Port != 2222 || conf.Username != "root" || conf.Password != "secret" {
		t.Fatalf("unexpected config: %+v", conf)
	}
	if conf.TransportMode != "exec" {
		t.Fatalf("expected default transport mode exec, got %s", conf.TransportMode)
	}
	if conf.CommandTimeoutMs != DefaultCommandTimeoutMs {
		t.Fatalf("expected default command timeout %d, got %d", DefaultCommandTimeoutMs, conf.CommandTimeoutMs)
	}
}

func TestParseArgsPositionals(t *testing.T) {
	opts, err := ParseArgs([]string{"1.2.3.4", "22", "alice", "pwd"})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["default"]
	if conf.Host != "1.2.3.4" || conf.Username != "alice" || conf.Password != "pwd" {
		t.Fatalf("unexpected config: %+v", conf)
	}
}

func TestParseArgsConfigFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
		"dev": {"host": "10.0.0.1", "port": 22, "username": "root", "password": "x",
		         "commandLogSize": 20, "commandLogOnlySuccess": true, "sftpConcurrency": 8},
		"prod": {"host": "10.0.0.2", "port": "2222", "user": "deploy", "privateKey": "~/keys/id_rsa"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if len(opts.Configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(opts.Configs))
	}
	dev := opts.Configs["dev"]
	if dev.CommandLogSize != 20 || !dev.CommandLogOnlySuccess || dev.SftpConcurrency != 8 {
		t.Fatalf("unexpected dev config: %+v", dev)
	}
	prod := opts.Configs["prod"]
	if prod.Port != 2222 || prod.Username != "deploy" {
		t.Fatalf("unexpected prod config: %+v", prod)
	}
	if prod.PrivateKey == "~/keys/id_rsa" {
		t.Fatal("privateKey should be home-expanded")
	}
}

func TestParseArgsSSHParamJSON(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--ssh", `{"name":"dev","host":"10.0.0.1","port":22,"username":"root","password":"x"}`,
		"--ssh", `{"name":"prod","host":"10.0.0.2","port":22,"username":"deploy","password":"y"}`,
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	if len(opts.Configs) != 2 {
		t.Fatalf("expected 2 configs, got %d", len(opts.Configs))
	}
}

func TestParseArgsSSHParamLegacy(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--ssh", "name=dev,host=10.0.0.1,port=22,user=root,password=x",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["dev"]
	if conf.Host != "10.0.0.1" || conf.Username != "root" {
		t.Fatalf("unexpected config: %+v", conf)
	}
}

func TestParseArgsCommandLogDefaults(t *testing.T) {
	opts, err := ParseArgs([]string{
		"--host", "1.2.3.4", "--username", "root", "--password", "x",
		"--command-log-size", "50", "--command-log-dir", "logs", "--command-log-only-success",
	})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["default"]
	if conf.CommandLogSize != 50 || conf.CommandLogDir != "logs" || !conf.CommandLogOnlySuccess {
		t.Fatalf("unexpected command log config: %+v", conf)
	}
}

func TestParseArgsInvalidTransportMode(t *testing.T) {
	_, err := ParseArgs([]string{
		"--host", "1.2.3.4", "--username", "root", "--password", "x",
		"--transport-mode", "bogus",
	})
	if err == nil {
		t.Fatal("expected error for invalid transport mode")
	}
}

func TestParseArgsMissingAuth(t *testing.T) {
	_, err := ParseArgs([]string{"--host", "1.2.3.4", "--username", "root"})
	if err == nil {
		t.Fatal("expected error for missing auth method")
	}
}

func TestParseArgsEnvVarReferences(t *testing.T) {
	t.Setenv("SSH_MCP_TEST_PASSWORD", "s3cret")
	t.Setenv("SSH_MCP_TEST_HOST", "10.9.9.9")
	dir := t.TempDir()
	path := filepath.Join(dir, "config.json")
	content := `{
		"dev": {"host": "${SSH_MCP_TEST_HOST}", "port": 22, "username": "root",
		        "password": "${SSH_MCP_TEST_PASSWORD}"}
	}`
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	opts, err := ParseArgs([]string{"--config-file", path})
	if err != nil {
		t.Fatalf("ParseArgs failed: %v", err)
	}
	conf := opts.Configs["dev"]
	if conf.Host != "10.9.9.9" || conf.Password != "s3cret" {
		t.Fatalf("env vars not expanded: %+v", conf)
	}
}

func TestParseArgsNoConfig(t *testing.T) {
	_, err := ParseArgs(nil)
	if err != nil {
		t.Fatalf("empty args should not error, got: %v", err)
	}
}

func TestNormalizeRejectsBothProxies(t *testing.T) {
	conf := &SSHConfig{Host: "h", Username: "u", Proxy: "socks5://1.2.3.4:1080", SocksProxy: "socks5://1.2.3.4:1080"}
	if err := conf.Normalize(); err == nil {
		t.Fatal("expected error for both proxy and socksProxy")
	}
}

func TestNormalizeSftpDefaults(t *testing.T) {
	conf := &SSHConfig{Host: "h", Username: "u"}
	if err := conf.Normalize(); err != nil {
		t.Fatal(err)
	}
	if conf.SftpConcurrency != DefaultSftpConcurrency || conf.SftpChunkSize != DefaultSftpChunkSize {
		t.Fatalf("unexpected sftp defaults: %+v", conf)
	}
}
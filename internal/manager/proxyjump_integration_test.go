//go:build integration

// ProxyJump integration tests against two real sshd containers (REQ-001 §6):
//
//	[test] --SSH--> bastion (published on 127.0.0.1:<free>) --direct-tcpip--> internal:22
//
// The "internal" service is reachable ONLY through the bastion: it publishes
// no host port and its hostname does not resolve on the test host, so a
// direct TCP dial is impossible by construction. Run with:
//
//	go test -tags integration ./internal/manager/ -run ProxyJump -v
//
// Without Docker every test skips, so the default `go test ./...` stays green.
package manager

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/pem"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

func requireDocker(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker not installed; skipping ProxyJump integration fixture")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker daemon unreachable; skipping ProxyJump integration fixture: %v: %s", err, strings.TrimSpace(string(out)))
	}
}

// jumpEnv is the running two-sshd fixture.
type jumpEnv struct {
	project     string // docker compose project name
	dir         string // temp dir with compose file and keys
	bastionAddr string // 127.0.0.1:<free port>
	bastionKey  string // private key that opens the bastion
	targetKey   string // private key that opens internal
}

var (
	jumpFixtureOnce sync.Once
	jumpFixture     *jumpEnv
	jumpFixtureErr  error
)

// sharedJumpFixture boots the fixture once for the whole test binary and
// skips the caller when Docker is not usable.
func sharedJumpFixture(t *testing.T) *jumpEnv {
	t.Helper()
	requireDocker(t)
	jumpFixtureOnce.Do(func() {
		jumpFixture, jumpFixtureErr = startJumpFixture()
	})
	if jumpFixtureErr != nil {
		t.Skipf("ProxyJump fixture unavailable: %v", jumpFixtureErr)
	}
	return jumpFixture
}

func startJumpFixture() (*jumpEnv, error) {
	dir, err := os.MkdirTemp("", "sshmcp-proxyjump-")
	if err != nil {
		return nil, err
	}
	env := &jumpEnv{project: filepath.Base(dir), dir: dir}

	bastionKeyPath := filepath.Join(dir, "bastion_key")
	targetKeyPath := filepath.Join(dir, "target_key")
	if err := writeED25519KeyPair(bastionKeyPath); err != nil {
		return nil, err
	}
	if err := writeED25519KeyPair(targetKeyPath); err != nil {
		return nil, err
	}
	env.bastionKey, env.targetKey = bastionKeyPath, targetKeyPath

	// The bastion publishes a fixed but dynamically chosen loopback port so
	// parallel runs and occupied ports never conflict.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	freePort := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	env.bastionAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(freePort))

	testdata, err := filepath.Abs(filepath.Join("..", "..", "testdata", "proxyjump"))
	if err != nil {
		return nil, err
	}
	compose := fmt.Sprintf(`
services:
  bastion:
    build:
      context: %s
      dockerfile: Dockerfile.sshd
    ports:
      - "127.0.0.1:%d:22"
    volumes:
      - %s:/root/.ssh/authorized_keys:ro
    command: ["/bin/sh", "-c", "ssh-keygen -A && exec /usr/sbin/sshd -D -e -o PermitRootLogin=yes -o PasswordAuthentication=no -o AllowTcpForwarding=yes"]
  internal:
    build:
      context: %s
      dockerfile: Dockerfile.sshd
    volumes:
      - %s:/root/.ssh/authorized_keys:ro
    command: ["/bin/sh", "-c", "ssh-keygen -A && exec /usr/sbin/sshd -D -e -o PermitRootLogin=yes -o PasswordAuthentication=no"]
`, testdata, freePort, bastionKeyPath+".pub", testdata, targetKeyPath+".pub")
	composePath := filepath.Join(dir, "compose.yaml")
	if err := os.WriteFile(composePath, []byte(compose), 0o600); err != nil {
		return nil, err
	}

	if err := composeCmd("up", "-d", "--build", "--quiet-pull")(env); err != nil {
		return nil, fmt.Errorf("docker compose up: %w", err)
	}

	// Wait for the bastion's sshd to accept TCP connections.
	deadline := time.Now().Add(60 * time.Second)
	for {
		conn, dialErr := net.DialTimeout("tcp", env.bastionAddr, 2*time.Second)
		if dialErr == nil {
			conn.Close()
			break
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("bastion sshd never became reachable: %w", dialErr)
		}
		time.Sleep(500 * time.Millisecond)
	}
	return env, nil
}

func (e *jumpEnv) down() {
	_ = composeCmd("down", "-v")(e)
	_ = os.RemoveAll(e.dir)
}

func composeCmd(args ...string) func(*jumpEnv) error {
	return func(e *jumpEnv) error {
		full := append([]string{"compose", "-p", e.project, "-f", filepath.Join(e.dir, "compose.yaml")}, args...)
		cmd := exec.Command("docker", full...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%v: %s", err, strings.TrimSpace(string(out)))
		}
		return nil
	}
}

// writeED25519KeyPair writes a fresh keypair: the private key as OpenSSH PEM
// and the public half as authorized_keys next to it (<path>.pub).
func writeED25519KeyPair(path string) error {
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		return err
	}
	block, err := ssh.MarshalPrivateKey(priv, "proxyjump integration test key")
	if err != nil {
		return err
	}
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		return err
	}
	return os.WriteFile(path+".pub", ssh.MarshalAuthorizedKey(signer.PublicKey()), 0o600)
}

// proxyJumpManager builds a Manager over the fixture: the good target, a
// target configured with the WRONG key (the bastion's key), and shell
// transport over the tunnel.
func proxyJumpManager(t *testing.T, env *jumpEnv) *Manager {
	t.Helper()
	internalHost := "internal" // compose DNS name; unresolvable on the host
	configs := map[string]*config.SSHConfig{
		"bastion":      jumpConfig(t, "bastion", env.bastionAddr, env.bastionKey, ""),
		"internal":     jumpConfig(t, "internal", net.JoinHostPort(internalHost, "22"), env.targetKey, "bastion"),
		"wrongkey":     jumpConfig(t, "wrongkey", net.JoinHostPort(internalHost, "22"), env.bastionKey, "bastion"),
		"internal-sh":  withJumpTransport(jumpConfig(t, "internal-sh", net.JoinHostPort(internalHost, "22"), env.targetKey, "bastion")),
	}
	m, err := New(configs, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.DisconnectAll)
	return m
}

func jumpConfig(t *testing.T, name, addr, privateKey, via string) *config.SSHConfig {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := &config.SSHConfig{
		Name:        name,
		Host:        host,
		Port:        port,
		Username:    "root",
		PrivateKey:  privateKey,
		Via:         via,
		HostKeyCheck: "none",
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func withJumpTransport(cfg *config.SSHConfig) *config.SSHConfig {
	cfg.TransportMode = "shell"
	return cfg
}

func TestProxyJumpExecHopOK(t *testing.T) {
	env := sharedJumpFixture(t)
	m := proxyJumpManager(t, env)

	res, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "internal", RunOptions{})
	if err != nil {
		t.Fatalf("via exec failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "hop-ok") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
}

// The internal service publishes no host port and its DNS name does not
// resolve outside the compose network: a direct dial from the test process
// cannot succeed, so reaching it at all proves the tunnel is real.
func TestProxyJumpNotDirect(t *testing.T) {
	env := sharedJumpFixture(t)
	_ = env

	conn, err := net.DialTimeout("tcp", "internal:22", 3*time.Second)
	if err == nil {
		conn.Close()
		t.Fatal("internal:22 must not be reachable from the test process")
	}
}

// The bastion's key must not open the target: each hop authenticates with
// its own credentials.
func TestProxyJumpWrongKey(t *testing.T) {
	env := sharedJumpFixture(t)
	m := proxyJumpManager(t, env)

	_, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "wrongkey", RunOptions{})
	if err == nil {
		t.Fatal("bastion key must not authenticate on the target")
	}
	if !strings.Contains(AsToolError(err).Message, "target authentication") {
		t.Fatalf("expected a target-auth failure, got: %s", AsToolError(err).Message)
	}
}

// Stopping the bastion is reported as a jump-host failure and returns within
// connection-timeout scale instead of hanging.
func TestProxyJumpBastionDown(t *testing.T) {
	env := sharedJumpFixture(t)
	m := proxyJumpManager(t, env)

	if err := composeCmd("stop", "bastion")(env); err != nil {
		t.Fatalf("failed to stop bastion: %v", err)
	}
	defer func() {
		if err := composeCmd("start", "bastion")(env); err != nil {
			t.Fatalf("failed to restart bastion: %v", err)
		}
	}()

	start := time.Now()
	_, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "internal", RunOptions{Timeout: 15 * time.Second})
	if err == nil {
		t.Fatal("stopped bastion must fail the dial")
	}
	if elapsed := time.Since(start); elapsed > 30*time.Second {
		t.Fatalf("bastion-down error took too long: %s", elapsed)
	}
	if !strings.Contains(AsToolError(err).Message, "jump host [bastion]") {
		t.Fatalf("expected jump-host failure, got: %s", AsToolError(err).Message)
	}
}

// SFTP rides the tunnel too: the bastion itself never needs SFTP enabled
// beyond TCP forwarding.
func TestProxyJumpSftp(t *testing.T) {
	env := sharedJumpFixture(t)
	m := proxyJumpManager(t, env)

	local := filepath.Join(t.TempDir(), "upload.txt")
	if err := os.WriteFile(local, []byte("through the tunnel\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := m.TransferFile(context.Background(), "upload", local, "/tmp/proxyjump-upload.txt", "internal", true, nil)
	if err != nil {
		t.Fatalf("sftp upload via tunnel failed: %v", err)
	}
	if res != nil && len(res.Failed) > 0 {
		t.Fatalf("sftp upload reported failures: %v", res.Failed)
	}

	check, err := m.ExecuteCommand(context.Background(), "cat /tmp/proxyjump-upload.txt", "", "internal", RunOptions{})
	if err != nil {
		t.Fatalf("readback failed: %v", err)
	}
	if !strings.Contains(check.Stdout, "through the tunnel") {
		t.Fatalf("uploaded content missing: %q", check.Stdout)
	}
}

// Shell transport over the tunnel behaves like a direct connection.
func TestProxyJumpShellMode(t *testing.T) {
	env := sharedJumpFixture(t)
	m := proxyJumpManager(t, env)

	res, err := m.ExecuteCommand(context.Background(), "echo shell-hop-ok", "", "internal-sh", RunOptions{})
	if err != nil {
		t.Fatalf("shell-mode exec via tunnel failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "shell-hop-ok") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
}

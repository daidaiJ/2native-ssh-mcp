package manager

import (
	"context"
	"errors"
	"net"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

// testHostKeyPEM is a throwaway ed25519 host key used only by the in-process
// fake server below (never installed anywhere).
const testHostKeyPEM = `-----BEGIN OPENSSH PRIVATE KEY-----
b3BlbnNzaC1rZXktdjEAAAAABG5vbmUAAAAEbm9uZQAAAAAAAAABAAAAMwAAAAtzc2gtZW
QyNTUxOQAAACBsFf54WC0xoWmSdC7lF2vHhwda4ll8lubPrGY9EkpWnQAAAIglTNauJUzW
rgAAAAtzc2gtZWQyNTUxOQAAACBsFf54WC0xoWmSdC7lF2vHhwda4ll8lubPrGY9EkpWnQ
AAAEAsojkS+QgHswUzOjDZTsU7iI5CCpM3A3Vdro6XmF3A6WwV/nhYLTGhaZJ0LuUXa8eH
B1riWXyW5s+sZj0SSladAAAABHRlc3QB
-----END OPENSSH PRIVATE KEY-----`

// startFakeKbdintServer runs an in-process SSH server that authenticates
// only through keyboard-interactive: the "Password:" question must carry the
// configured password and, when code is non-empty, the "Verification code:"
// question must carry the code. Any successful session answers "auth-ok\n"
// with exit status 0, regardless of the requested command. Returns the
// host:port to dial.
func startFakeKbdintServer(t *testing.T, password, code string) string {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	srv := &ssh.ServerConfig{
		KeyboardInteractiveCallback: func(_ ssh.ConnMetadata, client ssh.KeyboardInteractiveChallenge) (*ssh.Permissions, error) {
			questions := []string{"Password:"}
			if code != "" {
				questions = append(questions, "Verification code:")
			}
			answers, err := client("mock bastion", "", questions, make([]bool, len(questions)))
			if err != nil {
				return nil, err
			}
			ok := len(answers) == len(questions) && answers[0] == password
			if ok && code != "" {
				ok = answers[1] == code
			}
			if !ok {
				return nil, errors.New("invalid credentials")
			}
			return nil, nil
		},
	}
	srv.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			nConn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveFakeSSHConn(nConn, srv)
		}
	}()
	return ln.Addr().String()
}

func serveFakeSSHConn(nConn net.Conn, srv *ssh.ServerConfig) {
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, srv)
	if err != nil {
		nConn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		if newCh.ChannelType() != "session" {
			newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
			continue
		}
		ch, chReqs, err := newCh.Accept()
		if err != nil {
			continue
		}
		go serveFakeSession(ch, chReqs)
	}
}

func serveFakeSession(ch ssh.Channel, reqs <-chan *ssh.Request) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		_ = req.Reply(true, nil)
		_, _ = ch.Write([]byte("auth-ok\n"))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		return
	}
}

// newKbdintManager builds a Manager whose connections all point at addr, with
// password authentication plus tryKeyboard enabled. Command/audit logs are
// disabled (empty log dir) so tests exercise only the auth path.
func newKbdintManager(t *testing.T, addr string, names ...string) *Manager {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	configs := map[string]*config.SSHConfig{}
	for _, name := range names {
		cfg := &config.SSHConfig{
			Name:         name,
			Host:         host,
			Port:         port,
			Username:     "tester",
			Password:     "secret",
			TryKeyboard:  true,
			HostKeyCheck: "none",
		}
		if err := cfg.Normalize(); err != nil {
			t.Fatal(err)
		}
		configs[name] = cfg
	}
	m, err := New(configs, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.DisconnectAll)
	return m
}

// gateElicitor signals when the prompt reached the client and blocks until
// released — used to hold a handshake mid-prompt while other work proceeds.
type gateElicitor struct {
	entered chan struct{}
	release chan struct{}
	answers []string
}

func (g *gateElicitor) Prompt(_ context.Context, _ Challenge) ([]string, error) {
	select {
	case <-g.entered:
	default:
		close(g.entered)
	}
	<-g.release
	return g.answers, nil
}

func TestKbdintElicitorSuccess(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "")

	rec := &RecordingElicitor{Answers: []string{"654321"}}
	res, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "otp", RunOptions{})
	if err != nil {
		t.Fatalf("elicitor-supplied code should authenticate: %v", err)
	}
	if !strings.Contains(res.Stdout, "auth-ok") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
	if len(rec.Challenges) != 1 {
		t.Fatalf("expected exactly one prompt, got %d", len(rec.Challenges))
	}
	ch := rec.Challenges[0]
	if ch.Connection != "otp" {
		t.Fatalf("challenge must carry the connection name, got %q", ch.Connection)
	}
}

// The password question is answered from the configured password and must
// never reach the Elicitor — and when the server asks nothing else, the
// human is not prompted at all.
func TestKbdintPasswordNotElicited(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "")
	m := newKbdintManager(t, addr, "pwonly")
	t.Setenv("SSH_MCP_2FA_CODE", "")

	rec := &RecordingElicitor{Answers: []string{"unused"}}
	_, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "pwonly", RunOptions{})
	if err != nil {
		t.Fatalf("password-only server should authenticate from config: %v", err)
	}
	if len(rec.Challenges) != 0 {
		t.Fatalf("password questions must not reach the elicitor, got %+v", rec.Challenges)
	}

	// Mixed round: only the OTP question is forwarded.
	addr = startFakeKbdintServer(t, "secret", "654321")
	m = newKbdintManager(t, addr, "mixed")
	rec = &RecordingElicitor{Answers: []string{"654321"}}
	if _, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "mixed", RunOptions{}); err != nil {
		t.Fatalf("mixed round should authenticate: %v", err)
	}
	if len(rec.Challenges) != 1 {
		t.Fatalf("expected one prompt, got %d", len(rec.Challenges))
	}
	ch := rec.Challenges[0]
	if len(ch.Questions) != 1 || ch.Questions[0] != "Verification code:" {
		t.Fatalf("elicitor must only see the OTP question, got %+v", ch.Questions)
	}
	if len(ch.Echos) != 1 || ch.Echos[0] {
		t.Fatalf("OTP question must be echoed as secret, got %+v", ch.Echos)
	}
}

func TestKbdintElicitorDecline(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "")

	rec := &RecordingElicitor{Err: errors.New("user declined")}
	_, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "otp", RunOptions{})
	if err == nil {
		t.Fatal("declined prompt with no env fallback must fail authentication")
	}
	if len(rec.Challenges) != 1 {
		t.Fatalf("expected the elicitor to be consulted once, got %d", len(rec.Challenges))
	}
}

func TestKbdintElicitorTimeout(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "")

	old := kbdintPromptTimeout
	kbdintPromptTimeout = 100 * time.Millisecond
	defer func() { kbdintPromptTimeout = old }()

	rec := &RecordingElicitor{Answers: []string{"654321"}, Delay: 10 * time.Second}
	start := time.Now()
	_, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "otp", RunOptions{})
	if err == nil {
		t.Fatal("unanswered prompt with no env fallback must fail authentication")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("prompt timeout did not bound the wait: %s", elapsed)
	}
	if len(rec.Challenges) != 1 {
		t.Fatalf("expected one prompt, got %d", len(rec.Challenges))
	}
}

func TestKbdintNoElicitorNoEnv(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "")

	if _, err := m.ExecuteCommand(context.Background(), "echo ok", "", "otp", RunOptions{}); err == nil {
		t.Fatal("no elicitor and no env must fail authentication")
	}
}

func TestKbdintNoElicitorEnvOK(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "654321")

	if _, err := m.ExecuteCommand(context.Background(), "echo ok", "", "otp", RunOptions{}); err != nil {
		t.Fatalf("env fallback should authenticate headless runs: %v", err)
	}
}

// While an elicitor is attached it takes exclusive responsibility: the env
// var is not consulted even when it holds the correct code.
func TestKbdintElicitorBeatsStaleEnv(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "654321")

	rec := &RecordingElicitor{Answers: []string{"000000"}}
	_, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "otp", RunOptions{})
	if err == nil {
		t.Fatal("wrong elicitor answer must not be rescued by the env var")
	}
}

// Fail-open contract: a declined/unsupported prompt degrades to the env
// fallback instead of aborting the handshake.
func TestKbdintElicitorDeclineEnvFallback(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "otp")
	t.Setenv("SSH_MCP_2FA_CODE", "654321")

	rec := &RecordingElicitor{Err: errors.New("client does not support elicitation")}
	if _, err := m.ExecuteCommand(WithElicitor(context.Background(), rec), "echo ok", "", "otp", RunOptions{}); err != nil {
		t.Fatalf("declined prompt must fall back to env code: %v", err)
	}
}

// The handshake — and the human prompt inside it — runs without holding the
// Manager lock: a second connection must complete while the first one waits
// on its prompt.
func TestKbdintPromptDoesNotBlockOtherConnections(t *testing.T) {
	addr := startFakeKbdintServer(t, "secret", "654321")
	m := newKbdintManager(t, addr, "slow", "fast")
	t.Setenv("SSH_MCP_2FA_CODE", "654321")

	gate := &gateElicitor{entered: make(chan struct{}), release: make(chan struct{}), answers: []string{"654321"}}
	slowErr := make(chan error, 1)
	go func() {
		slowErr <- m.Connect(WithElicitor(context.Background(), gate), "slow")
	}()
	select {
	case <-gate.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("elicitor prompt never reached")
	}

	start := time.Now()
	if err := m.Connect(context.Background(), "fast"); err != nil {
		t.Fatalf("second connection must not block on the pending prompt: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("second connection waited %s behind the prompt", elapsed)
	}

	close(gate.release)
	select {
	case err := <-slowErr:
		if err != nil {
			t.Fatalf("released prompt should complete authentication: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("slow connection never finished")
	}
}

package manager

import (
	"context"
	"errors"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

// fakeHopServer is an in-process sshd used to exercise the via (ProxyJump)
// path without Docker: it authenticates with a fixed password and serves
// exec requests with a canned line. A bastion instance additionally accepts
// direct-tcpip channels — but only towards allowTo, so a test can prove the
// tunnel is scoped (the same assertion the Docker fixture makes with real
// network isolation).
type fakeHopServer struct {
	listener net.Listener
	addr     string
	// allowTo, when non-empty, is the only host:port direct-tcpip may
	// reach; any other target is rejected like a firewall would.
	allowTo string
	execOut string

	mu      sync.Mutex
	forward int // direct-tcpip channels accepted
}

// startFakeHopServer boots the server; the caller is responsible for
// Close() (tests usually defer it to control "bastion down" scenarios).
func startFakeHopServer(t *testing.T, password, allowTo, execOut string) *fakeHopServer {
	t.Helper()
	signer, err := ssh.ParsePrivateKey([]byte(testHostKeyPEM))
	if err != nil {
		t.Fatalf("parse test host key: %v", err)
	}
	srv := &ssh.ServerConfig{
		PasswordCallback: func(conn ssh.ConnMetadata, got []byte) (*ssh.Permissions, error) {
			if string(got) == password && conn.User() == "tester" {
				return nil, nil
			}
			return nil, errors.New("invalid credentials")
		},
	}
	srv.AddHostKey(signer)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	h := &fakeHopServer{listener: ln, addr: ln.Addr().String(), allowTo: allowTo, execOut: execOut}
	go func() {
		for {
			nConn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveHopConn(nConn, srv, h)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return h
}

func (h *fakeHopServer) Close() { h.listener.Close() }

// forwardedCount reports how many direct-tcpip channels were accepted.
func (h *fakeHopServer) forwardedCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.forward
}

func serveHopConn(nConn net.Conn, srv *ssh.ServerConfig, h *fakeHopServer) {
	sconn, chans, reqs, err := ssh.NewServerConn(nConn, srv)
	if err != nil {
		nConn.Close()
		return
	}
	defer sconn.Close()
	go ssh.DiscardRequests(reqs)
	for newCh := range chans {
		switch newCh.ChannelType() {
		case "session":
			ch, chReqs, err := newCh.Accept()
			if err != nil {
				continue
			}
			go serveHopSession(ch, chReqs, h.execOut)
		case "direct-tcpip":
			var msg struct {
				Addr     string
				Port     uint32
				OrigAddr string
				OrigPort uint32
			}
			if err := ssh.Unmarshal(newCh.ExtraData(), &msg); err != nil {
				newCh.Reject(ssh.ConnectionFailed, "bad direct-tcpip payload")
				continue
			}
			target := net.JoinHostPort(msg.Addr, strconv.Itoa(int(msg.Port)))
			if h.allowTo != "" && target != h.allowTo {
				newCh.Reject(ssh.Prohibited, "forwarding not allowed to "+target)
				continue
			}
			up, err := net.Dial("tcp", target)
			if err != nil {
				newCh.Reject(ssh.ConnectionFailed, "dial "+target+" failed")
				continue
			}
			ch, _, err := newCh.Accept()
			if err != nil {
				up.Close()
				continue
			}
			h.mu.Lock()
			h.forward++
			h.mu.Unlock()
			go func() {
				io.Copy(up, ch)
				up.Close()
			}()
			io.Copy(ch, up)
			ch.Close()
		default:
			newCh.Reject(ssh.UnknownChannelType, "unsupported channel")
		}
	}
}

func serveHopSession(ch ssh.Channel, reqs <-chan *ssh.Request, execOut string) {
	defer ch.Close()
	for req := range reqs {
		if req.Type != "exec" {
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
			continue
		}
		_ = req.Reply(true, nil)
		_, _ = ch.Write([]byte(execOut))
		_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(struct{ Status uint32 }{0}))
		return
	}
}

// viaTestSetup boots a bastion that forwards only to the internal server and
// builds a Manager with both connections.
func viaTestSetup(t *testing.T, bastionPass, targetPass string) (*Manager, *fakeHopServer, *fakeHopServer) {
	t.Helper()
	internal := startFakeHopServer(t, "dbpass", "", "hop-ok\n")
	bastion := startFakeHopServer(t, bastionPass, internal.addr, "bastion-ok\n")

	m := newViaManager(t, bastion.addr, internal.addr, targetPass)
	return m, bastion, internal
}

func newViaManager(t *testing.T, bastionAddr, internalAddr, targetPass string) *Manager {
	t.Helper()
	configs := map[string]*config.SSHConfig{
		"bastion": viaConfig(t, "bastion", bastionAddr, "jumppass", ""),
		"db":      viaConfig(t, "db", internalAddr, targetPass, "bastion"),
	}
	m, err := New(configs, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.DisconnectAll)
	return m
}

func viaConfig(t *testing.T, name, addr, password, via string) *config.SSHConfig {
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
		Name:         name,
		Host:         host,
		Port:         port,
		Username:     "tester",
		Password:     password,
		HostKeyCheck: "none",
		Via:          via,
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestViaTunnelExec(t *testing.T) {
	m, bastion, _ := viaTestSetup(t, "jumppass", "dbpass")

	res, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "db", RunOptions{})
	if err != nil {
		t.Fatalf("via exec failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "hop-ok") {
		t.Fatalf("unexpected output: %q", res.Stdout)
	}
	if bastion.forwardedCount() != 1 {
		t.Fatalf("expected one direct-tcpip channel through the bastion, got %d", bastion.forwardedCount())
	}
}

// The inner handshake uses the target's own credentials: jump-host
// credentials do not work there, and failures name both connections.
func TestViaTargetAuthFailure(t *testing.T) {
	m, _, _ := viaTestSetup(t, "jumppass", "wrong-db-pass")

	start := time.Now()
	_, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "db", RunOptions{})
	if err == nil {
		t.Fatal("target auth with wrong password must fail")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("target auth failure took too long: %s", elapsed)
	}
	te := AsToolError(err)
	if te.Code != CodeSSHConnectionFailed || !strings.Contains(te.Message, "target authentication") {
		t.Fatalf("expected a target-auth failure, got %s: %s", te.Code, te.Message)
	}
	if !strings.Contains(te.Message, "[db]") || !strings.Contains(te.Message, "[bastion]") {
		t.Fatalf("failure must name target and jump host, got: %s", te.Message)
	}
}

// When the jump host itself cannot authenticate, the error is about the
// outer hop, not the target.
func TestViaBastionAuthFailure(t *testing.T) {
	internal := startFakeHopServer(t, "dbpass", "", "hop-ok\n")
	bastion := startFakeHopServer(t, "real-pass", internal.addr, "bastion-ok\n")
	configs := map[string]*config.SSHConfig{
		"bastion": viaConfig(t, "bastion", bastion.addr, "wrong-jump-pass", ""),
		"db":      viaConfig(t, "db", internal.addr, "dbpass", "bastion"),
	}
	m, err := New(configs, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.DisconnectAll)

	_, err = m.ExecuteCommand(context.Background(), "echo hop-ok", "", "db", RunOptions{})
	if err == nil {
		t.Fatal("bastion auth failure must fail the dial")
	}
	te := AsToolError(err)
	if !strings.Contains(te.Message, "jump host [bastion] is unreachable") {
		t.Fatalf("expected jump-host failure, got %s: %s", te.Code, te.Message)
	}
}

// A dead bastion is reported as such — within connection-timeout scale, not
// a hang — and remains a connection failure, not a lost connection.
func TestViaBastionDown(t *testing.T) {
	m, bastion, _ := viaTestSetup(t, "jumppass", "dbpass")
	bastion.Close()

	start := time.Now()
	_, err := m.ExecuteCommand(context.Background(), "echo hop-ok", "", "db", RunOptions{})
	if err == nil {
		t.Fatal("dead bastion must fail the dial")
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("bastion-down error took too long: %s", elapsed)
	}
	if !strings.Contains(AsToolError(err).Message, "jump host [bastion]") {
		t.Fatalf("expected jump-host failure, got: %s", AsToolError(err).Message)
	}
}

// The bastion only forwards to the declared internal address: a target
// pointing elsewhere gets the channel rejected — this is the unit-level
// stand-in for the Docker fixture's "internal is not directly reachable".
func TestViaForwardingScoped(t *testing.T) {
	internal := startFakeHopServer(t, "dbpass", "", "hop-ok\n")
	other := startFakeHopServer(t, "x", "", "elsewhere\n")
	bastion := startFakeHopServer(t, "jumppass", internal.addr, "bastion-ok\n")

	configs := map[string]*config.SSHConfig{
		"bastion": viaConfig(t, "bastion", bastion.addr, "jumppass", ""),
		"db":      viaConfig(t, "db", internal.addr, "dbpass", "bastion"),
		"other":   viaConfig(t, "other", other.addr, "x", "bastion"),
	}
	m, err := New(configs, "")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(m.DisconnectAll)

	_, err = m.ExecuteCommand(context.Background(), "echo elsewhere", "", "other", RunOptions{})
	if err == nil {
		t.Fatal("bastion must reject forwarding to undeclared targets")
	}
	if !strings.Contains(AsToolError(err).Message, "could not open a channel") {
		t.Fatalf("expected channel-rejection failure, got: %s", AsToolError(err).Message)
	}
}

// list-servers must expose via so an agent understands the host is not
// directly reachable.
func TestViaServerInfoVia(t *testing.T) {
	m, _, _ := viaTestSetup(t, "jumppass", "dbpass")
	for _, info := range m.GetAllServerInfos() {
		if info.Name != "db" {
			continue
		}
		if info.Via != "bastion" {
			t.Fatalf("ServerInfo.Via must carry the jump host, got %q", info.Via)
		}
		return
	}
	t.Fatal("db connection missing from server infos")
}

package manager

import (
	"context"
	"testing"
	"time"

	"2native-ssh-mcp/internal/config"
)

func TestSessionNameValidation(t *testing.T) {
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	_, err = m.OpenSession("", "dev")
	if err == nil {
		t.Fatal("expected error for empty session name")
	}
	_, err = m.OpenSession("bad name!", "dev")
	if err == nil {
		t.Fatal("expected error for invalid session name")
	}
}

func TestListSessionsEmpty(t *testing.T) {
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(m.ListSessions()) != 0 {
		t.Fatalf("expected no sessions")
	}
}

func newIdleTestManager(t *testing.T) *Manager {
	t.Helper()
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func addSession(m *Manager, ns *namedSession) {
	m.mu.Lock()
	m.sessions[ns.name] = ns
	m.mu.Unlock()
}

func TestExpireSessionIfIdleStaleGenerationKeepsSession(t *testing.T) {
	m := newIdleTestManager(t)
	ns := &namedSession{name: "s1", connectionKey: "dev", lastUsed: time.Now()}
	addSession(m, ns)

	// A callback from an older generation (timer was reset meanwhile) must
	// not close the session.
	m.expireSessionIfIdle("s1", ns.idleGen+1)

	m.mu.Lock()
	_, ok := m.sessions["s1"]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expire with stale generation must not delete the session")
	}
}

func TestExpireSessionIfIdleBgRunningKeepsSession(t *testing.T) {
	m := newIdleTestManager(t)
	ns := &namedSession{name: "s1", connectionKey: "dev", background: true, bgRunning: true, lastUsed: time.Now()}
	addSession(m, ns)

	m.expireSessionIfIdle("s1", ns.idleGen)

	m.mu.Lock()
	_, ok := m.sessions["s1"]
	m.mu.Unlock()
	if !ok {
		t.Fatal("expire must not close a session with a running background job")
	}
}

func TestExpireSessionIfIdleClosesIdleSession(t *testing.T) {
	m := newIdleTestManager(t)
	ns := &namedSession{name: "s1", connectionKey: "dev", lastUsed: time.Now()}
	addSession(m, ns)

	m.expireSessionIfIdle("s1", ns.idleGen)

	m.mu.Lock()
	_, ok := m.sessions["s1"]
	m.mu.Unlock()
	if ok {
		t.Fatal("expire with matching generation must close the idle session")
	}
}

func TestExpireSessionIfIdleAfterCloseIsNoop(t *testing.T) {
	m := newIdleTestManager(t)
	ns := &namedSession{name: "s1", connectionKey: "dev", lastUsed: time.Now()}
	addSession(m, ns)

	if err := m.CloseSession("s1"); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	// A late expire callback for the closed session must not panic or
	// resurrect anything.
	m.expireSessionIfIdle("s1", ns.idleGen)

	m.mu.Lock()
	_, ok := m.sessions["s1"]
	m.mu.Unlock()
	if ok {
		t.Fatal("late expire must not resurrect a closed session")
	}
}

func TestCloseSessionIdempotent(t *testing.T) {
	m := newIdleTestManager(t)
	if err := m.CloseSession("never-opened"); err != nil {
		t.Fatalf("closing a never-opened session must succeed, got: %v", err)
	}
}

// TestRunNamedShellCommandNilShellReturnsError verifies the concurrency
// contract: after closeSessionsForConnection has torn down a session's shell
// (ns.shell == nil under m.mu), a racing runNamedShellCommand must return a
// retriable connection error instead of panicking on the nil shell.
func TestRunNamedShellCommandNilShellReturnsError(t *testing.T) {
	m := newIdleTestManager(t)
	ns := &namedSession{name: "s1", connectionKey: "dev", lastUsed: time.Now()}
	addSession(m, ns)

	_, err := m.runNamedShellCommand(context.Background(), ns, "echo hi", "", "", time.Second)
	if err == nil {
		t.Fatal("expected error when session shell is nil")
	}
	te, ok := err.(*ToolError)
	if !ok {
		t.Fatalf("expected *ToolError, got %T", err)
	}
	if te.Code != CodeSSHConnectionFailed || !te.Retriable {
		t.Fatalf("expected retriable connection error, got code=%s retriable=%v", te.Code, te.Retriable)
	}
}

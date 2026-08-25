package manager

import (
	"errors"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestKeepaliveOK(t *testing.T) {
	cases := []struct {
		name string
		ok   bool
		err  error
		want bool
	}{
		{"reply ok", true, nil, true},
		{"request failure is alive", false, nil, true}, // SSH_MSG_REQUEST_FAILURE is a normal reply
		{"request error is dead", false, errors.New("connection reset"), false},
		{"request error with ok", true, errors.New("EOF"), false},
	}
	for _, tc := range cases {
		if got := keepaliveOK(tc.ok, tc.err); got != tc.want {
			t.Fatalf("keepaliveOK(ok=%v, err=%v) = %v, want %v", tc.ok, tc.err, got, tc.want)
		}
	}
}

func TestApplyKeepaliveResult(t *testing.T) {
	if got := applyKeepaliveResult(2, keepaliveAlive); got != 0 {
		t.Fatalf("alive should reset counter, got %d", got)
	}
	if got := applyKeepaliveResult(0, keepaliveUnanswered); got != 1 {
		t.Fatalf("unanswered should increment, got %d", got)
	}
	if got := applyKeepaliveResult(1, keepaliveDead); got != 2 {
		t.Fatalf("dead should increment, got %d", got)
	}
}

func TestHasActiveWorkInFlight(t *testing.T) {
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	if m.hasActiveWork("dev") {
		t.Fatal("expected no active work initially")
	}
	m.beginOp("dev")
	if !m.hasActiveWork("dev") {
		t.Fatal("expected active work with in-flight op")
	}
	m.endOp("dev")
	if m.hasActiveWork("dev") {
		t.Fatal("expected no active work after endOp")
	}
}

func TestHasActiveWorkBackground(t *testing.T) {
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ns := &namedSession{name: "bg", connectionKey: "dev", bgRunning: true}
	m.mu.Lock()
	m.sessions["bg"] = ns
	m.mu.Unlock()
	if !m.hasActiveWork("dev") {
		t.Fatal("expected active work with running background job")
	}
}

func TestCloseSessionsForConnectionKeepsSession(t *testing.T) {
	m, err := New(map[string]*config.SSHConfig{
		"dev": {Host: "h", Username: "u", Port: 22},
	}, "")
	if err != nil {
		t.Fatal(err)
	}
	ns := &namedSession{name: "s1", connectionKey: "dev", shell: &shellSession{}}
	m.mu.Lock()
	m.sessions["s1"] = ns
	m.mu.Unlock()

	m.closeSessionsForConnection("dev")

	m.mu.Lock()
	kept := m.sessions["s1"]
	m.mu.Unlock()
	if kept == nil {
		t.Fatal("session should survive connection teardown")
	}
	if kept.shell != nil {
		t.Fatal("shell should be nil after teardown")
	}
	if !kept.disconnected {
		t.Fatal("session should be marked disconnected")
	}
}

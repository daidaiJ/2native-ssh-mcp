package manager

import (
	"testing"

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

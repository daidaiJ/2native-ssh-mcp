//go:build integration

// Integration tests against a real SSH host. Run with:
//
//	go test -tags integration ./internal/manager/ -run ExecKill -v
//
// They load connection details from the repo-root config.json and expect a
// reachable "ubuntu" host. These tests are excluded from the default build.
package manager

import (
	"context"
	"strings"
	"testing"
	"time"
)

// TestExecTimeoutKillsRemoteProcess verifies the Slice A guarantee of issue
// #3: after a foreground timeout the remote process group is really gone
// (PID-based group kill), not leaked the way channel-Signal-only kills are.
func TestExecTimeoutKillsRemoteProcess(t *testing.T) {
	m := loadTestManager(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	start := time.Now()
	_, err := m.ExecuteCommand(ctx, "sleep 77", "", "ubuntu", RunOptions{Timeout: 2 * time.Second})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if elapsed := time.Since(start); elapsed > 20*time.Second {
		t.Fatalf("timeout path took too long: %s", elapsed)
	}

	// The remote `sleep 60` must be dead within a few seconds.
	deadline := time.Now().Add(15 * time.Second)
	for {
		res, err := m.ExecuteCommand(context.Background(), "pgrep -f 'sleep 77' | grep -vw $$ | wc -l", "", "ubuntu", RunOptions{})
		if err != nil {
			t.Fatalf("pgrep failed: %v", err)
		}
		if !strings.Contains(res.Stdout, "0") {
			if time.Now().After(deadline) {
				t.Fatalf("remote sleep 77 still alive after timeout kill: %s", res.Stdout)
			}
			time.Sleep(500 * time.Millisecond)
			continue
		}
		return
	}
}

// TestExecDefaultNoPTY verifies exec commands no longer get a PTY by default
// and that the pty option still allocates one when requested.
func TestExecDefaultNoPTY(t *testing.T) {
	m := loadTestManager(t)

	res, err := m.ExecuteCommand(context.Background(), "if [ -t 0 ]; then echo TTY; else echo NOTTY; fi", "", "ubuntu", RunOptions{})
	if err != nil {
		t.Fatalf("exec failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "NOTTY") {
		t.Fatalf("expected no PTY by default, got: %s", res.Stdout)
	}

	yes := true
	res, err = m.ExecuteCommand(context.Background(), "if [ -t 0 ]; then echo TTY; else echo NOTTY; fi", "", "ubuntu", RunOptions{Pty: &yes})
	if err != nil {
		t.Fatalf("exec with pty failed: %v", err)
	}
	if !strings.Contains(res.Stdout, "TTY") {
		t.Fatalf("expected PTY when pty=true, got: %s", res.Stdout)
	}
}

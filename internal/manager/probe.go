package manager

import (
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/logger"
)

// RemoteCapabilities records what a remote host offers for the two utilities
// an agent reaches for when a command line gets awkward: base64 (moving a
// payload past a quoting layer) and jq (building JSON safely). It is probed
// once per connection and reported by list-servers, so an agent can pick a
// form the host actually supports instead of discovering it by failure.
type RemoteCapabilities struct {
	Base64 bool `json:"base64"`
	JQ     bool `json:"jq"`
	// ProbedAt is when the probe ran (RFC3339, empty when it was skipped).
	ProbedAt string `json:"probedAt,omitempty"`
	// Error carries the tool-error code when the probe itself could not run
	// (connection lost while probing); the booleans stay false.
	Error string `json:"error,omitempty"`
	// Skipped explains why no probe ran, e.g. "autoProbe=false".
	Skipped string `json:"skipped,omitempty"`
}

// capabilityProbes are the remote commands behind RemoteCapabilities. Each is
// one readable line — no encoding, no obfuscation — because whatever runs here
// is also visible to the remote host's own logging (session recording, a
// bastion, the shell's history file).
//
// The base64 fallback chain exists because `openssl base64 -d` silently emits
// nothing (exit code 0) when the input has no trailing newline, so the payload
// carries one and the result is compared byte for byte rather than by exit
// code. The jq probe pipes `{}` through `jq -c .`, which must come back as
// exactly `{}`.
var capabilityProbes = map[string]string{
	"base64": "printf 'YQ==\\n' | base64 -d 2>/dev/null || printf 'YQ==\\n' | base64 --decode 2>/dev/null || printf 'YQ==\\n' | openssl base64 -d 2>/dev/null",
	"jq":     "printf '{}\\n' | jq -c . 2>/dev/null",
}

// capabilityExpect is the byte-exact output each probe must produce.
var capabilityExpect = map[string]string{
	"base64": "a",
	"jq":     "{}",
}

// scheduleCapabilityProbe probes the connection's remote capabilities once,
// shortly after the connection is established. autoProbe=false (per
// connection, or globally through $global.autoProbe) turns it off entirely:
// the probe is a remote command, and on a connection that is audited or
// recorded remotely the server should not add commands of its own.
func (m *Manager) scheduleCapabilityProbe(key string) {
	m.mu.Lock()
	client := m.clients[key]
	m.mu.Unlock()
	cfg := m.configs[key]
	if cfg != nil && !cfg.GetAutoProbe() {
		m.setCapabilities(key, client, &RemoteCapabilities{Skipped: "autoProbe=false"})
		logger.Info("Capability probe disabled for [%s] (autoProbe=false)", key)
		return
	}
	go m.collectCapabilities(key, client)
}

// collectCapabilities runs the probes and caches the result. client is the
// SSH client this probe was scheduled against: a result is discarded if the
// connection was replaced or torn down before the probe finished.
func (m *Manager) collectCapabilities(key string, client *ssh.Client) {
	caps := &RemoteCapabilities{ProbedAt: time.Now().Format(time.RFC3339)}
	marker := fmt.Sprintf("__MCP_CAP_%x_", time.Now().UnixNano())
	var probes []string
	for _, field := range []string{"base64", "jq"} {
		probes = append(probes, fmt.Sprintf("printf '\\n%s%s\\n'; { %s; } 2>/dev/null", marker, field, capabilityProbes[field]))
	}
	script := strings.Join(probes, "; ") + "; true"

	result, err := m.runCommandInternal(key, script, RunOptions{Prevalidated: true})
	if err != nil {
		caps.Error = AsToolError(err).Code
		m.setCapabilities(key, client, caps)
		logger.Info("Capability probe for [%s] failed: %s", key, caps.Error)
		return
	}
	values := parseStatusOutput(result.Stdout, marker)
	caps.applyOutput(values)
	m.setCapabilities(key, client, caps)
	logger.Info("Capability probe for [%s]: base64=%t jq=%t", key, caps.Base64, caps.JQ)
}

// applyOutput turns one probe's parsed field values into the capability
// booleans. The comparison is byte-exact: `openssl base64 -d` reports success
// while emitting nothing when its input lacks a trailing newline, so an exit
// code would call a broken pipeline working.
func (c *RemoteCapabilities) applyOutput(values map[string]string) {
	for field, want := range capabilityExpect {
		if strings.TrimSpace(values[field]) != want {
			continue
		}
		switch field {
		case "base64":
			c.Base64 = true
		case "jq":
			c.JQ = true
		}
	}
}

func (m *Manager) setCapabilities(key string, client *ssh.Client, caps *RemoteCapabilities) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if client == nil || m.clients[key] != client {
		return
	}
	m.capabilities[key] = caps
}

// Capabilities returns the cached probe result for a connection, or nil when
// it has not run yet (never connected, or the probe is still in flight).
func (m *Manager) Capabilities(name string) *RemoteCapabilities {
	key := m.resolveName(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.capabilities[key]
}

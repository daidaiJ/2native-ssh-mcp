package manager

import (
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

// keepaliveGrace is how long a keepalive may go unanswered before it counts
// as a failure, on top of the configured interval.
const keepaliveGrace = 5 * time.Second

// keepaliveOK reports whether a keepalive reply means the peer is alive.
// OpenSSH treats any reply as success; SSH_MSG_REQUEST_FAILURE (ok=false)
// is a normal reply, not a failure. Port of OpenSSH clientloop.c
// server_alive_check and Scylla sshtools serverAliveCheck (Apache-2.0,
// https://github.com/scylladb/go-sshtools/blob/master/keepalive.go).
func keepaliveOK(ok bool, err error) bool { return err == nil }

// keepaliveResult is the outcome of a single keepalive probe.
type keepaliveResult int

const (
	keepaliveAlive keepaliveResult = iota
	keepaliveUnanswered
	keepaliveDead
)

// runKeepaliveRound performs one keepalive probe. SendRequest runs in a
// goroutine so a half-dead peer cannot block the whole SSH mux (Fuchsia
// fxbug.dev/47698); the round is bounded by grace. A reply within grace
// means alive (any reply, matching OpenSSH); no reply means unanswered; a
// request error means dead.
func runKeepaliveRound(client *ssh.Client, grace time.Duration) keepaliveResult {
	ch := make(chan keepaliveResult, 1)
	go func() {
		ok, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
		if keepaliveOK(ok, err) {
			ch <- keepaliveAlive
		} else {
			ch <- keepaliveDead
		}
	}()
	select {
	case res := <-ch:
		return res
	case <-time.After(grace):
		return keepaliveUnanswered
	}
}

// applyKeepaliveResult folds a probe outcome into the unanswered counter.
func applyKeepaliveResult(unanswered int, res keepaliveResult) int {
	if res == keepaliveAlive {
		return 0
	}
	return unanswered + 1
}

// startHeartbeat sends keepalive requests to detect dead connections. The
// probe runs in a goroutine and at most one probe is in flight at a time, so
// a half-dead peer cannot pile requests on the SSH mux. Only err matters for
// liveness; ok==false (REQUEST_FAILURE) is a normal reply.
func (m *Manager) startHeartbeat(key string, client *ssh.Client, cfg *config.SSHConfig) {
	interval := time.Duration(cfg.KeepaliveIntervalMs) * time.Millisecond
	maxCount := cfg.KeepaliveCountMax
	grace := interval + keepaliveGrace
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		unanswered := 0
		var pending chan keepaliveResult // non-nil while a probe is in flight
		for range ticker.C {
			m.mu.Lock()
			current := m.clients[key]
			m.mu.Unlock()
			if current != client {
				return
			}
			if pending != nil {
				select {
				case res := <-pending:
					pending = nil
					unanswered = applyKeepaliveResult(unanswered, res)
				default:
					// Previous probe still in flight; do not pile up on the mux.
					continue
				}
			}
			ch := make(chan keepaliveResult, 1)
			pending = ch
			go func() { ch <- runKeepaliveRound(client, grace) }()
			select {
			case res := <-ch:
				pending = nil
				unanswered = applyKeepaliveResult(unanswered, res)
			case <-time.After(grace):
				unanswered++
			}
			if unanswered >= maxCount {
				m.keepaliveFailed(key, client)
				return
			}
		}
	}()
}

// keepaliveFailed handles a connection whose keepalives have gone unanswered
// past the threshold. When a command is in flight the connection is only
// marked unhealthy so the running Wait() can fail with partial output;
// otherwise it is disconnected immediately.
func (m *Manager) keepaliveFailed(key string, client *ssh.Client) {
	m.mu.Lock()
	inFlight := m.inFlight[key]
	m.mu.Unlock()
	if inFlight > 0 {
		logger.Warn("Keepalive failed for [%s] while a command is in flight; marking unhealthy, will disconnect when it finishes", key)
		m.mu.Lock()
		m.unhealthy[key] = true
		m.mu.Unlock()
		return
	}
	logger.Info("Keepalive failed for [%s], invalidating connection", key)
	m.Disconnect(key)
}

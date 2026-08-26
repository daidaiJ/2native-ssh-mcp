package manager

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"

	"2native-ssh-mcp/internal/config"
)

// buildHostKeyCallback returns the SSH host key verification callback for a
// connection. hostKeyCheck: "accept-new" (default) | "strict" | "none".
func buildHostKeyCallback(cfg *config.SSHConfig) (ssh.HostKeyCallback, error) {
	switch cfg.HostKeyCheck {
	case "none":
		return ssh.InsecureIgnoreHostKey(), nil
	case "strict":
		cb, err := knownHostsCallback(cfg.KnownHostsFile)
		if err != nil {
			return nil, err
		}
		return wrapHostKeyCheck(cb, knownHostsPath(cfg.KnownHostsFile), false), nil
	default: // "accept-new" (empty → accept-new)
		path := knownHostsPath(cfg.KnownHostsFile)
		if err := ensureKnownHostsFile(path); err != nil {
			return nil, err
		}
		cb, err := knownhosts.New(path)
		if err != nil {
			return nil, err
		}
		return wrapHostKeyCheck(cb, path, true), nil
	}
}

// knownHostsPath resolves the known_hosts file path (default
// ~/.ssh/known_hosts).
func knownHostsPath(configured string) string {
	if configured != "" {
		return configured
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".ssh/known_hosts"
	}
	return filepath.Join(home, ".ssh", "known_hosts")
}

// knownHostsCallback builds a callback from the given known_hosts file. A
// missing file means there is no trust anchor yet: every host is unknown.
func knownHostsCallback(path string) (ssh.HostKeyCallback, error) {
	cb, err := knownhosts.New(path)
	if err != nil {
		if os.IsNotExist(err) {
			// No trust anchor yet: every host is unknown (empty Want).
			return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
				return &knownhosts.KeyError{}
			}, nil
		}
		return nil, err
	}
	return cb, nil
}

// ensureKnownHostsFile creates the known_hosts file (0600) when missing,
// including its parent directory (0700, e.g. ~/.ssh).
func ensureKnownHostsFile(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	return f.Close()
}

// wrapHostKeyCheck translates knownhosts.KeyError into structured errors.
// With acceptNew, an unknown host is recorded into the file and accepted
// (OpenSSH accept-new semantics); otherwise it is rejected as
// SSH_HOST_KEY_UNKNOWN. A known host with a different key is always rejected
// as SSH_HOST_KEY_MISMATCH.
func wrapHostKeyCheck(cb ssh.HostKeyCallback, path string, acceptNew bool) ssh.HostKeyCallback {
	return func(hostname string, remote net.Addr, key ssh.PublicKey) error {
		err := cb(hostname, remote, key)
		if err == nil {
			return nil
		}
		var ke *knownhosts.KeyError
		if !errors.As(err, &ke) {
			return err
		}
		if len(ke.Want) > 0 {
			return newToolError(CodeSSHHostKeyMismatch, fmt.Sprintf(
				"Host key verification failed for %s: remote %s key does not match known_hosts. Remove the stale line from %s, or set \"hostKeyCheck\": \"none\" to disable verification",
				hostname, key.Type(), path), false)
		}
		if !acceptNew {
			return newToolError(CodeSSHHostKeyUnknown, fmt.Sprintf(
				"Host key for %s (%s) is not in %s. Add it to known_hosts, or set \"hostKeyCheck\": \"accept-new\" to trust and record new hosts",
				hostname, key.Type(), path), false)
		}
		line := knownhosts.Line([]string{hostname}, key) + "\n"
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			return err
		}
		if _, err := f.WriteString(line); err != nil {
			f.Close()
			return err
		}
		f.Close()
		// Rebuild the in-memory DB from the file so repeat checks hit
		// instead of appending duplicates.
		if newCB, err := knownhosts.New(path); err == nil {
			cb = newCB
		}
		return nil
	}
}

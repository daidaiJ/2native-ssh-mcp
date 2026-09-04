package manager

import (
	"context"
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

var (
	ansiOSCPattern     = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiCSIPattern     = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
	ansiCharsetPattern = regexp.MustCompile(`\x1b\([B0]`)
	// Matches the exit code printed right after the end marker prefix.
	shellExitCodePattern = regexp.MustCompile(`^(-?\d+)__(?:\r)?\n`)
)

// shellSession is a persistent interactive shell used in shell transport
// mode. Commands are serialized and framed with unique markers.
type shellSession struct {
	session *ssh.Session
	stdin   io.WriteCloser

	mu     sync.Mutex
	cond   *sync.Cond
	buffer string
	ready  bool
	closed bool
}

func (sh *shellSession) close() {
	sh.mu.Lock()
	if sh.closed {
		sh.mu.Unlock()
		return
	}
	sh.closed = true
	if sh.cond != nil {
		sh.cond.Broadcast()
	}
	session := sh.session
	sh.mu.Unlock()
	if session != nil {
		session.Close()
	}
}

// initShell opens a persistent shell and probes it until it responds.
func (m *Manager) initShell(key string, client *ssh.Client, cfg *config.SSHConfig) error {
	sh, err := newShellSession(client, cfg)
	if err != nil {
		return err
	}
	sh.ready = true
	m.mu.Lock()
	m.shells[key] = sh
	m.mu.Unlock()
	logger.Info("Shell transport initialized for [%s]", key)
	return nil
}

// newShellSession opens and probes a persistent interactive shell.
func newShellSession(client *ssh.Client, cfg *config.SSHConfig) (*shellSession, error) {
	session, err := client.NewSession()
	if err != nil {
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport: %v", err), true)
	}
	if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
		session.Close()
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport: %v", err), true)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport: %v", err), true)
	}

	// Merge stdout and stderr into a single stream, matching the raw channel
	// behaviour of the reference implementation.
	pr, pw := io.Pipe()
	session.Stdout = pw
	session.Stderr = pw

	if err := session.Shell(); err != nil {
		session.Close()
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport: %v", err), true)
	}

	sh := &shellSession{session: session, stdin: stdin}
	sh.cond = sync.NewCond(&sh.mu)

	go sh.readLoop(pr)

	readyMarker := fmt.Sprintf("__MCP_READY__%s__", randomID("ready"))
	payload := fmt.Sprintf("printf '%s\\n'\n", readyMarker)
	readyTimeout := time.Duration(cfg.ShellReadyTimeoutMs) * time.Millisecond

	if err := sh.waitForReady(readyMarker, payload, readyTimeout); err != nil {
		sh.close()
		return nil, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Shell transport initialization failed: %v", err), true)
	}

	sh.configure()
	return sh, nil
}

// readLoop drains the merged shell output into the buffer.
func (sh *shellSession) readLoop(r io.Reader) {
	buf := make([]byte, 32*1024)
	for {
		n, err := r.Read(buf)
		if n > 0 {
			sh.mu.Lock()
			sh.buffer += string(buf[:n])
			sh.cond.Broadcast()
			sh.mu.Unlock()
		}
		if err != nil {
			sh.mu.Lock()
			sh.closed = true
			sh.cond.Broadcast()
			sh.mu.Unlock()
			return
		}
	}
}

// waitForReady probes the shell until the ready marker appears.
func (sh *shellSession) waitUntil(deadline time.Time) {
	d := time.Until(deadline)
	if d <= 0 {
		return
	}
	timer := time.AfterFunc(d, func() {
		sh.mu.Lock()
		sh.cond.Broadcast()
		sh.mu.Unlock()
	})
	defer timer.Stop()
	sh.cond.Wait()
}

// waitForReady probes the shell until the ready marker appears.
func (sh *shellSession) waitForReady(marker, payload string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	stopProbe := make(chan struct{})
	defer close(stopProbe)

	if _, err := sh.stdin.Write([]byte(payload)); err != nil {
		return err
	}

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopProbe:
				return
			case <-ticker.C:
				sh.mu.Lock()
				closed := sh.closed
				sh.mu.Unlock()
				if closed {
					return
				}
				_, _ = sh.stdin.Write([]byte(payload))
				sh.mu.Lock()
				sh.cond.Broadcast()
				sh.mu.Unlock()
			}
		}
	}()

	sh.mu.Lock()
	defer sh.mu.Unlock()
	for {
		if idx := strings.Index(sh.buffer, marker); idx != -1 {
			if lineEnd := strings.Index(sh.buffer[idx:], "\n"); lineEnd != -1 {
				sh.buffer = sh.buffer[idx+lineEnd+1:]
				return nil
			}
		}
		if sh.closed {
			return fmt.Errorf("shell channel closed before ready probe completed")
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("timed out waiting for shell ready marker after %s", timeout)
		}
		sh.waitUntil(deadline)
	}
}

// configure disables the prompt and echo so output stays clean.
func (sh *shellSession) configure() {
	sh.stdin.Write([]byte("export PS1=''\n"))
	sh.stdin.Write([]byte("stty -echo >/dev/null 2>&1 || true\n"))
}

// runShellCommand executes a command on the persistent shell, serialized per
// connection, and returns a structured result.
func (m *Manager) runShellCommand(ctx context.Context, key, cmdString, directory string, timeout time.Duration) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	sh := m.shells[key]
	m.mu.Unlock()
	if sh == nil || !sh.ready {
		return CommandResult{}, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Shell transport for [%s] is not ready", key), true)
	}

	cfg, err := m.getConfig(key)
	if err != nil {
		return CommandResult{}, err
	}

	commandID := randomID("command")
	script := buildShellScript(commandID, cmdString, directory, cfg.CommandTemplate)

	maxOutput := cfg.MaxOutputBytes
	deadline := time.Now().Add(timeout)
	stop := context.AfterFunc(ctx, func() {
		sh.mu.Lock()
		sh.cond.Broadcast()
		sh.mu.Unlock()
	})
	defer stop()

	return m.runShellScriptOnce(ctx, sh, script, timeout, deadline, maxOutput, cfg)
}

func buildShellScript(commandID, cmdString, directory, commandTemplate string) string {
	beginMarker := "__MCP_BEGIN__" + commandID + "__"
	endMarker := "__MCP_END__" + commandID + "__RC__"

	body := cmdString
	if directory != "" {
		body = "cd -- " + shellQuote(directory) + " && { " + cmdString + "; }"
	} else {
		body = "{ " + cmdString + "; }"
	}
	if commandTemplate != "" {
		body = applyCommandTemplate(commandTemplate, body)
	}

	return fmt.Sprintf("printf '%s\\n'\n%s\n__mcp_rc=$?\nprintf '\\n%s%%s__\\n' \"$__mcp_rc\"\n",
		beginMarker, body, endMarker)
}

// stripANSI removes CSI and OSC escape sequences (plus the common charset
// select ESC(B) from output. It is idempotent and safe to apply twice.
func stripANSI(s string) string {
	if strings.IndexByte(s, 0x1b) < 0 {
		return s // every ANSI sequence starts with ESC
	}
	s = ansiOSCPattern.ReplaceAllString(s, "")
	s = ansiCSIPattern.ReplaceAllString(s, "")
	return ansiCharsetPattern.ReplaceAllString(s, "")
}

// cleanShellOutput strips ANSI escape sequences and normalizes line endings.
func cleanShellOutput(output string) string {
	output = stripANSI(output)
	output = strings.ReplaceAll(output, "\r\n", "\n")
	return strings.ReplaceAll(output, "\r", "\n")
}
package manager

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

const (
	maxNamedSessionsPerConnection = 5
	defaultSessionIdleTTL         = 10 * time.Minute
	bgSessionIdleTTL              = 60 * time.Minute
)

var sessionNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,63}$`)

// SessionInfo summarizes an active named session.
type SessionInfo struct {
	Name           string `json:"name"`
	ConnectionName string `json:"connectionName"`
	CWD            string `json:"cwd,omitempty"`
	IdleSeconds    int64  `json:"idleSeconds"`
	Background     bool   `json:"background,omitempty"`
	Running        bool   `json:"running,omitempty"`
	BGCommand      string `json:"bgCommand,omitempty"`
}

// namedSession is a persistent shell scoped by name; CWD and environment
// persist between run-in-session calls on exec-mode connections.
type namedSession struct {
	name          string
	connectionKey string
	shell         *shellSession
	cwd           string
	lastUsed      time.Time
	idleTimer     *time.Timer
	background    bool
	bgCommand     string
	bgLogPath     string
	bgPIDPath     string
	bgOffset      int64
	bgRunning     bool
}

// OpenSession creates a named interactive session on an exec-mode connection.
func (m *Manager) OpenSession(sessionName, connectionName string) (*SessionInfo, error) {
	return m.openOrReuseSession(sessionName, connectionName)
}

// RunInSession executes a command in a named session, preserving CWD between calls.
func (m *Manager) RunInSession(ctx context.Context, sessionName, cmdString, directory string, opts RunOptions) (string, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return "", newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found; call session action=open first", sessionName), false)
	}

	if strings.TrimSpace(cmdString) == "" {
		return "", newToolError(CodeCommandValidationFailed, "cmdString must be a non-empty command", false)
	}
	if !opts.Prevalidated {
		if err := m.validateCommand(cmdString, ns.connectionKey); err != nil {
			return "", err
		}
	}

	cfg := m.configs[ns.connectionKey]
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Duration(cfg.CommandTimeoutMs) * time.Millisecond
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	output, exitCode, err := m.runNamedShellCommand(ctx, ns, cmdString, directory, cfg.CommandTemplate, timeout)
	success := err == nil
	m.RecordCommand(ns.connectionKey, cmdString, exitCode, success)

	ns.lastUsed = time.Now()
	ns.resetIdleTimer(m)

	keepAlive := true
	if opts.KeepAlive != nil {
		keepAlive = *opts.KeepAlive
	}
	duration := opts.KeepAliveDuration
	if duration <= 0 {
		duration = DefaultKeepAliveDuration
	}
	if keepAlive {
		m.Touch(ns.connectionKey, duration)
	}

	return output, err
}

// CloseSession closes a named session and its shell channel.
func (m *Manager) CloseSession(sessionName string) error {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	if !ok {
		m.mu.Unlock()
		return newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
	delete(m.sessions, sessionName)
	m.mu.Unlock()

	ns.stopIdleTimer()
	m.stopBackgroundProcess(ns)
	ns.shell.close()
	return nil
}

// ListSessions returns all active named sessions.
func (m *Manager) ListSessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, ns := range m.sessions {
		out = append(out, *m.sessionInfoLocked(ns))
	}
	return out
}

func (m *Manager) sessionInfoLocked(ns *namedSession) *SessionInfo {
	idle := int64(time.Since(ns.lastUsed).Seconds())
	return &SessionInfo{
		Name:           ns.name,
		ConnectionName: ns.connectionKey,
		CWD:            ns.cwd,
		IdleSeconds:    idle,
		Background:     ns.background,
		Running:        ns.bgRunning,
		BGCommand:      ns.bgCommand,
	}
}

func (ns *namedSession) resetIdleTimer(m *Manager) {
	if ns.idleTimer != nil {
		ns.idleTimer.Stop()
	}
	ttl := defaultSessionIdleTTL
	if ns.background {
		ttl = bgSessionIdleTTL
	}
	name := ns.name
	key := ns.connectionKey
	ns.idleTimer = time.AfterFunc(ttl, func() {
		logger.Info("Session [%s] idle for %s, closing", name, ttl)
		_ = m.CloseSession(name)
		m.Touch(key, DefaultKeepAliveDuration)
	})
}

func (ns *namedSession) stopIdleTimer() {
	if ns.idleTimer != nil {
		ns.idleTimer.Stop()
		ns.idleTimer = nil
	}
}

func (m *Manager) closeSessionsForConnection(connKey string) {
	m.mu.Lock()
	var names []string
	for name, ns := range m.sessions {
		if ns.connectionKey == connKey {
			names = append(names, name)
		}
	}
	m.mu.Unlock()
	for _, name := range names {
		_ = m.CloseSession(name)
	}
}

// runNamedShellCommand runs one command on a named session shell and updates cwd.
func (m *Manager) runNamedShellCommand(ctx context.Context, ns *namedSession, cmdString, directory, commandTemplate string, timeout time.Duration) (string, int, error) {
	sh := ns.shell
	cfg := m.configs[ns.connectionKey]
	maxOutput := 0
	if cfg != nil {
		maxOutput = cfg.MaxOutputBytes
	}
	deadline := time.Now().Add(timeout)
	stop := context.AfterFunc(ctx, func() {
		sh.mu.Lock()
		sh.cond.Broadcast()
		sh.mu.Unlock()
	})
	defer stop()

	script := buildNamedShellScript(cmdString, directory, commandTemplate)
	output, exitCode, err := m.runShellScriptOnce(ctx, sh, script, timeout, deadline, maxOutput, cfg)
	if err != nil {
		return output, exitCode, err
	}

	if pwd := extractShellPWD(output); pwd != "" {
		ns.cwd = pwd
	}
	output = stripShellPWDLine(output)
	return FinalizeCommandOutput(output, compressOpts(cfg)), exitCode, err
}

// buildNamedShellScript wraps the command and prints $PWD after execution.
func buildNamedShellScript(cmdString, directory, commandTemplate string) string {
	commandID := randomID("session")
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

	return fmt.Sprintf(
		"printf '%s\\n'\n%s\n__mcp_rc=$?\nprintf '__MCP_PWD__%%s__\\n' \"$PWD\"\nprintf '\\n%s%%s__\\n' \"$__mcp_rc\"\n",
		beginMarker, body, endMarker,
	)
}

var shellPWDPattern = regexp.MustCompile(`(?m)^__MCP_PWD__(.+?)__\r?\n`)

func extractShellPWD(output string) string {
	m := shellPWDPattern.FindStringSubmatch(output)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(m[1])
}

func stripShellPWDLine(output string) string {
	return strings.TrimSpace(shellPWDPattern.ReplaceAllString(output, ""))
}

// runShellScriptOnce executes a marker-framed script on sh and returns output.
func (m *Manager) runShellScriptOnce(ctx context.Context, sh *shellSession, script string, timeout time.Duration, deadline time.Time, maxOutput int, cfg *config.SSHConfig) (string, int, error) {
	commandID := extractCommandID(script)
	if commandID == "" {
		return "", -1, newToolError(CodeCommandExecutionError, "internal: missing command marker", false)
	}
	beginMarker := "__MCP_BEGIN__" + commandID + "__"
	endPrefix := "__MCP_END__" + commandID + "__RC__"

	sh.mu.Lock()
	held := true
	defer func() {
		if held {
			sh.mu.Unlock()
		}
	}()

	scanPos := len(sh.buffer)
	outputStart := -1
	countedEnd := scanPos
	capturedBytes := 0

	sh.stdin.Write([]byte(script))

	abort := func(err error) (string, int, error) {
		held = false
		sh.mu.Unlock()
		return "", -1, err
	}

	for {
		if outputStart == -1 {
			beginIdx := strings.Index(sh.buffer[scanPos:], beginMarker)
			if beginIdx == -1 {
				scanPos = maxInt(len(sh.buffer)-(len(beginMarker)-1), 0)
			} else {
				lineEnd := strings.Index(sh.buffer[scanPos+beginIdx:], "\n")
				if lineEnd == -1 {
					scanPos = maxInt(len(sh.buffer)-(len(beginMarker)-1), 0)
				} else {
					outputStart = scanPos + beginIdx + lineEnd + 1
					scanPos = outputStart
					countedEnd = outputStart
				}
			}
		}

		if outputStart != -1 {
			endIdx := strings.Index(sh.buffer[scanPos:], endPrefix)
			if endIdx != -1 {
				absEnd := scanPos + endIdx
				outputEnd := absEnd
				if outputEnd > outputStart && sh.buffer[outputEnd-1] == '\n' {
					outputEnd--
					if outputEnd > outputStart && sh.buffer[outputEnd-1] == '\r' {
						outputEnd--
					}
				}
				capturedBytes += len(sh.buffer[countedEnd:outputEnd])
				codeStart := absEnd + len(endPrefix)
				codeSlice := sh.buffer[codeStart:]
				if len(codeSlice) > 32 {
					codeSlice = codeSlice[:32]
				}
				matched := shellExitCodePattern.FindStringSubmatch(codeSlice)
				if matched != nil {
					consumedEnd := codeStart + len(matched[0])
					output := cleanShellOutput(sh.buffer[outputStart:outputEnd])
					sh.buffer = sh.buffer[consumedEnd:]
					exitCode := 0
					fmt.Sscanf(matched[1], "%d", &exitCode)
					output = strings.TrimSpace(strings.TrimPrefix(output, beginMarker+"\n"))
					output = FinalizeCommandOutput(output, compressOpts(cfg))
					if exitCode != 0 {
						return output, exitCode, newToolError(CodeCommandExecutionError,
							formatCommandFailure(output, "", exitCode, "", cfg), false)
					}
					return output, 0, nil
				}
				scanPos = absEnd
				continue
			}
			capturedBytes += len(sh.buffer[countedEnd:scanPos])
			countedEnd = scanPos
		}

		if maxOutput > 0 && capturedBytes > maxOutput {
			interruptShell(sh)
			return abort(newToolError(CodeOutputLimitExceeded,
				fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false))
		}

		if sh.closed {
			return "", -1, newToolError(CodeCommandExecutionError,
				"Shell channel closed during command execution", true)
		}
		if err := ctx.Err(); err != nil {
			interruptShell(sh)
			return abort(ctxToolError(ctx, CodeCommandTimeout,
				fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds())))
		}
		if time.Now().After(deadline) {
			interruptShell(sh)
			return abort(newToolError(CodeCommandTimeout,
				fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds()), true))
		}
		sh.waitUntil(deadline)
	}
}

func extractCommandID(script string) string {
	const prefix = "__MCP_BEGIN__"
	idx := strings.Index(script, prefix)
	if idx < 0 {
		return ""
	}
	rest := script[idx+len(prefix):]
	end := strings.Index(rest, "__")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// signalRemoteProcess attempts to terminate a remote exec-session process.
func signalRemoteProcess(session *ssh.Session, done <-chan error) {
	if session == nil {
		return
	}
	_ = session.Signal(ssh.SIGTERM)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		_ = session.Signal(ssh.SIGKILL)
	}
}

// interruptShell sends Ctrl-C to the foreground process in a shell session.
func interruptShell(sh *shellSession) {
	if sh == nil || sh.stdin == nil {
		return
	}
	_, _ = sh.stdin.Write([]byte("\x03"))
}

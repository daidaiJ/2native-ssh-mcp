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
	// Disconnected is true when the underlying SSH connection dropped but
	// the logical session (and any remote background job) survives.
	Disconnected bool `json:"disconnected,omitempty"`
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
	bgExitPath    string
	bgOffset      int64
	bgRunning     bool
	// disconnected is true when the physical connection dropped; the shell
	// is nil and will be recreated on next use.
	disconnected bool
}

// OpenSession creates a named interactive session on an exec-mode connection.
func (m *Manager) OpenSession(sessionName, connectionName string) (*SessionInfo, error) {
	return m.openOrReuseSession(sessionName, connectionName)
}

// ensureShell returns a usable shell for the session, reconnecting and
// recreating the shell channel if the connection was dropped. The saved CWD
// is restored on a fresh shell.
func (m *Manager) ensureShell(ns *namedSession) (*shellSession, error) {
	m.mu.Lock()
	sh := ns.shell
	disconnected := ns.disconnected
	m.mu.Unlock()
	if sh != nil && !disconnected {
		return sh, nil
	}

	client, err := m.EnsureConnected(ns.connectionKey)
	if err != nil {
		return nil, err
	}
	cfg := m.configs[ns.connectionKey]
	sh, err = newShellSession(client, cfg)
	if err != nil {
		return nil, err
	}

	if ns.cwd != "" {
		restore := "cd -- " + shellQuote(ns.cwd) + " || true"
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_, _ = m.runShellScriptOnce(ctx, sh, buildNamedShellScript(restore, "", ""), 15*time.Second, time.Now().Add(15*time.Second), 4096, cfg)
	}

	m.mu.Lock()
	ns.shell = sh
	ns.disconnected = false
	m.mu.Unlock()
	return sh, nil
}

// RunInSession executes a command in a named session, preserving CWD between calls.
func (m *Manager) RunInSession(ctx context.Context, sessionName, cmdString, directory string, opts RunOptions) (CommandResult, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return CommandResult{}, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found; call session action=open first", sessionName), false)
	}
	m.beginOp(ns.connectionKey)
	defer m.endOp(ns.connectionKey)

	if strings.TrimSpace(cmdString) == "" {
		return CommandResult{}, newToolError(CodeCommandValidationFailed, "cmdString must be a non-empty command", false)
	}
	if !opts.Prevalidated {
		if err := m.validateCommand(cmdString, ns.connectionKey); err != nil {
			return CommandResult{}, err
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

	if _, err := m.ensureShell(ns); err != nil {
		return CommandResult{}, err
	}

	result, err := m.runNamedShellCommand(ctx, ns, cmdString, directory, cfg.CommandTemplate, timeout)
	m.RecordCommand(ns.connectionKey, cmdString, result.ExitCode, result.ExitCode == 0)

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

	return result, err
}

// CloseSession closes a named session: stops its background job and shell
// channel, then removes it. This is the explicit close path; connection
// teardown does not go through here (see closeSessionsForConnection).
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
	if ns.shell != nil {
		ns.shell.close()
	}
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
		Disconnected:   ns.disconnected,
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

// closeSessionsForConnection tears down the physical shell channels of the
// sessions on a connection when it is disconnected. Logical sessions and
// remote background jobs survive: the shell is set to nil and the session is
// marked disconnected so it can be re-attached on next use. This must not
// call stopBackgroundProcess (which would grab the shell lock held by a
// running command) nor delete the session.
func (m *Manager) closeSessionsForConnection(connKey string) {
	m.mu.Lock()
	var shells []*shellSession
	for _, ns := range m.sessions {
		if ns.connectionKey != connKey {
			continue
		}
		if ns.shell != nil {
			shells = append(shells, ns.shell)
			ns.shell = nil
		}
		ns.disconnected = true
	}
	m.mu.Unlock()
	for _, sh := range shells {
		sh.close()
	}
}

// runNamedShellCommand runs one command on a named session shell and updates cwd.
func (m *Manager) runNamedShellCommand(ctx context.Context, ns *namedSession, cmdString, directory, commandTemplate string, timeout time.Duration) (CommandResult, error) {
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
	result, err := m.runShellScriptOnce(ctx, sh, script, timeout, deadline, maxOutput, cfg)
	if err != nil {
		return result, err
	}

	// The PWD line is the last line of the captured output; extract it
	// before the result is returned so the session CWD stays in sync.
	if pwd := extractShellPWD(result.Stdout); pwd != "" {
		ns.cwd = pwd
	}
	result.Stdout = stripShellPWDLine(result.Stdout)
	return result, nil
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

// runShellScriptOnce executes a marker-framed script on sh and returns a
// structured result. A non-zero exit is a normal result (Status=exited).
func (m *Manager) runShellScriptOnce(ctx context.Context, sh *shellSession, script string, timeout time.Duration, deadline time.Time, maxOutput int, cfg *config.SSHConfig) (CommandResult, error) {
	commandID := extractCommandID(script)
	if commandID == "" {
		return CommandResult{}, newToolError(CodeCommandExecutionError, "internal: missing command marker", false)
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

	abort := func(res CommandResult, err error) (CommandResult, error) {
		held = false
		sh.mu.Unlock()
		return res, err
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
					if exitCode != 0 {
						return buildCommandResult(output, "", exitCode, StatusExited, cfg), nil
					}
					return buildCommandResult(output, "", 0, StatusOK, cfg), nil
				}
				scanPos = absEnd
				continue
			}
			capturedBytes += len(sh.buffer[countedEnd:scanPos])
			countedEnd = scanPos
		}

		if maxOutput > 0 && capturedBytes > maxOutput {
			interruptShell(sh)
			return abort(buildCommandResult(partialShellOutput(sh, outputStart), "", -1, StatusOutputLimit, cfg),
				newToolError(CodeOutputLimitExceeded,
					fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false))
		}

		if sh.closed {
			return abort(buildCommandResult(partialShellOutput(sh, outputStart), "", -1, StatusConnectionLost, cfg),
				newToolError(CodeSSHConnectionLost,
					"SSH connection dropped during command; the remote process may still be running. Do not replay blindly.", false))
		}
		if err := ctx.Err(); err != nil {
			interruptShell(sh)
			return abort(buildCommandResult(partialShellOutput(sh, outputStart), "", -1, StatusTimeout, cfg),
				ctxToolError(ctx, CodeCommandTimeout,
					fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds())))
		}
		if time.Now().After(deadline) {
			interruptShell(sh)
			return abort(buildCommandResult(partialShellOutput(sh, outputStart), "", -1, StatusTimeout, cfg),
				newToolError(CodeCommandTimeout,
					fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds()), true))
		}
		sh.waitUntil(deadline)
	}
}

// partialShellOutput returns the un-consumed shell output from outputStart to
// the end of the buffer, cleaned of ANSI escapes.
func partialShellOutput(sh *shellSession, outputStart int) string {
	if outputStart < 0 || outputStart >= len(sh.buffer) {
		return ""
	}
	return cleanShellOutput(sh.buffer[outputStart:])
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

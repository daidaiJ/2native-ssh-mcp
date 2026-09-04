package manager

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

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
	// LogPath is the remote log file of a background job; it survives the
	// session so output can be re-read after a process restart.
	LogPath string `json:"logPath,omitempty"`
	// ExitCode is the background job's exit code once it has finished
	// (nil while running or unknown).
	ExitCode *int `json:"exitCode,omitempty"`
	// Disconnected is true when the underlying SSH connection dropped but
	// the logical session (and any remote background job) survives.
	Disconnected bool `json:"disconnected,omitempty"`
	// Orphaned is true when a close could not confirm that the remote
	// background job stopped (connection unavailable); the session stays
	// visible until a later close succeeds.
	Orphaned bool `json:"orphaned,omitempty"`
}

// namedSession is a persistent shell scoped by name; CWD and environment
// persist between run-in-session calls on exec-mode connections.
//
// Concurrency contract: every mutable field (shell, cwd, bgOffset,
// background, bgRunning, disconnected) is read or written only while holding
// m.mu, or copied to a local variable under the lock before use. Never touch
// these fields after unlocking.
type namedSession struct {
	name          string
	connectionKey string
	shell         *shellSession
	cwd           string
	lastUsed      time.Time
	idleTimer     *time.Timer
	// idleGen invalidates in-flight idle expire callbacks: every
	// resetIdleTimer bumps it, and an expire callback only acts when its
	// generation still matches. Read/written under m.mu.
	idleGen    uint64
	background bool
	bgCommand  string
	bgLogPath  string
	bgPIDPath  string
	bgExitPath string
	bgOffset   int64
	bgRunning  bool
	// bgExitCode is the job's exit code once the remote .exit file is
	// readable (nil while running or unknown).
	bgExitCode *int
	// disconnected is true when the physical connection dropped; the shell
	// is nil and will be recreated on next use.
	disconnected bool
	// orphaned is true when a close could not confirm the remote background
	// job stopped; the session is kept in the map until a close succeeds.
	orphaned bool
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
	cwd := ns.cwd
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

	if cwd != "" {
		restore := "cd -- " + shellQuote(cwd) + " || true"
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
// Closing a session that does not exist (or was already closed by the idle
// TTL) is a no-op success so repeated close calls are idempotent.
//
// The session is only removed from the map when the background stop is
// confirmed; otherwise it stays visible (orphaned) so list/read still see it
// and a later close can retry.
func (m *Manager) CloseSession(sessionName string) error {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	if !ok {
		m.mu.Unlock()
		return nil
	}
	ns.idleGen++ // invalidate any in-flight expire callback
	if ns.idleTimer != nil {
		ns.idleTimer.Stop()
		ns.idleTimer = nil
	}
	sh := ns.shell
	ns.shell = nil
	m.mu.Unlock()

	if !m.stopBackgroundProcess(ns) {
		// Remote job state unconfirmed: keep the session so a later close
		// (after the connection is back) can retry the stop.
		ns.resetIdleTimer(m)
		if sh != nil {
			sh.close()
		}
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("session %q: could not confirm the remote background job stopped (connection unavailable); the job may still be running. Reconnect and close again", sessionName), true)
	}
	m.mu.Lock()
	delete(m.sessions, sessionName)
	m.mu.Unlock()
	if sh != nil {
		sh.close()
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
		LogPath:        ns.bgLogPath,
		ExitCode:       ns.bgExitCode,
		Disconnected:   ns.disconnected,
		Orphaned:       ns.orphaned,
	}
}

// resetIdleTimer (re)arms the session's idle expiry and refreshes lastUsed.
// Every call bumps the generation so a previously scheduled expire callback
// can no longer act on this session. All idle fields are updated under m.mu.
func (ns *namedSession) resetIdleTimer(m *Manager) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ns.lastUsed = time.Now()
	ns.idleGen++
	gen := ns.idleGen
	ttl := defaultSessionIdleTTL
	if ns.background {
		ttl = bgSessionIdleTTL
	}
	name := ns.name
	ns.idleTimer = time.AfterFunc(ttl, func() {
		m.expireSessionIfIdle(name, gen)
	})
}

// expireSessionIfIdle is the idle TTL callback. It only acts when the
// session still exists and the generation still matches (i.e. the timer was
// not reset or the session closed in the meantime). A running background job
// is never closed by the idle path: the timer is simply re-armed.
func (m *Manager) expireSessionIfIdle(name string, gen uint64) {
	m.mu.Lock()
	ns := m.sessions[name]
	if ns == nil || ns.idleGen != gen {
		m.mu.Unlock()
		return // reset or already closed
	}
	if ns.bgRunning {
		// Job still running: renew the timer, never close or kill it.
		m.mu.Unlock()
		ns.resetIdleTimer(m)
		return
	}
	ttl := defaultSessionIdleTTL
	if ns.background {
		ttl = bgSessionIdleTTL
	}
	key := ns.connectionKey
	m.mu.Unlock()
	logger.Info("Session [%s] idle for %s, closing", name, ttl)
	_ = m.CloseSession(name)
	m.Touch(key, DefaultKeepAliveDuration)
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
	m.mu.Lock()
	sh := ns.shell
	m.mu.Unlock()
	if sh == nil {
		return CommandResult{}, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("session %q shell is not available (connection dropped); retry", ns.name), true)
	}
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

	// The PWD was extracted from the raw output inside runShellScriptOnce
	// (before trimming would remove the line separator the regex needs) and
	// travels in result.CWD so the session stays in sync even when the
	// output itself was spilled to a file or compressed.
	if result.CWD != "" {
		m.mu.Lock()
		ns.cwd = result.CWD
		m.mu.Unlock()
	}
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
	return shellPWDPattern.ReplaceAllString(output, "")
}

// finalizeShellOutput strips the __MCP_PWD__ line from the raw shell output —
// before any trimming can remove the trailing newline the PWD regex needs —
// then trims and wraps the cleaned output in a CommandResult that carries the
// PWD in CWD. This keeps the session CWD working even when the output is
// later spilled to a file or compressed.
func finalizeShellOutput(output string, exitCode int, status string, cfg *config.SSHConfig) CommandResult {
	pwd := extractShellPWD(output)
	if pwd != "" {
		output = stripShellPWDLine(output)
	}
	output = strings.TrimSpace(output)
	res := buildCommandResult(output, "", exitCode, status, cfg)
	res.CWD = pwd
	return res
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
					output = strings.TrimPrefix(output, beginMarker+"\n")
					if exitCode != 0 {
						return finalizeShellOutput(output, exitCode, StatusExited, cfg), nil
					}
					return finalizeShellOutput(output, 0, StatusOK, cfg), nil
				}
				scanPos = absEnd
				continue
			}
			capturedBytes += len(sh.buffer[countedEnd:scanPos])
			countedEnd = scanPos
		}

		if maxOutput > 0 && capturedBytes > maxOutput {
			interruptShell(sh)
			res := finalizeShellOutput(partialShellOutput(sh, outputStart), -1, StatusOutputLimit, cfg)
			res.Truncated = true // the tail stayed in the remote pipe; clipped size unknown
			return abort(res,
				newToolError(CodeOutputLimitExceeded,
					fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false))
		}

		if sh.closed {
			return abort(finalizeShellOutput(partialShellOutput(sh, outputStart), -1, StatusConnectionLost, cfg),
				newToolError(CodeSSHConnectionLost,
					"SSH connection dropped during command; the remote process may still be running. Do not replay blindly.", false))
		}
		if err := ctx.Err(); err != nil {
			interruptShell(sh)
			return abort(finalizeShellOutput(partialShellOutput(sh, outputStart), -1, StatusTimeout, cfg),
				ctxToolError(ctx, CodeCommandTimeout,
					fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds())))
		}
		if time.Now().After(deadline) {
			interruptShell(sh)
			return abort(finalizeShellOutput(partialShellOutput(sh, outputStart), -1, StatusTimeout, cfg),
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

// interruptShell sends Ctrl-C to the foreground process in a shell session.
func interruptShell(sh *shellSession) {
	if sh == nil || sh.stdin == nil {
		return
	}
	_, _ = sh.stdin.Write([]byte("\x03"))
}

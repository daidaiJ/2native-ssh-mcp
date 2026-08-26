package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

const (
	defaultBGReadBytes = 64 * 1024
	bgLogPrefix        = "/tmp/.2native-ssh-mcp-"
)

var bgStartedPattern = regexp.MustCompile(`(?m)^__MCP_BG_STARTED__ pid=(\d+)\r?\n`)

// SessionOpenOptions controls optional background start when opening a session.
type SessionOpenOptions struct {
	Background bool
	CmdString  string
	Directory  string
}

// SessionOutput holds polled background session output.
type SessionOutput struct {
	SessionName string `json:"sessionName"`
	Output      string `json:"output"`
	Offset      int64  `json:"offset"`
	TotalBytes  int64  `json:"totalBytes"`
	Running     bool   `json:"running"`
	// LogPath is the remote log file; it survives the session so output can
	// be re-read (e.g. with offset=0) or recovered after a restart.
	LogPath string `json:"logPath,omitempty"`
	// ExitCode is the job's exit code once it has finished (nil while
	// running or unknown).
	ExitCode *int `json:"exitCode,omitempty"`
}

// OpenSessionWithOptions creates or reuses a named session. When opts.Background
// is true, cmdString must be set and the command runs detached to a remote log.
func (m *Manager) OpenSessionWithOptions(sessionName, connectionName string, opts SessionOpenOptions) (*SessionInfo, error) {
	info, err := m.openOrReuseSession(sessionName, connectionName)
	if err != nil {
		return nil, err
	}
	if !opts.Background {
		return info, nil
	}
	if strings.TrimSpace(opts.CmdString) == "" {
		return nil, newToolError(CodeCommandValidationFailed,
			"cmdString is required when background is true", false)
	}
	m.mu.Lock()
	ns := m.sessions[sessionName]
	if ns != nil && ns.background {
		if ns.bgRunning {
			out := m.sessionInfoLocked(ns)
			m.mu.Unlock()
			return out, nil
		}
		// Finished job: refuse to restart it, otherwise the starter would
		// truncate the old log and the finished output would be lost.
		logPath := ns.bgLogPath
		m.mu.Unlock()
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q already exists (finished); close it first or read logPath=%s", sessionName, logPath), false)
	}
	m.mu.Unlock()
	return m.startBackgroundCommand(sessionName, opts.CmdString, opts.Directory)
}

// openOrReuseSession is the core of OpenSession without background start.
func (m *Manager) openOrReuseSession(sessionName, connectionName string) (*SessionInfo, error) {
	if !sessionNamePattern.MatchString(sessionName) {
		return nil, newToolError(CodeCommandValidationFailed,
			"sessionName must be 1-64 chars: letters, digits, '.', '-', '_'", false)
	}

	connKey := m.resolveName(connectionName)
	cfg, err := m.getConfig(connectionName)
	if err != nil {
		return nil, err
	}
	if cfg.TransportMode == "shell" {
		return nil, newToolError(CodeUnsupportedInShellMode,
			"Named sessions are not supported when transportMode is shell; use execute-command on the persistent connection shell instead", false)
	}

	m.mu.Lock()
	if existing, ok := m.sessions[sessionName]; ok {
		m.mu.Unlock()
		if _, err := m.ensureShell(existing); err != nil {
			return nil, err
		}
		m.mu.Lock()
		info := m.sessionInfoLocked(existing)
		m.mu.Unlock()
		return info, nil
	}
	count := 0
	for _, s := range m.sessions {
		if s.connectionKey == connKey {
			count++
		}
	}
	if count >= maxNamedSessionsPerConnection {
		m.mu.Unlock()
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("connection %q already has %d named sessions (max %d)", connKey, count, maxNamedSessionsPerConnection), false)
	}
	m.mu.Unlock()

	client, err := m.EnsureConnected(connKey)
	if err != nil {
		return nil, err
	}

	sh, err := newShellSession(client, cfg)
	if err != nil {
		return nil, err
	}

	ns := &namedSession{
		name:          sessionName,
		connectionKey: connKey,
		shell:         sh,
		lastUsed:      time.Now(),
		bgLogPath:     bgLogPrefix + sessionName + ".log",
		bgPIDPath:     bgLogPrefix + sessionName + ".pid",
		bgExitPath:    bgLogPrefix + sessionName + ".exit",
	}
	ns.resetIdleTimer(m)

	m.mu.Lock()
	m.sessions[sessionName] = ns
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()

	m.Touch(connKey, DefaultKeepAliveDuration)
	return info, nil
}

// startBackgroundCommand launches a command fully detached from the SSH
// channel: a fresh no-PTY exec runs a starter that setsid's the job into a
// new session with stdio redirected to a remote log, then exits. The job
// survives connection drops; read/stop re-attach via fresh exec channels.
func (m *Manager) startBackgroundCommand(sessionName, cmdString, directory string) (*SessionInfo, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
	m.beginOp(ns.connectionKey)
	defer m.endOp(ns.connectionKey)

	if err := m.validateCommand(cmdString, ns.connectionKey); err != nil {
		return nil, err
	}

	cfg := m.configs[ns.connectionKey]
	body := cmdString
	if directory != "" {
		body = "cd -- " + shellQuote(directory) + " && " + cmdString
	}
	if cfg != nil && cfg.CommandTemplate != "" {
		body = applyCommandTemplate(cfg.CommandTemplate, body)
	}

	client, err := m.EnsureConnected(ns.connectionKey)
	if err != nil {
		return nil, err
	}

	script := buildBGStarterScript(ns.bgLogPath, ns.bgPIDPath, ns.bgExitPath, body)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := m.runDetachedExec(ctx, client, script, 15*time.Second)
	if err != nil {
		return nil, err
	}

	if pid := parseBGStartedPID(output); pid == 0 {
		return nil, newToolError(CodeCommandExecutionError,
			"background command failed to start: the job is not alive on the remote host (check the command and that the log file is writable)", false)
	}

	m.mu.Lock()
	ns.background = true
	ns.bgCommand = cmdString
	ns.bgOffset = 0
	ns.bgRunning = true
	m.mu.Unlock()
	ns.resetIdleTimer(m)
	m.RecordCommand(ns.connectionKey, "[background] "+cmdString, 0, true)

	m.mu.Lock()
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()
	return info, nil
}

// buildBGStarterScript builds a POSIX sh script that starts a command fully
// detached from the SSH channel: setsid into a new session (no controlling
// tty), stdin from /dev/null, stdout/stderr appended to the log, and the job
// PID written to the pid file. The starter exits quickly after confirming the
// job is alive. The body is passed as a single-quoted argument and eval'd by
// the inner shell so embedded '&' and quoting survive.
func buildBGStarterScript(logPath, pidPath, exitPath, body string) string {
	return fmt.Sprintf(
		"LOG=%s\nPIDF=%s\nEXITF=%s\n"+
			"rm -f \"$PIDF\" \"$EXITF\"\n"+
			": > \"$LOG\"\n"+
			"setsid sh -c '\n"+
			"  trap \"\" HUP\n"+
			"  exec </dev/null\n"+
			"  exec >>\"$1\" 2>&1\n"+
			"  eval \"$2\"\n"+
			"  echo $? > \"$3\"\n"+
			"' _ \"$LOG\" %s \"$EXITF\" &\n"+
			"echo $! > \"$PIDF\"\n"+
			"sleep 1\n"+
			"PID=$(cat \"$PIDF\" 2>/dev/null)\n"+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then\n"+
			"  printf '__MCP_BG_STARTED__ pid=%%s\\n' \"$PID\"\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '__MCP_BG_FAILED__\\n'\n"+
			"exit 1\n",
		shellQuote(logPath), shellQuote(pidPath), shellQuote(exitPath), shellQuote(body),
	)
}

// parseBGStartedPID extracts the job PID from a starter's output.
func parseBGStartedPID(output string) int {
	m := bgStartedPattern.FindStringSubmatch(output)
	if len(m) < 2 {
		return 0
	}
	pid, _ := strconv.Atoi(m[1])
	return pid
}

// ReadSessionOutput returns bytes from a background session log. It runs on
// a fresh no-PTY exec channel so it works even when the named shell is gone
// (e.g. after a connection drop). A negative offset continues from the last
// read position; offset>=0 reads from that byte (0 re-reads from the start).
func (m *Manager) ReadSessionOutput(sessionName string, maxBytes int64, offset int64) (*SessionOutput, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
	if !ns.background {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q is not a background session; open with background=true first", sessionName), false)
	}
	m.beginOp(ns.connectionKey)
	defer m.endOp(ns.connectionKey)

	if maxBytes <= 0 {
		maxBytes = defaultBGReadBytes
	}
	if offset < 0 {
		offset = ns.bgOffset
	}

	client, err := m.EnsureConnected(ns.connectionKey)
	if err != nil {
		return nil, err
	}

	readScript := buildBGReadScript(ns.bgLogPath, ns.bgPIDPath, ns.bgExitPath, offset, maxBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := m.runDetachedExec(ctx, client, readScript, 30*time.Second)
	if err != nil {
		return nil, err
	}

	running, total, chunk, exitCode := parseBGReadOutput(output)
	m.mu.Lock()
	ns.bgOffset = offset + int64(len(chunk))
	ns.bgRunning = running
	ns.bgExitCode = exitCode
	m.mu.Unlock()
	ns.resetIdleTimer(m)

	cfg := m.configs[ns.connectionKey]
	if cfg == nil || cfg.GetStripAnsi() {
		chunk = stripANSI(chunk)
	}

	return &SessionOutput{
		SessionName: sessionName,
		Output:      redactCombinedOutput(chunk), // read path: chunk already partial; skip line compression
		Offset:      ns.bgOffset,
		TotalBytes:  total,
		Running:     running,
		LogPath:     ns.bgLogPath,
		ExitCode:    exitCode,
	}, nil
}

// buildBGReadScript builds a script that reports whether the job is running,
// the log size, the job exit code (empty while running or unknown), and the
// requested byte range of the log.
func buildBGReadScript(logPath, pidPath, exitPath string, offset, maxBytes int64) string {
	return fmt.Sprintf(
		"LOG=%s\nPIDF=%s\nEXITF=%s\n"+
			"RUN=0\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then RUN=1; fi; fi\n"+
			"SIZE=0\n"+
			"if [ -f \"$LOG\" ]; then SIZE=$(wc -c < \"$LOG\" | tr -d ' '); fi\n"+
			"EXIT=\n"+
			"if [ -f \"$EXITF\" ]; then EXIT=$(cat \"$EXITF\" 2>/dev/null | tr -d ' \\n'); fi\n"+
			"printf '__MCP_BG_HDR__running=%%s size=%%s exit=%%s\\n' \"$RUN\" \"$SIZE\" \"$EXIT\"\n"+
			"if [ -f \"$LOG\" ] && [ \"$SIZE\" -gt %d ]; then tail -c +%d \"$LOG\" | head -c %d; fi\n",
		shellQuote(logPath), shellQuote(pidPath), shellQuote(exitPath),
		offset, offset+1, maxBytes,
	)
}

func parseBGReadOutput(output string) (running bool, totalBytes int64, chunk string, exitCode *int) {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 {
		return false, 0, "", nil
	}
	header := lines[0]
	if idx := strings.Index(header, "__MCP_BG_HDR__"); idx >= 0 {
		header = header[idx+len("__MCP_BG_HDR__"):]
	}
	for _, part := range strings.Fields(header) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "running":
			running = kv[1] == "1"
		case "size":
			totalBytes, _ = strconv.ParseInt(kv[1], 10, 64)
		case "exit":
			if kv[1] != "" {
				if n, err := strconv.Atoi(kv[1]); err == nil {
					exitCode = &n
				}
			}
		}
	}
	if len(lines) > 1 {
		chunk = lines[1]
	}
	return running, totalBytes, chunk, exitCode
}

// stopBackgroundProcess terminates a background job (TERM, then KILL after a
// grace period) and removes its pid/log files. It runs on a fresh no-PTY exec
// channel and is only called on explicit session close.
func (m *Manager) stopBackgroundProcess(ns *namedSession) {
	if !ns.background {
		return
	}
	m.beginOp(ns.connectionKey)
	defer m.endOp(ns.connectionKey)

	client, err := m.EnsureConnected(ns.connectionKey)
	if err != nil {
		m.mu.Lock()
		ns.background = false
		ns.bgRunning = false
		m.mu.Unlock()
		return
	}
	script := buildBGStopScript(ns.bgPIDPath, ns.bgLogPath)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	_, _ = m.runDetachedExec(ctx, client, script, 15*time.Second)
	m.mu.Lock()
	ns.background = false
	ns.bgRunning = false
	m.mu.Unlock()
}

// buildBGStopScript builds a script that TERMs the job's process group, waits
// 2s, then KILLs it if still alive, and removes the pid/log files.
func buildBGStopScript(pidPath, logPath string) string {
	return fmt.Sprintf(
		"PIDF=%s\nLOG=%s\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then "+
			"kill -TERM -\"$PID\" 2>/dev/null || kill -TERM \"$PID\" 2>/dev/null; fi; fi\n"+
			"sleep 2\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then "+
			"kill -KILL -\"$PID\" 2>/dev/null || kill -KILL \"$PID\" 2>/dev/null; fi; fi\n"+
			"rm -f \"$PIDF\" \"$LOG\"\n",
		shellQuote(pidPath), shellQuote(logPath),
	)
}

// runDetachedExec runs a short-lived script on a fresh exec channel without
// a PTY, independent of any named shell. Used for background job
// start/read/stop so those operations never depend on (or block on) a
// persistent shell. A non-zero exit is not an error here; the caller inspects
// the output (e.g. the starter's __MCP_BG_STARTED__ marker).
//
// Stdout and stderr are captured into separate buffers: pointing both at the
// same buffer intermittently loses output (x/crypto/ssh dispatches both
// streams through the channel read loop).
func (m *Manager) runDetachedExec(ctx context.Context, client *ssh.Client, script string, timeout time.Duration) (string, error) {
	session, err := client.NewSession()
	if err != nil {
		return "", newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	if err := session.Start(script); err != nil {
		return "", newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}
	done := make(chan error, 1)
	go func() { done <- session.Wait() }()
	select {
	case waitErr := <-done:
		if waitErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(waitErr, &exitErr) {
				return combineDetachedOutput(stdout.String(), stderr.String()), nil
			}
			return combineDetachedOutput(stdout.String(), stderr.String()), newToolError(CodeCommandExecutionError,
				fmt.Sprintf("Command execution error: %v", waitErr), true)
		}
		return combineDetachedOutput(stdout.String(), stderr.String()), nil
	case <-ctx.Done():
		return combineDetachedOutput(stdout.String(), stderr.String()), ctxToolError(ctx, CodeCommandTimeout,
			fmt.Sprintf("Command timed out after %dms", timeout.Milliseconds()))
	}
}

// combineDetachedOutput joins stdout and stderr, keeping the stdout stream
// first so marker/header lines stay at the top.
func combineDetachedOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return strings.TrimSuffix(stdout, "\n") + "\n[stderr]\n" + stderr
}

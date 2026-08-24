package manager

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const (
	defaultBGReadBytes = 64 * 1024
	bgLogPrefix        = "/tmp/.2native-ssh-mcp-"
)

var bgStartedPattern = regexp.MustCompile(`(?m)^__MCP_BG_STARTED__\r?\n`)

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
	if ns != nil && ns.background && ns.bgRunning {
		out := m.sessionInfoLocked(ns)
		m.mu.Unlock()
		return out, nil
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
	}
	ns.resetIdleTimer(m)

	m.mu.Lock()
	m.sessions[sessionName] = ns
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()

	m.Touch(connKey, DefaultKeepAliveDuration)
	return info, nil
}

func (m *Manager) startBackgroundCommand(sessionName, cmdString, directory string) (*SessionInfo, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
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

	inner := fmt.Sprintf(
		"LOG=%s\nPID=%s\nrm -f \"$LOG\" \"$PID\"\n"+
			"nohup sh -c %s >> \"$LOG\" 2>&1 &\n"+
			"echo $! > \"$PID\"\n"+
			"printf '__MCP_BG_STARTED__\\n'\n",
		shellQuote(ns.bgLogPath), shellQuote(ns.bgPIDPath), shellQuote(body),
	)
	script := buildNamedShellScript(inner, "", "")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	timeout := 15 * time.Second
	maxOutput := 4096
	if cfg != nil && cfg.MaxOutputBytes > 0 && cfg.MaxOutputBytes < maxOutput {
		maxOutput = cfg.MaxOutputBytes
	}

	output, exitCode, err := m.runShellScriptOnce(ctx, ns.shell, script, timeout, time.Now().Add(timeout), maxOutput, cfg)
	if err != nil {
		return nil, err
	}
	if exitCode != 0 {
		return nil, newToolError(CodeCommandExecutionError,
			formatCommandFailure(output, "", exitCode, "", cfg), false)
	}
	if !bgStartedPattern.MatchString(output) {
		return nil, newToolError(CodeCommandExecutionError,
			"background command failed to start", true)
	}

	ns.background = true
	ns.bgCommand = cmdString
	ns.bgOffset = 0
	ns.bgRunning = true
	ns.lastUsed = time.Now()
	ns.resetIdleTimer(m)
	m.RecordCommand(ns.connectionKey, "[background] "+cmdString, 0, true)

	m.mu.Lock()
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()
	return info, nil
}

// ReadSessionOutput returns new bytes from a background session log.
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
	if maxBytes <= 0 {
		maxBytes = defaultBGReadBytes
	}
	if offset < 0 {
		offset = ns.bgOffset
	}

	readInner := fmt.Sprintf(
		"LOG=%s\nPIDF=%s\n"+
			"RUN=0\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then RUN=1; fi; fi\n"+
			"SIZE=0\n"+
			"if [ -f \"$LOG\" ]; then SIZE=$(wc -c < \"$LOG\" | tr -d ' '); fi\n"+
			"printf '__MCP_BG_HDR__running=%%s size=%%s\\n' \"$RUN\" \"$SIZE\"\n"+
			"if [ -f \"$LOG\" ] && [ \"$SIZE\" -gt %d ]; then tail -c +%d \"$LOG\" | head -c %d; fi\n",
		shellQuote(ns.bgLogPath), shellQuote(ns.bgPIDPath),
		offset, offset+1, maxBytes,
	)
	readScript := buildNamedShellScript(readInner, "", "")

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := m.configs[ns.connectionKey]
	maxOutput := int(maxBytes) + 512
	if cfg != nil && cfg.MaxOutputBytes > 0 {
		maxOutput = cfg.MaxOutputBytes
	}

	output, _, err := m.runShellScriptOnce(ctx, ns.shell, readScript, 30*time.Second, time.Now().Add(30*time.Second), maxOutput, cfg)
	if err != nil {
		return nil, err
	}

	running, total, chunk := parseBGReadOutput(output)
	ns.bgOffset = offset + int64(len(chunk))
	ns.bgRunning = running
	ns.lastUsed = time.Now()
	ns.resetIdleTimer(m)

	return &SessionOutput{
		SessionName: sessionName,
		Output:      redactCombinedOutput(chunk), // read path: chunk already partial; skip line compression
		Offset:      ns.bgOffset,
		TotalBytes:  total,
		Running:     running,
	}, nil
}

func parseBGReadOutput(output string) (running bool, totalBytes int64, chunk string) {
	lines := strings.SplitN(output, "\n", 2)
	if len(lines) == 0 {
		return false, 0, ""
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
		}
	}
	if len(lines) > 1 {
		chunk = lines[1]
	}
	return running, totalBytes, chunk
}

func (m *Manager) stopBackgroundProcess(ns *namedSession) {
	if !ns.background {
		return
	}
	inner := fmt.Sprintf(
		"PIDF=%s\nLOG=%s\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then kill \"$PID\" 2>/dev/null; fi; fi\n"+
			"rm -f \"$PIDF\" \"$LOG\"\n",
		shellQuote(ns.bgPIDPath), shellQuote(ns.bgLogPath),
	)
	script := buildNamedShellScript(inner, "", "")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, _, _ = m.runShellScriptOnce(ctx, ns.shell, script, 10*time.Second, time.Now().Add(10*time.Second), 4096, nil)
	ns.background = false
	ns.bgRunning = false
}

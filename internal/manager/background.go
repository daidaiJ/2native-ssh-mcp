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
	// bgLogHeadKeepBytes is how much of the beginning of a background job's
	// output is kept aside once its log has been trimmed: the first bytes say
	// what the job started doing, the newest bytes say what it just did, and
	// the middle is what a bounded log has to give up.
	bgLogHeadKeepBytes = 64 * 1024
	// bgLogChunkBytes is the read size of the bounded-capture loop.
	bgLogChunkBytes = 64 * 1024
	// maxBGWaitMs caps the session read waitMs parameter; bgWaitPollInterval
	// is the polling cadence while waiting for new output.
	maxBGWaitMs        = 30000
	bgWaitPollInterval = 200 * time.Millisecond
)

// bgPaths returns the five remote file paths of a background session: the
// bounded log, the job pid, the exit code, the kept beginning of the output
// and the dropped-byte counter. The one-time random id is shared by all of
// them so a predictable /tmp path cannot be symlink-preempted by another user.
func bgPaths(sessionName string) (logPath, pidPath, exitPath, headPath, dropPath string) {
	base := bgLogPrefix + sessionName + "-" + randomID("bg")
	return base + ".log", base + ".pid", base + ".exit", base + ".head", base + ".drop"
}

var bgStartedPattern = regexp.MustCompile(`(?m)^__MCP_BG_STARTED__ pid=(\d+)\r?\n`)

// SessionOpenOptions controls optional background start when opening a session.
type SessionOpenOptions struct {
	Background bool
	CmdString  string
	Directory  string
	// Pty wraps the detached job in a pseudo-terminal (util-linux `script`).
	// Nil uses the connection's pty setting; opt in for programs that refuse
	// to run without a TTY (their line buffering also disappears, on top of
	// PYTHONUNBUFFERED).
	Pty *bool
	// Approval is the destructive-command gate outcome for the audit record
	// of the detached command ("" means the gate did not need to ask).
	Approval string
	// AuditCommand is the readable equivalent of CmdString, used for the
	// whitelist/blacklist check and the audit record when the executed text
	// is a machine-generated rendering (argv mode, staged scriptFile).
	AuditCommand string
	// LintMode overrides the connection's lintMode for this detached command
	// ("off"/"warn"/"strict"); empty means use the connection's setting.
	LintMode string
	// Script is uploaded after whitelist/blacklist and lint pass, and
	// before the detached job starts.
	Script *ScriptUpload
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
	// DroppedBytes is how many bytes the remote side discarded from the
	// beginning of the log to keep it within backgroundLogMaxBytes; Output
	// then carries the kept beginning, an omission marker and the tail.
	DroppedBytes int64 `json:"droppedBytes,omitempty"`
	// LogMaxBytes is the cap in force on the remote log (0 when uncapped).
	LogMaxBytes int64 `json:"logMaxBytes,omitempty"`
	// Truncated is true when the requested maxBytes cut this read short:
	// Output is a prefix of what is available, and RemainingBytes more can
	// be fetched from Offset.
	Truncated bool `json:"truncated,omitempty"`
	// RemainingBytes is how many bytes of already-written output were left
	// unread after this chunk (0 when the read reached the end).
	RemainingBytes int64 `json:"remainingBytes,omitempty"`
	// ExitCode is the job's exit code once it has finished (nil while
	// running or unknown).
	ExitCode *int `json:"exitCode,omitempty"`
}

// OpenSessionWithOptions creates or reuses a named session. When opts.Background
// is true, cmdString must be set and the command runs detached to a remote log.
// The context carries the Elicitor for any dial this open triggers.
func (m *Manager) OpenSessionWithOptions(ctx context.Context, sessionName, connectionName string, opts SessionOpenOptions) (*SessionInfo, error) {
	info, err := m.openOrReuseSession(ctx, sessionName, connectionName)
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
	return m.startBackgroundCommand(ctx, sessionName, opts)
}

// openOrReuseSession is the core of OpenSession without background start.
func (m *Manager) openOrReuseSession(ctx context.Context, sessionName, connectionName string) (*SessionInfo, error) {
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
		if _, err := m.ensureShell(ctx, existing); err != nil {
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

	client, err := m.EnsureConnected(ctx, connKey)
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
	}
	// One-time random suffix shared by the five files, so a predictable
	// /tmp path cannot be symlink-preempted by another user.
	ns.bgLogPath, ns.bgPIDPath, ns.bgExitPath, ns.bgHeadPath, ns.bgDropPath = bgPaths(sessionName)
	ns.resetIdleTimer(m)

	m.mu.Lock()
	m.sessions[sessionName] = ns
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()

	m.recordSessionEvent(connKey, sessionName, "open")
	// A session holds its connection until it is closed or idles out, so
	// keepAliveMs: -1 does not apply here.
	if keepAliveMs := cfg.GetKeepAliveMs(); keepAliveMs > 0 {
		m.Touch(connKey, time.Duration(keepAliveMs)*time.Millisecond)
	} else {
		m.Touch(connKey, DefaultKeepAliveDuration)
	}
	return info, nil
}

// startBackgroundCommand launches a command fully detached from the SSH
// channel: a fresh no-PTY exec runs a starter that setsid's the job into a
// new session with stdio redirected to a remote log, then exits. The job
// survives connection drops; read/stop re-attach via fresh exec channels.
func (m *Manager) startBackgroundCommand(ctx context.Context, sessionName string, opts SessionOpenOptions) (*SessionInfo, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	m.mu.Unlock()
	if !ok {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
	m.beginOp(ns.connectionKey)
	defer m.endOp(ns.connectionKey)

	auditText := auditTextFor(opts.CmdString, opts.AuditCommand)
	if err := m.validateCommand(auditText, ns.connectionKey); err != nil {
		return nil, err
	}

	cfg := m.configs[ns.connectionKey]
	lintFindings, lintErr := m.checkCommandLint(opts.CmdString, LintModeFor(cfg, opts.LintMode))
	if lintErr != nil {
		return nil, lintErr
	}

	if err := m.stageUpload(ctx, ns.connectionKey, opts.Script); err != nil {
		return nil, err
	}

	body := opts.CmdString
	if opts.Directory != "" {
		body = "cd -- " + shellQuote(opts.Directory) + " && " + opts.CmdString
	}
	if cfg != nil && cfg.CommandTemplate != "" {
		body = applyCommandTemplate(cfg.CommandTemplate, body)
	}

	client, err := m.EnsureConnected(ctx, ns.connectionKey)
	if err != nil {
		return nil, err
	}

	usePty := cfg.GetPty()
	if opts.Pty != nil {
		usePty = *opts.Pty
	}
	script := buildBGStarterScript(bgJobSpec{
		logPath:     ns.bgLogPath,
		pidPath:     ns.bgPIDPath,
		exitPath:    ns.bgExitPath,
		headPath:    ns.bgHeadPath,
		dropPath:    ns.bgDropPath,
		body:        body,
		pty:         usePty,
		logMaxBytes: cfg.GetBackgroundLogMaxBytes(),
	})
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
	ns.bgCommand = opts.CmdString
	ns.bgOffset = 0
	ns.bgRunning = true
	m.mu.Unlock()
	ns.resetIdleTimer(m)
	m.recordAudit(ns.connectionKey, AuditRecord{
		Tool:     "execute-command",
		Command:  "[background] " + auditText,
		Session:  sessionName,
		Approval: opts.Approval,
		Success:  true,
	}, true)

	m.mu.Lock()
	info := m.sessionInfoLocked(ns)
	m.mu.Unlock()
	info.Lint = lintFindings
	return info, nil
}

// bgJobSpec is everything the background starter needs: where the job writes,
// how it is launched, and how much of its output may stay on the remote host.
type bgJobSpec struct {
	logPath  string
	pidPath  string
	exitPath string
	headPath string
	dropPath string
	body     string
	pty      bool
	// logMaxBytes caps the remote log; <= 0 keeps every byte (no trimming,
	// no head copy).
	logMaxBytes int
}

// buildBGStarterScript builds a POSIX sh script that starts a command fully
// detached from the SSH channel: setsid into a new session (no controlling
// tty), stdin from /dev/null, and the job's output piped through a bounded
// capture loop into the log. The starter exits quickly after confirming the
// job is alive. The body is passed as a single-quoted argument and eval'd by
// the inner shell so embedded '&' and quoting survive.
//
// The capture loop is what keeps a chatty job from filling the remote /tmp:
// once the log passes logMaxBytes the oldest bytes are trimmed off and
// counted in the drop file, while the first bgLogHeadKeepBytes are copied to
// the head file. A read reports both, so the agent sees the beginning, the
// newest bytes, and how much was discarded in between. Trimming happens in
// the capturing process, which is the only writer of the log, so no output
// can be lost to a race.
//
// With pty=true the job runs under util-linux `script` so it gets a
// pseudo-terminal (-e propagates the exit code, -f flushes output); without
// one, the body is eval'd directly. PYTHONUNBUFFERED is always exported so
// Python logs stream instead of block-buffering.
func buildBGStarterScript(spec bgJobSpec) string {
	flag := "0"
	if spec.pty {
		flag = "1"
	}
	headKeep := bgLogHeadKeepBytes
	if spec.logMaxBytes > 0 && spec.logMaxBytes < headKeep {
		headKeep = spec.logMaxBytes
	}
	return fmt.Sprintf(
		"LOG=%s\nPIDF=%s\nEXITF=%s\nHEADF=%s\nDROPF=%s\n"+
			"rm -f \"$PIDF\" \"$EXITF\" \"$DROPF\" \"$HEADF\"\n"+
			": > \"$LOG\"\n"+
			"setsid sh -c '\n"+
			"  LOG=$1; BODY=$2; EXITF=$3; HEADF=$4; DROPF=$5; CAP=$6; USE_PTY=$7\n"+
			"  trap \"\" HUP\n"+
			// Detach the channel's stdin/stdout/stderr: the job's output goes
			// through the pipe into the capture loop, and the exec channel
			// only closes (letting the starter return) once no process holds
			// its stdout any more.
			"  exec </dev/null >/dev/null 2>&1\n"+
			"  export PYTHONUNBUFFERED=1\n"+
			"  capture() {\n"+
			"    LOG=$1; HEADF=$2; DROPF=$3; CAP=$4\n"+
			"    CHUNK=$LOG.cap\n"+
			"    HEADLEN=0; DROPPED=0\n"+
			"    while dd bs=%d count=1 of=\"$CHUNK\" 2>/dev/null; do\n"+
			"      N=$(wc -c < \"$CHUNK\")\n"+
			"      [ \"$N\" -gt 0 ] || break\n"+
			"      if [ \"$HEADLEN\" -lt %d ]; then\n"+
			"        TAKE=$((%d - HEADLEN)); [ \"$N\" -lt \"$TAKE\" ] && TAKE=$N\n"+
			"        head -c \"$TAKE\" \"$CHUNK\" >> \"$HEADF\"\n"+
			"        HEADLEN=$((HEADLEN + TAKE))\n"+
			"      fi\n"+
			"      cat \"$CHUNK\" >> \"$LOG\"\n"+
			"      if [ \"$CAP\" -gt 0 ]; then\n"+
			"        SIZE=$(wc -c < \"$LOG\")\n"+
			"        if [ \"$SIZE\" -gt \"$CAP\" ]; then\n"+
			"          DROPPED=$((DROPPED + SIZE - CAP))\n"+
			"          tail -c \"$CAP\" \"$LOG\" > \"$CHUNK.tail\" && cat \"$CHUNK.tail\" > \"$LOG\"\n"+
			"          rm -f \"$CHUNK.tail\"\n"+
			// echo, not printf: the inner script is itself single-quoted in
			// the outer one, so a quote here would end that string early.
			"          echo \"$DROPPED\" > \"$DROPF\"\n"+
			"        fi\n"+
			"      fi\n"+
			"    done\n"+
			"    rm -f \"$CHUNK\"\n"+
			"  }\n"+
			"  ( if [ \"$USE_PTY\" = \"1\" ] && command -v script >/dev/null 2>&1; then\n"+
			"      script -qefc \"$BODY\" /dev/null\n"+
			"    else\n"+
			"      eval \"$BODY\"\n"+
			"    fi\n"+
			"    echo $? > \"$EXITF\"\n"+
			"  ) 2>&1 | capture \"$LOG\" \"$HEADF\" \"$DROPF\" \"$CAP\"\n"+
			"' _ %s %s %s %s %s %d %s &\n"+
			"echo $! > \"$PIDF\"\n"+
			"sleep 1\n"+
			"PID=$(cat \"$PIDF\" 2>/dev/null)\n"+
			// A job that already finished is a started job, not a failed one:
			// the exit file is written by the job itself and was removed
			// before the start, so its presence means this job ran.
			"if [ -f \"$EXITF\" ]; then\n"+
			"  printf '__MCP_BG_STARTED__ pid=%%s\\n' \"$PID\"\n"+
			"  exit 0\n"+
			"fi\n"+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then\n"+
			"  printf '__MCP_BG_STARTED__ pid=%%s\\n' \"$PID\"\n"+
			"  exit 0\n"+
			"fi\n"+
			"printf '__MCP_BG_FAILED__\\n'\n"+
			"exit 1\n",
		shellQuote(spec.logPath), shellQuote(spec.pidPath), shellQuote(spec.exitPath),
		shellQuote(spec.headPath), shellQuote(spec.dropPath),
		bgLogChunkBytes, headKeep, headKeep,
		shellQuote(spec.logPath), shellQuote(spec.body), shellQuote(spec.exitPath),
		shellQuote(spec.headPath), shellQuote(spec.dropPath), spec.logMaxBytes, flag,
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

// stopBackgroundProcess terminates a background job (TERM, then KILL after a
// grace period) and removes its pid/log files. It runs on a fresh no-PTY exec
// channel and is only called on explicit session close. It returns true when
// the stop was confirmed; otherwise the session is marked orphaned and the
// background state is kept so a later close can retry.
func (m *Manager) stopBackgroundProcess(ctx context.Context, ns *namedSession) bool {
	m.mu.Lock()
	background := ns.background
	connKey := ns.connectionKey
	pidPath := ns.bgPIDPath
	logPath := ns.bgLogPath
	headPath := ns.bgHeadPath
	dropPath := ns.bgDropPath
	m.mu.Unlock()
	if !background {
		return true
	}
	m.beginOp(connKey)
	defer m.endOp(connKey)

	client, err := m.EnsureConnected(ctx, connKey)
	if err != nil {
		m.mu.Lock()
		ns.orphaned = true
		m.mu.Unlock()
		return false
	}
	script := buildBGStopScript(pidPath, logPath, headPath, dropPath)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	output, err := m.runDetachedExec(ctx, client, script, 15*time.Second)
	if err != nil || strings.Contains(output, bgStopFailedMarker) {
		m.mu.Lock()
		ns.orphaned = true
		m.mu.Unlock()
		return false
	}
	m.mu.Lock()
	ns.background = false
	ns.bgRunning = false
	m.mu.Unlock()
	return true
}

// bgStopFailedMarker is printed by the stop script when the job is still
// alive after TERM+KILL, i.e. the stop could not be confirmed.
const bgStopFailedMarker = "__MCP_BG_STOP_FAILED__"

// buildBGStopScript builds a script that TERMs the job's process group, waits
// 2s, then KILLs it if still alive, verifies it is gone, and removes the
// session's remote files (pid, log, kept head, drop counter, capture chunk).
// It exits non-zero and prints a marker when the job survived.
func buildBGStopScript(pidPath, logPath, headPath, dropPath string) string {
	return fmt.Sprintf(
		"PIDF=%s\nLOG=%s\nHEADF=%s\nDROPF=%s\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then "+
			"kill -TERM -\"$PID\" 2>/dev/null || kill -TERM \"$PID\" 2>/dev/null; fi; fi\n"+
			"sleep 2\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then "+
			"kill -KILL -\"$PID\" 2>/dev/null || kill -KILL \"$PID\" 2>/dev/null; fi; fi\n"+
			"sleep 1\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then "+
			"printf '%s\\n'; exit 1; fi; fi\n"+
			"rm -f \"$PIDF\" \"$LOG\" \"$LOG.cap\" \"$LOG.cap.tail\" \"$HEADF\" \"$DROPF\"\n",
		shellQuote(pidPath), shellQuote(logPath), shellQuote(headPath), shellQuote(dropPath), bgStopFailedMarker,
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

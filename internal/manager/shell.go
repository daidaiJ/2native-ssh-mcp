package manager

import (
	"fmt"
	"io"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"ssh-mcp-server-go/internal/config"
	"ssh-mcp-server-go/internal/logger"
)

var (
	ansiOSCPattern = regexp.MustCompile(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	ansiCSIPattern = regexp.MustCompile(`\x1b\[[0-?]*[ -/]*[@-~]`)
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
	sh.closed = true
	sh.mu.Unlock()
	sh.session.Close()
}

// initShell opens a persistent shell and probes it until it responds.
func (m *Manager) initShell(key string, client *ssh.Client, cfg *config.SSHConfig) error {
	session, err := client.NewSession()
	if err != nil {
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport for [%s]: %v", key, err), true)
	}
	if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
		session.Close()
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport for [%s]: %v", key, err), true)
	}

	stdin, err := session.StdinPipe()
	if err != nil {
		session.Close()
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport for [%s]: %v", key, err), true)
	}

	// Merge stdout and stderr into a single stream, matching the raw channel
	// behaviour of the reference implementation.
	pr, pw := io.Pipe()
	session.Stdout = pw
	session.Stderr = pw

	if err := session.Shell(); err != nil {
		session.Close()
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Failed to initialize shell transport for [%s]: %v", key, err), true)
	}

	sh := &shellSession{session: session, stdin: stdin}
	sh.cond = sync.NewCond(&sh.mu)

	go sh.readLoop(pr)

	readyMarker := fmt.Sprintf("__MCP_READY__%s__", randomID("ready"))
	payload := fmt.Sprintf("printf '%s\\n'\n", readyMarker)
	readyTimeout := time.Duration(cfg.ShellReadyTimeoutMs) * time.Millisecond

	if err := sh.waitForReady(readyMarker, payload, readyTimeout); err != nil {
		sh.close()
		return newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Shell transport initialization failed for [%s]: %v", key, err), true)
	}

	sh.configure()
	m.mu.Lock()
	m.shells[key] = sh
	m.mu.Unlock()
	logger.Info("Shell transport initialized for [%s]", key)
	return nil
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
func (sh *shellSession) waitForReady(marker, payload string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	probe := time.NewTicker(time.Second)
	defer probe.Stop()

	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.stdin.Write([]byte(payload))

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
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for shell ready marker after %s", timeout)
		}
		select {
		case <-probe.C:
			sh.stdin.Write([]byte(payload))
		default:
		}
		sh.cond.Wait()
	}
}

// configure disables the prompt and echo so output stays clean.
func (sh *shellSession) configure() {
	sh.stdin.Write([]byte("export PS1=''\n"))
	sh.stdin.Write([]byte("stty -echo >/dev/null 2>&1 || true\n"))
}

// runShellCommand executes a command on the persistent shell, serialized per
// connection, and returns its output and exit code.
func (m *Manager) runShellCommand(key, cmdString, directory string, timeout time.Duration) (string, int, error) {
	m.mu.Lock()
	sh := m.shells[key]
	m.mu.Unlock()
	if sh == nil || !sh.ready {
		return "", -1, newToolError(CodeSSHConnectionFailed,
			fmt.Sprintf("Shell transport for [%s] is not ready", key), true)
	}

	cfg, err := m.getConfig(key)
	if err != nil {
		return "", -1, err
	}

	commandID := randomID("command")
	beginMarker := "__MCP_BEGIN__" + commandID + "__"
	endPrefix := "__MCP_END__" + commandID + "__RC__"
	script := buildShellScript(commandID, cmdString, directory, cfg.CommandTemplate)

	maxOutput := cfg.MaxOutputBytes

	sh.mu.Lock()
	defer sh.mu.Unlock()

	scanPos := len(sh.buffer)
	outputStart := -1
	countedEnd := scanPos
	capturedBytes := 0
	deadline := time.Now().Add(timeout)

	sh.stdin.Write([]byte(script))

	for {
		// Locate the begin marker.
		if outputStart == -1 {
			beginIdx := strings.Index(sh.buffer[scanPos:], beginMarker)
			if beginIdx == -1 {
				scanPos = len(sh.buffer) - (len(beginMarker) - 1)
				if scanPos < 0 {
					scanPos = 0
				}
			} else {
				lineEnd := strings.Index(sh.buffer[scanPos+beginIdx:], "\n")
				if lineEnd == -1 {
					scanPos = len(sh.buffer) - (len(beginMarker) - 1)
					if scanPos < 0 {
						scanPos = 0
					}
				} else {
					outputStart = scanPos + beginIdx + lineEnd + 1
					scanPos = outputStart
					countedEnd = outputStart
				}
			}
		}

		// Locate the end marker and parse the exit code.
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
				countedEnd = outputEnd

				codeStart := absEnd + len(endPrefix)
				codeSlice := sh.buffer[codeStart:]
				if len(codeSlice) > 32 {
					codeSlice = codeSlice[:32]
				}
				matched := shellExitCodePattern.FindStringSubmatch(codeSlice)
				if matched != nil {
					consumedEnd := codeStart + len(matched[0])
					output := cleanShellOutput(sh.buffer[outputStart:outputEnd])
					remainder := sh.buffer[consumedEnd:]
					sh.buffer = remainder

					exitCode := 0
					fmt.Sscanf(matched[1], "%d", &exitCode)
					output = strings.TrimSpace(strings.TrimPrefix(output, beginMarker+"\n"))
					if exitCode != 0 {
						return output, exitCode, newToolError(CodeCommandExecutionError,
							formatCommandFailure(output, "", exitCode, ""), false)
					}
					return output, 0, nil
				}
				scanPos = absEnd
				continue
			}
			// Count output bytes up to the scan position.
			capturedBytes += len(sh.buffer[countedEnd:scanPos])
			countedEnd = scanPos
		}

		if maxOutput > 0 && capturedBytes > maxOutput {
			m.Disconnect(key)
			return "", -1, newToolError(CodeOutputLimitExceeded,
				fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false)
		}

		if sh.closed {
			return "", -1, newToolError(CodeCommandExecutionError,
				"Shell channel closed during command execution", true)
		}
		if time.Now().After(deadline) {
			m.Disconnect(key)
			return "", -1, newToolError(CodeCommandTimeout,
				fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds()), true)
		}
		sh.cond.Wait()
	}
}

// buildShellScript frames a command with begin/end markers and captures its
// exit code.
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

// cleanShellOutput strips ANSI escape sequences and normalizes line endings.
func cleanShellOutput(output string) string {
	output = ansiOSCPattern.ReplaceAllString(output, "")
	output = ansiCSIPattern.ReplaceAllString(output, "")
	output = strings.ReplaceAll(output, "\r\n", "\n")
	return strings.ReplaceAll(output, "\r", "\n")
}
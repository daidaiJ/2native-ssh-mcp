package manager

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
)

// RunOptions controls a single command execution.
type RunOptions struct {
	Timeout           time.Duration // overrides the connection's command timeout
	KeepAlive         *bool         // keep the connection alive after the command (default: true)
	KeepAliveDuration time.Duration // idle duration after the command (default: 10 minutes)
	Prevalidated      bool          // skip whitelist/blacklist (internal commands)
}

// ExecuteCommand runs a command on the named connection and returns its
// combined output. After execution the connection is kept alive according to
// the keepAlive options (default: keep alive for 10 minutes).
func (m *Manager) ExecuteCommand(ctx context.Context, cmdString, directory, name string, opts RunOptions) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cmdString) == "" {
		return "", newToolError(CodeCommandValidationFailed, "cmdString must be a non-empty command", false)
	}
	if !opts.Prevalidated {
		if err := m.validateCommand(cmdString, name); err != nil {
			return "", err
		}
	}

	key := m.resolveName(name)
	cfg, err := m.getConfig(name)
	if err != nil {
		return "", err
	}

	timeout := opts.Timeout
	if timeout <= 0 {
		if cfg.TransportMode == "shell" {
			timeout = time.Duration(cfg.ShellCommandTimeoutMs) * time.Millisecond
		} else {
			timeout = time.Duration(cfg.CommandTimeoutMs) * time.Millisecond
		}
	}
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}

	client, err := m.EnsureConnected(name)
	if err != nil {
		return "", err
	}

	var output string
	var exitCode int
	if cfg.TransportMode == "shell" {
		output, exitCode, err = m.runShellCommand(ctx, key, cmdString, directory, timeout)
	} else {
		output, exitCode, err = m.runExecCommand(ctx, client, cfg, cmdString, directory, timeout, key)
	}

	success := err == nil
	m.RecordCommand(key, cmdString, exitCode, success)

	// Keepalive policy: keep the connection alive (default 10 minutes) or
	// close it right away.
	keepAlive := true
	if opts.KeepAlive != nil {
		keepAlive = *opts.KeepAlive
	}
	duration := opts.KeepAliveDuration
	if duration <= 0 {
		duration = DefaultKeepAliveDuration
	}
	if keepAlive {
		m.Touch(key, duration)
	} else {
		m.Disconnect(key)
	}

	return output, err
}

// limitedBuffer captures up to max bytes and records overflow.
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	exceeded bool
	onExceed func()
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if b.max <= 0 {
		return b.buf.Write(p)
	}
	if b.exceeded {
		return len(p), nil
	}
	remaining := b.max - b.buf.Len()
	if len(p) > remaining {
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
		b.exceeded = true
		if b.onExceed != nil {
			b.onExceed()
		}
		return len(p), nil
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// runExecCommand executes a command over a fresh exec channel.
func (m *Manager) runExecCommand(ctx context.Context, client *ssh.Client, cfg *config.SSHConfig,
	cmdString, directory string, timeout time.Duration, key string) (string, int, error) {

	commandToRun := cmdString
	if directory != "" {
		commandToRun = "cd -- " + shellQuote(directory) + " && " + cmdString
	}
	if cfg.CommandTemplate != "" {
		commandToRun = applyCommandTemplate(cfg.CommandTemplate, commandToRun)
	}

	session, err := client.NewSession()
	if err != nil {
		return "", -1, newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}
	defer session.Close()

	if cfg.GetPty() {
		if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
			return "", -1, newToolError(CodeCommandExecutionError,
				fmt.Sprintf("PTY request failed: %v", err), true)
		}
	}

	maxOutput := cfg.MaxOutputBytes
	exceededCh := make(chan struct{}, 1)
	notifyExceed := func() {
		select {
		case exceededCh <- struct{}{}:
		default:
		}
	}
	stdout := &limitedBuffer{max: maxOutput, onExceed: notifyExceed}
	stderr := &limitedBuffer{max: maxOutput, onExceed: notifyExceed}
	session.Stdout = stdout
	session.Stderr = stderr

	if err := session.Start(commandToRun); err != nil {
		return "", -1, newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case waitErr := <-done:
		if stdout.exceeded || stderr.exceeded {
			return "", -1, newToolError(CodeOutputLimitExceeded,
				fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false)
		}
		if waitErr != nil {
			var exitErr *ssh.ExitError
			if errors.As(waitErr, &exitErr) {
				code := exitErr.ExitStatus()
				return "", code, newToolError(CodeCommandExecutionError,
					formatCommandFailure(stdout.String(), stderr.String(), code, "", cfg), false)
			}
			return "", -1, newToolError(CodeCommandExecutionError,
				fmt.Sprintf("Command execution error: %v", waitErr), true)
		}
		return formatCommandSuccess(stdout.String(), stderr.String(), cfg), 0, nil
	case <-exceededCh:
		signalRemoteProcess(session, done)
		session.Close()
		return "", -1, newToolError(CodeOutputLimitExceeded,
			fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false)
	case <-ctx.Done():
		signalRemoteProcess(session, done)
		session.Close()
		return "", -1, ctxToolError(ctx, CodeCommandTimeout,
			fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds()))
	}
}

func formatCommandSuccess(stdout, stderr string, cfg *config.SSHConfig) string {
	stdout, stderr = redactCommandOutput(stdout, stderr)
	var result string
	if stderr == "" {
		result = stdout
	} else {
		result = strings.TrimSuffix(stdout, "\n") + "\n[stderr]\n" + stderr
	}
	return FinalizeCommandOutput(result, compressOpts(cfg))
}

func formatCommandFailure(stdout, stderr string, exitCode int, exitSignal string, cfg *config.SSHConfig) string {
	stdout, stderr = redactCommandOutput(stdout, stderr)
	var sections []string
	if stdout != "" {
		sections = append(sections, stdout)
	}
	if stderr != "" {
		sections = append(sections, "[stderr]\n"+stderr)
	}
	if exitCode >= 0 {
		sections = append(sections, fmt.Sprintf("[exit code] %d", exitCode))
	}
	if exitSignal != "" {
		sections = append(sections, "[signal] "+exitSignal)
	}
	if len(sections) == 0 {
		if exitSignal != "" {
			return fmt.Sprintf("Command terminated by signal %s", exitSignal)
		}
		return fmt.Sprintf("Command failed with exit code %d", exitCode)
	}
	return FinalizeCommandOutput(strings.Join(sections, "\n"), compressOpts(cfg))
}

func compressOpts(cfg *config.SSHConfig) CompressOptions {
	if cfg == nil {
		return DefaultCompressOptions()
	}
	return CompressOptionsFromConfig(cfg.OutputCompressLight, cfg.OutputCompressThreshold)
}

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

// ExecuteCommand runs a command on the named connection and returns a
// structured result. A non-zero exit is a normal result (Status=exited), not
// an error; errors are reserved for validation/connect failures and the
// statuses that must surface as MCP errors (timeout, output limit, cancelled,
// connection lost). After execution the connection is kept alive according to
// the keepAlive options (default: keep alive for 10 minutes).
func (m *Manager) ExecuteCommand(ctx context.Context, cmdString, directory, name string, opts RunOptions) (CommandResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(cmdString) == "" {
		return CommandResult{}, newToolError(CodeCommandValidationFailed, "cmdString must be a non-empty command", false)
	}
	if !opts.Prevalidated {
		if err := m.validateCommand(cmdString, name); err != nil {
			return CommandResult{}, err
		}
	}

	key := m.resolveName(name)
	m.beginOp(key)
	defer m.endOp(key)

	cfg, err := m.getConfig(name)
	if err != nil {
		return CommandResult{}, err
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
		return CommandResult{}, err
	}

	var result CommandResult
	if cfg.TransportMode == "shell" {
		result, err = m.runShellCommand(ctx, key, cmdString, directory, timeout)
	} else {
		result, err = m.runExecCommand(ctx, client, cfg, cmdString, directory, timeout, key)
	}

	m.RecordCommand(key, cmdString, result.ExitCode, result.ExitCode == 0)

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

	return result, err
}

// limitedBuffer captures up to max bytes and records overflow. When shared
// is non-nil, stdout and stderr share one byte budget so the combined output
// cannot exceed max (the per-stream cap is only a safety bound).
type limitedBuffer struct {
	buf      bytes.Buffer
	max      int
	shared   *int // shared remaining budget; nil for standalone buffers
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
	if b.shared != nil {
		remaining = *b.shared
	}
	if len(p) > remaining {
		if remaining > 0 {
			b.buf.Write(p[:remaining])
		}
		if b.shared != nil {
			*b.shared = 0
		}
		b.exceeded = true
		if b.onExceed != nil {
			b.onExceed()
		}
		return len(p), nil
	}
	if b.shared != nil {
		*b.shared -= len(p)
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string { return b.buf.String() }

// runExecCommand executes a command over a fresh exec channel.
func (m *Manager) runExecCommand(ctx context.Context, client *ssh.Client, cfg *config.SSHConfig,
	cmdString, directory string, timeout time.Duration, key string) (CommandResult, error) {

	commandToRun := cmdString
	if directory != "" {
		commandToRun = "cd -- " + shellQuote(directory) + " && " + cmdString
	}
	if cfg.CommandTemplate != "" {
		commandToRun = applyCommandTemplate(cfg.CommandTemplate, commandToRun)
	}

	session, err := client.NewSession()
	if err != nil {
		return CommandResult{}, newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}
	defer session.Close()

	if cfg.GetPty() {
		if err := session.RequestPty("xterm", 80, 24, ssh.TerminalModes{}); err != nil {
			return CommandResult{}, newToolError(CodeCommandExecutionError,
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
	budget := maxOutput
	stdout := &limitedBuffer{max: maxOutput, shared: &budget, onExceed: notifyExceed}
	stderr := &limitedBuffer{max: maxOutput, shared: &budget, onExceed: notifyExceed}
	session.Stdout = stdout
	session.Stderr = stderr

	if err := session.Start(commandToRun); err != nil {
		return CommandResult{}, newToolError(CodeCommandExecutionError,
			fmt.Sprintf("Command execution error: %v", err), true)
	}

	done := make(chan error, 1)
	go func() { done <- session.Wait() }()

	select {
	case waitErr := <-done:
		if stdout.exceeded || stderr.exceeded {
			return buildCommandResult(stdout.String(), stderr.String(), -1, StatusOutputLimit, cfg), newToolError(CodeOutputLimitExceeded,
				fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false)
		}
		if waitErr != nil {
			var exitErr *ssh.ExitError
			var missing *ssh.ExitMissingError
			switch {
			case errors.As(waitErr, &exitErr):
				// Non-zero exit is a normal result, not a transport error.
				return buildCommandResult(stdout.String(), stderr.String(), exitErr.ExitStatus(), StatusExited, cfg), nil
			case errors.As(waitErr, &missing):
				// Fuchsia sshutil treats ExitMissingError as a connection
				// failure: the remote never reported an exit status.
				return buildCommandResult(stdout.String(), stderr.String(), -1, StatusConnectionLost, cfg), newToolError(CodeSSHConnectionLost,
					"SSH connection dropped during command; the remote process may still be running. Do not replay blindly.", false)
			default:
				return buildCommandResult(stdout.String(), stderr.String(), -1, StatusConnectionLost, cfg), newToolError(CodeSSHConnectionLost,
					"SSH connection dropped during command; the remote process may still be running. Do not replay blindly.", false)
			}
		}
		return buildCommandResult(stdout.String(), stderr.String(), 0, StatusOK, cfg), nil
	case <-exceededCh:
		signalRemoteProcess(session, done)
		session.Close()
		return buildCommandResult(stdout.String(), stderr.String(), -1, StatusOutputLimit, cfg), newToolError(CodeOutputLimitExceeded,
			fmt.Sprintf("[truncated] Output exceeded maxOutputBytes=%d; the command was aborted.", maxOutput), false)
	case <-ctx.Done():
		signalRemoteProcess(session, done)
		session.Close()
		return buildCommandResult(stdout.String(), stderr.String(), -1, StatusTimeout, cfg), ctxToolError(ctx, CodeCommandTimeout,
			fmt.Sprintf("[timeout] Command timed out after %dms", timeout.Milliseconds()))
	}
}

func compressOpts(cfg *config.SSHConfig) CompressOptions {
	if cfg == nil {
		return DefaultCompressOptions()
	}
	return CompressOptionsFromConfig(cfg.OutputCompressLight, cfg.OutputCompressThreshold)
}

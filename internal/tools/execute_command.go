package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// registerExecuteCommand registers the execute-command tool.
func registerExecuteCommand(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("execute-command",
		mcp.WithDescription("Execute a command on a configured SSH server and return its output. Use list-servers first to pick connectionName. Set sessionName to run inside a named session (CWD persists); open the session first with session action=open. Large output is spilled to a local file — Grep/Read that file instead of re-running the command. Three ways to pass the command: cmdString (a shell command, run as written), cmdString + args (argv mode: the program and each argument verbatim, no shell parsing — use it instead of hand-quoting), or scriptFile (a local script, staged to the remote host and run — one call instead of write-file + file-transfer + execute-command)."),
		mutatingAnnotation("Execute remote command", true),
		mcp.WithString("cmdString", mcp.Description("Command to execute. In argv mode (args set) this is the program itself, e.g. \"grep\". Mutually exclusive with scriptFile")),
		mcp.WithArray("args", mcp.WithStringItems(), mcp.Description("argv mode: arguments for cmdString, each passed to the program byte-for-byte with no shell interpretation (spaces, quotes, $, backslashes and * stay literal). Use it whenever an argument contains characters you would otherwise have to quote. Mutually exclusive with scriptFile")),
		mcp.WithString("scriptFile", mcp.Description("Local script to run on the remote host: the file is read locally, staged to /tmp over SFTP and executed (its shebang decides the interpreter, otherwise scriptInterpreter, default sh). Mutually exclusive with cmdString/args")),
		mcp.WithBoolean("scriptCleanup", mcp.Description("Remove the staged scriptFile from the remote host once it finishes (default: the connection's scriptCleanup, itself default true; a run killed by timeout or the output limit may leave the file behind)")),
		mcp.WithString("lintMode", mcp.Description("Pre-execution structural lint for cmdString: \"off\", \"warn\" (default; findings are reported in the result and the command still runs) or \"strict\" (error-class findings refuse the command with COMMAND_LINT_FAILED). Defaults to the connection's lintMode. Not applicable to args/scriptFile")),
		mcp.WithString("directory", mcp.Description("Working directory for command execution")),
		mcp.WithString("sessionName", mcp.Description("Optional named session (from session action=open); when set, connectionName is ignored")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name or alias from list-servers (required unless a connection named 'default' is configured; omitting it without a 'default' connection fails with the list of available names)")),
		mcp.WithNumber("timeout", mcp.Description("Command execution timeout in milliseconds (optional; overrides the connection's commandTimeoutMs, which defaults to 30000ms)")),
		mcp.WithBoolean("background", mcp.Description("Run the command detached as a background job and return immediately with sessionName/logPath; poll with session action=read (minutes-long work belongs here, not in a bigger timeout)")),
		mcp.WithBoolean("pty", mcp.Description("Allocate a remote pseudo-terminal (default: false; opt in for interactive commands like sudo or vim; with background=true it wraps the detached job in a TTY)")),
		mcp.WithBoolean("keepAlive", mcp.Description("Keep the SSH connection alive after the command finishes (default: true; ignored for named sessions — the connection may host other sessions)")),
		mcp.WithNumber("keepAliveDuration", mcp.Description("How long to keep the connection alive after the command finishes, in milliseconds (default: 600000, i.e. 10 minutes)")),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		// Keyboard-interactive OTP prompts ride the same request context, so
		// a challenge during dial shows only on this client session.
		ctx = withElicitor(ctx, s)

		directory, _ := args["directory"].(string)
		sessionName, _ := args["sessionName"].(string)
		connectionName, _ := args["connectionName"].(string)
		background, _ := args["background"].(bool)

		req, err := resolveExecuteRequest(ctx, m, args, sessionName, connectionName)
		if err != nil {
			return errorResult(err), nil
		}

		opts := manager.RunOptions{
			AuditCommand: req.auditCommand,
			LintMode:     req.lintMode,
		}
		if v, ok := args["timeout"].(float64); ok && v > 0 {
			opts.Timeout = time.Duration(v) * time.Millisecond
		}
		if v, ok := args["pty"].(bool); ok {
			opts.Pty = &v
		}
		if v, ok := args["keepAlive"].(bool); ok {
			opts.KeepAlive = &v
		}
		if v, ok := args["keepAliveDuration"].(float64); ok && v > 0 {
			opts.KeepAliveDuration = time.Duration(v) * time.Millisecond
		}

		// Destructive-command approval gate (issue #5). Runs before both the
		// background and foreground paths: a detached job is exactly the case
		// where the human must be asked before the command starts. The gate
		// sees the readable equivalent, so a staged script is judged (and
		// shown) by its contents, not by its temp path.
		gateText := req.gateText()
		proceed, note, approval, decline := wrapWithGate(ctx, s, m, sessionName, connectionName, gateText)
		if !proceed {
			// The command never reached the remote host, but the refusal is
			// part of the audit trail (success=false, no remote exit).
			m.RecordRejectedCommand(sessionName, connectionName, gateText, approval)
			return decline, nil
		}
		opts.Approval = approval

		if background {
			bgPty, _ := args["pty"].(bool)
			res, err := handleBackgroundCommand(ctx, m, req, directory, sessionName, connectionName, bgPty, approval)
			return appendNote(res, note), err
		}

		var result manager.CommandResult
		if sessionName != "" {
			result, err = m.RunInSession(ctx, sessionName, req.execCommand, directory, opts)
		} else {
			result, err = m.ExecuteCommand(ctx, req.execCommand, directory, connectionName, opts)
		}
		if req.scriptRun != nil {
			result.ScriptFile = req.scriptRun
		}
		cfg := m.ConfigFor(sessionName, connectionName)
		stampDefaultConnection(cfg, sessionName, connectionName, &result)
		if err != nil {
			return appendNote(errorResultFor(err, result), note), nil
		}
		return appendNote(commandResult(result, cfg), note), nil
	})
}

// handleBackgroundCommand detaches a command via the named-session background
// machinery and returns immediately: the agent gets a session name and remote
// log path to poll with session action=read instead of burning a long
// synchronous timeout.
func handleBackgroundCommand(ctx context.Context, m *manager.Manager, req *executeRequest, directory, sessionName, connectionName string, pty bool, approval string) (*mcp.CallToolResult, error) {
	if sessionName == "" {
		sessionName = randomSessionName()
	}
	info, err := m.OpenSessionWithOptions(ctx, sessionName, connectionName, manager.SessionOpenOptions{
		Background:   true,
		CmdString:    req.execCommand,
		Directory:    directory,
		Pty:          pty,
		Approval:     approval,
		AuditCommand: req.auditCommand,
	})
	if err != nil {
		return errorResult(err), nil
	}
	raw, _ := marshalResultJSON(info)
	text := fmt.Sprintf(
		"Background job started: session=%q logPath=%s\nPoll with session action=read (sessionName=%q, waitMs blocks for new output); stop with session action=close.\n\n%s",
		info.Name, info.LogPath, info.Name, raw)
	if req.scriptRun != nil {
		text = req.scriptRun.Summary() + "\n" + text
	}
	if connectionName == "" {
		// Default-routed call: surface which connection the job runs on.
		if cfg := m.ConfigFor("", connectionName); cfg != nil {
			text = connectionSummary("default", cfg.Host, cfg.Port, cfg.Aliases) + "\n\n" + text
		}
	}
	return mcp.NewToolResultText(text), nil
}

// stampDefaultConnection records which connection actually served a call that
// omitted connectionName, on success and on error alike (a retrying agent must
// know which host it was talking to).
func stampDefaultConnection(cfg *config.SSHConfig, sessionName, connectionName string, result *manager.CommandResult) {
	if cfg == nil || connectionName != "" || sessionName != "" {
		return
	}
	result.Connection = "default"
	result.Host = cfg.Host
	result.Port = cfg.Port
	result.Aliases = cfg.Aliases
}

// connectionSummary renders a connection's host identity, e.g.
// "connection=default host=203.0.113.5 port=22 aliases=web,prod". It is
// included in responses to calls that omitted connectionName, where the
// agent otherwise cannot tell which host actually served the call.
func connectionSummary(connName, host string, port int, aliases []string) string {
	line := fmt.Sprintf("connection=%s host=%s port=%d", connName, host, port)
	if len(aliases) > 0 {
		line += " aliases=" + strings.Join(aliases, ",")
	}
	return line
}

// randomSessionName generates a throwaway background session name
// (bg-<hex>), matching the sessionNamePattern.
func randomSessionName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "bg-" + hex.EncodeToString(b[:])
}

// commandResult renders a successful command result in the connection's
// configured format: "text" (default) keeps the human-readable sectioned
// layout with real newlines; "json" emits the structured CommandResult JSON.
// Error paths always stay JSON so agents can branch on code/retriable.
func commandResult(res manager.CommandResult, cfg *config.SSHConfig) *mcp.CallToolResult {
	if cfg.GetResultFormat() == config.ResultFormatJSON {
		return commandResultJSON(res)
	}
	text := res.Text()
	if res.Connection != "" {
		text = connectionSummary(res.Connection, res.Host, res.Port, res.Aliases) + "\n\n" + text
	}
	return mcp.NewToolResultText(text)
}

// commandResultJSON renders a successful command result as structured JSON,
// matching the error-path shape (code/message plus stdout/stderr/exitCode/
// status/partial/replaySafe).
func commandResultJSON(res manager.CommandResult) *mcp.CallToolResult {
	text, _ := marshalResultJSON(res)
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(text)},
	}
}

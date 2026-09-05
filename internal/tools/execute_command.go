package tools

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerExecuteCommand registers the execute-command tool.
func registerExecuteCommand(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("execute-command",
		mcp.WithDescription("Execute a command on a configured SSH server and return its output. Use list-servers first to pick connectionName. Set sessionName to run inside a named session (CWD persists); open the session first with session action=open. Large output is spilled to a local file — Grep/Read that file instead of re-running the command."),
		mutatingAnnotation("Execute remote command", true),
		mcp.WithString("cmdString", mcp.Required(), mcp.Description("Command to execute")),
		mcp.WithString("directory", mcp.Description("Working directory for command execution")),
		mcp.WithString("sessionName", mcp.Description("Optional named session (from session action=open); when set, connectionName is ignored")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name or alias from list-servers (optional; defaults to the default connection)")),
		mcp.WithNumber("timeout", mcp.Description("Command execution timeout in milliseconds (optional; overrides the connection's commandTimeoutMs, which defaults to 30000ms)")),
		mcp.WithBoolean("background", mcp.Description("Run the command detached as a background job and return immediately with sessionName/logPath; poll with session action=read (minutes-long work belongs here, not in a bigger timeout)")),
		mcp.WithBoolean("pty", mcp.Description("Allocate a remote pseudo-terminal (default: false; opt in for interactive commands like sudo or vim; with background=true it wraps the detached job in a TTY)")),
		mcp.WithBoolean("keepAlive", mcp.Description("Keep the SSH connection alive after the command finishes (default: true; ignored for named sessions — the connection may host other sessions)")),
		mcp.WithNumber("keepAliveDuration", mcp.Description("How long to keep the connection alive after the command finishes, in milliseconds (default: 600000, i.e. 10 minutes)")),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()

		cmdString, _ := args["cmdString"].(string)
		directory, _ := args["directory"].(string)
		sessionName, _ := args["sessionName"].(string)
		connectionName, _ := args["connectionName"].(string)
		background, _ := args["background"].(bool)

		opts := manager.RunOptions{}
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
		// where the human must be asked before the command starts.
		proceed, note, decline := wrapWithGate(ctx, s, m, sessionName, connectionName, cmdString)
		if !proceed {
			return decline, nil
		}

		if background {
			bgPty, _ := args["pty"].(bool)
			res, err := handleBackgroundCommand(m, cmdString, directory, sessionName, connectionName, bgPty)
			return appendNote(res, note), err
		}

		var result manager.CommandResult
		var err error
		if sessionName != "" {
			result, err = m.RunInSession(ctx, sessionName, cmdString, directory, opts)
		} else {
			result, err = m.ExecuteCommand(ctx, cmdString, directory, connectionName, opts)
		}
		if err != nil {
			return appendNote(errorResultFor(err, result), note), nil
		}
		return appendNote(commandResultJSON(result), note), nil
	})
}

// handleBackgroundCommand detaches a command via the named-session background
// machinery and returns immediately: the agent gets a session name and remote
// log path to poll with session action=read instead of burning a long
// synchronous timeout.
func handleBackgroundCommand(m *manager.Manager, cmdString, directory, sessionName, connectionName string, pty bool) (*mcp.CallToolResult, error) {
	if strings.TrimSpace(cmdString) == "" {
		return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
			"cmdString is required for background=true", false)), nil
	}
	if sessionName == "" {
		sessionName = randomSessionName()
	}
	info, err := m.OpenSessionWithOptions(sessionName, connectionName, manager.SessionOpenOptions{
		Background: true,
		CmdString:  cmdString,
		Directory:  directory,
		Pty:        pty,
	})
	if err != nil {
		return errorResult(err), nil
	}
	raw, _ := json.MarshalIndent(info, "", "  ")
	text := fmt.Sprintf(
		"Background job started: session=%q logPath=%s\nPoll with session action=read (sessionName=%q, waitMs blocks for new output); stop with session action=close.\n\n%s",
		info.Name, info.LogPath, info.Name, raw)
	return mcp.NewToolResultText(text), nil
}

// randomSessionName generates a throwaway background session name
// (bg-<hex>), matching the sessionNamePattern.
func randomSessionName() string {
	var b [4]byte
	_, _ = rand.Read(b[:])
	return "bg-" + hex.EncodeToString(b[:])
}

// commandResultJSON renders a successful command result as structured JSON,
// matching the error-path shape (code/message plus stdout/stderr/exitCode/
// status/partial/replaySafe).
func commandResultJSON(res manager.CommandResult) *mcp.CallToolResult {
	text, _ := json.MarshalIndent(res, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(text))},
	}
}

package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerExecuteCommand registers the execute-command tool.
func registerExecuteCommand(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("execute-command",
		mcp.WithDescription("Execute a command on a configured SSH server and return its output. Use list-servers first to pick connectionName. Set sessionName to run inside a named session (CWD persists); open the session first with session action=open."),
		mutatingAnnotation("Execute remote command", true),
		mcp.WithString("cmdString", mcp.Required(), mcp.Description("Command to execute")),
		mcp.WithString("directory", mcp.Description("Working directory for command execution")),
		mcp.WithString("sessionName", mcp.Description("Optional named session (from session action=open); when set, connectionName is ignored")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name or alias from list-servers (optional; defaults to the default connection)")),
		mcp.WithNumber("timeout", mcp.Description("Command execution timeout in milliseconds (optional; overrides the connection's commandTimeoutMs, which defaults to 30000ms)")),
		mcp.WithBoolean("keepAlive", mcp.Description("Keep the SSH connection alive after the command finishes (default: true)")),
		mcp.WithNumber("keepAliveDuration", mcp.Description("How long to keep the connection alive after the command finishes, in milliseconds (default: 600000, i.e. 10 minutes)")),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()

		cmdString, _ := args["cmdString"].(string)
		directory, _ := args["directory"].(string)
		sessionName, _ := args["sessionName"].(string)
		connectionName, _ := args["connectionName"].(string)

		opts := manager.RunOptions{}
		if v, ok := args["timeout"].(float64); ok && v > 0 {
			opts.Timeout = time.Duration(v) * time.Millisecond
		}
		if v, ok := args["keepAlive"].(bool); ok {
			opts.KeepAlive = &v
		}
		if v, ok := args["keepAliveDuration"].(float64); ok && v > 0 {
			opts.KeepAliveDuration = time.Duration(v) * time.Millisecond
		}

		var result string
		var err error
		if sessionName != "" {
			result, err = m.RunInSession(ctx, sessionName, cmdString, directory, opts)
		} else {
			result, err = m.ExecuteCommand(ctx, cmdString, directory, connectionName, opts)
		}
		if err != nil {
			return errorResult(err), nil
		}
		return mcp.NewToolResultText(result), nil
	})
}
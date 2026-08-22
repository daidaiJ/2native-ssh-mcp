package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"ssh-mcp-server-go/internal/manager"
)

// registerExecuteCommand registers the execute-command tool.
func registerExecuteCommand(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("execute-command",
		mcp.WithDescription("Execute command on connected server and get output result"),
		mcp.WithString("cmdString", mcp.Required(), mcp.Description("Command to execute")),
		mcp.WithString("directory", mcp.Description("Working directory for command execution")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name (optional, default is 'default')")),
		mcp.WithNumber("timeout", mcp.Description("Command execution timeout in milliseconds (optional; overrides the connection's commandTimeoutMs, which defaults to 30000ms)")),
		mcp.WithBoolean("keepAlive", mcp.Description("Keep the SSH connection alive after the command finishes (default: true)")),
		mcp.WithNumber("keepAliveDuration", mcp.Description("How long to keep the connection alive after the command finishes, in milliseconds (default: 600000, i.e. 10 minutes)")),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()

		cmdString, _ := args["cmdString"].(string)
		directory, _ := args["directory"].(string)
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

		result, err := m.ExecuteCommand(cmdString, directory, connectionName, opts)
		if err != nil {
			return errorResult(err), nil
		}
		return mcp.NewToolResultText(result), nil
	})
}
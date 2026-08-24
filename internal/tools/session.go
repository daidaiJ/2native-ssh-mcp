package tools

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

func registerSession(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("session",
		mcp.WithDescription("Manage named SSH sessions on exec-mode connections. action=open creates a session (optionally starts a background job with background+cmdString); action=read polls background output; action=close stops background jobs and releases the shell. Use execute-command with sessionName to run commands with persistent CWD."),
		mcp.WithString("action", mcp.Required(), mcp.Description("Session operation"), mcp.Enum("open", "read", "close")),
		mcp.WithString("sessionName", mcp.Required(), mcp.Description("Unique session name (letters, digits, '.', '-', '_')")),
		mcp.WithString("connectionName", mcp.Description("Required for action=open on a new session")),
		mcp.WithBoolean("background", mcp.Description("For action=open: run cmdString as a background job")),
		mcp.WithString("cmdString", mcp.Description("For action=open with background=true")),
		mcp.WithString("directory", mcp.Description("Working directory when opening a background job")),
		mcp.WithNumber("maxBytes", mcp.Description("For action=read: max bytes (default 65536)")),
		mcp.WithNumber("offset", mcp.Description("For action=read: byte offset (default: continue from last read)")),
		mutatingAnnotation("Manage SSH session", false),
	)
	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		action, _ := args["action"].(string)
		sessionName, _ := args["sessionName"].(string)

		switch action {
		case "open":
			connectionName, _ := args["connectionName"].(string)
			background, _ := args["background"].(bool)
			cmdString, _ := args["cmdString"].(string)
			directory, _ := args["directory"].(string)
			info, err := m.OpenSessionWithOptions(sessionName, connectionName, manager.SessionOpenOptions{
				Background: background,
				CmdString:  cmdString,
				Directory:  directory,
			})
			if err != nil {
				return errorResult(err), nil
			}
			raw, _ := json.Marshal(info)
			msg := fmt.Sprintf("Session %q on connection %q", info.Name, info.ConnectionName)
			if info.Background {
				msg += " (background)"
			}
			return mcp.NewToolResultText(msg + "\n\n" + string(raw)), nil

		case "read":
			var maxBytes, offset int64 = 0, -1
			if v, ok := args["maxBytes"].(float64); ok {
				maxBytes = int64(v)
			}
			if v, ok := args["offset"].(float64); ok {
				offset = int64(v)
			}
			out, err := m.ReadSessionOutput(sessionName, maxBytes, offset)
			if err != nil {
				return errorResult(err), nil
			}
			raw, _ := json.Marshal(out)
			text := fmt.Sprintf("session=%s running=%v totalBytes=%d offset=%d\n\n%s",
				out.SessionName, out.Running, out.TotalBytes, out.Offset, out.Output)
			return mcp.NewToolResultText(text + "\n\nRaw JSON:\n" + string(raw)), nil

		case "close":
			if err := m.CloseSession(sessionName); err != nil {
				return errorResult(err), nil
			}
			return mcp.NewToolResultText(fmt.Sprintf("Session %q closed", sessionName)), nil

		default:
			return errorResult(fmt.Errorf("action must be open, read, or close, got: %s", action)), nil
		}
	})
}

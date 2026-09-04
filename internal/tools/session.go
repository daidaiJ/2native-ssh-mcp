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
		mcp.WithDescription("Manage named SSH sessions on exec-mode connections. action=open creates a session (optionally starts a background job with background+cmdString); action=read polls background output; action=close stops background jobs and releases the shell; action=list shows all sessions. Use execute-command with sessionName to run commands with persistent CWD."),
		mcp.WithString("action", mcp.Required(), mcp.Description("Session operation"), mcp.Enum("open", "read", "close", "list")),
		mcp.WithString("sessionName", mcp.Description("Unique session name (letters, digits, '.', '-', '_'); required for open/read/close")),
		mcp.WithString("connectionName", mcp.Description("Required for action=open on a new session; optional filter for action=list")),
		mcp.WithBoolean("background", mcp.Description("For action=open: run cmdString as a background job")),
		mcp.WithString("cmdString", mcp.Description("For action=open with background=true")),
		mcp.WithString("directory", mcp.Description("Working directory when opening a background job")),
		mcp.WithBoolean("pty", mcp.Description("For action=open with background=true: wrap the job in a pseudo-terminal (default false; for programs that need a TTY)")),
		mcp.WithNumber("maxBytes", mcp.Description("For action=read: max bytes (default 65536)")),
		mcp.WithNumber("offset", mcp.Description("For action=read: byte offset (default: continue from last read)")),
		mcp.WithNumber("waitMs", mcp.Description("For action=read: block up to this many milliseconds when there is nothing new and the job is still running (default 0 = return immediately; capped at 30000)")),
		mutatingAnnotation("Manage SSH session", false),
	)
	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleSessionTool(m, request.GetArguments())
	})
}

// handleSessionTool implements the session tool. sessionName is validated per
// action (list does not need it); unknown actions are reported before any
// missing-argument error.
func handleSessionTool(m *manager.Manager, args map[string]any) (*mcp.CallToolResult, error) {
	action, _ := args["action"].(string)
	sessionName, _ := args["sessionName"].(string)

	switch action {
	case "open":
		if sessionName == "" {
			return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
				"sessionName is required for action=open", false)), nil
		}
		connectionName, _ := args["connectionName"].(string)
		background, _ := args["background"].(bool)
		cmdString, _ := args["cmdString"].(string)
		directory, _ := args["directory"].(string)
		bgPty, _ := args["pty"].(bool)
		info, err := m.OpenSessionWithOptions(sessionName, connectionName, manager.SessionOpenOptions{
			Background: background,
			CmdString:  cmdString,
			Directory:  directory,
			Pty:        bgPty,
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
		if sessionName == "" {
			return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
				"sessionName is required for action=read", false)), nil
		}
		var maxBytes, offset, waitMs int64 = 0, -1, 0
		if v, ok := args["maxBytes"].(float64); ok {
			maxBytes = int64(v)
		}
		if v, ok := args["offset"].(float64); ok {
			offset = int64(v)
		}
		if v, ok := args["waitMs"].(float64); ok {
			waitMs = int64(v)
		}
		out, err := m.ReadSessionOutput(sessionName, maxBytes, offset, waitMs)
		if err != nil {
			return errorResult(err), nil
		}
		raw, _ := json.Marshal(out)
		text := fmt.Sprintf("session=%s running=%v totalBytes=%d offset=%d\n\n%s",
			out.SessionName, out.Running, out.TotalBytes, out.Offset, out.Output)
		return mcp.NewToolResultText(text + "\n\nRaw JSON:\n" + string(raw)), nil

	case "close":
		if sessionName == "" {
			return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
				"sessionName is required for action=close", false)), nil
		}
		if err := m.CloseSession(sessionName); err != nil {
			return errorResult(err), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Session %q closed", sessionName)), nil

	case "list":
		connectionName, _ := args["connectionName"].(string)
		sessions := m.ListSessions()
		if connectionName != "" {
			filtered := sessions[:0]
			for _, s := range sessions {
				if s.ConnectionName == connectionName {
					filtered = append(filtered, s)
				}
			}
			sessions = filtered
		}
		raw, _ := json.Marshal(sessions)
		return mcp.NewToolResultText(formatSessionList(sessions) + "\n\nSessions JSON:\n" + string(raw)), nil

	default:
		return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
			fmt.Sprintf("Invalid action %q: must be open, read, close, or list", action), false)), nil
	}
}
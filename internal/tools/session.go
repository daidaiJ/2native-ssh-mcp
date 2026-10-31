package tools

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

func registerSession(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("session",
		mcp.WithDescription("Manage named SSH sessions: action=open creates one (optionally starting a background job with background+cmdString), action=read polls background output, action=close stops background jobs and releases the shell, action=list shows all sessions. Use execute-command with sessionName to run commands with a persistent CWD."),
		mcp.WithString("action", mcp.Required(), mcp.Description("Session operation"), mcp.Enum("open", "read", "close", "list")),
		mcp.WithString("sessionName", mcp.Description("Session name (letters, digits, '.', '-', '_'); required for open/read/close")),
		mcp.WithString("connectionName", mcp.Description("Required for action=open on a new session; optional filter for action=list")),
		mcp.WithBoolean("background", mcp.Description("For action=open: run cmdString detached instead of opening a shell")),
		mcp.WithString("cmdString", mcp.Description("For action=open with background=true")),
		mcp.WithString("directory", mcp.Description("Working directory when opening a background job")),
		mcp.WithNumber("maxBytes", mcp.Description("For action=read: max bytes (default 65536); a capped read returns truncated=true and remainingBytes")),
		mcp.WithNumber("offset", mcp.Description("For action=read: byte offset (default: continue from last read)")),
		mcp.WithNumber("waitMs", mcp.Description("For action=read: block up to this many milliseconds when there is nothing new (default 0 = return immediately; capped at 30000)")),
		mutatingAnnotation("Manage SSH session", false),
	)
	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()
		ctx = withElicitor(ctx, s)
		if action, _ := args["action"].(string); action == "open" {
			// action=open with cmdString executes a command just like
			// execute-command (background or not), so it must pass the same
			// destructive-command approval gate.
			if cmdString, _ := args["cmdString"].(string); cmdString != "" {
				sessionName, _ := args["sessionName"].(string)
				connectionName, _ := args["connectionName"].(string)
				proceed, note, outcome, decline := wrapWithGate(ctx, s, m, sessionName, connectionName, cmdString)
				if !proceed {
					m.RecordRejectedCommand(sessionName, connectionName, cmdString, outcome)
					return decline, nil
				}
				res, err := handleSessionTool(ctx, m, args, outcome)
				return appendNote(res, note), err
			}
		}
		return handleSessionTool(ctx, m, args, "")
	})
}

// handleSessionTool implements the session tool. sessionName is validated per
// action (list does not need it); unknown actions are reported before any
// missing-argument error. approval is the destructive-command gate outcome
// carried into the audit record of a background start ("" when the gate did
// not run).
func handleSessionTool(ctx context.Context, m *manager.Manager, args map[string]any, approval string) (*mcp.CallToolResult, error) {
	action, _ := args["action"].(string)
	sessionName, _ := args["sessionName"].(string)

	switch action {
	case "open":
		return sessionOpenResult(ctx, m, args, sessionName, approval)
	case "read":
		return sessionReadResult(ctx, m, args, sessionName)
	case "close":
		if sessionName == "" {
			return errorResult(missingSessionName("close")), nil
		}
		if err := m.CloseSession(ctx, sessionName); err != nil {
			return errorResult(err), nil
		}
		return mcp.NewToolResultText(fmt.Sprintf("Session %q closed", sessionName)), nil
	case "list":
		return sessionListResult(m, args), nil
	default:
		return errorResult(manager.NewToolError(manager.CodeCommandValidationFailed,
			fmt.Sprintf("Invalid action %q: must be open, read, close, or list", action), false)), nil
	}
}

// missingSessionName is the validation error every action that addresses a
// single session reports.
func missingSessionName(action string) error {
	return manager.NewToolError(manager.CodeCommandValidationFailed,
		fmt.Sprintf("sessionName is required for action=%s", action), false)
}

// sessionOpenResult opens (or reuses) a named session, starting a background
// job when the call asked for one.
func sessionOpenResult(ctx context.Context, m *manager.Manager, args map[string]any, sessionName, approval string) (*mcp.CallToolResult, error) {
	if sessionName == "" {
		return errorResult(missingSessionName("open")), nil
	}
	connectionName, _ := args["connectionName"].(string)
	background, _ := args["background"].(bool)
	cmdString, _ := args["cmdString"].(string)
	directory, _ := args["directory"].(string)
	info, err := m.OpenSessionWithOptions(ctx, sessionName, connectionName, manager.SessionOpenOptions{
		Background: background,
		CmdString:  cmdString,
		Directory:  directory,
		Approval:   approval,
	})
	if err != nil {
		return errorResult(err), nil
	}
	raw, _ := marshalCompactJSON(info)
	msg := fmt.Sprintf("Session %q on connection %q", info.Name, info.ConnectionName)
	if info.Host != "" {
		msg += fmt.Sprintf(" (host %s:%d)", info.Host, info.Port)
	}
	if info.Background {
		msg += " (background)"
	}
	return mcp.NewToolResultText(msg + "\n\n" + raw), nil
}

// sessionReadResult polls a background session's output. The header states
// where the next read resumes and, when the remote log had to be trimmed, how
// many bytes were dropped from the beginning of the job's output.
func sessionReadResult(ctx context.Context, m *manager.Manager, args map[string]any, sessionName string) (*mcp.CallToolResult, error) {
	if sessionName == "" {
		return errorResult(missingSessionName("read")), nil
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
	out, err := m.ReadSessionOutput(ctx, sessionName, maxBytes, offset, waitMs)
	if err != nil {
		return errorResult(err), nil
	}
	if jsonResultsWanted(m, sessionName, "") {
		raw, _ := marshalCompactJSON(out)
		return mcp.NewToolResultText(raw), nil
	}
	return mcp.NewToolResultText(sessionReadText(out)), nil
}

// sessionReadText renders one background read. A chunk cut by maxBytes says
// so in the header and at the end: an agent that mistakes a prefix for the
// whole output stops polling early and reasons over incomplete evidence.
func sessionReadText(out *manager.SessionOutput) string {
	header := fmt.Sprintf("session=%s running=%v totalBytes=%d offset=%d",
		out.SessionName, out.Running, out.TotalBytes, out.Offset)
	if out.DroppedBytes > 0 {
		header += fmt.Sprintf(" droppedBytes=%d logMaxBytes=%d", out.DroppedBytes, out.LogMaxBytes)
	}
	if out.Truncated {
		header += fmt.Sprintf(" truncated=true remainingBytes=%d", out.RemainingBytes)
	}
	text := header + "\n\n" + out.Output
	if out.Truncated {
		text += fmt.Sprintf(
			"\n\n[output truncated: %d more bytes are already available; call session action=read again with offset=%d to continue]",
			out.RemainingBytes, out.Offset)
	}
	return text
}

// sessionListResult lists active sessions, optionally filtered to one
// connection.
func sessionListResult(m *manager.Manager, args map[string]any) *mcp.CallToolResult {
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
	if jsonResultsWanted(m, "", connectionName) {
		raw, _ := marshalCompactJSON(sessions)
		return mcp.NewToolResultText(raw)
	}
	return mcp.NewToolResultText(formatSessionList(sessions))}
package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerFileTransfer registers the file-transfer tool, which consolidates
// upload and download into a single tool with progress notifications.
func registerFileTransfer(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("file-transfer",
		mcp.WithDescription("Upload or download a file between the local machine and the connected server. Transfers are deduplicated (skipped when the destination already matches) and resumed from existing partial data unless force is set. Progress is reported via MCP progress notifications when the client requests them."),
		mcp.WithString("action", mcp.Required(), mcp.Description("Operation to perform"), mcp.Enum("upload", "download")),
		mcp.WithString("localPath", mcp.Required(), mcp.Description("Local path")),
		mcp.WithString("remotePath", mcp.Required(), mcp.Description("Remote path (absolute POSIX path)")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name (optional, default is 'default')")),
		mcp.WithBoolean("force", mcp.Description("Skip the dedup check and transfer the full file from scratch (default: false)")),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := request.GetArguments()

		action, _ := args["action"].(string)
		localPath, _ := args["localPath"].(string)
		remotePath, _ := args["remotePath"].(string)
		connectionName, _ := args["connectionName"].(string)
		force, _ := args["force"].(bool)

		if action != "upload" && action != "download" {
			return errorResult(fmt.Errorf("action must be 'upload' or 'download', got: %s", action)), nil
		}

		var token mcp.ProgressToken
		if request.Params.Meta != nil {
			token = request.Params.Meta.ProgressToken
		}
		sender := newProgressSender(s, token)

		result, err := m.TransferFile(action, localPath, remotePath, connectionName, force, sender.send)
		if err != nil {
			return errorResult(err), nil
		}

		// Always report the final 100% progress.
		if token != nil && !result.Skipped {
			sender.send(result.Bytes+result.ResumedFrom, result.Bytes+result.ResumedFrom)
		}

		var text string
		switch {
		case result.Skipped:
			text = fmt.Sprintf("%s skipped: %s already matches %s (same size and mtime)",
				action, result.RemotePath, result.LocalPath)
		case result.Resumed:
			text = fmt.Sprintf(
				"%s resumed from %d bytes: %s -> %s\n%d bytes transferred in %s (%.2f MB/s, %.1f%%)",
				action, result.ResumedFrom, result.LocalPath, result.RemotePath,
				result.Bytes, result.Elapsed.Round(time.Millisecond),
				result.SpeedBps/1024/1024, result.Percent,
			)
		default:
			text = fmt.Sprintf(
				"%s completed: %s -> %s\n%d bytes in %s (%.2f MB/s, %.1f%%)",
				action, result.LocalPath, result.RemotePath,
				result.Bytes, result.Elapsed.Round(time.Millisecond),
				result.SpeedBps/1024/1024, result.Percent,
			)
		}
		return mcp.NewToolResultText(text), nil
	})
}
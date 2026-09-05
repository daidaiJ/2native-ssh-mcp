package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerFileTransfer registers the file-transfer tool, which consolidates
// upload and download into a single tool with progress notifications.
// Directories transfer recursively (per-file atomic uploads, dedup and
// resume apply per entry); single files are sha256-verified when the remote
// has sha256sum.
func registerFileTransfer(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("file-transfer",
		mcp.WithDescription("Upload or download a file OR a directory (transferred recursively) between the local machine and the connected server. Single-file uploads are atomic (remote <target>.part + rename after a sha256 check) and never truncate an existing target on failure; transfers are deduplicated (skipped when the destination already matches) and resumed from an existing .part unless force is set. Progress is reported via MCP progress notifications to the requesting client when the client requests them."),
		mutatingAnnotation("Transfer file via SFTP", true),
		mcp.WithString("action", mcp.Required(), mcp.Description("Operation to perform"), mcp.Enum("upload", "download")),
		mcp.WithString("localPath", mcp.Required(), mcp.Description("Local path (a directory transfers recursively)")),
		mcp.WithString("remotePath", mcp.Required(), mcp.Description("Remote path (absolute POSIX path; a directory transfers recursively)")),
		mcp.WithString("connectionName", mcp.Description("SSH connection name or alias from list-servers (optional; defaults to the default connection)")),
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
		sender := newProgressSender(s, server.ClientSessionFromContext(ctx), token)

		result, err := m.TransferFile(ctx, action, localPath, remotePath, connectionName, force, sender.send)
		if err != nil {
			return errorResult(err), nil
		}

		// Always report the final 100% progress.
		if token != nil && !result.Skipped {
			sender.send(result.Bytes+result.ResumedFrom, result.Bytes+result.ResumedFrom)
		}
		return transferResultJSON(result), nil
	})
}

// transferResultJSON renders the transfer outcome as prose plus the full
// structured TransferResult (bytes/elapsed/speed/skipped/resumed/checksum/
// files/failed), so agents can read fields instead of parsing text.
// elapsedSeconds converts the raw duration to seconds rounded to 3 decimals,
// keeping millisecond precision for short transfers.
func elapsedSeconds(d time.Duration) float64 {
	return math.Round(d.Seconds()*1000) / 1000
}

// formatSeconds renders seconds compactly for the prose line: 0.02s, 1.23s,
// 12.3s, 123s — always with the unit, no Go-style "1m2s" composites.
func formatSeconds(s float64) string {
	switch {
	case s < 10:
		return fmt.Sprintf("%.2fs", s)
	case s < 100:
		return fmt.Sprintf("%.1fs", s)
	default:
		return fmt.Sprintf("%.0fs", s)
	}
}

func transferResultJSON(result *manager.TransferResult) *mcp.CallToolResult {
	result.ElapsedS = elapsedSeconds(result.Elapsed)
	result.SpeedBps = math.Round(result.SpeedBps)
	var text string
	switch {
	case result.Skipped:
		text = fmt.Sprintf("%s skipped: %s already matches %s (same size and mtime)",
			result.Action, result.RemotePath, result.LocalPath)
	case result.Files > 0:
		text = fmt.Sprintf("%s completed: %s -> %s\n%d files, %d bytes in %s (%.2f MB/s, %.1f%%)",
			result.Action, result.LocalPath, result.RemotePath, result.Files,
			result.Bytes, formatSeconds(result.ElapsedS),
			result.SpeedBps/1024/1024, result.Percent)
		if result.SkippedFiles > 0 {
			text += fmt.Sprintf(", %d skipped (deduplicated)", result.SkippedFiles)
		}
		if result.ResumedFiles > 0 {
			text += fmt.Sprintf(", %d resumed", result.ResumedFiles)
		}
		if len(result.Failed) > 0 {
			text += fmt.Sprintf("\n%d failed:\n  %s", len(result.Failed), strings.Join(result.Failed, "\n  "))
		}
	case result.Resumed:
		text = fmt.Sprintf(
			"%s resumed from %d bytes: %s -> %s\n%d bytes transferred in %s (%.2f MB/s, %.1f%%)",
			result.Action, result.ResumedFrom, result.LocalPath, result.RemotePath,
			result.Bytes, formatSeconds(result.ElapsedS),
			result.SpeedBps/1024/1024, result.Percent,
		)
	default:
		text = fmt.Sprintf(
			"%s completed: %s -> %s\n%d bytes in %s (%.2f MB/s, %.1f%%)",
			result.Action, result.LocalPath, result.RemotePath,
			result.Bytes, formatSeconds(result.ElapsedS),
			result.SpeedBps/1024/1024, result.Percent,
		)
	}
	switch result.ChecksumStatus {
	case "verified":
		text += fmt.Sprintf("\nsha256 verified: %s", result.Checksum)
	case "unverified":
		text += "\nsha256 unverified (no sha256sum on the remote)"
	}

	raw, _ := json.MarshalIndent(result, "", "  ")
	return mcp.NewToolResultText(text + "\n\nJSON:\n" + string(raw))
}

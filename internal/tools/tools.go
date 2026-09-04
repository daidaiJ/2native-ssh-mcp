// Package tools registers the MCP tools exposed by the server.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// RegisterAll registers every tool on the MCP server.
func RegisterAll(s *server.MCPServer, m *manager.Manager) {
	registerExecuteCommand(s, m)
	registerFileTransfer(s, m)
	registerListServers(s, m)
	registerSession(s, m)
}

// errorResult formats a ToolError the same way the reference implementation
// does: a JSON object with code, message and retriable.
func errorResult(err error) *mcp.CallToolResult {
	te := manager.AsToolError(err)
	text, _ := json.MarshalIndent(map[string]any{
		"code":      te.Code,
		"message":   te.Message,
		"retriable": te.Retriable,
	}, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(text))},
		IsError: true,
	}
}

// errorResultFor formats a ToolError together with the partial CommandResult
// captured before the failure (timeout, output limit, connection loss). The
// message stays short; the partial stdout/stderr travel in the same result.
func errorResultFor(err error, res manager.CommandResult) *mcp.CallToolResult {
	te := manager.AsToolError(err)
	payload := map[string]any{
		"code":      te.Code,
		"message":   te.Message,
		"retriable": te.Retriable,
	}
	if res.Status != "" {
		payload["stdout"] = res.Stdout
		payload["stderr"] = res.Stderr
		payload["exitCode"] = res.ExitCode
		payload["status"] = res.Status
		payload["partial"] = res.Partial
		payload["replaySafe"] = res.ReplaySafe
		if res.CWD != "" {
			payload["cwd"] = res.CWD
		}
		if res.OutputFile != "" {
			payload["outputFile"] = res.OutputFile
			payload["outputFileBytes"] = res.OutputFileBytes
			payload["outputFileLines"] = res.OutputFileLines
		}
		if res.Truncated {
			payload["truncated"] = true
			if res.ClippedBytes > 0 {
				payload["clippedBytes"] = res.ClippedBytes
			}
		}
	}
	text, _ := json.MarshalIndent(payload, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(text))},
		IsError: true,
	}
}

// progressSender sends MCP progress notifications, throttled to at most one
// per 100ms, with a final 100% notification. Safe for concurrent use: SFTP
// copy workers call send from several goroutines. When a client session is
// available from the request context, notifications go only to that session;
// otherwise (e.g. unknown transport) they broadcast to all clients.
type progressSender struct {
	server      *server.MCPServer
	session     server.ClientSession
	token       mcp.ProgressToken
	mu          sync.Mutex
	lastSent    int64
	lastPercent int
}

func newProgressSender(s *server.MCPServer, session server.ClientSession, token mcp.ProgressToken) *progressSender {
	return &progressSender{server: s, session: session, token: token}
}

func (p *progressSender) send(done, total int64) {
	if p.token == nil {
		return
	}
	now := time.Now().UnixMilli()
	percent := 0
	if total > 0 {
		percent = int(float64(done) / float64(total) * 100)
	}
	p.mu.Lock()
	if now-p.lastSent < 100 && percent < 100 {
		p.mu.Unlock()
		return
	}
	if percent == p.lastPercent && percent < 100 {
		p.mu.Unlock()
		return
	}
	p.lastSent = now
	p.lastPercent = percent
	p.mu.Unlock()

	params := map[string]any{
		"progressToken": p.token,
		"progress":      float64(done),
		"message":       fmt.Sprintf("%d / %d bytes (%d%%)", done, total, percent),
	}
	if total > 0 {
		params["total"] = float64(total)
	}
	notification := mcp.JSONRPCNotification{
		JSONRPC: mcp.JSONRPC_VERSION,
		Notification: mcp.Notification{
			Method: "notifications/progress",
			Params: mcp.NotificationParams{AdditionalFields: params},
		},
	}
	if p.session != nil {
		// Non-blocking: a stalled or slow reader must not stall the transfer.
		select {
		case p.session.NotificationChannel() <- notification:
		default:
		}
		return
	}
	p.server.SendNotificationToAllClients("notifications/progress", params)
}

// ToolHandler is the standard tool handler signature.
type ToolHandler = func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)
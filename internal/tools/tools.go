// Package tools registers the MCP tools exposed by the server.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
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
	}
	text, _ := json.MarshalIndent(payload, "", "  ")
	return &mcp.CallToolResult{
		Content: []mcp.Content{mcp.NewTextContent(string(text))},
		IsError: true,
	}
}

// progressSender sends MCP progress notifications, throttled to at most one
// per 100ms, with a final 100% notification.
type progressSender struct {
	server      *server.MCPServer
	token       mcp.ProgressToken
	lastSent    int64
	lastPercent int
}

func newProgressSender(s *server.MCPServer, token mcp.ProgressToken) *progressSender {
	return &progressSender{server: s, token: token}
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
	if now-p.lastSent < 100 && percent < 100 {
		return
	}
	if percent == p.lastPercent && percent < 100 {
		return
	}
	p.lastSent = now
	p.lastPercent = percent

	params := map[string]any{
		"progressToken": p.token,
		"progress":      float64(done),
		"message":       fmt.Sprintf("%d / %d bytes (%d%%)", done, total, percent),
	}
	if total > 0 {
		params["total"] = float64(total)
	}
	p.server.SendNotificationToAllClients("notifications/progress", params)
}

// ToolHandler is the standard tool handler signature.
type ToolHandler = func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error)
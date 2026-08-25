package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerListServers registers the list-servers tool.
func registerListServers(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("list-servers",
		mcp.WithDescription("List configured SSH servers (metadata, connection status, system info) and active named sessions. Call this first to pick connectionName or sessionName."),
		readOnlyAnnotation("List SSH servers and sessions"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		servers := m.GetAllServerInfos()
		sessions := m.ListSessions()
		return mcp.NewToolResultText(formatInventory(servers, sessions)), nil
	})
}

func formatInventory(servers []manager.ServerInfo, sessions []manager.SessionInfo) string {
	var parts []string
	parts = append(parts, formatServerList(servers))
	parts = append(parts, formatSessionList(sessions))
	return strings.Join(parts, "\n\n")
}

// formatServerList renders configured servers: summary lines + raw JSON.
func formatServerList(servers []manager.ServerInfo) string {
	if len(servers) == 0 {
		return "Configured SSH servers:\n(none)"
	}

	var lines []string
	for _, server := range servers {
		state := "disconnected"
		if server.Connected {
			state = "connected"
		}
		parts := []string{
			fmt.Sprintf("[%s] %s", state, server.Name),
			fmt.Sprintf("%s@%s:%d", server.Username, server.Host, server.Port),
		}
		if len(server.Aliases) > 0 {
			parts = append(parts, "aliases="+strings.Join(server.Aliases, ","))
		}
		if server.Description != "" {
			parts = append(parts, "description="+server.Description)
		}
		if server.Business != "" {
			parts = append(parts, "business="+server.Business)
		}
		if server.Notes != "" {
			parts = append(parts, "notes="+server.Notes)
		}
		if server.Status != nil {
			if server.Status.Hostname != "" {
				parts = append(parts, "hostname="+server.Status.Hostname)
			}
			if server.Status.OSName != "" {
				parts = append(parts, "os="+server.Status.OSName)
			}
			if server.Status.LastUpdated != "" {
				parts = append(parts, "updated="+server.Status.LastUpdated)
			}
		}
		lines = append(lines, strings.Join(parts, " | "))
	}

	raw, _ := json.Marshal(servers)
	return "Configured SSH servers:\n" + strings.Join(lines, "\n") + "\n\nServers JSON:\n" + string(raw)
}

func formatSessionList(sessions []manager.SessionInfo) string {
	if len(sessions) == 0 {
		return "Active sessions:\n(none)"
	}
	var lines []string
	for _, s := range sessions {
		parts := []string{
			fmt.Sprintf("session=%s", s.Name),
			fmt.Sprintf("connection=%s", s.ConnectionName),
			fmt.Sprintf("idle=%ds", s.IdleSeconds),
		}
		if s.CWD != "" {
			parts = append(parts, "cwd="+s.CWD)
		}
		if s.Disconnected {
			parts = append(parts, "disconnected=true")
		}
		if s.Background {
			parts = append(parts, "background=true", fmt.Sprintf("running=%v", s.Running))
			if s.BGCommand != "" {
				parts = append(parts, "cmd="+s.BGCommand)
			}
		}
		lines = append(lines, strings.Join(parts, " | "))
	}
	raw, _ := json.Marshal(sessions)
	return "Active sessions:\n" + strings.Join(lines, "\n") + "\n\nSessions JSON:\n" + string(raw)
}

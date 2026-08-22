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
		mcp.WithDescription("List all available SSH server configurations"),
	)

	s.AddTool(tool, func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		servers := m.GetAllServerInfos()
		return mcp.NewToolResultText(formatServerList(servers)), nil
	})
}

// formatServerList renders the server summary the same way the reference
// implementation does: a readable summary followed by the raw JSON.
func formatServerList(servers []manager.ServerInfo) string {
	if len(servers) == 0 {
		return "No SSH servers configured."
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
	return "Configured SSH servers:\n" + strings.Join(lines, "\n") + "\n\nRaw JSON:\n" + string(raw)
}
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// registerListServers registers the list-servers tool.
func registerListServers(s *server.MCPServer, m *manager.Manager) {
	tool := mcp.NewTool("list-servers",
		mcp.WithDescription("List configured SSH servers (metadata, connection status, system info, last few recorded commands) and active named sessions. Call this first to pick connectionName or sessionName — the recent commands help resume context from a previous session."),
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
		if recent := formatRecentCommands(server.RecentCommands); recent != "" {
			parts = append(parts, recent)
		}
		lines = append(lines, strings.Join(parts, " | "))
	}

	raw, _ := json.Marshal(servers)
	return "Configured SSH servers:\n" + strings.Join(lines, "\n") + "\n\nServers JSON:\n" + string(raw)
}

// recentCommandMaxRunes caps each command preview so one long command cannot
// dominate the inventory line.
const recentCommandMaxRunes = 60

// formatRecentCommands renders the last few recorded commands as a single
// "recent=5m cmd · 2h cmd (exit 7)" segment. Age is relative so an agent can
// judge staleness at a glance and timezones never matter; the exit code is
// shown only when non-zero. Returns "" when there is nothing to show (log
// disabled or no commands yet).
func formatRecentCommands(entries []manager.CommandLogEntry) string {
	return formatRecentCommandsAt(time.Now(), entries)
}

func formatRecentCommandsAt(now time.Time, entries []manager.CommandLogEntry) string {
	if len(entries) == 0 {
		return ""
	}
	var items []string
	for _, e := range entries {
		item := fmt.Sprintf("%s %s",
			compactAge(now.Sub(e.Timestamp)), truncateRunes(e.Command, recentCommandMaxRunes))
		if e.ExitCode != 0 {
			item += fmt.Sprintf(" (exit %d)", e.ExitCode)
		}
		items = append(items, item)
	}
	return "recent=" + strings.Join(items, " · ")
}

// compactAge renders a duration as 42s / 5m / 2h / 3d, clamping clock skew.
func compactAge(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// truncateRunes collapses whitespace and clips s to max runes, appending an
// ellipsis when clipped.
func truncateRunes(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max]) + "…"
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

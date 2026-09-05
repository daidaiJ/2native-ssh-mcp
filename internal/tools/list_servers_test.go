package tools

import (
	"strings"
	"testing"
	"time"

	"2native-ssh-mcp/internal/manager"
)

func TestFormatServerListIncludesMetadata(t *testing.T) {
	text := formatServerList([]manager.ServerInfo{
		{
			Name:        "centos",
			Aliases:     []string{"CentOS", "dev1"},
			Description: "CentOS 测试机",
			Business:    "MCP 联调",
			Notes:       "勿做破坏性操作",
			Host:        "192.0.2.1",
			Port:        22,
			Username:    "testuser",
		},
	})
	for _, want := range []string{"centos", "CentOS", "CentOS 测试机", "MCP 联调", "勿做破坏性操作", "192.0.2.1"} {
		if !strings.Contains(text, want) {
			t.Fatalf("formatted list missing %q:\n%s", want, text)
		}
	}
}

func TestFormatRecentCommands(t *testing.T) {
	now := time.Date(2026, 9, 5, 19, 21, 0, 0, time.Local)
	entries := []manager.CommandLogEntry{
		{Timestamp: now.Add(-3 * time.Minute), Command: "seq 1 3000", ExitCode: 0},
		{Timestamp: now.Add(-2 * time.Hour), Command: "echo panda1221 | sudo -S apt update", ExitCode: 3},
		{Timestamp: now.Add(-3 * 24 * time.Hour), Command: "old deploy", ExitCode: 0},
		{Timestamp: now.Add(2 * time.Minute), Command: "clock-skew", ExitCode: 0},
	}
	got := formatRecentCommandsAt(now, entries)
	for _, want := range []string{"recent=3m seq 1 3000", "2h echo panda1221", " (exit 3)", "3d old deploy", "0s clock-skew"} {
		if !strings.Contains(got, want) {
			t.Fatalf("recent segment missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "(exit 0)") || strings.Contains(got, " (0)") {
		t.Fatalf("zero exit code must be omitted from the summary:\n%s", got)
	}
	if formatRecentCommands(nil) != "" {
		t.Fatal("no entries must render as empty segment")
	}
	long := strings.Repeat("a", 100)
	trunc := truncateRunes(long, 60)
	if len([]rune(trunc)) != 61 || !strings.HasSuffix(trunc, "…") {
		t.Fatalf("truncateRunes must clip to 60 runes + ellipsis, got %d runes", len([]rune(trunc)))
	}
	if got := truncateRunes("multi\nline\tcommand", 60); got != "multi line command" {
		t.Fatalf("truncateRunes must collapse whitespace, got %q", got)
	}
}

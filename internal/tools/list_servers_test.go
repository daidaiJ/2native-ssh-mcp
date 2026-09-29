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

// A via target must be labeled as such so the agent knows the host is not
// directly reachable (and the JSON carries it too).
func TestFormatServerListShowsVia(t *testing.T) {
	text := formatServerList([]manager.ServerInfo{
		{Name: "internal-db", Host: "10.0.1.50", Port: 22, Username: "dbadmin", Via: "bastion"},
	})
	if !strings.Contains(text, "via=bastion") {
		t.Fatalf("summary line must show the jump host:\n%s", text)
	}
	if !strings.Contains(text, `"via":"bastion"`) {
		t.Fatalf("servers JSON must show the jump host:\n%s", text)
	}
}

// The default connection must be labeled so the agent knows which connection
// runs when connectionName is omitted (only possible with an explicit
// "default" connection).
func TestFormatServerListShowsDefault(t *testing.T) {
	text := formatServerList([]manager.ServerInfo{
		{Name: "centos", Host: "h", Port: 22, Username: "u"},
		{Name: "default", Host: "h", Port: 22, Username: "u", Default: true},
	})
	if !strings.Contains(text, "default=true (used when connectionName is omitted)") {
		t.Fatalf("summary line must mark the default connection:\n%s", text)
	}
	if !strings.Contains(text, `"default":true`) {
		t.Fatalf("servers JSON must mark the default connection:\n%s", text)
	}
	if strings.Count(text, "default=true") != 1 {
		t.Fatalf("only the default connection must be marked:\n%s", text)
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

// Session summaries must expose the host behind the connection so agents
// never guess which machine an active session — especially a default-routed
// one — is attached to.
func TestFormatSessionListShowsHost(t *testing.T) {
	text := formatSessionList([]manager.SessionInfo{
		{Name: "build", ConnectionName: "default", IdleSeconds: 5, Host: "203.0.113.5", Port: 22},
	})
	if !strings.Contains(text, "host=203.0.113.5:22") {
		t.Fatalf("session summary must carry the host:\n%s", text)
	}
}

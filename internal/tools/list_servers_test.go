package tools

import (
	"strings"
	"testing"

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

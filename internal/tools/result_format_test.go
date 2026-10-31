package tools

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// newFormatTestManager builds a manager over the given connections.
func newFormatTestManager(t *testing.T, conns ...*config.SSHConfig) *manager.Manager {
	return newFormatTestManagerWith(t, nil, conns...)
}

// newFormatTestManagerWith builds a manager with extra construction options.
func newFormatTestManagerWith(t *testing.T, opts []manager.Option, conns ...*config.SSHConfig) *manager.Manager {
	t.Helper()
	configs := make(map[string]*config.SSHConfig, len(conns))
	for _, cfg := range conns {
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("normalize %s: %v", cfg.Name, err)
		}
		configs[cfg.Name] = cfg
	}
	m, err := manager.New(configs, "", opts...)
	if err != nil {
		t.Fatalf("manager.New: %v", err)
	}
	t.Cleanup(m.DisconnectAll)
	return m
}

// The text rendering already carries the same facts as the JSON, so a result
// must never contain both: the format is per connection, text by default.
func TestJSONResultsWanted(t *testing.T) {
	jsonCfg := &config.SSHConfig{Name: "js", Host: "h", Port: 22, Username: "u", ResultFormat: config.ResultFormatJSON}
	textCfg := &config.SSHConfig{Name: "tx", Host: "h", Port: 22, Username: "u"}
	m := newFormatTestManager(t, jsonCfg, textCfg)

	if !jsonResultsWanted(m, "", "js") {
		t.Fatal("resultFormat=json must ask for the structured body")
	}
	if jsonResultsWanted(m, "", "tx") {
		t.Fatal("resultFormat unset must keep the text body")
	}
	if jsonResultsWanted(m, "no-such-session", "") {
		t.Fatal("an unresolvable target must not switch to JSON")
	}

	// A call that names no connection at all (list-servers) follows the
	// $global default instead.
	global := newFormatTestManagerWith(t, []manager.Option{manager.WithGlobalResultFormat(config.ResultFormatJSON)},
		&config.SSHConfig{Name: "solo", Host: "h", Port: 22, Username: "u"})
	if !jsonResultsWanted(global, "", "") {
		t.Fatal("$global.resultFormat must reach calls that name no connection")
	}
}

func TestFormatInventoryJSONOnlyOnRequest(t *testing.T) {
	servers := []manager.ServerInfo{{Name: "centos", Host: "192.0.2.1", Port: 22, Username: "root"}}
	sessions := []manager.SessionInfo{{Name: "build", ConnectionName: "centos", IdleSeconds: 3}}

	text := formatInventory(servers, sessions, false)
	if text == "" || !strings.Contains(text, "centos") || !strings.Contains(text, "build") {
		t.Fatalf("inventory must list servers and sessions:\n%s", text)
	}
	if strings.Contains(text, `"servers"`) {
		t.Fatalf("the JSON inventory must not be sent by default:\n%s", text)
	}

	structured := formatInventory(servers, sessions, true)
	if !strings.Contains(structured, `"servers":[{"name":"centos"`) || !strings.Contains(structured, `"sessions":[{"name":"build"`) {
		t.Fatalf("resultFormat=json must send the structured inventory:\n%s", structured)
	}
	if strings.Contains(structured, "Configured SSH servers") {
		t.Fatalf("JSON mode must send one payload, not the text as well:\n%s", structured)
	}
}

package tools

import (
	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// jsonResultsWanted reports whether a result should carry structured JSON
// only, per the connection's resultFormat setting (also settable once for
// every connection with $global.resultFormat). Text is the default: the
// human-readable rendering already states the same facts, so sending both
// copies — as these tools used to — doubles what the model reads for no new
// information.
//
// A call that names no connection at all (list-servers) has no per-connection
// setting to read, so it follows the $global default.
func jsonResultsWanted(m *manager.Manager, sessionName, connectionName string) bool {
	cfg := m.ConfigFor(sessionName, connectionName)
	if cfg == nil {
		return m.GlobalResultFormat() == config.ResultFormatJSON
	}
	return cfg.GetResultFormat() == config.ResultFormatJSON
}

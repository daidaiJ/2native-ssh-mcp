package tools

import (
	"context"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/logger"
)

// LogToolCalls logs one line per tool call: the tool name, how long it took
// and whether it ended as an error. It is registered outside
// server.WithRecovery, so a recovered panic shows up as the failed call it is
// instead of disappearing into the transport. The result is passed through
// untouched — this is observability only.
func LogToolCalls(next server.ToolHandlerFunc) server.ToolHandlerFunc {
	return func(ctx context.Context, request mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		start := time.Now()
		result, err := next(ctx, request)
		took := time.Since(start).Round(time.Millisecond)
		switch {
		case err != nil:
			logger.Warn("tool %s failed in %s: %v", request.Params.Name, took, err)
		case result != nil && result.IsError:
			logger.Warn("tool %s returned an error result in %s", request.Params.Name, took)
		default:
			logger.Debug("tool %s ok in %s", request.Params.Name, took)
		}
		return result, err
	}
}

// Instructions is the usage note sent once in the initialize reply. It
// carries the cross-tool contract — connection selection, where large output
// goes, and what an interrupted call means — so the individual tool
// descriptions only have to state what that tool does.
const Instructions = "SSH connections come from the user's configuration: call list-servers first and pass connectionName (a connection named 'default' serves calls that omit it). " +
	"Large command output is written to a local spill file whose path is in the result; Read or Grep that file instead of re-running the command or reading it remotely. " +
	"Output that was compressed or truncated says so. " +
	"A timeout or a dropped connection is not a failed command: the remote process may still be running, so check the remote state before retrying and never replay a command blindly. " +
	"Work that outlives one call belongs in a background session (session action=open with background=true), polled with session action=read."

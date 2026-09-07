package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/approval"
	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/manager"
)

// elicitationTimeout bounds the approval prompt: a client that advertised
// elicitation but never answers must not wedge the tool call forever. Var so
// tests can shorten it.
var elicitationTimeout = 5 * time.Minute

// approvalGate sits between the tool arguments and the SSH execution for
// destructive commands. It is a server-side best-effort second gate on top of
// the client's own auto-approve lists: clients that tick "always allow
// execute-command" still hit the elicitation prompt here.
//
// Semantics (issue #5):
//   - approvalMode "auto" (default): gate is a no-op, behavior unchanged.
//   - "ask-destructive" + client supports elicitation: destructive commands
//     (built-in classifier, widened by approvalPatterns, relaxed by
//     approvalExemptPatterns) wait for the human to accept.
//   - "ask-destructive" + client does not support elicitation: fail-open —
//     the command runs and the result carries an advisory note telling the
//     agent to consult the user before destructive actions on this
//     connection. A client capability gap must not lock the user out.
//
// It only gates execute-command; file transfers take structured, visible
// arguments and carry no free-text risk surface.
func approvalGate(
	ctx context.Context,
	srv *server.MCPServer,
	cfg *config.SSHConfig,
	cmdString string,
) (proceed bool, note string, decline *mcp.CallToolResult) {
	if cfg == nil || cfg.ApprovalMode != config.ApprovalModeAskDestructive {
		return true, "", nil
	}
	destructive, reason := approval.IsDestructive(cmdString, cfg.ApprovalPatterns, cfg.ApprovalExemptPatterns)
	if !destructive {
		return true, "", nil
	}

	elicitationCtx, cancel := context.WithTimeout(ctx, elicitationTimeout)
	defer cancel()
	result, err := srv.RequestElicitation(elicitationCtx, confirmationRequest(cfg.Name, cmdString, reason))
	switch {
	case err == nil:
		if result.Action != mcp.ElicitationResponseActionAccept || confirmDeclined(result) {
			verb, ok := map[mcp.ElicitationResponseAction]string{
				mcp.ElicitationResponseActionDecline: "declined",
				mcp.ElicitationResponseActionCancel:  "cancelled",
			}[result.Action]
			if !ok {
				verb = "did not approve"
			}
			return false, "", declinedResult(verb)
		}
		return true, "", nil
	case ctx.Err() != nil:
		// The caller's context is gone (client disconnected): fail-open like
		// any other transport failure.
		return true, advisoryNote(cfg.Name, "elicitation aborted: client disconnected"), nil
	case elicitationCtx.Err() == context.DeadlineExceeded:
		// The user did not answer in time: fail closed. A silent timeout must
		// never green-light a destructive command.
		return false, "", declinedResult(fmt.Sprintf("did not respond within %s to", elicitationTimeout))
	case errors.Is(err, server.ErrNoActiveSession), errors.Is(err, server.ErrElicitationNotSupported):
		// The client never advertised elicitation: fail-open with advice.
		return true, advisoryNote(cfg.Name, "the client does not support MCP elicitation"), nil
	default:
		// A transport error in an already-requested elicitation is treated
		// the same way: the design decision is that approval is best-effort.
		return true, advisoryNote(cfg.Name, fmt.Sprintf("elicitation failed: %v", err)), nil
	}
}

// confirmationRequest builds the elicitation shown to the human.
func confirmationRequest(connName, cmdString, reason string) mcp.ElicitationRequest {
	return mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message: fmt.Sprintf(
				"Destructive command on SSH connection %q\n%s\nReason: %s\nAllow execution?",
				connName, truncateRunes(cmdString, 500), reason),
			RequestedSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"confirm": map[string]any{
						"type":        "boolean",
						"title":       "Allow execution",
						"description": "true runs the command on the remote host",
						"default":     true,
					},
				},
				"required": []string{"confirm"},
			},
		},
	}
}

// confirmDeclined honors a client that returns accept with confirm=false
// instead of declining.
func confirmDeclined(result *mcp.ElicitationResult) bool {
	if result == nil || result.Content == nil {
		return false
	}
	content, ok := result.Content.(map[string]any)
	if !ok {
		return false
	}
	confirm, ok := content["confirm"].(bool)
	return ok && !confirm
}

// declinedResult tells the agent the human refused (or did not answer in
// time), as data rather than an error so the agent adapts instead of retrying.
func declinedResult(verb string) *mcp.CallToolResult {
	payload := map[string]any{
		"executed": false,
		"reason": fmt.Sprintf(
			"the user %s the request to run this destructive command; do not retry it without asking them what to do instead",
			verb),
	}
	text, _ := marshalResultJSON(payload)
	return mcp.NewToolResultText(text)
}

// advisoryNote is the fail-open message appended to a result that ran without
// the configured approval.
func advisoryNote(connName, why string) string {
	return fmt.Sprintf(
		"Note: connection %q has approvalMode \"ask-destructive\" but %s, so this command ran without confirmation. Ask the user for confirmation before destructive actions on this connection.",
		connName, why)
}

// appendNote adds the fail-open advisory to a successful result's text.
func appendNote(res *mcp.CallToolResult, note string) *mcp.CallToolResult {
	if note == "" || res == nil || len(res.Content) == 0 {
		return res
	}
	if text, ok := res.Content[0].(mcp.TextContent); ok {
		res.Content[0] = mcp.NewTextContent(strings.TrimRight(text.Text, "\n") + "\n\n" + note)
	}
	return res
}

// wrapWithGate resolves the request's connection and runs approvalGate,
// returning (proceed, note, decline-result).
func wrapWithGate(ctx context.Context, srv *server.MCPServer, m *manager.Manager, sessionName, connectionName, cmdString string) (bool, string, *mcp.CallToolResult) {
	cfg := m.ConfigFor(sessionName, connectionName)
	return approvalGate(ctx, srv, cfg, cmdString)
}

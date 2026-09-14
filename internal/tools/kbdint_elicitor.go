package tools

import (
	"context"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// sshElicitor adapts MCP elicitation to the manager.Elicitor interface: it
// turns the SSH server's non-password keyboard-interactive questions (OTP
// and similar challenges) into one elicitation prompt on the MCP session
// that triggered the dial.
//
// The prompt text is deliberately different from the destructive-command
// approval dialog ("SSH authentication …" vs "Destructive command …") so a
// user can never mistake an OTP prompt for an execution confirmation.
type sshElicitor struct {
	srv *server.MCPServer
}

// sshElicitorPromptTitle prefixes the OTP elicitation message. Asserted in
// tests to stay distinguishable from the approval gate's wording.
const sshElicitorPromptTitle = "SSH authentication"

// Prompt implements manager.Elicitor. It receives only the non-password
// questions and returns one answer per question. Errors (declined,
// cancelled, timed out, client without elicitation support) are returned to
// the caller, which degrades to the SSH_MCP_2FA_CODE fallback — never to a
// forced disconnect.
func (e sshElicitor) Prompt(ctx context.Context, ch manager.Challenge) ([]string, error) {
	properties := make(map[string]any, len(ch.Questions))
	required := make([]string, 0, len(ch.Questions))
	for i, q := range ch.Questions {
		name := fmt.Sprintf("answer_%d", i)
		field := map[string]any{
			"type":        "string",
			"title":       q,
			"description": q,
		}
		if i < len(ch.Echos) && !ch.Echos[i] {
			// Render as a secret when the schema allows it; OTP is not a
			// long-lived credential, plaintext fallback is acceptable.
			field["format"] = "password"
		}
		properties[name] = field
		required = append(required, name)
	}

	request := mcp.ElicitationRequest{
		Params: mcp.ElicitationParams{
			Message: fmt.Sprintf(
				"%s — connection %q\n%s\nAnswer the server's prompt(s) and submit.",
				sshElicitorPromptTitle, ch.Connection, strings.Join(ch.Questions, "\n")),
			RequestedSchema: map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		},
	}
	result, err := e.srv.RequestElicitation(ctx, request)
	if err != nil {
		return nil, err
	}
	if result == nil || result.Action != mcp.ElicitationResponseActionAccept {
		verb := "did not answer"
		if result != nil {
			switch result.Action {
			case mcp.ElicitationResponseActionDecline:
				verb = "declined"
			case mcp.ElicitationResponseActionCancel:
				verb = "cancelled"
			}
		}
		return nil, fmt.Errorf("the user %s the SSH authentication prompt", verb)
	}

	content, _ := result.Content.(map[string]any)
	answers := make([]string, len(ch.Questions))
	for i := range ch.Questions {
		if v, ok := content[fmt.Sprintf("answer_%d", i)].(string); ok {
			answers[i] = v
		}
	}
	return answers, nil
}

// withElicitor attaches the MCP elicitation adapter to the request context
// so any dial triggered by this tool call can prompt for OTP challenges.
func withElicitor(ctx context.Context, srv *server.MCPServer) context.Context {
	return manager.WithElicitor(ctx, sshElicitor{srv: srv})
}

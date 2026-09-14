package tools

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/manager"
)

// The OTP prompt and the destructive-command approval dialog both reach the
// same human through elicitation; their wording must never be confusable.
func TestElicitorPromptTitleDistinctFromApproval(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"answer_0": "246810"},
		},
	}}
	e := sshElicitor{srv: server.NewMCPServer("test", "1.0.0", server.WithElicitation())}
	answers, err := e.Prompt(ctxWithSession(s), manager.Challenge{
		Connection: "bastion",
		Name:       "double auth",
		Questions:  []string{"Verification code:"},
		Echos:      []bool{false},
	})
	if err != nil {
		t.Fatalf("accepted prompt should return answers: %v", err)
	}
	if len(answers) != 1 || answers[0] != "246810" {
		t.Fatalf("answers must map answer_0 back in order, got %v", answers)
	}

	req := s.requested
	if req == nil {
		t.Fatal("expected an elicitation request to be sent")
	}
	msg := req.Params.Message
	if !contains(msg, sshElicitorPromptTitle) || !contains(msg, `"bastion"`) {
		t.Fatalf("OTP message must carry the auth title and connection, got %q", msg)
	}
	if contains(msg, "Destructive command") || contains(msg, "Allow execution?") {
		t.Fatalf("OTP message must never reuse approval wording, got %q", msg)
	}

	// Schema: one required string per question, rendered as a secret because
	// the server asked for no echo.
	schema := req.Params.RequestedSchema.(map[string]any)
	props, _ := schema["properties"].(map[string]any)
	field, ok := props["answer_0"].(map[string]any)
	if !ok {
		t.Fatalf("expected answer_0 property, got %v", req.Params.RequestedSchema)
	}
	if field["format"] != "password" {
		t.Fatalf("non-echo question must render as a secret field, got %v", field)
	}
	required, _ := schema["required"].([]string)
	if len(required) != 1 || required[0] != "answer_0" {
		t.Fatalf("answer_0 must be required, got %v", schema["required"])
	}

	// Echoing questions are ordinary text fields.
	s2 := &mockApprovalSession{sessionID: "s2", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"answer_0": "tok", "answer_1": "sec"},
		},
	}}
	if _, err := e.Prompt(ctxWithSession(s2), manager.Challenge{
		Connection: "bastion",
		Questions:  []string{"Token:", "Passcode:"},
		Echos:      []bool{true, false},
	}); err != nil {
		t.Fatalf("echoed prompt should succeed: %v", err)
	}
	props2, _ := s2.requested.Params.RequestedSchema.(map[string]any)["properties"].(map[string]any)
	if f := props2["answer_0"].(map[string]any); f["format"] == "password" {
		t.Fatal("echoed question must not be a secret field")
	}
	if f := props2["answer_1"].(map[string]any); f["format"] != "password" {
		t.Fatal("non-echo question must be a secret field")
	}
}

func TestElicitorNonAcceptVerbs(t *testing.T) {
	cases := []struct {
		name   string
		result *mcp.ElicitationResult
		verb   string
	}{
		{"decline", &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionDecline}}, "declined"},
		{"cancel", &mcp.ElicitationResult{ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionCancel}}, "cancelled"},
		{"nil result", nil, "did not answer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := &mockApprovalSession{sessionID: "s", result: tc.result}
			e := sshElicitor{srv: server.NewMCPServer("test", "1.0.0", server.WithElicitation())}
			_, err := e.Prompt(ctxWithSession(s), manager.Challenge{Connection: "c", Questions: []string{"Code:"}})
			if err == nil || !contains(err.Error(), tc.verb) {
				t.Fatalf("expected error mentioning %q, got %v", tc.verb, err)
			}
			if err == nil || !contains(err.Error(), "SSH authentication") {
				t.Fatalf("error must stay distinguishable from approval, got %v", err)
			}
		})
	}
}

// Transport errors propagate unchanged: the manager's fail-open contract
// turns them into the SSH_MCP_2FA_CODE fallback (asserted in the manager
// package); the elicitor itself must not swallow or rewrite them.
func TestElicitorTransportErrorPropagates(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s", err: context.DeadlineExceeded}
	e := sshElicitor{srv: server.NewMCPServer("test", "1.0.0", server.WithElicitation())}
	_, err := e.Prompt(ctxWithSession(s), manager.Challenge{Connection: "c", Questions: []string{"Code:"}})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport error must propagate, got %v", err)
	}
}

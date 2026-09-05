package tools

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	"2native-ssh-mcp/internal/config"
)

// mockApprovalSession implements server.SessionWithElicitation with a canned
// response, mirroring mcp-go's own elicitation test mocks.
type mockApprovalSession struct {
	sessionID string
	result    *mcp.ElicitationResult
	err       error
	requested *mcp.ElicitationRequest
}

func (s *mockApprovalSession) Initialize()       {}
func (s *mockApprovalSession) Initialized() bool { return true }
func (s *mockApprovalSession) NotificationChannel() chan<- mcp.JSONRPCNotification {
	return make(chan mcp.JSONRPCNotification, 1)
}
func (s *mockApprovalSession) SessionID() string { return s.sessionID }
func (s *mockApprovalSession) RequestElicitation(_ context.Context, request mcp.ElicitationRequest) (*mcp.ElicitationResult, error) {
	s.requested = &request
	return s.result, s.err
}

func ctxWithSession(session server.ClientSession) context.Context {
	srv := server.NewMCPServer("test", "1.0.0", server.WithElicitation())
	return srv.WithContext(context.Background(), session)
}

func askConfig() *config.SSHConfig {
	return &config.SSHConfig{
		Name:         "prod",
		Host:         "h",
		Username:     "u",
		Port:         22,
		ApprovalMode: config.ApprovalModeAskDestructive,
	}
}

func TestGateAutoModeNeverAsks(t *testing.T) {
	// No session in the context: if the gate tried to elicit, it would
	// produce a fail-open note. auto mode must stay completely silent.
	proceed, note, decline := approvalGate(context.Background(), nil, &config.SSHConfig{Name: "dev", ApprovalMode: config.ApprovalModeAuto}, "rm -rf /")
	if !proceed || note != "" || decline != nil {
		t.Fatalf("auto mode must pass through untouched, got proceed=%v note=%q decline=%v", proceed, note, decline)
	}
}

func TestGateSafeCommandNeverAsks(t *testing.T) {
	proceed, note, decline := approvalGate(context.Background(), nil, askConfig(), "ls -la /var/log")
	if !proceed || note != "" || decline != nil {
		t.Fatalf("safe command must pass through, got proceed=%v note=%q decline=%v", proceed, note, decline)
	}
}

func TestGateFailOpenWithoutSession(t *testing.T) {
	// No elicitation-capable session: command still runs, note appended.
	proceed, note, decline := approvalGate(context.Background(), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if !proceed || decline != nil {
		t.Fatalf("fail-open must proceed without a decline result, got proceed=%v decline=%v", proceed, decline)
	}
	if note == "" || !contains(note, "does not support MCP elicitation") || !contains(note, `"prod"`) {
		t.Fatalf("note must name the connection and the reason, got %q", note)
	}
}

func TestGateFailOpenOnTransportError(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", err: context.DeadlineExceeded}
	proceed, note, decline := approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if !proceed || decline != nil {
		t.Fatalf("transport error must fail open, got proceed=%v decline=%v", proceed, decline)
	}
	if !contains(note, "elicitation failed") {
		t.Fatalf("note must mention the elicitation failure, got %q", note)
	}
	if s.requested == nil {
		t.Fatal("expected an elicitation request to be sent")
	}
}

func TestGateDeclineBlocks(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionDecline},
	}}
	proceed, note, decline := approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if proceed || note != "" || decline == nil {
		t.Fatalf("decline must block with a decline result, got proceed=%v note=%q decline=%v", proceed, note, decline)
	}
	text := resultText(t, decline)
	if !contains(text, `"executed": false`) {
		t.Fatalf("decline result must report executed=false, got %s", text)
	}
}

func TestGateCancelBlocks(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionCancel},
	}}
	proceed, _, decline := approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if proceed || decline == nil {
		t.Fatalf("cancel must block, got proceed=%v decline=%v", proceed, decline)
	}
}

func TestGateAcceptProceeds(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept},
	}}
	proceed, note, decline := approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if !proceed || note != "" || decline != nil {
		t.Fatalf("accept must proceed cleanly, got proceed=%v note=%q decline=%v", proceed, note, decline)
	}
}

func TestGateAcceptWithConfirmFalseBlocks(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{
			Action:  mcp.ElicitationResponseActionAccept,
			Content: map[string]any{"confirm": false},
		},
	}}
	proceed, _, decline := approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	if proceed || decline == nil {
		t.Fatalf("accept with confirm=false must block, got proceed=%v decline=%v", proceed, decline)
	}
}

func TestGateElicitationMessageCarriesCommandAndReason(t *testing.T) {
	s := &mockApprovalSession{sessionID: "s1", result: &mcp.ElicitationResult{
		ElicitationResponse: mcp.ElicitationResponse{Action: mcp.ElicitationResponseActionAccept},
	}}
	_, _, _ = approvalGate(ctxWithSession(s), server.NewMCPServer("test", "1.0.0", server.WithElicitation()), askConfig(), "rm -rf /tmp/build")
	msg := s.requested.Params.Message
	if !contains(msg, "prod") || !contains(msg, "rm -rf /tmp/build") || !contains(msg, "deletes files") {
		t.Fatalf("elicitation message must carry connection, command and reason, got %q", msg)
	}
}

func TestAppendNote(t *testing.T) {
	res := mcp.NewToolResultText("output")
	got := appendNote(res, "note body")
	if text := resultText(t, got); !contains(text, "output") || !contains(text, "note body") {
		t.Fatalf("note must be appended to the result text, got %q", text)
	}
	if same := appendNote(mcp.NewToolResultText("x"), ""); same == nil {
		t.Fatal("empty note must return the original result")
	}
}

func contains(s, sub string) bool {
	return strings.Contains(s, sub)
}

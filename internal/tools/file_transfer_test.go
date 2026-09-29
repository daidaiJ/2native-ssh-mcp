package tools

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"

	"2native-ssh-mcp/internal/manager"
)

func TestElapsedSecondsAndFormat(t *testing.T) {
	if got := elapsedSeconds(24 * time.Millisecond); got != 0.024 {
		t.Fatalf("elapsedSeconds(24ms) = %v, want 0.024", got)
	}
	cases := map[float64]string{
		0.024:  "0.02s",
		1.234:  "1.23s",
		12.34:  "12.3s",
		123.45: "123s",
	}
	for in, want := range cases {
		if got := formatSeconds(in); got != want {
			t.Fatalf("formatSeconds(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestTransferResultJSONUnitAndCounts(t *testing.T) {
	res := &manager.TransferResult{
		Action: "upload", Files: 3, SkippedFiles: 3,
		Elapsed: 24 * time.Millisecond, SpeedBps: 1929.3764339251486,
	}
	out := transferResultJSON(res)
	text := out.Content[0].(mcp.TextContent).Text
	if !strings.Contains(text, "0.02s") {
		t.Fatalf("prose must carry seconds: %s", text)
	}
	payload := text[strings.Index(text, "\n\nJSON:\n")+len("\n\nJSON:\n"):]
	var parsed struct {
		ElapsedS     float64 `json:"elapsedS"`
		SpeedBps     float64 `json:"speedBps"`
		SkippedFiles int     `json:"skippedFiles"`
	}
	if err := json.Unmarshal([]byte(payload), &parsed); err != nil {
		t.Fatalf("JSON payload unmarshal failed: %v", err)
	}
	if parsed.ElapsedS != 0.024 {
		t.Fatalf("elapsedS = %v, want 0.024", parsed.ElapsedS)
	}
	if parsed.SpeedBps != 1929 {
		t.Fatalf("speedBps must be rounded, got %v", parsed.SpeedBps)
	}
	if parsed.SkippedFiles != 3 {
		t.Fatalf("skippedFiles = %v, want 3", parsed.SkippedFiles)
	}
}

// A default-routed transfer (connectionName omitted) must surface which
// connection and host received the files.
func TestTransferResultJSONDefaultConnectionSummary(t *testing.T) {
	res := &manager.TransferResult{
		Action: "upload", Bytes: 10,
		Elapsed: 24 * time.Millisecond, SpeedBps: 400,
		Connection: "default", Host: "203.0.113.5", Port: 22,
		Aliases: []string{"web"},
	}
	text := transferResultJSON(res).Content[0].(mcp.TextContent).Text
	want := "connection=default host=203.0.113.5 port=22 aliases=web"
	if !strings.HasPrefix(text, want+"\n\n") {
		t.Fatalf("transfer result must start with the default-connection summary:\n%s", text)
	}

	plain := &manager.TransferResult{Action: "upload", Bytes: 10}
	if got := transferResultJSON(plain).Content[0].(mcp.TextContent).Text; strings.Contains(got, "connection=default") {
		t.Fatalf("explicit-connection transfer must not carry the summary:\n%s", got)
	}
}

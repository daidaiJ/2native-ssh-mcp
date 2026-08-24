package manager

import "testing"

func TestParseBGReadOutput(t *testing.T) {
	running, total, chunk := parseBGReadOutput("__MCP_BG_HDR__running=1 size=42\nhello")
	if !running || total != 42 || chunk != "hello" {
		t.Fatalf("parseBGReadOutput: running=%v total=%d chunk=%q", running, total, chunk)
	}
}

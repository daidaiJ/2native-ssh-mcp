package manager

import (
	"strings"
	"testing"
)

func TestCapabilityProbesAreReadableCommands(t *testing.T) {
	for field, cmd := range capabilityProbes {
		if strings.Contains(cmd, "--obfuscate") || strings.Contains(cmd, "eval ") {
			t.Fatalf("%s probe must stay a readable command: %s", field, cmd)
		}
		if !strings.Contains(cmd, "printf") {
			t.Fatalf("%s probe must feed the utility through printf: %s", field, cmd)
		}
	}
	// The base64 payload must keep its trailing newline: openssl's decoder
	// emits nothing (exit code 0) without it.
	if !strings.Contains(capabilityProbes["base64"], `printf 'YQ==\n'`) {
		t.Fatalf("base64 probe payload must end in a newline: %s", capabilityProbes["base64"])
	}
	if !strings.Contains(capabilityProbes["base64"], "openssl base64 -d") {
		t.Fatalf("base64 probe must fall back to openssl: %s", capabilityProbes["base64"])
	}
}

func TestCapabilityOutputParsing(t *testing.T) {
	marker := "__MCP_CAP_test_"
	output := "preamble\n" + marker + "base64\na\n" + marker + "jq\n{}\n"
	caps := &RemoteCapabilities{}
	caps.applyOutput(parseStatusOutput(output, marker))
	if !caps.Base64 || !caps.JQ {
		t.Fatalf("both capabilities must be detected, got %+v", caps)
	}
}

func TestCapabilityOutputRequiresByteExactMatch(t *testing.T) {
	marker := "__MCP_CAP_test_"
	// openssl base64 -d with no trailing newline in the input prints nothing
	// and still exits 0; jq on a syntax error prints its error on stderr.
	output := marker + "base64\n\n" + marker + "jq\njq: parse error\n"
	caps := &RemoteCapabilities{}
	caps.applyOutput(parseStatusOutput(output, marker))
	if caps.Base64 || caps.JQ {
		t.Fatalf("empty or erroring output must not count as available, got %+v", caps)
	}
}

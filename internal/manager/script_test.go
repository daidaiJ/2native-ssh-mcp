package manager

import (
	"fmt"
	"strings"
	"testing"
)

func TestScriptCommandWithoutShebangUsesInterpreter(t *testing.T) {
	content := []byte("echo hi\n")
	got := ScriptCommand("/tmp/.2native-ssh-mcp-x.sh", content, "sh", false)
	if got != "'sh' '/tmp/.2native-ssh-mcp-x.sh'" {
		t.Fatalf("staged script should run through the quoted interpreter, got: %s", got)
	}
}

func TestScriptCommandWithShebangRunsDirectly(t *testing.T) {
	content := []byte("#!/usr/bin/env python3\nprint('hi')\n")
	got := ScriptCommand("/tmp/.2native-ssh-mcp-x.sh", content, "sh", false)
	if got != "'/tmp/.2native-ssh-mcp-x.sh'" {
		t.Fatalf("a script with a shebang should run directly, got: %s", got)
	}
}

func TestScriptCommandCleanupPreservesExitCode(t *testing.T) {
	got := ScriptCommand("/tmp/x.sh", []byte("exit 7\n"), "sh", true)
	for _, want := range []string{"rm -f '/tmp/x.sh'", "__mcp_script_rc=$?", "(exit $__mcp_script_rc)"} {
		if !strings.Contains(got, want) {
			t.Fatalf("cleanup command missing %q:\n%s", want, got)
		}
	}
	// A bare `exit` would end the background starter's shell before it can
	// write the exit file, so the status must travel through a subshell.
	if strings.Contains(strings.ReplaceAll(got, "(exit $__mcp_script_rc)", ""), "exit ") {
		t.Fatalf("cleanup must not contain a bare exit:\n%s", got)
	}
}

func TestScriptAuditTextCarriesTheScriptItself(t *testing.T) {
	content := []byte("echo 'hi'\nrm -rf /tmp/junk\n")
	got := ScriptAuditText("/local/deploy.sh", "/tmp/.2native-ssh-mcp-deploy-script_ab.sh", "sh", content, true)
	if !strings.Contains(got, "scriptFile /local/deploy.sh staged to /tmp/.2native-ssh-mcp-deploy-script_ab.sh, run by sh") {
		t.Fatalf("audit header must name the source, the staged path and the runner:\n%s", got)
	}
	if !strings.Contains(got, "rm -rf /tmp/junk") {
		t.Fatalf("audit text must contain the commands that run remotely:\n%s", got)
	}
	if strings.Contains(got, "base64") {
		t.Fatalf("audit text must not encode the script:\n%s", got)
	}
}

func TestScriptAuditTextStatesTheCleanup(t *testing.T) {
	content := []byte("echo hi\n")
	kept := ScriptAuditText("/local/a.sh", "/tmp/a.sh", "sh", content, false)
	if !strings.Contains(kept, "left in place (scriptCleanup=false)") {
		t.Fatalf("the audit header must say the staged file stays:\n%s", kept)
	}
	removed := ScriptAuditText("/local/a.sh", "/tmp/a.sh", "sh", content, true)
	if !strings.Contains(removed, "removed afterwards") {
		t.Fatalf("the audit header must say the staged file is removed:\n%s", removed)
	}
}

func TestScriptAuditTextTruncatesLongScripts(t *testing.T) {
	content := []byte(strings.Repeat("echo x\n", 5000))
	got := ScriptAuditText("/local/big.sh", "/tmp/big.sh", "sh", content, true)
	if len(got) > scriptAuditMaxBytes+512 {
		t.Fatalf("audit copy must be capped, got %d bytes", len(got))
	}
	if !strings.Contains(got, "audit copy truncated") {
		t.Fatalf("a truncated audit copy must say so:\n%s", got[len(got)-200:])
	}
	if !strings.Contains(got, fmt.Sprintf("%d bytes in total", len(content))) {
		t.Fatalf("the truncation note must state the real size:\n%s", got[len(got)-200:])
	}
}

func TestScriptTempPathIsUnpredictableAndLegible(t *testing.T) {
	a := scriptTempPath("D:\\pro\\deploy release.sh")
	b := scriptTempPath("D:\\pro\\deploy release.sh")
	if a == b {
		t.Fatalf("two stagings must not share a path: %s", a)
	}
	if !strings.HasPrefix(a, scriptTempPrefix+"deploy_release.sh-script_") || !strings.HasSuffix(a, ".sh") {
		t.Fatalf("unexpected temp path: %s", a)
	}
	if strings.ContainsAny(a, " \\:") {
		t.Fatalf("temp path must be a clean POSIX path: %s", a)
	}
}

func TestQuoteInterpreterSplitsAndQuotesFields(t *testing.T) {
	got := ScriptCommand("/tmp/x.sh", []byte("echo hi\n"), "python3 -u", false)
	if got != "'python3' '-u' '/tmp/x.sh'" {
		t.Fatalf("interpreter flags must be quoted per field, got: %s", got)
	}
	got = ScriptCommand("/tmp/x.sh", []byte("echo hi\n"), "sh;rm", false)
	if got != "'sh;rm' '/tmp/x.sh'" {
		t.Fatalf("metacharacters in the interpreter must stay inside quotes, got: %s", got)
	}
}

func TestNormalizeScriptContentStripsBOMAndCRLF(t *testing.T) {
	raw := append(append([]byte{}, utf8BOM...), []byte("#!/bin/sh\r\necho hi\r\n")...)
	got := normalizeScriptContent(raw)
	if !scriptHasShebang(got) {
		t.Fatalf("a Windows shebang must still be recognized after normalize, got %q", got)
	}
	if strings.Contains(string(got), "\r") {
		t.Fatalf("CRLF must become LF, got %q", got)
	}
	if ScriptCommand("/tmp/x.sh", got, "sh", false) != "'/tmp/x.sh'" {
		t.Fatalf("normalized shebang script must run directly, got %s", ScriptCommand("/tmp/x.sh", got, "sh", false))
	}
}

func TestPrepareScriptDoesNotUpload(t *testing.T) {
	content := []byte("echo hi\n")
	exec, audit, run := PrepareScript("/local/deploy.sh", content, "sh", true)
	if run.RemotePath == "" || !strings.HasPrefix(run.RemotePath, scriptTempPrefix) {
		t.Fatalf("PrepareScript must pick a remote path, got %s", run.RemotePath)
	}
	if !strings.Contains(exec, run.RemotePath) {
		t.Fatalf("exec command must name the same path, got %s", exec)
	}
	if !strings.Contains(audit, "echo hi") || !strings.Contains(audit, run.RemotePath) {
		t.Fatalf("audit text must name the path and carry the body:\n%s", audit)
	}
}

func TestSanitizeScriptName(t *testing.T) {
	cases := map[string]string{
		"deploy.sh":                     "deploy.sh",
		"../etc/passwd":                 "passwd",
		"...":                           "script",
		"a b\\c$d.sh":                   "c_d.sh",
		strings.Repeat("x", 80) + ".sh": strings.Repeat("x", 40),
	}
	for in, want := range cases {
		if got := sanitizeScriptName(in); got != want {
			t.Fatalf("sanitizeScriptName(%q) = %q, want %q", in, got, want)
		}
	}
}

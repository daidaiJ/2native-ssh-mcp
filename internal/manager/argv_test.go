package manager

import (
	"strings"
	"testing"
)

func TestJoinArgvQuotesEveryArgumentForExecution(t *testing.T) {
	exec, audit, err := JoinArgv("printf", []string{"<%s>\\n", "a b", "it's", "$HOME", "back\\slash", "*"})
	if err != nil {
		t.Fatalf("JoinArgv: %v", err)
	}
	want := `'printf' '<%s>\n' 'a b' 'it'\''s' '$HOME' 'back\slash' '*'`
	if exec != want {
		t.Fatalf("exec text must quote every argument:\n got: %s\nwant: %s", exec, want)
	}
	// The readable form keeps only the quoting a shell needs, so the audit
	// line still looks like the command the caller meant.
	if !strings.Contains(audit, `'a b'`) || !strings.Contains(audit, "$HOME") {
		t.Fatalf("audit text should quote only what needs quoting, got: %s", audit)
	}
	if strings.Contains(audit, `'printf'`) {
		t.Fatalf("audit text should not quote a plain program name, got: %s", audit)
	}
}

func TestJoinArgvRejectsNullBytes(t *testing.T) {
	if _, _, err := JoinArgv("echo", []string{"a\x00b"}); err == nil {
		t.Fatal("a null byte in args must be rejected")
	}
	if _, _, err := JoinArgv("ec\x00ho", nil); err == nil {
		t.Fatal("a null byte in the program must be rejected")
	}
}

func TestShellQuoteMinimal(t *testing.T) {
	plain := []string{"grep", "-rn", "/etc/hosts", "a=b", "50%", "x,y"}
	for _, s := range plain {
		if got := shellQuoteMinimal(s); got != s {
			t.Fatalf("shellQuoteMinimal(%q) = %q, want it left alone", s, got)
		}
	}
	quoted := []string{"", "a b", "it's", "$HOME", "*", "~/x", "a;b", "a|b", ">out"}
	for _, s := range quoted {
		if got := shellQuoteMinimal(s); got == s {
			t.Fatalf("shellQuoteMinimal(%q) left a shell-active argument unquoted", s)
		}
	}
}

func TestAuditTextForFallsBackToExecutedCommand(t *testing.T) {
	if got := auditTextFor("echo hi", ""); got != "echo hi" {
		t.Fatalf("empty equivalent must fall back to the executed text, got %q", got)
	}
	if got := auditTextFor("echo 'hi'", "echo hi"); got != "echo hi" {
		t.Fatalf("equivalent must win, got %q", got)
	}
	if got := auditTextFor("echo 'hi'", "   "); got != "echo 'hi'" {
		t.Fatalf("blank equivalent must fall back, got %q", got)
	}
}

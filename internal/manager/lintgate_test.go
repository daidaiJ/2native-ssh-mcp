package manager

import (
	"strings"
	"testing"

	"2native-ssh-mcp/internal/config"
)

func TestLintModeForResolution(t *testing.T) {
	strict := &config.SSHConfig{LintMode: config.LintStrict}
	if got := LintModeFor(strict, ""); got != config.LintStrict {
		t.Fatalf("connection lintMode must apply, got %q", got)
	}
	if got := LintModeFor(strict, config.LintOff); got != config.LintOff {
		t.Fatalf("per-call override must win, got %q", got)
	}
	if got := LintModeFor(strict, " STRICT "); got != config.LintStrict {
		t.Fatalf("override must be trimmed and case-insensitive, got %q", got)
	}
	if got := LintModeFor(&config.SSHConfig{}, ""); got != config.LintWarn {
		t.Fatalf("default must be warn, got %q", got)
	}
	if got := LintModeFor(strict, "nonsense"); got != config.LintStrict {
		t.Fatalf("an unknown override must fall back to the connection, got %q", got)
	}
}

func TestCheckCommandLintOffSkipsEverything(t *testing.T) {
	m := &Manager{}
	findings, err := m.checkCommandLint(`echo 'unbalanced`, config.LintOff)
	if err != nil || findings != nil {
		t.Fatalf("lintMode=off must not run the lint: findings=%v err=%v", findings, err)
	}
}

func TestCheckCommandLintWarnReportsAndRuns(t *testing.T) {
	m := &Manager{}
	findings, err := m.checkCommandLint(`echo 'unbalanced`, config.LintWarn)
	if err != nil {
		t.Fatalf("warn must not block: %v", err)
	}
	if len(findings) != 1 || !strings.Contains(findings[0], CodeLintUnbalancedQuote) {
		t.Fatalf("warn must report the finding, got %v", findings)
	}
}

func TestCheckCommandLintStrictRefusesErrors(t *testing.T) {
	m := &Manager{}
	_, err := m.checkCommandLint(`echo 'unbalanced`, config.LintStrict)
	if err == nil {
		t.Fatal("strict must refuse a command with an error-class finding")
	}
	if code := AsToolError(err).Code; code != CodeCommandLintFailed {
		t.Fatalf("strict refusal must use %s, got %s", CodeCommandLintFailed, code)
	}
	if !strings.Contains(err.Error(), CodeLintUnbalancedQuote) {
		t.Fatalf("refusal must name the finding: %v", err)
	}
}

func TestCheckCommandLintStrictRunsWarnClassFindings(t *testing.T) {
	m := &Manager{}
	// A nested positional is warn-class: the command runs, with the finding
	// reported, even under strict.
	cmd := `bash -c "printf '%s\n' $1"`
	findings, err := m.checkCommandLint(cmd, config.LintStrict)
	if err != nil {
		t.Fatalf("strict must only refuse error-class findings: %v", err)
	}
	if len(findings) == 0 {
		t.Fatal("warn-class findings must still be reported under strict")
	}
}

func TestCheckCommandLintCleanCommandIsSilent(t *testing.T) {
	m := &Manager{}
	for _, cmd := range []string{`ls -la /tmp`, `echo "$(printf '%s' x)"`, `grep -c foo file || true`} {
		findings, err := m.checkCommandLint(cmd, config.LintStrict)
		if err != nil || len(findings) != 0 {
			t.Fatalf("clean command %q must pass silently: findings=%v err=%v", cmd, findings, err)
		}
	}
}

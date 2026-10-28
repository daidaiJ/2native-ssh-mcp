package manager

import (
	"strings"
	"testing"
)

// TestLintShellCorpus pins the linter against a corpus of commands that are
// valid in practice. The contract that matters is the false-positive rate:
// every "clean" entry must produce no error finding, and the entries marked
// wantPos must produce exactly the listed warnings.
func TestLintShellCorpus(t *testing.T) {
	type tc struct {
		name     string
		cmd      string
		wantErr  string // expected error code, "" for none
		wantWarn string // expected warn code, "" for none
		note     string
	}

	cases := []tc{
		// --- must stay clean: ordinary commands ---
		{"plain", `ls -la /tmp`, "", "", "plain command"},
		{"pipe", `cat /var/log/app.log | grep ERROR | head -20`, "", "", "pipeline"},
		{"quoted-glob", `find /var/log -name '*.log' 2>/dev/null | head -50`, "", "", "quoted glob"},
		{"apostrophe-in-double", `echo "it's fine"`, "", "", "apostrophe inside double quotes"},
		{"awk-program", `awk '{print $1, $2}' /etc/passwd`, "", "", "awk program in single quotes"},
		{"sed-range", `sed -n '1,10p' file.txt`, "", "", "sed range"},
		{"printf-args", `printf '%s\n' "$@"`, "", "", "printf with $@"},
		{"comment", `echo hi # a comment with ' and "`, "", "", "trailing comment"},
		{"backticks", "echo `date`", "", "", "backtick substitution"},
		{"dollar-paren", `echo "$(date +%Y)"`, "", "", "command substitution in double quotes"},
		{"nested-quote-trap", `echo "$(printf "%s" x)"`, "", "", "quotes inside $() inside \"\" — FP trap"},
		{"case-trap", `case $x in a) echo 1;; b) echo 2;; esac`, "", "", "case patterns — FP trap"},
		{"ansi-c-quote", `x=$'it\'s'; echo "$x"`, "", "", "$'…' with escaped quote"},
		{"brace-expansion", `echo "${VAR}" ${OTHER}`, "", "", "parameter expansion"},
		{"heredoc-odd-quote", "cat <<'EOF'\nit's\nEOF", "", "", "heredoc body with odd quote — FP trap"},
		{"heredoc-data", "cat <<EOF\n$1 and \"quotes\"\nEOF", "", "", "unquoted heredoc body is data"},
		{"heredoc-tabs", "cat <<-EOF\n\ttabbed\n\tEOF", "", "", "<<- strips leading tabs"},
		{"heredoc-spaced-opener", "cat << EOF\nbash -c \"echo $1\"\nEOF", "", "", "spaced heredoc opener: the body is data"},
{"heredoc-spaced-single-char", "cat << X\nbash -c \"echo $1\"\nX", "", "", "spaced opener with a one-char delimiter — the case the pre-fix build flagged"},
{"heredoc-spaced-two-spaces", "cat <<  XY\nbash -c \"echo $1\"\nXY", "", "", "spaced opener where the skipped whitespace matches the delimiter length"},
		{"trailing-backslash", `echo hi \`, "", "", "trailing backslash is literal, not an error (verified)"},
		{"base64-wrapper", `echo 'ZWNobyBh' | base64 -d | bash`, "", "", "base64 wrapper"},
		{"quote-dance", `bash -c 'echo "user=$USER" | awk '"'"'{print $1}'"'"''`, "", "", "nested shell, single-quoted"},
		{"nested-home", `bash -c "echo $HOME"`, "", "", "$HOME in -c is a deliberate outer expansion"},
		{"nested-status", `bash -c "test -f x && echo $?"`, "", "", "$? in -c is a deliberate outer expansion"},
		{"top-level-positional", `echo "arg is $1"`, "", "", "positional at top level is fine"},
		{"docker-exec-safe", `docker exec -i c sh -c 'tail -n 5 /log'`, "", "", "nested shell, single-quoted"},

		// --- must warn: positional parameter eaten by the outer shell ---
		{"nested-positional-1", `bash -c "echo \"user=$USER\" | awk '{print $1}'"`, "", CodeLintNestedPositional, "measured silent failure"},
		{"nested-positional-lc", `bash -lc "echo $1"`, "", CodeLintNestedPositional, "clustered -lc still hands the next arg to a nested shell"},
		{"nested-positional-su", `su root -c "cd -- '/d' && ls $1"`, "", CodeLintNestedPositional, "su -c form"},
		{"nested-positional-ssh", `ssh host "echo $1"`, "", CodeLintNestedPositional, "ssh host \"…\" form"},
		{"nested-positional-at", `sh -c "printf '%s' '$@'"`, "", CodeLintNestedPositional, "$@ form"},
		{"nested-positional-docker", `docker exec -i c sh -c "tail -n $1 /log"`, "", CodeLintNestedPositional, "docker exec … sh -c form"},

		// --- must error: unbalanced constructs ---
		{"unbalanced-single", `echo 'unbalanced`, CodeLintUnbalancedQuote, "", "unterminated single quote"},
		{"unbalanced-double", `echo "unbalanced`, CodeLintUnbalancedQuote, "", "unterminated double quote"},
		{"unbalanced-subst", `echo "a$(b"`, CodeLintUnbalancedQuote, "", "unterminated $() inside double quotes"},
		{"unbalanced-backtick", "echo `foo", CodeLintUnbalancedBacktick, "", "unterminated backtick"},
		{"unterminated-heredoc", "cat <<EOF\nhello", CodeLintUnterminatedHeredoc, "", "heredoc without delimiter"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := LintShell(c.cmd)
			var gotErr, gotWarn string
			for _, f := range got {
				switch f.Kind {
				case "error":
					if gotErr == "" {
						gotErr = f.Code
					}
				case "warn":
					if gotWarn == "" {
						gotWarn = f.Code
					}
				}
			}
			if gotErr != c.wantErr {
				t.Errorf("error finding = %q, want %q\n  cmd: %s\n  all: %s", gotErr, c.wantErr, c.cmd, FormatLint(got))
			}
			if gotWarn != c.wantWarn {
				t.Errorf("warn finding = %q, want %q\n  cmd: %s\n  all: %s", gotWarn, c.wantWarn, c.cmd, FormatLint(got))
			}
		})
	}
}

// TestLintNoFalsePositivesOnCleanCorpus asserts the headline property: none of
// the valid commands produces an error-class finding. Warnings are allowed
// here because warn-class findings never block execution.
func TestLintNoFalsePositivesOnCleanCorpus(t *testing.T) {
	clean := []string{
		`ls -la /tmp`,
		`cat /var/log/app.log | grep ERROR | head -20`,
		`find /var/log -name '*.log' 2>/dev/null | head -50`,
		`echo "it's fine"`,
		`awk '{print $1, $2}' /etc/passwd`,
		`sed -n '1,10p' file.txt`,
		`printf '%s\n' "$@"`,
		"echo `date`",
		`echo "$(date +%Y)"`,
		`echo "$(printf "%s" x)"`,
		`case $x in a) echo 1;; b) echo 2;; esac`,
		`x=$'it\'s'; echo "$x"`,
		`echo "${VAR}" ${OTHER}`,
		"cat <<'EOF'\nit's\nEOF",
		"cat <<EOF\n$1 and \"quotes\"\nEOF",
		"cat <<-EOF\n\ttabbed\n\tEOF",
		`echo hi \`,
		`echo 'ZWNobyBh' | base64 -d | bash`,
		`bash -c 'echo "user=$USER" | awk '"'"'{print $1}'"'"''`,
		`bash -c "echo $HOME"`,
		`bash -c "test -f x && echo $?"`,
		`docker exec -i c sh -c 'tail -n 5 /log'`,
		`for f in *.txt; do echo "$f"; done`,
		`if [ -f /tmp/x ]; then cat /tmp/x; fi`,
		`curl -s -o /tmp/o -w '%{http_code}' http://127.0.0.1:8080/health`,
		`jq -c -n --rawfile v /tmp/nasty.txt '{msg:$v}'`,
		`sh /tmp/argv.sh 'a b' 'it'\''s' '$HOME' 'back\slash' '*'`,
		`tar -xzf /tmp/a.tgz -C /opt && ls /opt`,
		`echo a && echo b || echo c`,
		`grep -v '^#' /etc/fstab | grep -v '^$'`,
		`systemctl is-active nginx && echo UP`,
		`awk -v n="$1" 'BEGIN{print n}' /dev/null`,
	}
	for _, cmd := range clean {
		if errs := LintErrors(LintShell(cmd)); len(errs) > 0 {
			t.Errorf("false positive on a valid command:\n  cmd: %s\n  %s", cmd, FormatLint(errs))
		}
	}
}

// TestLintNestedPositionalPositions checks the reported offset points at the
// parameter itself, so an agent can see exactly which token is at risk.
func TestLintNestedPositionalPositions(t *testing.T) {
	cmd := `bash -c "echo $1"`
	got := LintShell(cmd)
	if len(got) == 0 {
		t.Fatal("expected a finding")
	}
	if cmd[got[0].Pos:got[0].Pos+2] != "$1" {
		t.Fatalf("offset %d does not point at $1 (found %q)", got[0].Pos, cmd[got[0].Pos:])
	}
}

// TestLintStrictOnlyBlocksErrors guards the contract strict mode relies on:
// warnings are reported but never returned by LintErrors.
func TestLintStrictOnlyBlocksErrors(t *testing.T) {
	got := LintShell(`bash -c "echo $1"`)
	if len(LintErrors(got)) != 0 {
		t.Fatalf("warn-class finding must not block, got %v", LintErrors(got))
	}
	if !strings.Contains(FormatLint(got), CodeLintNestedPositional) {
		t.Fatalf("warning must still be reported, got %q", FormatLint(got))
	}
}

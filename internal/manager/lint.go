package manager

import (
	"fmt"
	"strings"
)

// LintFinding is one structural problem LintShell found in a command string.
// Kind is "error" (the command cannot work as written) or "warn" (it will run,
// but probably not as intended).
type LintFinding struct {
	Kind string
	Code string
	Pos  int
	Msg  string
}

// Lint finding codes.
const (
	CodeLintUnbalancedQuote     = "UNBALANCED_QUOTE"
	CodeLintUnbalancedBacktick  = "UNBALANCED_BACKTICK"
	CodeLintUnterminatedHeredoc = "UNTERMINATED_HEREDOC"
	CodeLintNestedPositional    = "NESTED_SHELL_POSITIONAL"
)

// LintShell runs cheap structural checks over a shell command string.
//
// It deliberately covers only what a shell parser cannot see. Everything a
// parser does see — unbalanced quotes, unterminated heredocs, `| |`, unclosed
// blocks — is left to the remote shell's own `-n` preflight, which is
// authoritative and has no false positives. The check here is the one `-n` is
// blind to: quoting that is valid for the outer shell but silently changes
// what an inner shell receives.
func LintShell(cmd string) []LintFinding {
	var out []LintFinding
	for _, t := range shellTokens(cmd) {
		if f, ok := lintNestedArg(t); ok {
			out = append(out, f)
		}
	}
	return append(out, lintQuotes(cmd)...)
}

// lintNestedArg reports a positional parameter inside a double-quoted script
// handed to another shell. The outer shell has no positional parameters, so
// `$1` becomes empty before the inner shell ever runs — the command still
// exits 0, with the wrong output.
func lintNestedArg(t shellToken) (LintFinding, bool) {
	if t.kind != tokQuote || t.quote != '"' || !t.nestedShellArg {
		return LintFinding{}, false
	}
	name, pos := firstPositional(t.text)
	if name == "" {
		return LintFinding{}, false
	}
	return LintFinding{
		Kind: "warn",
		Code: CodeLintNestedPositional,
		Pos:  t.contentPos + pos,
		Msg: fmt.Sprintf(
			"`%s` sits inside a double-quoted script handed to another shell, so the outer shell expands it to nothing before that shell runs; escape it as \\%s or single-quote the script",
			name, name),
	}, true
}

// firstPositional returns the first positional parameter ($1..$9, $@, $*, $#)
// in s and its offset, or "" when there is none. `$?` and `$$` are excluded:
// they are meaningful to the outer shell and routinely used there on purpose.
func firstPositional(s string) (string, int) {
	for i := 0; i+1 < len(s); i++ {
		if s[i] == '\\' {
			i++
			continue
		}
		if s[i] != '$' {
			continue
		}
		switch c := s[i+1]; {
		case c >= '0' && c <= '9':
			return s[i : i+2], i
		case c == '@' || c == '*' || c == '#':
			return s[i : i+2], i
		}
	}
	return "", 0
}

// --- quote balance -----------------------------------------------------

// scanFrame is one open quoting construct.
type scanFrame struct {
	kind  byte // '\'', '"', '$' (ansi-c), '`', '(' ($( ), '{' (${ )
	pos   int
	depth int // nested ( ) inside $( )
}

// heredocPending is a heredoc whose body has not been consumed yet.
type heredocPending struct {
	delim string
	tabs  bool
	pos   int
}

// quoteScanner walks a command string tracking which quoting construct is
// open. Heredoc bodies are skipped as data, and command substitutions inside
// double quotes are tracked, so neither is misread as unbalanced.
type quoteScanner struct {
	cmd       string
	out       []LintFinding
	stack     []scanFrame
	pending   []heredocPending
	i         int
	wordStart bool
}

func lintQuotes(cmd string) []LintFinding {
	s := &quoteScanner{cmd: cmd, wordStart: true}
	s.run()
	return s.out
}

func (s *quoteScanner) run() {
	for s.i < len(s.cmd) {
		if s.consumeHeredocBodies() {
			continue
		}
		if s.handleFramed() {
			continue
		}
		if s.handleShared() {
			continue
		}
		s.handlePlain()
	}
	s.reportUnclosed()
}

func (s *quoteScanner) top() byte {
	if len(s.stack) == 0 {
		return 0
	}
	return s.stack[len(s.stack)-1].kind
}

func (s *quoteScanner) push(kind byte, pos int) {
	s.stack = append(s.stack, scanFrame{kind: kind, pos: pos})
}

func (s *quoteScanner) pop() { s.stack = s.stack[:len(s.stack)-1] }

// consumeHeredocBodies swallows the body of every pending heredoc once the
// newline that ends the opener line is reached. The body is data: quotes and
// `$` inside it are not shell syntax.
func (s *quoteScanner) consumeHeredocBodies() bool {
	if len(s.pending) == 0 || s.cmd[s.i] != '\n' || s.top() != 0 {
		return false
	}
	s.i++
	for len(s.pending) > 0 {
		h := s.pending[0]
		if !s.skipToDelimiter(h) {
			s.out = append(s.out, LintFinding{"error", CodeLintUnterminatedHeredoc, h.pos,
				fmt.Sprintf("heredoc %q has no closing delimiter line", h.delim)})
		}
		s.pending = s.pending[1:]
	}
	return true
}

// skipToDelimiter advances past lines until one equals the delimiter.
func (s *quoteScanner) skipToDelimiter(h heredocPending) bool {
	for s.i <= len(s.cmd) {
		var line string
		if j := strings.IndexByte(s.cmd[s.i:], '\n'); j >= 0 {
			line, s.i = s.cmd[s.i:s.i+j], s.i+j+1
		} else {
			line, s.i = s.cmd[s.i:], len(s.cmd)+1
		}
		if h.tabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == h.delim {
			return true
		}
	}
	return false
}

// handleFramed handles the constructs that behave differently depending on
// what is already open (single quotes are literal inside double quotes, a
// `)` closes a substitution only at depth 0, and so on).
func (s *quoteScanner) handleFramed() bool {
	c := s.cmd[s.i]
	switch s.top() {
	case '\'':
		if c == '\'' {
			s.pop()
		}
		s.i++
		s.wordStart = false
		return true
	case '$': // $'…' — backslash escapes inside
		if c == '\\' {
			s.i += 2
			return true
		}
		if c == '\'' {
			s.pop()
		}
		s.i++
		return true
	case '`':
		if c == '\\' {
			s.i += 2
			return true
		}
		if c == '`' {
			s.pop()
			s.i++
			s.wordStart = false
			return true
		}
		// anything else inside `…` is ordinary text or a nested substitution
		return false
	case '"':
		return s.handleInsideDouble(c)
	case '(':
		return s.handleInsideSubst(c)
	case '{':
		if c == '}' {
			s.pop()
			s.i++
			return true
		}
	}
	return false
}

func (s *quoteScanner) handleInsideDouble(c byte) bool {
	switch {
	case c == '\\':
		s.i += 2
	case c == '"':
		s.pop()
		s.i++
		s.wordStart = false
	case c == '`':
		s.push('`', s.i)
		s.i++
		s.wordStart = false
	case c == '$' && s.peek(1) == '(':
		s.push('(', s.i)
		s.i += 2
		s.wordStart = false
	case c == '$' && s.peek(1) == '{':
		s.push('{', s.i)
		s.i += 2
		s.wordStart = false
	default:
		s.i++
	}
	return true
}

func (s *quoteScanner) handleInsideSubst(c byte) bool {
	switch c {
	case ')':
		if s.stack[len(s.stack)-1].depth == 0 {
			s.pop()
		} else {
			s.stack[len(s.stack)-1].depth--
		}
		s.i++
	case '(':
		s.stack[len(s.stack)-1].depth++
		s.i++
	default:
		return false
	}
	return true
}

// handleShared handles constructs that open the same way at every level:
// inside "…", inside `…`, inside $( ) and inside ${ }.
func (s *quoteScanner) handleShared() bool {
	c := s.cmd[s.i]
	switch {
	case c == '\\':
		s.i += 2
		s.wordStart = false
	case c == '\'':
		if s.top() == '"' {
			s.i++ // a single quote is literal inside double quotes
			return true
		}
		s.push('\'', s.i)
		s.i++
		s.wordStart = false
	case c == '"':
		s.push('"', s.i)
		s.i++
		s.wordStart = false
	case c == '`':
		s.push('`', s.i)
		s.i++
		s.wordStart = false
	case c == '$' && s.peek(1) == '\'':
		if s.top() == '"' {
			s.i++ // $'…' is not special inside double quotes
			return true
		}
		s.push('$', s.i)
		s.i += 2
		s.wordStart = false
	case c == '$' && s.peek(1) == '(':
		s.push('(', s.i)
		s.i += 2
		s.wordStart = false
	case c == '$' && s.peek(1) == '{':
		s.push('{', s.i)
		s.i += 2
		s.wordStart = false
	default:
		return false
	}
	return true
}

// handlePlain advances over anything that opens no construct: comments and
// heredoc openers (only recognised at top level).
func (s *quoteScanner) handlePlain() {
	c := s.cmd[s.i]
	if s.top() == 0 {
		if c == '#' && s.wordStart {
			for s.i < len(s.cmd) && s.cmd[s.i] != '\n' {
				s.i++
			}
			return
		}
		if c == '<' && s.peek(1) == '<' && s.peek(2) != '<' {
			if p, ok := s.consumeHeredocOpener(); ok {
				s.pending = append(s.pending, p)
				return
			}
		}
	}
	s.wordStart = c == ' ' || c == '\t' || c == '\n' ||
		c == ';' || c == '|' || c == '&' || c == '('
	s.i++
}

// consumeHeredocOpener records the delimiter of a heredoc opener at s.i and
// advances past the opener (delimiter included).
func (s *quoteScanner) consumeHeredocOpener() (heredocPending, bool) {
	pos, delim, tabs, ok := parseHeredocDelim(s.cmd, s.i)
	if !ok {
		return heredocPending{}, false
	}
	end := pos + len(delim)
	if end < len(s.cmd) && (s.cmd[end] == '\'' || s.cmd[end] == '"') {
		end++
	}
	s.i = end
	s.wordStart = false
	return heredocPending{delim: delim, tabs: tabs, pos: pos}, true
}

func (s *quoteScanner) peek(n int) byte {
	if s.i+n >= len(s.cmd) {
		return 0
	}
	return s.cmd[s.i+n]
}

func (s *quoteScanner) reportUnclosed() {
	for _, f := range s.stack {
		switch f.kind {
		case '\'':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedQuote, f.pos, "unterminated single quote"})
		case '"':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedQuote, f.pos, "unterminated double quote"})
		case '$':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedQuote, f.pos, "unterminated $'…' string"})
		case '`':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedBacktick, f.pos, "unterminated backtick substitution"})
		case '(':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedQuote, f.pos, "unterminated $( … ) substitution"})
		case '{':
			s.out = append(s.out, LintFinding{"error", CodeLintUnbalancedQuote, f.pos, "unterminated ${ … } expansion"})
		}
	}
}

// parseHeredocDelim reads the delimiter of a heredoc opener at i (which points
// at "<<"). It returns the delimiter, whether tabs are stripped (<<-), and the
// offset of the delimiter itself.
func parseHeredocDelim(cmd string, i int) (pos int, delim string, tabs bool, ok bool) {
	j := i + 2
	if j < len(cmd) && cmd[j] == '-' {
		tabs = true
		j++
	}
	for j < len(cmd) && (cmd[j] == ' ' || cmd[j] == '\t') {
		j++
	}
	quote := byte(0)
	if j < len(cmd) && (cmd[j] == '\'' || cmd[j] == '"') {
		quote = cmd[j]
		j++
	}
	start := j
	for j < len(cmd) {
		c := cmd[j]
		if quote != 0 {
			if c == quote {
				break
			}
		} else if !isShellWordByte(c) {
			break
		}
		j++
	}
	if j == start {
		return 0, "", false, false
	}
	return start, cmd[start:j], tabs, true
}

func isShellWordByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

// --- nested-shell detection --------------------------------------------

type tokKind int

const (
	tokWord tokKind = iota
	tokQuote
)

type shellToken struct {
	kind  tokKind
	text  string // for quotes: the text with the outer quotes removed
	quote byte
	pos   int // offset of the opening quote (or of the word)
	// contentPos is where the quoted text itself starts (pos+1, or pos+2 for
	// $'…'), so findings inside a quoted token can point at the real byte.
	contentPos int
	// nestedShellArg marks a double-quoted token that is the script argument
	// of `<shell> -c` or of `ssh <host> "…"`.
	nestedShellArg bool
}

// shellTokens splits a command into top-level words and quoted strings,
// ignoring heredoc bodies, then marks the script argument of nested shells.
func shellTokens(cmd string) []shellToken {
	toks := tokenize(cmd)
	markNestedShellArgs(toks)
	return toks
}

func tokenize(cmd string) []shellToken {
	var toks []shellToken
	wordStart := true
	for i := 0; i < len(cmd); {
		tok, next, ws, ok := nextToken(cmd, i, wordStart)
		if ok {
			toks = append(toks, tok)
		}
		i, wordStart = next, ws
	}
	return toks
}

// nextToken consumes one lexical item at i, returning the token it produced
// (when it produced one), the offset to continue from, and whether the next
// character starts a word.
func nextToken(cmd string, i int, wordStart bool) (tok shellToken, next int, ws, ok bool) {
	switch {
	case cmd[i] == '\\':
		return shellToken{}, i + 2, false, false
	case cmd[i] == '#' && wordStart:
		return shellToken{}, skipLine(cmd, i), wordStart, false
	case startsQuote(cmd, i):
		t, n := readQuoted(cmd, i)
		return t, n, false, true
	case isTokenBreak(cmd[i]):
		return shellToken{}, i + 1, true, false
	}
	if n, skipped := skipHeredocAt(cmd, i); skipped {
		return shellToken{}, n, true, false
	}
	if t, n, ok := readWord(cmd, i); ok {
		return t, n, false, true
	}
	return shellToken{}, i + 1, wordStart, false
}

func skipLine(cmd string, i int) int {
	for i < len(cmd) && cmd[i] != '\n' {
		i++
	}
	return i
}

// startsQuote reports whether a quoted string (including $'…') opens at i.
func startsQuote(cmd string, i int) bool {
	c := cmd[i]
	return c == '\'' || c == '"' || (c == '$' && i+1 < len(cmd) && cmd[i+1] == '\'')
}

// skipHeredocAt returns the offset past a heredoc opener and its body when one
// starts at i.
func skipHeredocAt(cmd string, i int) (int, bool) {
	if cmd[i] != '<' || i+1 >= len(cmd) || cmd[i+1] != '<' || (i+2 < len(cmd) && cmd[i+2] == '<') {
		return i, false
	}
	return skipHeredoc(cmd, i)
}

// readWord reads a bare word at i.
func readWord(cmd string, i int) (shellToken, int, bool) {
	start := i
	for i < len(cmd) && !strings.ContainsRune(" \t\n;|&()<>'\"`\\", rune(cmd[i])) {
		i++
	}
	if i == start {
		return shellToken{}, i, false
	}
	return shellToken{kind: tokWord, text: cmd[start:i], pos: start}, i, true
}

// readQuoted reads one quoted string starting at i, returning the token and
// the offset just past the closing quote.
func readQuoted(cmd string, i int) (shellToken, int) {
	quote := cmd[i]
	start := i
	if quote == '$' {
		quote = '\''
		i += 2
	} else {
		i++
	}
	contentPos := i
	var b strings.Builder
	for i < len(cmd) {
		if cmd[i] == '\\' && quote != '\'' {
			if i+1 < len(cmd) {
				b.WriteByte(cmd[i+1])
			}
			i += 2
			continue
		}
		if cmd[i] == quote {
			i++
			break
		}
		b.WriteByte(cmd[i])
		i++
	}
	return shellToken{kind: tokQuote, text: b.String(), quote: quote, pos: start, contentPos: contentPos}, i
}

// skipHeredoc returns the offset just past a heredoc opener and its body.
func skipHeredoc(cmd string, i int) (int, bool) {
	_, delim, tabs, ok := parseHeredocDelim(cmd, i)
	if !ok {
		return i, false
	}
	i += len(delim) + 2
	for i <= len(cmd) {
		var line string
		if j := strings.IndexByte(cmd[i:], '\n'); j >= 0 {
			line, i = cmd[i:i+j], i+j+1
		} else {
			line, i = cmd[i:], len(cmd)+1
		}
		if tabs {
			line = strings.TrimLeft(line, "\t")
		}
		if line == delim {
			break
		}
	}
	return i, true
}

func isTokenBreak(c byte) bool {
	switch c {
	case ' ', '\t', '\n', ';', '|', '&', '(', ')':
		return true
	}
	return false
}

// markNestedShellArgs flags the quoted argument of `<shell> -c "…"` and of
// `ssh <host> "…"`, which are re-parsed by a second shell.
func markNestedShellArgs(toks []shellToken) {
	for k, t := range toks {
		if t.kind != tokQuote || t.quote != '"' || k == 0 {
			continue
		}
		if prev := toks[k-1]; prev.kind == tokWord && prev.text == "-c" {
			if shellWordBefore(toks, k-2) {
				toks[k].nestedShellArg = true
			}
			continue
		}
		if k >= 2 && toks[k-2].kind == tokWord &&
			(toks[k-2].text == "ssh" || toks[k-2].text == "scp") {
			toks[k].nestedShellArg = true
		}
	}
}

// shellWordBefore reports whether one of the words just before a -c flag names
// a shell: `sh -c`, `su root -c` and `docker exec -i c sh -c` all qualify.
func shellWordBefore(toks []shellToken, from int) bool {
	lo := from - 2
	if lo < 0 {
		lo = 0
	}
	for j := from; j >= lo; j-- {
		if toks[j].kind == tokWord && shellWord(toks[j].text) {
			return true
		}
	}
	return false
}

// shellWord reports whether a word looks like a shell that takes -c.
func shellWord(w string) bool {
	if i := strings.LastIndexByte(w, '/'); i >= 0 {
		w = w[i+1:]
	}
	switch w {
	case "sh", "bash", "dash", "zsh", "ksh", "ash", "busybox", "su", "doas":
		return true
	}
	return false
}

// LintErrors returns only the findings that block execution under strict mode.
func LintErrors(findings []LintFinding) []LintFinding {
	var out []LintFinding
	for _, f := range findings {
		if f.Kind == "error" {
			out = append(out, f)
		}
	}
	return out
}

// FormatLint renders findings as one line each, for a result note or an error
// message.
func FormatLint(findings []LintFinding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "[%s] %s: %s\n", f.Kind, f.Code, f.Msg)
	}
	return strings.TrimRight(b.String(), "\n")
}

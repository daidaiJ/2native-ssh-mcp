package manager

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// ReadSessionOutput returns bytes from a background session log. It runs on
// a fresh no-PTY exec channel so it works even when the named shell is gone
// (e.g. after a connection drop). A negative offset continues from the last
// read position; offset>=0 reads from that byte (0 re-reads from the start).
// waitMs blocks for up to that many milliseconds when there is nothing new
// and the job is still running (0 returns immediately; capped at 30s), so
// callers do not have to spin-poll.
func (m *Manager) ReadSessionOutput(ctx context.Context, sessionName string, maxBytes, offset, waitMs int64) (*SessionOutput, error) {
	if waitMs < 0 {
		waitMs = 0
	}
	if waitMs > maxBGWaitMs {
		waitMs = maxBGWaitMs
	}
	deadline := time.Now().Add(time.Duration(waitMs) * time.Millisecond)

	out, err := m.readSessionOutputOnce(ctx, sessionName, maxBytes, offset)
	if err != nil || waitMs == 0 || !out.Running || out.Output != "" {
		return out, err
	}
	// Nothing new yet and the job is still running: poll until new bytes
	// arrive, the job exits, or the wait budget runs out.
	for {
		time.Sleep(bgWaitPollInterval)
		prev := out.Offset
		out, err = m.readSessionOutputOnce(ctx, sessionName, maxBytes, -1)
		if err != nil || out.Output != "" || !out.Running || out.Offset != prev {
			return out, err
		}
		if !time.Now().Before(deadline) {
			return out, err
		}
	}
}

// readSessionOutputOnce performs a single background log read.
func (m *Manager) readSessionOutputOnce(ctx context.Context, sessionName string, maxBytes, offset int64) (*SessionOutput, error) {
	m.mu.Lock()
	ns, ok := m.sessions[sessionName]
	if !ok {
		m.mu.Unlock()
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q not found", sessionName), false)
	}
	background := ns.background
	connKey := ns.connectionKey
	bgLogPath := ns.bgLogPath
	bgPIDPath := ns.bgPIDPath
	bgExitPath := ns.bgExitPath
	bgHeadPath := ns.bgHeadPath
	bgDropPath := ns.bgDropPath
	if offset < 0 {
		offset = ns.bgOffset
	}
	m.mu.Unlock()
	if !background {
		return nil, newToolError(CodeCommandValidationFailed,
			fmt.Sprintf("session %q is not a background session; open with background=true first", sessionName), false)
	}
	m.beginOp(connKey)
	defer m.endOp(connKey)

	if maxBytes <= 0 {
		maxBytes = defaultBGReadBytes
	}

	client, err := m.EnsureConnected(ctx, connKey)
	if err != nil {
		return nil, err
	}

	readScript := buildBGReadScript(bgLogPath, bgPIDPath, bgExitPath, bgHeadPath, bgDropPath, offset, maxBytes)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	output, err := m.runDetachedExec(ctx, client, readScript, 30*time.Second)
	if err != nil {
		return nil, err
	}

	read := parseBGReadOutput(output)
	// The agent's offset is absolute (counted from the first byte the job
	// ever produced); bytes the remote side trimmed are simply already
	// behind it.
	served := max(offset, read.dropped)
	newOffset := served + int64(len(read.chunk))
	m.mu.Lock()
	ns.bgOffset = newOffset
	ns.bgRunning = read.running
	ns.bgExitCode = read.exitCode
	m.mu.Unlock()
	ns.resetIdleTimer(m)

	cfg := m.configs[connKey]
	text := read.chunk
	if read.head != "" {
		var b strings.Builder
		b.WriteString(read.head)
		if !strings.HasSuffix(read.head, "\n") {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "... [%d bytes of earlier output were dropped; the remote log keeps at most %d bytes] ...\n",
			read.dropped, cfg.GetBackgroundLogMaxBytes())
		b.WriteString(read.chunk)
		text = b.String()
	}
	if cfg == nil || cfg.GetStripAnsi() {
		text = stripANSI(text)
	}
	text = normalizeCR(text)
	// Redaction is opt-in (redactSecrets), matching the command result path.
	if cfg != nil && cfg.GetRedactSecrets() {
		text = redactCombinedOutput(text)
	}

	return &SessionOutput{
		SessionName:    sessionName,
		Output:         text, // read path: chunk already partial; skip line compression
		Offset:         newOffset,
		TotalBytes:     read.dropped + read.size,
		Running:        read.running,
		LogPath:        bgLogPath,
		DroppedBytes:   read.dropped,
		LogMaxBytes:    int64(cfg.GetBackgroundLogMaxBytes()),
		Truncated:      read.remaining > 0,
		RemainingBytes: read.remaining,
		ExitCode:       read.exitCode,
	}, nil
}

// buildBGReadScript builds a script that reports whether the job is running,
// the log size, the job exit code (empty while running or unknown), how many
// bytes were dropped from the beginning of the log, and how much of the kept
// beginning to send. offset is absolute — counted from the first byte the job
// produced, dropped bytes included. When it points into the dropped region
// the script also returns the kept head, so one call answers with the
// beginning, an omission marker and the newest bytes.
func buildBGReadScript(logPath, pidPath, exitPath, headPath, dropPath string, offset, maxBytes int64) string {
	return fmt.Sprintf(
		"LOG=%s\nPIDF=%s\nEXITF=%s\nHEADF=%s\nDROPF=%s\n"+
			"RUN=0\n"+
			"if [ -f \"$PIDF\" ]; then PID=$(cat \"$PIDF\" 2>/dev/null); "+
			"if [ -n \"$PID\" ] && kill -0 \"$PID\" 2>/dev/null; then RUN=1; fi; fi\n"+
			"SIZE=0\n"+
			"if [ -f \"$LOG\" ]; then SIZE=$(wc -c < \"$LOG\" | tr -d ' '); fi\n"+
			"DROP=0\n"+
			"if [ -f \"$DROPF\" ]; then DROP=$(cat \"$DROPF\" 2>/dev/null | tr -d ' '); [ -n \"$DROP\" ] || DROP=0; fi\n"+
			"EXIT=\n"+
			"if [ -f \"$EXITF\" ]; then EXIT=$(cat \"$EXITF\" 2>/dev/null | tr -d ' \\n'); fi\n"+
			"REL=%d\n"+
			"if [ \"$REL\" -lt \"$DROP\" ]; then REL=$DROP; fi\n"+
			// The log starts at absolute offset DROP, so a stream offset has
			// to be rebased before it can index the file.
			"FOFF=$((REL - DROP))\n"+
			"HEADLEN=0\n"+
			"if [ %d -lt \"$DROP\" ] && [ -f \"$HEADF\" ]; then HEADLEN=$(wc -c < \"$HEADF\" | tr -d ' '); fi\n"+
			// REMAIN counts output that is already written but does not fit
			// in this chunk, so the caller can say the chunk is a prefix.
			"AVAIL=0\n"+
			"if [ \"$SIZE\" -gt \"$FOFF\" ]; then AVAIL=$((SIZE - FOFF)); fi\n"+
			"REMAIN=0\n"+
			"if [ \"$AVAIL\" -gt %d ]; then REMAIN=$((AVAIL - %d)); fi\n"+
			"printf '__MCP_BG_HDR__running=%%s size=%%s exit=%%s dropped=%%s headlen=%%s remain=%%s\\n' \"$RUN\" \"$SIZE\" \"$EXIT\" \"$DROP\" \"$HEADLEN\" \"$REMAIN\"\n"+
			"if [ \"$HEADLEN\" -gt 0 ]; then cat \"$HEADF\"; fi\n"+
			"if [ -f \"$LOG\" ] && [ \"$SIZE\" -gt \"$FOFF\" ]; then tail -c +$((FOFF+1)) \"$LOG\" | head -c %d; fi\n",
		shellQuote(logPath), shellQuote(pidPath), shellQuote(exitPath),
		shellQuote(headPath), shellQuote(dropPath),
		offset, offset, maxBytes, maxBytes, maxBytes,
	)
}

// bgReadResult is one parsed background log read.
type bgReadResult struct {
	running bool
	size    int64
	dropped int64
	// remaining is the number of already-written bytes left after this
	// chunk; non-zero means the chunk was cut to maxBytes.
	remaining int64
	exitCode  *int
	// head is the kept beginning of the output, present only when the
	// requested offset fell into the region the remote side trimmed.
	head  string
	chunk string
}

func parseBGReadOutput(output string) bgReadResult {
	var res bgReadResult
	header, rest := output, ""
	if i := strings.IndexByte(output, '\n'); i >= 0 {
		header, rest = output[:i], output[i+1:]
	}
	if idx := strings.Index(header, "__MCP_BG_HDR__"); idx >= 0 {
		header = header[idx+len("__MCP_BG_HDR__"):]
	}
	var headLen int64
	for _, part := range strings.Fields(header) {
		kv := strings.SplitN(part, "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch kv[0] {
		case "running":
			res.running = kv[1] == "1"
		case "size":
			res.size, _ = strconv.ParseInt(kv[1], 10, 64)
		case "dropped":
			res.dropped, _ = strconv.ParseInt(kv[1], 10, 64)
		case "remain":
			res.remaining, _ = strconv.ParseInt(kv[1], 10, 64)
		case "headlen":
			headLen, _ = strconv.ParseInt(kv[1], 10, 64)
		case "exit":
			if kv[1] != "" {
				if n, err := strconv.Atoi(kv[1]); err == nil {
					res.exitCode = &n
				}
			}
		}
	}
	if headLen > 0 {
		headLen = runeBoundary(rest, headLen)
		res.head, rest = rest[:headLen], rest[headLen:]
	}
	res.chunk = rest
	return res
}

// runeBoundary moves a byte index back to the start of the UTF-8 rune it
// falls inside, so splitting a stream at a byte count never leaves half a
// rune on either side of the split. An index at or past the end is the end.
func runeBoundary(s string, i int64) int64 {
	if i >= int64(len(s)) {
		return int64(len(s))
	}
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

// Package config defines SSH connection configuration and its defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Defaults shared by every connection.
const (
	DefaultPort                = 22
	DefaultTransportMode       = "exec"
	DefaultShellReadyTimeoutMs = 10000
	// DefaultCommandTimeoutMs is the per-command timeout for exec-mode
	// commands (and for shell-mode commands, see shellCommandTimeoutMs).
	// Agents override a 30s timeout on almost every call, so the default
	// matches what they ask for instead.
	DefaultCommandTimeoutMs = 120000
	// DefaultKeepAliveMs is how long a connection stays alive after a
	// command finishes.
	DefaultKeepAliveMs         = 600000
	DefaultConnectionTimeoutMs = 30000
	DefaultSftpTimeoutMs       = 300000
	DefaultMaxOutputBytes      = 10 * 1024 * 1024
	// DefaultBackgroundLogMaxBytes caps the remote log file a background
	// session writes to. Past the cap the oldest bytes are trimmed and
	// counted (the first 64 KiB stay available separately), so a chatty job
	// cannot fill the remote /tmp.
	DefaultBackgroundLogMaxBytes = 8 * 1024 * 1024
	DefaultKeepaliveIntervalMs = 10000
	DefaultKeepaliveCountMax   = 3
	DefaultCommandLogSize      = 20
	// DefaultCommandLogFlushMs is how long an audit record may sit in the
	// write buffer before it is flushed to disk. The buffer also flushes at
	// 32 records / 256 KiB; commandLogFlushMs: 0 means write-through (every
	// record is written synchronously, strictest durability).
	DefaultCommandLogFlushMs = 5000
	// DefaultCommandLogDir is the local directory for per-connection command
	// logs, relative to the process working directory so a workspace agent
	// can Grep it.
	DefaultCommandLogDir   = ".ssh-mcp-logs"
	DefaultHTTPAddr        = "127.0.0.1:8338"
	DefaultSftpConcurrency     = 16
	DefaultSftpChunkSize       = 32 * 1024
	DefaultSftpFileConcurrency = 4
	// DefaultOutputSpillThreshold is the combined stdout+stderr size above
	// which command output is spilled to a local file (one or two terminal
	// screens of a normal command; below it, light compression still applies
	// from 4KiB).
	DefaultOutputSpillThreshold = 8192
	// DefaultOutputSpillDir is the local directory for spilled command
	// output, relative to the process working directory so a workspace agent
	// can Grep it.
	DefaultOutputSpillDir = ".ssh-mcp-out"
)

// Local path restriction modes for file transfers.
const (
	// LocalPathModeCwd restricts local paths to the process working
	// directory plus allowedLocalPaths (default).
	LocalPathModeCwd = "cwd"
	// LocalPathModeList restricts local paths to allowedLocalPaths only.
	LocalPathModeList = "list"
	// LocalPathModeAny disables the local path restriction entirely.
	LocalPathModeAny = "any"
)

// Lint modes for the pre-execution structural check on a shell command.
const (
	// LintOff disables linting entirely.
	LintOff = "off"
	// LintWarn (default) reports findings and runs the command anyway.
	LintWarn = "warn"
	// LintStrict refuses commands whose findings are errors.
	LintStrict = "strict"
)

// Approval modes for destructive commands (execute-command only).
const (
	// ApprovalModeAuto runs everything without asking (default; YOLO).
	ApprovalModeAuto = "auto"
	// ApprovalModeAskDestructive asks the human via MCP elicitation before
	// running a command classified as destructive.
	ApprovalModeAskDestructive = "ask-destructive"
)

// SSHConfig is a single SSH connection configuration.
type SSHConfig struct {
	Name string `json:"name,omitempty"`
	// Description is a short human/agent-facing summary of what this
	// server is (OS, role, environment).
	Description string `json:"description,omitempty"`
	// Business is the business or workload this server is responsible for.
	Business string `json:"business,omitempty"`
	// Aliases are extra names that resolve to this connection
	// (list-servers / connectionName).
	Aliases []string `json:"aliases,omitempty"`
	// Notes are operational caveats the agent should read before acting.
	Notes              string   `json:"notes,omitempty"`
	Host               string   `json:"host"`
	Port               int      `json:"port"`
	Username           string   `json:"username"`
	Password           string   `json:"password,omitempty"`
	PrivateKey         string   `json:"privateKey,omitempty"`
	Passphrase         string   `json:"passphrase,omitempty"`
	Agent              string   `json:"agent,omitempty"`
	TryKeyboard        bool     `json:"tryKeyboard,omitempty"`
	CommandWhitelist   []string `json:"commandWhitelist,omitempty"`
	CommandBlacklist   []string `json:"commandBlacklist,omitempty"`
	Proxy              string   `json:"proxy,omitempty"`
	SocksProxy         string   `json:"socksProxy,omitempty"`
	// Via routes this connection's SSH handshake through another connection's
	// direct-tcpip channel (OpenSSH ProxyJump semantics): the jump host's
	// authentication/proxy/2FA apply to the hop, the target's credentials and
	// host key check apply to the inner handshake. The value must be another
	// connection name in the same configuration; multi-hop chains, self
	// references, cycles, and combining via with proxy/socksProxy on the same
	// connection are configuration errors (checked against the full table at
	// startup). Only the TCP layer changes — exec/shell/SFTP/background
	// behavior is identical to a direct connection.
	Via string `json:"via,omitempty"`
	Pty                *bool    `json:"pty,omitempty"`
	AllowedLocalPaths  []string `json:"allowedLocalPaths,omitempty"`
	AllowedRemotePaths []string `json:"allowedRemotePaths,omitempty"`
	// LocalPathMode controls which local paths file transfers may touch:
	// "cwd" (default) allows the process working directory plus
	// allowedLocalPaths, "list" allows only allowedLocalPaths, "any"
	// disables the restriction.
	LocalPathMode         string `json:"localPathMode,omitempty"`
	TransportMode         string `json:"transportMode,omitempty"`
	ShellReadyTimeoutMs   int    `json:"shellReadyTimeoutMs,omitempty"`
	ShellCommandTimeoutMs int    `json:"shellCommandTimeoutMs,omitempty"`
	CommandTimeoutMs      int    `json:"commandTimeoutMs,omitempty"`
	ConnectionTimeoutMs   int    `json:"connectionTimeoutMs,omitempty"`
	SftpTimeoutMs         int    `json:"sftpTimeoutMs,omitempty"`
	MaxOutputBytes        int    `json:"maxOutputBytes,omitempty"`
	// BackgroundLogMaxBytes caps the remote log a background session writes
	// to (default 8 MiB; -1 keeps the whole log). Past the cap the oldest
	// bytes are dropped and reported as droppedBytes by session action=read,
	// and the first 64 KiB of the output are kept aside so the beginning is
	// still available.
	BackgroundLogMaxBytes int `json:"backgroundLogMaxBytes,omitempty"`
	// OutputCompressLight enables lossy head/tail line compression when output
	// exceeds outputCompressThreshold bytes (default: true when unset).
	OutputCompressLight *bool `json:"outputCompressLight,omitempty"`
	// OutputCompressThreshold is the byte size before light compression runs
	// (default 4096; 0 uses default).
	OutputCompressThreshold int `json:"outputCompressThreshold,omitempty"`
	// OutputSpillThreshold is the combined stdout+stderr size above which the
	// full redacted output is written to a local file and the MCP result only
	// carries a short notice plus a small preview (default 8192; 0 uses the
	// default; -1 disables spilling).
	OutputSpillThreshold int `json:"outputSpillThreshold,omitempty"`
	// OutputSpillDir is the local directory for spilled command output
	// (default: .ssh-mcp-out under the process working directory; ~ is
	// expanded).
	OutputSpillDir string `json:"outputSpillDir,omitempty"`
	// ScriptCleanup removes the remote temp file that a scriptFile execution
	// staged, once the command finishes (default: true when unset).
	ScriptCleanup *bool `json:"scriptCleanup,omitempty"`
	// ScriptInterpreter is the remote interpreter used for a staged script
	// that has no shebang (default: "sh").
	ScriptInterpreter string `json:"scriptInterpreter,omitempty"`
	// LintMode controls the pre-execution structural check on a shell command
	// string: "off", "warn" (default; findings are reported and the command
	// still runs) or "strict" (error-class findings refuse the command).
	LintMode string `json:"lintMode,omitempty"`
	// KeepAliveMs is how long the connection stays alive after a command
	// finishes (default 600000 = 10 minutes). -1 closes it right after each
	// command; named sessions keep their connection regardless.
	KeepAliveMs int `json:"keepAliveMs,omitempty"`
	// AutoProbe enables the one-off remote capability probe (base64/jq) that
	// runs when the connection is established (default: true when unset).
	// Turn it off for connections whose remote side records every command and
	// where this server must not add any command of its own.
	AutoProbe *bool `json:"autoProbe,omitempty"`
	// StripAnsi strips ANSI escape sequences from command output before it
	// is returned (default: true when unset; false keeps colors/progress
	// bars for debugging).
	StripAnsi *bool `json:"stripAnsi,omitempty"`
	// RedactSecrets masks common secret patterns (password/token/bearer/PEM
	// blocks) in command output before it is returned or spilled to disk.
	// Default is false: even with a cheap anchor pre-scan, redacting output
	// that actually contains secrets costs ~200ms per MiB (regex passes), so
	// it is opt-in for connections whose commands print credentials.
	RedactSecrets       *bool  `json:"redactSecrets,omitempty"`
	// ResultFormat controls how successful execute-command results are
	// rendered in the MCP text content: "text" (default when unset) emits
	// the human-readable sectioned format with real newlines; "json" emits
	// the structured CommandResult JSON (pre-1.5 behavior).
	ResultFormat         string `json:"resultFormat,omitempty"`
	KeepaliveIntervalMs  int    `json:"keepaliveIntervalMs,omitempty"`
	KeepaliveCountMax   int    `json:"keepaliveCountMax,omitempty"`
	CommandTemplate     string `json:"commandTemplate,omitempty"`
	// CommandLogSize is how many recent commands to keep in the per-connection
	// command log file. Unset keeps DefaultCommandLogSize entries; an explicit
	// 0 disables the log.
	CommandLogSize *int `json:"commandLogSize,omitempty"`
	// CommandLogFlushMs is the audit-log flush policy in milliseconds: the
	// maximum time a record may sit in the write buffer before it is flushed
	// to disk (the buffer also flushes at 32 records / 256 KiB). Unset uses
	// DefaultCommandLogFlushMs; 0 means write-through — every record is
	// written synchronously (strictest durability, highest per-command cost).
	CommandLogFlushMs *int `json:"commandLogFlushMs,omitempty"`
	// CommandLogDir overrides the global command log directory for this
	// connection. The log file is <dir>/<name>.log.
	CommandLogDir string `json:"commandLogDir,omitempty"`
	// CommandLogOnlySuccess records only successful commands, keeping the log
	// free of noise from failed probes.
	CommandLogOnlySuccess bool `json:"commandLogOnlySuccess,omitempty"`
	// Algorithms customizes the SSH algorithm negotiation (kex, cipher,
	// server host key, hmac). Useful for legacy servers.
	Algorithms *Algorithms `json:"algorithms,omitempty"`
	// HostKeyCheck controls SSH host key verification: "accept-new" (default,
	// records unknown hosts into known_hosts), "strict" (rejects unknown
	// hosts), or "none" (disables verification; MITM risk).
	HostKeyCheck string `json:"hostKeyCheck,omitempty"`
	// KnownHostsFile is the OpenSSH known_hosts file used for host key
	// verification (default: ~/.ssh/known_hosts).
	KnownHostsFile string `json:"knownHostsFile,omitempty"`
	// SftpConcurrency is the number of parallel in-flight SFTP requests per
	// file transfer (default: 16; 1 disables concurrency).
	SftpConcurrency int `json:"sftpConcurrency,omitempty"`
	// SftpChunkSize is the SFTP transfer chunk size in bytes (default: 32768).
	// Values above 32 KiB are clamped: an SFTP request must fit in one SSH
	// session-channel data message, which OpenSSH caps at 32 KiB.
	SftpChunkSize int `json:"sftpChunkSize,omitempty"`
	// SftpFileConcurrency is how many files a directory transfer moves at
	// once (default: 4; 1 keeps one file at a time). Each file still uses
	// sftpConcurrency for its own in-flight requests.
	SftpFileConcurrency int `json:"sftpFileConcurrency,omitempty"`
	// SftpVerifyChecksum compares sha256 on both sides after a single-file
	// transfer (default: true). Turning it off saves a full read of the
	// transferred file on the remote side, at the cost of the content check.
	SftpVerifyChecksum *bool `json:"sftpVerifyChecksum,omitempty"`
	// SftpDedicatedConn runs file transfers over a dedicated SSH connection
	// instead of reusing the shell/exec connection. For gateways that front
	// sshd with a session-scoped overlay (shadow container), files written
	// over the reused session can be discarded when it ends; a separate
	// connection sidesteps that. Default: false.
	SftpDedicatedConn *bool `json:"sftpDedicatedConn,omitempty"`
	// HistoryFromLog supplements sparse remote `history` output with entries
	// from this server's MCP command log (tail-deduped) when a bare
	// "history" or "history <n>" is executed. Exec-mode commands run in a
	// fresh non-interactive shell where the history builtin prints nothing.
	// Default: false.
	HistoryFromLog *bool `json:"historyFromLog,omitempty"`
	// Utf8Sanitize replaces invalid UTF-8 sequences in command output with
	// \xNN byte escapes and flags the result nonUtf8, instead of letting
	// JSON marshalling silently mangle them into U+FFFD. The remote output
	// is never assumed to be UTF-8. Default: true.
	Utf8Sanitize *bool `json:"utf8Sanitize,omitempty"`
	// ApprovalMode controls the destructive-command approval gate on
	// execute-command: "auto" (default) never asks, "ask-destructive" sends
	// an MCP elicitation to the human before running a command classified as
	// destructive. When the client does not support elicitation the command
	// still runs (fail-open) with an advisory note in the result.
	ApprovalMode string `json:"approvalMode,omitempty"`
	// ApprovalPatterns are extra regexes that mark a command as destructive,
	// extending the built-in classifier. Anchored matching is not implied;
	// write patterns accordingly.
	ApprovalPatterns []string `json:"approvalPatterns,omitempty"`
	// ApprovalExemptPatterns are regexes that suppress the destructive
	// classification for a connection: matching commands run without asking
	// even when the built-in or extra patterns would flag them. Use to keep
	// routine mutating commands prompt-free without going full "auto".
	ApprovalExemptPatterns []string `json:"approvalExemptPatterns,omitempty"`
}

// Algorithms mirrors the ssh2 algorithms option of the reference
// implementation.
type Algorithms struct {
	Kex           []string `json:"kex,omitempty"`
	Cipher        []string `json:"cipher,omitempty"`
	ServerHostKey []string `json:"serverHostKey,omitempty"`
	Hmac          []string `json:"hmac,omitempty"`
}

// Normalize fills in defaults and validates the configuration.
func (c *SSHConfig) Normalize() error {
	if c.Host == "" {
		return fmt.Errorf("host is required")
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got: %d", c.Port)
	}
	if c.Username == "" {
		return fmt.Errorf("username is required")
	}
	if c.TransportMode == "" {
		c.TransportMode = DefaultTransportMode
	}
	if c.TransportMode != "exec" && c.TransportMode != "shell" {
		return fmt.Errorf("transportMode must be 'exec' or 'shell', got: %s", c.TransportMode)
	}
	if c.ShellReadyTimeoutMs <= 0 {
		c.ShellReadyTimeoutMs = DefaultShellReadyTimeoutMs
	}
	if c.ShellCommandTimeoutMs <= 0 {
		c.ShellCommandTimeoutMs = DefaultCommandTimeoutMs
	}
	if c.CommandTimeoutMs <= 0 {
		c.CommandTimeoutMs = DefaultCommandTimeoutMs
	}
	if c.ConnectionTimeoutMs <= 0 {
		c.ConnectionTimeoutMs = DefaultConnectionTimeoutMs
	}
	if c.SftpTimeoutMs <= 0 {
		c.SftpTimeoutMs = DefaultSftpTimeoutMs
	}
	if c.MaxOutputBytes < 0 {
		return fmt.Errorf("maxOutputBytes must be a non-negative integer, got: %d", c.MaxOutputBytes)
	}
	if c.MaxOutputBytes == 0 {
		c.MaxOutputBytes = DefaultMaxOutputBytes
	}
	if c.BackgroundLogMaxBytes == 0 {
		c.BackgroundLogMaxBytes = DefaultBackgroundLogMaxBytes
	}
	if c.BackgroundLogMaxBytes < -1 {
		return fmt.Errorf("backgroundLogMaxBytes must be -1 (keep the whole log) or a positive byte count, got: %d", c.BackgroundLogMaxBytes)
	}
	if c.KeepaliveIntervalMs <= 0 {
		c.KeepaliveIntervalMs = DefaultKeepaliveIntervalMs
	}
	if c.KeepaliveCountMax <= 0 {
		c.KeepaliveCountMax = DefaultKeepaliveCountMax
	}
	if c.CommandLogSize == nil {
		v := DefaultCommandLogSize
		c.CommandLogSize = &v
	} else if *c.CommandLogSize < 0 {
		return fmt.Errorf("commandLogSize must be a non-negative integer, got: %d", *c.CommandLogSize)
	}
	if c.CommandLogFlushMs != nil && *c.CommandLogFlushMs < 0 {
		return fmt.Errorf("commandLogFlushMs must be a non-negative integer (0 = write-through), got: %d", *c.CommandLogFlushMs)
	}
	if c.CommandLogDir == "" {
		c.CommandLogDir = DefaultCommandLogDir
	}
	if c.SftpConcurrency == 0 {
		c.SftpConcurrency = DefaultSftpConcurrency
	}
	if c.SftpConcurrency < 1 {
		return fmt.Errorf("sftpConcurrency must be a positive integer, got: %d", c.SftpConcurrency)
	}
	if c.SftpChunkSize == 0 {
		c.SftpChunkSize = DefaultSftpChunkSize
	}
	if c.SftpChunkSize < 1024 {
		return fmt.Errorf("sftpChunkSize must be at least 1024 bytes, got: %d", c.SftpChunkSize)
	}
	if c.SftpFileConcurrency == 0 {
		c.SftpFileConcurrency = DefaultSftpFileConcurrency
	}
	if c.SftpFileConcurrency < 1 {
		return fmt.Errorf("sftpFileConcurrency must be a positive integer, got: %d", c.SftpFileConcurrency)
	}
	if c.OutputSpillThreshold < 0 {
		c.OutputSpillThreshold = -1
	}
	if c.OutputSpillThreshold == 0 {
		c.OutputSpillThreshold = DefaultOutputSpillThreshold
	}
	if c.OutputSpillDir == "" {
		c.OutputSpillDir = DefaultOutputSpillDir
	}
	spillHadHome := strings.HasPrefix(c.OutputSpillDir, "~")
	c.OutputSpillDir = ExpandHome(c.OutputSpillDir)
	if spillHadHome {
		abs, err := filepath.Abs(c.OutputSpillDir)
		if err != nil {
			return fmt.Errorf("outputSpillDir: %w", err)
		}
		c.OutputSpillDir = abs
	}
	if c.CommandTemplate != "" &&
		!strings.Contains(c.CommandTemplate, "<command>") &&
		!strings.Contains(c.CommandTemplate, "<quotedCommand>") {
		return fmt.Errorf("commandTemplate must contain '<command>' or '<quotedCommand>' placeholder, got: %s", c.CommandTemplate)
	}
	if c.Proxy != "" && c.SocksProxy != "" {
		return fmt.Errorf("cannot use both 'proxy' and 'socksProxy'")
	}
	if c.HostKeyCheck == "" {
		c.HostKeyCheck = "accept-new"
	}
	if c.HostKeyCheck != "accept-new" && c.HostKeyCheck != "strict" && c.HostKeyCheck != "none" {
		return fmt.Errorf("hostKeyCheck must be 'accept-new', 'strict' or 'none', got: %s", c.HostKeyCheck)
	}
	if c.LocalPathMode == "" {
		c.LocalPathMode = LocalPathModeCwd
	}
	if c.LocalPathMode != LocalPathModeCwd && c.LocalPathMode != LocalPathModeList && c.LocalPathMode != LocalPathModeAny {
		return fmt.Errorf("localPathMode must be 'cwd', 'list' or 'any', got: %s", c.LocalPathMode)
	}
	if c.LintMode == "" {
		c.LintMode = LintWarn
	}
	switch c.LintMode {
	case LintOff, LintWarn, LintStrict:
	default:
		return fmt.Errorf("lintMode must be 'off', 'warn' or 'strict', got: %s", c.LintMode)
	}
	if c.KeepAliveMs == 0 {
		c.KeepAliveMs = DefaultKeepAliveMs
	}
	if c.KeepAliveMs < -1 {
		return fmt.Errorf("keepAliveMs must be -1 (close the connection after each command) or a positive duration in milliseconds, got: %d", c.KeepAliveMs)
	}
	switch strings.ToLower(strings.TrimSpace(c.ResultFormat)) {
	case "", ResultFormatText:
		c.ResultFormat = ResultFormatText
	case ResultFormatJSON:
		c.ResultFormat = ResultFormatJSON
	default:
		return fmt.Errorf("resultFormat must be 'text' or 'json', got: %s", c.ResultFormat)
	}
	if c.KnownHostsFile != "" {
		c.KnownHostsFile = ExpandHome(c.KnownHostsFile)
	}
	if c.SocksProxy != "" && !strings.HasPrefix(c.SocksProxy, "socks://") && !strings.HasPrefix(c.SocksProxy, "socks5://") {
		return fmt.Errorf("the legacy 'socksProxy' option only supports socks:// or socks5:// URLs")
	}
	if c.ApprovalMode == "" {
		c.ApprovalMode = ApprovalModeAuto
	}
	if c.ApprovalMode != ApprovalModeAuto && c.ApprovalMode != ApprovalModeAskDestructive {
		return fmt.Errorf("approvalMode must be 'auto' or 'ask-destructive', got: %s", c.ApprovalMode)
	}
	// Compile user approval patterns once here so a bad regex fails at load
	// with the field name, not at classification time on every command.
	for field, pats := range map[string][]string{
		"approvalPatterns":       c.ApprovalPatterns,
		"approvalExemptPatterns": c.ApprovalExemptPatterns,
	} {
		for _, p := range pats {
			if _, err := regexp.Compile(p); err != nil {
				return fmt.Errorf("%s: invalid regex %q: %w", field, p, err)
			}
		}
	}
	if c.PrivateKey != "" {
		c.PrivateKey = ExpandHome(c.PrivateKey)
	}
	for i, p := range c.AllowedLocalPaths {
		c.AllowedLocalPaths[i] = ExpandHome(p)
	}
	if len(c.Aliases) > 0 {
		seen := map[string]struct{}{}
		out := make([]string, 0, len(c.Aliases))
		for _, a := range c.Aliases {
			a = strings.TrimSpace(a)
			if a == "" {
				continue
			}
			if _, ok := seen[a]; ok {
				continue
			}
			seen[a] = struct{}{}
			out = append(out, a)
		}
		c.Aliases = out
	}
	return nil
}

// ValidateVias checks the via (ProxyJump) graph across the full connection
// table — a single Normalize cannot see the other names. Per connection with
// a via it rejects: multi-hop syntax (commas or whitespace), a self
// reference, an unknown jump name, a target that also carries its own
// proxy/socksProxy (the tunnel makes the semantics ambiguous — the jump host
// itself may keep its proxy), and cycles (A→B→A). Errors name the
// connections on the path so the misconfiguration is fixable in one read.
func ValidateVias(configs map[string]*SSHConfig) error {
	// Sorted order keeps validation errors and cycle reports deterministic —
	// map iteration order would otherwise rotate cycle paths run to run.
	names := make([]string, 0, len(configs))
	for name := range configs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		cfg := configs[name]
		via := strings.TrimSpace(cfg.Via)
		if via == "" {
			continue
		}
		if strings.ContainsAny(via, ", \t") {
			return fmt.Errorf("connection '%s': via must be a single connection name (multi-hop ProxyJump is not supported), got: %q", name, cfg.Via)
		}
		if cfg.Proxy != "" || cfg.SocksProxy != "" {
			return fmt.Errorf("connection '%s': via and proxy/socksProxy are mutually exclusive — the jump host [%s] owns the outer transport; put the proxy on the jump host's own connection", name, via)
		}
		if via == name {
			return fmt.Errorf("connection '%s': via must point at another connection, not itself", name)
		}
		if _, ok := configs[via]; !ok {
			return fmt.Errorf("connection '%s': via points at unknown connection '%s' — the jump host must be declared as a connection in the same configuration", name, via)
		}
	}
	// Cycle detection over the via graph. Every node participates, so even a
	// cycle with no entry point is reported with its full path.
	const visiting, done = 1, 2
	state := make(map[string]int, len(configs))
	var path []string
	var visit func(name string) error
	visit = func(name string) error {
		switch state[name] {
		case visiting:
			cycle := append(append([]string(nil), path...), name)
			start := 0
			for i, p := range path {
				if p == name {
					start = i
					break
				}
			}
			return fmt.Errorf("via cycle: %s", strings.Join(cycle[start:], " -> "))
		case done:
			return nil
		}
		state[name] = visiting
		path = append(path, name)
		cfg := configs[name]
		if cfg != nil {
			if via := strings.TrimSpace(cfg.Via); via != "" {
				if err := visit(via); err != nil {
					return err
				}
			}
		}
		path = path[:len(path)-1]
		state[name] = done
		return nil
	}
	for _, name := range names {
		if err := visit(name); err != nil {
			return err
		}
	}
	return nil
}

// GetPty returns whether a pseudo-tty should be allocated for exec commands.
// Default is false: PTY allocation makes long-running commands (docker, npm)
// behave interactively and die with the channel (SIGHUP). Interactive
// commands must opt in via "pty": true or the execute-command pty parameter.
func (c *SSHConfig) GetPty() bool {
	if c == nil || c.Pty == nil {
		return false
	}
	return *c.Pty
}

// GetBackgroundLogMaxBytes returns the byte cap for a background session's
// remote log (default DefaultBackgroundLogMaxBytes; -1 keeps every byte).
func (c *SSHConfig) GetBackgroundLogMaxBytes() int {
	if c == nil || c.BackgroundLogMaxBytes == 0 {
		return DefaultBackgroundLogMaxBytes
	}
	return c.BackgroundLogMaxBytes
}

// GetStripAnsi returns whether ANSI escape sequences should be stripped from
// command output (default: true when unset).
func (c *SSHConfig) GetStripAnsi() bool {
	if c.StripAnsi != nil {
		return *c.StripAnsi
	}
	return true
}

// GetRedactSecrets returns whether common secret patterns should be masked
// in command output (default: false — opt-in for its scanning cost).
func (c *SSHConfig) GetRedactSecrets() bool {
	if c.RedactSecrets != nil {
		return *c.RedactSecrets
	}
	return false
}

// Result format values for SSHConfig.ResultFormat.
const (
	// ResultFormatText renders successful command results as the
	// human-readable sectioned format with real newlines (default).
	ResultFormatText = "text"
	// ResultFormatJSON renders them as structured CommandResult JSON.
	ResultFormatJSON = "json"
)

// GetResultFormat normalizes the configured rendering format for successful
// execute-command results. Accepted values are "text" (default when unset)
// and "json"; unknown values fall back to "text". Nil-safe: a nil config
// yields the default.
func (c *SSHConfig) GetResultFormat() string {
	if c == nil {
		return ResultFormatText
	}
	switch strings.ToLower(strings.TrimSpace(c.ResultFormat)) {
	case ResultFormatJSON:
		return ResultFormatJSON
	default:
		return ResultFormatText
	}
}

// GetSftpDedicatedConn returns whether file transfers should run over a
// dedicated SSH connection instead of the shared shell/exec connection
// (default: false).
func (c *SSHConfig) GetSftpDedicatedConn() bool {
	if c.SftpDedicatedConn != nil {
		return *c.SftpDedicatedConn
	}
	return false
}

// GetSftpVerifyChecksum returns whether a single-file transfer is followed
// by a sha256 comparison on both sides (default: true).
func (c *SSHConfig) GetSftpVerifyChecksum() bool {
	if c == nil || c.SftpVerifyChecksum == nil {
		return true
	}
	return *c.SftpVerifyChecksum
}

// GetSftpFileConcurrency returns how many files a directory transfer moves
// at once (default: DefaultSftpFileConcurrency; never below 1).
func (c *SSHConfig) GetSftpFileConcurrency() int {
	if c == nil || c.SftpFileConcurrency <= 0 {
		return DefaultSftpFileConcurrency
	}
	return c.SftpFileConcurrency
}

// GetHistoryFromLog returns whether sparse remote `history` output should be
// supplemented from the MCP command log (default: false).
func (c *SSHConfig) GetHistoryFromLog() bool {
	if c.HistoryFromLog != nil {
		return *c.HistoryFromLog
	}
	return false
}

// GetScriptCleanup returns whether the remote temp file staged for a
// scriptFile execution is removed once the command finishes (default: true).
func (c *SSHConfig) GetScriptCleanup() bool {
	if c == nil || c.ScriptCleanup == nil {
		return true
	}
	return *c.ScriptCleanup
}

// GetScriptInterpreter returns the remote interpreter used for a staged
// script that has no shebang (default "sh").
func (c *SSHConfig) GetScriptInterpreter() string {
	if c == nil {
		return "sh"
	}
	if v := strings.TrimSpace(c.ScriptInterpreter); v != "" {
		return v
	}
	return "sh"
}

// GetKeepAliveMs returns how long the connection stays alive after a command
// finishes (default 600000). -1 means close it right after each command.
func (c *SSHConfig) GetKeepAliveMs() int {
	if c == nil || c.KeepAliveMs == 0 {
		return DefaultKeepAliveMs
	}
	return c.KeepAliveMs
}

// GetLintMode returns the pre-execution lint mode (default: warn). Unknown
// values fall back to the default; validation rejects them at startup.
func (c *SSHConfig) GetLintMode() string {
	if c == nil {
		return LintWarn
	}
	switch strings.ToLower(strings.TrimSpace(c.LintMode)) {
	case LintOff:
		return LintOff
	case LintStrict:
		return LintStrict
	default:
		return LintWarn
	}
}

// GetAutoProbe returns whether the remote capability probe runs on connect
// (default: true).
func (c *SSHConfig) GetAutoProbe() bool {
	if c == nil || c.AutoProbe == nil {
		return true
	}
	return *c.AutoProbe
}

// GetUtf8Sanitize returns whether invalid UTF-8 sequences in command output
// are replaced with \xNN byte escapes (default: true).
func (c *SSHConfig) GetUtf8Sanitize() bool {
	if c.Utf8Sanitize != nil {
		return *c.Utf8Sanitize
	}
	return true
}

// ExpandHome expands a leading ~ in a path.
func ExpandHome(p string) string {
	home := userHomeDir()
	if p == "~" {
		if home == "" {
			return p
		}
		return home
	}
	if strings.HasPrefix(p, "~/") {
		if home == "" {
			return p
		}
		return filepath.Join(home, p[2:])
	}
	return p
}

func userHomeDir() string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home
	}
	if home := os.Getenv("HOME"); home != "" {
		return home
	}
	if home := os.Getenv("USERPROFILE"); home != "" {
		return home
	}
	return ""
}

// ParsePort accepts a number or numeric string and returns an int.
func ParsePort(v any) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("port must be a valid number, got: %s", t)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("port must be a valid number, got: %v", v)
	}
}

// ParseBool accepts a bool or a "true"/"false" string.
func ParseBool(v any) (bool, error) {
	switch t := v.(type) {
	case bool:
		return t, nil
	case string:
		switch strings.ToLower(strings.TrimSpace(t)) {
		case "true":
			return true, nil
		case "false":
			return false, nil
		}
	}
	return false, fmt.Errorf("expected a boolean, got: %v", v)
}

// ParseInt accepts a number or numeric string and returns an int.
func ParseInt(v any, field string) (int, error) {
	switch t := v.(type) {
	case float64:
		return int(t), nil
	case int:
		return t, nil
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		if err != nil {
			return 0, fmt.Errorf("%s must be a positive number, got: %s", field, t)
		}
		return n, nil
	default:
		return 0, fmt.Errorf("%s must be a positive number, got: %v", field, v)
	}
}

// StringSlice normalizes a value into a string slice, accepting a JSON array
// or a pipe/comma separated string.
func StringSlice(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, item := range t {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case []string:
		return t
	case string:
		var out []string
		for _, part := range strings.FieldsFunc(t, func(r rune) bool { return r == '|' || r == ',' }) {
			if s := strings.TrimSpace(part); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

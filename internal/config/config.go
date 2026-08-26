// Package config defines SSH connection configuration and its defaults.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Defaults shared by every connection.
const (
	DefaultPort                = 22
	DefaultTransportMode       = "exec"
	DefaultShellReadyTimeoutMs = 10000
	DefaultCommandTimeoutMs    = 30000
	DefaultConnectionTimeoutMs = 30000
	DefaultSftpTimeoutMs       = 300000
	DefaultMaxOutputBytes      = 10 * 1024 * 1024
	DefaultKeepaliveIntervalMs = 10000
	DefaultKeepaliveCountMax   = 3
	DefaultCommandLogSize      = 0
	DefaultHTTPAddr            = "127.0.0.1:8338"
	DefaultSftpConcurrency     = 16
	DefaultSftpChunkSize       = 32 * 1024
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
	Notes                 string   `json:"notes,omitempty"`
	Host                  string   `json:"host"`
	Port                  int      `json:"port"`
	Username              string   `json:"username"`
	Password              string   `json:"password,omitempty"`
	PrivateKey            string   `json:"privateKey,omitempty"`
	Passphrase            string   `json:"passphrase,omitempty"`
	Agent                 string   `json:"agent,omitempty"`
	TryKeyboard           bool     `json:"tryKeyboard,omitempty"`
	CommandWhitelist      []string `json:"commandWhitelist,omitempty"`
	CommandBlacklist      []string `json:"commandBlacklist,omitempty"`
	Proxy                 string   `json:"proxy,omitempty"`
	SocksProxy            string   `json:"socksProxy,omitempty"`
	Pty                   *bool    `json:"pty,omitempty"`
	AllowedLocalPaths     []string `json:"allowedLocalPaths,omitempty"`
	AllowedRemotePaths    []string `json:"allowedRemotePaths,omitempty"`
	TransportMode         string   `json:"transportMode,omitempty"`
	ShellReadyTimeoutMs   int      `json:"shellReadyTimeoutMs,omitempty"`
	ShellCommandTimeoutMs int      `json:"shellCommandTimeoutMs,omitempty"`
	CommandTimeoutMs      int      `json:"commandTimeoutMs,omitempty"`
	ConnectionTimeoutMs   int      `json:"connectionTimeoutMs,omitempty"`
	SftpTimeoutMs         int      `json:"sftpTimeoutMs,omitempty"`
	MaxOutputBytes        int      `json:"maxOutputBytes,omitempty"`
	// OutputCompressLight enables lossy head/tail line compression when output
	// exceeds outputCompressThreshold bytes (default: true when unset).
	OutputCompressLight *bool `json:"outputCompressLight,omitempty"`
	// OutputCompressThreshold is the byte size before light compression runs
	// (default 4096; 0 uses default).
	OutputCompressThreshold int `json:"outputCompressThreshold,omitempty"`
	// StripAnsi strips ANSI escape sequences from command output before it
	// is returned (default: true when unset; false keeps colors/progress
	// bars for debugging).
	StripAnsi           *bool  `json:"stripAnsi,omitempty"`
	KeepaliveIntervalMs int    `json:"keepaliveIntervalMs,omitempty"`
	KeepaliveCountMax   int    `json:"keepaliveCountMax,omitempty"`
	CommandTemplate     string `json:"commandTemplate,omitempty"`
	// CommandLogSize is how many recent commands to keep in the per-connection
	// command log file (0 disables the log).
	CommandLogSize int `json:"commandLogSize,omitempty"`
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
	SftpChunkSize int `json:"sftpChunkSize,omitempty"`
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
	if c.KeepaliveIntervalMs <= 0 {
		c.KeepaliveIntervalMs = DefaultKeepaliveIntervalMs
	}
	if c.KeepaliveCountMax <= 0 {
		c.KeepaliveCountMax = DefaultKeepaliveCountMax
	}
	if c.CommandLogSize < 0 {
		return fmt.Errorf("commandLogSize must be a non-negative integer, got: %d", c.CommandLogSize)
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
	if c.KnownHostsFile != "" {
		c.KnownHostsFile = ExpandHome(c.KnownHostsFile)
	}
	if c.SocksProxy != "" && !strings.HasPrefix(c.SocksProxy, "socks://") && !strings.HasPrefix(c.SocksProxy, "socks5://") {
		return fmt.Errorf("the legacy 'socksProxy' option only supports socks:// or socks5:// URLs")
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

// GetPty returns whether a pseudo-tty should be allocated for exec commands.
func (c *SSHConfig) GetPty() bool {
	if c.Pty != nil {
		return *c.Pty
	}
	return true
}

// GetStripAnsi returns whether ANSI escape sequences should be stripped from
// command output (default: true when unset).
func (c *SSHConfig) GetStripAnsi() bool {
	if c.StripAnsi != nil {
		return *c.StripAnsi
	}
	return true
}

// ExpandHome expands a leading ~ in a path.
func ExpandHome(p string) string {
	if p == "~" {
		home, _ := os.UserHomeDir()
		return home
	}
	if strings.HasPrefix(p, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, p[2:])
	}
	return p
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

// Package manager owns SSH connections: lazy connect, command execution,
// file transfer, idle-based keepalive and per-connection command history.
package manager

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"2native-ssh-mcp/internal/config"
	"2native-ssh-mcp/internal/logger"
)

// DefaultKeepAliveDuration is how long a connection stays alive after the
// last activity when the caller does not override it.
const DefaultKeepAliveDuration = 10 * time.Minute

// ToolError codes, mirroring the TypeScript implementation.
const (
	CodeCommandValidationFailed = "COMMAND_VALIDATION_FAILED"
	CodeCommandExecutionError   = "COMMAND_EXECUTION_ERROR"
	CodeOutputLimitExceeded     = "OUTPUT_LIMIT_EXCEEDED"
	CodeCommandTimeout          = "COMMAND_TIMEOUT"
	CodeSSHConnectionFailed     = "SSH_CONNECTION_FAILED"
	CodeSSHConnectionTimeout    = "SSH_CONNECTION_TIMEOUT"
	CodeSSHAuthMissing          = "SSH_AUTHENTICATION_MISSING"
	CodeLocalPathNotAllowed     = "LOCAL_PATH_NOT_ALLOWED"
	CodeRemotePathNotAllowed    = "REMOTE_PATH_NOT_ALLOWED"
	CodeLocalFileReadFailed     = "LOCAL_FILE_READ_FAILED"
	CodeLocalFileWriteFailed    = "LOCAL_FILE_WRITE_FAILED"
	CodeOperationTimeout        = "OPERATION_TIMEOUT"
	CodeSFTPError               = "SFTP_ERROR"
	CodeUnsupportedInShellMode  = "UNSUPPORTED_IN_SHELL_MODE"
	CodeCancelled               = "OPERATION_CANCELLED"
	CodeUnknownError            = "UNKNOWN_ERROR"
)

// ToolError is a structured error surfaced to the MCP client.
type ToolError struct {
	Code      string
	Message   string
	Retriable bool
}

func (e *ToolError) Error() string { return e.Message }

func newToolError(code, message string, retriable bool) *ToolError {
	return &ToolError{Code: code, Message: message, Retriable: retriable}
}

func ctxToolError(ctx context.Context, timeoutCode, timeoutMsg string) error {
	if ctx.Err() == context.Canceled {
		return newToolError(CodeCancelled, "Operation cancelled", true)
	}
	return newToolError(timeoutCode, timeoutMsg, true)
}

// AsToolError converts any error into a ToolError.
func AsToolError(err error) *ToolError {
	var te *ToolError
	if errors.As(err, &te) {
		return te
	}
	return newToolError(CodeUnknownError, err.Error(), false)
}

// ServerInfo is the per-connection summary shown by list-servers.
type ServerInfo struct {
	Name        string        `json:"name"`
	Aliases     []string      `json:"aliases,omitempty"`
	Description string        `json:"description,omitempty"`
	Business    string        `json:"business,omitempty"`
	Notes       string        `json:"notes,omitempty"`
	Host        string        `json:"host"`
	Port        int           `json:"port"`
	Username    string        `json:"username"`
	Connected   bool          `json:"connected"`
	Status      *ServerStatus `json:"status,omitempty"`
}

type connectState struct {
	done chan struct{}
	err  error
}

// Manager holds all SSH connections and their configuration.
type Manager struct {
	mu          sync.Mutex
	configs     map[string]*config.SSHConfig
	clients     map[string]*ssh.Client
	connected   map[string]bool
	statuses    map[string]*ServerStatus
	pending     map[string]*connectState
	whitelist   map[string][]*regexp.Regexp
	blacklist   map[string][]*regexp.Regexp
	shells      map[string]*shellSession
	commandLogs map[string]*CommandLog
	idleTimers  map[string]*time.Timer
	aliases     map[string]string // alias -> canonical connection name
	sessions    map[string]*namedSession
	defaultName string
}

// New creates a Manager for the given configurations. defaultLogDir is the
// directory for per-connection command log files (overridable per
// connection); it may be empty to disable logging.
func New(configs map[string]*config.SSHConfig, defaultLogDir string) (*Manager, error) {
	m := &Manager{
		configs:     configs,
		clients:     map[string]*ssh.Client{},
		connected:   map[string]bool{},
		statuses:    map[string]*ServerStatus{},
		pending:     map[string]*connectState{},
		whitelist:   map[string][]*regexp.Regexp{},
		blacklist:   map[string][]*regexp.Regexp{},
		shells:      map[string]*shellSession{},
		commandLogs: map[string]*CommandLog{},
		idleTimers:  map[string]*time.Timer{},
		aliases:     map[string]string{},
		sessions:    map[string]*namedSession{},
	}

	names := make([]string, 0, len(configs))
	for name, cfg := range configs {
		names = append(names, name)
		whitelist, err := compilePatterns(cfg.CommandWhitelist, name, "whitelist")
		if err != nil {
			return nil, err
		}
		blacklist, err := compilePatterns(cfg.CommandBlacklist, name, "blacklist")
		if err != nil {
			return nil, err
		}
		m.whitelist[name] = whitelist
		m.blacklist[name] = blacklist
		if cfg.CommandLogSize > 0 {
			logDir := cfg.CommandLogDir
			if logDir == "" {
				logDir = defaultLogDir
			}
			if logDir != "" {
				log, err := NewCommandLog(logDir, name, cfg.CommandLogSize, cfg.CommandLogOnlySuccess)
				if err != nil {
					return nil, fmt.Errorf("failed to create command log for '%s': %w", name, err)
				}
				m.commandLogs[name] = log
			}
		}
	}
	sort.Strings(names)
	if _, ok := configs["default"]; ok {
		m.defaultName = "default"
	} else if len(names) > 0 {
		m.defaultName = names[0]
	}
	for _, name := range names {
		for _, alias := range configs[name].Aliases {
			if alias == name {
				continue
			}
			if _, exists := configs[alias]; exists {
				return nil, fmt.Errorf("alias %q of connection %q conflicts with an existing connection name", alias, name)
			}
			if other, ok := m.aliases[alias]; ok {
				return nil, fmt.Errorf("alias %q is used by both %q and %q", alias, other, name)
			}
			m.aliases[alias] = name
		}
	}
	return m, nil
}

func compilePatterns(patterns []string, name, kind string) ([]*regexp.Regexp, error) {
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid %s pattern for '%s': %s (%v)", kind, name, pattern, err)
		}
		out = append(out, re)
	}
	return out, nil
}

func (m *Manager) resolveName(name string) string {
	if name == "" {
		return m.defaultName
	}
	if _, ok := m.configs[name]; ok {
		return name
	}
	if canonical, ok := m.aliases[name]; ok {
		return canonical
	}
	return name
}

func (m *Manager) getConfig(name string) (*config.SSHConfig, error) {
	key := m.resolveName(name)
	cfg, ok := m.configs[key]
	if !ok {
		return nil, fmt.Errorf("SSH configuration for '%s' not set", key)
	}
	return cfg, nil
}

// ConfigNames returns all configured connection names.
func (m *Manager) ConfigNames() []string {
	names := make([]string, 0, len(m.configs))
	for name := range m.configs {
		names = append(names, name)
	}
	return names
}

// IsConnected reports whether the named connection is currently usable.
func (m *Manager) IsConnected(name string) bool {
	key := m.resolveName(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.connected[key] && m.clients[key] != nil
}

// Connect establishes the SSH connection lazily. Concurrent callers share
// the same in-flight connection attempt.
func (m *Manager) Connect(name string) error {
	key := m.resolveName(name)
	if m.hasUsableConnection(key) {
		return nil
	}

	m.mu.Lock()
	if p, ok := m.pending[key]; ok {
		m.mu.Unlock()
		<-p.done
		return p.err
	}
	st := &connectState{done: make(chan struct{})}
	m.pending[key] = st
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		delete(m.pending, key)
		m.mu.Unlock()
	}()

	cfg, err := m.getConfig(key)
	if err != nil {
		st.err = err
		close(st.done)
		return err
	}

	client, err := m.dial(key, cfg)
	if err != nil {
		st.err = err
		close(st.done)
		return err
	}

	if cfg.TransportMode == "shell" {
		if err := m.initShell(key, client, cfg); err != nil {
			client.Close()
			st.err = err
			close(st.done)
			return err
		}
	}

	m.mu.Lock()
	m.clients[key] = client
	m.connected[key] = true
	m.mu.Unlock()

	m.startHeartbeat(key, client, cfg)
	m.Touch(key, DefaultKeepAliveDuration)
	m.scheduleStatus(key)

	st.err = nil
	close(st.done)
	logger.Info("Successfully connected to SSH server [%s] %s:%d", key, cfg.Host, cfg.Port)
	return nil
}

// EnsureConnected returns a usable client, connecting if necessary.
func (m *Manager) EnsureConnected(name string) (*ssh.Client, error) {
	key := m.resolveName(name)
	if !m.hasUsableConnection(key) {
		if err := m.Connect(key); err != nil {
			return nil, err
		}
	}
	m.mu.Lock()
	client := m.clients[key]
	m.mu.Unlock()
	if client == nil {
		return nil, fmt.Errorf("SSH client for '%s' not initialized", key)
	}
	return client, nil
}

func (m *Manager) hasUsableConnection(key string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	client := m.clients[key]
	if client == nil || !m.connected[key] {
		return false
	}
	cfg := m.configs[key]
	if cfg != nil && cfg.TransportMode == "shell" {
		sh := m.shells[key]
		return sh != nil && sh.ready
	}
	return true
}

// Touch resets the idle timer for the connection. When the timer fires the
// connection is closed. A zero duration disables the timer.
func (m *Manager) Touch(name string, duration time.Duration) {
	key := m.resolveName(name)
	m.mu.Lock()
	defer m.mu.Unlock()
	if t, ok := m.idleTimers[key]; ok {
		t.Stop()
	}
	if duration <= 0 {
		delete(m.idleTimers, key)
		return
	}
	var t *time.Timer
	t = time.AfterFunc(duration, func() {
		m.mu.Lock()
		current := m.idleTimers[key]
		m.mu.Unlock()
		if current == t {
			logger.Info("Connection [%s] idle for %s, disconnecting", key, duration)
			m.Disconnect(key)
		}
	})
	m.idleTimers[key] = t
}

// Disconnect closes the connection immediately.
func (m *Manager) Disconnect(name string) {
	key := m.resolveName(name)
	m.mu.Lock()
	client := m.clients[key]
	delete(m.clients, key)
	delete(m.connected, key)
	if t, ok := m.idleTimers[key]; ok {
		t.Stop()
		delete(m.idleTimers, key)
	}
	if sh := m.shells[key]; sh != nil {
		sh.close()
		delete(m.shells, key)
	}
	m.mu.Unlock()
	m.closeSessionsForConnection(key)
	if client != nil {
		client.Close()
		logger.Info("SSH connection [%s] closed", key)
	}
}

// DisconnectAll closes every connection.
func (m *Manager) DisconnectAll() {
	for name := range m.configs {
		m.Disconnect(name)
	}
}

// GetAllServerInfos returns the summary for every configured connection.
func (m *Manager) GetAllServerInfos() []ServerInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	infos := make([]ServerInfo, 0, len(m.configs))
	for name, cfg := range m.configs {
		aliases := append([]string(nil), cfg.Aliases...)
		infos = append(infos, ServerInfo{
			Name:        name,
			Aliases:     aliases,
			Description: cfg.Description,
			Business:    cfg.Business,
			Notes:       cfg.Notes,
			Host:        cfg.Host,
			Port:        cfg.Port,
			Username:    cfg.Username,
			Connected:   m.connected[name],
			Status:      m.statuses[name],
		})
	}
	sort.Slice(infos, func(i, j int) bool { return infos[i].Name < infos[j].Name })
	return infos
}

// startHeartbeat sends keepalive requests to detect dead connections.
func (m *Manager) startHeartbeat(key string, client *ssh.Client, cfg *config.SSHConfig) {
	interval := time.Duration(cfg.KeepaliveIntervalMs) * time.Millisecond
	maxCount := cfg.KeepaliveCountMax
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		unanswered := 0
		for range ticker.C {
			m.mu.Lock()
			current := m.clients[key]
			m.mu.Unlock()
			if current != client {
				return
			}
			ok, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
			if err != nil || !ok {
				unanswered++
				if unanswered >= maxCount {
					logger.Info("Keepalive failed for [%s], invalidating connection", key)
					m.Disconnect(key)
					return
				}
			} else {
				unanswered = 0
			}
		}
	}()
}

// validateCommand checks the command against the whitelist and blacklist.
func (m *Manager) validateCommand(command, name string) error {
	key := m.resolveName(name)
	m.mu.Lock()
	whitelist := m.whitelist[key]
	blacklist := m.blacklist[key]
	m.mu.Unlock()

	for _, re := range whitelist {
		if re.MatchString(command) {
			return nil
		}
	}
	if len(whitelist) > 0 {
		return newToolError(CodeCommandValidationFailed,
			"Command validation failed: Command not in whitelist, execution forbidden", false)
	}
	for _, re := range blacklist {
		if re.MatchString(command) {
			return newToolError(CodeCommandValidationFailed,
				"Command validation failed: Command matches blacklist, execution forbidden", false)
		}
	}
	return nil
}

// RecordCommand appends an entry to the connection's command log file.
func (m *Manager) RecordCommand(name, command string, exitCode int, success bool) {
	key := m.resolveName(name)
	m.mu.Lock()
	log := m.commandLogs[key]
	m.mu.Unlock()
	if log != nil {
		log.Add(CommandLogEntry{
			Timestamp: time.Now(),
			Command:   command,
			ExitCode:  exitCode,
			Success:   success,
		})
	}
}

// --- path validation ---

func (m *Manager) validateLocalPath(localPath, name string, purpose string) (string, error) {
	if localPath == "" {
		return "", newToolError(CodeLocalPathNotAllowed, "Local path must be a non-empty string.", false)
	}
	if strings.ContainsRune(localPath, 0) {
		return "", newToolError(CodeLocalPathNotAllowed, "Local path must not contain null bytes.", false)
	}

	resolved, err := filepath.Abs(localPath)
	if err != nil {
		return "", newToolError(CodeLocalPathNotAllowed, fmt.Sprintf("Failed to resolve local path: %v", err), false)
	}

	cfg, err := m.getConfig(name)
	if err != nil {
		return "", err
	}

	allowedRoots := []string{}
	if cwd, err := os.Getwd(); err == nil {
		allowedRoots = append(allowedRoots, cwd)
	}
	for _, p := range cfg.AllowedLocalPaths {
		if strings.TrimSpace(p) != "" {
			allowedRoots = append(allowedRoots, p)
		}
	}
	realRoots := make([]string, 0, len(allowedRoots))
	for _, root := range allowedRoots {
		abs, err := filepath.Abs(root)
		if err != nil {
			continue
		}
		if real, err := filepath.EvalSymlinks(abs); err == nil {
			abs = real
		}
		realRoots = append(realRoots, abs)
	}

	parent := filepath.Dir(resolved)
	parentReal, _ := filepath.EvalSymlinks(parent)
	existing, _ := filepath.EvalSymlinks(resolved)

	pathToCheck := existing
	if pathToCheck == "" && parentReal != "" {
		pathToCheck = filepath.Join(parentReal, filepath.Base(resolved))
	}
	if pathToCheck == "" {
		pathToCheck = resolved
	}

	if purpose == "write" && parentReal == "" {
		return "", newToolError(CodeLocalPathNotAllowed,
			fmt.Sprintf("Local path parent directory must exist and be within an allowed local path. Resolved to: %s. %s",
				resolved, describeAllowedRoots("local", realRoots)), false)
	}

	for _, root := range realRoots {
		if isPathWithinRoot(pathToCheck, root) {
			return resolved, nil
		}
	}
	return "", newToolError(CodeLocalPathNotAllowed,
		fmt.Sprintf("Path traversal detected. Local path resolved to: %s. %s",
			pathToCheck, describeAllowedRoots("local", realRoots)), false)
}

func (m *Manager) validateRemotePath(remotePath, name string) (string, error) {
	if remotePath == "" {
		return "", newToolError(CodeRemotePathNotAllowed, "Remote path must be a non-empty string.", false)
	}
	if strings.ContainsRune(remotePath, 0) {
		return "", newToolError(CodeRemotePathNotAllowed, "Remote path must not contain null bytes.", false)
	}
	if !posixIsAbs(remotePath) {
		return "", newToolError(CodeRemotePathNotAllowed,
			fmt.Sprintf("Remote path must be an absolute POSIX path, got: %s", remotePath), false)
	}

	resolved := posixClean(remotePath)
	cfg, err := m.getConfig(name)
	if err != nil {
		return "", err
	}
	allowedRoots := cfg.AllowedRemotePaths
	if len(allowedRoots) == 0 {
		return resolved, nil
	}
	for _, root := range allowedRoots {
		if posixWithinRoot(resolved, root) {
			return resolved, nil
		}
	}
	return "", newToolError(CodeRemotePathNotAllowed,
		fmt.Sprintf("Remote path is not within the configured allowedRemotePaths. Resolved to: %s. %s",
			resolved, describeAllowedRoots("remote", allowedRoots)), false)
}

func describeAllowedRoots(kind string, roots []string) string {
	return fmt.Sprintf("Allowed %s paths for this connection: %s.", kind, strings.Join(roots, ", "))
}

func isPathWithinRoot(candidate, root string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel))
}

// shellQuote quotes a string for POSIX shell single-quote semantics.
func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'\''`) + "'"
}

// applyCommandTemplate substitutes <command> and <quotedCommand>.
func applyCommandTemplate(template, command string) string {
	quoted := shellQuote(command)
	out := strings.ReplaceAll(template, "<quotedCommand>", quoted)
	out = strings.ReplaceAll(out, "'<command>'", quoted)
	out = strings.ReplaceAll(out, `"<command>"`, quoted)
	return strings.ReplaceAll(out, "<command>", command)
}

func randomID(prefix string) string {
	return fmt.Sprintf("%s_%d_%x", prefix, time.Now().UnixNano(), os.Getpid()^int(time.Now().UnixNano()>>32))
}
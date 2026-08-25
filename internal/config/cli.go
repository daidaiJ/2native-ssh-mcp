package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"2native-ssh-mcp/internal/sshconfig"
)

// Options holds the parsed command line options.
type Options struct {
	Configs    map[string]*SSHConfig
	ConfigFile string
	AllowInsecureConfigPerms bool
	PreConnect bool
	Transport  string // "stdio" or "http"
	HTTPAddr   string
	// CommandLogSize is the global default for per-connection command log
	// size; 0 disables it unless a connection overrides it.
	CommandLogSize int
	// CommandLogDir is the global directory for per-connection command log
	// files (overridable per connection).
	CommandLogDir string
	// CommandLogOnlySuccess is the global default for recording only
	// successful commands in the command log.
	CommandLogOnlySuccess bool
}

// GlobalConfigKey is the reserved top-level key in a config file object that
// holds settings applying to the whole file rather than a single connection.
const GlobalConfigKey = "$global"

// GlobalConfig holds top-level settings that apply to the whole config file.
type GlobalConfig struct {
	// AllowInsecureConfigPerms skips the config file permission check
	// (Unix mode 0600/0700; Windows ACL) for this config file. Equivalent to
	// the --allow-insecure-config-perms flag, but declared inside the file.
	AllowInsecureConfigPerms bool `json:"allowInsecureConfigPerms,omitempty"`
}

// stringList is a repeatable flag value.
type stringList []string

func (s *stringList) String() string { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error {
	*s = append(*s, v)
	return nil
}

// Usage is the help text for the connection options.
const Usage = `Usage: 2native-ssh-mcp [command] [options] [host port username password]

Commands:
  (none)      Run in stdio mode (default; spawned by MCP clients)
  start       Start the HTTP daemon server (refcount managed, auto-connect)
  stop        Stop the daemon (decrease refcount; exits when it reaches 0)
  kill        Force stop the daemon
  status      Show daemon status
  install     Install Windows autostart (startup folder shortcut)
  uninstall   Remove Windows autostart
  version     Print version
  help        Print this help

Connection options:
  --config-file <path>             Load SSH server configs from a JSON file
  --ssh-config-file <path>         Read host aliases from SSH config (default: ~/.ssh/config)
  --ssh <config>                   Add an SSH config as JSON or legacy key=value pairs (repeatable)
  -h, --host <host>                SSH host or SSH config alias for single-host mode
  -p, --port <port>                SSH port for single-host mode
  -u, --username <name>            SSH username for single-host mode
  -w, --password <password>        SSH password for single-host mode
  -k, --privateKey <path>          SSH private key path for single-host mode
  -P, --passphrase <passphrase>    SSH private key passphrase
  -a, --agent <path>               SSH agent socket path or pageant on Windows
  -W, --whitelist <patterns>       Command whitelist regexes, comma-separated
  -B, --blacklist <patterns>       Command blacklist regexes, comma-separated
  --proxy <url>                    Proxy URL (SOCKS5, HTTP, or HTTPS)
  -s, --socksProxy <url>           Legacy SOCKS5 proxy URL
  --allowed-local-paths <paths>    Extra allowed local paths, comma-separated
  --allowed-remote-paths <paths>   Allowed remote POSIX absolute paths, comma-separated
  --transport-mode <mode>          SSH transport mode: exec or shell (default: exec)
  --shell-ready-timeout <ms>       Shell readiness probe timeout (default: 10000)
  --command-template <template>    Wrap commands with <command> or <quotedCommand>
  --pty                            Allocate pseudo-tty for exec mode commands (default: true)
  --try-keyboard                   Enable keyboard-interactive authentication
  --command-log-size <n>           Keep the last n executed commands per connection (0 disables)
  --command-log-dir <dir>          Directory for per-connection command log files
  --command-log-only-success       Only record successful commands in the command log
  --pre-connect                    Pre-connect to all SSH servers on startup

  --allow-insecure-config-perms    Skip config file permission checks (not recommended;
                                   also settable per file via "$global": {"allowInsecureConfigPerms": true})

Server options:
  --transport <stdio|http>         MCP transport (default: stdio; start implies http)
  --http-addr <host:port>          HTTP listen address (default: 127.0.0.1:8338)
  --version, -v                    Print version
  --help                           Print this help message`

// ParseArgs parses command line arguments for stdio or start mode.
func ParseArgs(args []string) (*Options, error) {
	fs := flag.NewFlagSet("2native-ssh-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		configFile       string
		sshConfigFile    string
		sshParams        stringList
		host             string
		portStr          string
		username         string
		password         string
		privateKey       string
		passphrase       string
		agent            string
		whitelist        string
		blacklist        string
		proxy            string
		socksProxy       string
		allowedLocal     string
		allowedRemote    string
		transportMode    string
		shellReady       string
		commandTemplate  string
		pty              bool
		ptySet           bool
		tryKeyboard      bool
		preConnect       bool
		commandLogSize   int
		commandLogDir    string
		commandLogOnly   bool
		transport        string
		httpAddr         string
		allowInsecure    bool
	)

	fs.StringVar(&configFile, "config-file", "", "")
	fs.StringVar(&sshConfigFile, "ssh-config-file", "", "")
	fs.Var(&sshParams, "ssh", "")
	fs.StringVar(&host, "host", "", "")
	fs.StringVar(&host, "h", "", "")
	fs.StringVar(&portStr, "port", "", "")
	fs.StringVar(&portStr, "p", "", "")
	fs.StringVar(&username, "username", "", "")
	fs.StringVar(&username, "u", "", "")
	fs.StringVar(&password, "password", "", "")
	fs.StringVar(&password, "w", "", "")
	fs.StringVar(&privateKey, "privateKey", "", "")
	fs.StringVar(&privateKey, "k", "", "")
	fs.StringVar(&passphrase, "passphrase", "", "")
	fs.StringVar(&passphrase, "P", "", "")
	fs.StringVar(&agent, "agent", "", "")
	fs.StringVar(&agent, "a", "", "")
	fs.StringVar(&whitelist, "whitelist", "", "")
	fs.StringVar(&whitelist, "W", "", "")
	fs.StringVar(&blacklist, "blacklist", "", "")
	fs.StringVar(&blacklist, "B", "", "")
	fs.StringVar(&proxy, "proxy", "", "")
	fs.StringVar(&socksProxy, "socksProxy", "", "")
	fs.StringVar(&socksProxy, "s", "", "")
	fs.StringVar(&allowedLocal, "allowed-local-paths", "", "")
	fs.StringVar(&allowedRemote, "allowed-remote-paths", "", "")
	fs.StringVar(&transportMode, "transport-mode", "", "")
	fs.StringVar(&shellReady, "shell-ready-timeout", "", "")
	fs.StringVar(&commandTemplate, "command-template", "", "")
	fs.BoolVar(&pty, "pty", false, "")
	fs.BoolVar(&tryKeyboard, "try-keyboard", false, "")
	fs.BoolVar(&preConnect, "pre-connect", false, "")
	fs.IntVar(&commandLogSize, "command-log-size", DefaultCommandLogSize, "")
	fs.StringVar(&commandLogDir, "command-log-dir", "", "")
	fs.BoolVar(&commandLogOnly, "command-log-only-success", false, "")
	fs.StringVar(&transport, "transport", "stdio", "")
	fs.StringVar(&httpAddr, "http-addr", DefaultHTTPAddr, "")
	fs.BoolVar(&allowInsecure, "allow-insecure-config-perms", false, "")

	// Track whether --pty was explicitly set (flag package cannot tell us).
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "pty" {
			ptySet = true
		}
	})

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	if transport != "stdio" && transport != "http" {
		return nil, fmt.Errorf("--transport must be 'stdio' or 'http', got: %s", transport)
	}

	positionals := fs.Args()
	opts := &Options{
		Configs:                  map[string]*SSHConfig{},
		AllowInsecureConfigPerms: allowInsecure,
		PreConnect:               preConnect,
		Transport:      transport,
		HTTPAddr:       httpAddr,
		CommandLogSize: commandLogSize,
		CommandLogDir:  commandLogDir,
		CommandLogOnlySuccess: commandLogOnly,
	}

	// Priority 1: config file.
	if configFile != "" {
		configs, global, err := loadConfigFile(configFile)
		if err != nil {
			return nil, err
		}
		if !allowInsecure && !global.AllowInsecureConfigPerms {
			if err := CheckConfigFilePermissions(configFile); err != nil {
				return nil, err
			}
		}
		opts.Configs = configs
		opts.ConfigFile = configFile
	}

	// Priority 2: --ssh parameters.
	if len(opts.Configs) == 0 && len(sshParams) > 0 {
		for _, sshStr := range sshParams {
			conf, err := parseSSHParam(sshStr)
			if err != nil {
				return nil, err
			}
			if conf.Name == "" || conf.Host == "" || conf.Username == "" {
				return nil, fmt.Errorf("each --ssh must include name, host, username")
			}
			opts.Configs[conf.Name] = conf
		}
	}

	// Priority 3: single-host legacy parameters.
	if len(opts.Configs) == 0 {
		conf, err := buildSingleHostConfig(host, portStr, username, password,
			privateKey, passphrase, agent, whitelist, blacklist, proxy, socksProxy,
			allowedLocal, allowedRemote, transportMode, shellReady, commandTemplate,
			pty, ptySet, tryKeyboard, positionals, sshConfigFile)
		if err != nil {
			return nil, err
		}
		if conf != nil {
			if conf.Password != "" || conf.PrivateKey != "" {
				fmt.Fprintln(os.Stderr, "WARNING: SSH credentials were provided via command line arguments, which are visible in the process list and MCP client config. Prefer --config-file with restricted file permissions, or environment variable references in the config file, e.g. \"password\": \"${SSH_MCP_PASSWORD}\".")
			}
			opts.Configs["default"] = conf
		}
	}

	// Apply the global command log defaults to connections that did not
	// configure their own.
	for _, conf := range opts.Configs {
		if conf.CommandLogSize == 0 && commandLogSize > 0 {
			conf.CommandLogSize = commandLogSize
		}
		if conf.CommandLogDir == "" && commandLogDir != "" {
			conf.CommandLogDir = commandLogDir
		}
		if commandLogOnly && !conf.CommandLogOnlySuccess {
			conf.CommandLogOnlySuccess = true
		}
		if err := conf.Normalize(); err != nil {
			return nil, fmt.Errorf("invalid config for '%s': %w", conf.Name, err)
		}
	}

	return opts, nil
}

// loadConfigFile loads SSH configs from a JSON file supporting both an array
// format and an object format. The object format may carry a reserved
// "$global" key with settings that apply to the whole file.
func loadConfigFile(path string) (map[string]*SSHConfig, *GlobalConfig, error) {
	resolved := filepath.Clean(path)
	data, err := os.ReadFile(resolved)
	if err != nil {
		return nil, nil, fmt.Errorf("config file not found or unreadable: %s", resolved)
	}

	var raw any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, nil, fmt.Errorf("invalid JSON in config file: %w", err)
	}

	configs := map[string]*SSHConfig{}
	global := &GlobalConfig{}
	switch t := raw.(type) {
	case []any:
		for _, item := range t {
			conf, err := normalizeConfig(item)
			if err != nil {
				return nil, nil, err
			}
			if conf.Name == "" || conf.Host == "" || conf.Username == "" {
				return nil, nil, fmt.Errorf("each config in array must include name, host, username")
			}
			configs[conf.Name] = conf
		}
	case map[string]any:
		for name, item := range t {
			if name == GlobalConfigKey {
				if err := parseGlobalConfig(item, global); err != nil {
					return nil, nil, err
				}
				continue
			}
			conf, err := normalizeConfig(item)
			if err != nil {
				return nil, nil, err
			}
			conf.Name = name
			configs[name] = conf
		}
	default:
		return nil, nil, fmt.Errorf("config file must contain an array or object of SSH configurations")
	}
	return configs, global, nil
}

// parseGlobalConfig fills global settings from the "$global" object.
func parseGlobalConfig(raw any, global *GlobalConfig) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", GlobalConfigKey)
	}
	if v, ok := m["allowInsecureConfigPerms"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s.allowInsecureConfigPerms: %w", GlobalConfigKey, err)
		}
		global.AllowInsecureConfigPerms = b
	}
	return nil
}

// parseSSHParam parses a single --ssh value: JSON or legacy key=value pairs.
func parseSSHParam(sshStr string) (*SSHConfig, error) {
	if strings.HasPrefix(strings.TrimSpace(sshStr), "{") {
		var raw any
		if err := json.Unmarshal([]byte(sshStr), &raw); err != nil {
			return nil, fmt.Errorf("invalid JSON format in --ssh parameter: %w", err)
		}
		conf, err := normalizeConfig(raw)
		if err != nil {
			return nil, err
		}
		if conf.Name == "" {
			return nil, fmt.Errorf("JSON config must include 'name' field")
		}
		return conf, nil
	}

	// Legacy comma-separated format: name=dev,host=1.2.3.4,port=22,user=alice,password=xxx
	raw := map[string]any{}
	for _, part := range strings.Split(sshStr, ",") {
		eq := strings.Index(part, "=")
		if eq > 0 {
			k := strings.TrimSpace(part[:eq])
			v := strings.TrimSpace(part[eq+1:])
			if k != "" && v != "" {
				raw[k] = v
			}
		}
	}
	return normalizeConfig(raw)
}

// normalizeConfig converts a raw JSON object into an SSHConfig.
func normalizeConfig(raw any) (*SSHConfig, error) {
	m, ok := raw.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("each SSH config must be an object")
	}

	port, err := ParsePort(m["port"])
	if err != nil {
		return nil, err
	}

	conf := &SSHConfig{
		Name:               str(m["name"]),
		Description:        str(firstAny(m["description"], m["desc"])),
		Business:           str(firstAny(m["business"], m["role"])),
		Aliases:            StringSlice(firstAny(m["aliases"], m["alias"])),
		Notes:              str(firstAny(m["notes"], m["note"], m["caveats"])),
		Host:               expandEnvVars(str(m["host"])),
		Port:               port,
		Username:           expandEnvVars(firstStr(m["username"], m["user"])),
		Password:           expandEnvVars(str(m["password"])),
		PrivateKey:         expandEnvVars(str(m["privateKey"])),
		Passphrase:         expandEnvVars(firstStr(m["passphrase"], os.Getenv("SSH_MCP_PASSPHRASE"))),
		Agent:              expandEnvVars(str(m["agent"])),
		Proxy:              expandEnvVars(str(m["proxy"])),
		SocksProxy:         expandEnvVars(str(m["socksProxy"])),
		CommandWhitelist:   StringSlice(firstAny(m["commandWhitelist"], m["whitelist"])),
		CommandBlacklist:   StringSlice(firstAny(m["commandBlacklist"], m["blacklist"])),
		AllowedLocalPaths:  StringSlice(m["allowedLocalPaths"]),
		AllowedRemotePaths: StringSlice(m["allowedRemotePaths"]),
		TransportMode:      str(m["transportMode"]),
		CommandTemplate:    str(m["commandTemplate"]),
		CommandLogDir:      expandEnvVars(str(m["commandLogDir"])),
	}

	if v, ok := m["pty"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.Pty = &b
	}
	if v, ok := m["tryKeyboard"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.TryKeyboard = b
	}
	if v, ok := m["commandLogOnlySuccess"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.CommandLogOnlySuccess = b
	}
	if v, ok := m["outputCompressLight"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.OutputCompressLight = &b
	}

	intFields := map[string]*int{
		"shellReadyTimeoutMs":   &conf.ShellReadyTimeoutMs,
		"shellCommandTimeoutMs": &conf.ShellCommandTimeoutMs,
		"commandTimeoutMs":      &conf.CommandTimeoutMs,
		"connectionTimeoutMs":   &conf.ConnectionTimeoutMs,
		"sftpTimeoutMs":         &conf.SftpTimeoutMs,
		"maxOutputBytes":          &conf.MaxOutputBytes,
		"outputCompressThreshold": &conf.OutputCompressThreshold,
		"keepaliveIntervalMs":   &conf.KeepaliveIntervalMs,
		"keepaliveCountMax":     &conf.KeepaliveCountMax,
		"commandLogSize":        &conf.CommandLogSize,
		"sftpConcurrency":       &conf.SftpConcurrency,
		"sftpChunkSize":         &conf.SftpChunkSize,
	}
	for field, target := range intFields {
		if v, ok := m[field]; ok && v != nil && v != "" {
			n, err := ParseInt(v, field)
			if err != nil {
				return nil, err
			}
			*target = n
		}
	}

	if rawAlgos, ok := m["algorithms"].(map[string]any); ok {
		algos := &Algorithms{
			Kex:           StringSlice(rawAlgos["kex"]),
			Cipher:        StringSlice(rawAlgos["cipher"]),
			ServerHostKey: StringSlice(rawAlgos["serverHostKey"]),
			Hmac:          StringSlice(rawAlgos["hmac"]),
		}
		conf.Algorithms = algos
	}

	return conf, nil
}

// buildSingleHostConfig builds the "default" connection from legacy flags.
func buildSingleHostConfig(host, portStr, username, password, privateKey, passphrase,
	agent, whitelist, blacklist, proxy, socksProxy, allowedLocal, allowedRemote,
	transportMode, shellReady, commandTemplate string, pty, ptySet, tryKeyboard bool,
	positionals []string, sshConfigFile string) (*SSHConfig, error) {

	if host == "" && len(positionals) > 0 {
		host = positionals[0]
	}
	if host == "" {
		return nil, nil
	}

	// Look up the alias in the SSH config file.
	var alias *sshconfig.Entry
	if host != "" {
		var err error
		alias, err = sshconfig.Lookup(host, sshConfigFile)
		if err != nil {
			return nil, err
		}
	}

	if portStr == "" && len(positionals) > 1 {
		portStr = positionals[1]
	}
	if portStr == "" && alias != nil && alias.Port > 0 {
		portStr = strconv.Itoa(alias.Port)
	}
	if portStr == "" {
		portStr = "22"
	}

	if username == "" && len(positionals) > 2 {
		username = positionals[2]
	}
	if username == "" && alias != nil {
		username = alias.User
	}
	if password == "" && len(positionals) > 3 {
		password = positionals[3]
	}
	if privateKey == "" && alias != nil {
		privateKey = alias.IdentityFile
	}
	if passphrase == "" {
		passphrase = os.Getenv("SSH_MCP_PASSPHRASE")
	}
	if agent == "" && password == "" && privateKey == "" {
		agent = os.Getenv("SSH_AUTH_SOCK")
	}

	actualHost := host
	if alias != nil && alias.HostName != "" {
		actualHost = alias.HostName
	}

	if actualHost == "" || username == "" || (password == "" && privateKey == "" && agent == "") {
		return nil, fmt.Errorf("missing required parameters, need to provide host, port, username and password, private key or agent")
	}

	port, err := strconv.Atoi(portStr)
	if err != nil {
		return nil, fmt.Errorf("port must be a valid number")
	}

	conf := &SSHConfig{
		Name:            "default",
		Host:            actualHost,
		Port:            port,
		Username:        username,
		Password:        password,
		PrivateKey:      privateKey,
		Passphrase:      passphrase,
		Agent:           agent,
		Proxy:           proxy,
		SocksProxy:      socksProxy,
		TransportMode:   transportMode,
		CommandTemplate: commandTemplate,
	}
	if ptySet {
		conf.Pty = &pty
	}
	conf.TryKeyboard = tryKeyboard
	conf.CommandWhitelist = splitCSV(whitelist)
	conf.CommandBlacklist = splitCSV(blacklist)
	conf.AllowedLocalPaths = splitCSV(allowedLocal)
	conf.AllowedRemotePaths = splitCSV(allowedRemote)
	if shellReady != "" {
		n, err := strconv.Atoi(shellReady)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("shell-ready-timeout must be a positive number, got: %s", shellReady)
		}
		conf.ShellReadyTimeoutMs = n
	}
	return conf, nil
}

func splitCSV(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if p := strings.TrimSpace(part); p != "" {
			out = append(out, p)
		}
	}
	return out
}

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// expandEnvVars resolves ${VAR} references in a config value from the
// environment, so credentials can stay out of the config file:
//
//	"password": "${SSH_MCP_PASSWORD}"
func expandEnvVars(s string) string {
	return os.Expand(s, os.Getenv)
}

func firstStr(vals ...any) string {
	for _, v := range vals {
		if s, ok := v.(string); ok && s != "" {
			return s
		}
	}
	return ""
}

func firstAny(vals ...any) any {
	for _, v := range vals {
		if v != nil {
			return v
		}
	}
	return nil
}

// IsHelpRequest reports whether the args request help output.
func IsHelpRequest(args []string) bool {
	for _, a := range args {
		if a == "--help" || a == "-help" || a == "help" {
			return true
		}
	}
	return false
}

// IsVersionRequest reports whether the args request the version.
func IsVersionRequest(args []string) bool {
	for _, a := range args {
		if a == "--version" || a == "-v" || a == "version" {
			return true
		}
	}
	return false
}

// ErrNoConfig is returned when no SSH configuration could be built.
var ErrNoConfig = errors.New("no SSH configuration provided")
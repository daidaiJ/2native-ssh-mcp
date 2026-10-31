package config

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"2native-ssh-mcp/internal/sshconfig"
)

// Options holds the parsed command line options.
type Options struct {
	Configs                  map[string]*SSHConfig
	// ResultFormat is the $global resultFormat default ("" when unset). It is
	// merged into every connection; this copy answers calls that name no
	// connection at all, such as list-servers.
	ResultFormat string
	ConfigFile               string
	AllowInsecureConfigPerms bool
	PreConnect               bool
	Transport                string // "stdio" or "http"
	HTTPAddr                 string
	// HTTPToken authenticates the /mcp endpoint when set. Required when the
	// HTTP server listens on a non-loopback address (fail closed).
	HTTPToken string
	// CommandLogSize is the global default for per-connection command log
	// size; 0 disables it unless a connection overrides it.
	CommandLogSize int
	// CommandLogDir is the global directory for per-connection command log
	// files (overridable per connection).
	CommandLogDir string
	// CommandLogOnlySuccess is the global default for recording only
	// successful commands in the command log.
	CommandLogOnlySuccess bool
	// CommandLogFlushMs overrides the audit-log flush policy (ms) for every
	// connection, when the flag was given. nil = not set on the CLI.
	// 0 = write-through (flush on every record).
	CommandLogFlushMs *int
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
	// HTTPToken authenticates the /mcp endpoint (Bearer). Falls back to
	// --http-token / SSH_MCP_HTTP_TOKEN when those are set.
	HTTPToken string `json:"httpToken,omitempty"`
	// HTTPAddr is the listen address of the HTTP daemon, used when
	// --http-addr is not given; DefaultHTTPAddr applies when neither is set.
	HTTPAddr string `json:"httpAddr,omitempty"`
	// ApprovalMode is the global default for per-connection approvalMode.
	ApprovalMode string `json:"approvalMode,omitempty"`
	// ApprovalPatterns is the global default for per-connection
	// approvalPatterns (extra destructive regexes).
	ApprovalPatterns []string `json:"approvalPatterns,omitempty"`
	// ApprovalExemptPatterns is the global default for per-connection
	// approvalExemptPatterns.
	ApprovalExemptPatterns []string `json:"approvalExemptPatterns,omitempty"`
	// SftpDedicatedConn is the global default for per-connection
	// sftpDedicatedConn (SFTP over a dedicated SSH connection).
	SftpDedicatedConn *bool `json:"sftpDedicatedConn,omitempty"`
	// SftpFileConcurrency is the global default for per-connection
	// sftpFileConcurrency (files moved at once by a directory transfer).
	SftpFileConcurrency *int `json:"sftpFileConcurrency,omitempty"`
	// SftpVerifyChecksum is the global default for per-connection
	// sftpVerifyChecksum (sha256 comparison after a single-file transfer).
	SftpVerifyChecksum *bool `json:"sftpVerifyChecksum,omitempty"`
	// ScriptCleanup is the global default for per-connection scriptCleanup.
	ScriptCleanup *bool `json:"scriptCleanup,omitempty"`
	// ScriptInterpreter is the global default for per-connection
	// scriptInterpreter.
	ScriptInterpreter string `json:"scriptInterpreter,omitempty"`
	// LintMode is the global default for per-connection lintMode.
	LintMode string `json:"lintMode,omitempty"`
	// ResultFormat is the global default for per-connection resultFormat:
	// "text" (default) or "json".
	ResultFormat string `json:"resultFormat,omitempty"`
	// KeepAliveMs is the global default for per-connection keepAliveMs.
	KeepAliveMs *int `json:"keepAliveMs,omitempty"`
	// BackgroundLogMaxBytes is the global default for the per-connection
	// backgroundLogMaxBytes cap.
	BackgroundLogMaxBytes *int `json:"backgroundLogMaxBytes,omitempty"`
	// AutoProbe is the global default for per-connection autoProbe.
	AutoProbe *bool `json:"autoProbe,omitempty"`
	// HistoryFromLog is the global default for per-connection
	// historyFromLog (supplement sparse remote history from the MCP log).
	HistoryFromLog *bool `json:"historyFromLog,omitempty"`
	// Utf8Sanitize is the global default for per-connection utf8Sanitize
	// (replace invalid UTF-8 in output with \xNN escapes).
	Utf8Sanitize *bool `json:"utf8Sanitize,omitempty"`
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
  audit-verify Verify a connection's tamper-evident audit log hash chain
              (--file <path>, or --config-file <path> for every connection)
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
  --allowed-local-paths <paths>    Allowed local paths, comma-separated (cwd mode: extra roots; list mode: the only roots)
  --allowed-remote-paths <paths>   Allowed remote POSIX absolute paths, comma-separated
  --local-path-mode <mode>         Local path restriction: cwd (default), list, or any
  --transport-mode <mode>          SSH transport mode: exec or shell (default: exec)
  --shell-ready-timeout <ms>       Shell readiness probe timeout (default: 10000)
  --command-template <template>    Wrap commands with <command> or <quotedCommand>
  --pty                            Allocate pseudo-tty for exec mode commands (default: true)
  --try-keyboard                   Enable keyboard-interactive authentication
  --command-log-size <n>           Keep the last n executed commands per connection (0 disables)
  --command-log-dir <dir>          Directory for per-connection command log files
  --command-log-only-success       Only record successful commands in the command log
  --command-log-flush-ms <ms>      Audit log flush policy: max ms a record waits in the write
                                   buffer (default 5000; buffer also flushes at 32 records;
                                   0 = write-through, every record written synchronously)
  --pre-connect                    Pre-connect to all SSH servers on startup

  --allow-insecure-config-perms    Skip config file permission checks (not recommended;
                                   also settable per file via "$global": {"allowInsecureConfigPerms": true})

Server options:
  --transport <stdio|http>         MCP transport (default: stdio; start implies http)
  --http-addr <host:port>          HTTP listen address (fallback: $global.httpAddr
                                   in the config file; default: 127.0.0.1:8338)
  --http-token <token>             Bearer token for /mcp (required for non-loopback
                                   listen; fallback: SSH_MCP_HTTP_TOKEN env, then
                                   $global.httpToken in the config file)
  --version, -v                    Print version
  --help                           Print this help message`

// ParseArgs parses command line arguments for stdio or start mode.
func ParseArgs(args []string) (*Options, error) {
	fs := flag.NewFlagSet("2native-ssh-mcp", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)

	var (
		configFile      string
		sshConfigFile   string
		sshParams       stringList
		host            string
		portStr         string
		username        string
		password        string
		privateKey      string
		passphrase      string
		agent           string
		whitelist       string
		blacklist       string
		proxy           string
		socksProxy      string
		allowedLocal    string
		allowedRemote   string
		localPathMode   string
		transportMode   string
		shellReady      string
		commandTemplate string
		pty             bool
		ptySet          bool
		tryKeyboard     bool
		preConnect      bool
		commandLogSize  int
		commandLogDir   string
		commandLogOnly  bool
		transport       string
		httpAddr        string
		httpToken       string
		allowInsecure   bool
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
	fs.StringVar(&localPathMode, "local-path-mode", "", "")
	fs.StringVar(&transportMode, "transport-mode", "", "")
	fs.StringVar(&shellReady, "shell-ready-timeout", "", "")
	fs.StringVar(&commandTemplate, "command-template", "", "")
	fs.BoolVar(&pty, "pty", false, "")
	fs.BoolVar(&tryKeyboard, "try-keyboard", false, "")
	fs.BoolVar(&preConnect, "pre-connect", false, "")
	fs.IntVar(&commandLogSize, "command-log-size", DefaultCommandLogSize, "")
	fs.StringVar(&commandLogDir, "command-log-dir", "", "")
	fs.BoolVar(&commandLogOnly, "command-log-only-success", false, "")
	var commandLogFlush int
	fs.IntVar(&commandLogFlush, "command-log-flush-ms", DefaultCommandLogFlushMs, "")
	fs.StringVar(&transport, "transport", "stdio", "")
	fs.StringVar(&httpAddr, "http-addr", DefaultHTTPAddr, "")
	fs.StringVar(&httpToken, "http-token", "", "")
	fs.BoolVar(&allowInsecure, "allow-insecure-config-perms", false, "")

	if err := fs.Parse(args); err != nil {
		return nil, err
	}

	// Which flags were explicitly set. FlagSet.Visit is a synchronous walk
	// over the flags that were set, so it has to run after Parse.
	var httpAddrSet, commandLogFlushSet bool
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "pty":
			ptySet = true
		case "http-addr":
			httpAddrSet = true
		case "command-log-flush-ms":
			commandLogFlushSet = true
		}
	})

	if transport != "stdio" && transport != "http" {
		return nil, fmt.Errorf("--transport must be 'stdio' or 'http', got: %s", transport)
	}

	positionals := fs.Args()
	// HTTP token sources, first one wins: --http-token, then
	// SSH_MCP_HTTP_TOKEN, then $global.httpToken (expanded below).
	if httpToken == "" {
		httpToken = os.Getenv("SSH_MCP_HTTP_TOKEN")
	}
	opts := &Options{
		Configs:                  map[string]*SSHConfig{},
		AllowInsecureConfigPerms: allowInsecure,
		PreConnect:               preConnect,
		Transport:                transport,
		HTTPAddr:                 httpAddr,
		HTTPToken:                httpToken,
		CommandLogSize:           commandLogSize,
		CommandLogDir:            commandLogDir,
		CommandLogOnlySuccess:    commandLogOnly,
	}
	if commandLogFlushSet {
		opts.CommandLogFlushMs = &commandLogFlush
	}

	// fileGlobal holds the $global section of the config file, applied to
	// connections below alongside the CLI-level defaults.
	var fileGlobal GlobalConfig

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
		if opts.HTTPToken == "" {
			opts.HTTPToken = expandEnvVars(global.HTTPToken)
		}
		// An explicit --http-addr wins; otherwise the config file decides,
		// and DefaultHTTPAddr applies when it names no address.
		if !httpAddrSet && global.HTTPAddr != "" {
			opts.HTTPAddr = global.HTTPAddr
		}
		fileGlobal = *global
		opts.ResultFormat = global.ResultFormat
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
			allowedLocal, allowedRemote, localPathMode, transportMode, shellReady, commandTemplate,
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
		if conf.CommandLogSize == nil && commandLogSize > 0 {
			v := commandLogSize
			conf.CommandLogSize = &v
		}
		if conf.CommandLogDir == "" && commandLogDir != "" {
			conf.CommandLogDir = commandLogDir
		}
		if commandLogOnly && !conf.CommandLogOnlySuccess {
			conf.CommandLogOnlySuccess = true
		}
		if conf.CommandLogFlushMs == nil && opts.CommandLogFlushMs != nil {
			conf.CommandLogFlushMs = opts.CommandLogFlushMs
		}
		// Apply the $global approval defaults; a connection that set its own
		// approvalMode or patterns keeps them (connection-level wins).
		if conf.ApprovalMode == "" && fileGlobal.ApprovalMode != "" {
			conf.ApprovalMode = fileGlobal.ApprovalMode
		}
		if len(conf.ApprovalPatterns) == 0 {
			conf.ApprovalPatterns = fileGlobal.ApprovalPatterns
		}
		if len(conf.ApprovalExemptPatterns) == 0 {
			conf.ApprovalExemptPatterns = fileGlobal.ApprovalExemptPatterns
		}
		// A connection that set its own sftpDedicatedConn keeps it.
		if conf.SftpDedicatedConn == nil {
			conf.SftpDedicatedConn = fileGlobal.SftpDedicatedConn
		}
		if conf.SftpFileConcurrency == 0 && fileGlobal.SftpFileConcurrency != nil {
			conf.SftpFileConcurrency = *fileGlobal.SftpFileConcurrency
		}
		if conf.SftpVerifyChecksum == nil {
			conf.SftpVerifyChecksum = fileGlobal.SftpVerifyChecksum
		}
		if conf.HistoryFromLog == nil {
			conf.HistoryFromLog = fileGlobal.HistoryFromLog
		}
		if conf.Utf8Sanitize == nil {
			conf.Utf8Sanitize = fileGlobal.Utf8Sanitize
		}
		if conf.ScriptCleanup == nil {
			conf.ScriptCleanup = fileGlobal.ScriptCleanup
		}
		if conf.ScriptInterpreter == "" {
			conf.ScriptInterpreter = fileGlobal.ScriptInterpreter
		}
		if conf.LintMode == "" {
			conf.LintMode = fileGlobal.LintMode
		}
		if conf.ResultFormat == "" {
			conf.ResultFormat = fileGlobal.ResultFormat
		}
		if conf.KeepAliveMs == 0 && fileGlobal.KeepAliveMs != nil {
			conf.KeepAliveMs = *fileGlobal.KeepAliveMs
		}
		if conf.BackgroundLogMaxBytes == 0 && fileGlobal.BackgroundLogMaxBytes != nil {
			conf.BackgroundLogMaxBytes = *fileGlobal.BackgroundLogMaxBytes
		}
		if conf.AutoProbe == nil {
			conf.AutoProbe = fileGlobal.AutoProbe
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

// parseGlobalConfig fills global settings from the "$global" object. Each key
// is read by a small helper so that adding a setting is one line here.
func parseGlobalConfig(raw any, global *GlobalConfig) error {
	m, ok := raw.(map[string]any)
	if !ok {
		return fmt.Errorf("%s must be an object", GlobalConfigKey)
	}
	readers := []func(map[string]any) error{
		globalBool("allowInsecureConfigPerms", &global.AllowInsecureConfigPerms),
		globalString("httpToken", &global.HTTPToken),
	globalHTTPAddr(&global.HTTPAddr),
		globalString("approvalMode", &global.ApprovalMode),
		globalStringSlice("approvalPatterns", &global.ApprovalPatterns),
		globalStringSlice("approvalExemptPatterns", &global.ApprovalExemptPatterns),
		globalOptBool("sftpDedicatedConn", &global.SftpDedicatedConn),
		globalOptInt("sftpFileConcurrency", &global.SftpFileConcurrency),
		globalOptBool("sftpVerifyChecksum", &global.SftpVerifyChecksum),
		globalOptBool("historyFromLog", &global.HistoryFromLog),
		globalOptBool("utf8Sanitize", &global.Utf8Sanitize),
		globalOptBool("scriptCleanup", &global.ScriptCleanup),
		globalString("scriptInterpreter", &global.ScriptInterpreter),
		globalString("lintMode", &global.LintMode),
		globalString("resultFormat", &global.ResultFormat),
		globalOptInt("keepAliveMs", &global.KeepAliveMs),
		globalOptInt("backgroundLogMaxBytes", &global.BackgroundLogMaxBytes),
		globalOptBool("autoProbe", &global.AutoProbe),
	}
	for _, read := range readers {
		if err := read(m); err != nil {
			return err
		}
	}
	return nil
}

func globalBool(key string, dst *bool) func(map[string]any) error {
	return func(m map[string]any) error {
		v, ok := m[key]
		if !ok {
			return nil
		}
		b, err := ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", GlobalConfigKey, key, err)
		}
		*dst = b
		return nil
	}
}

func globalOptBool(key string, dst **bool) func(map[string]any) error {
	return func(m map[string]any) error {
		v, ok := m[key]
		if !ok {
			return nil
		}
		b, err := ParseBool(v)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", GlobalConfigKey, key, err)
		}
		*dst = &b
		return nil
	}
}

func globalString(key string, dst *string) func(map[string]any) error {
	return func(m map[string]any) error {
		v, ok := m[key]
		if !ok {
			return nil
		}
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("%s.%s must be a string", GlobalConfigKey, key)
		}
		*dst = s
		return nil
	}
}

// globalHTTPAddr reads $global.httpAddr. A malformed address fails here, so
// the error names the config key instead of surfacing later as an invalid
// --http-addr at listen time.
func globalHTTPAddr(dst *string) func(map[string]any) error {
	return func(m map[string]any) error {
		if err := globalString("httpAddr", dst)(m); err != nil {
			return err
		}
		if *dst == "" {
			return nil
		}
		_, portStr, err := net.SplitHostPort(*dst)
		if err != nil {
			return fmt.Errorf("%s.httpAddr: %v", GlobalConfigKey, err)
		}
		if _, err := strconv.Atoi(portStr); err != nil {
			return fmt.Errorf("%s.httpAddr: port %q is not a number", GlobalConfigKey, portStr)
		}
		return nil
	}
}

func globalOptInt(key string, dst **int) func(map[string]any) error {
	return func(m map[string]any) error {
		v, ok := m[key]
		if !ok || v == nil || v == "" {
			return nil
		}
		n, err := ParseInt(v, key)
		if err != nil {
			return fmt.Errorf("%s.%s: %w", GlobalConfigKey, key, err)
		}
		*dst = &n
		return nil
	}
}

func globalStringSlice(key string, dst *[]string) func(map[string]any) error {
	return func(m map[string]any) error {
		if v, ok := m[key]; ok {
			*dst = StringSlice(v)
		}
		return nil
	}
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
		Name:                   str(m["name"]),
		Description:            str(firstAny(m["description"], m["desc"])),
		Business:               str(firstAny(m["business"], m["role"])),
		Aliases:                StringSlice(firstAny(m["aliases"], m["alias"])),
		Notes:                  str(firstAny(m["notes"], m["note"], m["caveats"])),
		Host:                   expandEnvVars(str(m["host"])),
		Port:                   port,
		Username:               expandEnvVars(firstStr(m["username"], m["user"])),
		Password:               expandEnvVars(str(m["password"])),
		PrivateKey:             expandEnvVars(str(m["privateKey"])),
		Passphrase:             expandEnvVars(firstStr(m["passphrase"], os.Getenv("SSH_MCP_PASSPHRASE"))),
		Agent:                  expandEnvVars(str(m["agent"])),
		Proxy:                  expandEnvVars(str(m["proxy"])),
		SocksProxy:             expandEnvVars(str(m["socksProxy"])),
		Via:                    str(m["via"]),
		CommandWhitelist:       StringSlice(firstAny(m["commandWhitelist"], m["whitelist"])),
		CommandBlacklist:       StringSlice(firstAny(m["commandBlacklist"], m["blacklist"])),
		AllowedLocalPaths:      StringSlice(m["allowedLocalPaths"]),
		AllowedRemotePaths:     StringSlice(m["allowedRemotePaths"]),
		LocalPathMode:          str(m["localPathMode"]),
		TransportMode:          str(m["transportMode"]),
		CommandTemplate:        str(m["commandTemplate"]),
		CommandLogDir:          expandEnvVars(str(m["commandLogDir"])),
		OutputSpillDir:         expandEnvVars(str(m["outputSpillDir"])),
		LintMode:               str(m["lintMode"]),
		ResultFormat:           str(m["resultFormat"]),
		ScriptInterpreter:      str(m["scriptInterpreter"]),
		ApprovalMode:           str(m["approvalMode"]),
		ApprovalPatterns:       StringSlice(m["approvalPatterns"]),
		ApprovalExemptPatterns: StringSlice(m["approvalExemptPatterns"]),
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
	if v, ok := m["scriptCleanup"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("scriptCleanup: %w", err)
		}
		conf.ScriptCleanup = &b
	}
	if v, ok := m["autoProbe"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, fmt.Errorf("autoProbe: %w", err)
		}
		conf.AutoProbe = &b
	}
	if v, ok := m["commandLogOnlySuccess"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.CommandLogOnlySuccess = b
	}
	if v, ok := m["commandLogSize"]; ok && v != nil && v != "" {
		n, err := ParseInt(v, "commandLogSize")
		if err != nil {
			return nil, err
		}
		conf.CommandLogSize = &n
	}
	if v, ok := m["commandLogFlushMs"]; ok && v != nil && v != "" {
		n, err := ParseInt(v, "commandLogFlushMs")
		if err != nil {
			return nil, err
		}
		conf.CommandLogFlushMs = &n
	}
	if v, ok := m["outputCompressLight"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.OutputCompressLight = &b
	}
	if v, ok := m["stripAnsi"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.StripAnsi = &b
	}
	if v, ok := m["redactSecrets"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.RedactSecrets = &b
	}
	if v, ok := m["sftpDedicatedConn"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.SftpDedicatedConn = &b
	}
	if v, ok := m["sftpVerifyChecksum"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.SftpVerifyChecksum = &b
	}
	if v, ok := m["historyFromLog"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.HistoryFromLog = &b
	}
	if v, ok := m["utf8Sanitize"]; ok {
		b, err := ParseBool(v)
		if err != nil {
			return nil, err
		}
		conf.Utf8Sanitize = &b
	}

	intFields := map[string]*int{
		"shellReadyTimeoutMs":     &conf.ShellReadyTimeoutMs,
		"shellCommandTimeoutMs":   &conf.ShellCommandTimeoutMs,
		"commandTimeoutMs":        &conf.CommandTimeoutMs,
		"connectionTimeoutMs":     &conf.ConnectionTimeoutMs,
		"sftpTimeoutMs":           &conf.SftpTimeoutMs,
		"maxOutputBytes":          &conf.MaxOutputBytes,
		"backgroundLogMaxBytes":   &conf.BackgroundLogMaxBytes,
		"outputCompressThreshold": &conf.OutputCompressThreshold,
		"outputSpillThreshold":    &conf.OutputSpillThreshold,
		"keepaliveIntervalMs":     &conf.KeepaliveIntervalMs,
		"keepaliveCountMax":       &conf.KeepaliveCountMax,
		"keepAliveMs":             &conf.KeepAliveMs,
		"sftpConcurrency":         &conf.SftpConcurrency,
		"sftpChunkSize":           &conf.SftpChunkSize,
		"sftpFileConcurrency":     &conf.SftpFileConcurrency,
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
	localPathMode, transportMode, shellReady, commandTemplate string, pty, ptySet, tryKeyboard bool,
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
	// A ProxyJump alias only resolves against a named connection table; the
	// single-host CLI has exactly one unnamed connection, so silently
	// creating a jump connection is not an option — fail loudly instead.
	if alias != nil && alias.ProxyJump != "" {
		return nil, fmt.Errorf(
			"ssh config alias %q sets ProxyJump %q; declare the jump host as a named connection in the configuration file and add \"via\": %q on this connection",
			host, alias.ProxyJump, alias.ProxyJump)
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
	conf.LocalPathMode = localPathMode
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

// envVarPattern matches only ${VAR} references; $HOME, $foo and $$ are left
// as literal text so passwords containing OpenSSH-style fragments stay safe.
var envVarPattern = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expandEnvVars resolves ${VAR} references in a config value from the
// environment, so credentials can stay out of the config file:
//
//	"password": "${SSH_MCP_PASSWORD}"
//
// An unset ${VAR} becomes the empty string, matching os.Expand.
func expandEnvVars(s string) string {
	return envVarPattern.ReplaceAllStringFunc(s, func(m string) string {
		return os.Getenv(m[2 : len(m)-1])
	})
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

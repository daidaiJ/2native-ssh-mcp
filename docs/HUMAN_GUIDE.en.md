> [中文版](HUMAN_GUIDE.md)

# HUMAN_GUIDE — Configuration and Deployment Guide (Human-Readable Version)

This guide is intended for **human users** and provides detailed instructions on configuring, deploying, and using 2native-ssh-mcp. For the token-optimized version for AI Agents, see [AGENT_GUIDE.md](AGENT_GUIDE.md).

## Security Configuration (Important)

**Do NOT hardcode server addresses and passwords in your MCP client configuration parameters** — command-line arguments are visible in the process list, and MCP configurations are often shared or committed to repositories. We recommend one of the following three approaches (choose one):

### Method 1: Configuration File + Environment Variable Reference (Recommended)

Reference credentials using `${environment-variable-name}` in your configuration file, so passwords never touch disk:

```json
{
  "dev": {
    "host": "10.0.0.1",
    "port": 22,
    "username": "root",
    "password": "${SSH_MCP_PASSWORD}"
  }
}
```

```bash
# Set environment variable (Windows: setx SSH_MCP_PASSWORD xxx)
export SSH_MCP_PASSWORD='your-password'
```

Only the configuration file path appears in your MCP client configuration:

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/2native-ssh-mcp.exe",
      "args": ["--config-file", "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/config.json"]
    }
  }
}
```

### Method 2: Configuration File + File Permission Locking

Write the password in plaintext in `config.json`, but tighten file permissions (Linux/macOS: `chmod 600 config.json`; Windows: Right-click → Properties → Security → Allow only your user). The MCP configuration still only specifies `--config-file`.

### Method 3: Reuse `~/.ssh/config` Aliases

Keep all credentials in your existing SSH configuration/agent, leaving the MCP configuration completely free of sensitive information:

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "2native-ssh-mcp",
      "args": ["--host", "myserver"]
    }
  }
}
```

> If you still pass credentials via `--password`/`--privateKey`, the program will print a security warning to stderr.

## Quick Start

### stdio Mode (Directly launched by MCP client)

After preparing `config.json` using one of the secure configuration methods above:

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/2native-ssh-mcp.exe",
      "args": ["--config-file", "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/config.json"]
    }
  }
}
```

### HTTP Persistent Daemon

```bash
# Start (reference count +1; if already running, just +1)
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338

# Check status / Stop (exits only when reference count reaches zero) / Force stop
2native-ssh-mcp.exe status
2native-ssh-mcp.exe stop
2native-ssh-mcp.exe kill

# Windows auto-start on boot (generates config.json template + shortcut in Startup folder)
2native-ssh-mcp.exe install
2native-ssh-mcp.exe uninstall
```

The `autostart.vbs` written by `install` creates the daemon through WMI with a `Win32_ProcessStartup` that sets `DETACHED_PROCESS`, so the process has **no console window at all**; plain WMI creation without startup information is not enough — its console window is created shown, and the default terminal (Windows Terminal) then displays it. If the WMI call fails, it falls back to a hidden-window launch (a hidden conhost window, invisible on screen). Restart the daemon manually with `wscript autostart.vbs`, not `cscript`.

**Reference Counting and Leases**: The first `start` creates an owner (lives as long as the daemon process, never expires); additional `start` commands create **guest leases** that are automatically reclaimed (count -1) after 15 minutes of idleness with no `/mcp` requests. Any authenticated `/mcp` request (regardless of client/credentials or MCP layer success) **refreshes the expiration time of ALL** guest leases — as long as the daemon is still in use, no guest will be reclaimed. `stop` decrements a guest first, and only decrements the owner if there are no guests left; the daemon exits when the count reaches zero. The daemon process itself never exits due to idleness (owner never expires). `kill` / Ctrl-C shuts down immediately, ignoring reference count.

MCP client configuration:

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "url": "http://127.0.0.1:8338/mcp"
    }
  }
}
```

**/mcp Authentication (token)**: When listening on a loopback address (default `127.0.0.1`), no token is required, and local clients work with zero configuration. When listening on a non-loopback address (e.g., `0.0.0.0`), a token is **required**, otherwise startup will be rejected (fail closed). Token is sourced in priority order: `--http-token` → environment variable `SSH_MCP_HTTP_TOKEN` → configuration file `$global.httpToken` (supports `${VAR}` expansion). When using a token, add the request header on the client side:

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "url": "http://0.0.0.0:8338/mcp",
      "headers": { "Authorization": "Bearer ${SSH_MCP_HTTP_TOKEN}" }
    }
  }
}
```

Admin API (`/__admin/*`) does not use this token and still restricts access to localhost only (loopback source + loopback `Host` header); `/__admin/shutdown` additionally requires **POST + `Content-Type: application/json`** to prevent accidental shutdown via GET from a local webpage.

### Multiple Server Configuration (config.json)

```json
{
  "$global": {
    "allowInsecureConfigPerms": true
  },
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root", "password": "${SSH_MCP_PASSWORD}",
    "description": "Development jump host",
    "business": "Order/Payment integration testing",
    "aliases": ["dev-box", "开发"],
    "notes": "Read-only mostly; avoid heavy queries during peak hours",
    "commandWhitelist": ["^ls ", "^cat ", "^df "],
    "commandBlacklist": ["^rm -rf"],
    "allowedRemotePaths": ["/tmp", "/home"],
    "commandLogSize": 50,
    "commandLogDir": "logs",
    "commandLogOnlySuccess": true
  },
  "prod": {
    "host": "10.0.0.2", "username": "deploy",
    "privateKey": "~/.ssh/id_rsa", "passphrase": "${SSH_MCP_PASSPHRASE}",
    "transportMode": "shell"
  }
}
```

`$global` is a reserved top-level key (only supported for object format, not array format) and holds settings that apply to the entire configuration file:

| Configuration | Default | Description |
|---|---|---|
| `allowInsecureConfigPerms` | false | Skip permission checks for this configuration file (equivalent to `--allow-insecure-config-perms` on the command line, but declared inside the file; not recommended, for development only) |
| `httpToken` | empty | Bearer token for `/mcp` (lower priority than `--http-token` and `SSH_MCP_HTTP_TOKEN`; supports `${VAR}` references) |
| `httpAddr` | empty | Listen address of the HTTP daemon (lower priority than `--http-addr`; `127.0.0.1:8338` when unset). Keeping the port in the config file means the `autostart.vbs` written by `install` needs no hand-added port |
| `scriptCleanup` / `scriptInterpreter` / `lintMode` / `keepAliveMs` / `autoProbe` / `backgroundLogMaxBytes` / `resultFormat` / `sftpFileConcurrency` / `sftpVerifyChecksum` | as above | Global defaults for the per-connection settings above; a connection-level value wins |

## Connecting to local WSL

This project has **no** separate `wsl` transport mode. Treat WSL as a normal Linux SSH target: MCP runs on Windows, sshd runs inside the distro, and you connect to `127.0.0.1` with a key. Do not launch this process via `"command": "wsl"` in the MCP config (`wsl.exe` non-interactive startups skip the login shell and mix in the Windows PATH).

After enabling and starting sshd in the distro, a sample config:

```json
{
  "wsl": {
    "host": "127.0.0.1",
    "port": 2222,
    "username": "your-linux-user",
    "privateKey": "~/.ssh/id_ed25519",
    "hostKeyCheck": "accept-new",
    "pty": false,
    "description": "Local WSL",
    "allowedRemotePaths": ["/home", "/tmp"]
  }
}
```

Notes:

- **Port**: if Windows OpenSSH Server already binds port 22, put WSL sshd on another port (e.g. 2222). Under mirrored networking, Windows and WSL **cannot each listen on the same port**; inbound LAN access to WSL also needs a Hyper-V firewall allow rule.
- **localhost**: Windows → WSL localhost forwarding works on most machines. Under default NAT, **`127.0.0.1` inside WSL is not the Windows loopback**. An MCP HTTP daemon bound to Windows `127.0.0.1:8338` is unreachable from a client inside WSL, and vice versa. For cross-side access use mirrored networking (`networkingMode=mirrored` in `.wslconfig`, and `hostAddressLoopback=true` if needed), or bind the daemon to an address the other side can route to and set `--http-token`.
- **Paths**: `file-transfer` `localPath` is a path on **the OS running the MCP process**. On Windows use `D:\\proj\\a.tar`, not `/mnt/c/...` or `\\\\wsl$\\Ubuntu\\...`. `remotePath` remains a POSIX path inside the distro (e.g. `/home/you/a.tar`).
- **Where files live**: SFTP onto Linux `/home` (ext4) is the fast path. Do not put build trees on `/mnt/c`, and do not scan the whole tree from Windows via `\\\\wsl$\\` (9P cross-filesystem I/O is slow).
- **PTY**: for docker/npm/builds on WSL, set `"pty": false` on that connection (same as other Linux targets).

## Tool Documentation

### execute-command

| Parameter | Required | Description |
|---|---|---|
| `cmdString` | ✅ | The command to execute |
| `directory` | | Working directory |
| `connectionName` | | Connection name or alias from list-servers |
| `timeout` | | Timeout in milliseconds, default from the connection's `commandTimeoutMs` (120000); on timeout the remote process group is killed by PID |
| `background` | | When `true`, the command is moved to a background session and immediately returns `sessionName`/`logPath`; poll with `session read` (use this for minute-scale jobs instead of raising timeout) |

`pty`, `keepAlive`/`keepAliveDuration` and similar set-once switches live in the connection config (see the config table): they are no longer tool parameters.

### file-transfer

| Parameter | Required | Description |
|---|---|---|
| `action` | ✅ | `upload` or `download` |
| `localPath` | ✅ | Local path (restricted by `localPathMode`, defaults to cwd + `allowedLocalPaths`) |
| `remotePath` | ✅ | Remote absolute path (must be within `allowedRemotePaths` if configured) |
| `connectionName` | | Connection name or alias from list-servers |
| `force` | | Skip deduplication/resumption and force full transfer |

- When `localPath`/`remotePath` is a **directory**, the whole tree is transferred recursively (remote parent dirs are created automatically; empty dirs are not recreated); each file uses atomic upload / dedup / resume; a single-file failure is collected in `failed` without aborting the batch; the result includes a `files` count; directory mode does not compute an overall sha256
- After a single-file transfer, both ends check sha256: match is `verified`; missing remote `sha256sum` is `unverified` (not a failure); mismatch is a hard error suggesting `force` retry
- Progress notifications go only to the client that started the request (HTTP multi-client no longer cross-talks); throttled to ~100ms, always reports 100% on completion
- If target file matches source in size and mtime → skipped (deduplication)
- Uploads **land atomically**: write same-dir `<target>.part`, posix-rename over the target after a byte-count check; failure does not touch an existing target, and `<target>.part` is kept for resume (if the server lacks posix-rename and the target already exists, the error asks you to `mv` manually)
- If remote `<target>.part` already exists and the header matches → resume from breakpoint (a failed concurrent write truncates back to the confirmed prefix and keeps the part); downloads use a temporary file + atomic rename
- Directory destinations are rejected before transfer (`IsDir` errors early)
- If source file grows during transfer → automatically appends the tail
- If local path is not within allowed range (determined by `localPathMode`) → `LOCAL_PATH_NOT_ALLOWED` with message "not within the allowed local paths for this connection"; path traversal attempts containing `..` are rejected with "Path traversal rejected" 

### list-servers

Lists all connections and active sessions: server metadata, connection status, system summary, the last 3 recorded commands per connection, + current session list.

### session

`action` parameter: `open` | `read` | `close` | `list` (exec mode only).

| action | Description |
|---|---|
| `open` | Open a session; `background=true` + `cmdString` starts a background task (with `"pty": true` on the connection the job is wrapped in a TTY) |
| `read` | Poll output from a background session (`offset` omitted/negative = continue reading, `0` = reread from beginning; `waitMs` blocks up to 30s for data or job exit when there is no new output) |
| `close` | Close the session and stop the background process (**idempotent**, can be called repeatedly) |
| `list` | List all sessions (optionally filtered by `connectionName`) |

### execute-command

Adds an optional `sessionName` parameter: executes in an already opened named session (CWD is preserved), `connectionName` is ignored in this case.

**Background tasks**: `session open` → multiple `session read` → `session close`

**Stateful operations**: `session open` → multiple `execute-command` (with sessionName) → `session close`

**For long-running / silent tasks, use `execute-command` with `background: true`** (creates a `bg-*` session and returns `sessionName`/`logPath`), **or `session background=true` + `cmdString`; poll with `read` (`waitMs` blocks for new output — do not busy-loop). Do not run `nohup ... &` or `setsid` via a foreground `execute-command`** — those die when the exec channel closes. Background tasks start in an independent channel without a PTY (new session, detached from sshd process group), **survives connection interruptions**: after disconnection, the session shows `disconnected=true` in `list-servers`, and `read` or `execute-command` with `sessionName` will automatically reconnect; only `action=close` will kill the remote background process.

**After a background job finishes, the session is still retained** (includes remote logs, 60-minute retention TTL), you must call `close` to release resources; `close` can be called repeatedly. `read` can use `offset=0` to reread from the beginning; the returned JSON includes `logPath` (remote log path) and `exitCode` (after job completion). Sessions only exist in memory: they are lost when the stdio process exits (remote logs still remain at `logPath` and can be read separately), only the persistent HTTP daemon can retain sessions across conversations. Repeating `open background=true` on an already finished session will be rejected with the `logPath` hint — close it first or read the old logs. **When the connection is unavailable, `close` cannot confirm the remote job has stopped**: the session remains in the list marked `orphaned=true` (`background` remains true), returns a retriable error, and you can just `close` it again once connectivity is restored. Background log/pid/exit file paths include a one-time random suffix (`/tmp/.2native-ssh-mcp-<session-name>-<id>.log`, etc.), and the `logPath` field always provides the actual path.

**Command result notes**:
- Non-zero exit codes are **normal results** (not errors): successful results are JSON — look at `exitCode` / `status` (`ok`/`exited`) / `stdout` / `stderr`; only validation failures, connection failures, timeouts, output limits exceeded, and connection interruptions are reported as error JSON (`code`/`message`/`retriable` plus partial `stdout`/`stderr`)
- Connection interruptions report `SSH_CONNECTION_LOST` (`retriable=false`), the remote process may still be running, **do not blindly retry**; the error JSON includes partial `stdout`/`stderr` and `replaySafe: false`
- The `timeout` for foreground commands must be greater than the actual execution time (the connection's `commandTimeoutMs`, default 120000, applies otherwise); on timeout the remote process group is killed by PID (channel Signal is only a fallback), so timed-out commands do not leak remote processes
- Output ≥8KB (`outputSpillThreshold`, `-1` to disable) spills the full output to local `.ssh-mcp-out/` (`outputSpillDir` configurable, keeps the newest 32, Unix 0600); the result carries only a notice + short preview; Agent should Grep/Read that local file, not re-run the command remotely
- **Truncation is labelled**: hitting `maxOutputBytes` sets `truncated: true` and `clippedBytes` in the error JSON and says the returned output is incomplete; light compression names the omitted line count; a `session action=read` cut by `maxBytes` returns `truncated` / `remainingBytes` and tells the caller to continue from the returned `offset` — never treat these as the complete output
- exec **does not allocate a PTY by default** (avoids docker/npm treating the session as interactive); enable it with the connection's `"pty": true` (background jobs follow the same setting when deciding whether to wrap the job in `script`)

**Agent Skill Installation**: You can copy [`skills/2native-ssh-mcp-helper/SKILL.md`](../skills/2native-ssh-mcp-helper/SKILL.md) from this repository to `.cursor/skills/` and then ask "help me configure 2native-ssh-mcp".

## Command-Line Arguments

```
2native-ssh-mcp [command] [options] [host port username password]

Commands:
  (none)      stdio mode (default, launched by MCP client)
  start       Start HTTP daemon (reference counted)
  stop        Stop (reference count -1, exits when zero)
  kill        Force stop
  status      Show status
  install     Install Windows auto-start on boot
  uninstall   Uninstall auto-start
  version     Show version
  help        Show help

Connection options:
  --config-file <path>             Load server configuration from JSON file
  --ssh-config-file <path>         Read SSH config aliases (default ~/.ssh/config)
  --ssh <config>                   Append a configuration (JSON or key=value, repeatable)
  -h, --host / -p, --port / -u, --username / -w, --password
  -k, --privateKey / -P, --passphrase / -a, --agent
  -W, --whitelist / -B, --blacklist
  --proxy <url> / -s, --socksProxy <url>
  --allowed-local-paths / --allowed-remote-paths
  --local-path-mode <cwd|list|any>  Local path restriction: cwd (default)/ list / any
  --transport-mode <exec|shell>
  --command-template <template>
  --pty / --try-keyboard
  --command-log-size <n> / --command-log-dir <dir> / --command-log-only-success
  --pre-connect                     Pre-connect all servers before startup (fail-fast if any fails)

  --allow-insecure-config-perms    Skip configuration file permission checks (not recommended, development only; can also be declared in $global of config file)

Server options:
  --transport <stdio|http>         Default stdio; start implies http
  --http-addr <host:port>          Listen address (fallback: $global.httpAddr in the config file; default: 127.0.0.1:8338)
  --http-token <token>             Bearer token for /mcp (required for non-loopback listening; also available via SSH_MCP_HTTP_TOKEN or $global.httpToken)
  --version, -v / --help
```

## Configuration Reference

| Configuration | Default | Description |
|---|---|---|
| `$global.allowInsecureConfigPerms` | false | Skip configuration file permission checks (equivalent to `--allow-insecure-config-perms`, development only) |
| `description` / `business` / `aliases` / `notes` | empty | Metadata for list-servers: purpose, business area, aliases, notes |
| `transportMode` | `exec` | `shell` is for jump host scenarios; **requires a remote POSIX `sh`-compatible interactive shell** (depends on `PS1`, `stty`, `printf`, `export`), for csh/tcsh/fish jump hosts use `exec` + `commandTemplate` |
| `commandWhitelist` / `commandBlacklist` | empty | Regex whitelist/blacklist for commands |
| `approvalMode` | `auto` | `ask-destructive`: execute-command prompts the human via MCP elicitation before running a destructive command (declined = not executed); fails open with an advisory note when the client cannot prompt |
| `approvalPatterns` / `approvalExemptPatterns` | empty | User regexes widening / exempting the destructive set (exemptions win); `$global` defaults, per-connection overrides |
| `allowedLocalPaths` / `allowedRemotePaths` | empty | Path whitelist for file transfers |
| `localPathMode` | `cwd` | Local path restriction: `cwd` (process working directory + `allowedLocalPaths`) / `list` (only `allowedLocalPaths`) / `any` (unrestricted) |
| `commandLogSize` | 20 | Recent-display window size (0 disables the audit log) |
| `commandLogDir` | `.ssh-mcp-logs` | Audit log directory (`<dir>/<connection-name>.jsonl`) |
| `commandLogOnlySuccess` | false | Show only successful commands in the window (disk always records failures) |
| `commandLogFlushMs` | 5000 | Audit flush policy: max ms a record waits in the buffer; 0 = write-through |
| `sftpConcurrency` / `sftpChunkSize` | 16 / 32768 | In-flight requests per file and chunk size |
| `sftpFileConcurrency` | 4 | Files moved at once by a directory transfer (1 = strictly sequential); pays off for many small files on a high-latency link; `$global` sets the default |
| `sftpVerifyChecksum` | true | sha256 comparison on both sides after a single-file transfer; disabling it saves one full remote read of the file, at the cost of the content check (directory transfers already skip the single-file checksum) |
| `sftpDedicatedConn` | false | Run SFTP over a dedicated SSH connection (lazy, 5 min idle recycle, liveness-probed before reuse); enable when a shadow container / session-scoped overlay in front of sshd loses transferred files; `$global` sets the default |
| `historyFromLog` | false | Supplements sparse remote `history` output (<10 entries) from the MCP command log (tail-deduped); mechanism in [FEATURES](FEATURES.en.md#mechanism-notes) |
| `utf8Sanitize` | true | Output encoding is never assumed UTF-8: invalid sequences become `\xNN` byte escapes flagged `nonUtf8`; set false to keep raw bytes |
| `algorithms` | empty | kex/cipher/serverHostKey/hmac negotiation |
| `hostKeyCheck` | `accept-new` | Host key verification: `accept-new` (accept after unknown record) / `strict` (reject unknown) / `none` (no verification); automatically creates `known_hosts` file and its directory (e.g., `~/.ssh`) if they don't exist |
| `knownHostsFile` | `~/.ssh/known_hosts` | known_hosts file for host key verification |
| `keepaliveIntervalMs` / `keepaliveCountMax` | 10000 / 3 | SSH keepalive |
| `commandTimeoutMs` / `connectionTimeoutMs` / `sftpTimeoutMs` | 120000 / 30000 / 300000 | Various timeouts (`commandTimeoutMs` is the foreground command timeout; `sftpTimeoutMs` is a no-progress timeout, each progress refresh restarts the timer) |
| `maxOutputBytes` | 10485760 | Maximum combined stdout+stderr output per single command, 0 means unlimited; hitting it sets `truncated`/`clippedBytes` and states that the output is incomplete |
| `outputCompressLight` / `outputCompressThreshold` | true / 4096 | Head/tail compression for large output and its threshold; compressed output names the omitted line count and says the content is incomplete |
| `outputSpillThreshold` / `outputSpillDir` | 8192 / `.ssh-mcp-out` | Spill output to a local directory at this threshold (`-1` disables) |
| `stripAnsi` | true | Strip ANSI escape sequences from output (false preserves colors/progress bars) |
| `scriptCleanup` | true | Remove the remote `/tmp` file that an `execute-command` `scriptFile` was staged to, once it finishes (a run interrupted by timeout or the output limit may leave it behind) |
| `scriptInterpreter` | `sh` | Interpreter for a staged script that has no shebang (a script with a shebang runs directly and ignores this) |
| `lintMode` | `warn` | Structural check before a command is sent: `off` skips it / `warn` reports and still runs / `strict` refuses error-class findings (`COMMAND_LINT_FAILED`); applies to `cmdString` only (argv/scriptFile are exempt), config file only |
| `keepAliveMs` | 600000 | How long the connection is kept after a command, in milliseconds; `-1` closes it right after each command. Named sessions hold their connection regardless |
| `autoProbe` | true | Probe the remote `base64`/`jq` once after connecting; the result shows as the `capabilities=` segment in `list-servers`. The probe is a plain one-line command, so turn it off on connections whose remote side records every command (bastion, session recording) |
| `backgroundLogMaxBytes` | 8388608 | Byte cap on a background session's remote log; past it the oldest bytes are trimmed and counted (`droppedBytes` in `session action=read`), while the first 64 KiB of the output are kept aside. `-1` keeps every byte |
| `resultFormat` | `text` | Result shape: `text` (default, sectioned plain text) or `json` (structured JSON). Applies to successful execute-command results, `session action=read`/`list` and `list-servers` |
| `commandTemplate` | empty | Command wrapper template (`<command>` / `<quotedCommand>`) |
| `pty` | false | Allocate a pseudo-terminal in exec mode (off by default; enable for interactive commands) |
| `redactSecrets` | false | Output redaction (password/token/Bearer/PEM); enabling has scan cost |
| `resultFormat` | text | Successful command result rendering: text = sectioned plain text (real newlines), json = structured JSON |
| `tryKeyboard` | false | Keyboard-interactive authentication (the server's 2FA challenge pops up in the MCP client; unattended setups fall back to `SSH_MCP_2FA_CODE`) |
| `via` | empty | Jump-host connection name (OpenSSH ProxyJump): the target is reached through the jump host's `direct-tcpip` tunnel; the target must not also set `proxy` |

> Strings in the configuration file support `${environment-variable-name}` references, so credentials can be stored in environment variables without touching disk.

## Authentication Methods

- Password: `password`
- Private Key: `privateKey` (may have `passphrase`, or use environment variable `SSH_MCP_PASSPHRASE`)
- ssh-agent: `agent` (Unix socket path; on Windows use `pageant` for Pageant)
- Keyboard-interactive: `tryKeyboard: true`. The password prompt is answered automatically from the configured password; OTP/verification-code prompts **pop up live in the MCP client that triggered the connection** (answer within 2 minutes; only the session that started the dial is prompted — other connections are never blocked). If the prompt is declined/cancelled/timed out, or the client cannot elicit at all (background jobs, headless runs), it automatically falls back to the `SSH_MCP_2FA_CODE` environment variable; if that is unset too, empty answers are sent and the server simply rejects — a failed authentication fails only that connection, never wedges the process or other connections.

## Jump Hosts (via, OpenSSH ProxyJump)

When an internal machine is unreachable from this machine and only reachable through a jump host, point the target connection's `via` at the jump-host connection. This machine never opens a TCP connection to the target directly — the SSH handshake rides a `direct-tcpip` tunnel through the jump host (standard OpenSSH ProxyJump semantics):

```json
{
  "bastion": {
    "host": "bastion.example.com",
    "username": "deploy",
    "agent": "pageant"
  },
  "internal-db": {
    "host": "10.0.1.50",
    "username": "dbadmin",
    "privateKey": "~/.ssh/db_key",
    "via": "bastion"
  }
}
```

- The jump host and the target **authenticate independently** (their own password/key/2FA, their own hostKeyCheck/known_hosts); no agent forwarding.
- The jump host is an ordinary connection: it may carry a `proxy` and may be shared by several `via` targets. The **target itself must not also set `proxy`/`socksProxy`** (ambiguous semantics — validation rejects it). Self references and cycles (A→B→A) fail at startup.
- `list-servers` labels the target with `via=bastion` so an agent knows it is not directly reachable.
- Commands, shell mode, SFTP, and background jobs all behave exactly as on a direct connection — the tunnel only replaces the TCP layer. A dead jump host produces a clear jump-host error, and the next call reconnects the jump host before re-establishing the tunnel.
- `ProxyJump <alias>` in `~/.ssh/config` is parsed and mapped to `via` (a single alias only; `a,b` multi-hop, `user@host`, and `host:port` are rejected). The alias must already be declared as a connection in the configuration — nothing is created implicitly.
- **Compatible with standard OpenSSH jump hosts; NOT compatible with appliances that only offer an interactive menu / disallow exec** — for those, keep using the existing `transportMode: shell` + `commandTemplate`.

Difference from `commandTemplate: "ssh jumphost <quotedCommand>"`: the template logs into the jump host and runs a *remote* ssh there — timeouts, process-group kills, and SFTP semantics all happen on the jump host, and the target never sees local keys. `via` is a true tunnel: the target gets its own authentication and session semantics. Pick one per scenario; do not mix them.

## Audit Log (tamper-evident command history, hash chain)

The audit log is enabled by default (disable with `"commandLogSize": 0`). Each connection's execution records are **appended** as JSON lines to `<commandLogDir>/<connection-name>.jsonl` (Unix file mode 0600). Every line carries the previous line's SHA-256 (`prevHash`), forming a **hash chain**: modifying any line afterwards makes verification fail at that line. **Command output is never recorded** — only who executed what, when, and with what outcome:

```json
{"v":1,"type":"command","ts":"2026-08-22T02:00:00.123Z","connection":"prod","tool":"execute-command","command":"ls -la /tmp","approval":"auto","exitCode":0,"success":true,"prevHash":"000...000","hash":"e3b0..."}
```

- Recorded: `execute-command` (success/failure/timeout/killed; approval refusals are logged with `approval=declined`), `file-transfer` (one summary per call, remote path only), named `session` open/close
- Flush policy `commandLogFlushMs` (ms; global CLI `--command-log-flush-ms`): default 5000 = records buffer and flush in batches at 5 s / 32 records / 256 KiB, whichever comes first; graceful shutdown flushes the tail (a hard crash can lose the last ≤5 s). `0` = write-through: each record is on disk before the command result returns (strictest; ~13 µs per record, ~8 µs batched — negligible next to the SSH round trip). Tiered hardening guidance: [SECURITY.en.md](../SECURITY.en.md#hardening-levels)
- **Failed commands are always written to disk** (internal audit needs the failure trail); `commandLogOnlySuccess: true` only affects the `recent=` display window in `list-servers` (which keeps showing successful commands only)
- `commandLogSize` only controls the in-memory display window (default 20) loaded across restarts — it **no longer truncates the disk file**. Files rotate at 16 MiB or 10000 lines (whichever comes first) into `<connection>.jsonl.<UTC-timestamp>`; the new file starts with a `chain-start` record linking the old tail hash, so the chain never breaks across files
- Verify one file: `2native-ssh-mcp audit-verify --file .ssh-mcp-logs/prod.jsonl`; verify every connection (including rotated files): `2native-ssh-mcp audit-verify --config-file config.json`. Exit codes: 0=chain intact or no log, 2=broken chain/legacy format (stderr prints file, line number, expected and actual hashes), 1=program error
- Upgrade compatibility: a legacy `<connection-name>.log` found at startup is renamed to `<connection-name>.log.legacy` and a new chain starts (one stderr notice); old lines are never spliced into the new chain
- Global defaults: `--command-log-size <n>` / `--command-log-dir <dir>`; per-connection override: `commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`

## Security

- On startup, checks `--config-file` permissions (Unix: `chmod 600` for file / `chmod 700` for directory; Windows: restricts ACL modification permissions), can be skipped with `--allow-insecure-config-perms` or `$global.allowInsecureConfigPerms: true` in the configuration file (not recommended, development only)
- Output redaction is off by default (`redactSecrets: true` enables it per connection): Bearer tokens, PEM private key blocks, `password=`/`token=` patterns, etc.; when on, spilled files also contain redacted content
- On timeout or output limit exceeded, sends SIGTERM/SIGKILL to the remote process group by PID (exec; channel Signal is only a fallback) or Ctrl-C (shell/session)
- MCP tools are annotated with `readOnlyHint` / `destructiveHint`, allowing clients to restrict dangerous operations accordingly
- Optional destructive-command approval gate: `approvalMode: "ask-destructive"` (per connection or `$global`). The built-in classifier flags rm/reboot/mkfs-style commands and prompts via MCP elicitation first; `approvalPatterns` adds what *you* consider dangerous, `approvalExemptPatterns` exempts what you consider safe — gray areas are entirely yours, there is no un-overridable built-in denylist

See [SECURITY.en.md](../SECURITY.en.md) for details.

## Automatic Release Publishing

Pushing a **tag with a message** triggers GitHub Actions (`.github/workflows/release.yml`):

```bash
git tag -a v1.0.1 -m "Fixed xxx issue"
git push origin v1.0.1
```

The workflow will:
1. Cross-compile binaries for 6 platforms: Windows / Linux / macOS × amd64 / arm64 (`CGO_ENABLED=0`, version number injected as the tag name)
2. Generate an independent `.sha256` checksum file for each binary
3. Create a GitHub Release with **release notes = tag message** (reads the annotated tag object via GitHub API, avoids the issue where lightweight tags on runners fall back to commit messages), with all binaries, checksums, `.mcpb` bundles, and one CycloneDX SBOM (`2native-ssh-mcp.cdx.json`) attached
4. Publish SLSA provenance and SBOM attestations to the repo Attestations tab. Verify a downloaded file with:

```bash
gh attestation verify --repo daidaiJ/2native-ssh-mcp 2native-ssh-mcp-linux-amd64
```

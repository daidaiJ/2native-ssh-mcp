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
| `timeout` | | Timeout in milliseconds, default 30000; on timeout the remote process group is killed by PID |
| `pty` | | Allocate a pseudo-terminal, default `false` (enable for interactive commands; with `background=true`, wraps the job in `script` when the program requires a TTY) |
| `background` | | When `true`, the command is moved to a background session and immediately returns `sessionName`/`logPath`; poll with `session read` (use this for minute-scale jobs instead of raising timeout) |
| `keepAlive` | | Whether to keep the connection alive after execution, default `true` (named sessions ignore `false`) |
| `keepAliveDuration` | | Keep-alive duration in milliseconds, default 600000 (10 minutes) |

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
| `open` | Open a session; `background=true` + `cmdString` starts a background task (`pty=true` wraps the job in a TTY) |
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
- The `timeout` for foreground commands must be greater than the actual execution time (the default `commandTimeoutMs=30000` still applies); on timeout the remote process group is killed by PID (channel Signal is only a fallback), so timed-out commands do not leak remote processes
- Output ≥8KB (`outputSpillThreshold`, `-1` to disable) spills the full output to local `.ssh-mcp-out/` (`outputSpillDir` configurable, keeps the newest 32, Unix 0600); the result carries only a notice + short preview; Agent should Grep/Read that local file, not re-run the command remotely
- exec **does not allocate a PTY by default** (avoids docker/npm treating the session as interactive); enable it with connection `"pty": true` or the `execute-command` `pty` parameter

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
  --http-addr <host:port>          Default 127.0.0.1:8338
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
| `commandLogSize` | 20 | Number of command logs to retain (0 disables) |
| `commandLogDir` | `.ssh-mcp-logs` | Command log directory (`<dir>/<connection-name>.log`) |
| `commandLogOnlySuccess` | false | Only log successful commands |
| `sftpConcurrency` / `sftpChunkSize` | 16 / 32768 | SFTP concurrency and chunk size |
| `algorithms` | empty | kex/cipher/serverHostKey/hmac negotiation |
| `hostKeyCheck` | `accept-new` | Host key verification: `accept-new` (accept after unknown record) / `strict` (reject unknown) / `none` (no verification); automatically creates `known_hosts` file and its directory (e.g., `~/.ssh`) if they don't exist |
| `knownHostsFile` | `~/.ssh/known_hosts` | known_hosts file for host key verification |
| `keepaliveIntervalMs` / `keepaliveCountMax` | 10000 / 3 | SSH keepalive |
| `commandTimeoutMs` / `connectionTimeoutMs` / `sftpTimeoutMs` | 30000 / 30000 / 300000 | Various timeouts (`sftpTimeoutMs` is a no-progress timeout; each progress refresh restarts the timer) |
| `maxOutputBytes` | 10485760 | Maximum combined stdout+stderr output per single command, 0 means unlimited |
| `outputCompressLight` / `outputCompressThreshold` | true / 4096 | Compress large output at head/tail and threshold |
| `outputSpillThreshold` / `outputSpillDir` | 8192 / `.ssh-mcp-out` | Spill output to a local directory at this threshold (`-1` disables) |
| `stripAnsi` | true | Strip ANSI escape sequences from output (false preserves colors/progress bars) |
| `commandTemplate` | empty | Command wrapper template (`<command>` / `<quotedCommand>`) |
| `pty` | false | Allocate a pseudo-terminal in exec mode (off by default; enable for interactive commands) |
| `redactSecrets` | false | Output redaction (password/token/Bearer/PEM); enabling has scan cost |
| `tryKeyboard` | false | Keyboard-interactive authentication (2FA code via environment variable `SSH_MCP_2FA_CODE`) |

> Strings in the configuration file support `${environment-variable-name}` references, so credentials can be stored in environment variables without touching disk.

## Authentication Methods

- Password: `password`
- Private Key: `privateKey` (may have `passphrase`, or use environment variable `SSH_MCP_PASSPHRASE`)
- ssh-agent: `agent` (Unix socket path; on Windows use `pageant` for Pageant)
- Keyboard-interactive: `tryKeyboard: true`, password prompt uses configured password, OTP prompt uses `SSH_MCP_2FA_CODE`

## Command Logging (remote execution history, keeps last N entries)

Command logging is enabled by default, keeping 20 entries per connection (disable with `"commandLogSize": 0`). Executed commands are appended to `<commandLogDir>/<connection-name>.log` in JSON lines format, keeping only the most recent N entries, and persists across restarts:

```json
{"timestamp":"2026-08-22T10:00:00+08:00","command":"ls -la /tmp","exitCode":0,"success":true}
```

- Global defaults: `--command-log-size <n>` / `--command-log-dir <dir>` (applies to connections without individual configuration)
- Per-connection override: `commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`
- Use `commandLogOnlySuccess: true` to only log successful commands, avoiding noise from probe commands

## Security

- On startup, checks `--config-file` permissions (Unix: `chmod 600` for file / `chmod 700` for directory; Windows: restricts ACL modification permissions), can be skipped with `--allow-insecure-config-perms` or `$global.allowInsecureConfigPerms: true` in the configuration file (not recommended, development only)
- Output redaction is off by default (`redactSecrets: true` enables it per connection): Bearer tokens, PEM private key blocks, `password=`/`token=` patterns, etc.; when on, spilled files also contain redacted content
- On timeout or output limit exceeded, sends SIGTERM/SIGKILL to the remote process group by PID (exec; channel Signal is only a fallback) or Ctrl-C (shell/session)
- MCP tools are annotated with `readOnlyHint` / `destructiveHint`, allowing clients to restrict dangerous operations accordingly
- Optional destructive-command approval gate: `approvalMode: "ask-destructive"` (per connection or `$global`). The built-in classifier flags rm/reboot/mkfs-style commands and prompts via MCP elicitation first; `approvalPatterns` adds what *you* consider dangerous, `approvalExemptPatterns` exempts what you consider safe — gray areas are entirely yours, there is no un-overridable built-in denylist

See [SECURITY.md](../SECURITY.md) for details.

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

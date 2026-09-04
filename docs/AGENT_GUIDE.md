> [中文版](AGENT_GUIDE.zh-CN.md)

# 2native-ssh-mcp — Agent Guide

SSH-based MCP server (Go). Remote command execution + file transfer as MCP tools. stdio or streamable HTTP. **Credentials never belong in MCP client config args** — see Secure setup.

> Human-readable version: [HUMAN_GUIDE.md](HUMAN_GUIDE.md)

## Tools

### execute-command
| Param | Type | Req | Notes |
|---|---|---|---|
| `cmdString` | string | ✅ | Command |
| `directory` | string | | Working dir (`cd -- '<dir>' && ...`) |
| `connectionName` | string | | Name or alias from list-servers |
| `timeout` | number | | ms; overrides `commandTimeoutMs` (default 30000) |
| `keepAlive` | boolean | | Keep connection after command (default **true**) |
| `keepAliveDuration` | number | | ms; idle TTL after command (default **600000** = 10 min) |

- Whitelist/blacklist regexes per connection; rejected → `COMMAND_VALIDATION_FAILED`.
- **Non-zero exit is a normal result, not an error** — read `exitCode` from the text (`[exit code] N`). Errors are reserved for validation/connect failures, `COMMAND_TIMEOUT`, `OUTPUT_LIMIT_EXCEEDED`, and `SSH_CONNECTION_LOST`.
- `SSH_CONNECTION_LOST` (retriable=false): the connection dropped mid-command; the remote process may still be running. **Do not replay blindly** — the error JSON carries partial `stdout`/`stderr` and `replaySafe: false`.
- Output cap `maxOutputBytes` (default 10 MB, **stdout+stderr combined**) → `OUTPUT_LIMIT_EXCEEDED`; timeout → `COMMAND_TIMEOUT`. For timeout/lost/limit the error message stays short; partial output is in the same result's `stdout`/`stderr` fields.
- **Light compress** (default): outputs ≥ `outputCompressThreshold` (4096 B) get head/tail lines + dedup; disable with `"outputCompressLight": false`. See `skills/2native-ssh-mcp-agent` for agent-side habits.
- **ANSI stripped by default**: colors/progress escapes are removed from all output (exec, shell, background reads); disable per connection with `"stripAnsi": false`.
- Connections **lazy**; after command kept alive per keepAlive policy, idle expiry closes. `keepAlive: false` closes immediately.
- Executed commands appended to connection's command log file (if configured) — **without output**.

### file-transfer
One tool, `action` param. Progress via `notifications/progress` when client sends `_meta.progressToken` (throttled ~100 ms, final 100% always).

| Param | Type | Req | Notes |
|---|---|---|---|
| `action` | string | ✅ | `upload` \| `download` |
| `localPath` | string | ✅ | Under `localPathMode` scope (default: cwd + `allowedLocalPaths`; `list` = only `allowedLocalPaths`; `any` = unrestricted) |
| `remotePath` | string | ✅ | Absolute POSIX; under `allowedRemotePaths` if set |
| `connectionName` | string | | Name or alias from list-servers |
| `force` | boolean | | Skip dedup/resume, full transfer |

- **Dedup**: destination matches (size+mtime) → skipped. **Resume**: partial destination → continue. Download = temp + atomic rename, stamps remote mtime.
- Growing source: tail appended after main copy.
- Concurrent SFTP (16×32 KB default; `sftpConcurrency`/`sftpChunkSize` per connection).
- Local path outside the `localPathMode` scope → `LOCAL_PATH_NOT_ALLOWED` with a scope message ("not within the allowed local paths for this connection"); a `..` escape is reported separately as "Path traversal rejected".

### list-servers
No args. Returns **servers** (metadata, status) and **active sessions**. Call first to pick `connectionName` or `sessionName`. **readOnly**.

### session
One tool, `action` param. Exec-mode connections only.

| action | Required | Notes |
|---|---|---|
| `open` | `sessionName`, `connectionName` (new) | Idempotent; `background=true` + `cmdString` starts long-running job |
| `read` | `sessionName` | Poll background log; optional `maxBytes`, `offset` (negative/omitted = continue, `0` = re-read from start), `waitMs` (block up to 30s for new output or job exit) |
| `close` | `sessionName` | Stop background job, release shell (**idempotent** — closing an already-closed session succeeds) |
| `list` | — | All sessions; optional `connectionName` filter |

**Long tasks: `execute-command` with `background: true`** detaches the command via a background session and returns `sessionName` + `logPath` immediately (a random `bg-*` session is created if none given). Poll with `session action=read` (`waitMs` blocks for new output instead of returning empty). Equivalent: `session action=open` with `background=true` + `cmdString`. Add `pty: true` for jobs that require a TTY (wrapped in `script`, exit code preserved). Do NOT `nohup ... &` or `setsid` through a foreground `execute-command` — those die with the exec channel. Background jobs are started detached (no PTY, new session) and **survive connection drops**; after a drop the session shows `disconnected=true` and `read`/`execute-command` reconnect automatically. Only `action=close` kills the remote job.

**Finished background jobs keep their session** (and remote log) for 60 min — `close` it to release. `read` returns `logPath` (remote log) and `exitCode` once the job finished; `offset=0` re-reads from the start. Sessions live in memory only: a stdio process exit loses them (the remote log survives at `logPath`); a resident HTTP daemon keeps them across conversations. Re-opening `background=true` on a finished session is rejected with the `logPath` — `close` first, or read the old log. **If the connection is down, `close` cannot confirm the remote job stopped**: the session stays listed with `orphaned=true` and the close returns a retriable error — retry `close` after the connection is back. BG log/pid/exit files use a one-time random suffix (`/tmp/.2native-ssh-mcp-<name>-<id>.log`); always use the returned `logPath`, never guess a fixed path.

### execute-command (with optional session)
| Param | Notes |
|---|---|
| `sessionName` | When set, runs in named session (CWD persists); use `session action=open` first |
| … | Same as before: `cmdString`, `directory`, `connectionName`, `timeout`, `keepAlive` |

**Background workflow:**
```
session(action=open, sessionName=logs, connectionName=dev, background=true, cmdString="tail -f /var/log/syslog")
session(action=read, sessionName=logs)          # repeat until running=false
session(action=close, sessionName=logs)
```

**Stateful workflow:**
```
session(action=open, sessionName=deploy, connectionName=dev)
execute-command(sessionName=deploy, cmdString="cd /app && git pull")
execute-command(sessionName=deploy, cmdString="npm ci")
session(action=close, sessionName=deploy)
```

## Config (per connection, JSON)

```json
{
  "$global": {
    "allowInsecureConfigPerms": true
  },
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root",
    "password": "${SSH_MCP_PASSWORD}",
    "description": "Development environment jump host",
    "business": "Order/Payment integration testing",
    "aliases": ["dev-box", "development"],
    "notes": "Read-only primarily; avoid heavy queries during peak hours",
    "commandWhitelist": ["^ls ", "^cat "],
    "commandBlacklist": ["rm -rf"],
    "allowedLocalPaths": ["C:/data"],
    "allowedRemotePaths": ["/tmp", "/home"],
    "localPathMode": "cwd",                    // or "list" / "any"
    "transportMode": "exec",                   // or "shell" (bastion)
    "commandLogSize": 50, "commandLogDir": "logs", "commandLogOnlySuccess": true,
    "sftpConcurrency": 16, "sftpChunkSize": 32768,
    "algorithms": {"kex": ["curve25519-sha256"], "cipher": ["aes128-ctr"]},
    "hostKeyCheck": "accept-new",            // or "strict" / "none"
    "knownHostsFile": "~/.ssh/known_hosts",
    "keepaliveIntervalMs": 10000, "keepaliveCountMax": 3,
    "commandTimeoutMs": 30000, "connectionTimeoutMs": 30000, "sftpTimeoutMs": 300000,
    "maxOutputBytes": 10485760,
    "outputCompressLight": true,
    "outputCompressThreshold": 4096,
    "outputSpillThreshold": 8192,          // full output at/above this goes to a local file; -1 disables
    "outputSpillDir": ".ssh-mcp-out",      // local spill directory (~ is expanded)
    "stripAnsi": true,
    "redactSecrets": false,                // opt-in: masks password=/token=/Bearer/PEM (~200ms per MiB of secret-bearing output)
    "commandTemplate": "sudo -n <quotedCommand>",
    "pty": false, "tryKeyboard": false
  }
}
```

Auth: password | privateKey (+passphrase, `SSH_MCP_PASSPHRASE` env) | agent (`SSH_AUTH_SOCK`, `"pageant"` on Windows) | keyboard-interactive (`tryKeyboard`, OTP via `SSH_MCP_2FA_CODE`). Proxy: `proxy` (socks5/http/https) or legacy `socksProxy`.

## Usage rules

- **Foreground `timeout` must exceed the real runtime** — the default `commandTimeoutMs=30000` still applies; a long build needs `timeout` ≥ its duration or it will be cut off with `COMMAND_TIMEOUT`. On timeout the remote process group is killed by PID (channel Signal is only a fallback — OpenSSH often ignores it), so timed-out commands do not leak remote processes.
- **exec runs without a PTY by default** — a PTY makes tools like docker/npm behave as if interactive and can cause SIGHUP issues on long tasks, so it is opt-in: connection `"pty": true` or the `execute-command` `pty` parameter. Background jobs never use a PTY regardless.
- **Non-zero exit is a normal result** — check `exitCode`; do not treat `[exit code] 1` as a transport failure.
- **`SSH_CONNECTION_LOST` is not replay-safe** — the command may have partially executed; inspect the partial `stdout` before deciding to retry.

## Secure setup (pick one — do NOT put credentials in MCP client args)

1. **Config file + env refs (recommended)**: `config.json` with `"password": "${SSH_MCP_PASSWORD}"`; set the env var in the MCP client or system. MCP config only has `--config-file`.
2. **Config file, locked perms**: plaintext `config.json`, `chmod 600` (Windows: restrict ACLs). MCP config only has `--config-file`.
3. **~/.ssh/config alias + agent**: `--host <alias>` only; credentials live in the user's SSH config/agent. No secrets anywhere in MCP config.

CLI-arg credentials (`--password` etc.) print a stderr warning — they're visible in process lists.

Config file permission check on startup (Unix `0600`/`0700`; Windows ACL). Override dev-only: `--allow-insecure-config-perms`, or `"$global": {"allowInsecureConfigPerms": true}` inside the config file (object format only).

`file-transfer` accepts **directories**: the tree transfers recursively (remote parents auto-created, per-file atomic upload, failures collected in `failed`), and single-file transfers are sha256-verified (`unverified` when the remote lacks sha256sum).

Output is **spilled to a local file** when stdout+stderr reach `outputSpillThreshold` (default 8 KiB): the result then only carries a short notice, the absolute path, size/line counts and a ~12-line preview — Read/Grep **that local file**, do not re-run the command or `cat` it remotely. Below the threshold, output ≥4 KiB is light-compressed. See [SECURITY.md](../SECURITY.md).

Secret redaction (Bearer tokens, PEM blocks, `password=`/`token=` lines) is **opt-in** via `"redactSecrets": true` — it is off by default because scanning secret-bearing output is expensive. When it is on, spilled files contain redacted content only.

## Deployment

### stdio (MCP client spawns)
```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "/path/to/2native-ssh-mcp",
      "args": ["--config-file", "/path/to/config.json"]
    }
  }
}
```

### HTTP daemon
```bash
2native-ssh-mcp start --config-file config.json --http-addr 127.0.0.1:8338
2native-ssh-mcp status | stop | kill
2native-ssh-mcp install   # Windows autostart (startup folder)
```
MCP client: `{"mcpServers": {"2native-ssh-mcp": {"url": "http://127.0.0.1:8338/mcp"}}}`

**/mcp auth**: loopback listen (default) needs no token. Non-loopback listen **requires** a Bearer token or the server refuses to start — sources in order: `--http-token`, env `SSH_MCP_HTTP_TOKEN`, `$global.httpToken` (config file). With a token, the client must send `Authorization: Bearer <token>` on every `/mcp` request (401 otherwise). Admin API is exempt (loopback + Host check only).

Daemon semantics: refcount (first `start` = owner lease, never expires; extra `start` = guest lease with a **15 min TTL** refreshed by any authenticated `/mcp` request — all guest leases at once, regardless of which `start` created them; `stop` −1 removes a guest first, then the owner; exits at 0), PID file, admin API `/__admin/{health,status,refcount,shutdown}` (loopback client + loopback `Host` header required, `"name":"2native-ssh-mcp"` verified; `shutdown` additionally requires POST + `Content-Type: application/json`). The daemon never exits from idling — only count 0, `kill`, or a signal.

## Release workflow

Push an **annotated tag** → GitHub Actions builds 6 binaries (win/linux/darwin × amd64/arm64) + SHA256SUMS, creates a release whose notes = **tag message**:
```bash
git tag -a v1.0.1 -m "fix: ..."
git push origin v1.0.1
```

## Gotchas

- **Local WSL**: treat as a normal Linux SSH target (sshd in the distro, `host: 127.0.0.1`). Do **not** launch this server via `"command": "wsl"`. `file-transfer` `localPath` is the MCP process OS — on Windows use `D:\\...`, never `/mnt/c/...` or `\\\\wsl$\\...`; keep Linux builds under `/home`, not `/mnt/c` (9P). If Windows already binds :22, put WSL sshd on another port. NAT: WSL `127.0.0.1` ≠ Windows loopback (HTTP daemon on one side is unreachable from the other). Full recipe: [HUMAN_GUIDE.md](HUMAN_GUIDE.md#connecting-to-local-wsl).
- Logs → **stderr** only (stdio protocol on stdout).
- Host keys verified against `known_hosts` by default (`hostKeyCheck: accept-new`): first contact is recorded (file + `~/.ssh` created automatically), later key changes fail with `SSH_HOST_KEY_MISMATCH` (retriable=false). `strict` rejects unknown hosts (`SSH_HOST_KEY_UNKNOWN`); `none` disables verification. A rekeyed server needs its stale `known_hosts` line removed (or `hostKeyCheck: none`).
- Shell mode serializes commands per connection; no SFTP in shell mode. **`transportMode: shell` requires a POSIX `sh`-compatible interactive shell** (relies on `PS1`, `stty`, `printf`, `export`) — csh/tcsh/fish bastions must use `exec` + `commandTemplate` instead.
- Command log files: `<dir>/<name>.log`, JSON lines, bounded, survive restarts.
- MCP endpoint path: `/mcp`.
- No SSH zlib compression (x/crypto/ssh limitation); use TCP keepalive + SFTP concurrency for slow links.

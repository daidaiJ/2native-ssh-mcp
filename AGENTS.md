# ssh-mcp-server-go — Agent Guide

SSH-based MCP server (Go). Exposes remote command execution + file transfer as MCP tools. stdio or streamable HTTP.

## Tools

### execute-command
Run a command on a connected server. Returns combined stdout+stderr.

| Param | Type | Req | Notes |
|---|---|---|---|
| `cmdString` | string | ✅ | Command to execute |
| `directory` | string | | Working dir (wrapped as `cd -- '<dir>' && ...`) |
| `connectionName` | string | | Default `default` |
| `timeout` | number | | ms; overrides connection `commandTimeoutMs` (default 30000) |
| `keepAlive` | boolean | | Keep connection after command (default **true**) |
| `keepAliveDuration` | number | | ms; idle TTL after command (default **600000** = 10 min) |

Behavior:
- Whitelist/blacklist regexes enforced per connection (see config). Rejected → `COMMAND_VALIDATION_FAILED`.
- Non-zero exit → error result `{code, message, retriable}` with stdout/stderr/exit code.
- Output capped at `maxOutputBytes` (default 10 MB) → `OUTPUT_LIMIT_EXCEEDED`.
- Timeout → `COMMAND_TIMEOUT` (retriable).
- Connections are **lazy**: first call connects. After the call, connection stays alive per keepAlive policy; idle expiry closes it. `keepAlive: false` closes immediately.
- Every executed command is appended to the connection's command log file (if configured) — **without output**.

### file-transfer
Upload or download. **One tool, `action` param.** Progress via MCP `notifications/progress` when client sends `_meta.progressToken` (throttled ~100 ms, final 100% always).

| Param | Type | Req | Notes |
|---|---|---|---|
| `action` | string | ✅ | `upload` \| `download` |
| `localPath` | string | ✅ | Local path (must be under cwd or `allowedLocalPaths`) |
| `remotePath` | string | ✅ | Absolute POSIX path (must be under `allowedRemotePaths` if configured) |
| `connectionName` | string | | Default `default` |
| `force` | boolean | | Skip dedup/resume, full transfer (default false) |

Behavior:
- **Dedup**: destination already matches (size + mtime) → skipped, result says so.
- **Resume**: partial destination (smaller) → continues from existing size. `force: true` overrides.
- Download writes temp file + rename (atomic); stamps remote mtime for future dedup.
- Growing source files: tail appended after main copy.
- Concurrent SFTP (16 workers × 32 KB chunks by default; `sftpConcurrency`/`sftpChunkSize` per connection) — matters on high-latency links.
- Result text: bytes, elapsed, MB/s, percent; `resumed from N bytes` or `skipped` variants.

### list-servers
No args. Summary + raw JSON per connection: name, host, port, username, connected, status (hostname/os/uptime/mem/disk collected ~1 s after connect).

## Config (per connection, JSON)

```json
{
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root",
    "password": "…",                       // or privateKey + passphrase, or agent
    "commandWhitelist": ["^ls ", "^cat "], // regexes; empty = allow all
    "commandBlacklist": ["rm -rf"],
    "allowedLocalPaths": ["C:/data"],       // upload/download local roots (cwd always allowed)
    "allowedRemotePaths": ["/tmp", "/home"],// upload/download remote roots (empty = any)
    "transportMode": "exec",                // or "shell" (bastion hosts)
    "commandLogSize": 50,                   // keep last 50 commands in log file
    "commandLogDir": "logs",                // per-connection override of --command-log-dir
    "commandLogOnlySuccess": true,          // skip failed commands (less noise)
    "sftpConcurrency": 16, "sftpChunkSize": 32768,
    "algorithms": {"kex": ["curve25519-sha256"], "cipher": ["aes128-ctr"]},
    "keepaliveIntervalMs": 10000, "keepaliveCountMax": 3,
    "commandTimeoutMs": 30000, "connectionTimeoutMs": 30000, "sftpTimeoutMs": 300000,
    "maxOutputBytes": 10485760,             // 0 = unlimited
    "commandTemplate": "sudo -n <quotedCommand>",
    "pty": true, "tryKeyboard": false
  }
}
```

Auth: password | privateKey (+passphrase, `SSH_MCP_PASSPHRASE` env) | agent (`SSH_AUTH_SOCK`, or `"pageant"` on Windows) | keyboard-interactive (`tryKeyboard`, OTP via `SSH_MCP_2FA_CODE` env). Proxy: `proxy` (socks5/http/https) or legacy `socksProxy`.

## CLI

```
ssh-mcp-server-go [command] [options] [host port username password]
```

- No command → **stdio** (spawned by MCP clients). `--transport http` → foreground HTTP.
- `start` → HTTP daemon (refcount; `--http-addr` default 127.0.0.1:8338). `stop`/`kill`/`status` manage it (port read from PID file). `install`/`uninstall` → Windows autostart (startup folder). `version`, `help`.
- Single host: `-h/-p/-u/-w/-k/-P/-a`; alias lookup in `~/.ssh/config` (Include supported).
- `--config-file <json>` (array or object), `--ssh '<json>'` repeatable, or legacy `--ssh name=dev,host=…,user=…`.
- `--command-log-size N --command-log-dir <dir> --command-log-only-success` — global log defaults.
- `--pre-connect` — connect all servers at startup (off by default; connections are lazy).

## Gotchas

- Logs go to **stderr** (never stdout — stdio protocol).
- Host keys are not verified (`InsecureIgnoreHostKey`, same as reference impl).
- Shell transport mode serializes commands per connection; SFTP is unavailable in shell mode.
- Command log files: `<dir>/<name>.log`, JSON lines, bounded to last N, survive restarts.
- HTTP daemon admin API: `/__admin/{health,status,refcount,shutdown}`, loopback-only, responses carry `"name":"ssh-mcp-server"`.
- MCP endpoint path: `/mcp`.
- No SSH zlib compression (x/crypto/ssh limitation); use TCP keepalive + SFTP concurrency for slow links.
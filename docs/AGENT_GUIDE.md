# ssh-mcp-server-go — Agent Guide

SSH-based MCP server (Go). Remote command execution + file transfer as MCP tools. stdio or streamable HTTP. **Credentials never belong in MCP client config args** — see Secure setup.

> Human-readable version: [HUMAN_GUIDE.md](HUMAN_GUIDE.md)

## Tools

### execute-command
| Param | Type | Req | Notes |
|---|---|---|---|
| `cmdString` | string | ✅ | Command |
| `directory` | string | | Working dir (`cd -- '<dir>' && ...`) |
| `connectionName` | string | | Default `default` |
| `timeout` | number | | ms; overrides `commandTimeoutMs` (default 30000) |
| `keepAlive` | boolean | | Keep connection after command (default **true**) |
| `keepAliveDuration` | number | | ms; idle TTL after command (default **600000** = 10 min) |

- Whitelist/blacklist regexes per connection; rejected → `COMMAND_VALIDATION_FAILED`.
- Non-zero exit → error `{code, message, retriable}` with output + exit code.
- Output cap `maxOutputBytes` (default 10 MB) → `OUTPUT_LIMIT_EXCEEDED`; timeout → `COMMAND_TIMEOUT`.
- Connections **lazy**; after command kept alive per keepAlive policy, idle expiry closes. `keepAlive: false` closes immediately.
- Executed commands appended to connection's command log file (if configured) — **without output**.

### file-transfer
One tool, `action` param. Progress via `notifications/progress` when client sends `_meta.progressToken` (throttled ~100 ms, final 100% always).

| Param | Type | Req | Notes |
|---|---|---|---|
| `action` | string | ✅ | `upload` \| `download` |
| `localPath` | string | ✅ | Under cwd or `allowedLocalPaths` |
| `remotePath` | string | ✅ | Absolute POSIX; under `allowedRemotePaths` if set |
| `connectionName` | string | | Default `default` |
| `force` | boolean | | Skip dedup/resume, full transfer |

- **Dedup**: destination matches (size+mtime) → skipped. **Resume**: partial destination → continue. Download = temp + atomic rename, stamps remote mtime.
- Growing source: tail appended after main copy.
- Concurrent SFTP (16×32 KB default; `sftpConcurrency`/`sftpChunkSize` per connection).

### list-servers
No args. Per connection: name, host, port, username, connected, status (hostname/os/uptime/mem/disk).

## Config (per connection, JSON)

```json
{
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root",
    "password": "${SSH_MCP_PASSWORD}",        // env ref — see Secure setup
    "commandWhitelist": ["^ls ", "^cat "],
    "commandBlacklist": ["rm -rf"],
    "allowedLocalPaths": ["C:/data"],
    "allowedRemotePaths": ["/tmp", "/home"],
    "transportMode": "exec",                   // or "shell" (bastion)
    "commandLogSize": 50, "commandLogDir": "logs", "commandLogOnlySuccess": true,
    "sftpConcurrency": 16, "sftpChunkSize": 32768,
    "algorithms": {"kex": ["curve25519-sha256"], "cipher": ["aes128-ctr"]},
    "keepaliveIntervalMs": 10000, "keepaliveCountMax": 3,
    "commandTimeoutMs": 30000, "connectionTimeoutMs": 30000, "sftpTimeoutMs": 300000,
    "maxOutputBytes": 10485760,                // 0 = unlimited
    "commandTemplate": "sudo -n <quotedCommand>",
    "pty": true, "tryKeyboard": false
  }
}
```

Auth: password | privateKey (+passphrase, `SSH_MCP_PASSPHRASE` env) | agent (`SSH_AUTH_SOCK`, `"pageant"` on Windows) | keyboard-interactive (`tryKeyboard`, OTP via `SSH_MCP_2FA_CODE`). Proxy: `proxy` (socks5/http/https) or legacy `socksProxy`.

## Secure setup (pick one — do NOT put credentials in MCP client args)

1. **Config file + env refs (recommended)**: `config.json` with `"password": "${SSH_MCP_PASSWORD}"`; set the env var in the MCP client or system. MCP config only has `--config-file`.
2. **Config file, locked perms**: plaintext `config.json`, `chmod 600` (Windows: restrict ACLs). MCP config only has `--config-file`.
3. **~/.ssh/config alias + agent**: `--host <alias>` only; credentials live in the user's SSH config/agent. No secrets anywhere in MCP config.

CLI-arg credentials (`--password` etc.) print a stderr warning — they're visible in process lists.

## Deployment

### stdio (MCP client spawns)
```json
{
  "mcpServers": {
    "ssh-mcp-server": {
      "command": "/path/to/ssh-mcp-server-go",
      "args": ["--config-file", "/path/to/config.json"]
    }
  }
}
```

### HTTP daemon
```bash
ssh-mcp-server-go start --config-file config.json --http-addr 127.0.0.1:8338
ssh-mcp-server-go status | stop | kill
ssh-mcp-server-go install   # Windows autostart (startup folder)
```
MCP client: `{"mcpServers": {"ssh-mcp-server": {"url": "http://127.0.0.1:8338/mcp"}}}`

Daemon semantics: refcount (start +1, stop −1, exits at 0), PID file, admin API `/__admin/{health,status,refcount,shutdown}` (loopback-only, `"name":"ssh-mcp-server"` verified).

## Release workflow

Push an **annotated tag** → GitHub Actions builds 6 binaries (win/linux/darwin × amd64/arm64) + SHA256SUMS, creates a release whose notes = **tag message**:
```bash
git tag -a v1.0.1 -m "fix: ..."
git push origin v1.0.1
```

## Gotchas

- Logs → **stderr** only (stdio protocol on stdout).
- Host keys not verified (`InsecureIgnoreHostKey`, same as reference impl).
- Shell mode serializes commands per connection; no SFTP in shell mode.
- Command log files: `<dir>/<name>.log`, JSON lines, bounded, survive restarts.
- MCP endpoint path: `/mcp`.
- No SSH zlib compression (x/crypto/ssh limitation); use TCP keepalive + SFTP concurrency for slow links.
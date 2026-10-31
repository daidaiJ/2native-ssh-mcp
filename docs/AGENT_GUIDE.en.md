> [中文版](AGENT_GUIDE.md)

# 2native-ssh-mcp — Agent Guide

SSH-based MCP server (Go). Remote command execution + file transfer as MCP tools. stdio or streamable HTTP. **Credentials never belong in MCP client config args** — see Secure setup.

> Human-readable version: [HUMAN_GUIDE.md](HUMAN_GUIDE.md)

## Tools

### execute-command
| Param | Type | Req | Notes |
|---|---|---|---|
| `cmdString` | string | ✅\* | Shell command; in argv mode, the program itself. `cmdString` or `scriptFile` — at least one |
| `args` | string[] | | argv mode: the server quotes each argument byte-for-byte before handing it to the program, with no shell parsing (spaces, quotes, `$`, backslashes and `*` arrive literally); mutually exclusive with `scriptFile` |
| `scriptFile` | string | | Local script: read, staged to remote `/tmp` over SFTP, then run (its shebang decides the interpreter, otherwise `scriptInterpreter`, default `sh`); mutually exclusive with `cmdString`/`args` |
| `directory` | string | | Working dir (`cd -- '<dir>' && ...`); the only way to set the CWD in argv mode |
| `connectionName` | string | | Name or alias from list-servers; **required** unless a connection named `default` exists |
| `timeout` | number | | ms; overrides `commandTimeoutMs` (default 120000) |

`lintMode`, `scriptCleanup`, `pty` and the keep-alive window are **connection settings** (see the config table in [HUMAN_GUIDE](HUMAN_GUIDE.en.md)), not tool parameters: they are configured once and stay put, so passing them per call only costs tokens.

\* `cmdString` is no longer schema-required: it is empty on a `scriptFile`-only call, and a call with neither is rejected (`COMMAND_VALIDATION_FAILED`).

- Whitelist/blacklist regexes per connection; rejected → `COMMAND_VALIDATION_FAILED` (in argv mode they match the command line built from the **original** arguments, so quoting cannot hide a match).
- **Non-zero exit is a normal result, not an error** — read `exitCode` from the text (`[exit code] N`). Errors are reserved for validation/connect failures, `COMMAND_TIMEOUT`, `OUTPUT_LIMIT_EXCEEDED`, and `SSH_CONNECTION_LOST`.
- **Successful results render as sectioned text by default** — real newlines: `stdout`, then `[stderr]`, `[exit code] N`, and a spill notice when output was persisted. Set `"resultFormat": "json"` on a connection to switch successful results to the structured CommandResult JSON; error paths always stay JSON.
- **Omitting `connectionName` (routed via the `default` connection) adds a summary of the connection that actually served the call**: in text format the first line is `connection=default host=<ip> port=<port> aliases=<...>` (aliases omitted when none); in JSON format the result gains `connection`/`host`/`port`/`aliases` fields; background job starts carry the same line, and so do **failed results** (a background start failure, a failed file transfer, a rejected argument set). Calls that named a `connectionName` or a `sessionName` do not repeat it (`sessionName` makes `connectionName` ignored, and the session already names the host).
- `SSH_CONNECTION_LOST` (retriable=false): the connection dropped mid-command; the remote process may still be running. **Do not replay blindly** — the error JSON carries partial `stdout`/`stderr` and `replaySafe: false`.
- Output cap `maxOutputBytes` (default 10 MB, **stdout+stderr combined**) → `OUTPUT_LIMIT_EXCEEDED`; timeout → `COMMAND_TIMEOUT`. For timeout/lost/limit the error message stays short; partial output is in the same result's `stdout`/`stderr` fields.
- **Light compress** (default): outputs ≥ `outputCompressThreshold` (4096 B) get head/tail lines + dedup; disable with `"outputCompressLight": false`. See `skills/2native-ssh-mcp-agent` for agent-side habits.
- **ANSI stripped by default**: colors/progress escapes are removed from all output (exec, shell, background reads); disable per connection with `"stripAnsi": false`.
- Connections **lazy**; after command kept alive per the connection's `keepAliveMs` (default 10 minutes; `-1` closes right after each command), idle expiry closes.
- Executed commands appended to the connection's audit log file (tamper-evident hash-chained JSONL; the `recent=` last 3 shown by `list-servers` come from the in-memory window, default 20, successful-only when `commandLogOnlySuccess: true`) — **without output**.
- **Same-batch tool calls run concurrently — no ordering guarantee.** Dependent operations (upload → verify → restart) must be separate sequential tool calls. See `skills/2native-ssh-mcp-agent` → "Operational traps" (pgrep self-match, sudo without TTY, overwriting running binaries).

#### Escaping: three ways to hand over a command

A quoting mistake is **silent** — the outer shell eats `$1`, the command still exits 0 with plausible output. Pick a form instead of hand-quoting:

| Form | Use when | What the remote runs | Audit record holds |
|---|---|---|---|
| `cmdString` | short command, clean arguments | the text as written | the text as written |
| `cmdString` + `args` | any argument contains a space/quote/`$`/`\`/`*`, or comes from a variable | every element single-quoted by the server | the **readable equivalent** of the same command line (only the quoting a shell needs, e.g. `grep -rn 'foo bar' /etc`) |
| `scriptFile` | anything past two or three lines | `sh /tmp/.2native-ssh-mcp-<name>-<rand>.sh` (the file itself when it has a shebang) | the script text itself, headed by a comment naming the source, the staged path and the interpreter |

- The whitelist/blacklist, the destructive classifier, the approval prompt and the audit record all see the **readable equivalent** (the original argv, the script body) — never the quoted execution text, never a base64 wrapper. An auditor can copy the record and re-run it.
- `scriptFile` is staged at `/tmp/.2native-ssh-mcp-<basename>-<rand>.sh` (mode 0700, written as `<target>.part` then renamed). The upload runs after the approval gate and the whitelist/blacklist, so a refused call never leaves a remote file. The connection's `scriptCleanup: false` keeps it for debugging — it is a **remote** file.
- Background mode behaves the same way: the cleanup is written as `(exit $rc)` in a subshell so the script's own exit code survives (a bare `exit` would skip the background starter's `echo $? > exitfile`).
- Lint runs on `cmdString` (shell mode) only: argv mode is already quoted per argument and has no shell syntax left, and a script's syntax errors are reported by the remote interpreter itself, with a line number.

### file-transfer
One tool, `action` param. Progress via `notifications/progress` when client sends `_meta.progressToken` (throttled ~100 ms, final 100% always).

| Param | Type | Req | Notes |
|---|---|---|---|
| `action` | string | ✅ | `upload` \| `download` |
| `localPath` | string | ✅ | Under `localPathMode` scope (default: cwd + `allowedLocalPaths`; `list` = only `allowedLocalPaths`; `any` = unrestricted) |
| `remotePath` | string | ✅ | Absolute POSIX; under `allowedRemotePaths` if set |
| `connectionName` | string | | Name or alias from list-servers; **required** unless a connection named `default` exists |
| `force` | boolean | | Skip dedup/resume, full transfer |

- **Dedup**: destination matches (size+mtime) → skipped. **Resume**: partial destination → continue. Download = temp + atomic rename, stamps remote mtime.
- Growing source: tail appended after main copy.
- Concurrent SFTP (16×32 KB default; `sftpConcurrency`/`sftpChunkSize` per connection).
- `sftpDedicatedConn: true` (per connection or `$global`): SFTP runs over its own SSH connection. Gateways with a shadow container / session-scoped overlay in front of sshd discard files written over the reused shell session — enable this when transferred files vanish; lazy creation, 5 min idle recycle, liveness-probed before reuse.
- When resuming context on a reconnect, run `history` first: with `historyFromLog` enabled on the connection, a sparse remote listing (<10 entries; usually 0 in exec mode) is automatically supplemented from the MCP command log (tail-deduped, counted in the `historySupplemented` field).
- **Omitting `connectionName` (routed via the `default` connection)** marks the success response like execute-command: the first text line is `connection=default host=<ip> port=<port> aliases=<...>`, and the JSON gains `connection`/`host`/`port`/`aliases` fields; a failed transfer carries them too.
- `\xNN` escapes and a `nonUtf8: true` flag in the output mean the remote output was not valid UTF-8 (e.g. GBK); convert remotely with `iconv -f gbk` for readable text.
- Local path outside the `localPathMode` scope → `LOCAL_PATH_NOT_ALLOWED` with a scope message ("not within the allowed local paths for this connection"); a `..` escape is reported separately as "Path traversal rejected".

### list-servers
No args. Returns **servers** (metadata, status, last few recorded commands) and **active sessions**. Call first to pick `connectionName` or `sessionName` — the recent-command history also helps resume context from a previous session. **readOnly**.

Once a connection is established (within about a second) each entry gains a `capabilities=base64,jq` segment: the probe result for the remote `base64` and `jq`. The probe itself is a **readable one-line command** with no encoding. `$global.autoProbe: false` (or `autoProbe: false` on the connection) disables it and reports `capabilities=not probed (autoProbe=false)`. Use it to pick the tool that exists on that host when a payload needs to move past a quoting layer or JSON needs building, instead of failing once to find out.

### session
One tool, `action` param. Exec-mode connections only.

| action | Required | Notes |
|---|---|---|
| `open` | `sessionName`, `connectionName` (new) | Idempotent; `background=true` + `cmdString` starts long-running job |
| `read` | `sessionName` | Poll background log; optional `maxBytes`, `offset` (negative/omitted = continue, `0` = re-read from start), `waitMs` (block up to 30s for new output or job exit) |
| `close` | `sessionName` | Stop background job, release shell (**idempotent** — closing an already-closed session succeeds) |
| `list` | — | All sessions; optional `connectionName` filter |

Session summaries and the `open`/`list` responses carry the actual host (`host=<ip>:<port>`, plus `host`/`port`/`aliases` JSON fields) — even a session on the default connection shows which machine it lives on.

**Long tasks: `execute-command` with `background: true`** detaches the command via a background session and returns `sessionName` + `logPath` immediately (a random `bg-*` session is created if none given). Poll with `session action=read` (`waitMs` blocks for new output instead of returning empty). Equivalent: `session action=open` with `background=true` + `cmdString`. Add the connection's `"pty": true` for jobs that require a TTY (wrapped in `script`, exit code preserved). Do NOT `nohup ... &` or `setsid` through a foreground `execute-command` — those die with the exec channel. Background jobs are started detached (no PTY, new session) and **survive connection drops**; after a drop the session shows `disconnected=true` and `read`/`execute-command` reconnect automatically. Only `action=close` kills the remote job.

**Finished background jobs keep their session** (and remote log) for 60 min — `close` it to release. `read` returns `logPath` (remote log) and `exitCode` once the job finished; `offset=0` re-reads from the start. Sessions live in memory only: a stdio process exit loses them (the remote log survives at `logPath`); a resident HTTP daemon keeps them across conversations. Re-opening `background=true` on a finished session is rejected with the `logPath` — `close` first, or read the old log. **If the connection is down, `close` cannot confirm the remote job stopped**: the session stays listed with `orphaned=true` and the close returns a retriable error — retry `close` after the connection is back. BG log/pid/exit files use a one-time random suffix (`/tmp/.2native-ssh-mcp-<name>-<id>.log`); always use the returned `logPath`, never guess a fixed path.

### execute-command (with optional session)
| Param | Notes |
|---|---|
| `sessionName` | When set, runs in named session (CWD persists); use `session action=open` first |
| … | Same as before: `cmdString`, `args`, `scriptFile`, `directory`, `connectionName`, `timeout`, `background` |

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
    "approvalMode": "auto",                  // 或 "ask-destructive"：破坏性命令先经 MCP elicitation 征询人类
    "approvalPatterns": ["terraform\s+destroy"],   // 用户追加"要问"的 regex
    "approvalExemptPatterns": [],            // 用户豁免"不问"的 regex，优先级最高
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
    "commandTimeoutMs": 120000, "connectionTimeoutMs": 30000, "sftpTimeoutMs": 300000,
    "maxOutputBytes": 10485760,
    "outputCompressLight": true,
    "outputCompressThreshold": 4096,
    "outputSpillThreshold": 8192,          // full output at/above this goes to a local file; -1 disables
    "outputSpillDir": ".ssh-mcp-out",      // local spill directory (~ is expanded)
    "stripAnsi": true,
    "scriptCleanup": true,                 // remove the staged scriptFile after it runs (an interrupted run may leave it)
    "scriptInterpreter": "sh",             // interpreter for a staged script that has no shebang
    "lintMode": "warn",                    // off / warn (default) / strict: structural check before the command is sent (config file only)
    "keepAliveMs": 600000,                 // keep the connection after a command (default 600000 = 10 min; -1 = close right after each command)
    "autoProbe": true,                     // probe remote base64/jq once after connecting (shown by list-servers); turn off when the remote logs every command
    "redactSecrets": false,                // opt-in: masks password=/token=/Bearer/PEM (~200ms per MiB of secret-bearing output)
    "commandTemplate": "sudo -n <quotedCommand>",
    "pty": false, "tryKeyboard": false
  }
}
```

Auth: password | privateKey (+passphrase, `SSH_MCP_PASSPHRASE` env) | agent (`SSH_AUTH_SOCK`, `"pageant"` on Windows) | keyboard-interactive (`tryKeyboard`: the 2FA challenge pops an MCP elicitation on the session that triggered the dial; if nobody answers it falls back to `SSH_MCP_2FA_CODE`, and failing that the server rejects authentication — it never wedges). Proxy: `proxy` (socks5/http/https) or legacy `socksProxy`.

## Usage rules

- **For `tryKeyboard` connections, the 2FA prompt only appears on the call that establishes the connection**: while the connection stays warm (idle disconnect defaults to 10 minutes) reuse does not prompt again; after an idle reconnect or a dropped link the next execute-command prompts again. Unattended/background flows (heartbeats, automatic reconnect after idle expiry) have no prompt context and go straight to the `SSH_MCP_2FA_CODE` fallback — keep that variable set for long-running tasks.
- **Connections shown as `via=<name>` in list-servers are not directly reachable**: they ride the jump host's direct-tcpip tunnel (OpenSSH ProxyJump); no direct TCP path to the target ever exists. A dead jump host yields an error naming the jump host — retrying reconnects it first. This is a different mechanism from `commandTemplate: "ssh jumphost …"` (a remote ssh exec on the jump host); do not mix them.

- **Foreground `timeout` must exceed the real runtime** — the connection's `commandTimeoutMs` (default 120000) applies otherwise; a longer build needs `timeout` ≥ its duration or it will be cut off with `COMMAND_TIMEOUT`. On timeout the remote process group is killed by PID (channel Signal is only a fallback — OpenSSH often ignores it), so timed-out commands do not leak remote processes.
- **exec runs without a PTY by default** — a PTY makes tools like docker/npm behave as if interactive and can cause SIGHUP issues on long tasks, so it is opt-in via the connection's `"pty": true`. Background jobs never use a PTY unless that setting is on.
- **Non-zero exit is a normal result** — check `exitCode`; do not treat `[exit code] 1` as a transport failure.
- **Connections with `approvalMode: "ask-destructive"`**: `list-servers` marks `approval=ask-destructive` and whether this client supports elicitation. When the destructive classifier matches, a prompt asks the user (it shows the connection name and the actual host:port) — declining/cancelling skips execution and returns `"executed": false`; do not retry. If the client does not support elicitation the command runs anyway (fail-open) with a trailing note — ask the user before destructive commands.
- **`SSH_CONNECTION_LOST` is not replay-safe** — the command may have partially executed; inspect the partial `stdout` before deciding to retry.

## Secure setup (pick one — do NOT put credentials in MCP client args)

1. **Config file + env refs (recommended)**: `config.json` with `"password": "${SSH_MCP_PASSWORD}"`; set the env var in the MCP client or system. MCP config only has `--config-file`.
2. **Config file, locked perms**: plaintext `config.json`, `chmod 600` (Windows: restrict ACLs). MCP config only has `--config-file`.
3. **~/.ssh/config alias + agent**: `--host <alias>` only; credentials live in the user's SSH config/agent. No secrets anywhere in MCP config.

CLI-arg credentials (`--password` etc.) print a stderr warning — they're visible in process lists.

Config file permission check on startup (Unix `0600`/`0700`; Windows ACL). Override dev-only: `--allow-insecure-config-perms`, or `"$global": {"allowInsecureConfigPerms": true}` inside the config file (object format only).

`file-transfer` accepts **directories**: the tree transfers recursively (remote parents auto-created, per-file atomic upload, failures collected in `failed`), and single-file transfers are sha256-verified (`unverified` when the remote lacks sha256sum).

Output is **spilled to a local file** when stdout+stderr reach `outputSpillThreshold` (default 8 KiB): the result then only carries a short notice, the absolute path, size/line counts and a ~12-line preview — Read/Grep **that local file**, do not re-run the command or `cat` it remotely. Below the threshold, output ≥4 KiB is light-compressed. See [SECURITY.en.md](../SECURITY.en.md).

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

**Listen address**: sources in order: `--http-addr`, then `$global.httpAddr` in the config file, then `127.0.0.1:8338`.

Daemon semantics: refcount (first `start` = owner lease, never expires; extra `start` = guest lease with a **15 min TTL** refreshed by any authenticated `/mcp` request — all guest leases at once, regardless of which `start` created them; `stop` −1 removes a guest first, then the owner; exits at 0), PID file, admin API `/__admin/{health,status,refcount,shutdown}` (loopback client + loopback `Host` header required, `"name":"2native-ssh-mcp"` verified; `shutdown` additionally requires POST + `Content-Type: application/json`). The daemon never exits from idling — only count 0, `kill`, or a signal.

### Hot update (upgrade the binary without losing config)

**HTTP daemon** — clients auto-reconnect after the restart:
```bash
# 1. build the new binary (version stamped)
go build -o 2native-ssh-mcp.new.exe -ldflags "-X main.version=vX.Y.Z" .
# 2. graceful stop — refcount drains, server exits
2native-ssh-mcp stop
# 3. swap binaries (keep the old one as .exe~)
mv 2native-ssh-mcp.exe 2native-ssh-mcp.exe~ && mv 2native-ssh-mcp.new.exe 2native-ssh-mcp.exe
# 4. restart hidden — `start` is a blocking owner-lease command, never foreground it
wscript autostart.vbs                 # Windows (WMI launch with DETACHED_PROCESS: no console window); elsewhere: nohup 2native-ssh-mcp start ... &
# 5. verify
2native-ssh-mcp status && curl -s http://127.0.0.1:8338/__admin/health
```

**stdio** — the client owns the process, so the swap takes effect when the client respawns it:
```bash
go build -o 2native-ssh-mcp.new.exe -ldflags "-X main.version=vX.Y.Z" .
mv 2native-ssh-mcp.exe 2native-ssh-mcp.exe~ && mv 2native-ssh-mcp.new.exe 2native-ssh-mcp.exe
# Windows: a running exe cannot be deleted but CAN be renamed — the swap above works while running
```
Then restart the MCP server from the client (restart the session / reconnect the MCP server); each client process runs its own server. Verify with a test tool call through the client.

Gotchas: the generated `autostart.vbs` passes no port — the daemon takes its listen address from `$global.httpAddr` in the config file (`--http-addr` wins when given, `127.0.0.1:8338` when neither is set), so an `install` run no longer drops a hand-added port. It starts the daemon through WMI with a `Win32_ProcessStartup` carrying `DETACHED_PROCESS`, so the process has **no console window at all** — plain WMI creation is not enough (its console window is created shown, and the default terminal, Windows Terminal, displays it). `config.json` lives in the deploy dir and is never touched by an upgrade. If `stop` hangs on guest leases, `kill` is the hard fallback.

## Release workflow

Push an **annotated tag** → GitHub Actions builds 6 binaries (win/linux/darwin × amd64/arm64) + SHA256SUMS, creates a release whose notes = **tag message**:
```bash
git tag -a v1.0.1 -m "fix: ..."
git push origin v1.0.1
```

## Gotchas

- **Local WSL**: treat as a normal Linux SSH target (sshd in the distro, `host: 127.0.0.1`). Do **not** launch this server via `"command": "wsl"`. `file-transfer` `localPath` is the MCP process OS — on Windows use `D:\\...`, never `/mnt/c/...` or `\\\\wsl$\\...`; keep Linux builds under `/home`, not `/mnt/c` (9P). If Windows already binds :22, put WSL sshd on another port. NAT: WSL `127.0.0.1` ≠ Windows loopback (HTTP daemon on one side is unreachable from the other). Full recipe: [HUMAN_GUIDE.md](HUMAN_GUIDE.en.md#connecting-to-local-wsl).
- Logs → **stderr** only (stdio protocol on stdout).
- Host keys verified against `known_hosts` by default (`hostKeyCheck: accept-new`): first contact is recorded (file + `~/.ssh` created automatically), later key changes fail with `SSH_HOST_KEY_MISMATCH` (retriable=false). `strict` rejects unknown hosts (`SSH_HOST_KEY_UNKNOWN`); `none` disables verification. A rekeyed server needs its stale `known_hosts` line removed (or `hostKeyCheck: none`).
- Shell mode serializes commands per connection; no SFTP in shell mode. **`transportMode: shell` requires a POSIX `sh`-compatible interactive shell** (relies on `PS1`, `stty`, `printf`, `export`) — csh/tcsh/fish bastions must use `exec` + `commandTemplate` instead.
- Audit log files: `<dir>/<name>.jsonl`, append-only hash-chained JSON lines, rotated at 16 MiB / 10000 lines, survive restarts; verify with `2native-ssh-mcp audit-verify --file <path>`. The `recent=` window in `list-servers` comes from memory (last `commandLogSize`, failures filtered when `commandLogOnlySuccess`), not from the file tail.
- MCP endpoint path: `/mcp`.
- No SSH zlib compression (x/crypto/ssh limitation); use TCP keepalive + SFTP concurrency for slow links.

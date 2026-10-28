> [English](AGENT_GUIDE.en.md)

# 2native-ssh-mcp — Agent Guide

SSH-based MCP server (Go). Remote command execution + file transfer as MCP tools. stdio or streamable HTTP. **Credentials never belong in MCP client config args** — see Secure setup.

> Human-readable version: [HUMAN_GUIDE.md](HUMAN_GUIDE.md)

## Tools

### execute-command
| Param | Type | Req | Notes |
|---|---|---|---|
| `cmdString` | string | ✅\* | shell 命令；argv 模式下是程序本身。`cmdString` 与 `scriptFile` 至少给一个 |
| `args` | string[] | | argv 模式：每个参数由服务端逐字引用后交给程序，不经 shell 解析（空格/引号/`$`/反斜杠/`*` 原样送达）；与 `scriptFile` 互斥 |
| `scriptFile` | string | | 本地脚本：读取后经 SFTP 暂存到远端 `/tmp` 再执行（有 shebang 直接运行，否则用 `scriptInterpreter`，默认 `sh`）；与 `cmdString`/`args` 互斥 |
| `scriptCleanup` | boolean | | 执行完删除暂存的脚本（默认取连接的 `scriptCleanup`，再默认 true；被超时/输出上限打断时可能残留） |
| `lintMode` | string | | 下发前的结构检查：`off` / `warn`（默认，报告且照常执行）/ `strict`（error 级 finding 直接拒绝，返回 `COMMAND_LINT_FAILED`）；不适用于 `args`/`scriptFile` |
| `directory` | string | | Working dir (`cd -- '<dir>' && ...`) |
| `connectionName` | string | | Name or alias from list-servers；**必填**，除非配置了名为 `default` 的连接 |
| `timeout` | number | | ms; overrides `commandTimeoutMs` (default 30000) |
| `keepAlive` | boolean | | Keep connection after command (default **true**) |
| `keepAliveDuration` | number | | ms; idle TTL after command (default **600000** = 10 min) |

\* `cmdString` 不再是 schema 必填项：`scriptFile` 单独调用时它为空，两个都不给会被拒（`COMMAND_VALIDATION_FAILED`）。

- Whitelist/blacklist regexes per connection; rejected → `COMMAND_VALIDATION_FAILED`（argv 模式按**原始参数**拼出的命令行匹配，不受引用影响）。
- **Non-zero exit is a normal result, not an error** — read `exitCode` from the text (`[exit code] N`). Errors are reserved for validation/connect failures, `COMMAND_TIMEOUT`, `OUTPUT_LIMIT_EXCEEDED`, and `SSH_CONNECTION_LOST`.
- **Successful results render as sectioned text by default** — real newlines: `stdout`, then `[stderr]`, `[exit code] N`, and a spill notice when output was persisted. Set `"resultFormat": "json"` on a connection to switch successful results to the structured CommandResult JSON; error paths always stay JSON.
- **省略 `connectionName`（走 `default` 连接）时，响应会带上实际命中的连接摘要**：文本格式首行为 `connection=default host=<ip> port=<port> aliases=<...>`（无别名则省略），JSON 格式则多出 `connection`/`host`/`port`/`aliases` 字段；后台任务启动响应同样带这一行，**失败结果**（后台启动失败、file-transfer 失败、参数校验失败）也带这些字段。传了 `connectionName` 或 `sessionName` 的调用不重复此信息（`sessionName` 生效时 `connectionName` 被忽略，会话自身已指明主机）。
- `SSH_CONNECTION_LOST` (retriable=false): the connection dropped mid-command; the remote process may still be running. **Do not replay blindly** — the error JSON carries partial `stdout`/`stderr` and `replaySafe: false`.
- Output cap `maxOutputBytes` (default 10 MB, **stdout+stderr combined**) → `OUTPUT_LIMIT_EXCEEDED`; timeout → `COMMAND_TIMEOUT`. For timeout/lost/limit the error message stays short; partial output is in the same result's `stdout`/`stderr` fields.
- **Light compress** (default): outputs ≥ `outputCompressThreshold` (4096 B) get head/tail lines + dedup; disable with `"outputCompressLight": false`. See `skills/2native-ssh-mcp-agent` for agent-side habits.
- **ANSI stripped by default**: colors/progress escapes are removed from all output (exec, shell, background reads); disable per connection with `"stripAnsi": false`.
- Connections **lazy**; after command kept alive per keepAlive policy, idle expiry closes. `keepAlive: false` closes immediately.
- 每条执行的命令都会追加到连接的审计日志文件（防篡改哈希链 JSONL；`list-servers` 展示的 `recent=` 最近 3 条来自内存窗口，默认 20 条，且 `commandLogOnlySuccess: true` 时只显示成功命令）——**不含输出**。
- **同批次的工具调用是并发执行的——没有顺序保证。** 有依赖关系的操作（上传 → 验证 → 重启）必须拆成多次顺序调用。详见 `skills/2native-ssh-mcp-agent` 的 "Operational traps" 一节（pgrep 自匹配、sudo 无 TTY、覆盖运行中的二进制）。

#### Escaping：三种交出命令的方式

引号写错是**静默**的——外层 shell 吃掉 `$1`，命令照样 exit 0、输出看着合理。别手写嵌套引号，选形态：

| 形态 | 什么时候用 | 远端实际执行 | 审计记录里存的是 |
|---|---|---|---|
| `cmdString` | 短命令、参数干净 | 原样文本 | 原样文本 |
| `cmdString` + `args` | 任一参数含空格/引号/`$`/`\`/`*`，或参数来自变量 | 服务端逐参数单引号引用 | 同一命令行的**可读等效**（只加必要的引号，如 `grep -rn 'foo bar' /etc`） |
| `scriptFile` | 超过两三行的脚本 | `sh /tmp/.2native-ssh-mcp-<name>-<rand>.sh`（有 shebang 时直接跑该文件） | 脚本正文本身，首行是注释头：来源、暂存路径、解释器 |

- 白/黑名单、破坏性分类器、审批弹窗、审计记录看的都是**可读等效命令**（argv 的原始参数、脚本正文），不是引用后的执行串，也没有任何 base64 包装——审计日志里能直接照抄重跑。
- `scriptFile` 暂存到 `/tmp/.2native-ssh-mcp-<basename>-<rand>.sh`（模式 0700，`<target>.part` + rename 原子落盘）；`scriptCleanup: false` 留档排查，注意它是**远端**文件。
- 后台模式同样适用：清理写成 `(exit $rc)` 子壳，保住脚本自身的退出码（裸 `exit` 会跳过后台 starter 的 `echo $? > exitfile`）。
- lint 只跑 `cmdString`（shell 模式）：argv 模式已经逐参数引用，不存在 shell 语法；脚本的语法错误由远端解释器自己报（带行号）。

### file-transfer
One tool, `action` param. Progress via `notifications/progress` when client sends `_meta.progressToken` (throttled ~100 ms, final 100% always).

| Param | Type | Req | Notes |
|---|---|---|---|
| `action` | string | ✅ | `upload` \| `download` |
| `localPath` | string | ✅ | Under `localPathMode` scope (default: cwd + `allowedLocalPaths`; `list` = only `allowedLocalPaths`; `any` = unrestricted) |
| `remotePath` | string | ✅ | Absolute POSIX; under `allowedRemotePaths` if set |
| `connectionName` | string | | Name or alias from list-servers；**必填**，除非配置了名为 `default` 的连接 |
| `force` | boolean | | Skip dedup/resume, full transfer |

- **Dedup**: destination matches (size+mtime) → skipped. **Resume**: partial destination → continue. Download = temp + atomic rename, stamps remote mtime.
- Growing source: tail appended after main copy.
- Concurrent SFTP (16×32 KB default; `sftpConcurrency`/`sftpChunkSize` per connection).
- `sftpDedicatedConn: true`（每连接或 `$global`）：SFTP 单开一条 SSH 连接。sshd 前有影子容器/会话级 overlay 的网关会丢弃复用 shell 会话写入的文件，传输后文件消失时开这个；连接懒创建、空闲 5 分钟回收、复用前探活。
- 重接会话恢复上下文时优先跑 `history`：连接开了 `historyFromLog` 的话，远程历史过少（<10 条，exec 模式通常为 0）会自动从 MCP 命令日志补齐（尾部去重，`historySupplemented` 字段计数）。
- **省略 `connectionName`（走 `default` 连接）时**，成功响应与 execute-command 相同：文本首行为 `connection=default host=<ip> port=<port> aliases=<...>`，JSON 里多 `connection`/`host`/`port`/`aliases` 字段；传输失败的结果也带这些字段。
- 输出里的 `\xNN` 转义和 `nonUtf8: true` 标记表示远程输出不是有效 UTF-8（如 GBK）；需要可读文本时远程 `iconv -f gbk` 转换。
- Local path outside the `localPathMode` scope → `LOCAL_PATH_NOT_ALLOWED` with a scope message ("not within the allowed local paths for this connection"); a `..` escape is reported separately as "Path traversal rejected".

### list-servers
无参数。返回**服务器**（元数据、状态、最近几条已记录命令）与**活动会话**。先调它来选 `connectionName` 或 `sessionName`——最近命令历史也有助于从上一个会话恢复上下文。**readOnly**。

连接建立后（约一秒内）会多一段 `capabilities=base64,jq`：远端 `base64` / `jq` 的探测结果（探测命令是**可读的单行命令**，不做任何编码；`$global.autoProbe: false` 或连接级 `autoProbe: false` 会关掉探测并显示 `capabilities=not probed (autoProbe=false)`）。用途：需要搬运含特殊字符的 payload 或拼 JSON 时，先看这台机器支持哪个，而不是失败一次再试。

### session
One tool, `action` param. Exec-mode connections only.

| action | Required | Notes |
|---|---|---|
| `open` | `sessionName`, `connectionName` (new) | Idempotent; `background=true` + `cmdString` starts long-running job |
| `read` | `sessionName` | Poll background log; optional `maxBytes`, `offset` (negative/omitted = continue, `0` = re-read from start), `waitMs` (block up to 30s for new output or job exit) |
| `close` | `sessionName` | Stop background job, release shell (**idempotent** — closing an already-closed session succeeds) |
| `list` | — | All sessions; optional `connectionName` filter |

会话摘要与 `open`/`list` 响应均带实际主机（`host=<ip>:<port>`，JSON 的 `host`/`port`/`aliases` 字段）——default 连接上的会话也能一眼看出在哪台机器。

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
    "commandTimeoutMs": 30000, "connectionTimeoutMs": 30000, "sftpTimeoutMs": 300000,
    "maxOutputBytes": 10485760,
    "outputCompressLight": true,
    "outputCompressThreshold": 4096,
    "outputSpillThreshold": 8192,          // full output at/above this goes to a local file; -1 disables
    "outputSpillDir": ".ssh-mcp-out",      // local spill directory (~ is expanded)
    "stripAnsi": true,
    "scriptCleanup": true,                 // scriptFile 跑完删除远端暂存脚本（被打断时可能残留）
    "scriptInterpreter": "sh",             // 无 shebang 的暂存脚本用哪个解释器
    "lintMode": "warn",                    // off / warn(默认) / strict：命令下发前的结构检查
    "autoProbe": true,                     // 连接建立后探一次远端 base64/jq（list-servers 展示）；远端会记录每条命令的连接可关掉
    "redactSecrets": false,                // opt-in: masks password=/token=/Bearer/PEM (~200ms per MiB of secret-bearing output)
    "commandTemplate": "sudo -n <quotedCommand>",
    "pty": false, "tryKeyboard": false
  }
}
```

Auth: password | privateKey (+passphrase, `SSH_MCP_PASSPHRASE` env) | agent (`SSH_AUTH_SOCK`, `"pageant"` on Windows) | keyboard-interactive (`tryKeyboard`：2FA 挑战实时弹窗到触发连接的 MCP 会话；无人应答时回退 `SSH_MCP_2FA_CODE`，再不行由服务器拒绝认证，不会挂死). Proxy: `proxy` (socks5/http/https) or legacy `socksProxy`.

## Usage rules

- **`tryKeyboard` 的连接，2FA 弹窗只出现在建立连接的那次调用**：连接保活期内（默认空闲 10 分钟断开）复用连接不会再弹；一旦空闲重连或连接掉线，下一次 execute-command 会重新弹窗。无人值守/后台任务（心跳、空闲过期后的自动重连）没有弹窗上下文，直接走 `SSH_MCP_2FA_CODE` 兜底——长任务建议该环境变量始终配置。
- **`list-servers` 显示 `via=<name>` 的连接不可直连**：它经跳板连接的 direct-tcpip 隧道（OpenSSH ProxyJump），本机到目标的 TCP 从不存在；跳板挂掉会报带跳板名的错误，重试即自动重连跳板。这与 `commandTemplate: "ssh jumphost …"`（远端再 exec 一层 ssh）是两条路，不要混用。

- **Foreground `timeout` must exceed the real runtime** — the default `commandTimeoutMs=30000` still applies; a long build needs `timeout` ≥ its duration or it will be cut off with `COMMAND_TIMEOUT`. On timeout the remote process group is killed by PID (channel Signal is only a fallback — OpenSSH often ignores it), so timed-out commands do not leak remote processes.
- **exec runs without a PTY by default** — a PTY makes tools like docker/npm behave as if interactive and can cause SIGHUP issues on long tasks, so it is opt-in: connection `"pty": true` or the `execute-command` `pty` parameter. Background jobs never use a PTY regardless.
- **Non-zero exit is a normal result** — check `exitCode`; do not treat `[exit code] 1` as a transport failure.
- **`approvalMode: "ask-destructive"` 的连接**：`list-servers` 会标注 `approval=ask-destructive` 与本客户端是否支持 elicitation。命中破坏性分类器时会弹窗征询用户（提示中带连接名与实际 host:port）——拒绝/取消则命令不执行，返回 `"executed": false`，不要重试；客户端不支持 elicitation 时命令照常执行（fail-open），结果尾部带提示，此时应先征询用户再发破坏性命令。
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
cscript //nologo autostart.vbs        # Windows; elsewhere: nohup 2native-ssh-mcp start ... &
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

Gotchas: `install` rewrites `autostart.vbs` and **drops a manually added `--http-addr`** — re-add it after running install. `config.json` lives in the deploy dir and is never touched by an upgrade. If `stop` hangs on guest leases, `kill` is the hard fallback.

## Release workflow

Push an **annotated tag** → GitHub Actions builds 6 binaries (win/linux/darwin × amd64/arm64) + SHA256SUMS, creates a release whose notes = **tag message**:
```bash
git tag -a v1.0.1 -m "fix: ..."
git push origin v1.0.1
```

## Gotchas

- **Local WSL**: treat as a normal Linux SSH target (sshd in the distro, `host: 127.0.0.1`). Do **not** launch this server via `"command": "wsl"`. `file-transfer` `localPath` is the MCP process OS — on Windows use `D:\\...`, never `/mnt/c/...` or `\\\\wsl$\\...`; keep Linux builds under `/home`, not `/mnt/c` (9P). If Windows already binds :22, put WSL sshd on another port. NAT: WSL `127.0.0.1` ≠ Windows loopback (HTTP daemon on one side is unreachable from the other). Full recipe: [HUMAN_GUIDE.md](HUMAN_GUIDE.md#连接本地-wsl).
- Logs → **stderr** only (stdio protocol on stdout).
- Host keys verified against `known_hosts` by default (`hostKeyCheck: accept-new`): first contact is recorded (file + `~/.ssh` created automatically), later key changes fail with `SSH_HOST_KEY_MISMATCH` (retriable=false). `strict` rejects unknown hosts (`SSH_HOST_KEY_UNKNOWN`); `none` disables verification. A rekeyed server needs its stale `known_hosts` line removed (or `hostKeyCheck: none`).
- Shell mode serializes commands per connection; no SFTP in shell mode. **`transportMode: shell` requires a POSIX `sh`-compatible interactive shell** (relies on `PS1`, `stty`, `printf`, `export`) — csh/tcsh/fish bastions must use `exec` + `commandTemplate` instead.
- Audit log files: `<dir>/<name>.jsonl`, append-only hash-chained JSON lines, rotated at 16 MiB / 10000 lines, survive restarts; verify with `2native-ssh-mcp audit-verify --file <path>`. The `recent=` window in `list-servers` comes from memory (last `commandLogSize`, failures filtered when `commandLogOnlySuccess`), not from the file tail.
- MCP endpoint path: `/mcp`.
- No SSH zlib compression (x/crypto/ssh limitation); use TCP keepalive + SFTP concurrency for slow links.

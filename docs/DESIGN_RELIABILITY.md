# 可靠性改进：连接保活、后台任务、Session 生命周期、错误信息

给 Auto 的实现规格。按任务顺序落地，不要一次改完再测。每个任务有明确的文件、做法、验收。

**不要做**：扩大工具数量、改认证/白名单模型、加 SSH zlib、改 HTTP daemon 协议。**不要引入** easyssh / goph / go-sshlib / fuchsia sshutil 作为依赖（理由见 §0.5）。

---

## 0.5 Go 生态调研：复用什么、不引进什么

`golang.org/x/crypto/ssh` **没有**内建 client keepalive、重连、后台作业。上层 wrapper 很多，但没有一个同时覆盖本项目已有的：多连接配置、白名单、SFTP 并发/断点、MCP session、shell marker 协议、Pageant/2FA。整体换库会倒退。策略是 **继续自管连接，把经过生产验证的小段逻辑抄进来**（BSD/Apache，注明出处）。

### 不要当依赖引进

| 库 | 为什么不引进 |
|---|---|
| [appleboy/easyssh-proxy](https://github.com/appleboy/easyssh-proxy)、[melbahja/goph](https://github.com/melbahja/goph) | 薄封装：一次 Dial + Run/SCP。无正确 keepalive、无会话/后台、SFTP 能力弱于现有 `pkg/sftp`。 |
| [blacknon/go-sshlib](https://github.com/blacknon/go-sshlib) | 交互式客户端（lssh）：ControlMaster/X11/TTY relay，体积和模型都不适合 MCP。可抄 session keepalive 片段。 |
| Fuchsia `tools/net/sshutil`、[aucloud/go-sshutil](https://github.com/aucloud/go-sshutil) | keepalive+重连写得好，但不是独立模块（拖整棵 Fuchsia 树）；fork 已停更。抄模式，不 `require`。 |
| [scylladb/go-sshtools](https://github.com/scylladb/go-sshtools) | keepalive 语义正确，**已 archived**。~40 行，抄过来即可。 |

### 必须对齐的现有包（已在 go.mod）

| 来源 | 用法 |
|---|---|
| `golang.org/x/crypto/ssh` | `*ssh.ExitError`（已有）；**补** `*ssh.ExitMissingError` —— 这就是 `remote command exited without exit status or exit signal` 的类型，不要用字符串匹配。 |
| `github.com/pkg/sftp` | `*sftp.StatusError` / `os.ErrNotExist` 区分远端父目录不存在 vs 权限（T6）。 |

### 按缺陷抄哪些实现

**D1 keepalive（T1）——三家结论一致：只看 `err`，不看 `ok`。**

OpenSSH 客户端 `server_alive_check`、Scylla `keepalive.go`、Fuchsia `conn.go`、go-sshlib `startSessionKeepAlive` 都是：

```go
_, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
return err // ok==false 仍算活着；有回复即可
```

`x/crypto/ssh` 客户端对**收到的** keepalive 也是 `Reply(false)`（[client.go](https://github.com/golang/crypto/blob/master/ssh/client.go)），与 OpenSSH 一致。本仓库把 `!ok` 当失败，是明确写反了。

另外抄 Fuchsia（[fxbug.dev/47698](https://bugs.fuchsia.dev/p/fuchsia/issues/detail?id=47698)）：`SendRequest` 在对端半死时会 **阻塞整个 SSH mux**（长命令看起来像挂死）。心跳必须：

1. 在独立 goroutine 里 `SendRequest`
2. 超过 `keepaliveIntervalMs`（或 interval+grace）仍无返回 → 记一次 unanswered
3. 同时只允许 **一个** in-flight keepalive，避免请求堆在 mux 上

可选加强（go-sshlib）：exec 进行中再对 **`ssh.Session`** 发 `session.SendRequest("keepalive@openssh.com", true, nil)`，只看 `err`。NAT 空闲断连时 channel 级 ping 比只 ping Client 更贴长 `docker build`。不要因此在失败时 `session.Close()` 拆掉整条连接——失败只计入 unanswered，由 T2 in-flight 策略决定是否拆。

**D1 断连分类（T5）——用标准库类型，Fuchsia 已这样映射。**

```go
var missing *ssh.ExitMissingError
if errors.As(waitErr, &missing) {
    // status=connection_lost, retriable=false, 带 partial stdout
}
```

Fuchsia 把 `*ssh.ExitMissingError` 当成 connection error，而不是“命令失败”。

**D1 握手超时**：Fuchsia `connectToSSH` 把 `ssh.NewClientConn` 放进 goroutine + `ctx`；底层 `net.Conn` 在握手期间设 deadline，完成后清掉（[golang/go#21941](https://github.com/golang/go/issues/21941) `ssh.Dial` 认证阶段可挂死）。本仓库已有 `ConnectionTimeoutMs`；T1 顺手确认 handshake 被该超时罩住，缺则补，不要另开任务。

**D1 探测死连接**：`client.Wait()` 在对端断电时经常 **不返回**（Go forum 共识）。保活 ping 是正路，不要用 Wait 当唯一 liveness。

**D2 后台作业——Go SSH 库都不做这件事。** 这是 sshd 进程组/PTY/SIGHUP 问题。应对齐 **Ansible async / 运维双 fork 惯例**，不是再找一个 SSH client：

- 无 PTY 的短 exec（本设计 T3）
- stdin 接到 `/dev/null`，stdout/stderr 进 log（否则 SSH 会一直等 fd）
- `( ( nohup cmd </dev/null >>log 2>&1 ) & )` 或 `setsid` + double-fork，作业离开 sshd 进程组
- starter 打出 PID 后立刻 exit；必要时 `sleep 1` 让调度器挂上作业（Ansible `raw` 的坑）
- 轮询 PID/log，等价于 Ansible `async_status`

**D3 重连**：Fuchsia `Reconnect` + disconnect listener 是“整条连接换新”。本项目需要 **逻辑 session / 远程 job 在断线后仍在**，自动重连不能杀远程进程。只借鉴“断线通知 + 下次操作再 dial”，不要把 Fuchsia Client 整段嵌进来。

---

## 0. 缺陷与根因（已对照代码）

### D1 长命令 / 空闲命令必断连（P0）

现象：`docker build`（timeout 300s）或 `sleep 30` → `remote command exited without exit status or exit signal`；session 内则 `Shell channel closed during command execution`。

根因叠了三层：

1. **心跳把正常回复当成死亡**（主因，约 30s 必杀）

   `internal/manager/manager.go` `startHeartbeat`：

   ```go
   ok, _, err := client.SendRequest("keepalive@openssh.com", true, nil)
   if err != nil || !ok { unanswered++ }
   ```

   OpenSSH **服务端**对未知 global request 回 `SSH_MSG_REQUEST_FAILURE`（`ok=false, err=nil`）。OpenSSH **客户端**只要有回复就算活着。本实现把 `!ok` 当失败。默认 `keepaliveIntervalMs=10000` × `keepaliveCountMax=3` → **约 30s 主动 `Disconnect`**。这正好解释 `sleep 30` 和长 build。

2. **执行中途 `Disconnect` 吞结果**

   `Disconnect` → `client.Close()` + `closeSessionsForConnection`。正在 `session.Wait()` 的 exec 拿不到 `exit-status`，于是 `Wait()` 返回 `remote command exited without exit status or exit signal`。`runExecCommand` 把这段当 `COMMAND_EXECUTION_ERROR`，**不带回已有 stdout**，且 `retriable=true`，Agent 不敢重放。

3. **执行期间不暂停空闲定时器 / 心跳可关连接**

   `Touch` 只在命令**结束后**调用。长命令进行中，连接 idle timer 与心跳仍可把连接拆掉。没有 in-flight 计数。

4. **Session 关闭与执行死锁（解释 “result was not recorded”）**

   `runShellScriptOnce` 持有 `shellSession.mu` 等待输出。`Disconnect` → `CloseSession` → `stopBackgroundProcess` → 再次 `runShellScriptOnce`（抢同一把锁）→ 死锁。MCP handler 永不返回，客户端报 `result was not recorded`。

### D2 后台任务无法存活（P0）

`startBackgroundCommand`（`internal/manager/background.go`）在 **带 PTY 的交互 shell** 里跑：

```sh
nohup sh -c '<cmd>' >> "$LOG" 2>&1 &
echo $! > "$PID"
```

问题：

- PTY 会话结束时内核/sshd 向会话进程组发 SIGHUP；`nohup`/`setsid` 若没 **double-fork 出 sshd 进程组并关掉 controlling tty**，子进程仍被收割。日志文件都不出现 = 进程从未活过 channel 关闭。
- 后台启动绑在 named session 的 PTY 上；连接一断 session 被删，PID 文件/日志一并不可达。
- `CloseSession` 会主动 `kill` 后台 PID（`stopBackgroundProcess`），连接空闲断开也会走到这条路径。
- Agent 自己 `nohup ... &` / `setsid` 走 exec 同样失败：默认 `GetPty()==true`，exec channel 带 PTY，sshd 关 channel 时清进程组。

### D3 Session 生命周期脆弱（P0，依赖 D1/D2）

`namedSession` 把逻辑会话（名字、cwd、后台任务）和物理 `*shellSession`（SSH channel）绑死。`Disconnect` 直接 `delete` session。没有重连、没有“断线但任务还在远程跑”的状态。

### D4 错误信息误导（P1）

| 现象 | 根因 |
|---|---|
| SFTP：`File upload failed: file does not exist`（本地文件明明在） | `sftp.Create` 在父目录不存在时返回 SSH_FX_NO_SUCH_FILE，未区分 local/remote/父目录 |
| `COMMAND_EXECUTION_ERROR` 把 stdout 塞进 `message`（`BUILD_RUNNING\r\n[exit code] 1`） | 非 0 退出走 `errorResult`；`runExecCommand` 成功路径返回 `""`，输出只在 error.message |
| `action=list` 报缺 `sessionName` | `session` 工具把 `sessionName` 标成 schema `Required`，MCP 在 handler 之前校验；`list` 不在 enum 里 |

---

## 1. 目标架构

### 1.1 连接层：保活 ≠ 拆连接

```
TCP keepalive (已有)
        +
SSH global request "keepalive@openssh.com" wantReply=true
        → 唯一失败条件：err != nil（无回复 / 连接坏）
        → ok==false 视为成功（与 OpenSSH 客户端一致）
        +
inFlight counter：>0 时禁止 Disconnect（idle timer 延期，心跳失败只打标）
        +
命令结束后再 Touch / 再允许拆连接
```

心跳 goroutine 失败达到阈值时：

- `inFlight==0`：现有逻辑，`Disconnect`
- `inFlight>0`：只 `markUnhealthy`，**不** `Disconnect`；让进行中的 `Wait()` 自己失败并带上 partial output；命令结束后再清连接

`Disconnect` / `CloseSession` **禁止**在持有 `shellSession.mu` 的路径上再进入 `runShellScriptOnce`。杀后台必须用**独立 exec channel**（无 PTY）。

### 1.2 命令结果：结构化，非 0 退出不是传输错误

统一内部类型（建议放 `internal/manager/result.go`）：

```go
type CommandResult struct {
    Stdout     string `json:"stdout"`
    Stderr     string `json:"stderr,omitempty"`
    ExitCode   int    `json:"exitCode"`    // 未知为 -1
    Signal     string `json:"signal,omitempty"`
    Status     string `json:"status"`      // ok | exited | timeout | cancelled | connection_lost | output_limit
    Partial    bool   `json:"partial,omitempty"`
    ReplaySafe bool   `json:"replaySafe"`  // false = 可能已执行，禁止盲目重放
}

func (r CommandResult) Text() string // 给 MCP 的人类可读正文
```

MCP 映射：

| Status | `IsError` | code |
|---|---|---|
| `ok` / `exited`（含 exit≠0） | false | — 正文含 stdout/stderr/`[exit code]` |
| `timeout` / `cancelled` / `output_limit` | true | 现有 code；**message 不含大段 stdout**；stdout 放在同一次 tool result 的 text 里（见下） |
| `connection_lost` | true | **新 code** `SSH_CONNECTION_LOST`；`retriable=false`；带 partial stdout |

实现建议：`errorResult` 扩展为可附带 `stdout` 字段，或 timeout/lost 时返回 **一条 text（结构化 JSON）+ IsError**，JSON 形状：

```json
{
  "code": "SSH_CONNECTION_LOST",
  "message": "SSH connection dropped during command; the remote process may still be running. Do not replay blindly.",
  "retriable": false,
  "stdout": "<partial>",
  "stderr": "",
  "exitCode": -1,
  "status": "connection_lost",
  "partial": true,
  "replaySafe": false
}
```

`Wait()` 若 `errors.Is(err, ...)` 或 `err.Error()` 包含 `exited without exit status` / `wait: EOF` / connection reset → 一律 `connection_lost`，不要再包装成含糊的 `COMMAND_EXECUTION_ERROR`。

`execute-command` 成功（含 exit≠0）：返回 `CommandResult.Text()`，**不要** `IsError`。Agent 看 `exitCode`。这是行为变化，必须改 `docs/AGENT_GUIDE.md`、`docs/HUMAN_GUIDE.md`、`skills/2native-ssh-mcp-agent/SKILL.md`。

### 1.3 后台任务：与 PTY session 解耦，远程可重附着

后台 job **不用** named PTY shell 启动。用 **exec、无 PTY、短生命周期 channel** 跑 starter，starter 立刻退出。

远程布局（沿用 `/tmp/.2native-ssh-mcp-<sessionName>.*`，可保持）：

| 文件 | 用途 |
|---|---|
| `.log` | stdout+stderr |
| `.pid` | 真正的作业 PID（setsid 后的子进程） |
| `.exit` | 作业结束后写入退出码（可选，read 时用来判断 running） |

Starter 必须满足（POSIX `sh`，不要 bash-only）：

1. 关掉 stdin；stdout/stderr 追加到 log
2. `setsid` 进入新 session（没有 controlling tty）
3. **double-fork** 或 `setsid` 后父进程立即退出，使作业不在 sshd 的进程组里
4. 写 PID 文件的是作业 PID，不是 starter
5. 打印一行 `__MCP_BG_STARTED__ pid=<n>` 后 **exit 0**（整个 exec 应在数秒内结束）
6. `trap '' HUP` 在作业一侧

参考脚本骨架（实现时以测试为准，可微调）：

```sh
LOG=...
PIDF=...
EXITF=...
CMD=...   # 已 quote
rm -f "$PIDF" "$EXITF"
touch "$LOG"
# 外层 fork：立刻返回
sh -c '
  trap "" HUP
  exec </dev/null
  # 新会话 + 后台
  setsid sh -c "
    trap \"\" HUP
    exec >>\"$1\" 2>&1
    $3
    echo \$? > \"$2\"
  " _ "$LOG" "$EXITF" "$CMD" &
  echo $! > "$PIDF"
' _
# 确认 PID 活着
PID=$(cat "$PIDF" 2>/dev/null)
if [ -n "$PID" ] && kill -0 "$PID" 2>/dev/null; then
  printf "__MCP_BG_STARTED__ pid=%s\n" "$PID"
  exit 0
fi
printf "__MCP_BG_FAILED__\n"
exit 1
```

注意：`$CMD` 不要再套一层会吞 `&` 的错误 quoting。使用已有 `shellQuote`。若配置了 `commandTemplate`，只套在作业 body 上，不要套在 starter 本身。

`ReadSessionOutput` / `stopBackgroundProcess` 同样走 **独立 exec、无 PTY**，不依赖 named shell 是否还在。

`session action=open background=true` 成功条件：远程 `kill -0 $PID` 为真 **且** 见到 `__MCP_BG_STARTED__`。只 echo 了 PID 但进程已死 → 返回明确错误，不要假装成功。

连接断开时：**不杀** 远程作业，**不删** 逻辑 session。重连后 `read` 用 pid/log 文件恢复 `running`。

### 1.4 逻辑 Session vs 物理 channel

```
namedSession {
  name, connectionKey, cwd
  shell *shellSession   // 可 nil；断线后清空，下次用再 ensureShell
  bg *bgJob             // 可独立于 shell 存在
  disconnected bool
}
```

规则：

- `Disconnect(conn)`：**不** `delete` sessions；把 `shell=nil`、`disconnected=true`。后台 job 元数据保留。
- `OpenSession`：已存在则 reuse；若 `disconnected`，`EnsureConnected` + `ensureShell` + `cd -- savedCwd`。
- `RunInSession`：若 shell 空或 closed → 重建 + 恢复 cwd，再执行。不要报 `session not found`（除非从未 open 或已显式 close）。
- 显式 `action=close`：SIGTERM 作业（独立 exec）→ 关 shell → **这时才**从 map 删除。
- 空闲 TTL：有 `bgRunning` 时用 `bgSessionIdleTTL`（60m），并且 **连接 idle timer 不得短于任何活跃 session TTL**。有 in-flight 或 bgRunning 时 `Touch` 至少延长到 TTL。
- `action=list`：列出逻辑 session（含 `disconnected` / `running`）。`list-servers` 已有会话列表，list action 与其对齐同一 `ListSessions()`。

### 1.5 Exec 默认 PTY

`GetPty()` 默认 `true` 对长任务有害（SIGHUP、docker/npm 以为有 TTY）。

本次不做全局默认翻转（避免交互式/2FA 回归）。但：

- **后台 starter / read / stop：强制不申请 PTY**
- 文档写明：跑 build/CI 的连接建议 `"pty": false`
- 可选（P2）：`execute-command` 增加 `pty` 覆盖参数——若改动面大可跳过，只写文档

---

## 2. 任务列表（按顺序交给 Auto）

每个任务独立可测。完成后 `go test ./...` 必须绿。改对外行为时同步三份文档（AGENT / HUMAN / agent skill）。

---

### T1 — 修复心跳：对齐 OpenSSH / Scylla / Fuchsia（P0）

**文件：** `internal/manager/manager.go`；新增 `internal/manager/heartbeat.go` + `heartbeat_test.go`（纯函数可测）

**做（抄语义，不引库）：**

- 抽出 `serverAliveCheck`，注释写明 port of OpenSSH `clientloop.c` / Scylla `sshtools.serverAliveCheck`：

  ```go
  // keepaliveOK reports whether a keepalive reply means the peer is alive.
  // OpenSSH treats any reply as success; REQUEST_FAILURE (ok=false) is normal.
  func keepaliveOK(ok bool, err error) bool { return err == nil }
  ```

- `startHeartbeat` 只用该函数。**禁止** `if err != nil || !ok`。
- 按 Fuchsia sshutil：`SendRequest` 放独立 goroutine；超过 interval（建议 `interval + 5s` grace）无返回 → unanswered++。全程最多一个 in-flight keepalive。
- 确认 `dial` 握手受 `ConnectionTimeoutMs` 约束（必要时对 `NewClientConn` 做与 Fuchsia `connectToSSH` 相同的 goroutine+deadline）。
- 可选：`runExecCommand` 在 `Wait` 期间对 **session** 发 keepalive（go-sshlib 模式），失败只计数，不直接 `session.Close()`。

**验收：**

- 单元测试：`(ok=false, err=nil) => alive`；`(err!=nil) => dead`。
- 人工：`execute-command cmdString="sleep 45" timeout=60000` 必须跑完，不得出现 `without exit status`。

**不要：** 改 interval 默认值来“绕过”bug；不要 `require` scylladb/go-sshtools 或 fuchsia。

---

### T2 — in-flight 保护，禁止执行中拆连接（P0）

**文件：** `internal/manager/manager.go`、`exec.go`、`session.go`、`shell.go`、`sftp.go`

**做：**

- `Manager` 增加 `inFlight map[string]int`（按 connection key）。
- `beginOp(key)` / `endOp(key)`（defer）。`ExecuteCommand`、`RunInSession`、`TransferFile`、后台 start/read/stop 都要包。
- `Touch` 的 idle callback：若 `inFlight[key]>0` 或该连接上有 `bgRunning` session，**重置 timer，不要 Disconnect**。
- `startHeartbeat` 达到阈值：`inFlight>0` 则 `logger.Warn` + 设 unhealthy，**return 而不 Disconnect**；等 `endOp` 降到 0 再 `Disconnect`（若仍 unhealthy）。
- `Disconnect` 与 `runShellScriptOnce` 解耦：`CloseSession` 在连接被拆时 **不要** 调 `stopBackgroundProcess`（见 T3）。显式 close 才杀作业。
- `shellSession.close` 保持“先标 closed + Broadcast，再 `session.Close()`”，且 **close 不得再抢执行侧已持有的锁去做 I/O**。

**验收：**

- 单测：mock 或直接测 `Touch` callback 在 inFlight>0 时不调用 Disconnect（可把 callback 条件抽函数）。
- 人工：session 内 `sleep 45` 不得死锁、不得 `result was not recorded`。

---

### T3 — 后台作业：无 PTY exec + 真正脱离会话（P0）

**文件：** `internal/manager/background.go`、`background_test.go`；必要时小幅改 `session.go`

**做：**

- 对齐 Ansible/运维惯例，而不是某个 Go SSH 库：无 PTY 短 exec、stdio 全部离开 SSH channel、double-fork/`setsid`、starter 立即退出。参考：`( ( nohup cmd </dev/null >>log 2>&1 ) & )`；starter 末尾可 `sleep 1` 防止 ssh 过快断开导致作业没挂上。
- 新增 `runDetachedExec(ctx, client, script string) (output string, err error)`：`NewSession`、**不** `RequestPty`、短超时（15–30s）、独立于 named shell。
- `startBackgroundCommand` 改走 `runDetachedExec` + 上面的 starter 脚本。成功需 `__MCP_BG_STARTED__` **且** 可选再 exec 一次 `kill -0`。
- `ReadSessionOutput` / `stopBackgroundProcess` 改走 `runDetachedExec`。
- 停止策略：`kill -TERM -$PID` 或 `kill -TERM $PID` 后等 2s，再 `KILL`；只在 **显式 close** 时调用。`Disconnect` / idle 断连 **不杀**。
- 启动失败（没有 PID、立刻 `kill -0` 失败、无 log 文件）→ 明确 `COMMAND_EXECUTION_ERROR`，message 说明作业未在远程存活，**不要**返回假 PID。
- 单元测试：解析 `__MCP_BG_STARTED__ pid=123`；starter 脚本字符串包含 `setsid`、`</dev/null`、且不是“仅 nohup &”。

**验收：**

```
session action=open sessionName=bgtest background=true
  cmdString="sleep 60; echo DONE"
session action=read  → running=true，log 文件存在
# 可选：人为断开 SSH（或等 keepalive 重连）后再 read，running 仍 true
session action=close → 进程消失
```

Agent 侧 `nohup docker build &` **不是**本任务的承诺；承诺的是 **MCP background session API**。文档写清：长任务用 `session background=true`，不要自己 nohup。

**不要：** 在 PTY named shell 里修 nohup 当主路径。

---

### T4 — Session 与连接解绑，支持重附着（P0）

**文件：** `internal/manager/session.go`、`manager.go`、`internal/tools/session.go`、`list_servers.go`；测试 `session_test.go`

**做：**

- `Disconnect` 不再 `closeSessionsForConnection` 里删逻辑 session；只 `ns.shell.close()` + `ns.shell=nil` + `disconnected=true`。
- `ensureShell(ns)`：连上后 `newShellSession`，若 `ns.cwd!=""` 则 `cd --` 恢复。
- `RunInSession` / `ReadSessionOutput`：session 存在但 disconnected → 重连+ensure，而不是 `not found`。
- `OpenSession` 对已存在名字保持幂等，并尝试 ensure。
- `SessionInfo` 增加 `Disconnected bool`（json `disconnected,omitempty`）。`list-servers` 展示该字段。
- 显式 close 才 `delete` + 杀 bg。
- 单测：`closeSessionsForConnection` 行为（session 仍在 map，shell nil）。不需要真 SSH。

**验收：** 断连后 `list-servers` 仍能看到 `basebuild`；`session action=read` 或 `execute-command sessionName=basebuild` 触发重连而非 `session "basebuild" not found`。

---

### T5 — 命令结果结构化；非 0 退出不再冒充传输失败（P1）

**文件：** `internal/manager/exec.go`、`shell.go`、`session.go`、`result.go`（新）、`internal/tools/execute_command.go`、`tools.go`；文档三份

**做：**

- `ExecuteCommand` / `RunInSession` 改为返回 `(CommandResult, error)`。`error` 仅用于校验失败、连不上、以及需要 `IsError` 的 status（timeout/lost/limit）。更干净的做法：永远返回 `CommandResult`，用 `result.Status` 决定 MCP `IsError`。
- **禁止** `formatCommandFailure` 把 stdout 整段塞进 `ToolError.Message`。Message 短句：`command exited with code 1` / `SSH connection lost during command`。
- exit≠0 → `status=exited`，MCP **成功**返回 text，含 stdout、stderr、`[exit code] N`。
- connection drop → `SSH_CONNECTION_LOST`，`retriable=false`，`replaySafe=false`，带 partial stdout。
- 识别断连用标准库类型，不要匹配英文串：

  ```go
  var missing *ssh.ExitMissingError
  var exitErr *ssh.ExitError
  switch {
  case errors.As(waitErr, &exitErr):
      // status=exited, ExitCode=exitErr.ExitStatus()
  case errors.As(waitErr, &missing):
      // status=connection_lost（Fuchsia sshutil 同样把 ExitMissingError 当连接故障）
  }
  ```
- 保持现有脱敏 + light compress，作用在 stdout/stderr 上。

**验收：**

- 命令 `sh -c 'echo BUILD_RUNNING; exit 1'`：tool **非** error；正文能分开看到 `BUILD_RUNNING` 和 exit code 1。
- 断连类错误：message **不含**整份 build log；JSON 有 `stdout` 字段；`retriable=false`。
- 更新 AGENT_GUIDE「Non-zero exit → error」那一行。

---

### T6 — SFTP 错误区分本地 / 远端 / 父目录（P1）

**文件：** `internal/manager/sftp.go`；测试 `sftp_error_test.go`（纯函数分类即可）

**做：**

- `Create`/`Open` 失败时分类：

  | 条件 | message |
  |---|---|
  | 本地 `os.Open` 失败 | 已有 `LOCAL_FILE_READ_FAILED`（保持） |
  | 远端父目录 `Stat` 不存在 | `Remote parent directory does not exist: <dir> (local file exists: <local>)` |
  | 远端目标不存在（download） | `Remote file does not exist: <path>` |
  | 权限 | `Remote permission denied: <path>` |
  | 其它 | `File upload/download failed: <err> (local=<...> remote=<...>)` |

- 用 `*sftp.StatusError` / `errors.Is(..., os.ErrNotExist)`。抽 `classifySftpError(op, local, remote, err) error`。
- **不要**自动 `mkdir -p` 远端（行为变化太大）。message 里提示需要先建目录。

**验收：** 上传到不存在的 `/no/such/dir/file.tar` → 错误明确写 **remote parent**，不得只说 `file does not exist`。

---

### T7 — Session 工具：`list` action + 参数按 action 校验（P1）

**文件：** `internal/tools/session.go`、AGENT/HUMAN/skill

**做：**

- `action` enum：`open | read | close | list`。
- `sessionName` **不要** schema `Required`。Handler 内：`open/read/close` 缺名字 → `COMMAND_VALIDATION_FAILED: sessionName is required for action=<...>`。
- `list`：不需要 `sessionName`；可选 `connectionName` 过滤；返回与 `list-servers` 相同的 session 摘要（可 JSON）。
- 未知 action（schema 被绕过或将来扩展）：`Invalid action "listxxx": must be open, read, close, or list`。**不要**报缺 sessionName。
- 非法 action 在缺 sessionName 时也应先报 action 无效——因此 schema 层 `action` 用 enum，handler `default` 仍保留人话错误。

**验收：** 只传 `action=list` 成功；只传 `action=nope` 报 action 无效。

---

### T8 — 文档与 Agent 用法（随 T3–T7 改，最后通读）

**文件：** `docs/AGENT_GUIDE.md`、`docs/HUMAN_GUIDE.md`、`skills/2native-ssh-mcp-agent/SKILL.md`、`README.md` 如需

**必须写清：**

1. 长任务 / 无输出任务：用 `session background=true` + `read` 轮询；**不要** `nohup`/`setsid` 走 `execute-command`。
2. 前台命令：`timeout` 必须大于真实耗时；默认 `commandTimeoutMs=30000` 仍在。
3. 非 0 退出是正常结果，看 `exitCode`。
4. `SSH_CONNECTION_LOST` 不可盲目重放。
5. 跑 build 的 host 建议 `"pty": false`。
6. Session 在连接闪断后仍可 read/重连；`close` 才杀后台。

---

## 3. 建议实现顺序与 Autopilot 提示

```
T1 心跳
T2 in-flight + 禁止执行中 Disconnect/死锁
T3 后台脱离 PTY
T4 session 重附着
T5 结构化命令结果
T6 SFTP 错误分类
T7 session list/校验
T8 文档（也可每任务顺手改对应段落）
```

给 Auto 的约束：

- 每个任务单独提交逻辑清晰的改动；先测试后文档。
- 不改 `go.mod` 依赖（keepalive/后台都不需要新模块）。T6 用现有 `github.com/pkg/sftp` 的 `StatusError`。
- 从 Scylla/Fuchsia/go-sshlib 抄逻辑时在注释里写上来源 URL + 许可证（Apache-2.0 / BSD）。不要大段粘贴无关文件。
- Windows 上远程脚本仍是 POSIX（远端 Linux）；本地测试分类函数即可。有 Linux 远端再做 T3 人工验收。
- 保持现有错误 code 字符串稳定，只 **新增** `SSH_CONNECTION_LOST`。
- `GetPty()` 默认值不要改。

---

## 4. 完成定义（Definition of Done）

| # | 场景 | 期望 |
|---|---|---|
| 1 | `sleep 45`，timeout=60000，exec | 正常结束，有空输出，无 without-exit-status |
| 2 | 前台 `docker build`，timeout=300000 | 等到结束或真正 COMMAND_TIMEOUT；不断连吞结果 |
| 3 | `session open background=true` + `sleep 30; echo OK` | 立刻返回已启动；read 看到 OK；进程不随 MCP 命令返回而消失 |
| 4 | 后台 build 中 SSH 闪断 | session 仍在；重连 read 能继续；进程仍在远程 |
| 5 | 上传到不存在的远端目录 | 报 remote parent missing |
| 6 | `echo X; exit 1` | 非 IsError；能分开看 X 和 exit 1 |
| 7 | `session action=list` 不带 sessionName | 成功列表 |
| 8 | `go test ./...` | 全绿 |

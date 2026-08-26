# Issue #1：完成后会话被清掉、ANSI 残留、路径/close 文案

给 Auto 的实现规格。对应 [issue #1](https://github.com/daidaiJ/2native-ssh-mcp/issues/1)（长 build 验收后的四处残留缺陷）。按任务顺序落地，不要一次改完再测。每个任务有明确的文件、做法、验收。

前置：[docs/DESIGN_RELIABILITY.md](DESIGN_RELIABILITY.md) 的 T1–T8 已落地。**不要做**：扩大工具数量、改认证/白名单模型、改 HTTP daemon 协议、引入新依赖。`GetPty()` 默认值不要改。

---

## 0. 缺陷与根因（已对照代码）

### D1 后台作业结束后会话被清掉（P1）

现象：`session action=read` 已经拿到 `running=false` 的终态输出，紧接着 `session action=close` 报 `COMMAND_VALIDATION_FAILED: session "…" not found`，`action=list` 也是空的。终态输出如果没在清理前读走就永久丢失；没有 log 路径可回读；`close` 不幂等。

与 [DESIGN_RELIABILITY.md](DESIGN_RELIABILITY.md) §1.4 / T4 矛盾：**逻辑 session 只应在显式 close（或明确的 TTL）时删除**。

代码里真正 `delete(m.sessions)` 的只有 `CloseSession`。它会被两条路径调用：

1. MCP `action=close`（显式）
2. `namedSession.resetIdleTimer` 的 `time.AfterFunc`（空闲 TTL）

根因叠了几层，任意一层都能解释「刚读完终态，会话就没了」：

1. **Idle `AfterFunc` 与 `Timer.Stop()` 竞态（最像「立刻消失」）**

   `resetIdleTimer`：

   ```go
   if ns.idleTimer != nil {
       ns.idleTimer.Stop() // 已触发/正在跑的 callback 拦不住
   }
   ns.idleTimer = time.AfterFunc(ttl, func() {
       _ = m.CloseSession(name)
   })
   ```

   长作业快到 TTL、最后一次 `read` 与旧 timer 撞车：`Stop()` 返回 false → 新 timer 已挂上 → 旧 callback 仍执行 `CloseSession` → 本次 `read` 刚返回 `running=false`，map 里已经没了。`ns.idleTimer` 的读写也没有统一锁。

2. **`CloseSession` 会毁掉远端证据**

   `stopBackgroundProcess` → `buildBGStopScript` 末尾 `rm -f "$PIDF" "$LOG"`。TTL/竞态一旦 close，远端 log 一起没，没有内存缓存、没有 log 路径回读。

3. **正在跑的后台作业也会被 idle close 杀掉**

   TTL 只看 `ns.background`（60m），不看 `bgRunning`。超过 60 分钟没 `read`/`RunInSession`，即使 PID 还活着也会 `CloseSession` → SIGTERM/KILL。T4 的本意是「有 bg 用更长 TTL」，不是「超时就杀作业」。

4. **进程一重启，内存 map 全丢**

   Session 只活在 `Manager.sessions`。stdio 子进程退出、HTTP daemon 重启后，远端 `/tmp/.2native-ssh-mcp-<name>.{log,pid,exit}` 还在，但 MCP 侧无法 reattach。同名再 `open background=true` 会走 starter 的 `: > "$LOG"`，把旧 log 截断。

5. **`close` 不幂等**（D4，放大 D1）

   会话已被 idle/竞态删掉后，调用方按「读完再 close」的正确流程会吃一记 `COMMAND_VALIDATION_FAILED`。

`read` 的 `offset<0` 从 `ns.bgOffset` 续读，这是对的；缺的是：会话还在时能 `offset=0` 重读，以及结果里带上远端 `logPath` 以便会话丢了还能用 `execute-command`/`cat` 补救。

### D2 exec 输出不剥 ANSI（P2）

现象：`docker images` 等命令的 MCP 正文里夹着 `\x1b[...m`。远端 `docker` 是带颜色的 wrapper；默认 `"pty": true` 时程序认定自己有 TTY。

`cleanShellOutput`（`internal/manager/shell.go`）已经剥 CSI/OSC 并规范化 `\r`，但**只用于 shell transport** 的 `runShellScriptOnce`。

| 路径 | 是否剥 ANSI |
|---|---|
| shell 模式 `execute-command` / named session 前台命令 | 是（`cleanShellOutput`） |
| exec 模式 `runExecCommand`（默认） | 否，直接 `buildCommandResult` → redact + compress |
| 后台 `ReadSessionOutput` | 否，只 `redactCombinedOutput` |

所以验收里看到的 `docker images`（exec + PTY）原样带回转义序列。压缩若在剥 ANSI 之前跑，还会把 CSI 算进 4KB 阈值。

### D3 `LOCAL_PATH_NOT_ALLOWED` 文案像在报攻击（P3）

`validateLocalPath` 在路径未落入 cwd / `allowedLocalPaths` 时一律：

```text
Path traversal detected. Local path resolved to: …. Allowed local paths for this connection: ….
```

`..` 逃逸和「就是想下到白名单外的合法路径」走同一句话。远端同类错误已经写的是 *not within the configured allowedRemotePaths*，本地没有对齐。错误码保持 `LOCAL_PATH_NOT_ALLOWED`，只改 message。

### D4 `close` 找不到会话是错误（P3）

`CloseSession`：map 里没有 → `COMMAND_VALIDATION_FAILED: session "…" not found`。对 Agent 来说 close 是清理动作，重复 close / 已被 TTL 清掉都应成功。缺 `sessionName` 仍应校验失败（那是参数错误，不是幂等范围）。

---

## 1. 目标架构

### 1.1 逻辑 session 生命周期

```
open (background=true)
    │
    ▼
  running  ──────── idle 期间禁止自动 close / 禁止杀 PID
    │
    │  job 退出（kill -0 失败，或 .exit 存在）
    ▼
  finished  ────── 元数据 + 远端 log 保留
    │                 read 可续读 / 从 0 重读
    │                 结果带 logPath
    │
    ├─ 显式 close     → 停 PID（若还在）→ 删远端 pid/log/exit → delete map
    └─ retain TTL     → 自 lastUsed 起满 bgSessionIdleTTL（仍 60m）
                       → 同显式 close（防泄漏；max 5 session/连接）
```

规则：

- **`bgRunning==true`：session idle timer 不得调用 `CloseSession`。** 只刷新 lastUsed（read/open/run）。连接层 `hasActiveWork` 已按 `bgRunning` 保活，保持。
- **`bgRunning==false`（含从未后台的普通 named session）：** 自 lastUsed 起算 TTL，到期才 `CloseSession`。普通 session 仍 10m（`defaultSessionIdleTTL`），后台（含已结束）仍 60m（`bgSessionIdleTTL`）。
- **`Disconnect` 仍然不删逻辑 session**（T4 已做）。finished + disconnected 时 `read` 重连后继续读 log，不要 `not found`。
- **显式 close 幂等**：不存在视为已关闭，返回成功（可在正文写 `already closed`）。
- **idle callback 必须带 generation**：`Stop()` 成功或失败都不能让过期 callback 删掉新一代 session。

Idle 实现建议（不要再直接 `AfterFunc` + 裸 `Stop`）：

```go
ns.idleGen++
gen := ns.idleGen
ns.idleTimer = time.AfterFunc(ttl, func() {
    m.expireSessionIfIdle(name, gen)
})

func (m *Manager) expireSessionIfIdle(name string, gen uint64) {
    m.mu.Lock()
    ns := m.sessions[name]
    if ns == nil || ns.idleGen != gen {
        m.mu.Unlock()
        return // 已被重置或已 close
    }
    if ns.bgRunning {
        // 作业还在：只续期，不删
        m.mu.Unlock()
        ns.resetIdleTimer(m)
        return
    }
    m.mu.Unlock()
    _ = m.CloseSession(name)
}
```

`resetIdleTimer` / `CloseSession` / `idleGen` 的读写都要在 `m.mu` 下，或给 `namedSession` 自己一把锁。禁止无锁改 `idleTimer`。

### 1.2 远端产物与 read

远程布局保持 `/tmp/.2native-ssh-mcp-<sessionName>.{log,pid,exit}`。

| 时机 | 远端文件 |
|---|---|
| job 仍在跑 | 保留 log/pid；read 用 `kill -0` |
| job 结束 | **保留** log 与 `.exit`（若 starter 有写）；pid 文件可留作诊断 |
| 显式 close 或 retain TTL | 再 `rm -f` pid/log/exit |

`SessionOutput` / `SessionInfo` 增加（json omitempty 可保留）：

```go
LogPath    string `json:"logPath,omitempty"`    // 远端绝对路径
ExitCode   *int   `json:"exitCode,omitempty"`   // 能从 .exit 读到时
```

`read`：

- `offset<0`：从 `ns.bgOffset` 续读（现状）
- `offset>=0`：从该字节读；**不要**把「已经读过」当成错误
- 读完后 `ns.bgOffset = offset + len(chunk)`（显式 0 即重读）
- 剥 ANSI（见 1.3）+ redact；chunk 仍跳过整段 light compress（现状注释保留）

`buildBGReadScript` 建议带上 `.exit`：header 增加 `exit=<n>` 或 `exit=`（空=还在跑/未知），这样 `running=false` 时 Agent 能看到作业退出码，不必再猜。

同名 `open background=true` 且已有 **finished** 会话：不要立刻 `: > "$LOG"` 开新作业。两种合法策略，选 **A**（更小行为变化）：

- **A（默认）**：拒绝，报 `session "x" already exists (finished); close it first or read logPath=…`
- **B**：幂等返回现有 SessionInfo，不重启作业

不要静默截断旧 log。进程重启后 map 为空、远端 log 仍在：本次 **不**做跨进程持久化（避免新的落盘格式）。在文档里写清：stdio 进程退出等于会话丢失；HTTP daemon 常驻才能跨对话保留。可选 P3（本 issue **不要做**）：open 时探测远端 log/pid 并 rehydrate。

### 1.3 ANSI：所有给人看的输出都走同一剥离

抽出纯函数（从 `cleanShellOutput` 拆，shell 路径继续调用它）：

```go
func stripANSI(s string) string  // CSI + OSC；可顺手剥 \x1b[?… 与 charset ESC(B
func cleanShellOutput(s string) string {
    s = stripANSI(s)
    // 现有 \r\n / \r 规范化
}
```

调用点：

- `buildCommandResult`：redact **之前**对 stdout/stderr 调 `stripANSI`（exec + shell 的结构化结果都覆盖；shell 会剥两次，必须幂等）
- `ReadSessionOutput`：对 `chunk` 在 redact 之前剥

配置（可选，默认开）：

```go
StripAnsi *bool `json:"stripAnsi,omitempty"` // nil / true = 剥；false = 原样
```

与 `OutputCompressLight` 同样用 `*bool`，nil 当 true。`GetStripAnsi()` 放 `internal/config`。`stripAnsi: false` 时跳过剥离（调试颜色/进度条）。不要新 CLI flag，除非 `cli.go` 里其它 `*bool` 已有对称解析——有则顺手加，没有就只 JSON。

正则保持现有 CSI/OSC 即可覆盖 docker wrapper 的颜色码；单测加 `\x1b[38;5;196m`、`\x1b[0m`、OSC title。不要为了「完整 VT100」上第三方库。

### 1.4 本地路径错误分类

`validateLocalPath` 白名单失败时：

| 条件 | message |
|---|---|
| 原始路径含 `..` 段，且解析后落到所有 root 之外 | `Path traversal rejected. Local path resolved to: <p>. <allowed>` |
| 其它未入白名单（含 cwd） | `Local path is not within the process cwd or configured allowedLocalPaths. Resolved to: <p>. <allowed>` |

错误码仍是 `LOCAL_PATH_NOT_ALLOWED`。父目录不存在的 write 分支（已有 *parent directory must exist*）不动。抽 `localPathDeniedMessage(raw, resolved, roots) string` 方便单测。

`..` 判定用 `filepath.Split`/`filepath.Clean` 后的分量，Windows `/` 与 `\` 都算；不要只 `strings.Contains(path, "..")`（会误伤 `foo..bar`）。

### 1.5 close 幂等

```go
func (m *Manager) CloseSession(sessionName string) error {
    // sessionName 空：仍由 tools 层报 required
    m.mu.Lock()
    ns, ok := m.sessions[sessionName]
    if !ok {
        m.mu.Unlock()
        return nil // 幂等
    }
    delete(m.sessions, sessionName)
    ns.idleGen++ // 作废 in-flight expire
    m.mu.Unlock()
    ns.stopIdleTimer()
    m.stopBackgroundProcess(ns)
    if ns.shell != nil {
        ns.shell.close()
    }
    return nil
}
```

tools 层：`close` 成功正文可以是 `Session "x" closed` 或 `Session "x" already closed`。不要为此新增错误码。

---

## 2. 任务列表（按顺序交给 Auto）

每个任务独立可测。完成后 `go test ./...` 必须绿。改对外行为时同步三份文档（AGENT / HUMAN / agent skill）。

---

### T1 — 作业结束后保留会话；idle 不得误杀（P1）

**文件：** `internal/manager/session.go`、`background.go`、`session_test.go`；必要时 `heartbeat_test.go` 里与 idle/close 相关的用例

**做：**

- `namedSession` 增加 `idleGen uint64`。`resetIdleTimer` / expire callback / `CloseSession` 按 §1.1 用 generation 作废过期 callback。idle 字段与 `sessions` map 的更新在同一把锁下。
- `bgRunning==true` 时 expire **只续期、不 `CloseSession`、不杀 PID**。
- 作业结束后（`ReadSessionOutput` 得到 `running=false`）：**不要**从 map 删除；**不要** `rm` 远端 log。`ns.background` 保持 true，TTL 仍用 `bgSessionIdleTTL`。
- `buildBGStopScript` 只在显式 close / retain 到期时调用；finished 状态的日常 `read` 不走 stop。
- 同名 `open background=true` 撞上已存在的 finished 会话：按 §1.2 策略 A 拒绝，message 带 `logPath`。
- 单测（不需要真 SSH）：
  - expire gen 不匹配 → 不删 session
  - `bgRunning=true` 的 expire → session 仍在
  - `bgRunning=false` 且 gen 匹配 → 删除
  - `CloseSession` 后迟到的 expire 不再二次生效（可对已删名字再调 expire）

**验收：**

- 人工（有 Linux 远端）：`background=true` 跑短命令（`echo DONE; sleep 1`）→ `read` 到 `running=false` → `list` 仍有该 session → `close` 成功 → 再 `list` 没有。
- 不得在 `running=false` 的当次 `read` 之后立刻 `not found`。

**不要：** 给 session 做磁盘持久化；不要改 `maxNamedSessionsPerConnection`；不要在 `Disconnect` 里 `delete` session。

---

### T2 — `close` 幂等（P3，可与 T1 同一提交）

**文件：** `internal/manager/session.go`、`internal/tools/session.go`、`internal/tools/session_test.go`

**做：**

- `CloseSession` 找不到名字 → `nil`。空名字仍由 handler 报 `sessionName is required for action=close`。
- Handler：调用后用「close 前是否存在」区分正文 `closed` / `already closed` 可选；不区分也可以，统一 `Session %q closed`。
- 单测：对空 Manager `action=close sessionName=nope` → `IsError=false`。

**验收：** 连关两次同一名字，第二次不是 `COMMAND_VALIDATION_FAILED`。

**不要：** 把「从未 open 过的 close」做成新错误码；幂等就是成功。

---

### T3 — `read` 可从头重读，并暴露 `logPath`（P2，issue 建议项）

**文件：** `internal/manager/background.go`、`session.go`、`internal/tools/session.go`；测试 `background_test.go`

**做：**

- `SessionOutput`（及 `SessionInfo`）增加 `LogPath`。`read` / `list` / `open` 的 JSON 都能看到。
- `offset=0` 从 log 头读取；文档写明「省略 offset / 负值 = 续读」。tools 层现状 `offset` 默认 `-1`，保持。
- `buildBGReadScript` 增加 `.exit`：header `running` / `size` / `exit`。`running=false` 且读到数字则填 `ExitCode`。
- 单测：`parseBGReadOutput` 兼容旧 header（无 exit 字段）；新 header 能解析 exit。

**验收：** 同一 finished session：先默认续读，再 `offset=0` 能再次拿到开头内容；JSON 含 `logPath` 且等于 `/tmp/.2native-ssh-mcp-<name>.log`。

**不要：** 改 MCP 工具名或拆新 tool；不要把整份 log 当 error message。

---

### T4 — 全路径剥离 ANSI（P2）

**文件：** `internal/manager/shell.go`（抽出函数）、`result.go`、`background.go`、`config/config.go`（+ `cli.go` 若已有对称 bool）；测试 `manager_test.go` 现有 `TestCleanShellOutput` 旁加 `stripANSI` 用例、`result_test.go`

**做：**

- `stripANSI` 纯函数；`cleanShellOutput` 调用它。剥离必须幂等。
- `buildCommandResult` 在 `redactCommandOutput` 之前对 stdout/stderr 执行（受 `GetStripAnsi()` 控制）。
- `ReadSessionOutput` 对 chunk 同样处理。
- `StripAnsi *bool`，默认剥。
- 单测：
  - `\x1b[32mgreen\x1b[0m` → `green`
  - `\x1b[38;5;196mred\x1b[0m` → `red`
  - OSC title 与现有 `TestCleanShellOutput` 行为不变
  - `StripAnsi=false` 时 `buildCommandResult` 保留 CSI

**验收：** exec 模式 `docker images`（或 `printf '\033[32mhi\033[0m\n'`）正文无 ESC；shell 模式不回归。

**不要：** 依赖第三方 ANSI 库；不要改 `pty` 默认值来「绕过」颜色。

---

### T5 — 本地路径白名单 vs 穿越，文案分开（P3）

**文件：** `internal/manager/manager.go`；新增或扩 `internal/manager/path_test.go`（纯函数即可）

**做：**

- 按 §1.4 抽 `localPathDeniedMessage`（或等价）。
- 分量含 `..` 且最终不在 root 内 → traversal 句。
- 否则 → cwd / `allowedLocalPaths` 范围句。列出允许根（已有 `describeAllowedRoots`）。
- 单测：`foo/../../etc/passwd` 走 traversal；`D:\\other\\a.tar`（不在根内、无 `..`）走 whitelist 句，**不得**含 `Path traversal detected`。

**验收：** 下载到白名单外目录时，Agent 能看出是范围问题，会去改 `allowedLocalPaths` 或换路径，而不是当攻击告警。

**不要：** 放宽白名单；不要自动把目标目录加进允许列表。

---

### T6 — 文档与 Agent 用法（随 T1–T5，最后通读）

**文件：** `docs/AGENT_GUIDE.md`、`docs/HUMAN_GUIDE.md`、`skills/2native-ssh-mcp-agent/SKILL.md`；README 仅当特性列表需要补一句

**必须写清：**

1. 后台作业结束后 session **还在**，必须 `close` 才释放（或 60m retain TTL）。`close` 可重复调用。
2. `read` 可带 `offset=0` 重读；JSON 有 `logPath`。进程重启后内存会话丢失，常驻 HTTP daemon 才能跨对话保留。
3. 输出默认剥 ANSI；`"stripAnsi": false` 可关。
4. 本地路径报错：穿越 vs 未在 `allowedLocalPaths`/cwd 内，两种说法。
5. 仍强调：长任务用 `background=true` + `read`，不要自己 `nohup`。

**不要：** 改 DESIGN_RELIABILITY.md 的历史根因段落；本文件就是它的后续规格。可在 DESIGN_RELIABILITY 文首加一行指针指向本文件。

---

## 3. 建议实现顺序与 Autopilot 提示

```
T1 会话保留 + idle generation
T2 close 幂等（建议与 T1 同 PR）
T3 read offset=0 + logPath + .exit
T4 ANSI
T5 路径文案
T6 文档
```

给 Auto 的约束：

- 每个任务单独可测；先测试后文档。T1+T2 允许同一提交，因为 close 幂等是 T1 验收的一部分。
- 不改 `go.mod`。
- 保持现有错误 **code** 稳定：仍用 `COMMAND_VALIDATION_FAILED`、`LOCAL_PATH_NOT_ALLOWED`。只改 message 与是否对 close 报错。
- Windows 上远程脚本仍是 POSIX；T1/T3 的脚本字符串单测在本地即可，有 Linux 远端再做成对 `read`/`close` 人工验收。
- `GetPty()` 默认值不要改。

---

## 4. 完成定义（Definition of Done）

| # | 场景 | 期望 |
|---|---|---|
| 1 | 后台短命令结束 → `read` `running=false` → `list` | 会话仍在，有 `logPath` |
| 2 | 随后 `close`，再 `close` | 两次都成功，无 `not found` |
| 3 | `read offset=0` | 能重读 log 开头 |
| 4 | exec `printf '\033[31mX\033[0m'` | 正文为 `X`，无 ESC（默认配置） |
| 5 | download 到白名单外、路径无 `..` | message **不含** `Path traversal detected` |
| 6 | `..` 逃逸出 root | message 含 traversal/rejected |
| 7 | 后台作业运行中，idle 到期（单测 gen/`bgRunning`） | 不删 session、不杀作业 |
| 8 | `go test ./...` | 全绿 |

人工补一轮 issue 原路径（有远端时）：`docker build` 后台完成 → `read` 终态 → `close` 成功 → `docker images` 无 ANSI → 白名单外下载文案正确。

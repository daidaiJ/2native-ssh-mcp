# 需求与实现规划

对应对比结论里三条**能在没有公司跳板机的情况下做对**的改进。下面五条是选型讨论里对「没这种跳板机环境，做了也不知道对不对」的答复；每条后面直接跟实现任务、验收标准和实现时要拍板的细节。完整配置契约仍在 [REQ-001](001-proxyjump-via.md) / [REQ-002](002-audit-policy.md) / [REQ-003](003-keyboard-interactive-elicitation.md)。

实现时各开 PR，不要捆成一个提交。用户可见说明仍以 `HUMAN_GUIDE` / `FEATURES` 为准，在对应实现 PR 里同步。

| 条 | 做什么 | 实现分支 | 任务 |
|---|---|---|---|
| [1 ProxyJump](#1-proxyjump别等真环境) | `via` + `direct-tcpip` | `feat/proxyjump-via` | T1.1–T1.5 |
| [2 审计链](#2-角色矩阵--opa--审计更不该卡在跳板机上) | 追加 JSONL + 哈希链 | `feat/audit-chain` | T2.1–T2.6 |
| [3 2FA 弹窗](#3-2fa环境变量过期是设计问题不是环境问题) | keyboard-interactive → elicitation | `feat/kbdint-elicitation` | T3.1–T3.6 |
| [4 测试缺口](#4-现在真正缺的测试不是-tufantunc-那三项) | 双 sshd fixture，默认 CI 不变红 | 跟 001 / 003 走 | T4.1–T4.3 |
| [5 明确不做](#5-没有环境时不该做的) | 拒绝混开关、品牌宣称、OPA | — | 无工单 |

建议串行：002 → 003 → 001（002 先合少冲突；003 改 `dial` 认证，001 的跳板 2FA 直接复用）。可并行。

---

## 1. ProxyJump：别等真环境

`via` 不是「登录跳板再敲 ssh」，而是：

1. 先对 bastion 做完 SSH 握手
2. 在这条连接上开 `direct-tcpip` 到内网 `host:22`
3. 在这条 TCP 隧道上再做一次 SSH 握手（另一套密钥，**不**把 agent 转进去）

这是 RFC 4254 的标准通道，和 Jumpserver 网页、ForceCommand、菜单式跳板无关。tufantunc 能写 `via`，是因为用 Docker 起了两台 sshd，不是因为作者有机房。

本机就能证「对」的最小拓扑（WSL / Docker 都行）：

```
[测试进程] --SSH--> bastion:2222 --direct-tcpip--> internal:2223 --sshd--> 执行 echo hop-ok
```

断言只要这几条：

- `via` 指向的 profile 先连通，目标 profile 的 TCP 不直接暴露也能 exec
- 测试进程对 internal 地址直连失败（证明走的是隧道，不是事先做好的端口转发）
- 目标用的是目标自己的 key，bastion 的 key 登不进去
- bastion 挂了之后目标调用返回可识别错误，而不是挂死
- `~/.ssh/config` 的 `ProxyJump bastion` 解析进 `via`（当前 parser 只吃 HostName/User/Port/IdentityFile，这一项是纯字符串测试，零网络）

证过这些，就可以在文档里写：**兼容 OpenSSH ProxyJump，不兼容只给交互菜单的堡垒机。** 没有真 Jumpserver 时，**不要声称**支持那种机子。

`commandTemplate: "ssh jumphost <quotedCommand>"` 是另一条路（远程再 ssh），行为、超时、杀进程、SFTP 都和 `via` 不同。不要把两条混成一个功能。

契约：[REQ-001](001-proxyjump-via.md)。建议最后做。默认 CI 无 Docker，容器测试必须是可选 tag。

### 实现任务

一个 PR，两个 commit 即可：① parser + Normalize（纯单测）② `dial` 隧道 + Docker integration。

**T1.1 配置与环检测** — `internal/config/config.go`

- `SSHConfig.Via string` `json:"via"`
- 加载完整连接表之后做图校验（单条 `Normalize` 看不到其它名字）：未知 via、自指、环（A→B→A）、目标上 `via` 与 `proxy`/`socksProxy` 同时出现（跳板自己可以有 proxy）、`via` 含逗号或空格（多跳拒绝）
- 错误信息带上路径上的连接名

完成：表驱动测试覆盖每一种失败；合法 `internal.via=bastion` 通过。

**T1.2 sshconfig ProxyJump** — `internal/sshconfig`

- `Entry` 增加 `ProxyJump string`，只接受单个 token
- `ProxyJump a,b` / `user@host` / `host:port` → 失败，不要截成第一段
- 别名对上已有连接 `name` 才写入 `Via`；对不上则配置错误，**不要静默新建**跳板连接

完成：`TestLookupProxyJump` / `TestLookupProxyJumpMultiHopRejected`。

**T1.3 dial 二次握手** — `internal/manager/dial.go`、`manager.go`

1. `getClient(viaName)` 拿跳板 `*ssh.Client`（走跳板自己的 proxy / Pageant / 2FA）
2. `bastion.Dial("tcp", joinHostPort(target.Host, target.Port))`
3. 对返回的 conn 做目标 `ssh.NewClientConn`（目标的 User/Auth/HostKey/Algorithms/Timeout + 现有 deadline 补丁）
4. 三种失败分开包装：跳板认证 / 跳板 TCP 到目标 / 目标认证

细节：拿跳板 client 时不要握着锁等目标握手（目标 2FA 可能弹窗）；目标 `known_hosts` 用目标 HostName/IP，不能写成跳板；日志 `Using ProxyJump for [internal-db] via [bastion]`，无密钥；`list-servers` 输出 `via`。不要在跳板上 `exec("ssh internal …")` 冒充 ProxyJump。测试镜像不要 `AllowTcpForwarding no`。

**T1.4 双 sshd fixture** — `internal/manager/proxyjump_integration_test.go`，`//go:build integration`（与 [§4](#4-现在真正缺的测试不是-tufantunc-那三项) 共用）

- `bastion` 映射 `127.0.0.1:2222:22`，`AllowTcpForwarding yes`
- `internal` **不映射到宿主机**，只挂 compose 网络
- 两套不同密钥；测试进程只拿得到 bastion 的 host 端口

| 测试名 | 断言 |
|---|---|
| `TestProxyJumpExecHopOK` | `echo hop-ok` |
| `TestProxyJumpNotDirect` | 测试进程 Dial internal:22 失败 |
| `TestProxyJumpWrongKey` | 跳板密钥登目标失败 |
| `TestProxyJumpBastionDown` | 停 bastion 后错误可识别，且在 connectionTimeout 量级返回 |
| `TestProxyJumpSftp` | 目标上传小文件成功 |

无 Docker 时 `t.Skip`。不要改现有 `go test -race ./...` 让 CI 变红。

**T1.5 组合与文档**

- 目标 `transportMode: shell` 仍走隧道（integration 加一条 `echo` 即可）
- 后台作业在**目标**上，闪断重连仍走同一 `via`；代码路径不能把 via 特殊 casing 掉
- HUMAN_GUIDE / AGENT_GUIDE / FEATURES：配置示例、与 `commandTemplate ssh` 的对比表、明确不兼容菜单式堡垒机

顺序：`T1.1 → T1.2 → T1.3 → T1.4`；T1.5 可与 T1.4 并行。

### 本条要拍板的细节

| 问题 | 决议 |
|---|---|
| 多跳 `ProxyJump a,b`、`user@host:port` | 本期拒绝并报错，有真实用户再开需求 |
| `via` 和 `commandTemplate ssh` 是否一个开关 | 否，代码路径分开 |
| 目标再配 `proxy` | 配置错误，避免「到底谁代理」 |

本条不做：多跳、`ProxyCommand`、agent forwarding、Jumpserver、把 `via` 做成 `ssh jumphost cmd`。

---

## 2. 角色矩阵 / OPA / 审计：更不该卡在跳板机上

这些代码路径在 `dial()` 之前就结束了。现有 `commandLog` 测试已经是这个层级（截断、只记成功、JSONL）。

- 矩阵：给 `(role, group, commandClass)` 一张表，单测就能覆盖。**本期不搬。**
- 防篡改：hash chain 的下一行必须含上一行摘要；手改中间一行校验失败。**本期做这个。**
- OPA：本地 httptest 就能证。**本期不搬。**

等保要的是「谁在何时对哪台执行了什么、改不了日志」。授权继续用 whitelist / blacklist / `approvalMode` / 路径白名单。

契约：[REQ-002](002-audit-policy.md)。建议第一个实现：改 `commandLog` 语义，全是单测。

### 实现任务

一个 PR。不要拆成「先改格式再加 hash」——中间态会让旧测试和旧文件无法定义。

**T2.1 钉死磁盘格式**

- 文件名推荐 `<connection>.jsonl`（与旧 `.log` 一眼可分）。实现若改用 `.log`，同步 HUMAN_GUIDE，不要两种并存。
- `AuditRecord`：`v`, `type`, `ts`, `connection`, `tool`, `command`, `session`, `approval`, `exitCode`, `success`, `errorCode`, `prevHash`, `hash`
- canonical JSON：字段名排序、无 HTML escape、时间 UTC RFC3339 或 RFC3339Nano（**选一种钉在测试的固定字符串里**）
- `hash` = SHA-256(去掉 `hash` 后的 canonical JSON)，hex 小写 64 字符；第一条 `prevHash` = 64 个 `0`

完成：`commandlog_hash_test.go` 里「给定结构体 → 固定 hash」，两次编码必须相同。

**T2.2 改写入模型** — `internal/manager/commandlog.go`

- 去掉「读入全部、截成 N 条、整文件重写」
- `Add`：`O_APPEND|O_CREATE`，持锁算 prevHash、写一行；Unix `0600`；`fsync` 不必强制
- 内存 `items` 仍只保留最近 `size` 条给 `Recent()` / `list-servers`
- `commandLogOnlySuccess` **只过滤 Recent() 窗口**，磁盘始终写失败记录（与内审目标一致，破坏性变更要写进 HUMAN_GUIDE）
- 发现无 `hash` 的旧文件：rename `*.log.legacy` 后开新链，stderr 提示一次；不要把旧行接进新链

完成：现有 `TestCommandLogBoundedAndPersistent` / `TestCommandLogFileFormat` / `TestCommandLogOnlySuccess` 按新语义改绿；截断窗口测试不删磁盘行。

**T2.3 滚动**

- 上限：16 MiB **或** 10_000 行，先到为准；先写死常量
- rename `<name>.jsonl` → `<name>.jsonl.<utc>`，新文件第一行 `type=chain-start`，`prevHash` = 旧文件尾 hash；`chain-start` 自己也有 `hash`

完成：`TestAuditRotatePreservesChain`。

**T2.4 校验命令**

- `audit-verify`：`--file` 校单文件；`--config-file` 校每个连接的当前 jsonl **以及** 同前缀滚动文件（按时间接链）
- 退出码：0 完整或无文件；2 坏链 / 旧格式；1 程序错误
- 坏链输出：路径、行号、期望、实际，一行；不提供修复工具
- verify 函数放 `commandlog.go` 给单测直接调，main 只解析参数

完成：`TestAuditVerifyTamperedLine` 改中间 `command` → 2；空文件/缺失 → 0 + `no audit log`。

**T2.5 补齐写入点**

| 调用点 | tool | approval | 备注 |
|---|---|---|---|
| `execute-command` 成功/失败/超时 | `execute-command` | `none` / `auto` / `accepted` / `fail-open` / `timeout` | 审批拒绝也写，无远端 exit |
| `file-transfer` 一次一条 | `file-transfer` | `none` | 摘要只用远端路径，不按子文件拆，不写本地用户目录 |
| `session` open/close | `session` | `none` | `command` = action |

审批拒绝在 `approval_gate` 返回 `decline` 时写。磁盘满不得改变已经发生的 SSH 命令：stderr 报错，结果仍返回 Agent。

完成：tools 测试拒绝一次 `rm -rf`，jsonl 出现 `approval=declined`、`success=false`。

**T2.6 文档** — HUMAN_GUIDE（文件名、onlySuccess、`audit-verify`）；FEATURES / SECURITY（链、不记输出）；AGENT_GUIDE（list-servers 窗口仍来自内存）。禁止写「等保认证」。

顺序：`T2.1 → T2.2 → T2.3 → T2.4`；T2.5 可与 T2.3 并行（依赖 T2.2 字段）；T2.6 最后。

### 本条要拍板的细节

| 问题 | 决议 |
|---|---|
| `.jsonl` 还是沿用 `.log` | 实现 PR 选定一种并改文档，不要留别名 |
| `commandLogOnlySuccess` 是否还让失败不落盘 | 否，只过滤展示窗口 |
| 要不要 OPA / role×tier | 否，见 [§5](#5-没有环境时不该做的) |
| SIEM / syslog 导出 | 另开需求，本链当源 |

本条不做：OPA、role/group 矩阵、syslog、把 spill 输出拷进审计文件。

---

## 3. 2FA：环境变量过期是设计问题，不是环境问题

现在的实现是一次性把 `SSH_MCP_2FA_CODE` 塞进 keyboard-interactive（password 题用配置密码，其它题用这个环境变量）。码在进程启动时冻住，TOTP 约 30s 一轮，HTTP daemon 常驻后第二次空闲重连必然过期。这在任何会出 OTP 题的 sshd 上都能复现，**不必等公司堡垒机**。

本地可证的两档：

**A. 进程内假服务器（先做）**  
`golang.org/x/crypto/ssh` 只做 keyboard-interactive：第一题 `Password:`，第二题 `Verification code:`。

- 密码走配置，OTP 走 **MCP elicitation**
- 按时填对 → 握手成功；超时 / 取消 → 明确错误，不拿过期 env 顶上
- 保活期内不重新出题；下次 `dial`（空闲断开后）再弹

**B. 容器真 sshd + PAM（可选，不进默认 CI）** — 证 OpenSSH 默认 keyboard-interactive 没被 AuthMethod 顺序踩死。仍然不是 Jumpserver。

没有录包就不要写「支持 XX 堡垒机 2FA」。能诚实写的是：**keyboard-interactive 的非密码题通过 MCP 弹窗问人，不缓存 TOTP。**

契约：[REQ-003](003-keyboard-interactive-elicitation.md)。建议在 001 之前合：会改 `dial.go` 认证回调。

### 实现任务

一个 PR。假 sshd 测试必须和回调改动同一提交，否则「有 elicitation 时忽略过期 env」没人护着。

**T3.1 Elicitor 接口** — 不要让 `manager` 直接 import MCP server（循环依赖、测试难）。

```go
type Challenge struct {
    Connection  string
    Name        string // SSH 服务器给的 name
    Instruction string
    Questions   []string
    Echos       []bool
}

type Elicitor interface {
    // Prompt 只接收非密码题，返回与 questions 等长的 answers。
    Prompt(ctx context.Context, ch Challenge) (answers []string, err error)
}
```

错误要能区分：用户拒绝、超时、客户端不支持。tools 层用现有 `RequestElicitation` 适配。

完成：接口 + `RecordingElicitor` 测试替身（记录调用次数和入参）。

**T3.2 注入路径**

- 推荐 **context 值**：tools 在 `approvalGate` 旁边 `context.WithValue`，`dial` 闭包读它。HTTP daemon 多客户端时 OTP 只弹给触发这次 dial 的 session，不能广播。
- `dial` **不得**在持有 Manager 连接表锁时等用户（`KeyboardInteractiveChallenge` 在握手线程，elicitation 是 RPC）。先释放锁再握手，或临时 client 成功后再挂进表。改 `getClient`/`dial` 时画一眼锁顺序。

完成：fake elicitor 阻塞 50ms 时另一连接的 `list-servers` 仍能返回。

**T3.3 改 `keyboardInteractive`** — `internal/manager/dial.go`

1. 题面含 `password` 或 `密码` → 配置密码，不调 Elicitor
2. 非密码题：有 Elicitor → **忽略** `SSH_MCP_2FA_CODE`，一次 Prompt 收齐；无 Elicitor 且 env 非空 → 现有 CI 后备；都没有 → 返回 error，不要交空字符串
3. Decline / Cancel / DeadlineExceeded → `SSH_AUTH_CHALLENGE_DECLINED` / `SSH_AUTH_CHALLENGE_TIMEOUT`，`retriable=false`
4. 用户取消后不要自动改读 env 再试同一握手
5. 超时默认 2 分钟，第一期用常量，不加配置字段

**T3.4 进程内假 sshd** — `internal/manager/kbdint_test.go`，默认 `go test` 就能跑，不要 `integration` tag。

假服务器：题 1 `Password:` echo=false；题 2 `Verification code:` echo=false；码对则 `exec` 回 `auth-ok`。不要把 OTP 打进生产 logger。

| 测试名 | 给定 | 期望 |
|---|---|---|
| `TestKbdintElicitorSuccess` | 密码正确，elicitor 返回正确 OTP | exec 成功 |
| `TestKbdintElicitorDecline` | elicitor 拒绝 | dial 失败，命令未跑 |
| `TestKbdintElicitorTimeout` | 超过 deadline | `SSH_AUTH_CHALLENGE_TIMEOUT` |
| `TestKbdintNoElicitorNoEnv` | 都没有 | 失败 |
| `TestKbdintNoElicitorEnvOK` | 无 elicitor，env 正确 | 成功（CI 后备） |
| `TestKbdintElicitorBeatsStaleEnv` | elicitor 正确、env 错误 | 成功（env 不得抢主路径） |
| `TestKbdintPasswordNotElicited` | 两道题 | elicitor.Questions 不含密码题 |

**T3.5 tools 适配与文案**

- 标题含 `SSH 认证` / `SSH authentication` + 连接名；正文用服务器 question 原文
- 与破坏性命令审批文案必须能从字符串上区分（加测试）
- 无 elicitation 能力 → 不调 Prompt，走 T3.3 的 env/失败分支
- OTP 失败不得变成审批那种 fail-open（此时还没有连接）

**T3.6 文档** — HUMAN_GUIDE：主路径弹窗，`SSH_MCP_2FA_CODE` 改为 headless 后备；FEATURES / AGENT_GUIDE：空闲重连再弹，保活期内不弹。禁止写具体堡垒机品牌。

顺序：`T3.1 → T3.2 → T3.3 → T3.4`；T3.5 可与 T3.4 并行；T3.6 最后。

### 本条要拍板的细节

| 问题 | 决议 |
|---|---|
| 多非密码题一次多字段还是连弹 | 推荐一次收齐，实现 PR 按此写测试名 |
| OTP 超时要不要做成连接配置 | 第一期常量 2 分钟，有人嫌短再加字段 |
| elicitation 与过期 env 谁优先 | 有弹窗能力就忽略 env |
| 客户端不会弹时 fail-open 去连？ | 否；无 env 则认证失败 |

本条不做：真 PAM 容器（可另开可选 job）、WebAuthn、在 001 之前做两跳双 2FA（001 的 integration 再补）。

---

## 4. 现在真正缺的测试，不是 tufantunc 那三项

仓库里带 `//go:build integration` 的测试要求本机 `config.json` 里有一台叫 `ubuntu` 的直连机：

- 超时杀进程组、无 PTY、SFTP、后台重附着——有真机才能跑
- **shell 模式对堡垒机、ProxyJump、2FA elicitation——默认 CI 都不跑**

没有跳板机时，优先补的不是 OPA，而是 **CI / 本机都能起的双 sshd fixture**。国产堡垒机仍然标 `unverified`，直到有人拿录包或测试账号回来。

当前 CI（`.github/workflows/ci.yml`）只有 `go test -race ./...`，无 Docker job。

### 实现任务

这些任务挂在 001 / 003 的 PR 里，不单独开分支。

**T4.1 双 sshd compose** — 与 T1.4 同一套：bastion 可从宿主机连，internal 不可。无 Docker `t.Skip`。

**T4.2 默认 CI 不变红** — `go test -race ./...` 不含 `integration` tag。容器测试要么保持本机 `-tags integration`，要么以后另加 optional job（`services: docker`），**本期不强制加 CI job**。

**T4.3 假 keyboard-interactive 进默认测试** — 即 T3.4，不打 `integration` tag，保证 2FA 主路径在没有 Docker、没有公司堡垒机时也绿。

### 本条要拍板的细节

| 问题 | 决议 |
|---|---|
| 现有 `ubuntu` 直连 integration 怎么办 | 不动；ProxyJump 用新 fixture，不要绑同一台 |
| 要不要本期改 ci.yml 加 Docker job | 不强制 |
| 菜单式堡垒机谁来标 verified | 有录包或测试账号再开，不在本期验收里 |

---

## 5. 没有环境时不该做的

- 不要把 `via`、Jumpserver 菜单、`commandTemplate ssh` 做成同一个开关，测不了就分不清是谁坏了。
- 不要在 README / FEATURES 写「支持堡垒机 2FA / 等保审计」——本地假服务器只能支撑「支持 keyboard-interactive 弹窗」和「命令日志不可篡改」。
- 不要为了对齐对比矩阵去搬 tufantunc 的角色×tier 和 OPA。那是另一类买家；这个仓库的买家是 RDP 后面的 Windows 运维机。

**可做、且能自己验收：** OpenSSH `ProxyJump`（双容器）+ keyboard-interactive 改成 elicitation（假 sshd）+ 审计日志加强（纯单测）。

**做成了也不知道对不对、应该明确不做或标实验：** 任何「跟某某堡垒机一模一样」的声明。

### 对应「任务」：实现 PR 里直接关掉的范围

收到下面这类工单时关，不要开半截实现：

- 多跳 ProxyJump / `ProxyCommand` / agent forwarding
- Jumpserver / PAM 菜单 / ForceCommand 方言
- 用远端 `ssh jumphost cmd` 冒充 `via`
- OPA sidecar、role×prod/staging 矩阵、`privileged-command` 独立工具
- 审计文件写入 stdout/stderr、宣称等保认证
- 文档出现具体堡垒机品牌 + 2FA
- 为 TOTP 做种子生成器 / WebAuthn

---

## 需求契约

完整字段、非目标、验收表仍以这三份为准（实现 PR 改行为时同步改它们）：

| ID | 文档 | 本地如何知道是对的 |
|---|---|---|
| REQ-001 | [001-proxyjump-via.md](001-proxyjump-via.md) | 两台 sshd + parser 单测 |
| REQ-002 | [002-audit-policy.md](002-audit-policy.md) | 纯单测 |
| REQ-003 | [003-keyboard-interactive-elicitation.md](003-keyboard-interactive-elicitation.md) | 进程内假 sshd |

## 本分支

文档落在 `feat/reqs-proxyjump-audit-2fa`。这里只合文档。实现从 main（或本文合入后的 main）再开 `feat/audit-chain`、`feat/kbdint-elicitation`、`feat/proxyjump-via`。

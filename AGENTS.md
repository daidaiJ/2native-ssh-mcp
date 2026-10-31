# AGENTS.md — 2native-ssh-mcp

SSH-based MCP server (Go): remote command execution + file transfer over MCP.
Human-facing docs live in `README.md` and `docs/`; agent-facing API reference is
`docs/AGENT_GUIDE.md`; the defensive playbook for agents driving this server is
`skills/2native-ssh-mcp-agent/SKILL.md`.

## 🔄 Handoff 摘要

（无进行中的交接。）

### escaping-optimizations — done

- **已落地：** `execute-command` 的三种命令形态 —— `cmdString`（shell）、`cmdString`+`args[]`（argv，服务端逐参数引用）、`scriptFile`（本地脚本 SFTP 暂存到远端后执行）；`scriptCleanup` / `scriptInterpreter` 接线；lint 接线（`off`/`warn`/`strict`，`strict` 报 `COMMAND_LINT_FAILED`）；连接建立后一次的远端 `base64`/`jq` 探测（`autoProbe` 可关，`list-servers` 显示 `capabilities=`）
- **可审计性（用户硬要求）：** 白/黑名单、破坏性分类器、审批弹窗与审计记录看的都是**可读等效命令** —— argv 记 `grep -rn 'foo bar' /etc` 这种最小引用形式，`scriptFile` 记脚本正文（首行注释标注来源、暂存路径、解释器、是否清理），不做 base64 之类的包装
- **验证证据：** `go test ./...` 绿（含结构门禁）；`golangci-lint run ./...` 0 issues；`go test -tags integration ./internal/manager/` 全绿（Ubuntu 24.04 实测：argv 逐字节还原且对照 naive join 会拆分/展开、暂存脚本执行+清理、后台模式经清理子壳保住退出码 5、lint warn/strict、审计记录内容、能力探测与关闭）；MCP stdio 端到端驱动（`tools/list` 参数表、argv/scriptFile/lint 调用、`capabilities=base64,jq`）
- **详情指针：** [`.handoff/escaping-optimizations.md`](.handoff/escaping-optimizations.md)

### schema-slimming — done (v1.9.1)

- **决策（用户，2026-10-28）：** 配一次就长期不变的开关留在连接配置，不上推成工具参数（策略开关尤其不该握在调用方手里）；工具描述精准、长文本交给配套 skill。
- **已落地：** `execute-command` 去掉 `lintMode` / `scriptCleanup` / `pty` / `keepAlive` / `keepAliveDuration`（前三个配置层早有同名字段；保活改为新配置项 `keepAliveMs`，`-1` = 执行完即断，`$global` 可设默认）；`session` 去掉 `pty`（后台任务按连接的 `pty` 决定是否包 `script`）；`DefaultCommandTimeoutMs` 30000 → 120000（实测 137 次调用里 130 次显式传 timeout，中位 90s、p75 120s，只有 3 次用得上旧默认值）；四个工具描述压到 1–3 句。
- **实测收益：** `tools/list` 载荷 7251 B → 4716 B（−35%），参数 28 → 22，`execute-command` 13 → 8 个参数（3663 B → 1778 B）。
- **验证证据：** `golangci-lint run ./...` 0 issues；`go test ./...` 绿（`internal/config` 本机 ACL 既有红除外，CI 跑 Linux）；`go vet -tags integration ./...` 干净；`go test -tags integration ./internal/manager/` 全绿（含 `TestExecDefaultNoPTY`、`TestBackgroundPTY` 两条 PTY 用例，以及暂存脚本清理、后台退出码用例）；新增 `internal/config/config_keepalive_test.go` 覆盖默认值/全局默认/连接覆盖/非法值。

### stability-and-context — done (2026-10-31)

- **来源：** 调研同类 SSH MCP（Motty 内置的 MCP server、TermBridge 的执行语义 ADR、n0madic/ssh-mcp、remotessh-mcp）后按差距落地；**不做**跨主机一次调用（用户明确否决：爆炸半径太大）。
- **稳定性：**
  - `server.WithRecovery()` + `tools.LogToolCalls` 中间件（`internal/tools/middleware.go`）：handler panic 变成该次调用的错误响应，不再让客户端等到超时；日志记录工具名/耗时/错误。
  - HTTP 传输：`ReadHeaderTimeout` 10s、`IdleTimeout` 2m、`MaxHeaderBytes` 16 KiB、`/mcp` 请求体上限 4 MiB（`limitBody`）。loopback 的 DNS-rebinding 保护是 mcp-go 内建。
  - 后台日志有界捕获（**用户点名加强**）：新配置 `backgroundLogMaxBytes`（默认 8 MiB，`-1` 不裁剪，`$global` 可设默认）。启动脚本把作业输出经 `capture()` 管道写入日志，超限时从最早处裁剪、丢弃字节数写 `.drop`、输出开头 64 KiB 存 `.head`；裁剪发生在唯一写入者进程内，无竞争丢数据。`session action=read` 的 `offset`/`totalBytes` 改为**绝对字节**，命中被裁剪区间时一次返回「保留开头 + 省略标记 + 最新输出」，头部另报 `droppedBytes`/`logMaxBytes`。
  - 中断语义：`CommandResult` 新增 `executionState`（`started` = 已开始、结果未知；`unknown` = 断线，可能没开始/在跑/已结束）与 `guidance`，只在被中断的结果上出现；skill/AGENT_GUIDE 写明「超时 ≠ 命令没跑，先核对远端状态再决定是否重试」。
  - 顺带修掉两个既有 bug：`normalizeConfig` 从未解码 `resultFormat`（object 形式配置文件里该项一直被静默忽略）；后台 job 在 1 秒内跑完会被误报 "job is not alive"（现改为：退出文件存在即视为启动成功）。
- **上下文：** `session action=read`/`list` 与 `list-servers` 不再「文本 + Raw JSON」双份发送，统一由 `resultFormat` 二选一（新增 `$global.resultFormat`）；spill 预览改为头 8 行 + 尾 4 行；initialize 增加 `instructions`（跨工具契约），`execute-command` 描述里与之重复的一句删除。
- **实测：** `tools/list` 4716 B → 4619 B（并新增 685 B 的 instructions，只发一次）；`list-servers` 单连接实测 491 B（旧版另附一份结构化清单，json 模式下该清单 550 B）；`session action=list` 462 B → 142 B。
- **验证证据：** `golangci-lint run ./...` 0 issues；`go test ./...` 绿（WSL Ubuntu-22.04 下 `go test -race ./...` 全绿；本机 `internal/config` 的 ACL 既有红除外）；`go vet -tags integration ./...` 干净；`go test -tags integration ./internal/manager/` 全绿（含新增 `TestBackgroundLogCapIntegration`：8 MiB→64 KiB 上限下裁剪计数、head+marker+tail 一次返回、绝对 offset 不回退）；MCP stdio 端到端驱动（initialize instructions、`tools/list` 字节数、session open/read/list/close、list-servers 两种形态）。

### ssh-sftp-efficiency-and-truncation — done (2026-10-31)

- **截断提示（用户点名要求）：** 任何“手里不是完整输出”的结果都显式标注，agent 不会把前缀当全文。
  - `maxOutputBytes` 命中：错误 JSON 保留 `truncated: true` / `clippedBytes`，消息改为 `[output truncated] … the returned output is incomplete (N bytes discarded)`；文本结果尾部新增 `[output truncated] … incomplete` 段落。
  - 轻压缩：正文省略标记与尾注都写明 `the output above is incomplete because it was truncated for length`；spill 预览写明 `this preview is not the complete output`。
  - `session action=read` 被 `maxBytes` 截断：`SessionOutput` 新增 `truncated` / `remainingBytes`；远端读脚本返回 `remain=`，文本头部带 `truncated=true remainingBytes=N`，尾部提示用返回的 `offset` 继续读（旧 header 无 `remain=` 时按完整读取解析）。
- **协议效率：**
  - 下载：新文件（> 1 个 chunk）改走 `sftp.File.WriteTo` 流水线读，不再一块一等；小文件保留顺序读，避免 WriteTo 的额外 stat。
  - 上传/续传：`copyFile` 只要超过一个 chunk 就并发，去掉“chunk 数少于 workers 就串行”的退化（2–8 块的文件在高延迟链路上收益最大）。
  - 客户端：`MaxConcurrentRequestsPerFile` 接到 `sftpConcurrency`，读写流水线与自研 worker 共用一个在途预算。
  - 目录传输：新增 `sftpFileConcurrency`（默认 4，`$global` 可设默认），单个文件仍用 `sftpConcurrency`；每文件原子落盘/续传/失败隔离语义不变，进度与 `failed` 列表并发安全，失败列表排序稳定。
  - 校验：新增 `sftpVerifyChecksum`（默认 true；`$global` 可设默认），关掉可省一次远端整文件读取。
- **未做（评估后保留）：** SSH 压缩（`x/crypto/ssh` 只支持 `none`）；cipher 调优（已有 `algorithms.cipher`，按 CPU/链路自选）；exec 的 PID 临时文件（每次命令一次远端小写入，收益不足以改超时 kill 语义）。
- **验证证据：** WSL Ubuntu-22.04 `go test -race ./...` 全绿；`golangci-lint run ./...` 0 issues；`go vet -tags integration ./...` 干净；`go test -tags integration ./internal/manager/` 全绿（含新增 `TestBackgroundReadTruncationIntegration`、`TestTransferChecksumDisabled`，以及目录并发/续传/去重/独立连接用例）；MCP stdio 端到端驱动实测：`session action=read maxBytes=256` 返回 `truncated=true remainingBytes=1436` + 尾部续读提示，第二次读完整；`seq 1 1000` 在 `maxOutputBytes=200` 下返回 `truncated: true` / `clippedBytes: 3693` / message 明说不完整。
- **基准（localhost，3 次均值）：** 16 MiB 下载 93.19 → 92.40 MB/s、上传 51.17 → 55.52 MB/s，处于波动范围；流水线收益取决于 RTT，本机无法体现。
- **发布：** v1.10.0（tag 正文即 release notes，双语；未推 `-registry`）。

### 未验证事项

- [ ] `make check` 本机仍未跑过（未装 `make`）；等价地逐条跑了 `golangci-lint run ./...` + `go test ./...`，CI 的两道门禁与之一致
- [ ] 后台日志上限的**远端磁盘**效果未做长时间实测（只验证了 64 KiB 上限下日志确实停在 65536 B）
- [ ] 高延迟链路（>50 ms RTT）下的 SFTP 提速未做真实测量；localhost 基准只能证明没有回退

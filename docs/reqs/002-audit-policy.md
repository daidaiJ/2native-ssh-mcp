> 英文版暂不提供。本文是实现契约，不是用户指南。

# REQ-002 防篡改命令审计

状态：草稿  
分支：`feat/reqs-proxyjump-audit-2fa`  
依赖：无  
验证环境：纯单测，**不需要** SSH 主机，更不需要跳板机

## 1. 背景

现有 `commandLog`（`internal/manager/commandlog.go`）按连接保留最近 N 条命令（无输出），JSON 行，**整文件重写**。缺少：

- 执行人 / 工具名 / 审批结果
- 追加写入与完整性校验
- 被改一行能发现

对比矩阵里 tufantunc 的「角色×prod/staging 矩阵 + OPA sidecar + 防篡改审计」是三件不同的产品。等保/内审要的是**留痕且改不了**；角色矩阵和 OPA 是另一类买家的授权引擎。没有合规客户点名、没有 OPA 集群时，把后两件搬过来无法验收。

本需求只做审计。授权继续用已经存在的：`commandWhitelist` / `commandBlacklist` / `approvalMode` / 路径白名单。

## 2. 目标

1. 命令审计改为**追加 JSONL**，每条带前一条的 SHA-256，形成哈希链。
2. 记录足够内审的字段（见 R2），**仍然不记命令输出**（输出里可能有密钥；脱敏扫描也不是审计的职责）。
3. 提供只读校验：链断了、中间被改、文件被截断，能报出第一条坏记录的偏移。
4. 默认行为对未打开审计加固的用户兼容：现有 `commandLogSize` 仍可作「list-servers 里展示最近 N 条」的内存窗口；磁盘上的链按日期或体积滚动，而不是重写缩成 N 行。

## 3. 非目标

- OPA / Rego sidecar、`--opaUrl`、AuthZEN。需要时另开需求。
- tufantunc 式 `role × group(prod/staging/dev)` 命令分类矩阵、`privileged-command` 独立工具。现有 whitelist + 破坏性审批已经覆盖「这台机器能跑什么」。
- 把 stdout/stderr 打进审计文件。
- 远程 syslog / SIEM 导出（可在链稳定后另做）。
- 用审计链替代 `known_hosts` 或配置文件 ACL。
- Windows Event Log 双写。

## 4. 用户场景

安全岗抽查：某连接在 14:02 是否执行过 `rm`、当时审批是接受还是自动放行、日志有没有被运维改过。

```
2native-ssh-mcp audit-verify --config-file config.json
# 或校验指定文件
2native-ssh-mcp audit-verify --file .ssh-mcp-logs/prod.jsonl
```

退出码 0 = 链完整；非 0 = 打印第一条失败行号 / hash。

## 5. 需求条目

### R1 存储格式

- 路径：默认仍在 `commandLogDir`（默认 `.ssh-mcp-logs`），文件名改为 `<connection>.jsonl`（或保留 `<connection>.log` 但内容改为带 hash 的 JSONL；实现时二选一，用户文档写死一种，**不要两种并存**）。
- 追加写，`O_APPEND`；Unix 文件模式 `0600`，Windows 继承现有配置 ACL 思路（仅当前用户可改）。
- 每一行一个 JSON 对象，字段稳定、未知字段校验工具忽略。
- 滚动：单文件达到可配上限（默认 16 MiB 或 10_000 行，以先到为准）时 rename 为 `<name>.jsonl.<utc-timestamp>`，新文件第一行是 `type: "chain-start"`，`prevHash` 为上一文件最后一条的 hash（跨文件链不断）。
- **禁止**再把整文件重写成「最近 N 条」——那会毁掉链。`commandLogSize` 只影响内存里给 `list-servers` 看的窗口。

### R2 记录字段

每条命令记录（`type` 缺省或 `"command"`）：

| 字段 | 说明 |
|---|---|
| `v` | 格式版本，从 `1` 起 |
| `type` | `command` / `chain-start` / `rotate` |
| `ts` | RFC3339 UTC |
| `connection` | 连接名 |
| `tool` | `execute-command` / `session` / `file-transfer` |
| `command` | 命令或传输摘要（上传记 `upload <remotePath>`，不要本地绝对路径里的用户目录以外的部分；实现用连接配置里的远端路径） |
| `session` | 可选，命名会话 |
| `approval` | `none` / `auto` / `accepted` / `declined` / `fail-open` / `timeout` |
| `exitCode` | 命令退出码；传输失败用约定非 0 |
| `success` | bool |
| `errorCode` | 可选，现有 `ToolError` code |
| `prevHash` | 64 位 hex SHA-256；文件第一条为 64 个 `0` |
| `hash` | 本条 hash |

`hash` 计算：对**去掉 `hash` 字段后的 canonical JSON**（字段名排序、无多余空白）做 SHA-256。实现必须在单测里钉死一组固定字节，避免不同 JSON 库把链算崩。

`commandLogOnlySuccess: true` 时仍**写入失败记录**（内审要失败痕迹），但 `list-servers` 的最近 N 条窗口可以继续只展示成功——两个开关职责分离。若坚持「磁盘也不记失败」，与内审目标冲突，**不允许**。现有 `commandLogOnlySuccess` 文档改为只影响展示窗口，并在 HUMAN_GUIDE 写清破坏性变更。

### R3 写入时机

- `execute-command`：命令结束后写一条（含超时、被杀、审批拒绝——拒绝也要写，`approval=declined`，`success=false`，没有远端 exit）。
- `file-transfer`：每次调用一条摘要，不按目录下每个文件拆（避免刷屏）；`failed` 非空则 `success=false`。
- `session open/close`：各一条，`command` 为 action。
- 审批 fail-open 执行成功：`approval=fail-open`，`success` 按远端结果。

写入失败（磁盘满）不得改变 SSH 命令已经发生的事实：stderr 打错误，命令结果仍返回给 Agent。

### R4 校验

- 子命令 `audit-verify`：读文件，重算每行 hash，检查 `prevHash` 衔接、`chain-start` 跨文件衔接。
- 坏链：stderr 人类可读一行（文件、行号、期望 hash、实际），退出码 `2`（配置/数据问题，与现有 CLI 习惯对齐：1=程序缺陷，2=用法或数据）。
- 空文件 / 不存在：退出码 0，打印 `no audit log`。
- 不提供「修复链」工具。坏了就是坏了，留给人工拷贝备份。

### R5 兼容

- 读到旧格式（无 `hash` 的 JSON 行）：校验工具报 `legacy log, not a hash chain` 退出 2；运行时**不要**把旧行接进新链。启动时若发现旧文件，rename 为 `<name>.log.legacy` 再开新链，stderr 提示一次。
- 未配置 `commandLogDir` 且 `commandLogSize` 显式 `0`：不写审计（与现在「关闭日志」一致）。默认 size>0 时开启链。

## 6. 验收（全是单测）

1. 追加 3 条，重算链通过。
2. 改中间一行的 `command`，verify 在该行失败。
3. 删最后一行（截断），verify 失败或报告 unexpected EOF（二选一写进测试名，行为稳定）。
4. 滚动：写超上限 → 出现 `*.jsonl.<ts>` + 新文件 `chain-start.prevHash` 等于旧文件尾 hash，verify 两个文件都过。
5. canonical JSON 钉死：同一结构体两次编码 hash 相同。
6. `commandLogOnlySuccess` 不再阻止失败记录落盘；Recent() 窗口仍可过滤。
7. 审批拒绝也落盘（可在 `approval` 包或 tools 层用 fake log 测，不必真 SSH）。
8. **没有** Docker、没有 sshd 也可以在 CI `go test ./...` 全绿。

## 7. 实现落点

| 位置 | 改动 |
|---|---|
| `internal/manager/commandlog.go` | 替换重写模型；hash；滚动 |
| `internal/manager/manager_test.go` | 现有 bounded/onlySuccess 测试按 R2/R5 改语义 |
| `main.go` / CLI | `audit-verify` |
| `internal/tools/*` | 写入 tool / approval 字段 |
| `docs/HUMAN_GUIDE.md`、`SECURITY.md`、`FEATURES.md` | 实现后同步破坏性变更（onlySuccess、文件名） |

## 8. 实现时注意

- hash 输入必须稳定：`time` 用 UTC 固定格式；map 不要靠随机迭代。
- 不要把密码、私钥路径写进 `command`；file-transfer 摘要只用远端路径。
- 并发：同一连接的 log 已有 mutex，保持；不要在持锁时做 SSH。

## 9. 开放问题

任务拆解与拍板表见 [README.md §2](README.md#2-角色矩阵--opa--审计更不该卡在跳板机上)。

- 文件名用 `.jsonl` 还是沿用 `.log`：实现 PR 选定一种并改文档，不要留别名。
- 若后续真有等保客户要 SIEM，另开「审计导出」需求，本链是源。

# 实现规划：REQ-002 防篡改命令审计

契约：[002-audit-policy.md](002-audit-policy.md)  
实现分支（文档合入 main 之后再开）：`feat/audit-chain`  
建议第一个实现：改 `commandLog` 语义，冲突面最小，且全是单测。

## PR 边界

一个 PR。不要拆成「先改格式再加 hash」——中间态会让旧测试和旧文件无法定义。

## 任务

### T2.1 钉死磁盘格式

- 选定文件名：推荐 `<connection>.jsonl`（与旧 `.log` 一眼可分）。规划里按这个写；若实现改用 `.log`，同步 HUMAN_GUIDE，不要两种并存。
- 定义 `AuditRecord`：`v`, `type`, `ts`, `connection`, `tool`, `command`, `session`, `approval`, `exitCode`, `success`, `errorCode`, `prevHash`, `hash`。
- canonical JSON：字段名排序、无 HTML escape、时间 UTC RFC3339Nano 或 RFC3339（选一种钉在测试里的固定字符串）。
- `hash` = SHA-256(canonical JSON without `hash`)，hex 小写 64 字符。
- 文件第一条 `prevHash` = 64 个 `0`。

完成标准：一张表「给定结构体 → 固定 hash」，放在 `commandlog_hash_test.go`，两次编码必须相同。

### T2.2 改写入模型

文件：`internal/manager/commandlog.go`

- 去掉「读入全部、截成 N 条、整文件重写」。
- `Add`：`O_APPEND|O_CREATE`，持锁算 prevHash、写一行、`fsync` 不必强制（单测不依赖），Unix `0600`。
- 内存 `items` 仍只保留最近 `size` 条给 `Recent()` / `list-servers`。
- `commandLogOnlySuccess` **只过滤 Recent() 窗口**，磁盘始终写失败记录。改 `TestCommandLogOnlySuccess`。
- `load()`：跳过无 `hash` 的旧行（或发现旧文件则 rename `*.log.legacy` 后开新链，stderr 提示一次）。不要把旧行接进新链。

完成标准：现有 `TestCommandLogBoundedAndPersistent` / `TestCommandLogFileFormat` 按新语义改绿；新增截断窗口测试不删磁盘行。

### T2.3 滚动

- 上限：默认 16 MiB **或** 10_000 行，先到为准。可先写死常量，不必新配置字段。
- 触发：rename `<name>.jsonl` → `<name>.jsonl.<utc>`，新文件第一行 `type=chain-start`，`prevHash` = 旧文件最后一条 `hash`。
- `chain-start` 自己也有 `hash`。

完成标准：`TestAuditRotatePreservesChain` 写超上限后 verify 两个文件都过。

### T2.4 校验命令

- `main.go` 增加 `audit-verify`：
  - `--file <path>` 校单个文件
  - `--config-file` + 默认/配置的 `commandLogDir` 校每个连接的当前 jsonl **以及** 同前缀的滚动文件（按时间排序接链）
- 退出码：0 完整或无文件；2 坏链 / 旧格式；1 程序错误。
- 坏链输出：路径、行号、期望、实际，一行人类可读。
- 不提供修复工具。

完成标准：`TestAuditVerifyTamperedLine` 改中间 `command` → 退出码 2；空文件/缺失 → 0 + `no audit log`。

CLI 测试可以 `os/exec` 调编译出来的二进制，或把 verify 函数放 `commandlog.go` 给单测直接调（推荐后者，main 只做参数解析）。

### T2.5 补齐写入点

现在 `Add` 只有 Command/ExitCode/Success。补：

| 调用点 | tool | approval | 备注 |
|---|---|---|---|
| `execute-command` 成功/失败/超时 | `execute-command` | `none` / `auto` / `accepted` / `fail-open` / `timeout` | 审批拒绝也写，无远端 exit |
| `file-transfer` 一次调用一条 | `file-transfer` | `none` | 摘要 `upload <remote>` / `download <remote>`，不按子文件拆 |
| `session` open/close | `session` | `none` | `command` = action |

审批拒绝路径在 `approval_gate` 返回 `decline` 时写，不要等 Manager。可用 fake log 测，不必真 SSH。

完成标准：tools 层测试里拒绝一次 `rm -rf`，jsonl 出现 `approval=declined`、`success=false`。

### T2.6 文档

- HUMAN_GUIDE：文件名、onlySuccess 语义变更、`audit-verify` 用法。
- FEATURES / SECURITY：审计链、不记输出。
- AGENT_GUIDE：list-servers 的最近命令仍来自内存窗口。

禁止写「等保认证」「符合某某标准」。

## 顺序

```
T2.1 → T2.2 → T2.3 → T2.4
              ↘ T2.5（可与 T2.3 并行，依赖 T2.2 的字段）
T2.6 最后
```

## 不做（本 PR）

OPA、role/group 矩阵、syslog、把 spill 输出拷进审计文件。

> [English](SECURITY.en.md)

# 安全

2native-ssh-mcp 让 AI agent 以受控方式访问远程主机。agent 能执行命令的安全边界，取决于你配置的凭据、网络路径与策略。本文描述威胁模型、默认行为与加固建议。

## 威胁模型

**主要风险**：拥有 shell 权限的 LLM 可能被不可信内容影响（日志、网页、粘贴的配置）。提示注入没有通用解，应假设**每条命令都可能被攻击者左右**。

**本服务端做的事：**

- SSH 凭据只保存在本地配置 / 环境变量 / agent 中——绝不进入暴露给模型的 MCP 工具参数
- 命令先经过每连接的白名单/黑名单正则校验
- 破坏性命令经 MCP elicitation 弹窗由人类确认（`approvalMode: "ask-destructive"`，默认关闭）
- 文件传输路径受 `allowedLocalPaths` / `allowedRemotePaths` 限制（本地范围按连接用 `localPathMode` 控制：默认 `cwd`，可选 `list` / `any`）
- 命令输出返回客户端前，脱敏常见密钥格式（Bearer token、PEM 块、`password=`/`token=` 行）
- 启动时检查配置文件权限（Unix：文件 `0600` / 目录 `0700`；Windows：ACL 修改权限）
- 默认对照 `known_hosts` 校验 SSH 主机密钥（`accept-new`），密钥变更即拒绝
- 命令超时或超出输出上限时，exec 模式发 SIGTERM/SIGKILL、shell 模式发 Ctrl-C
- 暴露 MCP 工具提示（`readOnlyHint`、`destructiveHint`），客户端可据此拦截危险工具

**本服务端不做的事：**

- 不保证审批分类器抓住每一条破坏性命令——分类是尽力而为，且客户端无法弹窗时**fail-open**（见下文）
- 不是完整的命令分级策略引擎（只读/安全/破坏性/特权 × 角色 × 主机组）
- 不保证脱敏覆盖所有密钥格式

## 主机密钥校验

主机密钥对照 OpenSSH `known_hosts` 文件校验（默认 `~/.ssh/known_hosts`，可按连接用 `knownHostsFile` 覆盖；文件及其父目录缺失时自动创建）。三种模式，按连接配置（`hostKeyCheck`）：

| 模式 | 行为 |
|---|---|
| `accept-new`（默认） | 未知主机记入 `known_hosts` 并放行；已知主机密钥**变更**则拒绝（`SSH_HOST_KEY_MISMATCH`） |
| `strict` | 未知主机拒绝（`SSH_HOST_KEY_UNKNOWN`）；密钥变更拒绝 |
| `none` | 不校验（有中间人风险）——仅适用于动态 IP / 频繁重装系统的实验机 |

默认值从"接受任意密钥"改为 `accept-new` 是刻意的：首次接触信任（TOFU），此后密钥钉死。服务端合法换钥时，删除 `known_hosts` 中对应行即可（或对该连接设 `hostKeyCheck: none`）。

## 加固建议

1. **不要指向 root。** 使用专用低权限账号。
2. **生产环境用白名单正则。** 从只读（`^ls `、`^cat `、`^df `）起步，逐步放开。
3. **凭据放环境变量引用：** `"password": "${SSH_MCP_PASSWORD}"`——不要写进 MCP 客户端的 JSON 参数。
4. **锁死配置文件：** Unix 上 `chmod 600 config.json && chmod 700 $(dirname config.json)`。
5. **`allowedRemotePaths` / `allowedLocalPaths` 配到最小必要范围**；`"localPathMode": "list"` 可把进程工作目录排除在本地范围外；`"localPathMode": "any"` 关闭本地限制——仅限可信单用户机器。
6. **HTTP daemon（`start`）不要无 token 暴露到 `127.0.0.1` 之外**：非回环监听地址必须配 `--http-token` / `SSH_MCP_HTTP_TOKEN` / `$global.httpToken`（fail closed），之后每个 `/mcp` 请求都要带 `Authorization: Bearer <token>`。
7. **有状态操作优先 exec 模式 + 命名会话**；`transportMode: shell` 留给禁止 exec 的堡垒机。
8. **生产连接开启 `approvalMode: "ask-destructive"`。** 它只对分类器命中破坏性的命令弹窗；内置分类器在你的机器上不准时，用 `approvalPatterns` / `approvalExemptPatterns` 调教，而不是直接裸奔。

## 审批闸门（破坏性命令）

按连接选配（`approvalMode`）：`auto`（默认——行为不变）或 `ask-destructive`——`execute-command` 执行被分类为破坏性的命令前，服务端向客户端发 MCP **elicitation** 弹窗等人类确认。拒绝或取消则不执行，并告知 agent 用户已拒绝。`$global.approvalMode` 是默认值，连接级配置覆盖它。

**什么算破坏性**——三层，按序：

1. `approvalExemptPatterns`（用户正则）：命中即**永不弹窗**，即使内置分类器命中同一命令。豁免优先级最高——这个权衡归运维者。
2. 内置分类器：`rm`、`reboot`、`mkfs`、`dd` 写设备、`systemctl stop/restart`、`kill`/`pkill`、递归 `chmod`/`chown` 等调用，加模式识别（fork 炸弹、`iptables -F`、重定向覆盖磁盘或 `/etc/passwd` / `authorized_keys`、`git push --force`、`DROP TABLE`……）。
3. `approvalPatterns`（用户正则）：命中即弹窗，把"在你的机器上危险"的命令补进内置集合（`terraform destroy`、`systemctl restart postgres`……）。

**灰色地带由你决定。** 内置分类器是起步集，刻意保持保守标记、也刻意不留不可覆盖的 deny 规则：在哪台机器上判得不准，就按连接豁免或扩展。硬控制始终是你自己配的 `commandWhitelist` / `commandBlacklist` 与路径限制。闸门只改变**是否先问人**；`auto` 模式下绝不静默改写或拦截任何命令。

**设计上 fail-open。** 客户端未声明 elicitation 能力（或请求失败）时，命令照常执行，结果附提示告知 agent 在该连接上执行破坏性操作前应先询问用户。客户端能力缺失不能把运维者锁在门外；`list-servers` 会标注每个连接的 `approvalMode` 与当前客户端能否弹窗，agent 执行前即知晓。

## 工具注解

| 工具 | readOnly | destructive |
|---|---|---|
| `list-servers` | yes | — |
| `execute-command` | no | yes (hint) |
| `session` | no | no（read 操作实际只读） |
| `file-transfer` | no | yes (hint；上传会覆盖) |

支持 MCP 注解的客户端可据此自动拦截或确认破坏性工具。

## 输出脱敏

脱敏是**选配**（连接配置 `"redactSecrets": true`；默认关闭——扫描含密钥的输出约每 MiB 花 200ms）。开启后，输出返回客户端或落盘前会掩码：

- `Bearer <token>` 头及类似 JWT 片段
- PEM 块（`-----BEGIN … PRIVATE KEY-----`）
- 匹配 `password=` / `token=` / `secret=` / `api_key=` 的行（大小写不敏感）

脱敏是尽力而为。高度敏感的环境不要把它当唯一控制：脱敏关闭时，落盘文件与 MCP 结果都是原始输出（落盘文件在工作目录下，权限 0600）。

## 配置文件权限

启动且指定 `--config-file` 时，服务端校验：

- **Unix：** 配置文件与父目录的权限位不含 group/other
- **Windows：** 咨询 `icacls`；`Everyone` / `Authenticated Users` 可修改该文件则拒绝启动

`--allow-insecure-config-perms` 仅用于本地开发；同一开关也可在配置文件（对象格式）内用 `"$global": {"allowInsecureConfigPerms": true}` 声明。检查发生在文件解析之后，所以在配置里关闭它会被视为用户的刻意选择。

## 审计日志（防篡改命令记录）

每个连接在 `<commandLogDir>/<connection>.jsonl` 维护追加式 JSONL 审计日志（Unix 权限 0600）。每行记录连接、工具（`execute-command` / `file-transfer` / `session`）、命令或传输摘要、审批结论、退出码与成功标志——**绝不记录命令输出**（输出可能含密钥；脱敏不是审计的职责）。

每行携带 `prevHash`——前一行 canonical JSON 的 SHA-256——构成以 64 个 0 锚定的哈希链。记录经一个小缓冲写入，审计成本是每条命令微秒级，而不是文件系统往返；落盘策略用户可配（见[加固分级](#加固分级)）。事后改动任何一行（命令文本、退出码、删除）都会断链，并可定位到：

```
2native-ssh-mcp audit-verify --file .ssh-mcp-logs/prod.jsonl
2native-ssh-mcp audit-verify --config-file config.json   # 所有连接，含滚动文件
```

退出码 2 表示断链，或文件是旧格式（无链）日志；stderr 会给出文件、行号、期望与实际哈希。**刻意不提供修复工具**：断链就是证据，留底待查。失败命令始终落盘（审批拒绝也记，`approval=declined`）；`commandLogOnlySuccess` 只影响 `list-servers` 的展示窗口。链覆盖的是本服务端执行过的东西——不追溯证明日志之外发生的事，也不是合规认证。

### 审计落盘策略

审计日志先入缓冲、批量落盘，命令因此从不等文件系统——除非你要求。策略按连接配置（`commandLogFlushMs`，全局 CLI `--command-log-flush-ms`）；缓冲额外在 32 条或 256 KiB 先到时落盘，每次优雅退出（`DisconnectAll`、daemon stop）都会补写尾部。

| 策略 | 配置 | 持久性 | 实测单条成本 |
|---|---|---|---|
| 批量（默认） | 不配，或 `commandLogFlushMs: 5000` | 硬崩溃（`kill -9`、断电）最多丢最后 ≤5 秒；磁盘上的链始终可校验 | ~8 µs |
| 写穿 | `commandLogFlushMs: 0` | 每条记录在命令结果返回前已在磁盘——最严格 | ~13 µs |
| 更长批量 | 如 `commandLogFlushMs: 60000` | 磁盘更安静，暴露窗口最长 60 秒 | ~8 µs |
| 关闭审计 | `commandLogSize: 0` | 完全没有执行轨迹 | 0 |

实测于桌面 Windows 机器（AMD Ryzen 5 4600H）；被替换的旧实现每条记录全量重写文件、成本毫秒级。这些数字只在容量决策时有意义——任何策略都被 SSH 往返时间淹没。

## 加固分级

按部署场景选一级；除注明外每个开关都按连接配置。分级是累积的——每一级包含更低级的一切。

**Level 0 — 基线（默认值）**

- 审计链开启：`commandLogSize` 不配（默认 20 条展示窗口），`commandLogFlushMs` 不配（批量 5 秒落盘）。
- 主机密钥：`hostKeyCheck: "accept-new"`（首连记录，之后变更即失败）。
- `approvalMode: "auto"`——agent 执行前不询问。
- 适用于个人机器与开发沙箱。

**Level 1 — 加固**

- `commandLogFlushMs: 0`——写穿审计：命令与记录之间没有任何东西能打挂进程。
- `approvalMode: "ask-destructive"`，配 `approvalPatterns` 补内置分类器漏掉的；`approvalExemptPatterns` 让常规变更类命令免打扰。
- 首次接触记录后切 `hostKeyCheck: "strict"`。
- 命令会打印凭据的连接开 `redactSecrets: true`；`stripAnsi` 保持开启（默认）。
- `commandLogOnlySuccess: false`（默认），让展示窗口也包含失败记录。

**Level 2 — 严格（内部审计 / 证据级）**

- 专用 `commandLogDir`，目录 ACL 只允许服务账号（Unix：目录 `0700`，日志文件写 `0600`）——与 agent 可读的工作区分开，记录的存续与可信度高于 agent 的篡改能力。
- 定时校验：用 cron / 计划任务跑 `2native-ssh-mcp audit-verify --config-file config.json`，退出码 2 告警。没人查的链只算半个控制。
- 每个滚动窗口结束后，把滚出的 `*.jsonl.<utc>` 归档搬离主机；哈希链让事后篡改归档可被发现，归档之间还能互相比对。
- HTTP 传输只在回环地址 + `--http-token` 下使用；能 stdio 就 stdio。
- `localPathMode: "list"` 加显式 `allowedRemotePaths`，传输无处乱跑。
- 永远不要设 `--allow-insecure-config-perms` / `$global.allowInsecureConfigPerms`。
- 不要在一次性环境之外关闭审计（`commandLogSize: 0`）——那时就完全没有执行轨迹了。

## 漏洞报告

开 GitHub issue 或私下联系维护者。漏洞报告里不要带凭据。

## 延伸阅读

- [Simon Willison — the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
- [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) — 安全配置实操
- [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) — agent 工具速查

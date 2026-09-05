> [English](FEATURES.en.md)

# 特性详情与已知限制

配置字段的完整语义见 [AGENT_GUIDE](AGENT_GUIDE.md)（速查）与 [HUMAN_GUIDE](HUMAN_GUIDE.md)（易读版）；安全设计见 [SECURITY.md](../SECURITY.md)。

## 功能特性

- **双传输模式**：stdio（MCP 客户端子进程）与 streamable HTTP（常驻服务）
- **四个工具**（最小必要集，与上游 classfang 对齐 + 会话合并）：
  - `list-servers` — 服务器 + 活动会话一览
  - `execute-command` — 远程执行；可选 `sessionName` 在有状态会话中执行；`background: true` 把命令转入后台作业并立即返回 `sessionName`/`logPath`——**后台长任务是本工具的特殊模式**，不是单独的工具
  - `session` — 会话生命周期 `action=open|read|close|list`（后台日志轮询用 `read`，支持 `waitMs` 阻塞等新输出，免空转轮询）
  - `file-transfer` — 上传/下载，带进度通知、去重、断点续传（续传打在远端 `<目标>.part` 上）；**目录递归传输**（自动建远端父目录，逐文件原子落盘，单文件失败不中断整批，结果聚合 `files`/`failed`）；上传原子落盘（同目录 `.part` + posix-rename，失败不截断已有目标）；单文件传输后 sha256 双端校验（远端无 `sha256sum` 标 `unverified`，mismatch 硬报错）
- **破坏性命令审批（MCP elicitation，可选）**：按连接配置 `approvalMode: "ask-destructive"`，`execute-command` 执行分类为破坏性的命令前向人类弹 elicitation 确认（拒绝/取消则不执行，并告知 agent 用户已拒绝）；分类器 = 内置规则 + `approvalPatterns`（用户扩展"要问"的）+ `approvalExemptPatterns`（用户豁免"不问"的，优先级最高），灰色地带完全由用户决定；客户端不支持 elicitation 时**fail-open**照常执行、结果附提示；`list-servers` 会标注每个连接的模式与客户端是否支持弹窗。见 [SECURITY.md 审批闸门](../SECURITY.md#approval-gate-destructive-commands)
- **Agent Skill**：[`skills/2native-ssh-mcp-helper`](../skills/2native-ssh-mcp-helper/SKILL.md)（安装配置）、[`skills/2native-ssh-mcp-agent`](../skills/2native-ssh-mcp-agent/SKILL.md)（远程执行防御与输出/token 策略）
- **连接生命周期**：懒连接（首次调用才建立），执行后按 `keepAlive`/`keepAliveDuration` 保活（默认 10 分钟），空闲自动断开；执行中的命令受 in-flight 保护，不会被空闲/心跳误拆
- **可靠性**：TCP keepalive + 应用层心跳（OpenSSH 语义：任意回复即存活，单 in-flight 发送）；前台超时按远端 PID 杀掉整个进程组（channel Signal 仅作补充，OpenSSH 经常忽略它）；exec 默认不分配 PTY（连接配置 `pty: true` 或工具参数 `pty` 按需开启；后台任务可用 `pty: true` 经 `script` 包一层 TTY）；后台任务以无 PTY 独立通道启动（setsid 脱离会话），**连接闪断后仍存活**，会话可自动重连重附着；非 0 退出码是正常结果（看 `exitCode`），连接中断报 `SSH_CONNECTION_LOST`（`retriable=false`，带部分输出，不可盲目重放）
- **会话保留**：后台作业结束后会话与远端日志保留 60 分钟，`read` 可 `offset=0` 重读（JSON 带 `logPath`/`exitCode`），`close` 幂等可重复调用
- **输出处理**：默认剥离 ANSI 转义序列（颜色/进度条，`stripAnsi: false` 可关）；大输出分层处理——≥4KB 头尾摘要压缩（`outputCompressLight`/`outputCompressThreshold`），≥8KB（`outputSpillThreshold`，`-1` 关闭）把完整输出落盘到本地目录（默认 `.ssh-mcp-out/`，`outputSpillDir` 可改，保留最近 32 个），MCP 结果只留通知+短预览，Agent 用本地 Read/Grep 查看全文，不必远程重跑
- **命令日志**：按连接记录最近 N 条执行过的命令（不含输出），可只记成功命令，落盘为 JSON 行文件（配置：`commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`，或全局 `--command-log-size` 等 CLI 参数）
- **安全**：命令白/黑名单、路径白名单（本地/远端，本地范围可配 `localPathMode`：cwd / list / any）、凭据隔离（SSH 凭据留在本地，不暴露给模型）、输出脱敏（`redactSecrets`，默认关闭——脱敏扫描对含密钥的大输出有明显开销，需要时按连接开启）、配置权限检查（Unix `0600`/`0700`，Windows ACL；可用 `--allow-insecure-config-perms` 或配置文件 `$global.allowInsecureConfigPerms` 跳过）
- **认证与兼容性**：密码/私钥/ssh-agent/Pageant/键盘交互认证（2FA）、代理（SOCKS5/HTTP/HTTPS）、算法协商配置（兼容老服务器）
- **文件传输性能**：SFTP 并发传输（`sftpConcurrency`/`sftpChunkSize`，高延迟链路提速；大文件自动启用并发写）、SFTP 客户端按连接池化（空闲 5 分钟回收）、`sftpTimeoutMs` 为**无进展超时**而非总时长（健康的大文件传输不再被墙钟切断）、断点续传、去重、进度通知
- **HTTP daemon**：`start/stop/status/kill` 子命令 + 引用计数 + PID 文件 + 健康检查端点；`install` 一键注册 Windows 开机自启
- **自动发布**：推送带消息的 tag 即触发 GitHub Actions 构建 6 平台二进制并创建 Release（日志用 tag 消息），详见 [DEVELOPMENT.md](DEVELOPMENT.md#自动发布-release)

## 已知限制

- Go 的 `x/crypto/ssh` 不支持 SSH zlib 压缩（[golang/go#22795](https://github.com/golang/go/issues/22795)）；高延迟/低带宽场景建议依赖 TCP keepalive 与 SFTP 并发传输（`sftpConcurrency`）缓解
- 主机密钥默认按 `accept-new` 校验（首次连接记录到 `known_hosts`，之后密钥变更会被拒绝）；动态 IP / 频繁换密钥的机器需设 `"hostKeyCheck": "none"`
- shell 传输模式下不支持 SFTP 上传/下载
- 本地 WSL **没有**单独传输模式：当普通 Linux SSH 目标用（发行版内 sshd）。不要用 `"command": "wsl"` 拉起本进程；`file-transfer` 的 `localPath` 是 MCP 所在 OS 的路径。端口、localhost、9P 路径等见 [HUMAN_GUIDE 连接本地 WSL](HUMAN_GUIDE.md#连接本地-wsl)

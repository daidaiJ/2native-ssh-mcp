> [English](README.md)

# 2native-ssh-mcp

[![CI](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/daidaiJ/2native-ssh-mcp)](https://github.com/daidaiJ/2native-ssh-mcp/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/daidaiJ/2native-ssh-mcp)](go.mod)
[![License](https://img.shields.io/github/license/daidaiJ/2native-ssh-mcp)](LICENSE)
[![SLSA](https://img.shields.io/badge/SLSA-provenance-brightgreen)](https://github.com/daidaiJ/2native-ssh-mcp/attestations)
[![MCP Registry](https://img.shields.io/badge/MCP_Registry-listed-555)](https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp)

基于 SSH 的 MCP (Model Context Protocol) 服务器，Go 实现。让 AI 助手通过 MCP 协议远程执行命令、传输文件，SSH 凭据完全留在本地，不暴露给模型。

> 本项目参考了 [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server)（TypeScript 版）的设计与实现，在其基础上用 Go 重写，并将文件操作整合为**单个工具**、支持**进度通知**。感谢原作者的开源贡献。

## 📖 指南

| 读者 | 文档 | 说明 |
|---|---|---|
| 🤖 AI Agent | [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) | 省 token 版：工具参数、配置、安全模式、部署命令速查 |
| 👤 人类用户 | [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) | 易读版：安全配置、快速开始、工具说明、参数速查、发布流程 |

## 特性

- **双传输模式**：stdio（MCP 客户端子进程）与 streamable HTTP（常驻服务）
- **四个工具**（最小必要集，与上游 classfang 对齐 + 会话合并）：
  - `list-servers` — 服务器 + 活动会话一览
  - `execute-command` — 远程执行；可选 `sessionName` 在有状态会话中执行
  - `session` — 会话生命周期 `action=open|read|close|list`（含后台长任务，断连后仍可重附着；`read` 支持 `waitMs` 阻塞等新输出，免空转轮询）
  - `file-transfer` — 上传/下载，带进度通知、去重、断点续传（续传打在远端 `<目标>.part` 上）；**目录递归传输**（自动建远端父目录，逐文件原子落盘，单文件失败不中断整批，结果聚合 `files`/`failed`）；上传原子落盘（同目录 `.part` + posix-rename，失败不截断已有目标）；单文件传输后 sha256 双端校验（远端无 `sha256sum` 标 `unverified`，mismatch 硬报错）
- **Agent Skill**：[`skills/2native-ssh-mcp-helper`](skills/2native-ssh-mcp-helper/SKILL.md)（安装配置）、[`skills/2native-ssh-mcp-agent`](skills/2native-ssh-mcp-agent/SKILL.md)（远程执行防御与输出/token 策略）
- **连接生命周期**：懒连接（首次调用才建立），执行后按 `keepAlive`/`keepAliveDuration` 保活（默认 10 分钟），空闲自动断开；执行中的命令受 in-flight 保护，不会被空闲/心跳误拆
- **可靠性**：TCP keepalive + 应用层心跳（OpenSSH 语义：任意回复即存活，单 in-flight 发送）；前台超时按远端 PID 杀掉整个进程组（channel Signal 仅作补充，OpenSSH 经常忽略它）；exec 默认不分配 PTY（连接配置 `pty: true` 或工具参数 `pty` 按需开启；后台任务可用 `pty: true` 经 `script` 包一层 TTY）；后台任务以无 PTY 独立通道启动（setsid 脱离会话），**连接闪断后仍存活**，会话可自动重连重附着；非 0 退出码是正常结果（看 `exitCode`），连接中断报 `SSH_CONNECTION_LOST`（`retriable=false`，带部分输出，不可盲目重放）
- **会话保留**：后台作业结束后会话与远端日志保留 60 分钟，`read` 可 `offset=0` 重读（JSON 带 `logPath`/`exitCode`），`close` 幂等可重复调用
- **输出处理**：默认剥离 ANSI 转义序列（颜色/进度条，`stripAnsi: false` 可关）；大输出分层处理——≥4KB 头尾摘要压缩（`outputCompressLight`/`outputCompressThreshold`），≥8KB（`outputSpillThreshold`，`-1` 关闭）把完整输出落盘到本地目录（默认 `.ssh-mcp-out/`，`outputSpillDir` 可改，保留最近 32 个），MCP 结果只留通知+短预览，Agent 用本地 Read/Grep 查看全文，不必远程重跑
- **命令日志**：按连接记录最近 N 条执行过的命令（不含输出），可只记成功命令，落盘为 JSON 行文件（配置：`commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`，或全局 `--command-log-size` 等 CLI 参数）
- **安全**：命令白/黑名单、路径白名单（本地/远端，本地范围可配 `localPathMode`：cwd / list / any）、凭据隔离（SSH 凭据留在本地，不暴露给模型）、输出脱敏（`redactSecrets`，默认关闭——脱敏扫描对含密钥的大输出有明显开销，需要时按连接开启）、配置权限检查（Unix `0600`/`0700`，Windows ACL；可用 `--allow-insecure-config-perms` 或配置文件 `$global.allowInsecureConfigPerms` 跳过，见 [SECURITY.md](SECURITY.md)）
- **认证与兼容性**：密码/私钥/ssh-agent/Pageant/键盘交互认证（2FA）、代理（SOCKS5/HTTP/HTTPS）、算法协商配置（兼容老服务器）
- **文件传输性能**：SFTP 并发传输（`sftpConcurrency`/`sftpChunkSize`，高延迟链路提速；大文件自动启用并发写）、SFTP 客户端按连接池化（空闲 5 分钟回收）、`sftpTimeoutMs` 为**无进展超时**而非总时长（健康的大文件传输不再被墙钟切断）、断点续传、去重、进度通知
- **HTTP daemon**：`start/stop/status/kill` 子命令 + 引用计数 + PID 文件 + 健康检查端点；`install` 一键注册 Windows 开机自启
- **自动发布**：推送带消息的 tag 即触发 GitHub Actions 构建 6 平台二进制并创建 Release（日志用 tag 消息）

## 快速上手

```bash
# 构建
go build -o 2native-ssh-mcp.exe .

# stdio 模式（MCP 客户端拉起，凭据放 config.json 或环境变量）
2native-ssh-mcp.exe --config-file config.json

# HTTP 常驻服务
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338
```

详细配置与部署步骤见上方两份指南。

## 自动发布 Release

推送**带消息的 tag**（annotated tag）即触发 GitHub Actions（`.github/workflows/release.yml`）：

```bash
git tag -a v1.0.1 -m "修复了 xxx"
git push origin v1.0.1
```

**重要说明**：

- **Release 日志 = tag 的 message**。工作流通过 GitHub API 读取 annotated tag 对象的消息（不用本地 git——runner 检出时可能只有 lightweight tag 引用，`%(contents)` 会回退成 commit message，导致日志变成提交信息）
- 构建 **6 个平台**的二进制：Windows / Linux / macOS × amd64 / arm64（`CGO_ENABLED=0`，版本号注入为 tag 名），产物命名 `2native-ssh-mcp-<os>-<arch>[.exe]`
- 每个二进制附带独立的 `.sha256` 校验和文件
- 每个二进制同时打包为 **`.mcpb` bundle**（MCP Bundle：zip + `manifest.json`，`server.type: "binary"`），附带独立 `.sha256`
- 另附一份 CycloneDX SBOM（`2native-ssh-mcp.cdx.json`）；SLSA provenance 与 SBOM 证明发布在仓库 [Attestations](https://github.com/daidaiJ/2native-ssh-mcp/attestations) 页（不往 Release 里塞 `.intoto` 文件）。校验下载的二进制：`gh attestation verify --repo daidaiJ/2native-ssh-mcp <file>`
- 用 `git tag -a -m` 打**带消息的 tag**；轻量 tag（`git tag v1.0.1`）没有消息，日志会退化为 tag 名

### 发布到官方 MCP Registry

发布到 [registry.modelcontextprotocol.io](https://registry.modelcontextprotocol.io) 是**显式且独立**的动作：普通 tag 只构建 release；带 `-registry` 后缀的 tag 才会把对应的稳定 release 发布到官方 MCP Registry（OIDC 认证，无需密钥）：

```bash
git tag -a v1.0.1 -m "发布 v1.0.1 到 MCP Registry"
git push origin v1.0.1-registry
```

`v1.0.1-registry` tag 触发 `publish-registry` job：从已存在的 `v1.0.1` release 下载 `.mcpb` 产物，生成 `server.json`（6 平台包条目），以 `io.github.daidaiJ/2native-ssh-mcp` 发布。验证：

```bash
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp"
```

## 项目结构

```
2native-ssh-mcp/
├── main.go                        # 入口：子命令分发（start/stop/status/install…）+ stdio/HTTP 双 transport
├── internal/
│   ├── config/                    # SSH 配置类型、默认值、CLI 参数解析、${ENV_VAR} 引用
│   ├── sshconfig/                 # ~/.ssh/config 解析（Include、通配符、first-match-wins）
│   ├── logger/                    # stderr 日志（不污染 stdio 协议）
│   ├── manager/                   # 连接管理核心
│   │   ├── manager.go             #   懒连接、空闲保活、路径校验、命令日志
│   │   ├── dial.go                #   认证（密码/私钥/agent/Pageant/2FA）、代理（SOCKS5/HTTP/HTTPS）
│   │   ├── hostkey.go             #   主机密钥校验（accept-new/strict/none，known_hosts）
│   │   ├── exec.go                #   exec 模式命令执行（超时/输出上限/退出码）
│   │   ├── shell.go               #   shell 模式（marker 协议、ANSI 清理、串行队列）
│   │   ├── background.go          #   后台任务（detached exec、日志轮询、停止）
│   │   ├── session.go             #   命名会话（CWD 保持、断连重附着、空闲 TTL）
│   │   ├── heartbeat.go           #   应用层心跳（keepalive 语义、in-flight 保护）
│   │   ├── result.go              #   结构化结果（退出码、状态、ANSI 剥离）
│   │   ├── compress.go            #   大输出头尾压缩
│   │   ├── redact.go              #   输出脱敏
│   │   ├── sftp.go                #   文件传输（并发拷贝、进度、去重、断点续传）
│   │   ├── status.go              #   远程系统状态采集（hostname/OS/内存/磁盘等）
│   │   └── commandlog.go          #   命令日志文件（JSON 行、保留最近 N 条）
│   ├── daemon/                    # HTTP daemon：PID 文件、refcount+guest 租约、admin 端点、Windows 自启动
│   └── tools/                     # MCP 工具：list-servers / execute-command / session / file-transfer
├── SECURITY.md                    # 威胁模型与安全建议
├── docs/
│   ├── AGENT_GUIDE.md             # 🤖 Agent 版指南（省 token，按需读取）
│   └── HUMAN_GUIDE.md             # 👤 人类版指南（易读）
├── .github/workflows/ci.yml       # push/PR → ubuntu-latest go test -race ./...
├── .github/workflows/release.yml  # tag 推送 → 6 平台构建 + Release（日志 = tag 消息）
├── plan/                          # 设计规格（HARDENING.md 等，git-ignored）
├── todo/                          # 实施任务清单 T01–T10（git-ignored）
├── LICENSE                        # ISC（含上游版权声明）
└── README.md
```

## 构建与测试

```bash
go build ./...
go test -race ./...
```

## 已知限制

- Go 的 `x/crypto/ssh` 不支持 SSH zlib 压缩（[golang/go#22795](https://github.com/golang/go/issues/22795)）；高延迟/低带宽场景建议依赖 TCP keepalive 与 SFTP 并发传输（`sftpConcurrency`）缓解
- 主机密钥默认按 `accept-new` 校验（首次连接记录到 `known_hosts`，之后密钥变更会被拒绝）；动态 IP / 频繁换密钥的机器需设 `"hostKeyCheck": "none"`
- shell 传输模式下不支持 SFTP 上传/下载
- 本地 WSL **没有**单独传输模式：当普通 Linux SSH 目标用（发行版内 sshd）。不要用 `"command": "wsl"` 拉起本进程；`file-transfer` 的 `localPath` 是 MCP 所在 OS 的路径。端口、localhost、9P 路径等见 [HUMAN_GUIDE 连接本地 WSL](docs/HUMAN_GUIDE.zh-CN.md#连接本地-wsl)

## 参考项目

- [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) — 本项目参考的 TypeScript 版 SSH MCP Server，工具设计、错误码、路径校验、shell 传输协议等均源自该项目

## License

ISC License（与上游一致，保留上游版权声明，见 [LICENSE](LICENSE)）
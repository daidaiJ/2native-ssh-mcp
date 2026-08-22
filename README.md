# 2native-ssh-mcp

基于 SSH 的 MCP (Model Context Protocol) 服务器，Go 实现。让 AI 助手通过 MCP 协议远程执行命令、传输文件，SSH 凭据完全留在本地，不暴露给模型。

> 本项目参考了 [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server)（TypeScript 版）的设计与实现，在其基础上用 Go 重写，并将文件操作整合为**单个工具**、支持**进度通知**。感谢原作者的开源贡献。

## 📖 指南

| 读者 | 文档 | 说明 |
|---|---|---|
| 🤖 AI Agent | [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) | 省 token 版：工具参数、配置、安全模式、部署命令速查 |
| 👤 人类用户 | [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) | 易读版：安全配置、快速开始、工具说明、参数速查、发布流程 |

## 特性

- **双传输模式**：stdio（MCP 客户端子进程）与 streamable HTTP（常驻服务）
- **三个工具**：
  - `execute-command` — 远程执行命令，支持白/黑名单、超时、输出上限、工作目录
  - `file-transfer` — 上传/下载整合为一个工具，带 **MCP 进度通知**、**去重**（大小+mtime 一致自动跳过）、**断点续传**、`force` 强制全量
  - `list-servers` — 服务器列表与系统状态（主机名/OS/内存/磁盘等）
- **连接生命周期**：懒连接（首次调用才建立），执行后按 `keepAlive`/`keepAliveDuration` 保活（默认 10 分钟），空闲自动断开
- **命令日志**：按连接记录最近 N 条执行过的命令（不含输出），可只记成功命令，落盘为 JSON 行文件
- **安全**：命令白/黑名单、本地/远端路径白名单、凭据不进 MCP 配置参数（支持 `${ENV_VAR}` 环境变量引用）
- **实用 SSH 特性**：TCP keepalive、心跳检测、算法协商配置（兼容老服务器）、SFTP 并发传输（高延迟链路提速）、代理（SOCKS5/HTTP/HTTPS）、Pageant/ssh-agent、键盘交互认证（2FA）
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
- 用 `git tag -a -m` 打**带消息的 tag**；轻量 tag（`git tag v1.0.1`）没有消息，日志会退化为 tag 名

## 项目结构

```
2native-ssh-mcp/
├── main.go                        # 入口：子命令分发（start/stop/status/install…）+ stdio/HTTP 双 transport
├── internal/
│   ├── config/                    # SSH 配置类型、默认值、CLI 参数解析、${ENV_VAR} 引用
│   ├── sshconfig/                 # ~/.ssh/config 解析（Include、通配符、first-match-wins）
│   ├── logger/                    # stderr 日志（不污染 stdio 协议）
│   ├── manager/                   # 连接管理核心
│   │   ├── manager.go             #   懒连接、空闲保活、心跳、路径校验、命令日志
│   │   ├── dial.go                #   认证（密码/私钥/agent/Pageant/2FA）、代理（SOCKS5/HTTP/HTTPS）
│   │   ├── exec.go                #   exec 模式命令执行（超时/输出上限/退出码）
│   │   ├── shell.go               #   shell 模式（marker 协议、ANSI 清理、串行队列）
│   │   ├── sftp.go                #   文件传输（并发拷贝、进度、去重、断点续传）
│   │   ├── status.go              #   远程系统状态采集（hostname/OS/内存/磁盘等）
│   │   └── commandlog.go          #   命令日志文件（JSON 行、保留最近 N 条）
│   ├── daemon/                    # HTTP daemon：PID 文件、refcount、admin 端点、Windows 自启动
│   └── tools/                     # MCP 工具注册：execute-command / file-transfer / list-servers
├── docs/
│   ├── AGENT_GUIDE.md             # 🤖 Agent 版指南（省 token，按需读取）
│   └── HUMAN_GUIDE.md             # 👤 人类版指南（易读）
├── .github/workflows/release.yml  # tag 推送 → 6 平台构建 + Release（日志 = tag 消息）
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
- 主机密钥不做校验（与参考实现一致）
- shell 传输模式下不支持 SFTP 上传/下载

## 参考项目

- [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) — 本项目参考的 TypeScript 版 SSH MCP Server，工具设计、错误码、路径校验、shell 传输协议等均源自该项目

## License

ISC License（与上游一致，保留上游版权声明，见 [LICENSE](LICENSE)）
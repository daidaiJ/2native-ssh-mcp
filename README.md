# ssh-mcp-server-go

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
go build -o ssh-mcp-server-go.exe .

# stdio 模式（MCP 客户端拉起，凭据放 config.json 或环境变量）
ssh-mcp-server-go.exe --config-file config.json

# HTTP 常驻服务
ssh-mcp-server-go.exe start --config-file config.json --http-addr 127.0.0.1:8338
```

详细配置与部署步骤见上方两份指南。

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

ISC
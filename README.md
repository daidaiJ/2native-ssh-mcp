> [English](README.en.md)

# 2native-ssh-mcp

[![CI](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/daidaiJ/2native-ssh-mcp)](https://github.com/daidaiJ/2native-ssh-mcp/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/daidaiJ/2native-ssh-mcp)](go.mod)
[![License](https://img.shields.io/github/license/daidaiJ/2native-ssh-mcp)](LICENSE)
[![SLSA](https://img.shields.io/badge/SLSA-provenance-brightgreen)](https://github.com/daidaiJ/2native-ssh-mcp/attestations)
[![MCP Registry](https://img.shields.io/badge/MCP_Registry-listed-555)](https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp)

基于 SSH 的 MCP (Model Context Protocol) 服务器，Go 实现。让 AI 助手通过 MCP 协议远程执行命令、传输文件，SSH 凭据完全留在本地，不暴露给模型。

> 本项目参考了 [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server)（TypeScript 版）的设计与实现，在其基础上用 Go 重写，并将文件操作整合为**单个工具**、支持**进度通知**。感谢原作者的开源贡献。

## 📖 文档

| 读者 | 文档 | 说明 |
|---|---|---|
| 🤖 AI Agent | [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) | 省 token 版：工具参数、配置、安全模式、部署命令速查 |
| 👤 人类用户 | [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) | 易读版：安全配置、快速开始、工具说明、参数速查、发布流程 |
| 🔍 特性详情 | [docs/FEATURES.md](docs/FEATURES.md) | 完整特性列表与已知限制（英文版 .en.md） |
| 🛠 开发与发布 | [docs/DEVELOPMENT.md](docs/DEVELOPMENT.md) | 项目结构、构建测试、Release 与 MCP Registry 发布流程 |
| 🔐 安全 | [SECURITY.md](SECURITY.md) | 威胁模型、审批闸门、加固建议 |

各文档均为中文默认，同名 `.en.md` 为英文版。

## ✨ 核心特性

- **四个工具**：`list-servers` / `execute-command` / `session` / `file-transfer`；后台长任务是 `execute-command` 的 `background: true` 特殊模式，不是第五个工具
- **远程执行**：懒连接 + keepalive、超时按远端 PID 杀进程组、默认不分配 PTY、后台任务断连存活可重附着
- **输出处理**：ANSI 剥离、大输出落盘到本地（`.ssh-mcp-out/`），Agent 用 Read/Grep 查全文，不远程重跑
- **文件传输**：原子落盘、去重、断点续传、目录递归、sha256 校验、进度通知
- **破坏性命令审批（可选）**：`approvalMode: "ask-destructive"` 经 MCP elicitation 弹窗确认，内置分类器 + 用户扩展/豁免，灰色地带用户自定；客户端不支持时 fail-open
- **安全**：命令白/黑名单、路径白名单、凭据隔离、配置权限检查
- **双传输**：stdio / streamable HTTP daemon（引用计数、健康检查、Windows 一键自启）
- **认证兼容**：密码/私钥/ssh-agent/Pageant/2FA、代理、算法协商（兼容老服务器）

完整特性列表见 [docs/FEATURES.md](docs/FEATURES.md)。

## 🚀 快速上手

```bash
# 构建
go build -o 2native-ssh-mcp.exe .

# stdio 模式（MCP 客户端拉起，凭据放 config.json 或环境变量）
2native-ssh-mcp.exe --config-file config.json

# HTTP 常驻服务
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338
```

详细配置与部署步骤见上方两份指南。

## License

ISC License（与上游一致，保留上游版权声明，见 [LICENSE](LICENSE)）

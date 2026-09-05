> [中文](README.md)

# 2native-ssh-mcp

[![CI](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/daidaiJ/2native-ssh-mcp)](https://github.com/daidaiJ/2native-ssh-mcp/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/daidaiJ/2native-ssh-mcp)](go.mod)
[![License](https://img.shields.io/github/license/daidaiJ/2native-ssh-mcp)](LICENSE)
[![SLSA](https://img.shields.io/badge/SLSA-provenance-brightgreen)](https://github.com/daidaiJ/2native-ssh-mcp/attestations)
[![MCP Registry](https://img.shields.io/badge/MCP_Registry-listed-555)](https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp)

An SSH-based MCP (Model Context Protocol) server in Go. Lets AI assistants run remote commands and transfer files over MCP while SSH credentials stay entirely local, never exposed to the model.

> This project draws on the design of [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) (TypeScript), rewritten in Go with file operations consolidated into a **single tool** and **progress notifications**. Thanks to the original author for open-sourcing it.

## 📖 Docs

| Reader | Doc | Notes |
|---|---|---|
| 🤖 AI Agent | [docs/AGENT_GUIDE.en.md](docs/AGENT_GUIDE.en.md) | Token-efficient: tool params, config, secure setup, deployment |
| 👤 Human users | [docs/HUMAN_GUIDE.en.md](docs/HUMAN_GUIDE.en.md) | Readable: secure setup, quick start, tool reference, release flow |
| 🔍 Features | [docs/FEATURES.en.md](docs/FEATURES.en.md) | Full feature list and known limitations |
| 🛠 Development | [docs/DEVELOPMENT.en.md](docs/DEVELOPMENT.en.md) | Project structure, build & test, Release and MCP Registry publishing |
| 🔐 Security | [SECURITY.md](SECURITY.md) | Threat model, approval gate, hardening recommendations |

Docs are Chinese by default; the same-name `.en.md` files are the English versions.

## ✨ Highlights

- **Four tools**: `list-servers` / `execute-command` / `session` / `file-transfer`; background long-running work is a special mode of `execute-command` (`background: true`), not a fifth tool
- **Remote execution**: lazy connection + keepalive, timeouts kill the remote process group by PID, no PTY by default, background jobs survive disconnects and reattach
- **Output handling**: ANSI stripped, large output spilled to local files (`.ssh-mcp-out/`) for the agent to Read/Grep instead of re-running remotely
- **File transfer**: atomic landing, deduplication, resumable transfer, recursive directories, sha256 verification, progress notifications
- **Destructive-command approval (optional)**: `approvalMode: "ask-destructive"` prompts via MCP elicitation; built-in classifier plus user extensions/exemptions — gray areas are yours to decide; fails open when the client cannot prompt
- **Security**: command whitelist/blacklist, path whitelisting, credential isolation, config permission checks
- **Dual transport**: stdio / streamable HTTP daemon (refcounting, health check, one-click Windows autostart)
- **Auth & compatibility**: password/private key/ssh-agent/Pageant/2FA, proxies, algorithm negotiation (legacy servers)

Full feature list: [docs/FEATURES.en.md](docs/FEATURES.en.md).

## 🚀 Quick Start

```bash
# Build
go build -o 2native-ssh-mcp.exe .

# stdio mode (launched by MCP client, credentials in config.json or environment variables)
2native-ssh-mcp.exe --config-file config.json

# HTTP persistent daemon
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338
```

See the two guides above for detailed configuration and deployment steps.

## License

ISC License (same as upstream, upstream copyright notice preserved, see [LICENSE](LICENSE))

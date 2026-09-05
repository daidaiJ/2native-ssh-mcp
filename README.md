> [中文版](README.zh-CN.md)

# 2native-ssh-mcp

[![CI](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml/badge.svg)](https://github.com/daidaiJ/2native-ssh-mcp/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/daidaiJ/2native-ssh-mcp)](https://github.com/daidaiJ/2native-ssh-mcp/releases/latest)
[![Go](https://img.shields.io/github/go-mod/go-version/daidaiJ/2native-ssh-mcp)](go.mod)
[![License](https://img.shields.io/github/license/daidaiJ/2native-ssh-mcp)](LICENSE)
[![SLSA](https://img.shields.io/badge/SLSA-provenance-brightgreen)](https://github.com/daidaiJ/2native-ssh-mcp/attestations)
[![MCP Registry](https://img.shields.io/badge/MCP_Registry-listed-555)](https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp)

SSH-based MCP (Model Context Protocol) server implemented in Go. Enables AI assistants to remotely execute commands and transfer files via the MCP protocol, while keeping SSH credentials **locally on your machine** — never exposed to the model.

> This project references the design and implementation from [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) (TypeScript version). It's a complete rewrite in Go with consolidated file operations into a **single tool** and added **progress notifications**. Thanks to the original author for their open-source contribution.

## 📖 Guides

| Audience | Document | Description |
|---|---|---|
| 🤖 AI Agent | [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) | Token-efficient version: Quick reference for tool parameters, configuration, security modes, and deployment commands |
| 👤 Human User | [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) | Readable version: Security configuration, quick start, tool documentation, parameter reference, release process |

## Features

- **Dual transport modes**: stdio (MCP client subprocess) and streamable HTTP (persistent daemon)
- **Four tools** (minimal necessary set, aligned with upstream classfang + session consolidation):
  - `list-servers` — List servers + active sessions
  - `execute-command` — Remote execution; optional `sessionName` to execute within a stateful session
  - `session` — Session lifecycle with `action=open|read|close|list` (supports long-running background tasks that can be reattached after disconnection; `read` supports `waitMs` to block for new output instead of empty polling)
  - `file-transfer` — Upload/download with progress notifications, deduplication, and resumable transfer (resume writes to remote `<target>.part`); **recursive directory transfer** (auto-creates remote parent dirs, per-file atomic landing, a single-file failure does not abort the batch, results aggregate `files`/`failed`); atomic upload (same-dir `.part` + posix-rename, failure does not truncate an existing target); sha256 check after single-file transfer (`unverified` if remote has no `sha256sum`, hard error on mismatch)
- **Agent Skill**: [`skills/2native-ssh-mcp-helper`](skills/2native-ssh-mcp-helper/SKILL.md) (installation & configuration), [`skills/2native-ssh-mcp-agent`](skills/2native-ssh-mcp-agent/SKILL.md) (remote execution defense and output/token strategies)
- **Connection lifecycle**: Lazy connection (established on first call), kept alive after execution per `keepAlive`/`keepAliveDuration` (default 10 minutes), automatically disconnects when idle; in-flight commands are protected from accidental disconnection by idle/heartbeat cleanup
- **Reliability**: TCP keepalive + application-level heartbeat (OpenSSH semantics: any reply confirms liveness, single in-flight send); foreground timeouts kill the remote process group by PID (channel Signal is only a fallback — OpenSSH often ignores it); exec allocates no PTY by default (connection `pty: true` or tool param `pty` as needed; background jobs can use `pty: true` wrapped in `script`); background tasks started on a separate PTY-less channel (setsid to detach from session), **survives connection interruptions**, and sessions can be automatically reconnected and reattached; non-zero exit codes are normal results (check `exitCode`), connection failures report `SSH_CONNECTION_LOST` (`retriable=false`, includes partial output, not safe to blindly replay)
- **Session retention**: Sessions and remote logs are retained for 60 minutes after background job completion; `read` can replay from `offset=0` (JSON includes `logPath`/`exitCode`), `close` is idempotent and can be called repeatedly
- **Output processing**: ANSI escape sequences (colors/progress bars) are stripped by default (`stripAnsi: false` to disable); large outputs are handled in layers — ≥4KB head/tail summarization (`outputCompressLight`/`outputCompressThreshold`), ≥8KB (`outputSpillThreshold`, `-1` to disable) spills the full output to a local directory (default `.ssh-mcp-out/`, `outputSpillDir` configurable, keeps the newest 32), MCP result keeps only a notice + short preview; Agent uses local Read/Grep for the full text without re-running remotely
- **Command logging**: Records the last N executed commands per connection (excludes output), can log only successful commands, persisted as JSON lines file (configured via `commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`, or global CLI parameters like `--command-log-size`)
- **Security**: Command whitelist/blacklist, path whitelisting (local/remote, configurable via `localPathMode` for local paths: cwd / list / any), credential isolation (SSH credentials stay local, never exposed to the model), output redaction (`redactSecrets`, off by default — scanning secret-bearing large output is expensive, enable per connection when needed), configuration permission checking (Unix `0600`/`0700`, Windows ACL; can be skipped with `--allow-insecure-config-perms` or `$global.allowInsecureConfigPerms`, see [SECURITY.md](SECURITY.md))
- **Authentication & Compatibility**: Password/private key/ssh-agent/Pageant/keyboard-interactive authentication (2FA), proxy (SOCKS5/HTTP/HTTPS), cipher/kex algorithm configuration (compatible with older servers)
- **File transfer performance**: SFTP concurrent transfers (`sftpConcurrency`/`sftpChunkSize`, improves speed on high-latency links; large files automatically enable concurrent writes), SFTP clients pooled per connection (idle 5 min recycle), `sftpTimeoutMs` is a **no-progress timeout** rather than a wall-clock cap (healthy large transfers are no longer cut off by wall time), resumable transfer, deduplication, progress notifications
- **HTTP daemon**: `start/stop/status/kill` subcommands + reference counting + PID file + health check endpoint; `install` registers Windows auto-startup with one click
- **Automated releases**: Pushing a tagged commit with a message triggers GitHub Actions to build 6-platform binaries and create a Release (uses tag message for release notes)

## Quick Start

```bash
# Build
go build -o 2native-ssh-mcp.exe .

# stdio mode (launched by MCP client, credentials in config.json or environment variables)
2native-ssh-mcp.exe --config-file config.json

# HTTP persistent daemon
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338
```

See the two guides above for detailed configuration and deployment steps.

## Automated Release

Pushing an **annotated tag with a message** triggers GitHub Actions (`.github/workflows/release.yml`):

```bash
git tag -a v1.0.1 -m "Fixed xxx issue"
git push origin v1.0.1
```

**Important notes**:

- **Release notes = tag message**. The workflow reads the annotated tag object message via the GitHub API (avoids using the local git on runner — when runners checkout, they might only have a lightweight tag reference and `%(contents)` would fall back to the commit message, turning release notes into the last commit message)
- Builds **6 platforms**: Windows / Linux / macOS × amd64 / arm64 (`CGO_ENABLED=0`, version injected as tag name), outputs named `2native-ssh-mcp-<os>-<arch>[.exe]`
- Each binary includes an independent `.sha256` checksum file
- Each binary is also packaged as an **`.mcpb` bundle** (MCP Bundle: zip + `manifest.json`, `server.type: "binary"`) with its own `.sha256`
- One CycloneDX SBOM (`2native-ssh-mcp.cdx.json`) is attached; SLSA provenance and SBOM attestations are published to the repo [Attestations](https://github.com/daidaiJ/2native-ssh-mcp/attestations) tab (not extra `.intoto` files on the release). Verify a downloaded binary with `gh attestation verify --repo daidaiJ/2native-ssh-mcp <file>`
- Use `git tag -a -m` to create an **annotated tag with a message**; lightweight tags (`git tag v1.0.1`) have no message, so release notes will fall back to the tag name

### Publishing to the official MCP Registry

Publishing to [registry.modelcontextprotocol.io](https://registry.modelcontextprotocol.io) is **explicit and separate**: a plain tag only builds the release; a tag with the `-registry` suffix additionally publishes the matching stable release to the official MCP Registry (via OIDC, no secrets needed):

```bash
git tag -a v1.0.1 -m "Publish v1.0.1 to MCP Registry"
git push origin v1.0.1-registry
```

The `v1.0.1-registry` tag triggers the `publish-registry` job, which downloads the `.mcpb` bundles from the existing `v1.0.1` release, generates `server.json` (6 platform packages), and publishes as `io.github.daidaiJ/2native-ssh-mcp`. Verify with:

```bash
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp"
```

## Project Structure

```
2native-ssh-mcp/
├── main.go                        # Entry point: subcommand dispatch (start/stop/status/install…) + stdio/HTTP dual transport
├── internal/
│   ├── config/                    # SSH configuration types, defaults, CLI parsing, ${ENV_VAR} interpolation
│   ├── sshconfig/                 # ~/.ssh/config parsing (Include, wildcards, first-match-wins)
│   ├── logger/                    # stderr logging (doesn't pollute stdio protocol)
│   ├── manager/                   # Connection management core
│   │   ├── manager.go             #   Lazy connection, idle keep-alive, path validation, command logging
│   │   ├── dial.go                #   Authentication (password/private key/agent/Pageant/2FA), proxy (SOCKS5/HTTP/HTTPS)
│   │   ├── hostkey.go             #   Host key verification (accept-new/strict/none, known_hosts)
│   │   ├── exec.go                #   exec mode command execution (timeout/output limits/exit code)
│   │   ├── shell.go               #   shell mode (marker protocol, ANSI cleanup, serial queue)
│   │   ├── background.go          #   Background tasks (detached exec, log polling, stopping)
│   │   ├── session.go             #   Named sessions (CWD preservation, reattachment after disconnect, idle TTL)
│   │   ├── heartbeat.go           #   Application-level heartbeat (keepalive semantics, in-flight protection)
│   │   ├── result.go              #   Structured result (exit code, status, ANSI stripping)
│   │   ├── compress.go            #   Large output head/tail compression
│   │   ├── redact.go              #   Output redaction
│   │   ├── sftp.go                #   File transfer (concurrent copying, progress, deduplication, resumable transfer)
│   │   ├── status.go              #   Remote system status collection (hostname/OS/memory/disk, etc.)
│   │   └── commandlog.go          #   Command log file (JSON lines, retains last N commands)
│   ├── daemon/                    # HTTP daemon: PID file, refcount+guest lease, admin endpoints, Windows auto-start
│   └── tools/                     # MCP tools: list-servers / execute-command / session / file-transfer
├── SECURITY.md                    # Threat model and security recommendations
├── docs/
│   ├── AGENT_GUIDE.md             # 🤖 Agent guide (token-efficient, on-demand reading)
│   ├── AGENT_GUIDE.zh-CN.md       # 🤖 中文版 Agent guide
│   ├── HUMAN_GUIDE.md             # 👤 Human guide (readable)
│   └── HUMAN_GUIDE.zh-CN.md       # 👤 中文版 Human guide
├── .github/workflows/ci.yml       # push/PR → ubuntu-latest go test -race ./...
├── .github/workflows/release.yml  # tag push → 6-platform build + Release; "-registry" tag → publish to MCP Registry
├── plan/                          # Design specifications (HARDENING.md, etc., git-ignored)
├── todo/                          # Implementation task list T01–T10 (git-ignored)
├── LICENSE                        # ISC (includes upstream copyright notice)
├── README.md                      # English (default)
└── README.zh-CN.md                # 中文版
```

## Build & Test

```bash
go build ./...
go test -race ./...
```

## Known Limitations

- Go's `x/crypto/ssh` does not support SSH zlib compression ([golang/go#22795](https://github.com/golang/go/issues/22795)); for high-latency/low-bandwidth scenarios, rely on TCP keepalive and SFTP concurrent transfers (`sftpConcurrency`) to mitigate
- Host key verification defaults to `accept-new` (records first connection to `known_hosts`, rejects subsequent key changes); for dynamic IP / frequently rotated keys, set `"hostKeyCheck": "none"`
- SFTP upload/download is not supported in shell transport mode
- Local WSL has **no** separate transport mode: treat it as a normal Linux SSH target (sshd inside the distro). Do not launch this process with `"command": "wsl"`; `file-transfer` `localPath` is a path on the OS running MCP. See [HUMAN_GUIDE Connecting to local WSL](docs/HUMAN_GUIDE.md#connecting-to-local-wsl) for ports, localhost, and 9P paths

## References

- [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server) — The TypeScript SSH MCP Server this project references. Tool design, error codes, path validation, shell transport protocol, etc., are derived from this project.

## License

ISC License (consistent with upstream, retains upstream copyright notice, see [LICENSE](LICENSE))

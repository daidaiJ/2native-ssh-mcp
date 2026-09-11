> [English](DEVELOPMENT.en.md)

# 开发与发布

## 项目结构

```
2native-ssh-mcp/
├── main.go                        # 入口：子命令分发（start/stop/status/install…）+ stdio/HTTP 双 transport
├── internal/
│   ├── config/                    # SSH 配置类型、默认值、CLI 参数解析、${ENV_VAR} 引用
│   ├── approval/                  # 破坏性命令分类器（内置规则 + 用户扩展/豁免）
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
├── docs/                          # 指南与专项文档（中文默认，.en.md 为英文版）
├── skills/                        # Agent Skill（安装配置 / 远程执行策略）
├── .github/workflows/ci.yml       # push/PR → ubuntu-latest go test -race ./...
├── .github/workflows/release.yml  # tag 推送 → 6 平台构建 + Release；"-registry" tag → 发布到 MCP Registry
├── LICENSE                        # ISC（含上游版权声明）
└── README.md                      # 中文（默认）；英文版 README.en.md
```

## 构建与测试

```bash
go build ./...
go test -race ./...
```

### 基准测试

```bash
# 输出管线（stripANSI / redact / compress，纯本地，无需 SSH）
go test ./internal/manager/ -bench BenchmarkOutput -benchmem

# SFTP 传输吞吐（走 integration 标签，需要 config.json 里有可达连接，如 WSL sshd）
go test -tags integration ./internal/manager/ -bench BenchmarkTransfer -benchmem
```

**实测数据**（2026-09-11，Ryzen 5 4600H，WSL2 Ubuntu-22.04 localhost sshd；输出基准 1 MiB 文本，传输基准 16 MiB 文件，固定 5 轮取中位）：

| 基准 | 结果 |
|---|---|
| stripANSI，纯文本 1 MiB | ~15 µs（~70000 MB/s，0 alloc） |
| stripANSI，ANSI 密集 1 MiB | ~1.9 ms（~550 MB/s，1.9 MB alloc） |
| RedactSensitiveOutput，含密 1 MiB | ~4.7 ms（~220 MB/s） |
| SFTP 上传 16 MiB（32KB 分块） | ~52 MB/s |
| SFTP 下载 16 MiB（32KB 分块） | ~95 MB/s |

说明：

- localhost 下 SFTP 吞吐由 pkg/sftp 的协议行为主导；`sftpConcurrency`/`sftpChunkSize` 的收益要在高延迟链路上才明显
- `UseConcurrentReads` 实测在低延迟链路无收益（下载 -7%），故未启用
- `sftpChunkSize` 上限 32KB（SSH session channel 单条数据消息的上限，pkg/sftp 硬拒绝更大值），超配自动钳制

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

## 设计决策：命令结果默认 text 渲染（2026-09-07）

- **背景**：e4888a6（2026-09-04，v1.5.0 发布前夜）把 execute-command 成功路径从 `result.Text()` 分节文本改为 `commandResultJSON`。MCP 客户端把 text content 原样渲染，而 JSON 里的 `stdout` 是单行转义字符串（换行是字面 `\n`），长输出在终端里被软换行成一整块，可读性差；v1.4.1 及之前的分节文本才是用户与 agent 都易读的形态。
- **决策**：成功路径默认恢复 `result.Text()`；新增按连接配置 `resultFormat`（`text` 默认 / `json` 可选，见 `SSHConfig.GetResultFormat`）。错误路径保持 JSON（agent 依赖 `code`/`message`/`retriable`/`partial`/`replaySafe` 分支）；background/session-open 的 info JSON 与 list-servers 不变。
- **兼容性**：默认行为变化——依赖 JSON 形状的消费方在连接配置里显式加 `"resultFormat": "json"` 即可恢复旧行为。

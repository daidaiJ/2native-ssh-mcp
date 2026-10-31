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
│   │   ├── dial.go                #   认证（密码/私钥/agent/Pageant/2FA）、代理（SOCKS5/HTTP/HTTPS）、via 跳板隧道
│   │   ├── hostkey.go             #   主机密钥校验（accept-new/strict/none，known_hosts）
│   │   ├── exec.go                #   exec 模式命令执行（超时/输出上限/退出码）
│   │   ├── shell.go               #   shell 模式（marker 协议、ANSI 清理、串行队列）
│   │   ├── background.go          #   后台任务（detached exec、日志轮询、停止）
│   │   ├── session.go             #   命名会话（CWD 保持、断连重附着、空闲 TTL）
│   │   ├── heartbeat.go           #   应用层心跳（keepalive 语义、in-flight 保护）
│   │   ├── kbdint.go              #   键盘交互认证（2FA 挑战经 MCP elicitation 弹窗、env 兜底）
│   │   ├── result.go              #   结构化结果（退出码、状态、ANSI 剥离）
│   │   ├── compress.go            #   大输出头尾压缩
│   │   ├── redact.go              #   输出脱敏
│   │   ├── sftp.go                #   文件传输（并发拷贝、进度、去重、断点续传）
│   │   ├── status.go              #   远程系统状态采集（hostname/OS/内存/磁盘等）
│   │   └── commandlog.go          #   审计日志（追加 JSONL、SHA-256 哈希链、批量落盘、滚动）
│   ├── daemon/                    # HTTP daemon：PID 文件、refcount+guest 租约、admin 端点、Windows 自启动
│   └── tools/                     # MCP 工具：list-servers / execute-command / session / file-transfer
├── SECURITY.md                    # 威胁模型、审计落盘策略与分级加固建议（英文版 SECURITY.en.md）
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

### 提交前必做：`make check`

```bash
make check   # = make lint && make test
```

两道门禁，CI（`.github/workflows/ci.yml`）跑同样的两道：

| 步骤 | 命令 | 拦住什么 |
|---|---|---|
| `lint` | `golangci-lint run ./...` | govet / staticcheck / ineffassign / unused，以及复杂度 |
| `test` | `go test ./...` | 功能，以及**结构门禁** |

结构门禁在 `complexity_test.go` 里，跟着 `go test` 走，所以不装任何外部工具也生效：单文件 ≤ 700 行、单函数 ≤ 100 行、圈复杂度 ≤ 20。`golangci-lint` 的 `gocyclo`/`funlen` 用同一套阈值和同一份豁免名单（`.golangci.yml`）。

历史遗留的超长文件/函数列在 `grandfatheredFiles` / `grandfatheredFuncs`（以及 `.golangci.yml` 的 `exclusions.rules`）里。**这份名单只能变短**：拆分某个文件或函数时，在同一个提交里删掉它的条目；新代码必须直接达标。

需要 `golangci-lint` v2：

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.15.0
```

### gofmt（尚未进门禁）

`make fmt-report` 会列出 `gofmt` 想要改动的文件。当前有 25 个（结构体字段对齐、import 分组、缺结尾换行）——它们早于任何 gofmt 门禁。**建议单独一个提交把这些文件格式化**，然后把 `fmt-report` 提升为 `check` 的一环并加进 CI；在那之前不要把它放进门禁，否则 CI 会红。

注意 Windows 工作区若开了 `core.autocrlf`，整棵树都是 CRLF，`gofmt -l .` 会列出所有文件。`make fmt-report` 两侧都剥掉 CR，所以只报真实差异；Linux CI 检出本身就是 LF，不受影响。

### 集成测试（需要真实环境）

```bash
# ProxyJump 双 sshd 容器 fixture：bastion 映射本机端口，internal 仅容器网络可达。
# 无 Docker 时自动跳过，不影响默认 CI。
go test -tags integration ./internal/manager/ -run ProxyJump -v

# 其余 integration 测试走仓库根 config.json 里的可达连接（如 WSL sshd）
go test -tags integration ./internal/manager/ -run ExecKill -v

# config.json 不在仓库根时（例如复用 MCP 客户端那份），用环境变量指过去
SSH_MCP_TEST_CONFIG=/path/to/config.json go test -tags integration ./internal/manager/ -v
```

`escape_integration_test.go` 覆盖转义相关工作：argv 逐字节还原（对照 naive join 的拆分/展开）、`scriptFile` 暂存+执行+清理、后台模式经清理子壳保住退出码、lint 的 warn/strict 行为、审计记录里的可读等效命令、能力探测（含 `autoProbe: false`）。

### 基准测试

```bash
# 输出管线（stripANSI / redact / compress，纯本地，无需 SSH）
go test ./internal/manager/ -bench BenchmarkOutput -benchmem

# 审计日志链路（canonical JSON+哈希、追加写穿 vs 批量缓冲、旧重写模型对照；纯本地，无需 SSH）
go test ./internal/manager/ -bench 'BenchmarkCommandLog|BenchmarkAuditCanonicalHash' -benchmem

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

## 设计决策：稳定性与上下文收敛（2026-10-31）

调研同类 SSH MCP（Motty 内置的 MCP server、TermBridge 的执行语义 ADR、n0madic/ssh-mcp、remotessh-mcp）后确认的差距与对应决策：

- **工具 panic 会让请求悬空**：mcp-go 的恢复中间件是可选项，此前没开——handler 内 panic 会落到传输层兜底，客户端只等到超时。现在 `newMCPServer` 注册 `server.WithRecovery()`，并在其外层加 `tools.LogToolCalls` 中间件（记录工具名、耗时、错误），panic 变成那一次调用的错误响应并留痕。
- **HTTP 传输无超时/体积上限**：`http.Server` 现在设 `ReadHeaderTimeout` 10s、`IdleTimeout` 2m、`MaxHeaderBytes` 16 KiB，`/mcp` 请求体上限 4 MiB（`limitBody`）。loopback 监听下的 DNS-rebinding 保护是 mcp-go 内建的（Host 非 localhost 直接 403）。
- **后台任务远端日志无界**：新增连接配置 `backgroundLogMaxBytes`（默认 8 MiB，`-1` 不裁剪，`$global` 可设默认）。启动脚本把作业输出经 `capture()` 管道写入日志：超限时从最早处裁剪，丢弃字节数写进 `.drop` 文件，输出开头 64 KiB 复制到 `.head`；裁剪发生在唯一的写入者进程内，因此不存在与作业竞争的丢数据窗口。`session action=read` 的 `offset`/`totalBytes` 改为**绝对字节**（从作业第一个字节算起，含已丢弃部分），命中被裁剪区间时一次返回「保留的开头 + 省略标记 + 最新输出」，头部另报 `droppedBytes`/`logMaxBytes`。
- **中断缺少可执行的恢复语义**：`CommandResult` 新增 `executionState`（`started` = 命令已开始、结果未知；`unknown` = 断线，可能没开始/在跑/已结束）与 `guidance`（下一步动作），只在被中断的结果上出现；`session`/`list-servers`/`skills/2native-ssh-mcp-agent` 同步写明「超时 ≠ 命令没跑，先核对远端状态再决定是否重试」。
- **同一份数据发两遍**：`session action=read`/`list` 与 `list-servers` 过去在文本之后再附一份 `Raw JSON`/`Servers JSON`。现在统一由 `resultFormat` 二选一（默认文本），并支持 `$global.resultFormat` 一次配全部连接。spill 预览改为头 8 行 + 尾 4 行（失败信息在尾部）。initialize 增加 `instructions` 承载跨工具契约（连接选择、落盘文件、中断语义、后台会话）。

**兼容性**：session/list-servers 的响应默认不再含 `Raw JSON`/`Servers JSON` 段（需要结构化形态就配 `"resultFormat": "json"`）；`offset` 语义变为绝对字节（旧偏移仍有效，只是不再随裁剪回退）；后台日志默认 8 MiB 上限，需要完整日志的连接设 `"backgroundLogMaxBytes": -1`。

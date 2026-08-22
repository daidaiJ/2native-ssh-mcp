# ssh-mcp-server-go

基于 SSH 的 MCP (Model Context Protocol) 服务器，Go 实现。让 AI 助手通过 MCP 协议远程执行命令、传输文件，SSH 凭据完全留在本地，不暴露给模型。

> 本项目参考了 [classfang/ssh-mcp-server](https://github.com/classfang/ssh-mcp-server)（TypeScript 版）的设计与实现，在其基础上用 Go 重写，并将文件操作整合为**单个工具**、支持**进度通知**。感谢原作者的开源贡献。

## 特性

- **双传输模式**：stdio（MCP 客户端子进程）与 streamable HTTP（常驻服务）
- **三个工具**：
  - `execute-command` — 远程执行命令，支持白/黑名单、超时、输出上限、工作目录
  - `file-transfer` — 上传/下载整合为一个工具，带 **MCP 进度通知**、**去重**（大小+mtime 一致自动跳过）、**断点续传**、`force` 强制全量
  - `list-servers` — 服务器列表与系统状态（主机名/OS/内存/磁盘等）
- **连接生命周期**：懒连接（首次调用才建立），执行后按 `keepAlive`/`keepAliveDuration` 保活（默认 10 分钟），空闲自动断开
- **命令日志**：按连接记录最近 N 条执行过的命令（不含输出），可只记成功命令，落盘为 JSON 行文件
- **安全**：命令白/黑名单、本地/远端路径白名单、凭据不落盘
- **实用 SSH 特性**：TCP keepalive、心跳检测、算法协商配置（兼容老服务器）、SFTP 并发传输（高延迟链路提速）、代理（SOCKS5/HTTP/HTTPS）、Pageant/ssh-agent、键盘交互认证（2FA）
- **HTTP daemon**：`start/stop/status/kill` 子命令 + 引用计数 + PID 文件 + 健康检查端点；`install` 一键注册 Windows 开机自启

## 快速开始

### 方式一：stdio（推荐，MCP 客户端直接拉起）

```json
{
  "mcpServers": {
    "ssh-mcp-server": {
      "command": "D:/CODE/ai/ssh-mcp/ssh-mcp-server-go/ssh-mcp-server-go.exe",
      "args": ["--host", "192.168.1.1", "--port", "22", "--username", "root", "--password", "pwd123456"]
    }
  }
}
```

### 方式二：HTTP 常驻服务

```bash
# 启动（引用计数 +1；已运行则直接 +1）
ssh-mcp-server-go.exe start --config-file config.json --http-addr 127.0.0.1:8338

# 查看状态 / 停止（引用计数归零才退出）/ 强制停止
ssh-mcp-server-go.exe status
ssh-mcp-server-go.exe stop
ssh-mcp-server-go.exe kill

# Windows 开机自启（生成 config.json 模板 + 启动文件夹快捷方式）
ssh-mcp-server-go.exe install
ssh-mcp-server-go.exe uninstall
```

MCP 客户端配置：

```json
{
  "mcpServers": {
    "ssh-mcp-server": {
      "url": "http://127.0.0.1:8338/mcp"
    }
  }
}
```

### 多服务器配置（config.json）

```json
{
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root", "password": "…",
    "commandWhitelist": ["^ls ", "^cat ", "^df "],
    "allowedRemotePaths": ["/tmp", "/home"],
    "commandLogSize": 50,
    "commandLogDir": "logs",
    "commandLogOnlySuccess": true
  },
  "prod": {
    "host": "10.0.0.2", "username": "deploy",
    "privateKey": "~/.ssh/id_rsa", "passphrase": "…",
    "transportMode": "shell"
  }
}
```

```bash
ssh-mcp-server-go.exe start --config-file config.json
```

## 工具说明

### execute-command

| 参数 | 必填 | 说明 |
|---|---|---|
| `cmdString` | ✅ | 要执行的命令 |
| `directory` | | 工作目录 |
| `connectionName` | | 连接名，默认 `default` |
| `timeout` | | 超时（毫秒），默认 30000 |
| `keepAlive` | | 执行后是否保活连接，默认 `true` |
| `keepAliveDuration` | | 保活时长（毫秒），默认 600000（10 分钟） |

### file-transfer

| 参数 | 必填 | 说明 |
|---|---|---|
| `action` | ✅ | `upload` 或 `download` |
| `localPath` | ✅ | 本地路径（须在 cwd 或 `allowedLocalPaths` 内） |
| `remotePath` | ✅ | 远端绝对路径（配置了 `allowedRemotePaths` 时须在其内） |
| `connectionName` | | 连接名，默认 `default` |
| `force` | | 跳过去重/续传，强制全量传输 |

- 客户端在请求中携带 `_meta.progressToken` 时，服务端通过 `notifications/progress` 上报进度（约 100ms 节流，结束必报 100%）
- 目标文件与源文件大小、mtime 一致 → 直接跳过（去重）
- 目标已有部分数据 → 从断点续传；下载走临时文件 + 原子改名
- 传输中源文件继续增长 → 自动补传尾部

### list-servers

列出所有连接：名称、地址、连接状态、系统状态摘要。

## 命令行参数

```
ssh-mcp-server-go [command] [options] [host port username password]

Commands:
  (none)      stdio 模式（默认，供 MCP 客户端拉起）
  start       启动 HTTP daemon（引用计数管理）
  stop        停止（引用计数 -1，归零退出）
  kill        强制停止
  status      查看状态
  install     安装 Windows 开机自启
  uninstall   卸载开机自启
  version     版本号
  help        帮助

Connection options:
  --config-file <path>             从 JSON 文件加载服务器配置
  --ssh-config-file <path>         读取 SSH config 别名（默认 ~/.ssh/config）
  --ssh <config>                   追加一个配置（JSON 或 key=value，可重复）
  -h, --host / -p, --port / -u, --username / -w, --password
  -k, --privateKey / -P, --passphrase / -a, --agent
  -W, --whitelist / -B, --blacklist
  --proxy <url> / -s, --socksProxy <url>
  --allowed-local-paths / --allowed-remote-paths
  --transport-mode <exec|shell>
  --command-template <template>
  --pty / --try-keyboard
  --command-log-size <n> / --command-log-dir <dir> / --command-log-only-success
  --pre-connect

Server options:
  --transport <stdio|http>         默认 stdio；start 隐含 http
  --http-addr <host:port>          默认 127.0.0.1:8338
  --version, -v / --help
```

## 配置项速查

| 配置 | 默认 | 说明 |
|---|---|---|
| `transportMode` | `exec` | `shell` 用于堡垒机/跳板机场景 |
| `commandWhitelist` / `commandBlacklist` | 空 | 命令正则白/黑名单 |
| `allowedLocalPaths` / `allowedRemotePaths` | 空 | 文件传输路径白名单 |
| `commandLogSize` | 0（关闭） | 命令日志保留条数 |
| `commandLogDir` | 空 | 命令日志目录（`<dir>/<连接名>.log`） |
| `commandLogOnlySuccess` | false | 只记录成功命令 |
| `sftpConcurrency` / `sftpChunkSize` | 16 / 32768 | SFTP 并发数与分块大小 |
| `algorithms` | 空 | kex/cipher/serverHostKey/hmac 协商 |
| `keepaliveIntervalMs` / `keepaliveCountMax` | 10000 / 3 | SSH 心跳 |
| `commandTimeoutMs` / `connectionTimeoutMs` / `sftpTimeoutMs` | 30000 / 30000 / 300000 | 各类超时 |
| `maxOutputBytes` | 10485760 | 单命令输出上限，0 为不限 |
| `commandTemplate` | 空 | 命令包装模板（`<command>` / `<quotedCommand>`） |
| `pty` | true | exec 模式分配伪终端 |
| `tryKeyboard` | false | 键盘交互认证（2FA 码用环境变量 `SSH_MCP_2FA_CODE`） |

## 认证方式

- 密码：`password`
- 私钥：`privateKey`（可带 `passphrase`，或环境变量 `SSH_MCP_PASSPHRASE`）
- ssh-agent：`agent`（Unix socket 路径；Windows 填 `pageant` 使用 Pageant）
- 键盘交互：`tryKeyboard: true`，密码提示用配置的密码，OTP 提示用 `SSH_MCP_2FA_CODE`

## 命令日志

配置 `commandLogSize`（>0）后，每个连接执行过的命令会追加写入 `<commandLogDir>/<连接名>.log`，JSON 行格式，只保留最近 N 条，重启不丢失：

```json
{"timestamp":"2026-08-22T10:00:00+08:00","command":"ls -la /tmp","exitCode":0,"success":true}
```

配合 `commandLogOnlySuccess: true` 可只记录成功命令，避免探测类命令产生噪声。

## 构建与测试

```bash
go build -o ssh-mcp-server-go.exe .
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
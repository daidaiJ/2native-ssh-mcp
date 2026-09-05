> [English](HUMAN_GUIDE.md)

# HUMAN_GUIDE — 配置与部署指南（人类阅读版）

本指南面向**人类用户**，详细讲解 2native-ssh-mcp 的配置、部署与使用。给 AI Agent 看的省 token 版见 [AGENT_GUIDE.md](AGENT_GUIDE.md)。

## 安全配置（重要）

**不要把服务器地址和密码写进 MCP 客户端的配置参数里**——命令行参数在进程列表里可见，MCP 配置也容易被分享或提交到仓库。推荐以下三种方式（任选其一）：

### 方式一：配置文件 + 环境变量引用（推荐）

配置文件里用 `${环境变量名}` 引用凭据，密码不落盘：

```json
{
  "dev": {
    "host": "10.0.0.1",
    "port": 22,
    "username": "root",
    "password": "${SSH_MCP_PASSWORD}"
  }
}
```

```bash
# 设置环境变量（Windows: setx SSH_MCP_PASSWORD xxx）
export SSH_MCP_PASSWORD='你的密码'
```

MCP 客户端配置里只出现配置文件路径：

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/2native-ssh-mcp.exe",
      "args": ["--config-file", "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/config.json"]
    }
  }
}
```

### 方式二：配置文件 + 文件权限锁定

密码明文写在 `config.json` 里，但收紧文件权限（Linux/macOS：`chmod 600 config.json`；Windows：右键属性 → 安全 → 仅当前用户），MCP 配置同样只写 `--config-file`。

### 方式三：复用 ~/.ssh/config 别名

凭据全部放在你已有的 SSH 配置/agent 里，MCP 配置零敏感信息：

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "2native-ssh-mcp",
      "args": ["--host", "myserver"]
    }
  }
}
```

> 如果仍通过 `--password`/`--privateKey` 传凭据，程序会在 stderr 打印安全警告。

## 快速开始

### stdio 模式（MCP 客户端直接拉起）

按上面的安全配置方式准备好 `config.json` 后：

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/2native-ssh-mcp.exe",
      "args": ["--config-file", "D:/CODE/ai/ssh-mcp/2native-ssh-mcp/config.json"]
    }
  }
}
```

### HTTP 常驻服务

```bash
# 启动（引用计数 +1；已运行则直接 +1）
2native-ssh-mcp.exe start --config-file config.json --http-addr 127.0.0.1:8338

# 查看状态 / 停止（引用计数归零才退出）/ 强制停止
2native-ssh-mcp.exe status
2native-ssh-mcp.exe stop
2native-ssh-mcp.exe kill

# Windows 开机自启（生成 config.json 模板 + 启动文件夹快捷方式）
2native-ssh-mcp.exe install
2native-ssh-mcp.exe uninstall
```

**引用计数与租约**：第一个 `start` 是 owner（与 daemon 进程同寿，永不过期）；额外的 `start` 是 **guest 租约**，空闲 **15 分钟**没有 `/mcp` 请求就会被回收（计数自动 -1）。任何通过鉴权的 `/mcp` 请求（无论来自哪个客户端/凭证、无论 MCP 层是否成功）都会**刷新全部** guest 租约的到期时间——只要 daemon 还在被用，任何 guest 都不会掉。`stop` 优先减一个 guest，没有 guest 才减 owner；计数归零 daemon 退出。daemon 进程本身不会因空闲退出（owner 不过期）。`kill` / Ctrl-C 直接关，不看计数。

MCP 客户端配置：

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "url": "http://127.0.0.1:8338/mcp"
    }
  }
}
```

**/mcp 鉴权（token）**：监听地址是 loopback（默认 `127.0.0.1`）时不需要 token，本机客户端零配置。监听非 loopback 地址（如 `0.0.0.0`）时**必须**配置 token，否则拒绝启动（fail closed）。token 来源按优先级：`--http-token` → 环境变量 `SSH_MCP_HTTP_TOKEN` → 配置文件 `$global.httpToken`（支持 `${VAR}` 展开）。带 token 时客户端加请求头：

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "url": "http://0.0.0.0:8338/mcp",
      "headers": { "Authorization": "Bearer ${SSH_MCP_HTTP_TOKEN}" }
    }
  }
}
```

Admin API（`/__admin/*`）不用这个 token，仍只限本机访问（loopback 来源 + loopback `Host` 头）；`/__admin/shutdown` 额外要求 **POST + `Content-Type: application/json`**，防止本地网页用 GET 触发关停。

### 多服务器配置（config.json）

```json
{
  "$global": {
    "allowInsecureConfigPerms": true
  },
  "dev": {
    "host": "10.0.0.1", "port": 22, "username": "root", "password": "${SSH_MCP_PASSWORD}",
    "description": "开发环境跳板机",
    "business": "订单/支付联调",
    "aliases": ["dev-box", "开发"],
    "notes": "只读为主；高峰期勿跑重查询",
    "commandWhitelist": ["^ls ", "^cat ", "^df "],
    "commandBlacklist": ["^rm -rf"],
    "allowedRemotePaths": ["/tmp", "/home"],
    "commandLogSize": 50,
    "commandLogDir": "logs",
    "commandLogOnlySuccess": true
  },
  "prod": {
    "host": "10.0.0.2", "username": "deploy",
    "privateKey": "~/.ssh/id_rsa", "passphrase": "${SSH_MCP_PASSPHRASE}",
    "transportMode": "shell"
  }
}
```

`$global` 是保留的顶层键（对象格式专属，数组格式不支持），存放作用于整个配置文件的设置：

| 配置 | 默认 | 说明 |
|---|---|---|
| `allowInsecureConfigPerms` | false | 跳过本配置文件的权限检查（等价于命令行 `--allow-insecure-config-perms`，但声明在文件内部；不推荐，仅开发用） |
| `httpToken` | 空 | `/mcp` 的 Bearer token（优先级低于 `--http-token` 和 `SSH_MCP_HTTP_TOKEN`；支持 `${VAR}` 引用） |

## 连接本地 WSL

本项目**没有**单独的 `wsl` 传输模式。把 WSL 当普通 Linux SSH 目标即可：MCP 跑在 Windows 上，发行版里开 sshd，用密钥连 `127.0.0.1`。不要用 MCP 配置里的 `"command": "wsl"` 把本进程塞进发行版（`wsl.exe` 非交互启动不读 login shell，还会混进 Windows PATH）。

发行版内启用并启动 sshd 后，配置示例：

```json
{
  "wsl": {
    "host": "127.0.0.1",
    "port": 2222,
    "username": "your-linux-user",
    "privateKey": "~/.ssh/id_ed25519",
    "hostKeyCheck": "accept-new",
    "pty": false,
    "description": "本机 WSL",
    "allowedRemotePaths": ["/home", "/tmp"]
  }
}
```

注意：

- **端口**：Windows 若已开启 OpenSSH Server（占用 22），WSL sshd 换端口（如 2222）。mirrored networking 下 Windows 与 WSL **不能各听同一端口**；从局域网进 WSL 还要加 Hyper-V 防火墙入站规则。
- **localhost**：Windows → WSL 的 localhost 转发在多数机器上可用。默认 NAT 下，**WSL 里的 `127.0.0.1` 不是 Windows 的 loopback**。MCP HTTP daemon 若绑在 Windows 的 `127.0.0.1:8338`，WSL 内的客户端打不通；反过来也一样。需要跨侧访问时用 mirrored networking（`.wslconfig` 里 `networkingMode=mirrored`，必要时 `hostAddressLoopback=true`），或把 daemon 绑在对方能路由到的地址并配 `--http-token`。
- **路径**：`file-transfer` 的 `localPath` 是 **MCP 进程所在 OS** 的路径。Windows 上跑就用 `D:\\proj\\a.tar`，不要传 `/mnt/c/...` 或 `\\\\wsl$\\Ubuntu\\...`。`remotePath` 仍是发行版内的 POSIX 路径（如 `/home/you/a.tar`）。
- **文件放哪**：SFTP 落到 Linux 的 `/home`（ext4）是快路径。不要把构建目录放在 `/mnt/c`，也不要从 Windows 经 `\\\\wsl$\\` 扫整棵树（9P 跨文件系统，I/O 很慢）。
- **PTY**：WSL 上跑 docker/npm/构建时建议该连接 `"pty": false`（与其它 Linux 目标相同）。

## 工具说明

### execute-command

| 参数 | 必填 | 说明 |
|---|---|---|
| `cmdString` | ✅ | 要执行的命令 |
| `directory` | | 工作目录 |
| `connectionName` | | 连接名或 list-servers 中的别名 |
| `timeout` | | 超时（毫秒），默认 30000；超时后按远端 PID 杀掉进程组 |
| `pty` | | 分配伪终端，默认 `false`（交互命令按需开启；`background=true` 时用 `script` 包裹后台任务） |
| `background` | | `true` 时命令转入后台会话立即返回 `sessionName`/`logPath`，用 `session read` 轮询（分钟级任务用它，别调大 timeout） |
| `keepAlive` | | 执行后是否保活连接，默认 `true`（named session 忽略 `false`） |
| `keepAliveDuration` | | 保活时长（毫秒），默认 600000（10 分钟） |

### file-transfer

| 参数 | 必填 | 说明 |
|---|---|---|
| `action` | ✅ | `upload` 或 `download` |
| `localPath` | ✅ | 本地路径（受 `localPathMode` 限制，默认 cwd + `allowedLocalPaths`） |
| `remotePath` | ✅ | 远端绝对路径（配置了 `allowedRemotePaths` 时须在其内） |
| `connectionName` | | 连接名或 list-servers 中的别名 |
| `force` | | 跳过去重/续传，强制全量传输 |

- `localPath`/`remotePath` 是**目录**时递归传输整棵树（自动建远端父目录，空目录不重建）；逐文件走原子上传/去重/续传，单文件失败收集进 `failed` 不中断整批，结果带 `files` 计数；目录模式不做整体 sha256
- 单文件传输完成后 sha256 双端校验：一致标 `verified`，远端无 `sha256sum` 标 `unverified`（不算失败），mismatch 硬报错提示 `force` 重传
- 进度通知只发给发起请求的客户端（HTTP 多客户端不再互相串扰）；约 100ms 节流，结束必报 100%
- 目标文件与源文件大小、mtime 一致 → 直接跳过（去重）
- 上传**原子落盘**：先写同目录 `<目标>.part`，字节数校验通过后 posix-rename 替换目标；失败不触碰已有目标，`<目标>.part` 保留供续传（服务端不支持 posix-rename 且目标已存在时报错提示手动 mv）
- 远端已有 `<目标>.part` 且头部匹配 → 从断点续传（并发写失败会截回已确认前缀再保留）；下载走临时文件 + 原子改名
- 传输前即拒绝目录目标（`IsDir` 提前报错）
- 传输中源文件继续增长 → 自动补传尾部
- 本地路径不在允许范围内（`localPathMode` 决定范围）→ `LOCAL_PATH_NOT_ALLOWED`，提示「not within the allowed local paths for this connection」；含 `..` 的路径逃逸单独报「Path traversal rejected」

### list-servers

列出所有连接及活动会话：服务器元数据、连接状态、系统摘要 + 当前 session 列表。

### session

`action` 参数：`open` | `read` | `close` | `list`（仅 exec 模式）。

| action | 说明 |
|---|---|
| `open` | 打开会话；`background=true` + `cmdString` 启动后台任务（`pty=true` 为后台任务包一层 TTY） |
| `read` | 轮询后台会话输出（`offset` 省略/负值=续读，`0`=从头重读；`waitMs` 无新输出时最多阻塞 30s 等数据或作业结束） |
| `close` | 关闭会话并停止后台进程（**幂等**，可重复调用） |
| `list` | 列出所有会话（可选 `connectionName` 过滤） |

### execute-command

增加可选 `sessionName`：在已打开的命名会话中执行（CWD 保持），此时忽略 `connectionName`。

**后台任务：** `session open` → 多次 `session read` → `session close`

**有状态操作：** `session open` → 多次 `execute-command`（带 sessionName）→ `session close`

**长任务 / 无输出任务：用 `execute-command` 带 `background: true`（自动建 `bg-*` 会话并返回 `sessionName`/`logPath`），或 `session background=true` + `cmdString`；用 `read` 轮询（`waitMs` 阻塞等新输出，别空转）。不要在前台 `execute-command` 里跑 `nohup ... &` 或 `setsid`**——后者会随 exec 通道关闭而消亡。后台任务以无 PTY 的独立通道启动（新会话、脱离 sshd 进程组），**连接闪断后仍然存活**：断连后 `list-servers` 里会话显示 `disconnected=true`，`read` 或带 `sessionName` 的 `execute-command` 会自动重连；只有 `action=close` 才会杀掉远端后台进程。

**后台作业结束后会话仍然保留**（含远端日志，60 分钟 retain TTL），必须 `close` 才释放；`close` 可重复调用。`read` 可带 `offset=0` 从头重读；返回 JSON 带 `logPath`（远端日志路径）和 `exitCode`（作业结束后）。会话只存在内存中：stdio 进程退出即丢失（远端日志仍在 `logPath`，可另行读取），常驻 HTTP daemon 才能跨对话保留。对已结束的会话重复 `open background=true` 会被拒绝并提示 `logPath`——先 `close` 或先读旧日志。**连接不可用时 `close` 无法确认远端作业已停**：会话会留在列表里并标记 `orphaned=true`（`background` 仍为 true），报可重试错误，等连接恢复后再 `close` 一次即可。后台日志/pid/exit 文件路径带一次性随机后缀（`/tmp/.2native-ssh-mcp-<会话名>-<id>.log` 等），`logPath` 字段始终给出实际路径。

**命令结果说明：**
- 非 0 退出码是**正常结果**（不是错误）：成功结果是 JSON，看 `exitCode` / `status`（`ok`/`exited`）/ `stdout` / `stderr` 字段；只有校验失败、连不上、超时、输出超限、连接中断才报错误 JSON（带 `code`/`message`/`retriable` + 部分 `stdout`/`stderr`）
- 连接中断报 `SSH_CONNECTION_LOST`（`retriable=false`），远端进程可能还在跑，**不要盲目重放**；错误 JSON 里带部分 `stdout`/`stderr` 和 `replaySafe: false`
- 前台命令的 `timeout` 必须大于真实耗时（默认 `commandTimeoutMs=30000` 仍然生效）；超时后会按远端 PID 杀掉整个进程组（channel Signal 仅作补充），不会留下远端泄漏进程
- 输出 ≥8KB（`outputSpillThreshold`，`-1` 关闭）会把完整输出落到本地 `.ssh-mcp-out/`（`outputSpillDir` 可改，保留最近 32 个，Unix 0600），结果只带通知+短预览；Agent 应该 Grep/Read 本地文件，不要远程重跑同一命令
- exec 默认**不分配 PTY**（避免 docker/npm 等误以为有交互终端）；交互命令用连接配置 `"pty": true` 或 `execute-command` 的 `pty` 参数显式开启

**Agent 安装 Skill**：仓库内 [`skills/2native-ssh-mcp-helper/SKILL.md`](../skills/2native-ssh-mcp-helper/SKILL.md) 可复制到 `.cursor/skills/` 后说「帮我配置 2native-ssh-mcp」。

## 命令行参数

```
2native-ssh-mcp [command] [options] [host port username password]

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
  --local-path-mode <cwd|list|any>  本地路径限制：cwd（默认）/ list / any
  --transport-mode <exec|shell>
  --command-template <template>
  --pty / --try-keyboard
  --command-log-size <n> / --command-log-dir <dir> / --command-log-only-success
  --pre-connect                     启动前预连接所有服务器（任一失败即退出，fail-fast）

  --allow-insecure-config-perms    跳过配置文件权限检查（不推荐，仅开发用；也可在配置文件的 $global 里声明）

Server options:
  --transport <stdio|http>         默认 stdio；start 隐含 http
  --http-addr <host:port>          默认 127.0.0.1:8338
  --http-token <token>             /mcp 的 Bearer token（非 loopback 监听必填；也可用 SSH_MCP_HTTP_TOKEN 或 $global.httpToken）
  --version, -v / --help
```

## 配置项速查

| 配置 | 默认 | 说明 |
|---|---|---|
| `$global.allowInsecureConfigPerms` | false | 跳过本配置文件权限检查（等价 `--allow-insecure-config-perms`，仅开发用） |
| `description` / `business` / `aliases` / `notes` | 空 | 给 list-servers 展示的元数据：用途、业务、别名、注意事项 |
| `transportMode` | `exec` | `shell` 用于堡垒机/跳板机场景；**要求远端是 POSIX `sh` 兼容的交互式 shell**（依赖 `PS1`、`stty`、`printf`、`export`），csh/tcsh/fish 堡垒机请用 `exec` + `commandTemplate` |
| `commandWhitelist` / `commandBlacklist` | 空 | 命令正则白/黑名单 |
| `allowedLocalPaths` / `allowedRemotePaths` | 空 | 文件传输路径白名单 |
| `localPathMode` | `cwd` | 本地路径限制：`cwd`（进程工作目录 + `allowedLocalPaths`）/ `list`（仅 `allowedLocalPaths`）/ `any`（不限制） |
| `commandLogSize` | 20 | 命令日志保留条数（0 关闭） |
| `commandLogDir` | `.ssh-mcp-logs` | 命令日志目录（`<dir>/<连接名>.log`） |
| `commandLogOnlySuccess` | false | 只记录成功命令 |
| `sftpConcurrency` / `sftpChunkSize` | 16 / 32768 | SFTP 并发数与分块大小 |
| `algorithms` | 空 | kex/cipher/serverHostKey/hmac 协商 |
| `hostKeyCheck` | `accept-new` | 主机密钥校验：`accept-new`（未知记录后接受）/ `strict`（未知拒绝）/ `none`（不校验）；`known_hosts` 文件及其目录（如 `~/.ssh`）不存在时自动创建 |
| `knownHostsFile` | `~/.ssh/known_hosts` | 主机密钥校验用的 known_hosts 文件 |
| `keepaliveIntervalMs` / `keepaliveCountMax` | 10000 / 3 | SSH 心跳 |
| `commandTimeoutMs` / `connectionTimeoutMs` / `sftpTimeoutMs` | 30000 / 30000 / 300000 | 各类超时（`sftpTimeoutMs` 为无进展超时，进度每刷新一次就重新计时） |
| `maxOutputBytes` | 10485760 | 单命令 stdout+stderr 合计输出上限，0 为不限 |
| `outputCompressLight` / `outputCompressThreshold` | true / 4096 | 大输出头尾压缩与阈值 |
| `outputSpillThreshold` / `outputSpillDir` | 8192 / `.ssh-mcp-out` | 输出达到阈值落盘到本地目录（`-1` 关闭） |
| `stripAnsi` | true | 输出剥离 ANSI 转义序列（false 保留颜色/进度条） |
| `commandTemplate` | 空 | 命令包装模板（`<command>` / `<quotedCommand>`） |
| `pty` | false | exec 模式分配伪终端（默认关闭，交互命令按需开启） |
| `redactSecrets` | false | 输出脱敏（password/token/Bearer/PEM），开启有扫描开销 |
| `tryKeyboard` | false | 键盘交互认证（2FA 码用环境变量 `SSH_MCP_2FA_CODE`） |

> 配置文件中的字符串支持 `${环境变量名}` 引用，凭据可放在环境变量里而不落盘。

## 认证方式

- 密码：`password`
- 私钥：`privateKey`（可带 `passphrase`，或环境变量 `SSH_MCP_PASSPHRASE`）
- ssh-agent：`agent`（Unix socket 路径；Windows 填 `pageant` 使用 Pageant）
- 键盘交互：`tryKeyboard: true`，密码提示用配置的密码，OTP 提示用 `SSH_MCP_2FA_CODE`

## 命令日志（远程执行记录，保留最后 N 条）

命令日志默认开启（每连接保留 20 条，`"commandLogSize": 0` 关闭）。每个连接执行过的命令会追加写入 `<commandLogDir>/<连接名>.log`，JSON 行格式，只保留最近 N 条，重启不丢失：

```json
{"timestamp":"2026-08-22T10:00:00+08:00","command":"ls -la /tmp","exitCode":0,"success":true}
```

- 全局默认：`--command-log-size <n>` / `--command-log-dir <dir>`（对未单独配置的连接生效）
- 单连接覆盖：`commandLogSize` / `commandLogDir` / `commandLogOnlySuccess`
- 配合 `commandLogOnlySuccess: true` 可只记录成功命令，避免探测类命令产生噪声

## 安全

- 启动时检查 `--config-file` 权限（Unix：`chmod 600` 文件 / `chmod 700` 目录；Windows：限制 ACL 修改权限），可用 `--allow-insecure-config-perms` 或配置文件内 `$global.allowInsecureConfigPerms: true` 跳过（不推荐，仅开发用）
- 输出脱敏默认关闭（`redactSecrets: true` 按连接开启）：Bearer token、PEM 私钥块、`password=`/`token=` 等；开启后落盘的 spill 文件也是脱敏后的内容
- 超时或输出超限时按远端 PID 向进程组发送 SIGTERM/SIGKILL（exec；channel Signal 仅作补充）或 Ctrl-C（shell/会话）
- MCP 工具标注 `readOnlyHint` / `destructiveHint`，客户端可据此限制危险操作

详见 [SECURITY.md](../SECURITY.md)。

## 自动发布 Release

推送**带消息的 tag** 即触发 GitHub Actions（`.github/workflows/release.yml`）：

```bash
git tag -a v1.0.1 -m "修复了 xxx"
git push origin v1.0.1
```

工作流会：
1. 交叉编译 6 个平台二进制：Windows / Linux / macOS × amd64 / arm64（`CGO_ENABLED=0`，版本号注入为 tag 名）
2. 每个二进制生成独立的 `.sha256` 校验和文件
3. 创建 GitHub Release，**日志内容 = tag 的 message**（通过 GitHub API 读取 annotated tag 对象，避免 runner 本地 lightweight tag 回退成 commit message 的问题），附件为全部二进制 + 校验和 + `.mcpb` + 一份 CycloneDX SBOM（`2native-ssh-mcp.cdx.json`）
4. 把 SLSA provenance 和 SBOM 证明发到仓库 Attestations 页。校验下载的文件：

```bash
gh attestation verify --repo daidaiJ/2native-ssh-mcp 2native-ssh-mcp-linux-amd64
```
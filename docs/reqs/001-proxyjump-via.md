> 英文版暂不提供。本文是实现契约，不是用户指南。

# REQ-001 OpenSSH ProxyJump（`via`）

状态：草稿  
分支：`feat/reqs-proxyjump-audit-2fa`  
依赖：无  
验证环境：本地两台 sshd（Docker / WSL），**不需要**公司堡垒机

## 1. 背景

当前 `dial` 只支持直连或 SOCKS5/HTTP/HTTPS 代理。`~/.ssh/config` 解析只取 HostName / User / Port / IdentityFile，忽略 `ProxyJump`。

国内常见的「RDP 后面再跳内网」有两条完全不同的路：

| 路径 | 协议 | 本需求 |
|---|---|---|
| OpenSSH jump：先连跳板，再 `direct-tcpip` 到内网 22 | RFC 4254 | **本需求要做** |
| 国产堡垒机只给交互菜单 / ForceCommand / 禁止 exec | 厂商私货 | **明确不做**，继续用已有 `transportMode: shell` + `commandTemplate` |

没有真堡垒机也能验收本需求：标准 OpenSSH 二次拨号与 Jumpserver 无关。

## 2. 目标

1. 连接配置增加 `via`：值为**另一个连接名**，目标 SSH 握手走跳板的 `direct-tcpip` 隧道。
2. 解析 `~/.ssh/config` 的 `ProxyJump <alias>`（单跳），映射到 `via`。
3. 不转发 agent。跳板与目标各用自己的认证与 `known_hosts`。
4. 目标连接的 `transportMode` / SFTP / 后台任务行为与直连相同——隧道只替换 TCP。

## 3. 非目标

- 多跳 `ProxyJump a,b,c`（第一期只允许单跳；配置出现逗号或空格分隔的多个 hop 时启动失败，错误信息写明）。
- `ProxyCommand`、`netcat`、`ssh -W` 包装。
- Agent forwarding（`-A`）。
- 把 `via` 与 `commandTemplate: "ssh jumphost …"` 合成一个开关。后者是「远端再 exec ssh」，超时、杀进程、SFTP 语义都不同。
- 声称兼容 Jumpserver / PAM 菜单式跳板。
- `shell` 模式登录跳板后再交互式 `ssh internal`（那是 shell + template 的事）。

## 4. 用户场景

运维机只能 SSH 到 `bastion`，内网 `10.0.1.50` 对运维机不可达。

```json
{
  "bastion": {
    "host": "bastion.example.com",
    "username": "deploy",
    "agent": "pageant"
  },
  "internal-db": {
    "host": "10.0.1.50",
    "username": "dbadmin",
    "privateKey": "~/.ssh/db_key",
    "via": "bastion"
  }
}
```

Agent 对 `internal-db` 调 `execute-command` / `file-transfer` 时，本机从不直接 TCP 连接 `10.0.1.50:22`。

## 5. 需求条目

### R1 配置

- `SSHConfig.Via`：`json:"via"`，可选，字符串，必须是**同一配置文件中的另一个连接 `name`**（或对象格式的键）。
- 启动 / `Normalize` 时解析：未知名、自指、环（A→B→A）→ 失败，错误带环路上的名字。
- `via` 指向的连接本身还可以有 `proxy`（运维机到跳板走 SOCKS/HTTP）；**目标不得再对内网地址走同一 proxy**——内网地址只出现在跳板的 `direct-tcpip` 里。
- 目标的 `proxy` / `socksProxy` 与 `via` 同时出现 → 配置错误（语义冲突，避免「到底谁代理」）。
- 跳板连接可被多个 `via` 目标复用（连接池已有懒连接 + keepalive）。

### R2 拨号

顺序：

1. 按现有逻辑拿到跳板 `*ssh.Client`（可走 proxy、Pageant、2FA）。
2. `bastion.Dial("tcp", net.JoinHostPort(target.Host, target.Port))`。
3. 在返回的 `net.Conn` 上对目标做 `ssh.NewClientConn`，使用**目标**的 User / Auth / HostKeyCallback / Algorithms / Timeout。
4. 目标握手超时仍用目标的 `connectionTimeoutMs`，并像现有 `dial` 一样给底层 conn 设 deadline（规避 `ssh.NewClientConn` 不尊重 `ClientConfig.Timeout`）。

跳板 TCP 断了：目标调用返回现有 `SSH_CONNECTION_LOST` 一类错误，`retriable` 语义与直连掉线一致，不无限挂起。

### R3 主机密钥

- 跳板与目标分别按各自 `hostKeyCheck` / `knownHostsFile` 校验。
- 默认仍是 `accept-new` 写 `known_hosts`。两条记录的 Host 字段必须能区分（跳板 hostname 与目标 hostname），不能把目标密钥记成跳板。

### R4 `~/.ssh/config`

`internal/sshconfig.Entry` 增加 `ProxyJump string`（单别名）。

支持的写法（第一期）：

```
Host internal
    HostName 10.0.1.50
    User dbadmin
    ProxyJump bastion
```

`ProxyJump bastion.example.com`（裸主机而不是已解析的 Host 别名）：若配置文件里没有同名连接，**不要静默新建连接**；Lookup 仍返回 `ProxyJump` 原文，上层在拼 MCP 连接时若无法解析为已有 `name` 则报配置错误，提示用户在 config.json 里显式声明跳板。

不支持：`ProxyJump user@host:port`、`-J` 多跳、`none`。遇到则 Lookup 或 Normalize 失败，不要截断成第一段装成功。

### R5 与现有能力的组合

| 组合 | 期望 |
|---|---|
| 目标 `transportMode: exec` | 隧道上 exec，与直连相同 |
| 目标 `transportMode: shell` | 隧道上 shell，与直连相同 |
| 目标 SFTP | 隧道上 SFTP（跳板不必开 SFTP） |
| 目标 `background: true` | 后台作业在**目标**上，闪断重附着走同一 `via` |
| 跳板空闲断开 | 目标下次调用先重连跳板再打隧道 |
| `list-servers` | 目标条目带 `via: "<name>"`，便于 Agent 理解不可直连 |

### R6 可观测性

- 日志：`Using ProxyJump for [internal-db] via [bastion]`（无密钥）。
- 拨号失败区分：跳板认证失败 / 跳板 TCP 到目标失败 / 目标认证失败，错误信息含连接名，不把三种合成一条 `SSH connection failed`。

## 6. 验收（无公司跳板机）

用 Docker 或 WSL 起两个 sshd：`bastion:2222` 可从测试进程到达；`internal:2223` **仅**对 bastion 容器可达（不要把 2223 映射到宿主机，或映射后测试进程禁止直连）。

必须通过：

1. `via: "bastion"` 的 `execute-command` 得到 `hop-ok`。
2. 测试进程对 internal 地址 `DialTimeout` 失败（证明不是直连）。
3. 目标用目标密钥；跳板密钥无法登录目标。
4. 关掉 bastion 后再调用，错误可识别且在 `connectionTimeoutMs` 量级返回。
5. 目标 `file-transfer` 上传小文件成功（证明 SFTP 走隧道）。
6. parser 单测：`ProxyJump bastion` 写入 Entry；`ProxyJump a,b` 失败。
7. 配置单测：环、自指、未知 via、via+proxy 同时出现 → Normalize 失败。

CI：`go test -tags integration ./internal/manager/ -run ProxyJump` 在有 Docker 的 job 上跑；无 Docker 的默认 `go test ./...` 只跑 parser / Normalize 单测。

无法验收、文档禁止写的：任何具体国产堡垒机品牌。

## 7. 实现落点

| 位置 | 改动 |
|---|---|
| `internal/config/config.go` | `Via` 字段、环检测、via 与 proxy 互斥 |
| `internal/sshconfig` | `ProxyJump`、多跳拒绝 |
| `internal/manager/dial.go` | 跳板 `Dial` + 目标 `NewClientConn` |
| `internal/manager/manager.go` | 确保 via 目标复用跳板 client；跳板断开时目标失效 |
| `internal/tools/list_servers.go` | 输出 `via` |
| `docs/HUMAN_GUIDE.md` + `.en.md`、`AGENT_GUIDE`、`FEATURES` | 实现后同步；本文不替代用户文档 |

## 8. 实现时注意

- 不要在跳板上 `exec("ssh internal …")` 冒充 ProxyJump。
- `ssh.Client.Dial` 走的是 `direct-tcpip`，跳板 sshd 须允许 TCP forwarding（OpenSSH 默认允许；测试镜像不要 `AllowTcpForwarding no`）。
- 目标 `host` 可以是内网 IP；known_hosts 用这个 IP（或 HostName）做 key。

## 9. 开放问题

无。多跳与 `user@host` 语法若有真实用户再开后续需求，不在本期膨胀。

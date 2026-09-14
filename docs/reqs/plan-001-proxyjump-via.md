# 实现规划：REQ-001 OpenSSH ProxyJump（`via`）

契约：[001-proxyjump-via.md](001-proxyjump-via.md)  
实现分支：`feat/proxyjump-via`  
建议最后做：依赖稳定的 `dial`（REQ-003 若先合，跳板 2FA 自动可用）。默认 CI 无 Docker，容器测试必须是可选 tag。

## PR 边界

可以拆两个提交、仍然一个 PR：

1. parser + 配置 Normalize（纯单测）
2. `dial` 隧道 + Docker integration（`-tags integration`）

不要把 `commandTemplate ssh` 和 `via` 放进同一段代码路径。

## 任务

### T1.1 配置与环检测

文件：`internal/config/config.go` 及现有 `*_test.go`

- `SSHConfig.Via string` `json:"via"`
- 在加载完整连接表之后做图校验（单条 `Normalize` 看不到其它名字）：
  - 未知 via
  - 自指
  - 环（A→B→A）
  - `via` 与 `proxy`/`socksProxy` **同时出现在目标上**（跳板自己可以有 proxy）
  - `via` 含逗号或空格（多跳拒绝）
- 错误信息带上路经上的连接名。

完成标准：表驱动测试覆盖上面每一种失败；合法 `internal.via=bastion` 通过。

### T1.2 sshconfig ProxyJump

文件：`internal/sshconfig`

- `Entry` 增加 `ProxyJump string`
- 只接受单个 token（别名或裸主机名）
- `ProxyJump a,b` / `user@host` / `host:port` → Lookup 或上层 Normalize 失败，不要截成第一段
- 上层把 `ProxyJump` 别名对上已有连接 `name` 才写入 `Via`；对不上则配置错误，**不要静默新建**跳板连接

完成标准：`TestLookupProxyJump` / `TestLookupProxyJumpMultiHopRejected`。

### T1.3 dial 二次握手

文件：`internal/manager/dial.go`、`manager.go`

顺序：

1. `getClient(viaName)` 拿到跳板 `*ssh.Client`（走跳板自己的 proxy / Pageant / 2FA）
2. `bastion.Dial("tcp", joinHostPort(target.Host, target.Port))`
3. 对返回的 conn 做目标 `ssh.NewClientConn`（目标的 User/Auth/HostKey/Algorithms/Timeout + 现有 deadline 补丁）
4. 三种失败分开包装：跳板认证 / 跳板 TCP 到目标 / 目标认证

锁：拿跳板 client 时不要一直握着锁去等目标握手（目标 2FA 可能弹窗）。

Host key：目标 `known_hosts` 条目用目标 HostName/IP，不能写成跳板。

日志：`Using ProxyJump for [internal-db] via [bastion]`，无密钥。

`list-servers` 输出 `via` 字段。

完成标准：见 T1.4。单测可用 fake `ssh.Client` 的难度高，这一层以 integration 为主；错误包装可用注入 `dialTCP` 的方式测「目标 proxy+via 在 dial 前就被 Normalize 拦住」。

### T1.4 双 sshd fixture

新：`internal/manager/proxyjump_integration_test.go` 带 `//go:build integration`

推荐 Docker Compose 两服务：

- `bastion`：映射 `127.0.0.1:2222:22`，`AllowTcpForwarding yes`
- `internal`：**不映射到宿主机**，只挂在 compose 网络，hostname `internal`，sshd 22
- 两套不同密钥；测试进程只拿得到 bastion 的 host 端口

用例：

| 测试名 | 断言 |
|---|---|
| `TestProxyJumpExecHopOK` | `echo hop-ok` |
| `TestProxyJumpNotDirect` | 测试进程 Dial internal:22 失败 |
| `TestProxyJumpWrongKey` | 跳板密钥登目标失败 |
| `TestProxyJumpBastionDown` | 停 bastion 后错误可识别，且在 connectionTimeout 量级返回 |
| `TestProxyJumpSftp` | 目标上传小文件成功 |

无 Docker 时 `t.Skip`。不要改现有 `go test -race ./...` 让 CI 变红。

可选后续：CI 加 `integration` job（`services: docker`），本期不强制。

### T1.5 组合与文档

- 目标 `transportMode: shell` 仍走隧道（可只在 integration 加一条 `echo`，不必上堡垒机）。
- 后台任务：文档写明作业在**目标**上；闪断重连仍走同一 `via`。若本 PR 时间紧，后台重附着可跟现有 `ubuntu` 集成测试一样标「有 Docker 再跑」，但代码路径不能特殊 casing 掉 via。
- HUMAN_GUIDE / AGENT_GUIDE / FEATURES：配置示例、与 `commandTemplate ssh` 的对比表、**明确不兼容**菜单式堡垒机。
- 测试镜像不要 `AllowTcpForwarding no`。

## 顺序

```
T1.1 → T1.2 → T1.3 → T1.4
                     T1.5 文档与 list-servers 可与 T1.4 并行
```

## 不做（本 PR）

多跳、`ProxyCommand`、agent forwarding、Jumpserver、把 `via` 做成 `ssh jumphost cmd`。

> 英文版暂不提供。本文是三条需求的共同前提，不是实现契约。

# 为什么没有跳板机也能做这三项——以及什么仍然不能做

下面五条是选型讨论里对「没这种跳板机环境，做了也不知道对不对」的答复，原样收成产品边界。REQ-001/002/003 和对应规划文档都受这五条约束。

---

## 1. ProxyJump：别等真环境

`via` 不是「登录跳板再敲 ssh」，而是：

1. 先对 bastion 做完 SSH 握手
2. 在这条连接上开 `direct-tcpip` 到内网 `host:22`
3. 在这条 TCP 隧道上再做一次 SSH 握手（另一套密钥，**不**把 agent 转进去）

这是 RFC 4254 的标准通道，和 Jumpserver 网页、ForceCommand、菜单式跳板无关。tufantunc 能写 `via`，是因为用 Docker 起了两台 sshd，不是因为作者有机房。

本机就能证「对」的最小拓扑（WSL / Docker 都行）：

```
[测试进程] --SSH--> bastion:2222 --direct-tcpip--> internal:2223 --sshd--> 执行 echo hop-ok
```

断言只要这几条：

- `via` 指向的 profile 先连通，目标 profile 的 TCP 不直接暴露也能 exec
- 测试进程对 internal 地址直连失败（证明走的是隧道，不是事先做好的端口转发）
- 目标用的是目标自己的 key，bastion 的 key 登不进去
- bastion 挂了之后目标调用返回可识别错误，而不是挂死
- `~/.ssh/config` 的 `ProxyJump bastion` 解析进 `via`（当前 parser 只吃 HostName/User/Port/IdentityFile，这一项是纯字符串测试，零网络）

证过这些，就可以在文档里写：**兼容 OpenSSH ProxyJump，不兼容只给交互菜单的堡垒机。** 这不是含糊，是边界。没有真 Jumpserver 时，**不要声称**支持那种机子。

`commandTemplate: "ssh jumphost <quotedCommand>"` 是另一条路（远程再 ssh），行为、超时、杀进程、SFTP 都和 `via` 不同。没有环境时更不该把两条混成一个功能。

→ 落成 [REQ-001](001-proxyjump-via.md) / [规划](plan-001-proxyjump-via.md)

---

## 2. 角色矩阵 / OPA / 审计：更不该卡在跳板机上

这些代码路径在 `dial()` 之前就结束了。现有 `commandLog` 测试已经是这个层级（截断、只记成功、JSONL）。

- 矩阵：给 `(role, group, commandClass)` 一张表，单测就能覆盖。**本期不搬。** 没有合规客户点名、没有 OPA 集群时，抄 tufantunc 的角色×tier 是无法验收的表面积。
- 防篡改：hash chain 的下一行必须含上一行摘要；手改中间一行校验失败。**本期做这个。**
- OPA：本地 httptest 就能证。**本期不搬。** 没 OPA 的用户不该被强迫依赖它。

等保要的是「谁在何时对哪台执行了什么、改不了日志」，不是「必须有跳板机才能写这段代码」。授权继续用已经存在的 whitelist / blacklist / `approvalMode` / 路径白名单。

→ 落成 [REQ-002](002-audit-policy.md) / [规划](plan-002-audit-chain.md)

---

## 3. 2FA：环境变量过期是设计问题，不是环境问题

现在的实现是一次性把 `SSH_MCP_2FA_CODE` 塞进 keyboard-interactive（password 题用配置密码，其它题用这个环境变量）。码在进程启动时就冻住，TOTP 30 秒一变，HTTP daemon 常驻时一定过期。这在任何会出 OTP 题的 sshd 上都能复现，**不必等公司堡垒机**。

本地可证的两档：

**A. 进程内假服务器（先做）**  
用 `golang.org/x/crypto/ssh` 起一个只做 keyboard-interactive 的 listener：第一题 `Password:`，第二题 `Verification code:`。断言：

- 密码走配置，OTP 不走环境变量，而走 **MCP elicitation**（和破坏性命令审批同一条人机通道）
- 用户按时填对 → 握手成功
- 超时 / 取消 → 明确错误，不拿过期 env 顶上
- 连接保活期内不重新出题；**下次 `dial`（空闲断开后）再弹**

这证的是协议和 UX，证不了「某某堡垒机 prompt 文案是中文验证码还是 Token」。文案匹配做成：password 类关键词（已有「密码」）+ 其余一律当 OTP 弹窗，比猜 prompt 字符串更稳。

**B. 容器里的真 sshd + PAM（可选，不进默认 CI）**  
`ChallengeResponseAuthentication yes` + 固定码 PAM。用来证 OpenSSH 默认 keyboard-interactive 没被 AuthMethod 顺序踩死。仍然**不是** Jumpserver。

真堡垒机上永远无法预知：先密钥再 OTP、OTP 在密码前、一次登录两道题、banner 里夹 prompt。没有录包就不要写「支持 XX 堡垒机 2FA」。能诚实写的是：**keyboard-interactive 的非密码题通过 MCP 弹窗问人，不缓存 TOTP。**

→ 落成 [REQ-003](003-keyboard-interactive-elicitation.md) / [规划](plan-003-kbdint-elicitation.md)

---

## 4. 现在真正缺的测试，不是 tufantunc 那三项

仓库里带 `//go:build integration` 的测试（`exec_integration_test.go` 等）要求本机 `config.json` 里有一台叫 `ubuntu` 的直连机。也就是说：

- 超时杀进程组、无 PTY、SFTP、后台重附着——有真机才能跑
- **shell 模式对堡垒机、ProxyJump、2FA elicitation——默认 CI 都不跑**

没有跳板机时，优先补的不是 OPA，而是一个 **CI 里能起的双 sshd fixture**（testcontainers 或 `docker compose` 两个 OpenSSH）。这能把 `via`、假 keyboard-interactive、以及「shell 模式串行 + 无 SFTP」变成可回归的绿。国产堡垒机仍然标 `unverified`，直到有人拿录包或一台测试账号回来。

当前 CI（`.github/workflows/ci.yml`）只有 `go test -race ./...`，无 Docker job。REQ-001 的容器测试要么加可选 job，要么保持 `-tags integration` 本机跑；不要让默认 CI 因为没 Docker 变红。

---

## 5. 没有环境时不该做的

- 不要把 `via`、Jumpserver 菜单、`commandTemplate ssh` 做成同一个开关，测不了就分不清是谁坏了。
- 不要在 README / FEATURES 写「支持堡垒机 2FA / 等保审计」——本地假服务器只能支撑「支持 keyboard-interactive 弹窗」和「命令日志不可篡改」。
- 不要为了对齐对比矩阵去搬 tufantunc 的角色×tier 和 OPA。那是另一类买家；这个仓库的买家是 RDP 后面的 Windows 运维机。没有环境、没有合规客户点名，搬过去是无法验收的表面积。

**可做、且能自己验收：** OpenSSH `ProxyJump`（双容器）+ keyboard-interactive 改成 elicitation（假 sshd）+ 审计日志加强（纯单测）。

**做成了也不知道对不对、应该明确不做或标实验：** 任何「跟某某堡垒机一模一样」的声明。

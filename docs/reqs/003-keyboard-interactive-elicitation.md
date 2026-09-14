> 英文版暂不提供。本文是实现契约，不是用户指南。

# REQ-003 Keyboard-interactive 用人机弹窗答 OTP

状态：草稿  
分支：`feat/reqs-proxyjump-audit-2fa`  
依赖：无（复用已有 MCP elicitation，与 REQ-001 的 `via` 正交；跳板若开 2FA，弹窗发生在跳板握手）  
验证环境：进程内假 sshd（`golang.org/x/crypto/ssh`），**不需要**公司堡垒机或真 TOTP 服务器

## 1. 背景

`tryKeyboard: true` 时，`keyboardInteractive`（`internal/manager/dial.go`）用配置里的密码答「password/密码」题，其余题用进程环境变量 `SSH_MCP_2FA_CODE`。

问题：

- TOTP 约 30s 一轮。stdio 每次拉起或许赶得上；HTTP daemon 常驻后环境变量冻在启动时刻，第二次空闲重连必然过期。
- RDP 运维机上改环境变量再重启 daemon，比在弹窗里敲 6 位码重。
- 破坏性命令已经用 MCP elicitation 问人（`internal/tools/approval_gate.go`）；OTP 却走旁路。

本需求把「非密码的 keyboard-interactive 题」接到同一条人机通道。证的是协议与 UX，**不声称**兼容某品牌堡垒机的 prompt 文案。

## 2. 目标

1. `tryKeyboard` 握手时：密码类题仍用配置密码；其它题通过 MCP elicitation 问当前用户。
2. 不把 OTP 写进连接配置、不缓存到下次 `dial`。保活期内的已建立连接不再出题。
3. `SSH_MCP_2FA_CODE` 降为无弹窗环境的后备（CI / 假客户端），不再是主路径。
4. 人没答、客户端不会弹、超时 → 握手失败，**不**用空字符串或过期 env 蒙混成功（与审批闸门的 fail-open 不同：这里失败的是认证，不是「锁死运维」）。

## 3. 非目标

- 实现 TOTP 生成器、读取手机种子。
- 自动识别「某某堡垒机」的中文/英文 prompt 全集。
- 在 SSH 握手阶段做 WebAuthn / FIDO。
- 改密码认证本身（密码仍来自配置 / `${ENV}` / agent / 密钥）。
- 为 2FA 单独加 MCP 工具。Agent 不可见 OTP。
- 保证 Jumpserver 网页 OTP 可用（那根本不走 SSH keyboard-interactive）。

## 4. 用户场景

HTTP daemon 已运行。空闲断开后 Agent 再次 `execute-command`：

1. 懒连接触发 `dial`。
2. 服务器出 `Verification code:`（echo=false）。
3. MCP 客户端弹出与破坏性审批类似的框，标题含连接名，正文含服务器给的 question 原文。
4. 用户输入 6 位码，握手成功，命令执行。
5. keepalive 有效期内第二条命令不再弹。
6. 再空闲断开后重连，再弹一次（新 TOTP）。

## 5. 需求条目

### R1 回调要能弹窗

现在 `buildAuthMethods` / `keyboardInteractive` 拿不到 `MCPServer` 和 `context`。需要把 elicitation 能力注入 `Manager` 或 `dial`（例如 tool 层在每次调用设好 `Elicitor`，dial 闭包捕获）。

约束：

- elicitation 必须用**当前工具调用**的 context，带超时（建议默认 2 分钟，短于审批的 5 分钟；可配，但不要无限等）。
- 无 MCP 会话时（测试、未来若有启动期 pre-connect）走 R3 后备，不 panic。

### R2 答题规则

对 `questions[i]`：

1. 小写后包含 `password` 或 `密码` → 配置中的 `password`；未配置则该槽空，让 SSH 认证失败。
2. 否则视为 OTP/挑战： elicitation 问人；`echos[i]==false` 时客户端应按密码框渲染（能设 `format` 就设，不能则明文也接受——OTP 不是长期密钥）。
3. 一道握手多题：一次 SSH callback 里可能 1..N 题。允许**一次弹窗收集全部非密码题**（表单多字段）或逐题弹；实现选一种并在测试里钉死。推荐一次弹完，少打断。
4. 用户 Decline / Cancel / 超时 → 回调返回 error，dial 失败，错误码稳定（新建如 `SSH_AUTH_CHALLENGE_DECLINED` / `SSH_AUTH_CHALLENGE_TIMEOUT`），`retriable=false`（盲着重放不会变出 OTP）。
5. 客户端不支持 elicitation：
   - 若 `SSH_MCP_2FA_CODE` 非空 → 用它答所有非密码题（现有行为，供 CI）。
   - 否则 dial 失败，错误说明需要会弹窗的客户端或 env 后备。**禁止**空答案碰运气。

### R3 环境变量后备

- 文档改口：`SSH_MCP_2FA_CODE` 是 headless 后备，不是推荐用法。
- 主路径（客户端支持 elicitation）**忽略**环境变量，避免「弹窗里填了新码、闭包却先读到过期 env」。
- 优先级写进 HUMAN_GUIDE：elicitation 有能力则只用弹窗；否则才读 env。

### R4 与审批弹窗的关系

- 复用 `RequestElicitation`，不要第二套协议。
- 文案必须能区分：OTP 框标题含 `SSH authentication` / `SSH 认证` + 连接名；审批框保持现有破坏性命令文案。避免用户把 OTP 当成「确认 rm」。
- OTP 弹窗失败不得改成 fail-open 去执行命令——此时连接还不存在。

### R5 与 `via`（REQ-001）

若目标 `via` 跳板 `tryKeyboard: true`，弹窗发生在**跳板**握手。目标自己再 2FA，第二次弹窗，标题用目标连接名。两跳两套题，不要串配置密码。

本期 003 不依赖 001 先合并；假服务器单跳即可验收。两跳组合测放在 001 的 integration 里后续补。

### R6 日志

- 只记 `keyboard-interactive challenge for [name], N questions`。
- 禁止把 question 原文以外的答案、OTP、密码打到 stderr。question 原文可记（通常是 `Verification code:`）。

## 6. 验收（假 sshd）

用 `golang.org/x/crypto/ssh` 在 `127.0.0.1:0` 起一个只启用 keyboard-interactive 的 server：

- 题 1：`Password:` echo=false，期望配置密码。
- 题 2：`Verification code:` echo=false，期望回调提供的码。
- 码对则接受会话，`exec` 返回 `auth-ok`。

必须通过的单测（可放 `internal/manager`，不必 `-tags integration`）：

1. Fake elicitor 返回正确 OTP → `ExecuteCommand` 成功。
2. Fake elicitor Decline → dial 失败，命令未执行。
3. Fake elicitor 超时 → 失败，错误码为 timeout 类。
4. 无 elicitor + 无 env → 失败。
5. 无 elicitor + `SSH_MCP_2FA_CODE` 正确 → 成功（CI 后备）。
6. 有 elicitor 时即使 env 是错的，也用 elicitor 的正确码（env 不得抢主路径）。
7. 密码题不弹窗（elicitor 调用次数 = 非密码题次数或 1 次多字段，按实现钉死）。

可选（有 Docker 再跑）：真实 OpenSSH `ChallengeResponseAuthentication` + 固定 PAM 码。**不**作为默认 CI 门槛。

无法验收、文档禁止写的：具体堡垒机品牌、短信/微信二次认证。

## 7. 实现落点

| 位置 | 改动 |
|---|---|
| `internal/manager/dial.go` | 回调接 elicitor；优先级 R3 |
| `internal/manager` 或 `internal/tools` | `Elicitor` 接口，tool 调用注入 |
| `internal/tools/approval_gate.go` | 可抽公共 elicitation 超时/错误映射，OTP 与审批文案分开 |
| `docs/HUMAN_GUIDE.md`、`AGENT_GUIDE.md`、`FEATURES.md` | 实现后改 2FA 说明 |

## 8. 实现时注意

- `KeyboardInteractiveChallenge` 在握手线程跑；elicitation 是 RPC。不要在回调里死锁 Manager 的连接锁。建议 dial 不持 `Manager` 的全局锁等用户。
- HTTP daemon 多客户端：OTP 必须弹给**触发这次 dial 的那个 MCP session**，不能广播。
- 用户取消后不要自动改读 env 再试同一握手。

## 9. 开放问题

任务拆解、测试表与拍板表见 [README.md §3](README.md#3-2fa环境变量过期是设计问题不是环境问题)。

- 多非密码题用单次多字段还是连弹：推荐单次，实现 PR 按此写测试名。
- OTP elicitation 超时默认 2 分钟是否写入连接配置：第一期用常量即可，有人嫌短再加字段。

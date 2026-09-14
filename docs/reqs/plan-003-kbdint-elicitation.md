# 实现规划：REQ-003 Keyboard-interactive 弹窗

契约：[003-keyboard-interactive-elicitation.md](003-keyboard-interactive-elicitation.md)  
实现分支：`feat/kbdint-elicitation`  
建议在 REQ-001 之前合：会改 `dial.go` 认证回调，跳板 2FA 直接复用。

## PR 边界

一个 PR。假 sshd 测试必须和回调改动同一提交，否则主路径会在「有 elicitation 时忽略过期 env」上行为变化却没人护着。

## 任务

### T3.1 Elicitor 接口

不要让 `manager` 直接 import MCP server 包去调 `RequestElicitation`（循环依赖、测试难）。

```go
// 放在 internal/manager 或 internal/authprompt
type Challenge struct {
    Connection string
    Name       string // SSH 服务器给的 name
    Instruction string
    Questions  []string
    Echos      []bool
}

type Elicitor interface {
    // Prompt 只接收非密码题。返回与 questions 等长的 answers。
    Prompt(ctx context.Context, ch Challenge) (answers []string, err error)
}
```

错误要能区分：用户拒绝、超时、客户端不支持。tools 层用现有 `RequestElicitation` 适配。

完成标准：接口 + 一个 `RecordingElicitor` 测试替身（记录调用次数和入参）。

### T3.2 注入路径

- `Manager` 增加 `SetElicitor(Elicitor)` 或每轮 `ExecuteCommand` 从 context 取。
- 推荐 **context 值**：tools 在 `approvalGate` 旁边 `context.WithValue`，`dial` 的 keyboard-interactive 闭包读它。HTTP daemon 多客户端时 OTP 只弹给触发这次 dial 的 session。
- `dial` **不得**在持有 Manager 连接表锁的情况下等用户。先释放锁再握手，或握手用临时 client 成功后再挂进表（看现有 `getClient`/`dial` 锁顺序，改的时候画一眼，避免死锁）。

完成标准：注释或小测试证明 fake elicitor 阻塞 50ms 时另一连接的 `list-servers` 仍能返回。

### T3.3 改 `keyboardInteractive`

文件：`internal/manager/dial.go`

规则（按顺序）：

1. 题面含 `password` 或 `密码` → 配置密码，不调 Elicitor。
2. 否则非密码题：
   - context 里有 Elicitor → **忽略** `SSH_MCP_2FA_CODE`，一次 Prompt 收齐所有非密码题（推荐单次多字段）。
   - 无 Elicitor 且 env 非空 → 现有行为（CI 后备）。
   - 无 Elicitor 且 env 空 → 返回 error，不要交空字符串。
3. Decline / Cancel / DeadlineExceeded → 包装成稳定 `ToolError`：
   - `SSH_AUTH_CHALLENGE_DECLINED`
   - `SSH_AUTH_CHALLENGE_TIMEOUT`
   - `retriable=false`

超时默认 2 分钟，常量即可，第一期不加配置字段。

完成标准：见 T3.4 测试表。

### T3.4 进程内假 sshd

新文件：`internal/manager/kbdint_test.go`（默认 `go test` 就能跑，不要 `integration` tag）。

假服务器：只开 keyboard-interactive；题 1 `Password:` echo=false；题 2 `Verification code:` echo=false；码对则 `exec` 回 `auth-ok`。

| 测试名 | 给定 | 期望 |
|---|---|---|
| `TestKbdintElicitorSuccess` | 密码配置正确，elicitor 返回正确 OTP | exec 成功 |
| `TestKbdintElicitorDecline` | elicitor 拒绝 | dial 失败，命令未跑 |
| `TestKbdintElicitorTimeout` | elicitor 超过 deadline | `SSH_AUTH_CHALLENGE_TIMEOUT` |
| `TestKbdintNoElicitorNoEnv` | 都没有 | 失败 |
| `TestKbdintNoElicitorEnvOK` | 无 elicitor，env 正确 | 成功（CI 后备） |
| `TestKbdintElicitorBeatsStaleEnv` | elicitor 正确、env 错误 | 成功（env 不得抢主路径） |
| `TestKbdintPasswordNotElicited` | 两道题 | elicitor.Questions 不含密码题 |

不要把 OTP 打进 `t.Log` 以外的生产 logger。

### T3.5 tools 适配与文案

- `internal/tools`：MCP elicitation 请求标题含 `SSH 认证` / `SSH authentication` + 连接名；正文用服务器 question 原文。
- 与 `approval_gate` 的破坏性命令文案必须能从字符串上区分（加测试：两种 request 的 message 不同）。
- 无 elicitation 能力 → 不调 Prompt，走 T3.3 的 env/失败分支。
- OTP 失败不得变成审批那种 fail-open 去执行命令（此时还没有连接）。

### T3.6 文档

- HUMAN_GUIDE：主路径是弹窗；`SSH_MCP_2FA_CODE` 改为 headless 后备。
- FEATURES / AGENT_GUIDE：空闲重连会再弹；保活期内不弹。
- 禁止写具体堡垒机品牌。

## 顺序

```
T3.1 → T3.2 → T3.3 → T3.4
              ↘ T3.5（依赖 T3.1，可与 T3.4 并行）
T3.6 最后
```

## 不做（本 PR）

真 PAM 容器（可另开可选 job）、WebAuthn、在 REQ-001 之前做两跳双 2FA（001 的 integration 里再补）。

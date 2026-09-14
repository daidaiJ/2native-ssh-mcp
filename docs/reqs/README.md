# 需求与实现规划

对应对比结论里三条**能在没有公司跳板机的情况下做对**的改进。需求是契约，规划是拆任务；实现时各开 PR，不要捆成一个提交。

## 决策前提

[000-local-verification.md](000-local-verification.md) 收录当时答复的五条，三条需求都受它约束：

1. ProxyJump 用两台本地 sshd 就能证，不要等公司跳板机
2. 审计是本机哈希链；角色矩阵和 OPA 本期不搬
3. 2FA 环境变量过期是设计问题，用假 sshd + MCP 弹窗验收
4. 真正缺的是 CI 里的双 sshd fixture，不是 tufantunc 那套授权引擎
5. 没有环境时：不混 `via` / 菜单堡垒机 / `commandTemplate ssh`，不宣称品牌兼容，不抄 OPA

**可做且能自己验收：** OpenSSH ProxyJump（双容器）+ keyboard-interactive 弹窗（假 sshd）+ 审计链（纯单测）。  
**做成了也不知道对不对：** 任何「跟某某堡垒机一模一样」的声明。

## 需求（契约）

| ID | 文档 | 做什么 | 本地如何知道是对的 | 明确不做 |
|---|---|---|---|---|
| [REQ-001](001-proxyjump-via.md) | OpenSSH `via` / ProxyJump | 跳板 `direct-tcpip` 二次握手 | 两台 sshd 容器 + parser 单测 | 国产交互式堡垒机、多跳、`ssh jumphost cmd` 冒充 |
| [REQ-002](002-audit-policy.md) | 防篡改命令审计 | 追加 JSONL + 哈希链 + `audit-verify` | 纯单测 | OPA、角色×环境矩阵、把输出写进审计 |
| [REQ-003](003-keyboard-interactive-elicitation.md) | 2FA 弹窗 | keyboard-interactive 非密码题走 MCP elicitation | 进程内假 sshd | 真 TOTP 服务器、品牌堡垒机 prompt |

## 实现规划（任务拆解）

| 规划 | 对应需求 | 实现分支 | PR |
|---|---|---|---|
| [plan-002](plan-002-audit-chain.md) | REQ-002 | `feat/audit-chain` | T2.1–T2.6，一个 PR |
| [plan-003](plan-003-kbdint-elicitation.md) | REQ-003 | `feat/kbdint-elicitation` | T3.1–T3.6，一个 PR |
| [plan-001](plan-001-proxyjump-via.md) | REQ-001 | `feat/proxyjump-via` | T1.1–T1.5，一个 PR（parser 与 dial 可两个 commit） |

## 建议实现顺序

三份无代码依赖，可并行。若串行：

1. **REQ-002** 最小、全单测、会改 `commandLog` 语义，先合减少冲突。
2. **REQ-003** 改 `dial` 认证回调，与 001 的 `via` 都会动 `dial.go`；先合 003 再在跳板握手上复用 elicitor。
3. **REQ-001** 需要 Docker integration，放最后；默认 `go test ./...` 不得变红。

本目录是契约和任务表。用户可见说明仍以 `HUMAN_GUIDE` / `FEATURES` 为准，在对应实现 PR 里同步，不要只改这里。

## 本分支做什么

文档落在 `feat/reqs-proxyjump-audit-2fa`。这里只合文档，不堆实现代码。实现从 main（或本文合入后的 main）再开上表里的三条 feat 分支。

# AGENTS.md — 2native-ssh-mcp

SSH-based MCP server (Go): remote command execution + file transfer over MCP.
Human-facing docs live in `README.md` and `docs/`; agent-facing API reference is
`docs/AGENT_GUIDE.md`; the defensive playbook for agents driving this server is
`skills/2native-ssh-mcp-agent/SKILL.md`.

## 🔄 Handoff 摘要

（无进行中的交接。）

### escaping-optimizations — done

- **已落地：** `execute-command` 的三种命令形态 —— `cmdString`（shell）、`cmdString`+`args[]`（argv，服务端逐参数引用）、`scriptFile`（本地脚本 SFTP 暂存到远端后执行）；`scriptCleanup` / `scriptInterpreter` 接线；lint 接线（`off`/`warn`/`strict`，`strict` 报 `COMMAND_LINT_FAILED`）；连接建立后一次的远端 `base64`/`jq` 探测（`autoProbe` 可关，`list-servers` 显示 `capabilities=`）
- **可审计性（用户硬要求）：** 白/黑名单、破坏性分类器、审批弹窗与审计记录看的都是**可读等效命令** —— argv 记 `grep -rn 'foo bar' /etc` 这种最小引用形式，`scriptFile` 记脚本正文（首行注释标注来源、暂存路径、解释器、是否清理），不做 base64 之类的包装
- **验证证据：** `go test ./...` 绿（含结构门禁）；`golangci-lint run ./...` 0 issues；`go test -tags integration ./internal/manager/` 全绿（Ubuntu 24.04 实测：argv 逐字节还原且对照 naive join 会拆分/展开、暂存脚本执行+清理、后台模式经清理子壳保住退出码 5、lint warn/strict、审计记录内容、能力探测与关闭）；MCP stdio 端到端驱动（`tools/list` 参数表、argv/scriptFile/lint 调用、`capabilities=base64,jq`）
- **详情指针：** [`.handoff/escaping-optimizations.md`](.handoff/escaping-optimizations.md)

### 未验证事项

- [ ] `make check` 本机仍未跑过（未装 `make`）；等价地逐条跑了 `golangci-lint run ./...` + `go test ./...`，CI 的两道门禁与之一致

---
name: 2native-ssh-mcp-agent
description: >-
  Use when an AI agent operates 2native-ssh-mcp remotely: choosing connections,
  writing safe commands, limiting output/token use, and using sessions. Invoke
  before destructive remote work or when command output is large (logs, find,
  kubectl, docker ps -a).
---

# 2native-ssh-mcp-agent

Defensive playbook for agents using **2native-ssh-mcp**. Read this before executing on production hosts.

## Golden rules

1. **Call `list-servers` first** — read metadata, aliases, notes; pick `connectionName` or plan a `sessionName`.
2. **Never use root** — use a dedicated low-privilege account.
3. **Prefer narrow commands** — scope with `head`, `tail`, `grep`, `wc`, paths; avoid blind `cat` on large files.
4. **Respect whitelist/blacklist** — if a command is rejected, do not bypass; ask the operator to widen policy.
5. **Secrets** — server redacts Bearer tokens, PEM blocks, `password=` lines; still avoid printing credentials.

## Output & tokens

### Built-in (automatic)

2native-ssh-mcp applies **light compression** when output ≥ 4KB (default):

- Head + tail lines, middle replaced with `... [N lines omitted] ...`
- Consecutive duplicate lines collapsed (`... [repeated K times]`)
- Excessive blank lines trimmed

Disable per connection: `"outputCompressLight": false` in config.

Tune threshold: `"outputCompressThreshold": 8192`.

### Agent-side habits (before running)

| Bad | Better |
|---|---|
| `cat /var/log/app.log` | `tail -n 100 /var/log/app.log` |
| `find / -name '*.log'` | `find /var/log -maxdepth 2 -name '*.log' 2>/dev/null \| head -50` |
| `docker ps -a` (huge) | `docker ps --format 'table {{.Names}}\t{{.Status}}' \| head -30` |
| `kubectl get pods -A` | add `\| head` or `-l app=...` |
| `ls -laR /` | `ls -la /specific/path` |

## Tool choice (4 tools)

| Goal | Tool |
|---|---|
| Discover hosts + sessions | `list-servers` |
| One-shot command | `execute-command` |
| Multi-step same CWD | `session action=open` → `execute-command` + `sessionName` → `session action=close` |
| Long-running (tail -f, build) | `session action=open` + `background=true` → `session action=read` loop → `close` |
| Files | `file-transfer` (exec mode only) |

## Session patterns

**Stateful deploy:**
```
session(action=open, sessionName=deploy, connectionName=dev)
execute-command(sessionName=deploy, cmdString="cd /app && git status")
execute-command(sessionName=deploy, cmdString="git pull")
session(action=close, sessionName=deploy)
```

**Tail logs:**
```
session(action=open, sessionName=logs, connectionName=dev,
        background=true, cmdString="tail -n 200 -f /var/log/syslog")
session(action=read, sessionName=logs)   # repeat until running=false
session(action=close, sessionName=logs)
```

**Long tasks / no-output tasks: use `session background=true` + `read` polling. Do NOT `nohup ... &` / `setsid` through `execute-command`** — those die when the exec channel closes. Background jobs survive connection drops; after a drop the session shows `disconnected=true` and `read`/`execute-command` reconnect automatically. Only `action=close` kills the remote job.

## Reading results

- **Non-zero exit is a normal result** — read `[exit code] N` from the text; do not treat it as a transport failure.
- **`SSH_CONNECTION_LOST` (retriable=false)**: the connection dropped mid-command; the remote process may still be running. **Do not replay blindly** — inspect the partial `stdout` in the error JSON (`replaySafe: false`).
- Foreground `timeout` must exceed the real runtime (default `commandTimeoutMs=30000`).
- Build/CI hosts: suggest `"pty": false` in the connection config.

## Production checklist

- [ ] Read server `notes` and `business` from list-servers
- [ ] Confirm connection name/alias (not guessing host IP)
- [ ] Start with read-only probes (`whoami`, `pwd`, `df -h`, `systemctl status`)
- [ ] Destructive commands (`rm`, `systemctl restart`, migrations) — confirm with operator
- [ ] Large output — use head/tail/grep or background session + read
- [ ] Close sessions when done (`session action=close`)

## Related

- Install/config wizard: [2native-ssh-mcp-helper](../2native-ssh-mcp-helper/SKILL.md)
- Security model: [SECURITY.md](../../SECURITY.md)
- Agent API: [docs/AGENT_GUIDE.md](../../docs/AGENT_GUIDE.md)

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

1. **Call `list-servers` first** — read metadata, aliases, notes; pick `connectionName` or plan a `sessionName`. A `via=<name>` label means the host is only reachable through that jump-host connection — no direct TCP path exists, and jump-host failures name it.
2. **Always pass `connectionName` explicitly** — taken from `list-servers`. Omitting it only works when a connection named `default` exists (marked `default=true` in list-servers); otherwise the call fails. Never guess a connection: running on the wrong host is worse than a failed call.
3. **Never use root** — use a dedicated low-privilege account.
4. **Prefer narrow commands** — scope with `head`, `tail`, `grep`, `wc`, paths; avoid blind `cat` on large files.
5. **Respect whitelist/blacklist** — if a command is rejected, do not bypass; ask the operator to widen policy.
6. **Secrets** — redaction is off by default (`"redactSecrets": true` enables it per connection); regardless, avoid printing credentials.
7. **Host key rejection** — if a connection fails with a host key mismatch (server reimaged / key rotated), do not bypass; report it. Fix is operator-side: remove the stale line from `known_hosts` (or set `"hostKeyCheck": "none"` for dynamic-IP hosts).
8. **Assume everything is recorded** — executed commands land in a per-connection append-only, hash-chained audit log (failures included); act accordingly on production hosts.

## Composing commands (escaping)

`cmdString` reaches the remote login shell as one string. Quoting only hurts when the text has to survive **more than one parser** — a nested `sh -c`, `su -c`, `docker exec … sh -c`, `ssh jumphost …`, or a payload that is itself shell code. Measured on Ubuntu 24.04 through this server:

| You write | Remote result |
|---|---|
| `bash -c "… awk '{print $1}' …"` | `$1` expanded to empty by the outer shell; **exit code 0, silently wrong output** |
| the same with `\$1`, or single quotes plus `'"'"'` | correct |
| `echo '<base64>' \| base64 -d \| bash` | correct, and unreadable to a human reviewer |

Habits that remove the class:

1. **Arguments with spaces, quotes, `$`, backslashes or `*` → use `args`, not `cmdString`.** `cmdString: "grep"` + `args: ["-rn", "foo bar", "/etc"]` sends each argument byte-for-byte with no shell parsing, and the audit record keeps the readable command line (`grep -rn 'foo bar' /etc`). Hand-quoting is where `$1` and `*` get eaten silently.
2. **Payload with newlines, quotes, backslashes or `$` → put it in a file and use `scriptFile`.** `execute-command(scriptFile="deploy.sh")` reads the file locally, stages it to the remote `/tmp` (mode 0700, atomic) and runs it — one call, no `file-transfer` round trip. Staging happens after the approval gate and whitelist/blacklist, so a refused call never leaves a remote file. The audit record holds the script text itself, so a reviewer sees what ran. `scriptCleanup: false` keeps the staged copy for debugging; `scriptInterpreter` (default `sh`) applies to scripts without a shebang.
3. **Keep `cmdString` a single simple line** for plain invocations; the `directory` parameter exists so you never hand-write `cd`.
4. **Do not hand-build a second shell layer.** For `su -c` / `docker exec sh -c` / `ssh jumphost`, put the inner command in a file (`scriptFile`), or use the connection's `commandTemplate` with `<quotedCommand>` — the server quotes it for you.
5. **Never paste a JSON body or a config file into `cmdString`.** `jq -n --rawfile v /path '{key:$v}'` builds valid JSON from a file with no shell quoting at all; `jq -c -Rs .` does the same from stdin. A hand-written `{"a":"b"}` with inner quotes fails to parse. `list-servers` reports `capabilities=base64,jq` once connected, so you know which of the two exists before you compose.
6. **Reaching for backslashes to protect `$` is the signal** — that is the point to switch to `args` or a file.

The server also lints `cmdString` before sending it (`lintMode`, default `warn`): findings arrive in the result as `[lint] …` lines and the command still runs; `lintMode: "strict"` refuses error-class findings with `COMMAND_LINT_FAILED`. A finding is a hint that the command will fail or — worse — succeed with the wrong output, so fix it rather than switching the lint off. `args` and `scriptFile` are not linted: they have no shell syntax left to get wrong.

Before sending, scan for: an odd number of quotes, a command ending in a bare `\`, a heredoc with no closing delimiter line, or a `$` inside a double-quoted `<shell> -c` string (the outer shell expands it first). If a base64-wrapped command returns **empty output with exit code 0**, the remote's base64 implementation did not accept the payload — some (`openssl base64 -d` on OpenSSL 3.0.13) produce empty output and exit 0 when the input lacks a trailing newline. Re-run without the wrapper instead of trusting the empty result.

## Output & tokens

### Built-in (automatic)

2native-ssh-mcp layers large output automatically:

- **Directories transfer recursively** via `file-transfer` (per-file atomic upload, `files`/`failed` in the result; single files get a sha256 check).
- **≥ 8KB** (`outputSpillThreshold`, `-1` disables): the full output is written to a local file (default `.ssh-mcp-out/`, `outputSpillDir` configurable, newest 32 kept). The result carries only a short notice, the absolute path, byte/line counts and a ~12-line preview. **Grep/Read that local file — never re-run the command or `cat` it remotely.**
- **4–8KB**: light compression — head + tail lines, middle replaced with `... [N lines omitted] ...`, duplicate/blank lines collapsed.
- Disable compression per connection: `"outputCompressLight": false`; tune threshold: `"outputCompressThreshold": 8192`.

ANSI escape sequences (colors, progress bars) are stripped from all output by default; disable per connection: `"stripAnsi": false`.

Successful execute-command results render as sectioned text by default (real newlines + `[exit code] N` + spill notice); set `"resultFormat": "json"` per connection for the structured CommandResult JSON (error paths always JSON).

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

**A finished background job keeps its session and remote log for 60 min** — always `close` when done (idempotent, safe to call twice). `read` supports `offset=0` to re-read from the start; the JSON carries `logPath` (remote log) and `exitCode` once the job finished. Sessions are in-memory: a stdio process restart loses them (the remote log survives at `logPath`); a resident HTTP daemon keeps them across conversations. Re-opening `background=true` on a finished session is rejected with the `logPath` — `close` first, or read the old log.

## Reading results

- **Non-zero exit is a normal result** — read `[exit code] N` from the text; do not treat it as a transport failure.
- **`SSH_CONNECTION_LOST` (retriable=false)**: the connection dropped mid-command; the remote process may still be running. **Do not replay blindly** — inspect the partial `stdout` in the error JSON (`replaySafe: false`).
- Foreground `timeout` must exceed the real runtime (default `commandTimeoutMs=30000`). On timeout the remote process group is killed by PID, so nothing is left running remotely.
- exec runs **without a PTY by default**; interactive commands need `"pty": true` (connection config) or the `pty` tool parameter.
- **"jump host [X] is unreachable" / "target authentication … rejected"** — two different failure layers on `via=` connections: the first is the jump host (retry — it may be a transient network issue), the second is the target's own credentials (do not retry; report).

## Operational traps (verified on real hosts)

- **Same-batch tool calls run concurrently — no ordering guarantee.** The server processes requests in parallel: a `list-servers` or `execute-command` issued in the same batch as a `file-transfer` may run *before* the transfer lands. Any sequence with dependencies (upload → verify → restart) must be **separate sequential tool calls**, waiting for each result.
- **`pgrep -f` matches the invoking shell's own argv.** `pgrep -f runme` sees the SSH exec shell carrying that literal string and reports the check itself as a hit. Use the bracket trick `pgrep -f "[r]unme"` or `pgrep -x sleep` for exact process names.
- **sudo over exec has no TTY.** Password prompts fail fast (~1s) with `sudo: a terminal is required...` — pipe the password via `sudo -S` (never a CLI argument), or set `"pty": true` for commands that insist on a terminal (e.g. some `systemctl` policy setups).
- **Overwriting a running binary is safe.** Single-file uploads use remote `<target>.part` + rename, so in-place "Text file busy" never happens: the running process keeps the old inode, the disk gets the new version. Verify with the result's `checksumStatus: "verified"` sha256.

## Production checklist

- [ ] Read server `notes` and `business` from list-servers
- [ ] Confirm connection name/alias (not guessing host IP)
- [ ] Start with read-only probes (`whoami`, `pwd`, `df -h`, `systemctl status`)
- [ ] Destructive commands (`rm`, `systemctl restart`, migrations) — confirm with operator
- [ ] Large output — use head/tail/grep or background session + read; if the result carries an output-file notice, Grep that local file
- [ ] Close sessions when done (`session action=close`)

## Related

- Install/config wizard: [2native-ssh-mcp-helper](../2native-ssh-mcp-helper/SKILL.md)
- Security model: [SECURITY.md](../../SECURITY.en.md)
- Agent API: [docs/AGENT_GUIDE.md](../../docs/AGENT_GUIDE.md)

# Security

2native-ssh-mcp gives AI agents controlled SSH access to remote hosts. Any command an agent runs is only as safe as the credentials, network path, and policy you configure. This document describes the threat model, defaults, and hardening recommendations.

## Threat model

**Primary risk:** An LLM with shell access can be influenced by untrusted content (logs, web pages, pasted configs). Prompt injection has no general fix. Assume every command may be attacker-influenced.

**What this server does:**

- Keeps SSH credentials in local config / env / agent — never in MCP tool arguments exposed to the model
- Validates commands against per-connection whitelist/blacklist regexes
- Restricts file-transfer paths via `allowedLocalPaths` / `allowedRemotePaths` (local scope per connection: `localPathMode` — `cwd` default, `list`, or `any`)
- Redacts common secret patterns (Bearer tokens, PEM blocks, `password=`/`token=` lines) from command output before returning to the client
- Checks config file permissions at startup (Unix: mode `0600` file / `0700` dir; Windows: ACL modify access)
- Verifies SSH host keys against `known_hosts` by default (`accept-new`), rejecting changed keys
- Sends SIGTERM/SIGKILL (exec mode) or Ctrl-C (shell mode) when commands time out or exceed output limits
- Exposes MCP tool hints (`readOnlyHint`, `destructiveHint`) so clients can gate dangerous tools

**What this server does not do:**

- Human-in-the-loop approval for destructive commands
- Command tiering (read-only vs destructive) beyond whitelist/blacklist
- Guarantee that redaction catches every secret format

## Host key verification

Host keys are verified against an OpenSSH `known_hosts` file (default `~/.ssh/known_hosts`, override per connection with `knownHostsFile`; the file and its parent directory are created automatically when missing). Three modes, per connection (`hostKeyCheck`):

| Mode | Behavior |
|---|---|
| `accept-new` (default) | Unknown hosts are recorded into `known_hosts` and accepted; a known host whose key **changed** is rejected (`SSH_HOST_KEY_MISMATCH`) |
| `strict` | Unknown hosts are rejected (`SSH_HOST_KEY_UNKNOWN`); changed keys are rejected |
| `none` | No verification (MITM risk) — only for dynamic-IP / frequently-rekeyed lab machines |

The default changed from "accept any key" to `accept-new` deliberately: first contact is trusted (TOFU), afterwards the key is pinned. If a server legitimately rekeys, remove its line from `known_hosts` (or set `hostKeyCheck: none` for that connection).

## Recommendations

1. **Never point at root.** Use a dedicated low-privilege account.
2. **Use whitelist regexes in production.** Start read-only (`^ls `, `^cat `, `^df `) and expand deliberately.
3. **Store credentials in env refs:** `"password": "${SSH_MCP_PASSWORD}"` — not in MCP client JSON args.
4. **Lock down the config file:** `chmod 600 config.json && chmod 700 $(dirname config.json)` on Unix.
5. **Set `allowedRemotePaths` and `allowedLocalPaths`** to the smallest scope needed; use `"localPathMode": "list"` to exclude the process working directory from the local scope. `"localPathMode": "any"` disables the local restriction — only for trusted single-user machines.
6. **Do not expose the HTTP daemon** (`start`) beyond `127.0.0.1` without a token: a non-loopback listen address requires `--http-token` / `SSH_MCP_HTTP_TOKEN` / `$global.httpToken` (fail closed), and every `/mcp` request must then carry `Authorization: Bearer <token>`.
7. **Prefer exec mode + named sessions** for stateful work; reserve `transportMode: shell` for bastions that block exec.

## Tool annotations

| Tool | readOnly | destructive |
|---|---|---|
| `list-servers` | yes | — |
| `execute-command` | no | yes (hint) |
| `session` | no | no (read action is read-only in practice) |
| `file-transfer` | no | yes (hint; upload overwrites) |

Clients that honor MCP annotations may block or confirm destructive tools automatically.

## Output redaction

Before returning command output, the server masks:

- `Bearer <token>` headers and similar JWT fragments
- PEM blocks (`-----BEGIN … PRIVATE KEY-----`)
- Lines matching `password=` / `token=` / `secret=` / `api_key=` (case-insensitive)

Redaction is best-effort. Do not rely on it as the only control for highly sensitive environments.

## Config file permissions

On startup, when `--config-file` is set, the server verifies:

- **Unix:** config file mode has no group/other bits; parent directory mode has no group/other bits
- **Windows:** `icacls` is consulted; refuse when `Everyone` / `Authenticated Users` can modify the file

Pass `--allow-insecure-config-perms` only for local development; the same override can be declared inside the config file itself (object format) via `"$global": {"allowInsecureConfigPerms": true}`. The check runs after the file is parsed, so a config that disables it is loaded as a deliberate user choice.

## Reporting vulnerabilities

Open a GitHub issue or contact the maintainer privately. Do not disclose credentials in bug reports.

## Related reading

- [Simon Willison — the lethal trifecta](https://simonwillison.net/2025/Jun/16/the-lethal-trifecta/)
- [docs/HUMAN_GUIDE.md](docs/HUMAN_GUIDE.md) — secure setup walkthrough
- [docs/AGENT_GUIDE.md](docs/AGENT_GUIDE.md) — tool reference for agents

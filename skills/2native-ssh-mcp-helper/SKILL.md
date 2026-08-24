---
name: 2native-ssh-mcp-helper
description: >-
  Use when the user wants to install, configure, or add a 2native-ssh-mcp MCP
  connection (e.g. "help me set up 2native-ssh-mcp", "configure SSH MCP in Cursor",
  "add a remote host to MCP", "write mcp.json for 2native-ssh-mcp"). Guides
  through binary discovery, config.json creation, credential handling, and
  writing the mcpServers JSON snippet for Cursor / Claude Code / other MCP clients.
---

# 2native-ssh-mcp-helper

Interactive wizard for **2native-ssh-mcp** (Go SSH MCP server). Produces a locked `config.json` plus an MCP client snippet. **Never paste passwords into MCP client args** — use env refs in the config file.

## When to use

- Install or configure 2native-ssh-mcp for Cursor / Claude Code / Cline / Continue
- Add another SSH host to an existing setup
- User asks how to write `mcp.json` for this project

## When not to use

- User wants to change server source code → edit the repo directly
- User only wants to run a remote command → use existing MCP tools after setup

## Workflow

1. **Locate binary**
   - Prefer a release binary: `2native-ssh-mcp` / `2native-ssh-mcp.exe` on PATH or from [GitHub Releases](https://github.com/daidaiJ/2native-ssh-mcp/releases)
   - Or build: `go build -o 2native-ssh-mcp .` in the repo root
   - Confirm: `2native-ssh-mcp --version`

2. **Choose MCP client** (AskQuestion if available)

   | Client | Config file |
   |---|---|
   | Cursor | `~/.cursor/mcp.json` |
   | Claude Code (global) | `~/.claude.json` → `mcpServers` |
   | Claude Code (project) | `<project>/.mcp.json` |
   | Other | Ask user for path |

3. **Single vs multi host** (AskQuestion)
   - **Single**: one entry in `config.json` keyed `"default"` or a custom name
   - **Multi**: object with multiple keys (`dev`, `prod`, …) + optional `aliases`

4. **Auth** (AskQuestion)
   - `password` → config `"password": "${SSH_MCP_PASSWORD}"`, set env in MCP client
   - `privateKey` → `"privateKey": "~/.ssh/id_ed25519"`, optional `"passphrase": "${SSH_MCP_PASSPHRASE}"`
   - `ssh-config alias` → `"host"` in config matches `~/.ssh/config` Host; CLI `--host alias` only for single-host mode
   - `agent` → `"agent": "/path/to/socket"` or `"pageant"` on Windows
   - `2FA` → add `"tryKeyboard": true`, OTP via `SSH_MCP_2FA_CODE` env

5. **Security prompts** (AskQuestion yes/no each)
   - Command whitelist regexes (recommended for prod)
   - Command blacklist
   - `allowedLocalPaths` / `allowedRemotePaths`
   - Server metadata: `description`, `business`, `aliases`, `notes` for list-servers
   - Bastion: `"transportMode": "shell"` (no SFTP on shell mode)

6. **Write config.json**
   - Store outside the repo if it contains secrets; e.g. `~/.config/2native-ssh-mcp/config.json`
   - Unix: `chmod 600 config.json && chmod 700 $(dirname config.json)`
   - Windows: restrict ACL so other users cannot modify the file
   - Example:

```json
{
  "dev": {
    "host": "10.0.0.1",
    "port": 22,
    "username": "deploy",
    "password": "${SSH_MCP_PASSWORD}",
    "description": "Dev jump host",
    "aliases": ["dev"],
    "commandWhitelist": ["^ls ", "^cat ", "^df "],
    "allowedRemotePaths": ["/tmp", "/home/deploy"]
  }
}
```

7. **Generate MCP client snippet**

   **stdio (default — MCP client spawns the process):**

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "command": "C:/path/to/2native-ssh-mcp.exe",
      "args": ["--config-file", "C:/Users/you/.config/2native-ssh-mcp/config.json"],
      "env": {
        "SSH_MCP_PASSWORD": "your-password-here"
      }
    }
  }
}
```

   **HTTP daemon (optional — shared long-running server):**

```json
{
  "mcpServers": {
    "2native-ssh-mcp": {
      "url": "http://127.0.0.1:8338/mcp"
    }
  }
}
```

   Start daemon once: `2native-ssh-mcp start --config-file /path/config.json --http-addr 127.0.0.1:8338`

   Rules:
   - Use **absolute paths** for binary and config on Windows
   - Each CLI flag and value are **separate** `args` elements
   - Do not put passwords in `args`; use config env refs + `env` block

8. **Merge & write**
   - Read existing client JSON, merge under `mcpServers`
   - If key exists → AskQuestion: overwrite / rename / cancel
   - Show final snippet before write

9. **Verify**
   - Restart MCP client
   - Call `list-servers`, then `execute-command` with `cmdString: "whoami"`

## Quick reference

| Scenario | Config / CLI |
|---|---|
| Multi-host | `--config-file` with object of named connections |
| Alias for connectionName | `"aliases": ["prod-web"]` in config |
| Long-running logs | `session action=open` + `background=true`, poll `session action=read` |
| Stateful shell (exec mode) | `session action=open` → `execute-command` with `sessionName` |
| File upload/download | `file-transfer` (exec mode only) |
| Skip config perm check (dev) | `--allow-insecure-config-perms` |

## Pitfalls

- Do not use `--password` in MCP `args` (visible in process list)
- `transportMode: shell` disables SFTP file-transfer
- Config file must be mode 0600 on Unix or server refuses to start
- Named sessions require `transportMode: exec` (default)

## More docs

- Human guide: [docs/HUMAN_GUIDE.md](../../docs/HUMAN_GUIDE.md)
- Agent guide: [docs/AGENT_GUIDE.md](../../docs/AGENT_GUIDE.md)
- Security: [SECURITY.md](../../SECURITY.md)

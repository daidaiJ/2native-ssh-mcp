> [中文](DEVELOPMENT.md)

# Development & Release

## Project Structure

```
2native-ssh-mcp/
├── main.go                        # Entry point: subcommand dispatch (start/stop/status/install…) + stdio/HTTP dual transport
├── internal/
│   ├── config/                    # SSH configuration types, defaults, CLI parsing, ${ENV_VAR} interpolation
│   ├── approval/                  # Destructive-command classifier (built-in rules + user extensions/exemptions)
│   ├── sshconfig/                 # ~/.ssh/config parsing (Include, wildcards, first-match-wins)
│   ├── logger/                    # stderr logging (doesn't pollute stdio protocol)
│   ├── manager/                   # Connection management core
│   │   ├── manager.go             #   Lazy connection, idle keep-alive, path validation, command logging
│   │   ├── dial.go                #   Authentication (password/private key/agent/Pageant/2FA), proxy (SOCKS5/HTTP/HTTPS)
│   │   ├── hostkey.go             #   Host key verification (accept-new/strict/none, known_hosts)
│   │   ├── exec.go                #   exec mode command execution (timeout/output limits/exit code)
│   │   ├── shell.go               #   shell mode (marker protocol, ANSI cleanup, serial queue)
│   │   ├── background.go          #   Background tasks (detached exec, log polling, stopping)
│   │   ├── session.go             #   Named sessions (CWD preservation, reattachment after disconnect, idle TTL)
│   │   ├── heartbeat.go           #   Application-level heartbeat (keepalive semantics, in-flight protection)
│   │   ├── result.go              #   Structured result (exit code, status, ANSI stripping)
│   │   ├── compress.go            #   Large output head/tail compression
│   │   ├── redact.go              #   Output redaction
│   │   ├── sftp.go                #   File transfer (concurrent copying, progress, deduplication, resumable transfer)
│   │   ├── status.go              #   Remote system status collection (hostname/OS/memory/disk, etc.)
│   │   └── commandlog.go          #   Command log file (JSON lines, retains last N commands)
│   ├── daemon/                    # HTTP daemon: PID file, refcount+guest lease, admin endpoints, Windows auto-start
│   └── tools/                     # MCP tools: list-servers / execute-command / session / file-transfer
├── SECURITY.md                    # Threat model and security recommendations
├── docs/                          # Guides and topic docs (Chinese by default; .en.md is the English version)
├── skills/                        # Agent Skills (installation / remote execution strategies)
├── .github/workflows/ci.yml       # push/PR → ubuntu-latest go test -race ./...
├── .github/workflows/release.yml  # tag push → 6-platform build + Release; "-registry" tag → publish to MCP Registry
├── LICENSE                        # ISC (includes upstream copyright notice)
└── README.md                      # Chinese (default); English version: README.en.md
```

## Build & Test

```bash
go build ./...
go test -race ./...
```

## Automated Release

Pushing an **annotated tag with a message** triggers GitHub Actions (`.github/workflows/release.yml`):

```bash
git tag -a v1.0.1 -m "Fixed xxx issue"
git push origin v1.0.1
```

**Important notes**:

- **Release notes = tag message**. The workflow reads the annotated tag object message via the GitHub API (avoids using the local git on runner — when runners checkout, they might only have a lightweight tag reference and `%(contents)` would fall back to the commit message, turning release notes into the last commit message)
- Builds **6 platforms**: Windows / Linux / macOS × amd64 / arm64 (`CGO_ENABLED=0`, version injected as tag name), outputs named `2native-ssh-mcp-<os>-<arch>[.exe]`
- Each binary includes an independent `.sha256` checksum file
- Each binary is also packaged as an **`.mcpb` bundle** (MCP Bundle: zip + `manifest.json`, `server.type: "binary"`) with its own `.sha256`
- One CycloneDX SBOM (`2native-ssh-mcp.cdx.json`) is attached; SLSA provenance and SBOM attestations are published to the repo [Attestations](https://github.com/daidaiJ/2native-ssh-mcp/attestations) tab (not extra `.intoto` files on the release). Verify a downloaded binary with `gh attestation verify --repo daidaiJ/2native-ssh-mcp <file>`
- Use `git tag -a -m` to create an **annotated tag with a message**; lightweight tags (`git tag v1.0.1`) have no message, so release notes will fall back to the tag name

### Publishing to the official MCP Registry

Publishing to [registry.modelcontextprotocol.io](https://registry.modelcontextprotocol.io) is **explicit and separate**: a plain tag only builds the release; a tag with the `-registry` suffix additionally publishes the matching stable release to the official MCP Registry (via OIDC, no secrets needed):

```bash
git tag -a v1.0.1 -m "Publish v1.0.1 to MCP Registry"
git push origin v1.0.1-registry
```

The `v1.0.1-registry` tag triggers the `publish-registry` job, which downloads the `.mcpb` bundles from the existing `v1.0.1` release, generates `server.json` (6 platform packages), and publishes as `io.github.daidaiJ/2native-ssh-mcp`. Verify with:

```bash
curl "https://registry.modelcontextprotocol.io/v0.1/servers?search=2native-ssh-mcp"
```

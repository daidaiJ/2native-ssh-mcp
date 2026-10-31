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
│   │   ├── dial.go                #   Authentication (password/private key/agent/Pageant/2FA), proxy (SOCKS5/HTTP/HTTPS), via jump tunnel
│   │   ├── hostkey.go             #   Host key verification (accept-new/strict/none, known_hosts)
│   │   ├── exec.go                #   exec mode command execution (timeout/output limits/exit code)
│   │   ├── shell.go               #   shell mode (marker protocol, ANSI cleanup, serial queue)
│   │   ├── background.go          #   Background tasks (detached exec, log polling, stopping)
│   │   ├── session.go             #   Named sessions (CWD preservation, reattachment after disconnect, idle TTL)
│   │   ├── heartbeat.go           #   Application-level heartbeat (keepalive semantics, in-flight protection)
│   │   ├── kbdint.go              #   Keyboard-interactive auth (2FA challenge via MCP elicitation, env fallback)
│   │   ├── result.go              #   Structured result (exit code, status, ANSI stripping)
│   │   ├── compress.go            #   Large output head/tail compression
│   │   ├── redact.go              #   Output redaction
│   │   ├── sftp.go                #   File transfer (concurrent copying, progress, deduplication, resumable transfer)
│   │   ├── status.go              #   Remote system status collection (hostname/OS/memory/disk, etc.)
│   │   └── commandlog.go          #   Audit log (append-only JSONL, SHA-256 hash chain, batched flush, rotation)
│   ├── daemon/                    # HTTP daemon: PID file, refcount+guest lease, admin endpoints, Windows auto-start
│   └── tools/                     # MCP tools: list-servers / execute-command / session / file-transfer
├── SECURITY.en.md                 # Threat model, audit flush policy, tiered hardening (Chinese: SECURITY.md)
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

### Integration tests (real environment required)

```bash
# ProxyJump dual-sshd container fixture: bastion publishes a host port,
# internal is reachable only through the compose network. Skips cleanly
# without Docker, so the default CI is unaffected.
go test -tags integration ./internal/manager/ -run ProxyJump -v

# The remaining integration tests use a reachable connection from the
# repo-root config.json (e.g. a WSL sshd)
go test -tags integration ./internal/manager/ -run ExecKill -v

# When config.json is not at the repo root (e.g. you reuse the MCP client's
# copy), point the tests at it
SSH_MCP_TEST_CONFIG=/path/to/config.json go test -tags integration ./internal/manager/ -v
```

`escape_integration_test.go` covers the escaping work: argv delivered byte-for-byte (against the naive join's splitting/globbing), `scriptFile` staging + run + cleanup, the background exit code surviving the cleanup subshell, lint warn/strict behavior, the readable equivalent in the audit record, and the capability probe (including `autoProbe: false`).

### Benchmarks

```bash
# Output pipeline (stripANSI / redact / compress, pure local, no SSH needed)
go test ./internal/manager/ -bench BenchmarkOutput -benchmem

# Audit log path (canonical JSON + hashing, append write-through vs batched
# buffering, legacy rewrite-model comparison; pure local, no SSH needed)
go test ./internal/manager/ -bench 'BenchmarkCommandLog|BenchmarkAuditCanonicalHash' -benchmem

# SFTP transfer throughput (integration tag; needs a reachable connection in
# config.json, e.g. a WSL sshd)
go test -tags integration ./internal/manager/ -bench BenchmarkTransfer -benchmem
```

**Measured data** (2026-09-11, Ryzen 5 4600H, WSL2 Ubuntu-22.04 localhost sshd; 1 MiB text for output benchmarks, 16 MiB file for transfers, median of 5 fixed rounds):

| Benchmark | Result |
|---|---|
| stripANSI, plain 1 MiB | ~15 µs (~70000 MB/s, 0 allocs) |
| stripANSI, ANSI-dense 1 MiB | ~1.9 ms (~550 MB/s, 1.9 MB allocs) |
| RedactSensitiveOutput, with secrets 1 MiB | ~4.7 ms (~220 MB/s) |
| SFTP upload 16 MiB (32KB chunks) | ~52 MB/s |
| SFTP download 16 MiB (32KB chunks) | ~95 MB/s |

Notes:

- On localhost, SFTP throughput is dominated by pkg/sftp protocol behavior; `sftpConcurrency`/`sftpChunkSize` pay off on high-latency links
- `UseConcurrentReads` showed no benefit on low-latency links (downloads -7%) and is not enabled
- `sftpChunkSize` caps at 32KB (the SSH session channel single-data-message limit, hard-rejected by pkg/sftp beyond it); larger configured values clamp

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

## Design decision: command results render as text by default (2026-09-07)

- **Background**: e4888a6 (2026-09-04, just before v1.5.0) switched the execute-command success path from `result.Text()` sectioned text to `commandResultJSON`. MCP clients render text content verbatim, and the `stdout` value inside that JSON is a single escaped string (newlines are literal `\n`), so long output gets soft-wrapped into an unreadable block in terminals; the sectioned text from v1.4.1 and earlier is the readable form for both humans and agents.
- **Decision**: the success path defaults back to `result.Text()`; a per-connection `resultFormat` config was added (`text` default / `json` opt-in, see `SSHConfig.GetResultFormat`). Error paths stay JSON (agents branch on `code`/`message`/`retriable`/`partial`/`replaySafe`); background/session-open info JSON and list-servers are unchanged.
- **Compatibility**: this is a default behavior change — consumers that depend on the JSON shape can set `"resultFormat": "json"` per connection to restore it.

## Design decision: stability and context convergence (2026-10-31)

Surveying comparable SSH MCP servers (Motty's built-in MCP server, TermBridge's execution-semantics ADRs, n0madic/ssh-mcp, remotessh-mcp) surfaced five gaps, each fixed here:

- **A panicking tool handler left the request hanging**: mcp-go's recovery middleware is opt-in and was not enabled — a panic inside a handler fell through to the transport, so the client just waited for its timeout. `newMCPServer` now registers `server.WithRecovery()` with `tools.LogToolCalls` outside it (tool name, duration, error), so a panic becomes that call's error response and is logged.
- **The HTTP transport had no timeouts or size bounds**: `http.Server` now sets `ReadHeaderTimeout` 10s, `IdleTimeout` 2m, `MaxHeaderBytes` 16 KiB, and `/mcp` request bodies are capped at 4 MiB (`limitBody`). DNS-rebinding protection on a loopback listener is built into mcp-go (a non-localhost Host is refused with 403).
- **A background job's remote log was unbounded**: new per-connection `backgroundLogMaxBytes` (default 8 MiB, `-1` keeps everything, settable once via `$global`). The starter pipes the job's output through a `capture()` loop: past the cap the oldest bytes are trimmed, the discarded count goes to a `.drop` file, and the first 64 KiB are copied to `.head`. Trimming happens inside the only writer of the log, so no output can be lost to a race. `session action=read` now treats `offset`/`totalBytes` as **absolute** bytes (counted from the job's first byte, trimmed bytes included) and, when the requested offset falls in the trimmed region, answers with the kept beginning, an omission marker and the newest bytes; the header also reports `droppedBytes`/`logMaxBytes`.
- **Interrupted calls had no actionable recovery semantics**: `CommandResult` gains `executionState` (`started` = the command did run, outcome unknown; `unknown` = the connection dropped, so it may never have started, may be running, or may have finished) and a `guidance` string naming the next step, present on interrupted results only. The session tool, `list-servers` and `skills/2native-ssh-mcp-agent` now state the same contract: a timeout is not a command that did not run — verify the remote state before retrying.
- **The same payload was sent twice**: `session action=read`/`list` and `list-servers` used to append `Raw JSON`/`Servers JSON` after the text. Both now follow `resultFormat` (text by default), settable for every connection with `$global.resultFormat`. The spill preview became a first-8/last-4-line preview (failures print last), and the initialize reply gained an `instructions` note carrying the cross-tool contract (connection choice, spill file, interruption semantics, background sessions).

**Compatibility**: session/list-servers responses no longer include the `Raw JSON`/`Servers JSON` sections by default (set `"resultFormat": "json"` for the structured shape); `offset` now means absolute bytes (old offsets stay valid, they just never rewind); the background log is capped at 8 MiB by default — set `"backgroundLogMaxBytes": -1` on connections that need the whole log.

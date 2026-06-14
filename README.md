# BalenaMCP

[![CI](https://github.com/schubydoo/balenamcp/actions/workflows/ci.yml/badge.svg?branch=main)](https://github.com/schubydoo/balenamcp/actions/workflows/ci.yml)
[![Security](https://github.com/schubydoo/balenamcp/actions/workflows/security.yml/badge.svg?branch=main)](https://github.com/schubydoo/balenamcp/actions/workflows/security.yml)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/schubydoo/balenamcp/badge)](https://scorecard.dev/viewer/?uri=github.com/schubydoo/balenamcp)
[![Go Report Card](https://goreportcard.com/badge/github.com/schubydoo/balenamcp)](https://goreportcard.com/report/github.com/schubydoo/balenamcp)
[![Go Reference](https://pkg.go.dev/badge/github.com/schubydoo/balenamcp.svg)](https://pkg.go.dev/github.com/schubydoo/balenamcp)
[![Latest Release](https://img.shields.io/github/v/release/schubydoo/balenamcp?logo=github)](https://github.com/schubydoo/balenamcp/releases/latest)
[![Renovate](https://img.shields.io/badge/renovate-enabled-brightgreen?logo=renovatebot)](https://docs.renovatebot.com)
[![Conventional Commits](https://img.shields.io/badge/Conventional%20Commits-1.0.0-yellow.svg)](https://www.conventionalcommits.org/en/v1.0.0/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

A [Model Context Protocol](https://modelcontextprotocol.io/) server that wraps
the [balena CLI](https://github.com/balena-io/balena-cli) so MCP-aware agents
(Claude Code, Claude Desktop, Cursor, Continue, Cline, Goose, etc.) can list
fleets, inspect devices, manage tags and env vars, pin releases, reboot
devices, and more.

It exposes all three MCP primitives: **tools** (direct balena actions),
**prompts** (guided multi-step workflows like canary rollouts and device
diagnosis), and **resources** (read-only fleet/device/release state you attach
as context). See the [Tools](#tools), [Prompts](#prompts), and
[Resources](#resources) sections below.

This is a personal fork of [klutchell/balenamcp](https://github.com/klutchell/balenamcp)
brought up to date with the current balena CLI and the current `mark3labs/mcp-go`.

## Prerequisites

- [`balena` CLI](https://github.com/balena-io/balena-cli) on `PATH`
- An MCP-aware agent or IDE (Claude Code, Claude Desktop, Cursor, …)
- Go 1.23+ (only if you're building from source — pre-built binaries don't require it)

## Install

Three options, pick whichever fits.

### 1. Pre-built binary from GitHub Releases (recommended)

Grab the archive for your OS/arch from
[Releases](https://github.com/schubydoo/balenamcp/releases/latest):

| OS | Arch | Archive |
|---|---|---|
| Linux | x86_64 | `balenamcp_<version>_Linux_x86_64.tar.gz` |
| Linux | arm64 | `balenamcp_<version>_Linux_arm64.tar.gz` |
| macOS | x86_64 (Intel) | `balenamcp_<version>_Darwin_x86_64.tar.gz` |
| macOS | arm64 (Apple Silicon) | `balenamcp_<version>_Darwin_arm64.tar.gz` |
| Windows | x86_64 | `balenamcp_<version>_Windows_x86_64.zip` |

Each release also publishes:

- **`checksums.txt`** — SHA-256 of every artifact
- **`checksums.txt.sigstore.json`** — cosign signature over `checksums.txt`
- **`<archive>.sbom.cdx.json`** — CycloneDX Software Bill of Materials per archive
- **`<archive>.sbom.cdx.json.sigstore.json`** — cosign signature over each SBOM

#### Verifying a download (recommended)

The release is signed with [cosign](https://docs.sigstore.dev/cosign/installation/)
using Sigstore keyless signing — no public-key juggling needed. Install
cosign, then:

```sh
# 1. Verify the signature on checksums.txt
cosign verify-blob \
  --bundle checksums.txt.sigstore.json \
  --certificate-identity-regexp \
    'https://github.com/schubydoo/balenamcp/.github/workflows/release.yml@.*' \
  --certificate-oidc-issuer https://token.actions.githubusercontent.com \
  checksums.txt

# 2. Verify your archive against the (now-trusted) checksums file
sha256sum --check checksums.txt --ignore-missing
```

If both commands succeed, you've cryptographically verified that the
archive was built by **this** repo's release workflow (not a typo-squatter,
not tampered with in transit).

#### Extract and install

```sh
tar -xzf balenamcp_<version>_Linux_x86_64.tar.gz
sudo install balenamcp /usr/local/bin/
balenamcp --help   # or just `balenamcp` to start serving over stdio
```

### 2. `go install` (Go developers)

```sh
go install github.com/schubydoo/balenamcp@latest
```

Resolves to the highest semver tag, builds locally, and drops the binary
in `$GOBIN` (default `$GOPATH/bin` or `~/go/bin`). For docs and package
info: <https://pkg.go.dev/github.com/schubydoo/balenamcp>.

`@latest` follows the most recent release; pin a specific version with
`go install github.com/schubydoo/balenamcp@v0.1.0` if you'd rather not
auto-update.

### 3. Build from source

```sh
git clone https://github.com/schubydoo/balenamcp.git
cd balenamcp
go mod download
go build -o bin/balenamcp
```

Cross-compile examples:

```sh
GOOS=linux   GOARCH=amd64 go build -o bin/balenamcp-linux-amd64
GOOS=windows GOARCH=amd64 go build -o bin/balenamcp-windows-amd64.exe
GOOS=darwin  GOARCH=arm64 go build -o bin/balenamcp-darwin-arm64
```

For a versioned source build (matches release builds):

```sh
go build -ldflags='-s -w -X github.com/schubydoo/balenamcp/server.Version=v0.1.0' \
  -trimpath -o bin/balenamcp .
```

Without the `-X` ldflag, the server reports `dev` as its version.

A `-dry-run` flag is available — the server prints the balena command it
*would* run instead of executing it. Useful for testing/debugging.

### Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `BALENAMCP_EXEC_TIMEOUT` | `60` (seconds) | Wall-clock cap on any single balena CLI subprocess. Prevents `device-logs --tail` and similar long-running commands from blocking the MCP transport indefinitely. Set to a higher integer for slow networks; the server logs a warning and falls back to default if the value is non-positive or non-numeric. |
| `BALENAMCP_REQUIRE_CONFIRM` | unset (off) | When set to `1`/`true`, every destructive tool refuses to run unless the call carries `confirm: true` in its arguments. A belt-and-suspenders safety net for MCP clients that ignore the `destructiveHint` annotation. Off by default — Claude Desktop and other compliant clients already prompt before invoking destructive tools, so the gate is redundant there. |

## Authenticate

Before using any tool that touches balenaCloud, log in once on the host:

```sh
balena login
```

The MCP server inherits that session by shelling out to the same `balena`
binary.

## MCP client setup

The server speaks stdio JSON-RPC — any MCP-compliant client wires it up the
same way: point at the binary, optionally pass args, restart the client.
Specifics below.

### Claude Code

CLI (recommended):

```sh
claude mcp add balena /absolute/path/to/balenamcp
```

That edits `~/.claude.json` for you. Use `claude mcp add balena --scope project ...`
to scope it to the current repo (`.mcp.json` in repo root) instead.

Or edit `~/.claude.json` by hand:

```json
{
  "mcpServers": {
    "balena": {
      "command": "/absolute/path/to/balenamcp",
      "args": []
    }
  }
}
```

The tools appear in the next Claude Code session. Run `/mcp` to confirm.

### Claude Desktop

Add to `claude_desktop_config.json`:

| OS | Path |
|---|---|
| macOS | `~/Library/Application Support/Claude/claude_desktop_config.json` |
| Windows | `%APPDATA%\Claude\claude_desktop_config.json` |
| Linux* | `~/.config/Claude/claude_desktop_config.json` |

\* Claude Desktop on Linux is unofficial; the path mirrors the XDG location
that community builds use.

```json
{
  "mcpServers": {
    "balena": {
      "command": "/absolute/path/to/balenamcp",
      "args": []
    }
  }
}
```

Restart Claude Desktop. The tools appear under the `balena` server.

### Other MCP clients

Setup is similar — point the client at the binary as a stdio MCP server.
Consult each tool's docs for the exact config file / command:

- **[Cursor](https://docs.cursor.com/context/model-context-protocol)** — Settings → MCP → Add server, or `~/.cursor/mcp.json`
- **[Continue](https://docs.continue.dev/customize/deep-dives/mcp)** — `mcpServers` block in `~/.continue/config.yaml`
- **[Cline](https://github.com/cline/cline)** — VS Code settings under `cline.mcpServers`
- **[Goose](https://block.github.io/goose/docs/getting-started/using-extensions)** — `goose configure` (interactive) or `~/.config/goose/config.yaml`
- **[LibreChat](https://www.librechat.ai/docs/configuration/librechat_yaml/object_structure/mcp_servers)** — `mcpServers:` in `librechat.yaml`

If your client supports MCP via stdio, the binary will work — there's
nothing balenamcp-specific about the wiring.

## Tools

> ### ⚠️ Destructive tools — read this first
>
> **13 of the 30 tools change state on real devices or in balenaCloud.** A
> reboot or `device-purge` can't be undone from inside the model. Every
> destructive tool is flagged with `destructiveHint: true` in its MCP
> annotation, and Claude Desktop (and other compliant MCP clients) prompts
> you for confirmation before running them.
>
> | Tool | Effect | Reversible? |
> |---|---|---|
> | `device-reboot` | Remote reboot | yes (device comes back up) |
> | `device-restart` | Restart containers (no reboot) | yes |
> | `device-shutdown` | Remote shutdown — **manual power cycle to recover** | requires physical access |
> | `device-purge` | **Wipe `/data` on the device** | **no — data is gone** |
> | `device-ssh` | Run an arbitrary command on the device (host OS or a service container) | **depends on the command run** |
> | `device-pin` | Pin a device to a specific release | yes (`device-track-fleet` or re-pin) |
> | `device-track-fleet` | Drop a device's pin and resume tracking the fleet's release | yes (`device-pin` again) |
> | `fleet-pin` | Pin a fleet to a specific release | yes |
> | `release-finalize` | Promote a draft release to final | **no — finals can't be un-finalized** |
> | `tag-set` | Create or update a tag | yes (`tag-rm`) |
> | `tag-rm` | Remove a tag | yes (`tag-set`) |
> | `env-set` | Set/update an env or config variable | yes (`env-rm` or `env-set` again) |
> | `env-rm` | Delete an env or config variable (needs `yes: true` to bypass the CLI's confirm prompt) | yes (`env-set` again) |
>
> **Belt-and-suspenders gate:** set `BALENAMCP_REQUIRE_CONFIRM=1` and every
> destructive tool will refuse to run unless the call carries
> `confirm: true` in its arguments. Useful with MCP clients that don't
> honor `destructiveHint`.

### Read-only

The remaining 17 tools are read-only — they shell out to balena with no
state change. Safe to call without confirmation.

| Tool | Purpose |
|---|---|
| `version` | balena CLI version |
| `whoami` | Current user / org / device session info |
| `fleet-list` | List all accessible fleets |
| `fleet-info` | Detailed info for one fleet |
| `device-list` | List devices, optionally filtered by fleet |
| `device-info` | Detailed info for one device |
| `device-logs` | Recent historical logs from a device, optionally per-service. Streaming (`--tail`) is not supported over MCP — run the balena CLI directly for continuous monitoring. **Note:** this tool's identifier arg is named `device` (accepts UUID, IP, or `.local` address) while every other `device-*` tool uses `uuid`. Kept this way to match the broader argument shape balena's CLI accepts here. |
| `device-type-list` | Supported balena device types |
| `release-list` | Releases of a fleet |
| `release-info` | Metadata or composition of one release |
| `release-asset-list` | Binary assets attached to a release |
| `tag-list` | Tags on a fleet **or** device **or** release |
| `env-list` | Env/config variables on a fleet **or** device, optionally per-service |
| `os-versions` | Available balenaOS versions for a device type |
| `organization-list` | Organizations the user belongs to |
| `ssh-key-list` | SSH keys registered in balenaCloud |
| `api-key-list` | balenaCloud API keys |

### Argument constraints

`tag-list` / `tag-set` / `tag-rm` require **exactly one** of
`fleet` / `device` / `release`. `env-list` / `env-set` require **exactly one**
of `fleet` / `device`.

## Prompts

Beyond tools, the server exposes **MCP prompts** — guided, multi-step
workflows you invoke from your client (in Claude Desktop they appear in the
prompt/slash picker). A prompt runs no balena commands itself; it returns a
runbook that tells the model which tools to call and in what order, encoding
the sequencing and safety ordering an experienced operator would follow.
Destructive steps still go through the same `destructiveHint` /
`BALENAMCP_REQUIRE_CONFIRM` guards as any other tool call.

| Prompt | Arguments | What it walks the model through |
|---|---|---|
| `diagnose-device` | `uuid` | Pull status, logs, env, tags, and pin state for one device, then summarize a health verdict and likely root cause. Read-only. |
| `fleet-health-report` | `fleet` | Tally device status across a fleet, compare each device against the fleet's target release, flag what needs attention. Read-only. |
| `safe-release-rollout` | `fleet`, `release` | Canary-first rollout: record the rollback target, pin **one** device, verify it, then roll out fleet-wide — pausing for approval before each state change. |
| `rollback-device` | `uuid` | Identify a previously known-good release and roll a single device back to it, after confirming the target. |
| `audit-config` | `fleet` | Compare device-level env/config variables against fleet defaults; surface drift, secret-shaped values (never echoed), and orphaned overrides. Read-only. |
| `compare-releases` | `release_a`, `release_b` | Diff two releases: per-service image-size deltas (how much bigger/smaller), composition changes, and asset differences. Read-only. |
| `replicate-config` | `source`, `target` | Copy env/config variables from one fleet/device to another, with a masked plan and an approval step before any write. |
| `bulk-tag` | `fleet`, `key`, `value` (optional) | Apply a tag to many devices in a fleet at once, with an approval step before any write. |

## Resources

The server also exposes read-only balena state as **MCP resources** under the
`balena://` URI scheme. Where a tool is a single CLI call the model invokes, a
resource is context you *attach* to the conversation — and each one **composes
several CLI calls into one JSON document**, so a single read gives the model a
coherent picture instead of forcing several separate tool calls. Composition
degrades gracefully: if a sub-call fails (e.g. logs for an offline device) the
document still returns the sections that succeeded and records the rest under
an `"errors"` object with `"partial": true`.

| Resource URI | Type | Aggregates |
|---|---|---|
| `balena://account` | static | `whoami` + organizations |
| `balena://account/keys` | static | registered SSH public keys + API key names (no secrets) |
| `balena://gotchas` | static | known balena CLI foot-guns + correct invocations (SSH one-shot, log streaming limits) |
| `balena://fleets` | static | all accessible fleets |
| `balena://device-types` | static | supported device types |
| `balena://device/{uuid}` | template | device status + recent logs + env/config + tags |
| `balena://fleet/{org}/{fleet}` | template | fleet metadata + devices + env/config + releases |
| `balena://fleet/{org}/{fleet}/releases` | template | the fleet's release history |
| `balena://release/{id}` | template | release metadata + docker-compose composition + assets |
| `balena://os-versions/{type}` | template | available balenaOS versions (stable + ESR + draft) for a device type |

> Fleet slugs are `org/fleet`, so the fleet templates take the two parts as
> separate path segments (e.g. `balena://fleet/myorg/myfleet`).

## Development

```sh
go build -o bin/balenamcp           # build
go test ./...                       # unit tests (in-process MCP client, dry-run)
```

For a live end-to-end sweep against real balenaCloud (requires `balena login`
and a sacrificial device):

```sh
BALENA_LIVE_FLEET=myorg/myfleet \
BALENA_LIVE_DEVICE=<uuid> \
BALENA_LIVE_RELEASE=<commit> \
BALENA_LIVE_RELEASE_ALT=<other-commit> \
  go test -tags=integration -v -count=1 -run TestLiveSweep .
```

Irreversible sub-tests (`device-purge`, `device-shutdown`, `release-finalize`)
gate on additional `BALENA_LIVE_ALLOW_*` opt-in env vars and skip by default.

Layout:

- `main.go` — entry point, flag parsing, stdio transport
- `server/setup.go` — all tool definitions
- `main_test.go` — in-process MCP client driving every tool in dry-run mode
- `livetest_test.go` — build-tagged (`integration`) end-to-end sweep against
  real balenaCloud, opt-in via env vars

### Release flow

Releases are automated via [release-please](https://github.com/googleapis/release-please)
+ [goreleaser](https://goreleaser.com):

1. Commits to `main` use [Conventional Commits](https://www.conventionalcommits.org/)
   (`feat:`, `fix:`, `perf:`, `chore:`, `docs:`, etc.). The PR title gate
   enforces this on every PR.
2. **release-please** watches `main` and opens a "chore(main): release vX.Y.Z"
   PR that bumps `.release-please-manifest.json` and rewrites `CHANGELOG.md`
   based on the conventional-commit history since the last tag.
3. Merging that PR pushes a `vX.Y.Z` tag.
4. **goreleaser** (in `release.yml`) fires on the tag push: cross-compiles
   for 5 targets, generates CycloneDX SBOMs via syft, signs `checksums.txt`
   and SBOMs with cosign keyless, uploads everything to the GitHub Release
   release-please already created.

End-to-end, a release looks like:

```
$ git commit -m "feat: add foo tool"     # conventional commit
$ git push                                # to a branch + PR + merge
                                          # ... release-please opens release PR ...
$ # merge the release PR ...
$ # ... goreleaser publishes binaries + SBOMs + signatures to GH Releases
```

## Troubleshooting

### Where to find the server's logs

Logs land wherever the MCP host writes them:

| Client | Path |
|---|---|
| Claude Desktop, macOS | `~/Library/Logs/Claude/mcp-server-balena.log` |
| Claude Desktop, Windows | `%APPDATA%\Claude\logs\mcp-server-balena.log` |
| Claude Desktop, Linux | `~/.config/Claude/logs/mcp-server-balena.log` |
| Claude Code | `claude --mcp-debug` enables verbose MCP logging to the current session; persistent logs live in `~/.claude/projects/<encoded-cwd>/` |
| Cursor / Continue / others | Check each client's own log dir; the server itself writes only its startup line to stderr |

### Common errors

- **`balena CLI error: exec: not found`** — the `balena` binary isn't on
  `PATH` for whatever user the MCP host runs the server as. For desktop
  apps that's the GUI's `PATH`, not your shell's. Add a symlink to
  `/usr/local/bin/balena` or set the absolute path in the client's MCP
  config under `env` / `command`.
- **Auth errors** — `balena login` again on the host where the server runs.
- **`We need to slow down your requests temporarily.`** — balenaCloud
  rate-limits fast sequential calls. The server doesn't queue or back off;
  it surfaces the message as the tool result. Wait a few seconds and retry,
  or pace the agent's calls. Most noticeable when sweeping many tools in a
  row (e.g. validation runs).
- **Tool annotated as destructive but the client didn't prompt** — set
  `BALENAMCP_REQUIRE_CONFIRM=1` in the server's env (via the client's MCP
  `env:` block) so the server itself refuses destructive calls without
  `confirm: true` in the arguments. Documented above.

## License

MIT

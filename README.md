# BalenaMCP

A [Model Context Protocol](https://modelcontextprotocol.io/) server that wraps
the [balena CLI](https://github.com/balena-io/balena-cli) so MCP-aware clients
(Claude Desktop, etc.) can list fleets, inspect devices, manage tags and env
vars, pin releases, reboot devices, and more.

This is a personal fork of [klutchell/balenamcp](https://github.com/klutchell/balenamcp)
brought up to date with the current balena CLI and the current `mark3labs/mcp-go`.

## Prerequisites

- Go 1.23+
- [`balena` CLI](https://github.com/balena-io/balena-cli) on `PATH`
- An MCP client (e.g. Claude Desktop)

## Build

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

A `-dry-run` flag is available — the server prints the balena command it
*would* run instead of executing it. Useful for testing/debugging.

### Environment variables

| Variable | Default | Purpose |
|---|---|---|
| `BALENAMCP_EXEC_TIMEOUT` | `60` (seconds) | Wall-clock cap on any single balena CLI subprocess. Prevents `device-logs --tail` and similar long-running commands from blocking the MCP transport indefinitely. Set to a higher integer for slow networks; the server logs a warning and falls back to default if the value is non-positive or non-numeric. |

## Authenticate

Before using any tool that touches balenaCloud, log in once on the host:

```sh
balena login
```

The MCP server inherits that session by shelling out to the same `balena`
binary.

## Claude Desktop config

Add this to `claude_desktop_config.json` (macOS:
`~/Library/Application Support/Claude/`, Windows: `%APPDATA%\Claude\`):

```json
{
  "mcpServers": {
    "balena": {
      "command": "/absolute/path/to/balenamcp/bin/balenamcp",
      "args": []
    }
  }
}
```

Restart Claude Desktop. The tools below appear under the `balena` server.

## Tools

### Read-only

| Tool | Purpose |
|---|---|
| `version` | balena CLI version |
| `whoami` | Current user / org / device session info |
| `fleet-list` | List all accessible fleets |
| `fleet-info` | Detailed info for one fleet |
| `device-list` | List devices, optionally filtered by fleet |
| `device-info` | Detailed info for one device |
| `device-logs` | Recent (or streamed) logs from a device, optionally per-service |
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

### Mutating (marked `destructiveHint` so well-behaved MCP clients confirm before invoking)

| Tool | Effect |
|---|---|
| `device-reboot` | Remote reboot |
| `device-restart` | Restart containers (no reboot) |
| `device-shutdown` | Remote shutdown (manual power cycle to recover) |
| `device-purge` | Wipe `/data` on the device |
| `device-pin` | Pin a device to a specific release |
| `fleet-pin` | Pin a fleet to a specific release |
| `release-finalize` | Promote a draft release to final |
| `tag-set` | Create or update a tag |
| `tag-rm` | Remove a tag |
| `env-set` | Set/update an env or config variable |
| `env-rm` | Delete an env or config variable (needs `yes: true` to bypass the CLI's confirm prompt) |

`tag-list` / `tag-set` / `tag-rm` require **exactly one** of
`fleet` / `device` / `release`. `env-list` / `env-set` require **exactly one**
of `fleet` / `device`.

## Development

```sh
go build -o bin/balenamcp           # build
go test ./...                       # unit/integration tests (uses in-process MCP client, dry-run)
```

Layout:

- `main.go` — entry point, flag parsing, stdio transport
- `server/setup.go` — all tool definitions
- `main_test.go` — in-process MCP client driving every tool in dry-run mode

## Troubleshooting

- Claude Desktop log: `%APPDATA%\Claude\logs\mcp-server-balena.log` (Windows) /
  `~/Library/Logs/Claude/mcp-server-balena.log` (macOS).
- If a tool errors with "balena CLI error: exec: not found", the binary is not
  on `PATH` for the user running Claude Desktop.
- Auth errors → `balena login` again.

## License

MIT

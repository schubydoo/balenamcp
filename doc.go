// Command balenamcp is a Model Context Protocol (MCP) server that wraps
// the balena CLI, letting MCP-aware AI assistants (Claude Desktop, Claude
// Code, Cursor, Cline, Continue, and others) drive balenaCloud through
// a structured tool interface instead of free-form shell invocation.
//
// # Usage
//
//	balenamcp [-dry-run]
//
// The server speaks MCP over stdio. It is normally launched by an MCP
// client via that client's server-configuration mechanism (for example,
// "claude mcp add balena /usr/local/bin/balenamcp"), not run directly.
//
// The -dry-run flag swaps real command execution for stubbed responses
// that report the argv that would have been invoked. Useful for tool
// development and CI smoke tests without a live balena login.
//
// # Prerequisites
//
// The balena CLI must be installed and on PATH, and the invoking user
// must be authenticated (run "balena login" once). balenamcp shells out
// to the CLI for every operation; it does not talk to the balenaCloud
// API directly.
//
// # Tool surface
//
// The server exposes 29 tools split into two categories. Read-only tools
// (version, whoami, fleet-list, fleet-info, device-list, device-info,
// device-logs, device-type-list, release-list, release-info,
// release-asset-list, tag-list, env-list, os-versions, organization-list,
// ssh-key-list, api-key-list) carry the MCP readOnlyHint and are safe to
// invoke without confirmation.
//
// Mutating tools (device-reboot, device-restart, device-shutdown,
// device-purge, device-pin, device-track-fleet, fleet-pin,
// release-finalize, tag-set, tag-rm, env-set, env-rm) carry the
// destructiveHint so compliant clients can prompt the user before
// running them.
//
// Every tool's identifier arguments are flag-shape guarded: arguments
// beginning with "-" are rejected server-side to prevent argv injection
// where an agent passes "--help" or similar as a UUID. Free-form values
// (tag values, env values) are intentionally exempt.
//
// # Configuration
//
// All configuration lives in the invoking client's MCP server definition.
// There are no environment variables specific to balenamcp itself — the
// balena CLI's own authentication state (typically ~/.balena/token) is
// inherited from the launching shell.
//
// # Verifying a release
//
// Release archives are signed via Sigstore cosign keyless signing and
// published with CycloneDX SBOMs. See the project README's "Install"
// section for the cosign verify-blob invocation.
//
// # Source
//
// https://github.com/schubydoo/balenamcp
package main

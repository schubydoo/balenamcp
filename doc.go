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
// The server exposes 64 tools covering fleets, devices, releases, tags,
// env vars, organizations, SSH keys, API keys and release assets, plus 11
// guided workflow prompts and read-only balena:// resources. 22 tools carry
// the MCP readOnlyHint and are safe to invoke without confirmation; 42
// carry the destructiveHint so compliant clients can prompt the user
// before running them. The per-tool inventory lives in the README and in
// package server's documentation — this file deliberately does not repeat
// the list, which goes stale with every addition.
//
// Every tool's identifier arguments are flag-shape guarded: arguments
// beginning with "-" are rejected server-side to prevent argv injection
// where an agent passes "--help" or similar as a UUID. Free-form values
// (tag values, env values) are intentionally exempt. List-accepting device
// commands are constrained to one device per call, and the tools that
// touch the host filesystem are confined to the directory named by
// BALENAMCP_ASSET_DIR (disabled entirely when unset).
//
// # Configuration
//
// The balena CLI's own authentication state (typically ~/.balena/token)
// is inherited from the launching shell. Three environment variables tune
// the server itself:
//
//   - BALENAMCP_EXEC_TIMEOUT — wall-clock cap in seconds for any single
//     balena CLI subprocess (default 60).
//   - BALENAMCP_REQUIRE_CONFIRM — when truthy, every destructive tool
//     refuses to run unless the call carries confirm:true in its
//     arguments.
//   - BALENAMCP_ASSET_DIR — the single directory the release-asset
//     download/upload and ssh-key-add tools may read or write; unset (the
//     default) disables those tools entirely.
//
// See the README's environment-variable table for the full semantics.
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

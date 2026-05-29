// Package server implements the MCP tool registry for balenamcp.
//
// The package exposes one entry point — [SetupServer] — that constructs an
// [github.com/mark3labs/mcp-go/server.MCPServer], wires up all 29 balena CLI
// wrappers as MCP tools, and returns it ready to serve over stdio. The
// invoking process (typically [main]) calls
// [github.com/mark3labs/mcp-go/server.ServeStdio] on the returned server.
//
// # Configuration
//
// Runtime tuning lives in package-level state, populated from environment
// variables at SetupServer time:
//
//   - [Config].DryRun (set from main.go's -dry-run flag, not env) — stub
//     subprocess execution and return the rendered argv instead. Used by
//     tests and ad-hoc inspection.
//   - [Config].ExecTimeout (BALENAMCP_EXEC_TIMEOUT, seconds) — per-call
//     wall-clock cap on the underlying balena CLI subprocess. Defaults to
//     60s. Falls back to default on invalid input with a stderr warning.
//   - [Config].RequireConfirm (BALENAMCP_REQUIRE_CONFIRM) — when true, every
//     destructive tool refuses to run unless the caller passes
//     confirm:true in arguments. Belt-and-suspenders for MCP clients that
//     ignore the destructiveHint annotation.
//
// [Version] is overridden at build time via -ldflags; goreleaser injects the
// release tag, source builds report "dev".
//
// # Tool surface
//
// Tools split into two categories: 17 read-only (registered under the
// readOnlyHint annotation) and 12 destructive (destructiveHint annotation,
// plus an injected confirm bool field). Registration is grouped by category
// across helper functions (registerReadOnlyIdentity,
// registerMutatingDeviceLifecycle, …) — each helper handles a small cluster
// of related tools to keep cyclomatic complexity manageable and to make
// "where is tool X wired up?" greppable.
//
// # Safety invariants
//
// Identifier arguments (UUIDs, slugs, commit hashes, env var names, tag
// keys, service names) are flag-shape guarded via [rejectFlagShape]: any
// value starting with '-' is rejected before reaching the balena CLI, to
// prevent argv injection where an agent passes "--help" as a UUID.
// Free-form values (tag values, env values) are intentionally exempt.
//
// device-logs additionally refuses tail:true server-side: streaming
// responses don't work over the request/response MCP transport.
//
// env-list refuses --config + --service in the same call, surfacing the
// balena CLI's "these flags are mutually exclusive" earlier with a
// clearer message.
//
// # Prompts
//
// Alongside tools, the server registers a set of MCP prompts
// (registerPrompts) — user-invoked workflow templates for multi-step
// operations: diagnose-device, fleet-health-report, safe-release-rollout,
// rollback-device, audit-config, compare-releases, replicate-config, and
// bulk-tag. A prompt makes no balena CLI calls
// itself; it returns a single user-role message that instructs the model
// which tools to call and in what order, encoding operational sequencing and
// safety ordering (e.g. safe-release-rollout pins one canary and verifies it
// before touching the whole fleet). The actual guards stay on the tools the
// prompt steers toward. Prompt handlers are therefore pure functions of their
// arguments.
//
// # Resources
//
// The server also exposes read-only balena state as MCP resources
// (registerResources) under the balena:// URI scheme — three static
// resources (balena://account, balena://fleets, balena://device-types) and
// three URI templates (balena://device/{uuid},
// balena://fleet/{org}/{fleet}, balena://fleet/{org}/{fleet}/releases).
// Whereas a tool is one CLI call invoked by the model, a resource is
// user-attached context that COMPOSES several CLI calls into one JSON
// document — the device snapshot folds status, recent logs, env/config
// variables, and tags into a single read. Composition degrades gracefully:
// the shared composite helper records a failed sub-call under an "errors"
// object with "partial": true rather than failing the whole read. Fleet
// slugs contain a slash, so the fleet templates use two path params
// reassembled by parseFleetURI.
package server

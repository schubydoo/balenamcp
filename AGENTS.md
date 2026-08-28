# balenamcp

Guidance for agents working in **balenamcp** — a Go MCP server that wraps the
[balena CLI](https://github.com/balena-io/balena-cli) and exposes tools, prompts, and
resources to MCP-aware agents.

## Layout
- `main.go` — entry point; `-dry-run` prints the balena argv instead of executing.
- `server/setup.go` — server construction + **tool** registration and handlers.
- `server/prompts.go` — **prompt** (guided workflow) handlers.
- `server/resources.go` — `balena://` **resource** handlers (static + templated).
- `server/doc.go` — package doc; carries the **count-bearing prose** ("44 balena
  CLI tools") that goes stale the moment you add or remove one.
- `*_test.go` next to each; `livetest_test.go` holds env-gated live tests.

## Conventions that span multiple files (don't miss these)
When you add, rename, or remove a **tool / prompt / resource**, update all of:
1. The registration in `server/*.go`.
2. The matching `required=(...)` / `required_prompts=(...)` / `required_resources=(...)`
   array in `.github/workflows/ci.yml` — the **mcp-smoke** job asserts the exact
   advertised inventory over stdio and fails on any mismatch.
3. The corresponding **Tools / Prompts / Resources** section of `README.md`,
   including the destructive-tool table and the "the remaining N tools are
   read-only" counts.
4. The tool/prompt/resource counts in `server/doc.go`.
5. Tests, keeping line coverage **≥ 80%** (the `coverage` CI gate). Mutation
   testing runs too — `gremlins --diff` per PR, full-tree weekly — but both are
   **advisory, not blocking**, so a surviving mutant is a signal to assert
   behavior rather than a merge blocker.

Before shipping such a change, confirm the four axes line up: advertised inventory
(server ↔ ci.yml ↔ README), argv parity vs the current balena CLI surface, exec-safety
annotations, and coverage.

## Safety rules
- All execution goes through `exec.CommandContext(ctx, "balena", args...)` with
  args as a **slice** — never build shell strings or interpolate input into a
  command line.
- **Destructive** tools (reboot, restart, shutdown, purge, finalize, *-rm) must
  set `DestructiveHint: true` and call `guardDestructive(...)`; read-only tools
  set `ReadOnlyHint: true`. CI spot-checks these annotations.
- Use the `readOnly` / `destructive` option helpers, never the raw mcp-go
  annotations: **mcp-go's `NewTool` defaults `DestructiveHint` to true**, so a
  tool given only `WithReadOnlyHintAnnotation(true)` ships flagged as both.
  `readOnly` clears it explicitly; `destructive` also injects the `confirm`
  schema field that `BALENAMCP_REQUIRE_CONFIRM` reads.
- Env knobs: `BALENAMCP_EXEC_TIMEOUT` (per-call timeout), `BALENAMCP_REQUIRE_CONFIRM`
  (force confirmation on destructive tools).

## Scope
Not every balena CLI command is meant to be wrapped. Before adding a tool, check
the **"What balenamcp deliberately does not wrap"** table in `README.md` — build/
deploy, OS imaging, `config *`, `join`/`leave`/`tunnel`, auth, `support`,
`settings` and `api-key generate` are excluded by decision, each with a recorded
reason. A command listed there is accounted for, not a gap; reopening one of
those decisions is a conversation, not a PR. The same table carries the standing
rule that only the canonical `fleet` form of a fleet-class command is wrapped,
never its `app`/`block` variants.

## Releases
release-please + Conventional Commits drive versioning. **Do not** hand-edit
`CHANGELOG.md`, `.release-please-manifest.json`, or version numbers — the release
PR owns them. Commit and PR titles must be Conventional Commits
(`feat:`/`fix:`/`chore:`/… — enforced by `pr-title.yml`).

## Local verification (mirrors CI)
```bash
gofmt -l . && go vet ./... && go build ./... && go test -race ./...
```
Live tests hit real balenaCloud hardware and are gated on `BALENA_LIVE_FLEET` +
`BALENA_LIVE_DEVICE`, so `go test ./...` never runs them. To run them deliberately,
set those two env vars.

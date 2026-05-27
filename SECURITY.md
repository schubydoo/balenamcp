# Security Policy

## Reporting a vulnerability

If you've found a security issue in BalenaMCP, please report it via GitHub's
private vulnerability reporting:

**https://github.com/schubydoo/balenamcp/security/advisories/new**

This is the preferred channel — it keeps the disclosure private until a fix
ships and gives a clean paper trail. If the report involves an embargo or
coordination with another upstream (e.g. the balena CLI itself), say so in
the report and I'll work with you on disclosure timing.

Please **do not** open a public GitHub issue for security reports. If
GitHub's private flow is unavailable to you for some reason, email
`schubydoo@users.noreply.github.com` with a description and we'll move it
to the right channel.

## In scope

Vulnerabilities in the MCP server itself, including:

- The tool dispatch path in `server/setup.go` — argument validation,
  command construction, the destructive-tool confirmation gate
  (`BALENAMCP_REQUIRE_CONFIRM`), the `executeCommand` subprocess wrapper
- The MCP transport / stdio handling
- The build & release pipeline (`.github/workflows/`) — anything that
  could let a malicious PR exfiltrate a secret or tamper with the
  released binary

## Out of scope

- Vulnerabilities in the upstream `balena` CLI — report those directly to
  [balena-io/balena-cli](https://github.com/balena-io/balena-cli/security/policy).
  Once a fix lands upstream, this server will surface it on the next
  `balena` install.
- Dependency CVEs in `go.mod` — these are tracked automatically by
  Dependabot security alerts + Renovate; the public `dependencies` /
  `security` labels and the weekly Renovate sweep cover routine triage.
  Open a private report only if you've identified an exploit chain
  specific to how this server uses the dep, not just "library X has a
  CVE."
- The behavior of any tool whose effect is documented as destructive in
  the README's *⚠️ Destructive tools* table — those are working as
  intended; clients are expected to confirm before invocation.

## What "fixed" means here

A reasonable response time for a private report is days, not hours.
This is a personal-fork project maintained on hobby time. If a report
needs a faster turnaround (active exploit, embargo deadline), say so up
front and I'll prioritize accordingly.

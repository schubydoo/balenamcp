package server

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Prompts expose guided, multi-step balena operations as MCP prompts. Unlike
// tools (which the model invokes one CLI call at a time) a prompt is a
// user-invoked workflow template: it returns a single user-role message that
// instructs the model on WHICH tools to call and in WHAT order, encoding the
// operational sequencing and safety ordering an experienced operator would
// follow.
//
// Prompt handlers make no balena CLI calls themselves — they are pure
// functions of their arguments. The model does the actual work by calling the
// named read-only/destructive tools afterwards. This keeps prompts trivially
// testable and side-effect free; the real guards (flag-shape rejection, the
// BALENAMCP_REQUIRE_CONFIRM gate) live on the tools the prompt steers the
// model toward.

// requirePromptArg fetches a required prompt argument, returning a non-nil
// error when it is missing or empty. The MCP spec lets clients omit arguments
// even when the prompt declares them required, so handlers validate
// defensively rather than trusting the transport.
func requirePromptArg(req mcp.GetPromptRequest, key, human string) (string, error) {
	v := req.Params.Arguments[key]
	if v == "" {
		return "", fmt.Errorf("missing required argument %q (%s)", key, human)
	}
	return v, nil
}

// registerPrompts wires every workflow prompt onto srv. Kept as a thin
// dispatcher so each prompt's definition stays greppable and the function
// stays well under gocyclo's complexity ceiling.
func registerPrompts(srv *server.MCPServer) {
	srv.AddPrompt(mcp.NewPrompt("diagnose-device",
		mcp.WithPromptDescription(
			"Guided health diagnosis of a single device: pulls status, logs, "+
				"env, and pin state, then summarizes the likely root cause."),
		mcp.WithArgument("uuid", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Device UUID to diagnose.")),
	), handleDiagnoseDevice)

	srv.AddPrompt(mcp.NewPrompt("fleet-health-report",
		mcp.WithPromptDescription(
			"Summarize the health of a fleet: device status tally, target "+
				"release, and which devices are offline or behind."),
		mcp.WithArgument("fleet", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Fleet name or org/fleet slug.")),
	), handleFleetHealthReport)

	srv.AddPrompt(mcp.NewPrompt("safe-release-rollout",
		mcp.WithPromptDescription(
			"Canary-first rollout of a release to a fleet: record the rollback "+
				"target, pin one canary, verify it, then roll out fleet-wide."),
		mcp.WithArgument("fleet", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Fleet name or org/fleet slug to roll out to.")),
		mcp.WithArgument("release", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Release commit to roll out.")),
	), handleSafeReleaseRollout)

	srv.AddPrompt(mcp.NewPrompt("rollback-device",
		mcp.WithPromptDescription(
			"Roll a single device back to a previously known-good release, "+
				"with user confirmation of the target."),
		mcp.WithArgument("uuid", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Device UUID to roll back.")),
	), handleRollbackDevice)

	srv.AddPrompt(mcp.NewPrompt("audit-config",
		mcp.WithPromptDescription(
			"Audit a fleet's env/config variables for drift, secret-shaped "+
				"values, and orphaned device overrides."),
		mcp.WithArgument("fleet", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Fleet name or org/fleet slug to audit.")),
	), handleAuditConfig)

	srv.AddPrompt(mcp.NewPrompt("compare-releases",
		mcp.WithPromptDescription(
			"Compare two releases: per-service image-size deltas, composition "+
				"differences, and asset changes."),
		mcp.WithArgument("release_a", mcp.RequiredArgument(),
			mcp.ArgumentDescription("First release commit or numeric ID (the baseline).")),
		mcp.WithArgument("release_b", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Second release commit or numeric ID (compared against the baseline).")),
	), handleCompareReleases)

	srv.AddPrompt(mcp.NewPrompt("replicate-config",
		mcp.WithPromptDescription(
			"Copy env/config variables from a source fleet or device to a "+
				"target, with an approval step before any change is written."),
		mcp.WithArgument("source", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Source fleet slug (org/fleet) or device UUID to copy variables FROM.")),
		mcp.WithArgument("target", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Target fleet slug (org/fleet) or device UUID to copy variables TO. Must be the same kind as source.")),
	), handleReplicateConfig)

	srv.AddPrompt(mcp.NewPrompt("bulk-tag",
		mcp.WithPromptDescription(
			"Apply a tag to many devices in a fleet at once, with an approval "+
				"step before any change is written."),
		mcp.WithArgument("fleet", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Fleet name or org/fleet slug whose devices to tag.")),
		mcp.WithArgument("key", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Tag key to set on each device.")),
		mcp.WithArgument("value",
			mcp.ArgumentDescription("Tag value to set. Omit to set an empty-value tag.")),
	), handleBulkTag)

	srv.AddPrompt(mcp.NewPrompt("deep-diagnose-device",
		mcp.WithPromptDescription(
			"Deep host-level diagnosis of a device over SSH: pulls memory, disk, "+
				"load, failed services, and container state via device-ssh, then "+
				"summarizes resource risks and root cause."),
		mcp.WithArgument("uuid", mcp.RequiredArgument(),
			mcp.ArgumentDescription("Device UUID to diagnose.")),
	), handleDeepDiagnoseDevice)

	srv.AddPrompt(mcp.NewPrompt("prepare-local-dev",
		mcp.WithPromptDescription(
			"Enable local mode on a device for LAN-based development, discovering "+
				"it with device-detect first if needed, with an approval step."),
		mcp.WithArgument("device",
			mcp.ArgumentDescription("Device UUID or IP/.local address. Omit to discover devices on the LAN with device-detect.")),
	), handlePrepareLocalDev)

	srv.AddPrompt(mcp.NewPrompt("rotate-api-keys",
		mcp.WithPromptDescription(
			"Review balenaCloud API keys and revoke the ones no longer needed, "+
				"with an explicit approval step before any key is revoked."),
		mcp.WithArgument("fleet",
			mcp.ArgumentDescription("Optional fleet slug to scope the key review to a fleet's keys. Omit to review your user-level keys.")),
	), handleRotateApiKeys)
}

// ----- handlers -----------------------------------------------------------
//
// Each handler validates its required arguments, interpolates them into a
// runbook template that names the exact balenamcp tools to call, and returns
// it as a single user-role message. The %[n]s indices let a single argument
// be referenced multiple times in the template.

const diagnoseDeviceTemplate = `You are diagnosing the health of balena device %[1]s. Work through these steps using the balenamcp tools, then report your findings.

1. Call device-info with uuid=%[1]s to get status, IP, supervisor version, OS version, and the currently running/target release.
2. Call device-logs with device=%[1]s to retrieve recent logs. Look for crash loops, restart cycles, image-download failures, or service errors.
3. Call env-list with device=%[1]s to review device-level environment and config variables that could affect behavior.
4. Call tag-list with device=%[1]s for any operational tags.
5. Call device-pin with uuid=%[1]s (omit release) to see whether the device is pinned to a specific release or tracking the fleet.

Then summarize:
- Overall health verdict (healthy / degraded / offline / unknown).
- The most likely root cause, citing specific log lines or status fields.
- Concrete recommended next actions.

Do not take any destructive action (reboot, restart, purge, pin) without explicit user approval.`

func handleDiagnoseDevice(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	uuid, err := requirePromptArg(req, "uuid", "device UUID")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Diagnosis plan for device %s", uuid),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(diagnoseDeviceTemplate, uuid))),
		},
	), nil
}

const fleetHealthReportTemplate = `Produce a health report for balena fleet %[1]s using the balenamcp tools.

1. Call device-list with fleet=%[1]s and json=true to enumerate every device.
2. Tally devices by status (online / offline / updating).
3. Call fleet-info with fleet=%[1]s to find the fleet's pinned/target release.
4. Call release-list with fleet=%[1]s to see recent releases.
5. Flag any device whose running release differs from the fleet target, and any device that is offline.

Report:
- Device count by status.
- The fleet target release and how many devices are on it versus behind.
- A prioritized list of devices needing attention, with the reason for each.

Keep it concise and scannable. This is a read-only investigation — take no destructive action.`

func handleFleetHealthReport(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	fleet, err := requirePromptArg(req, "fleet", "fleet slug")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Health report for fleet %s", fleet),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(fleetHealthReportTemplate, fleet))),
		},
	), nil
}

const safeReleaseRolloutTemplate = `Guide a SAFE rollout of release %[2]s to fleet %[1]s. Follow canary-first ordering and never skip a verification step.

1. Call fleet-pin with fleet=%[1]s (omit release) to record the CURRENT pinned release. State it explicitly — this is the rollback target.
2. Call device-list with fleet=%[1]s to list candidate devices. Pick ONE healthy, online device as the canary and explain why.
3. Pin the canary: call device-pin with uuid=<canary> and release=%[2]s. This is a state-changing tool — surface the change to the user first, and pass confirm:true if the server requires confirmation.
4. Verify the canary: call device-info and device-logs on the canary and confirm it downloads, starts, and runs %[2]s cleanly. Re-check if it is still updating.
5. ONLY if the canary is healthy on %[2]s, roll out fleet-wide: call fleet-pin with fleet=%[1]s and release=%[2]s.
6. If the canary fails, do NOT proceed with the fleet-wide pin. Recover the canary by re-pinning it to the original release with device-pin (or device-track-fleet to resume fleet tracking). If release %[2]s is fundamentally broken, also call release-invalidate with id=%[2]s so it can never auto-deploy to tracking devices (reversible later with release-validate). Then report the failure.
7. If you have ALREADY rolled out fleet-wide (step 5) and then discover a problem, roll the FLEET back: re-pin it to the original release with fleet-pin, or call fleet-track-latest to resume tracking the latest known-good release. Then report.

Pause for explicit user approval before step 3 and before step 5. Restate the rollback path at every stage.`

func handleSafeReleaseRollout(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	fleet, err := requirePromptArg(req, "fleet", "fleet slug")
	if err != nil {
		return nil, err
	}
	release, err := requirePromptArg(req, "release", "release commit")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Canary-first rollout of %s to fleet %s", release, fleet),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(safeReleaseRolloutTemplate, fleet, release))),
		},
	), nil
}

const rollbackDeviceTemplate = `Roll back device %[1]s to a previously known-good release, safely.

1. Call device-info with uuid=%[1]s to see the currently running/target release and the device's fleet.
2. Call device-pin with uuid=%[1]s (omit release) to see the current explicit pin, if any.
3. Call release-list for the device's fleet to enumerate available releases in order. Identify the release immediately prior to the current one, or a specific known-good release the user names.
4. Confirm the rollback target with the user, including its commit and creation date.
5. Apply the rollback: call device-pin with uuid=%[1]s and release=<target>. This is a state-changing tool — pass confirm:true if the server requires it, only after user approval.
6. Verify with device-info and device-logs that the device picks up and runs the target release.
7. If the release you rolled back FROM is itself broken (not merely unwanted on this one device), consider calling release-invalidate with id=<bad release> so it stops auto-deploying to other tracking devices — confirm with the user first, and note it is reversible with release-validate.

Do not apply the pin without explicit user approval of the target release.`

func handleRollbackDevice(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	uuid, err := requirePromptArg(req, "uuid", "device UUID")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Rollback plan for device %s", uuid),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(rollbackDeviceTemplate, uuid))),
		},
	), nil
}

const auditConfigTemplate = `Audit the environment and config variables of balena fleet %[1]s for drift and risk using the balenamcp tools.

1. Call env-list with fleet=%[1]s and json=true to capture fleet-level variables (the defaults every device inherits).
2. Call device-list with fleet=%[1]s to enumerate devices, then call env-list with device=<uuid> for each. If the fleet is large, sample a representative subset and say so.
3. Compare device-level variables against the fleet defaults and flag:
   - Drift: device overrides that differ from the fleet default.
   - Secret-shaped values: variables whose names suggest credentials (TOKEN, KEY, SECRET, PASSWORD). Note their presence — never echo their values.
   - Orphans: device variables with no fleet counterpart.
4. Note any config variables (set config=true on env-list) that diverge across devices.

Report a drift table (variable, fleet value, divergent devices) and a short risk summary. This is read-only — make no changes. Never print secret values in full; mask them.`

func handleAuditConfig(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	fleet, err := requirePromptArg(req, "fleet", "fleet slug")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Config audit for fleet %s", fleet),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(auditConfigTemplate, fleet))),
		},
	), nil
}

const compareReleasesTemplate = `Compare two balena releases, %[1]s and %[2]s, using the balenamcp tools, and report the differences.

1. Call release-info with id=%[1]s and json=true, then with id=%[2]s and json=true. Capture each release's services and their image sizes, status, and creation date.
2. Call release-info with id=%[1]s and composition=true, then id=%[2]s and composition=true, to capture each release's docker-compose composition.
3. Call release-asset-list with id=%[1]s, then id=%[2]s, to capture attached binary assets and their sizes.

Then report:
- Per-service image-size deltas: which service images grew or shrank between %[1]s and %[2]s and by how much, plus the net total size change.
- Composition differences: services added or removed, image/tag changes, and changed environment or label entries.
- Asset differences: assets added, removed, or changed in size.
- A one-line summary (e.g. "%[2]s is ~40 MB larger than %[1]s, driven by the <service> image").

Read-only — make no changes.`

func handleCompareReleases(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	a, err := requirePromptArg(req, "release_a", "release commit or ID")
	if err != nil {
		return nil, err
	}
	b, err := requirePromptArg(req, "release_b", "release commit or ID")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Comparison of releases %s and %s", a, b),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(compareReleasesTemplate, a, b))),
		},
	), nil
}

const replicateConfigTemplate = `Replicate environment/config variables from %[1]s to %[2]s using the balenamcp tools. Each of %[1]s and %[2]s is either a fleet slug (org/fleet) or a device UUID — pick the matching env-list/env-set flag (--fleet vs --device) for each.

1. Call env-list on the SOURCE %[1]s with json=true to capture its variables (names, values, service scope, and whether each is a config variable).
2. Call env-list on the TARGET %[2]s with json=true so you can report which variables will be newly created versus overwritten.
3. Present a plan to the user: the variables to copy, marking each as create or overwrite, and MASKING any secret-shaped values (names containing TOKEN, KEY, SECRET, or PASSWORD). Never print secret values.
4. WAIT for explicit user approval before writing anything.
5. On approval, for each variable call env-set on %[2]s with the name, value, and the matching --service scope where applicable. env-set is a state-changing tool — pass confirm:true if the server requires it.
6. Skip variables that should not be blindly copied (e.g. per-device identifiers or addresses); call these out instead of copying them.

Report a summary: variables created, overwritten, and skipped.`

func handleReplicateConfig(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	source, err := requirePromptArg(req, "source", "fleet slug or device UUID")
	if err != nil {
		return nil, err
	}
	target, err := requirePromptArg(req, "target", "fleet slug or device UUID")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Replicate config from %s to %s", source, target),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(replicateConfigTemplate, source, target))),
		},
	), nil
}

const bulkTagTemplate = `Apply the tag %[2]s = %[3]s to multiple devices in fleet %[1]s using the balenamcp tools.

1. Call device-list with fleet=%[1]s and json=true to enumerate the devices. If the user described a subset (e.g. only offline devices, or devices matching a name pattern), filter to those and state the filter you applied.
2. Present the plan: the exact list of devices that will be tagged, the tag key %[2]s, and the value %[3]s.
3. WAIT for explicit user approval before writing anything.
4. On approval, for each selected device call tag-set with device=<uuid>, key=%[2]s, and the value. tag-set is a state-changing tool — pass confirm:true if the server requires it.
5. If a tag-set fails on a device, continue with the rest and collect the failure.

Report a summary: devices tagged successfully and any that failed.`

func handleBulkTag(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	fleet, err := requirePromptArg(req, "fleet", "fleet slug")
	if err != nil {
		return nil, err
	}
	key, err := requirePromptArg(req, "key", "tag key")
	if err != nil {
		return nil, err
	}
	value := req.Params.Arguments["value"]
	valueDisplay := value
	if valueDisplay == "" {
		valueDisplay = "(empty value)"
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Bulk-tag devices in fleet %s with %s", fleet, key),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(bulkTagTemplate, fleet, key, valueDisplay))),
		},
	), nil
}

const deepDiagnoseDeviceTemplate = `You are running a DEEP health diagnosis of balena device %[1]s — going past cloud metadata to inspect the host OS directly over SSH. Work through these steps with the balenamcp tools, then report.

1. Call device-info with uuid=%[1]s for cloud-side status, IP, supervisor/OS version, and the running/target release. Confirm the device is ONLINE first — device-ssh needs a reachable device, so if it is offline, stop and report that.
2. Gather host-level metrics with device-ssh. It runs ONE command on the host OS and returns its output; it is annotated destructive (raw shell access), so surface each command and pass confirm:true if the server requires confirmation. Run, in turn, only these read-only inspection commands:
   - device-ssh uuid=%[1]s command="cat /proc/meminfo | head -n 3" — memory pressure (MemTotal / MemFree / MemAvailable).
   - device-ssh uuid=%[1]s command="df -h /" — root/data filesystem usage; flag anything near full.
   - device-ssh uuid=%[1]s command="uptime" — load average and host uptime.
   - device-ssh uuid=%[1]s command="systemctl --failed --no-legend" — failed host services.
   - device-ssh uuid=%[1]s command="balena ps -a" — container states; restarting/exited containers signal crash loops.
3. Call device-logs with device=%[1]s for recent supervisor/service logs, and correlate them with anything the host metrics flagged.

Then summarize:
- A health verdict (healthy / degraded / critical / offline), citing the specific metric or log line behind it.
- Resource risks: memory exhaustion, full disk, high load, failed services, or restart loops.
- Concrete next actions.

Run only the read-only inspection commands above. Do not run mutating shell commands, reboot, restart, or purge without explicit user approval.`

func handleDeepDiagnoseDevice(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	uuid, err := requirePromptArg(req, "uuid", "device UUID")
	if err != nil {
		return nil, err
	}
	return mcp.NewGetPromptResult(
		fmt.Sprintf("Deep diagnosis plan for device %s", uuid),
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(deepDiagnoseDeviceTemplate, uuid))),
		},
	), nil
}

const prepareLocalDevTemplate = `Set up a balena device for LOCAL development. Local mode enables LAN-based push/SSH but SUSPENDS cloud-managed updates for that device until it is turned back off.

1. Identify the target device. %[1]s
2. Call device-local-mode-get with uuid=<device> to read the current state. If local mode is already enabled, say so and stop — there is nothing to do.
3. Explain the trade-off to the user: this device will stop receiving cloud updates until local mode is disabled again. WAIT for explicit user approval.
4. On approval, call device-local-mode-set with uuid=<device> and enable=true. This is a state-changing tool — pass confirm:true if the server requires it.
5. Verify with device-local-mode-get that local mode is now enabled, and remind the user they can restore cloud management later with device-local-mode-set enable=false.

Report the final local-mode state and the device's local address for push/SSH.`

func handlePrepareLocalDev(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	// device is optional: when the caller already knows the device we skip the
	// LAN scan; otherwise the runbook steers them through device-detect.
	device := req.Params.Arguments["device"]
	clause := "Run device-detect to scan the LAN for balenaOS devices, and confirm with the user which one to use if several appear."
	desc := "Prepare a device for local development"
	if device != "" {
		clause = fmt.Sprintf("The user named device %s — use it directly (no scan needed).", device)
		desc = fmt.Sprintf("Prepare device %s for local development", device)
	}
	return mcp.NewGetPromptResult(
		desc,
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(prepareLocalDevTemplate, clause))),
		},
	), nil
}

const rotateApiKeysTemplate = `Review balenaCloud API keys and revoke the ones the user no longer needs, safely.

1. Call api-key-list%[1]s to enumerate API keys (id and name; note created/last-used where shown). %[2]s
2. Present the keys to the user and ask WHICH to revoke. You may recommend candidates (clearly stale, unnamed, or superseded keys) but never decide unilaterally.
3. WAIT for explicit user approval naming the exact key IDs to revoke.
4. On approval, call api-key-revoke with ids=<comma-separated IDs> — a single comma-separated list with no spaces, e.g. "12,34". This is IRREVERSIBLE and destructive: anything authenticating with a revoked key stops working immediately. Pass confirm:true if the server requires it.
5. Confirm by calling api-key-list again and reporting which keys remain.

Never revoke a key the user has not explicitly named.`

func handleRotateApiKeys(_ context.Context, req mcp.GetPromptRequest) (*mcp.GetPromptResult, error) {
	// fleet is optional: present scopes the listing to a fleet's keys, absent
	// reviews the user's own API keys.
	fleet := req.Params.Arguments["fleet"]
	scope := ""
	note := "These are your user-level API keys."
	desc := "Review and revoke API keys"
	if fleet != "" {
		scope = fmt.Sprintf(" with fleet=%s", fleet)
		note = fmt.Sprintf("These are the API keys scoped to fleet %s.", fleet)
		desc = fmt.Sprintf("Review and revoke API keys for fleet %s", fleet)
	}
	return mcp.NewGetPromptResult(
		desc,
		[]mcp.PromptMessage{
			mcp.NewPromptMessage(mcp.RoleUser,
				mcp.NewTextContent(fmt.Sprintf(rotateApiKeysTemplate, scope, note))),
		},
	), nil
}

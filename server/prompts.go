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
6. If the canary fails, do NOT proceed. Re-pin the canary to the original release with device-pin, or call device-track-fleet to resume fleet tracking, then report the failure.

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

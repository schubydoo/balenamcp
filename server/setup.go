package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// defaultExecTimeout caps how long any single balena CLI subprocess may run
// before we forcibly kill it. Some balena commands (notably `device logs
// --tail`) are legitimately long-running and would otherwise block the MCP
// transport forever once an agent invoked them. 60s is comfortably above the
// p99 latency of cloud-side balena CLI calls observed in practice while still
// surfacing a clean timeout error to the LLM caller in pathological cases.
const defaultExecTimeout = 60 * time.Second

// ServerConfig holds runtime configuration shared across tool handlers.
type ServerConfig struct {
	DryRun bool

	// ExecTimeout is the per-call wall-clock cap for the underlying balena CLI
	// subprocess. Populated from the BALENAMCP_EXEC_TIMEOUT env var (seconds)
	// at SetupServer time; defaults to defaultExecTimeout when unset/invalid.
	ExecTimeout time.Duration

	// RequireConfirm, when true, forces every destructive tool to receive
	// confirm:true in its arguments before it will run. Acts as a safety
	// belt for MCP clients that don't honor the destructiveHint annotation
	// (or for shared deployments where you don't trust every connected
	// agent). Populated from BALENAMCP_REQUIRE_CONFIRM at SetupServer time.
	RequireConfirm bool
}

var Config = ServerConfig{}

// loadConfigFromEnv reads server tuning from env vars. Called once from
// SetupServer; safe to re-invoke from tests when env state changes.
func loadConfigFromEnv() {
	Config.ExecTimeout = loadExecTimeoutFromEnv()
	Config.RequireConfirm = loadRequireConfirmFromEnv()
}

// loadExecTimeoutFromEnv parses BALENAMCP_EXEC_TIMEOUT (seconds). Invalid or
// non-positive values fall back to defaultExecTimeout with a stderr warning.
func loadExecTimeoutFromEnv() time.Duration {
	v := os.Getenv("BALENAMCP_EXEC_TIMEOUT")
	if v == "" {
		return defaultExecTimeout
	}
	secs, err := strconv.Atoi(v)
	if err != nil || secs <= 0 {
		fmt.Fprintf(os.Stderr,
			"BALENAMCP_EXEC_TIMEOUT=%q is not a positive integer; using default %s\n",
			v, defaultExecTimeout)
		return defaultExecTimeout
	}
	return time.Duration(secs) * time.Second
}

// loadRequireConfirmFromEnv parses BALENAMCP_REQUIRE_CONFIRM as a Go bool
// literal (true/false/1/0/T/F/…). Unset or unparseable values default to
// false; an unparseable value logs a stderr warning.
func loadRequireConfirmFromEnv() bool {
	v := os.Getenv("BALENAMCP_REQUIRE_CONFIRM")
	if v == "" {
		return false
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"BALENAMCP_REQUIRE_CONFIRM=%q is not a boolean; defaulting to off\n", v)
		return false
	}
	return b
}

// executeCommand shells out to the balena CLI (or pretends to, in dry-run
// mode). The ctx carries both client-side cancellation (the MCP framework
// gives us the handler's context) and the per-call timeout — whichever fires
// first kills the subprocess.
//
// In dry-run mode the rendered command is returned verbatim so tests and
// inspection can verify the argv shape without hitting balenaCloud.
func executeCommand(ctx context.Context, args []string) (string, error) {
	if Config.DryRun {
		cmdStr := "balena " + strings.Join(args, " ")
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would execute: %s\n", cmdStr)
		return fmt.Sprintf("[DRY RUN] %s", cmdStr), nil
	}

	timeout := Config.ExecTimeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "balena", args...)
	output, err := cmd.CombinedOutput()

	// Distinguish "we ran out of time" from "the CLI itself failed". The
	// timeout case is the one a caller can recover from by chunking work
	// differently (e.g., not asking for --tail); the CLI-error case usually
	// just needs the stderr surfaced to the user.
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "", fmt.Errorf("balena CLI timed out after %s (set BALENAMCP_EXEC_TIMEOUT to override)", timeout)
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		return "", fmt.Errorf("balena CLI cancelled by caller")
	}
	if err != nil {
		return "", fmt.Errorf("balena CLI error: %v\n%s", err, string(output))
	}
	return string(output), nil
}

// runCmd is the standard exit point of a tool handler: run the argv with the
// handler's context, return the CLI output as tool text or a structured tool
// error.
func runCmd(ctx context.Context, args []string) (*mcp.CallToolResult, error) {
	out, err := executeCommand(ctx, args)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(out), nil
}

// runCmdAllowingBenignError runs the command like runCmd, but if the CLI
// returns a non-zero exit whose stdout contains `benignMarker`, the call is
// treated as a successful empty-state result rather than an error.
//
// Motivation: `balena tag list` exits 1 with the stdout "No tags found" when a
// fleet/device/release simply has no tags. Empty-list is not an error condition
// from an agent's point of view, but the CLI's exit code says otherwise. We
// surface the benign case as success while still propagating actual failures
// (auth, network, malformed identifiers) unchanged.
func runCmdAllowingBenignError(ctx context.Context, args []string, benignMarker string) (*mcp.CallToolResult, error) {
	out, err := executeCommand(ctx, args)
	if err == nil {
		return mcp.NewToolResultText(out), nil
	}
	if strings.Contains(err.Error(), benignMarker) {
		return mcp.NewToolResultText(benignMarker), nil
	}
	return mcp.NewToolResultError(err.Error()), nil
}

// appendBoolFlag appends `cliFlag` to flags if the named bool arg is true.
func appendBoolFlag(flags []string, r mcp.CallToolRequest, name, cliFlag string) []string {
	if r.GetBool(name, false) {
		flags = append(flags, cliFlag)
	}
	return flags
}

// appendStringFlag appends `cliFlag value` to flags if the named string arg is non-empty.
func appendStringFlag(flags []string, r mcp.CallToolRequest, name, cliFlag string) []string {
	if v := r.GetString(name, ""); v != "" {
		flags = append(flags, cliFlag, v)
	}
	return flags
}

// pickResource enforces that exactly one of the given string args is set and
// returns its CLI flag form (--fleet/--device/--release). Used by tag-* and env-* tools.
func pickResource(r mcp.CallToolRequest, keys ...string) ([]string, *mcp.CallToolResult) {
	var found string
	var value string
	for _, k := range keys {
		if v := r.GetString(k, ""); v != "" {
			if found != "" {
				return nil, mcp.NewToolResultError(
					fmt.Sprintf("specify exactly one of: %s", strings.Join(keys, ", ")))
			}
			found = k
			value = v
		}
	}
	if found == "" {
		return nil, mcp.NewToolResultError(
			fmt.Sprintf("one of these args is required: %s", strings.Join(keys, ", ")))
	}
	if e := rejectFlagShape(value, found); e != nil {
		return nil, e
	}
	return []string{"--" + found, value}, nil
}

// rejectFlagShape blocks identifier strings that start with "-", which the
// balena CLI would otherwise mis-parse as a flag. Applies to UUIDs, slugs,
// commit hashes, env var names, service names, and tag keys — none of which
// legitimately start with a dash. Free-form values (tag values, env values)
// are intentionally not validated through this helper.
func rejectFlagShape(v, what string) *mcp.CallToolResult {
	if strings.HasPrefix(v, "-") {
		return mcp.NewToolResultError(
			fmt.Sprintf("%q is not a valid %s: identifiers cannot start with '-'", v, what))
	}
	return nil
}

// requireIdentifier wraps RequireString with the flag-shape guard. Returns
// (value, nil) on success or ("", errResult) when the arg is missing or
// malformed; callsites just propagate errResult to the client.
func requireIdentifier(r mcp.CallToolRequest, key, what string) (string, *mcp.CallToolResult) {
	v, err := r.RequireString(key)
	if err != nil {
		return "", mcp.NewToolResultError(err.Error())
	}
	if e := rejectFlagShape(v, what); e != nil {
		return "", e
	}
	return v, nil
}

// getIdentifier is the optional-arg companion to requireIdentifier. Returns
// ("", nil) when the arg is absent and (value, errResult) when present but
// flag-shaped.
func getIdentifier(r mcp.CallToolRequest, key, what string) (string, *mcp.CallToolResult) {
	v := r.GetString(key, "")
	if v == "" {
		return "", nil
	}
	if e := rejectFlagShape(v, what); e != nil {
		return "", e
	}
	return v, nil
}

// Version is the application version reported in the MCP `serverInfo` block
// and visible to clients on initialize. Overridable at build time via:
//
//	go build -ldflags='-X github.com/schubydoo/balenamcp/server.Version=v1.2.3' .
//
// Goreleaser populates this with the release tag on tagged builds; unset
// builds (local `go build`, `go install` from main) report "dev".
var Version = "dev"

// SetupServer wires up every tool and returns the MCP server ready to serve over stdio.
func SetupServer() *server.MCPServer {
	loadConfigFromEnv()

	srv := server.NewMCPServer(
		"BalenaMCP",
		Version,
		server.WithLogging(),
		server.WithRecovery(),
		server.WithToolCapabilities(true),
		// Static set of workflow prompts; we never mutate the list at runtime,
		// so listChanged notifications are not needed.
		server.WithPromptCapabilities(false),
		// Read-only balena state resources. We don't support subscriptions or
		// runtime list changes, so both capability flags are off.
		server.WithResourceCapabilities(false, false),
	)

	registerReadOnlyTools(srv)
	registerMutatingTools(srv)
	registerPrompts(srv)
	registerResources(srv)

	return srv
}

// ----- read-only tools ----------------------------------------------------

// readOnly applies the annotation pair for a tool that does not mutate state.
// mcp-go's NewTool default sets DestructiveHint=true, so we have to clear it
// explicitly — passing just ReadOnlyHint(true) would otherwise leave a tool
// flagged as both read-only and destructive.
func readOnly(t *mcp.Tool) {
	mcp.WithReadOnlyHintAnnotation(true)(t)
	mcp.WithDestructiveHintAnnotation(false)(t)
}

// requireConfirm enforces the BALENAMCP_REQUIRE_CONFIRM gate at the top of
// every destructive handler. When the gate is off this is a no-op; when on,
// the caller must pass confirm:true in arguments or the handler refuses to
// run. Returns nil on success or a structured error result to propagate.
func requireConfirm(r mcp.CallToolRequest) *mcp.CallToolResult {
	if !Config.RequireConfirm {
		return nil
	}
	if r.GetBool("confirm", false) {
		return nil
	}
	return mcp.NewToolResultError(
		"this server requires explicit confirmation for destructive tools: " +
			"set BALENAMCP_REQUIRE_CONFIRM=0 on the server, or pass confirm:true in the tool arguments to acknowledge the change")
}

// destructive is the annotation pair for tools that change cloud or device
// state. Also injects a `confirm` schema field so LLM clients can discover
// the BALENAMCP_REQUIRE_CONFIRM gate without reading source.
func destructive(t *mcp.Tool) {
	mcp.WithReadOnlyHintAnnotation(false)(t)
	mcp.WithDestructiveHintAnnotation(true)(t)
	mcp.WithBoolean("confirm",
		mcp.Description("Set to true to acknowledge the destructive operation. "+
			"Required only when the server is started with BALENAMCP_REQUIRE_CONFIRM=1; "+
			"ignored otherwise."))(t)
}

// guardDestructive runs the standard destructive-tool preamble in one call:
// the BALENAMCP_REQUIRE_CONFIRM gate, then a flag-shape-guarded lookup of
// the named identifier argument. On success returns (id, nil); on either
// guard failing returns ("", errResult) for the caller to propagate.
//
// Use only for tools whose canonical input is a single identifier (device
// UUID, fleet slug, release ID). Tools with multi-identifier arguments
// (tag-set/tag-rm, env-set/env-rm) still call requireConfirm + pickResource
// directly because their identifier-resolution shape doesn't match.
func guardDestructive(r mcp.CallToolRequest, idKey, what string) (string, *mcp.CallToolResult) {
	if errRes := requireConfirm(r); errRes != nil {
		return "", errRes
	}
	return requireIdentifier(r, idKey, what)
}

// registerReadOnlyTools wires every read-only tool onto srv. Kept as a thin
// dispatcher so each per-category helper stays under gocyclo's complexity
// ceiling (15) and so an LLM-assisted reader can grep for the relevant
// register* function instead of scrolling through 200+ lines of tool defs.
func registerReadOnlyTools(srv *server.MCPServer) {
	registerReadOnlyIdentity(srv)
	registerReadOnlyFleets(srv)
	registerReadOnlyDevices(srv)
	registerReadOnlyReleases(srv)
	registerReadOnlyTagsEnvs(srv)
	registerReadOnlyAccount(srv)
}

// registerReadOnlyIdentity: version, whoami.
func registerReadOnlyIdentity(srv *server.MCPServer) {

	// version --------------------------------------------------------------
	srv.AddTool(mcp.NewTool("version",
		mcp.WithDescription("Display the version of the underlying balena CLI."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd(ctx, []string{"version"})
	})

	// whoami ---------------------------------------------------------------
	srv.AddTool(mcp.NewTool("whoami",
		mcp.WithDescription("Show account info for the currently authenticated balenaCloud user."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd(ctx, []string{"whoami"})
	})
}

// registerReadOnlyFleets: fleet-list, fleet-info.
func registerReadOnlyFleets(srv *server.MCPServer) {

	// fleet-list -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-list",
		mcp.WithDescription("List all fleets the current user can access."),
		readOnly,
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := []string{"fleet", "list"}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// fleet-info -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-info",
		mcp.WithDescription("Show detailed information about a single fleet (name, slug, device type, pinned release)."),
		readOnly,
		mcp.WithString("fleet", mcp.Required(),
			mcp.Description("Fleet name or org/fleet slug (e.g. 'MyFleet' or 'myorg/myfleet').")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := requireIdentifier(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"fleet", fleet}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})
}

// registerReadOnlyDevices: device-list, device-info, device-logs,
// device-type-list, os-versions.
func registerReadOnlyDevices(srv *server.MCPServer) {

	// device-list ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-list",
		mcp.WithDescription("List all devices, optionally filtered by fleet."),
		readOnly,
		mcp.WithString("fleet", mcp.Description("Restrict to devices in this fleet (name or slug).")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := getIdentifier(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "list"}
		if fleet != "" {
			args = append(args, "--fleet", fleet)
		}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// device-info ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-info",
		mcp.WithDescription("Show detailed information about a single device (status, IP, supervisor version, running release, etc.)."),
		readOnly,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID (short or full).")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", uuid}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// device-logs ----------------------------------------------------------
	//
	// `tail` is deliberately NOT exposed. The balena CLI supports --tail to
	// stream logs indefinitely, but the MCP transport is request/response —
	// a streaming response would block the conversation until our 60s exec
	// timeout fires, returning a partial dump or a timeout error. Neither is
	// useful for an agent. Non-tail mode (the default) returns recent
	// historical logs and exits cleanly, which is what an agent actually
	// wants when it asks "what's going on with this device?". A defensive
	// guard below catches a non-compliant client that sends tail:true anyway.
	srv.AddTool(mcp.NewTool("device-logs",
		mcp.WithDescription("Show recent logs for a device and exit. Streaming (--tail) is not supported over the MCP transport — for continuous monitoring run `balena device logs <uuid> --tail` directly in a shell."),
		readOnly,
		mcp.WithString("device", mcp.Required(),
			mcp.Description("Device UUID, IP, or .local address.")),
		mcp.WithString("service", mcp.Description("Only show logs from this service name.")),
		mcp.WithBoolean("system", mcp.Description("Only show system (host) logs.")),
		mcp.WithNumber("max_retry", mcp.Description("Max reconnection attempts on connection loss; 0 disables auto reconnect.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if r.GetBool("tail", false) {
			return mcp.NewToolResultError(
				"device-logs does not support streaming over MCP (tail:true). " +
					"Omit tail to fetch recent historical logs; for continuous monitoring " +
					"run 'balena device logs <uuid> --tail' directly in a shell."), nil
		}
		device, errRes := requireIdentifier(r, "device", "device UUID or address")
		if errRes != nil {
			return errRes, nil
		}
		service, errRes := getIdentifier(r, "service", "service name")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "logs", device}
		if service != "" {
			args = append(args, "--service", service)
		}
		args = appendBoolFlag(args, r, "system", "--system")
		if v := r.GetInt("max_retry", -1); v >= 0 {
			args = append(args, "--max-retry", fmt.Sprintf("%d", v))
		}
		return runCmd(ctx, args)
	})

	// device-type-list -----------------------------------------------------
	srv.AddTool(mcp.NewTool("device-type-list",
		mcp.WithDescription("List supported balena device types (e.g. 'raspberrypi3', 'intel-nuc')."),
		readOnly,
		mcp.WithBoolean("all", mcp.Description("Include device types no longer supported by balena.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := []string{"device-type", "list"}
		args = appendBoolFlag(args, r, "all", "--all")
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// os-versions ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("os-versions",
		mcp.WithDescription("Show available balenaOS versions for a given device type."),
		readOnly,
		mcp.WithString("type", mcp.Required(),
			mcp.Description("Device type slug (e.g. 'raspberrypi4').")),
		mcp.WithBoolean("esr", mcp.Description("Select balenaOS ESR (Extended Support Release) versions.")),
		mcp.WithBoolean("include_draft", mcp.Description("Include pre-release balenaOS versions.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		deviceType, errRes := requireIdentifier(r, "type", "device type slug")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"os", "versions", deviceType}
		args = appendBoolFlag(args, r, "esr", "--esr")
		args = appendBoolFlag(args, r, "include_draft", "--include-draft")
		return runCmd(ctx, args)
	})
}

// registerReadOnlyReleases: release-list, release-info, release-asset-list.
func registerReadOnlyReleases(srv *server.MCPServer) {

	// release-list ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("release-list",
		mcp.WithDescription("List releases of a fleet."),
		readOnly,
		mcp.WithString("fleet", mcp.Required(),
			mcp.Description("Fleet name or org/fleet slug.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := requireIdentifier(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"release", "list", fleet}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// release-info ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("release-info",
		mcp.WithDescription("Get info for a single release."),
		readOnly,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit (full or short) or numeric release ID.")),
		mcp.WithBoolean("composition", mcp.Description("Return the release docker-compose composition instead of metadata.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := requireIdentifier(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"release", id}
		args = appendBoolFlag(args, r, "composition", "--composition")
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// release-asset-list ---------------------------------------------------
	srv.AddTool(mcp.NewTool("release-asset-list",
		mcp.WithDescription("List all assets (binary attachments) for a release."),
		readOnly,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit or numeric release ID.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := requireIdentifier(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"release-asset", "list", id}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})
}

// registerReadOnlyTagsEnvs: tag-list, env-list.
func registerReadOnlyTagsEnvs(srv *server.MCPServer) {

	// tag-list -------------------------------------------------------------
	srv.AddTool(mcp.NewTool("tag-list",
		mcp.WithDescription("List tags for a fleet, device, or release. Specify exactly one of: fleet, device, release."),
		readOnly,
		mcp.WithString("fleet", mcp.Description("Fleet name or slug to list tags for.")),
		mcp.WithString("device", mcp.Description("Device UUID to list tags for.")),
		mcp.WithString("release", mcp.Description("Release ID or commit to list tags for.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		flag, errRes := pickResource(r, "fleet", "device", "release")
		if errRes != nil {
			return errRes, nil
		}
		args := append([]string{"tag", "list"}, flag...)
		// balena CLI exits 1 with "No tags found" for an empty tag set on
		// the target. That's an empty-state response, not a failure — remap.
		return runCmdAllowingBenignError(ctx, args, "No tags found")
	})

	// env-list -------------------------------------------------------------
	srv.AddTool(mcp.NewTool("env-list",
		mcp.WithDescription("List environment/config variables for a fleet or device, optionally narrowed to a service."),
		readOnly,
		mcp.WithString("fleet", mcp.Description("Fleet name or slug. Mutually exclusive with 'device'.")),
		mcp.WithString("device", mcp.Description("Device UUID. Mutually exclusive with 'fleet'.")),
		mcp.WithString("service", mcp.Description("Restrict to variables of this service. Cannot combine with 'config'.")),
		mcp.WithBoolean("config", mcp.Description("Show config variables only. Cannot combine with 'service'.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		flag, errRes := pickResource(r, "fleet", "device")
		if errRes != nil {
			return errRes, nil
		}
		service, errRes := getIdentifier(r, "service", "service name")
		if errRes != nil {
			return errRes, nil
		}
		// Upstream balena CLI rejects --config + --service together; surface
		// that earlier with a clearer message instead of forwarding both and
		// letting the CLI complain about a flag combination the user wasn't
		// thinking of in those terms.
		if service != "" && r.GetBool("config", false) {
			return mcp.NewToolResultError(
				"'service' and 'config' are mutually exclusive (config variables don't belong to a specific service)"), nil
		}
		args := append([]string{"env", "list"}, flag...)
		if service != "" {
			args = append(args, "--service", service)
		}
		args = appendBoolFlag(args, r, "config", "--config")
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})
}

// registerReadOnlyAccount: organization-list, ssh-key-list, api-key-list.
func registerReadOnlyAccount(srv *server.MCPServer) {

	// organization-list ----------------------------------------------------
	srv.AddTool(mcp.NewTool("organization-list",
		mcp.WithDescription("List all balenaCloud organizations the current user belongs to."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd(ctx, []string{"organization", "list"})
	})

	// ssh-key-list ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("ssh-key-list",
		mcp.WithDescription("List SSH keys registered in balenaCloud for the current user."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd(ctx, []string{"ssh-key", "list"})
	})

	// api-key-list ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("api-key-list",
		mcp.WithDescription("List balenaCloud API keys for the current user or a specific fleet."),
		readOnly,
		mcp.WithString("fleet", mcp.Description("Show API keys for this fleet instead of the current user.")),
		mcp.WithBoolean("user", mcp.Description("Show only user-named API keys.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := getIdentifier(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"api-key", "list"}
		if fleet != "" {
			args = append(args, "--fleet", fleet)
		}
		args = appendBoolFlag(args, r, "user", "--user")
		return runCmd(ctx, args)
	})
}

// ----- mutating tools -----------------------------------------------------

// registerMutatingTools wires every mutating tool onto srv. Thin dispatcher,
// per registerReadOnlyTools above. Each helper stays well under gocyclo's
// complexity ceiling.
func registerMutatingTools(srv *server.MCPServer) {
	registerMutatingDeviceLifecycle(srv)
	registerMutatingPins(srv)
	registerMutatingTags(srv)
	registerMutatingEnvs(srv)
}

// registerMutatingDeviceLifecycle: device-reboot, device-restart,
// device-shutdown, device-purge.
func registerMutatingDeviceLifecycle(srv *server.MCPServer) {
	// device-reboot --------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-reboot",
		mcp.WithDescription("Remotely reboot a device. The device must be online."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID to reboot.")),
		mcp.WithBoolean("force", mcp.Description("Force reboot even if updates are in progress.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "reboot", uuid}
		args = appendBoolFlag(args, r, "force", "--force")
		return runCmd(ctx, args)
	})

	// device-restart -------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-restart",
		mcp.WithDescription("Restart application containers on a device (does NOT reboot the device itself). Optionally restart only a specific service."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. Multiple devices can be given as a comma-separated list (no spaces).")),
		mcp.WithString("service", mcp.Description("Service name(s) to restart, comma-separated. Omit to restart all services.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		service, errRes := getIdentifier(r, "service", "service name")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "restart", uuid}
		if service != "" {
			args = append(args, "--service", service)
		}
		return runCmd(ctx, args)
	})

	// device-shutdown ------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-shutdown",
		mcp.WithDescription("Remotely shut down a device. The device must be online; it will not come back without physical power-cycling."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID to shut down.")),
		mcp.WithBoolean("force", mcp.Description("Force shutdown even if updates are in progress.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "shutdown", uuid}
		args = appendBoolFlag(args, r, "force", "--force")
		return runCmd(ctx, args)
	})

	// device-purge ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-purge",
		mcp.WithDescription("Clear a device's /data directory. Persistent app data will be lost."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. Multiple devices can be given as a comma-separated list (no spaces).")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "purge", uuid})
	})
}

// registerMutatingPins: device-pin, device-track-fleet, fleet-pin,
// release-finalize. Grouped because they all modify the device/fleet→release
// binding (pin in, pin out, fleet pin, promote draft to final).
func registerMutatingPins(srv *server.MCPServer) {

	// device-pin -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-pin",
		mcp.WithDescription("Pin a device to a specific release. If release is omitted, prints the currently pinned release."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID.")),
		mcp.WithString("release", mcp.Description("Release commit to pin the device to. Omit to query the current pin.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		rel, errRes := getIdentifier(r, "release", "release commit")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "pin", uuid}
		if rel != "" {
			args = append(args, rel)
		}
		return runCmd(ctx, args)
	})

	// device-track-fleet ---------------------------------------------------
	// Inverse of device-pin: drops the device-level pin so it resumes
	// tracking whatever the fleet is pinned to. Without this, our pin
	// lifecycle is one-way through the server — once device-pin runs, the
	// only way back is re-pinning to another release. Surfaced as a real
	// gap during the live validation sweep.
	srv.AddTool(mcp.NewTool("device-track-fleet",
		mcp.WithDescription("Drop a device's pinned release and resume tracking the fleet's pinned release. Inverse of device-pin."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID to unpin.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "track-fleet", uuid})
	})

	// fleet-pin ------------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-pin",
		mcp.WithDescription("Pin a fleet to a specific release. If release is omitted, prints the currently pinned release."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet slug (org/fleet).")),
		mcp.WithString("release", mcp.Description("Release commit to pin the fleet to. Omit to query the current pin.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		rel, errRes := getIdentifier(r, "release", "release commit")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"fleet", "pin", fleet}
		if rel != "" {
			args = append(args, rel)
		}
		return runCmd(ctx, args)
	})

	// release-finalize -----------------------------------------------------
	srv.AddTool(mcp.NewTool("release-finalize",
		mcp.WithDescription("Promote a draft release to final. Final releases auto-deploy to tracking devices."),
		destructive,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit or numeric release ID to finalize.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := guardDestructive(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"release", "finalize", id})
	})
}

// registerMutatingTags: tag-set, tag-rm.
func registerMutatingTags(srv *server.MCPServer) {

	// tag-set --------------------------------------------------------------
	srv.AddTool(mcp.NewTool("tag-set",
		mcp.WithDescription("Set (create or update) a tag on a fleet, device, or release. Specify exactly one of: fleet, device, release."),
		destructive,
		mcp.WithString("key", mcp.Required(), mcp.Description("Tag key.")),
		mcp.WithString("value", mcp.Description("Tag value. If omitted, sets an empty-value tag.")),
		mcp.WithString("fleet", mcp.Description("Fleet name or slug to tag.")),
		mcp.WithString("device", mcp.Description("Device UUID to tag.")),
		mcp.WithString("release", mcp.Description("Release ID or commit to tag.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		key, errRes := requireIdentifier(r, "key", "tag key")
		if errRes != nil {
			return errRes, nil
		}
		flag, errRes := pickResource(r, "fleet", "device", "release")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"tag", "set", key}
		// value is free-form; intentionally not flag-shape-guarded.
		if v := r.GetString("value", ""); v != "" {
			args = append(args, v)
		}
		args = append(args, flag...)
		return runCmd(ctx, args)
	})

	// tag-rm ---------------------------------------------------------------
	srv.AddTool(mcp.NewTool("tag-rm",
		mcp.WithDescription("Remove a tag from a fleet, device, or release. Specify exactly one of: fleet, device, release."),
		destructive,
		mcp.WithString("key", mcp.Required(), mcp.Description("Tag key to remove.")),
		mcp.WithString("fleet", mcp.Description("Fleet name or slug.")),
		mcp.WithString("device", mcp.Description("Device UUID.")),
		mcp.WithString("release", mcp.Description("Release ID or commit.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		key, errRes := requireIdentifier(r, "key", "tag key")
		if errRes != nil {
			return errRes, nil
		}
		flag, errRes := pickResource(r, "fleet", "device", "release")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"tag", "rm", key}
		args = append(args, flag...)
		return runCmd(ctx, args)
	})
}

// registerMutatingEnvs: env-set, env-rm.
func registerMutatingEnvs(srv *server.MCPServer) {

	// env-set --------------------------------------------------------------
	srv.AddTool(mcp.NewTool("env-set",
		mcp.WithDescription("Set an env or config variable on a fleet or device, optionally scoped to a service. Specify exactly one of: fleet, device."),
		destructive,
		mcp.WithString("name", mcp.Required(), mcp.Description("Variable name.")),
		mcp.WithString("value", mcp.Description("Variable value. If omitted, the value of a same-named local shell env var is used by the CLI.")),
		mcp.WithString("fleet", mcp.Description("Fleet name or slug.")),
		mcp.WithString("device", mcp.Description("Device UUID.")),
		mcp.WithString("service", mcp.Description("Restrict to this service.")),
		mcp.WithBoolean("quiet", mcp.Description("Suppress warnings.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		name, errRes := requireIdentifier(r, "name", "env var name")
		if errRes != nil {
			return errRes, nil
		}
		flag, errRes := pickResource(r, "fleet", "device")
		if errRes != nil {
			return errRes, nil
		}
		service, errRes := getIdentifier(r, "service", "service name")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"env", "set", name}
		// value is free-form (env values can legitimately contain anything);
		// intentionally not flag-shape-guarded.
		if v := r.GetString("value", ""); v != "" {
			args = append(args, v)
		}
		args = append(args, flag...)
		if service != "" {
			args = append(args, "--service", service)
		}
		args = appendBoolFlag(args, r, "quiet", "--quiet")
		return runCmd(ctx, args)
	})

	// env-rm ---------------------------------------------------------------
	srv.AddTool(mcp.NewTool("env-rm",
		mcp.WithDescription("Remove an env or config variable by its numeric database ID (see env-list). Use --device/--service/--config booleans to disambiguate the variable type."),
		destructive,
		mcp.WithNumber("id", mcp.Required(),
			mcp.Description("Numeric database ID of the variable (from env-list).")),
		mcp.WithBoolean("device", mcp.Description("The variable is a device-scoped variable.")),
		mcp.WithBoolean("service", mcp.Description("The variable is a service-scoped variable.")),
		mcp.WithBoolean("config", mcp.Description("The variable is a config variable.")),
		mcp.WithBoolean("yes", mcp.Description("Skip the interactive confirmation prompt. Must be true for the call to actually delete.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		// env-rm's id is a numeric DB ID — no flag-shape risk.
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		args := []string{"env", "rm", fmt.Sprintf("%d", id)}
		args = appendBoolFlag(args, r, "device", "--device")
		args = appendBoolFlag(args, r, "service", "--service")
		args = appendBoolFlag(args, r, "config", "--config")
		args = appendBoolFlag(args, r, "yes", "--yes")
		return runCmd(ctx, args)
	})
}

package server

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ServerConfig holds runtime configuration shared across tool handlers.
type ServerConfig struct {
	DryRun bool
}

var Config = ServerConfig{}

// executeCommand shells out to the balena CLI (or pretends to, in dry-run mode).
// In dry-run mode the rendered command is returned verbatim so tests/inspection
// can verify the argv shape without hitting balenaCloud.
func executeCommand(args []string) (string, error) {
	if Config.DryRun {
		cmdStr := "balena " + strings.Join(args, " ")
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would execute: %s\n", cmdStr)
		return fmt.Sprintf("[DRY RUN] %s", cmdStr), nil
	}
	cmd := exec.Command("balena", args...)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("balena CLI error: %v\n%s", err, string(output))
	}
	return string(output), nil
}

// runCmd is the standard exit point of a tool handler: run the argv, return
// the CLI output as tool text or a structured tool error.
func runCmd(args []string) (*mcp.CallToolResult, error) {
	out, err := executeCommand(args)
	if err != nil {
		return mcp.NewToolResultError(err.Error()), nil
	}
	return mcp.NewToolResultText(out), nil
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

// SetupServer wires up every tool and returns the MCP server ready to serve over stdio.
func SetupServer() *server.MCPServer {
	srv := server.NewMCPServer(
		"BalenaMCP",
		"0.1.0",
		server.WithLogging(),
		server.WithRecovery(),
		server.WithToolCapabilities(true),
	)

	registerReadOnlyTools(srv)
	registerMutatingTools(srv)

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

// destructive is the annotation pair for tools that change cloud or device state.
func destructive(t *mcp.Tool) {
	mcp.WithReadOnlyHintAnnotation(false)(t)
	mcp.WithDestructiveHintAnnotation(true)(t)
}

func registerReadOnlyTools(srv *server.MCPServer) {

	// version --------------------------------------------------------------
	srv.AddTool(mcp.NewTool("version",
		mcp.WithDescription("Display the version of the underlying balena CLI."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd([]string{"version"})
	})

	// whoami ---------------------------------------------------------------
	srv.AddTool(mcp.NewTool("whoami",
		mcp.WithDescription("Show account info for the currently authenticated balenaCloud user."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd([]string{"whoami"})
	})

	// fleet-list -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-list",
		mcp.WithDescription("List all fleets the current user can access."),
		readOnly,
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := []string{"fleet", "list"}
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(args)
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
		return runCmd(args)
	})

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
		return runCmd(args)
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
		return runCmd(args)
	})

	// device-logs ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-logs",
		mcp.WithDescription("Show logs for a device. By default prints recent logs and exits."),
		readOnly,
		mcp.WithString("device", mcp.Required(),
			mcp.Description("Device UUID, IP, or .local address.")),
		mcp.WithString("service", mcp.Description("Only show logs from this service name.")),
		mcp.WithBoolean("system", mcp.Description("Only show system (host) logs.")),
		mcp.WithBoolean("tail", mcp.Description("Continuously stream new logs (CAUTION: blocks indefinitely).")),
		mcp.WithNumber("max_retry", mcp.Description("Max reconnection attempts on connection loss; 0 disables auto reconnect.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
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
		args = appendBoolFlag(args, r, "tail", "--tail")
		if v := r.GetInt("max_retry", -1); v >= 0 {
			args = append(args, "--max-retry", fmt.Sprintf("%d", v))
		}
		return runCmd(args)
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
		return runCmd(args)
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
		return runCmd(args)
	})

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
		return runCmd(args)
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
		return runCmd(args)
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
		return runCmd(args)
	})

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
		return runCmd(args)
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
		args := append([]string{"env", "list"}, flag...)
		if service != "" {
			args = append(args, "--service", service)
		}
		args = appendBoolFlag(args, r, "config", "--config")
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(args)
	})

	// organization-list ----------------------------------------------------
	srv.AddTool(mcp.NewTool("organization-list",
		mcp.WithDescription("List all balenaCloud organizations the current user belongs to."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd([]string{"organization", "list"})
	})

	// ssh-key-list ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("ssh-key-list",
		mcp.WithDescription("List SSH keys registered in balenaCloud for the current user."),
		readOnly,
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return runCmd([]string{"ssh-key", "list"})
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
		return runCmd(args)
	})
}

// ----- mutating tools -----------------------------------------------------

func registerMutatingTools(srv *server.MCPServer) {
	// device-reboot --------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-reboot",
		mcp.WithDescription("Remotely reboot a device. The device must be online."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID to reboot.")),
		mcp.WithBoolean("force", mcp.Description("Force reboot even if updates are in progress.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "reboot", uuid}
		args = appendBoolFlag(args, r, "force", "--force")
		return runCmd(args)
	})

	// device-restart -------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-restart",
		mcp.WithDescription("Restart application containers on a device (does NOT reboot the device itself). Optionally restart only a specific service."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. Multiple devices can be given as a comma-separated list (no spaces).")),
		mcp.WithString("service", mcp.Description("Service name(s) to restart, comma-separated. Omit to restart all services.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
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
		return runCmd(args)
	})

	// device-shutdown ------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-shutdown",
		mcp.WithDescription("Remotely shut down a device. The device must be online; it will not come back without physical power-cycling."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID to shut down.")),
		mcp.WithBoolean("force", mcp.Description("Force shutdown even if updates are in progress.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "shutdown", uuid}
		args = appendBoolFlag(args, r, "force", "--force")
		return runCmd(args)
	})

	// device-purge ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-purge",
		mcp.WithDescription("Clear a device's /data directory. Persistent app data will be lost."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. Multiple devices can be given as a comma-separated list (no spaces).")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd([]string{"device", "purge", uuid})
	})

	// device-pin -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-pin",
		mcp.WithDescription("Pin a device to a specific release. If release is omitted, prints the currently pinned release."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID.")),
		mcp.WithString("release", mcp.Description("Release commit to pin the device to. Omit to query the current pin.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
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
		return runCmd(args)
	})

	// fleet-pin ------------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-pin",
		mcp.WithDescription("Pin a fleet to a specific release. If release is omitted, prints the currently pinned release."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet slug (org/fleet).")),
		mcp.WithString("release", mcp.Description("Release commit to pin the fleet to. Omit to query the current pin.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := requireIdentifier(r, "fleet", "fleet slug")
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
		return runCmd(args)
	})

	// release-finalize -----------------------------------------------------
	srv.AddTool(mcp.NewTool("release-finalize",
		mcp.WithDescription("Promote a draft release to final. Final releases auto-deploy to tracking devices."),
		destructive,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit or numeric release ID to finalize.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := requireIdentifier(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd([]string{"release", "finalize", id})
	})

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
		return runCmd(args)
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
		return runCmd(args)
	})

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
		return runCmd(args)
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
		return runCmd(args)
	})
}

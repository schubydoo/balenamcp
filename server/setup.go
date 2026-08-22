package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

	// AssetDir is the single directory balenamcp may read from or write to on
	// the host. Populated from BALENAMCP_ASSET_DIR at SetupServer time and
	// stored fully resolved (absolute, symlinks expanded). Empty means the
	// filesystem-touching tools are disabled entirely, which is the default:
	// every other tool in the server is a pure cloud call, so host filesystem
	// access is opt-in rather than something an operator inherits by upgrading.
	AssetDir string
}

var Config = ServerConfig{}

// loadConfigFromEnv reads server tuning from env vars. Called once from
// SetupServer; safe to re-invoke from tests when env state changes.
func loadConfigFromEnv() {
	Config.ExecTimeout = loadExecTimeoutFromEnv()
	Config.RequireConfirm = loadRequireConfirmFromEnv()
	Config.AssetDir = loadAssetDirFromEnv()
}

// loadAssetDirFromEnv reads BALENAMCP_ASSET_DIR and resolves it to an
// absolute, symlink-free path. An unset value disables the filesystem tools.
// A value that cannot be resolved (missing directory, not a directory) also
// disables them, with a stderr warning: failing closed is the only safe
// reading of a misconfigured filesystem boundary.
func loadAssetDirFromEnv() string {
	v := os.Getenv("BALENAMCP_ASSET_DIR")
	if v == "" {
		return ""
	}
	abs, err := filepath.Abs(v)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"BALENAMCP_ASSET_DIR=%q cannot be resolved (%v); filesystem tools stay disabled\n", v, err)
		return ""
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"BALENAMCP_ASSET_DIR=%q does not exist or is unreadable (%v); filesystem tools stay disabled\n", v, err)
		return ""
	}
	info, err := os.Stat(real)
	if err != nil || !info.IsDir() {
		fmt.Fprintf(os.Stderr,
			"BALENAMCP_ASSET_DIR=%q is not a directory; filesystem tools stay disabled\n", v)
		return ""
	}
	return real
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
// execBinary is the CLI executable invoked for every tool. It is a package
// variable solely so tests can point the real-exec path at a stand-in (e.g.
// `cat`, which echoes stdin) to verify stdin wiring without a `balena` install.
// Production never changes it.
var execBinary = "balena"

func executeCommand(ctx context.Context, args []string) (string, error) {
	return executeCommandStdin(ctx, args, "")
}

// executeCommandStdin is executeCommand plus an optional stdin payload fed to
// the subprocess. Most tools build a complete argv and need no stdin (stdin=""
// behaves exactly like executeCommand). The exception is `device ssh`, whose
// one-shot command is delivered over stdin rather than argv — this keeps the
// "never interpolate input into a command line" guarantee (the command never
// touches argv or a shell) while letting us append the explicit `exit` the
// remote shell needs to terminate.
func executeCommandStdin(ctx context.Context, args []string, stdin string) (string, error) {
	if Config.DryRun {
		cmdStr := "balena " + strings.Join(args, " ")
		if stdin != "" {
			// strconv.Quote collapses the payload (incl. its newline + exit)
			// onto one line so the rendered command stays greppable.
			cmdStr += " <<<" + strconv.Quote(stdin)
		}
		fmt.Fprintf(os.Stderr, "[DRY RUN] Would execute: %s\n", cmdStr)
		return fmt.Sprintf("[DRY RUN] %s", cmdStr), nil
	}

	timeout := Config.ExecTimeout
	if timeout <= 0 {
		timeout = defaultExecTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, execBinary, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
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

// runCmdStdin is runCmd with an stdin payload — the exit point for tools that
// feed their input over stdin (currently just device-ssh).
func runCmdStdin(ctx context.Context, args []string, stdin string) (*mcp.CallToolResult, error) {
	out, err := executeCommandStdin(ctx, args, stdin)
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

// rejectMultiTarget blocks comma-separated values on arguments whose balena
// CLI counterpart would split on "," and act on every element.
//
// Several CLI commands (`device purge`, `restart`, `rm`, `deactivate`, `move`,
// `start-service`, `stop-service`) accept lists, so one tool call could reach
// an unbounded number of devices behind a single guardDestructive check —
// `device purge` would wipe /data on all of them, permanently. balenamcp
// constrains those arguments to one device per call: the blast radius stays
// bounded, and each action is individually visible to whoever is auditing the
// agent. An agent that genuinely needs N targets loops, which is cheap.
//
// The one deliberate exception is api-key-revoke, whose CLI command is
// inherently list-shaped and whose targets are credentials rather than
// devices, so per-call batching buys no safety. See README.
func rejectMultiTarget(v, what string) *mcp.CallToolResult {
	if strings.Contains(v, ",") {
		return mcp.NewToolResultError(fmt.Sprintf(
			"%q lists more than one %s: this tool accepts a single target per call, "+
				"so call it once per %s", v, what, what))
	}
	return nil
}

// requireSingleTarget is requireIdentifier plus the multi-target guard.
func requireSingleTarget(r mcp.CallToolRequest, key, what string) (string, *mcp.CallToolResult) {
	v, errRes := requireIdentifier(r, key, what)
	if errRes != nil {
		return "", errRes
	}
	if e := rejectMultiTarget(v, what); e != nil {
		return "", e
	}
	return v, nil
}

// getSingleTarget is the optional-arg companion to requireSingleTarget:
// getIdentifier plus the multi-target guard. Returns ("", nil) when absent.
func getSingleTarget(r mcp.CallToolRequest, key, what string) (string, *mcp.CallToolResult) {
	v, errRes := getIdentifier(r, key, what)
	if errRes != nil {
		return "", errRes
	}
	if v == "" {
		return "", nil
	}
	if e := rejectMultiTarget(v, what); e != nil {
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

// withinRoot reports whether p is root itself or lies underneath it. Uses
// filepath.Rel rather than a string prefix so that a sibling directory sharing
// a name prefix (/srv/assets-evil vs /srv/assets) is not treated as inside.
func withinRoot(root, p string) bool {
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
}

// evalExistingPrefix resolves symlinks on the longest existing ancestor of p
// and rejoins the not-yet-existing remainder. Needed because
// filepath.EvalSymlinks fails outright on a path that does not exist yet,
// which is the normal case for a download target.
func evalExistingPrefix(p string) (string, error) {
	rest := ""
	cur := p
	for {
		real, err := filepath.EvalSymlinks(cur)
		if err == nil {
			if rest == "" {
				return real, nil
			}
			return filepath.Join(real, rest), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			// Walked to the filesystem root without finding anything that
			// exists; nothing can be resolved, so report the cleaned path.
			return p, nil
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
}

// resolveAssetPath maps a caller-supplied path onto an absolute path confined
// to Config.AssetDir, or returns a structured error explaining the refusal.
//
// This is the whole security boundary for the filesystem-touching tools. Our
// argv-slice construction prevents shell injection but says nothing about path
// traversal: without this, an agent choosing `file_path` on an upload could
// exfiltrate any file the server process can read, and one choosing `output`
// on a download could write anywhere it can write. The rules are:
//
//   - the tools are off unless BALENAMCP_ASSET_DIR is set;
//   - the caller's path is always relative to that root — absolute paths,
//     Windows volume names and leading dashes are refused outright;
//   - the joined path must stay inside the root after cleaning, which rejects
//     ".." traversal;
//   - and it must still be inside after symlink resolution, which rejects a
//     symlink planted inside the root that points out of it.
func resolveAssetPath(raw, what string) (string, *mcp.CallToolResult) {
	root := Config.AssetDir
	if root == "" {
		return "", mcp.NewToolResultError(
			"filesystem access is disabled on this server: start balenamcp with " +
				"BALENAMCP_ASSET_DIR=/some/directory to enable the tools that read or " +
				"write local files. All paths are then relative to that directory.")
	}
	if raw == "" {
		return "", mcp.NewToolResultError(fmt.Sprintf("%s is required", what))
	}
	if e := rejectFlagShape(raw, what); e != nil {
		return "", e
	}
	if filepath.IsAbs(raw) || filepath.VolumeName(raw) != "" {
		return "", mcp.NewToolResultError(fmt.Sprintf(
			"%q is not a valid %s: paths must be relative to BALENAMCP_ASSET_DIR, not absolute", raw, what))
	}
	// Canonicalize the root here rather than trusting the caller to have done
	// it. loadAssetDirFromEnv already resolves it, but the containment checks
	// below compare against a symlink-resolved path, so both sides must be in
	// the same form or every call fails as a bogus symlink escape. Platforms
	// rewrite paths more than you would expect: macOS maps /var to
	// /private/var, and Windows expands 8.3 short names (RUNNER~1 ->
	// runneradmin).
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		return "", mcp.NewToolResultError(fmt.Sprintf(
			"BALENAMCP_ASSET_DIR (%q) cannot be resolved: %v", root, err))
	}
	full := filepath.Join(realRoot, raw)
	if !withinRoot(realRoot, full) {
		return "", mcp.NewToolResultError(fmt.Sprintf(
			"%q escapes BALENAMCP_ASSET_DIR: %s must stay inside the configured directory", raw, what))
	}
	real, err := evalExistingPrefix(full)
	if err != nil {
		return "", mcp.NewToolResultError(fmt.Sprintf(
			"%q cannot be resolved: %v", raw, err))
	}
	if !withinRoot(realRoot, real) {
		return "", mcp.NewToolResultError(fmt.Sprintf(
			"%q resolves outside BALENAMCP_ASSET_DIR through a symbolic link", raw))
	}
	return full, nil
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
	registerReadOnlyDiagnostics(srv)
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

	// device-public-url ----------------------------------------------------
	srv.AddTool(mcp.NewTool("device-public-url",
		mcp.WithDescription("Read a device's public device URL, or check whether the URL is enabled. Read-only: use device-public-url-set to turn the URL on or off."),
		readOnly,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
		mcp.WithBoolean("status",
			mcp.Description("true to report whether the public URL is enabled instead of printing the URL itself.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "public-url", uuid}
		args = appendBoolFlag(args, r, "status", "--status")
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
		mcp.WithBoolean("composition", mcp.Description("Return the release docker-compose composition instead of metadata. Mutually exclusive with json: the composition is emitted as YAML and the CLI silently ignores --json alongside it.")),
		mcp.WithBoolean("json", mcp.Description("Return JSON instead of a text table. Not combinable with composition.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := requireIdentifier(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		// The CLI's --composition path prints YAML and returns nothing for
		// oclif's --json machinery to serialize, so --json is silently
		// ignored — the caller asks for JSON and receives YAML. Reject the
		// combination instead of letting an agent misparse the output.
		if r.GetBool("composition", false) && r.GetBool("json", false) {
			return mcp.NewToolResultError(
				"the 'composition' and 'json' options are mutually exclusive: the composition is always emitted as YAML"), nil
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

	// ssh-key-info ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("ssh-key-info",
		mcp.WithDescription("Show a single SSH key registered in balenaCloud, by its numeric ID. Get IDs from ssh-key-list."),
		readOnly,
		mcp.WithNumber("id", mcp.Required(),
			mcp.Description("Numeric balenaCloud ID of the SSH key (from ssh-key-list).")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The CLI parses this as an integer, so there is no flag-shape risk.
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return runCmd(ctx, []string{"ssh-key", fmt.Sprintf("%d", id)})
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
	registerMutatingExec(srv)
	registerMutatingPins(srv)
	registerMutatingFleetLifecycle(srv)
	registerMutatingFleetCreation(srv)
	registerMutatingServices(srv)
	registerMutatingDeviceIdentity(srv)
	registerMutatingDeviceEstate(srv)
	registerMutatingReleaseAssets(srv)
	registerMutatingSSHKeys(srv)
	registerMutatingOrgs(srv)
	registerMutatingTags(srv)
	registerMutatingEnvs(srv)
	registerMutatingKeys(srv)
}

// registerMutatingExec: device-ssh. Arbitrary remote command execution, so it
// is treated as destructive (guarded + confirmable) even though many commands
// a caller runs are read-only — the server can't tell `cat /proc/meminfo` from
// `rm -rf /data` apart, so it errs on the side of the confirm gate.
func registerMutatingExec(srv *server.MCPServer) {
	// device-ssh -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-ssh",
		mcp.WithDescription("Run a single shell command on a device over balena's SSH gateway and return its output. "+
			"This is a ONE-SHOT runner: the command is delivered over stdin with an automatic `exit`, so it always terminates — it is NOT an interactive shell (for that, run `balena device ssh <uuid>` directly in a terminal). "+
			"Targets the device host OS by default; pass `service` to run inside a service container. "+
			"Note: remote service-container exec addressed by UUID is not supported by the balenaCloud backend (host OS works; service containers work only for local/VPN-reachable devices). "+
			"The command runs verbatim on the device — treat it with the same care as any remote root shell."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID, IP, or .local address.")),
		mcp.WithString("command", mcp.Required(),
			mcp.Description("Shell command to run on the device, e.g. \"cat /proc/meminfo\". Runs non-interactively; an `exit` is appended automatically so you do not need to add one.")),
		mcp.WithString("service", mcp.Description("Service container name to run inside. Omit to run on the host OS. Only works for local/VPN-reachable devices, not remote-by-UUID.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		command := strings.TrimSpace(r.GetString("command", ""))
		if command == "" {
			return mcp.NewToolResultError(
				"device-ssh requires a non-empty 'command' to run on the device"), nil
		}
		service, errRes := getIdentifier(r, "service", "service name")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "ssh", uuid}
		if service != "" {
			args = append(args, service)
		}
		// The remote shell does not close on stdin EOF, so a bare piped
		// command hangs until the exec timeout. Appending an explicit `exit`
		// makes the session terminate cleanly once the command completes.
		return runCmdStdin(ctx, args, command+"\nexit\n")
	})
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
		mcp.WithDescription("Restart application containers on a device (does NOT reboot the device itself). Optionally restart only a specific service. One device and one service per call — comma-separated lists are rejected."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. One device per call; comma-separated lists are rejected.")),
		mcp.WithString("service", mcp.Description("Service name to restart. One service per call; comma-separated lists are rejected. Omit to restart all services.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		service, errRes := getSingleTarget(r, "service", "service name")
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
		mcp.WithDescription("Clear a device's /data directory. Persistent app data will be lost and cannot be recovered. One device per call — comma-separated lists are rejected."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. One device per call; comma-separated lists are rejected.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "purge", uuid})
	})

	// device-os-update -----------------------------------------------------
	//
	// The version argument is required. Without --version the CLI renders an
	// inquirer list of candidate versions, which over MCP is a hang until
	// BALENAMCP_EXEC_TIMEOUT. --yes is always passed for the same reason: the
	// command asks for confirmation twice (once more when the update requires
	// a takeover), so guardDestructive is the only confirmation layer.
	//
	// --include-draft is deliberately not exposed. Upstream marks it mutually
	// exclusive with --version, so the two cannot be sent together, and it is
	// redundant anyway: when a version is supplied the CLI derives draft
	// support from the version string itself (a prerelease version enables it).
	srv.AddTool(mcp.NewTool("device-os-update",
		mcp.WithDescription("Start a host OS update on a device, pinning it to a target balenaOS version. Requires balenaCloud — this does not work against openBalena or standalone balenaOS. DANGEROUS: if the target requires a takeover update the device is re-partitioned, which ERASES ALL DATA on it and cannot be rolled back. The update is queued rather than immediate and finishes with a device restart; poll device-info to follow it. Find candidate versions with os-versions (keyed by device type) and the device's current version with device-info."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
		mcp.WithString("version", mcp.Required(),
			mcp.Description("Target balenaOS version, e.g. '2.101.7' or '2.31.0+rev1.prod'. Required: omitting it makes the CLI prompt interactively, which would hang. Must be one of the device's supported update targets or the CLI rejects it. A prerelease version enables draft targets automatically.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		version, errRes := requireIdentifier(r, "version", "balenaOS version")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "os-update", uuid, "--version", version, "--yes"})
	})

	// device-local-mode-set ------------------------------------------------
	srv.AddTool(mcp.NewTool("device-local-mode-set",
		mcp.WithDescription("Enable or disable local mode on a development device. Local mode permits local (LAN) push/SSH but suspends cloud-managed updates. Read the current state with device-local-mode-get."),
		destructive,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID.")),
		mcp.WithBoolean("enable", mcp.Required(),
			mcp.Description("true to enable local mode, false to disable it.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		enable, err := r.RequireBool("enable")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// --enable / --disable are mutually exclusive; pick exactly one.
		flag := "--disable"
		if enable {
			flag = "--enable"
		}
		return runCmd(ctx, []string{"device", "local-mode", uuid, flag})
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

	// release-invalidate ---------------------------------------------------
	srv.AddTool(mcp.NewTool("release-invalidate",
		mcp.WithDescription("Mark a release invalid so it is never auto-deployed to tracking devices. Reversible with release-validate."),
		destructive,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit or numeric release ID to invalidate.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := guardDestructive(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"release", "invalidate", id})
	})

	// release-validate -----------------------------------------------------
	srv.AddTool(mcp.NewTool("release-validate",
		mcp.WithDescription("Re-validate a previously invalidated release so it can deploy again. Inverse of release-invalidate."),
		destructive,
		mcp.WithString("id", mcp.Required(),
			mcp.Description("Release commit or numeric release ID to validate.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		id, errRes := guardDestructive(r, "id", "release commit or ID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"release", "validate", id})
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
		mcp.WithString("value", mcp.Required(),
			mcp.Description("Variable value. Required and non-empty: when the CLI receives no value it falls back to reading the SERVER process's environment variable of the same name and writes that to balenaCloud, which on an MCP server is an information leak, not a convenience.")),
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
		// value is free-form (env values can legitimately contain anything);
		// intentionally not flag-shape-guarded. It IS required and must be
		// non-empty: an omitted or empty value makes the CLI fall back to
		// process.env[name] — the balenamcp server's own environment — and
		// write that to balenaCloud. Any secret exported into the server
		// process would become readable by whoever can see the fleet's
		// variables, so the fallback is closed off entirely.
		value := r.GetString("value", "")
		if value == "" {
			return mcp.NewToolResultError(
				"env-set requires a non-empty 'value': the CLI's fallback for a missing value reads the server's own environment, which is not available over MCP"), nil
		}
		args := []string{"env", "set", name, value}
		args = append(args, flag...)
		if service != "" {
			args = append(args, "--service", service)
		}
		args = appendBoolFlag(args, r, "quiet", "--quiet")
		return runCmd(ctx, args)
	})

	// env-rm ---------------------------------------------------------------
	srv.AddTool(mcp.NewTool("env-rm",
		mcp.WithDescription("Remove an env or config variable by its numeric database ID (see env-list). Use --device/--service/--config booleans to disambiguate the variable type; --config and --service are mutually exclusive. --yes is always passed to bypass the CLI's interactive confirmation."),
		destructive,
		mcp.WithNumber("id", mcp.Required(),
			mcp.Description("Numeric database ID of the variable (from env-list).")),
		mcp.WithBoolean("device", mcp.Description("The variable is a device-scoped variable.")),
		mcp.WithBoolean("service", mcp.Description("The variable is a service-scoped variable.")),
		mcp.WithBoolean("config", mcp.Description("The variable is a config variable.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		// env-rm's id is a numeric DB ID — no flag-shape risk.
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// Upstream marks --config and --service mutually exclusive; reject the
		// combination here rather than surfacing oclif's late parse error.
		// (env-rename has carried this same guard from the start.)
		if r.GetBool("config", false) && r.GetBool("service", false) {
			return mcp.NewToolResultError(
				"the 'config' and 'service' options are mutually exclusive"), nil
		}
		args := []string{"env", "rm", fmt.Sprintf("%d", id)}
		args = appendBoolFlag(args, r, "device", "--device")
		args = appendBoolFlag(args, r, "service", "--service")
		args = appendBoolFlag(args, r, "config", "--config")
		// --yes is always passed: without it the CLI falls to an interactive
		// inquirer prompt, which cannot be answered over MCP (with stdin on
		// /dev/null it crashes node outright). Every other destructive wrapper
		// already hardcodes it; env-rm was the one holdout.
		args = append(args, "--yes")
		return runCmd(ctx, args)
	})

	// env-rename -----------------------------------------------------------
	// Despite the balena CLI name, `env rename` changes a variable's VALUE
	// (selected by numeric DB ID), not its name. Use --device/--service/--config
	// booleans to disambiguate the variable type, exactly as env-rm does.
	srv.AddTool(mcp.NewTool("env-rename",
		mcp.WithDescription("Change the VALUE of an existing env or config variable by its numeric database ID (see env-list). Note: despite the balena CLI command name, this updates the value, not the variable name. Use --device/--service/--config booleans to disambiguate the variable type; --config and --service are mutually exclusive."),
		destructive,
		mcp.WithNumber("id", mcp.Required(),
			mcp.Description("Numeric database ID of the variable (from env-list).")),
		mcp.WithString("value", mcp.Required(), mcp.Description("New value for the variable.")),
		mcp.WithBoolean("device", mcp.Description("The variable is a device-scoped variable.")),
		mcp.WithBoolean("service", mcp.Description("The variable is a service-scoped variable.")),
		mcp.WithBoolean("config", mcp.Description("The variable is a config variable.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		// id is a numeric DB ID — no flag-shape risk.
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		// value is free-form (may be any string); require it present but do not
		// flag-shape-guard it. An empty value would produce an empty positional.
		value := r.GetString("value", "")
		if value == "" {
			return mcp.NewToolResultError("env-rename requires a non-empty 'value'"), nil
		}
		if r.GetBool("config", false) && r.GetBool("service", false) {
			return mcp.NewToolResultError(
				"env-rename: 'config' and 'service' are mutually exclusive"), nil
		}
		args := []string{"env", "rename", fmt.Sprintf("%d", id), value}
		args = appendBoolFlag(args, r, "device", "--device")
		args = appendBoolFlag(args, r, "service", "--service")
		args = appendBoolFlag(args, r, "config", "--config")
		return runCmd(ctx, args)
	})
}

// registerMutatingFleetLifecycle: fleet-track-latest, fleet-purge,
// fleet-restart, fleet-rm. These act on EVERY device in the fleet. Note that
// the balena CLI runs fleet-purge and fleet-restart immediately with no --yes
// flag and no interactive prompt, so the only confirmation layer for them is
// guardDestructive here; fleet-rm does take --yes, which we always pass.
func registerMutatingFleetLifecycle(srv *server.MCPServer) {
	// fleet-track-latest ---------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-track-latest",
		mcp.WithDescription("Drop a fleet's release pin so it resumes tracking the latest final release. Fleet-level inverse of fleet-pin."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet name or slug.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "track-latest", fleet})
	})

	// fleet-purge ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-purge",
		mcp.WithDescription("Clear the /data directory on EVERY device in a fleet. Persistent app data is lost fleet-wide and cannot be recovered."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet name or slug.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "purge", fleet})
	})

	// fleet-restart --------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-restart",
		mcp.WithDescription("Restart all service containers on EVERY device in a fleet (does not reboot the devices)."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet name or slug.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "restart", fleet})
	})

	// fleet-rm -------------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-rm",
		mcp.WithDescription("Permanently delete a fleet. Irreversible. --yes is always passed to bypass the CLI's interactive confirmation."),
		destructive,
		mcp.WithString("fleet", mcp.Required(), mcp.Description("Fleet name or slug to delete.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "rm", fleet, "--yes"})
	})
}

// registerMutatingReleaseAssets: release-asset-download, release-asset-upload,
// release-asset-delete. The only tools in the server that touch the host
// filesystem; every path they take is confined by resolveAssetPath to
// BALENAMCP_ASSET_DIR, and download/upload refuse to run at all when that is
// unset. release-asset-delete is a pure cloud call and works either way.
func registerMutatingReleaseAssets(srv *server.MCPServer) {
	// release-asset-download -----------------------------------------------
	//
	// output is required even though the CLI defaults to the asset's original
	// filename: that default writes into the server process's working
	// directory, which is outside the configured root. The existence check
	// below stands in for --overwrite's interactive confirmation, which would
	// hang over MCP.
	srv.AddTool(mcp.NewTool("release-asset-download",
		mcp.WithDescription("Download a release asset to the server's configured asset directory. Requires BALENAMCP_ASSET_DIR to be set on the server; output is a path relative to it. Large assets can exceed BALENAMCP_EXEC_TIMEOUT — raise it for big downloads. List available assets with release-asset-list."),
		destructive,
		mcp.WithString("release", mcp.Required(),
			mcp.Description("Release commit hash or numeric ID.")),
		mcp.WithString("key", mcp.Required(),
			mcp.Description("Asset key, as listed by release-asset-list.")),
		mcp.WithString("output", mcp.Required(),
			mcp.Description("Destination path, relative to BALENAMCP_ASSET_DIR (e.g. 'downloads/app.tar.gz'). Absolute paths and '..' are rejected.")),
		mcp.WithBoolean("overwrite",
			mcp.Description("Set to true to replace an existing local file. Defaults to false, in which case an existing file is an error.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		release, key, errRes := assetTarget(r)
		if errRes != nil {
			return errRes, nil
		}
		output, errRes := resolveAssetPath(r.GetString("output", ""), "output path")
		if errRes != nil {
			return errRes, nil
		}
		overwrite := r.GetBool("overwrite", false)
		if !overwrite {
			if _, err := os.Stat(output); err == nil {
				return mcp.NewToolResultError(fmt.Sprintf(
					"%q already exists: pass overwrite:true to replace it",
					r.GetString("output", ""))), nil
			}
		}
		args := []string{"release-asset", "download", release, "--key", key, "--output", output}
		if overwrite {
			args = append(args, "--overwrite")
		}
		return runCmd(ctx, args)
	})

	// release-asset-upload -------------------------------------------------
	//
	// --chunk-size and --parallel-chunks are not exposed; they are throughput
	// tuning knobs with sane defaults and no bearing on what an agent is
	// trying to do.
	srv.AddTool(mcp.NewTool("release-asset-upload",
		mcp.WithDescription("Upload a local file as a release asset. Requires BALENAMCP_ASSET_DIR to be set on the server; file_path is a path relative to it, so only files placed in that directory can be uploaded. Large files can exceed BALENAMCP_EXEC_TIMEOUT."),
		destructive,
		mcp.WithString("release", mcp.Required(),
			mcp.Description("Release commit hash or numeric ID.")),
		mcp.WithString("key", mcp.Required(),
			mcp.Description("Key to store the asset under.")),
		mcp.WithString("file_path", mcp.Required(),
			mcp.Description("File to upload, relative to BALENAMCP_ASSET_DIR. Absolute paths and '..' are rejected.")),
		mcp.WithBoolean("overwrite",
			mcp.Description("Set to true to replace an asset already stored under this key. Defaults to false.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		release, key, errRes := assetTarget(r)
		if errRes != nil {
			return errRes, nil
		}
		filePath, errRes := resolveAssetPath(r.GetString("file_path", ""), "file path")
		if errRes != nil {
			return errRes, nil
		}
		info, err := os.Stat(filePath)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%q is not readable: %v", r.GetString("file_path", ""), err)), nil
		}
		if info.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%q is a directory, not a file", r.GetString("file_path", ""))), nil
		}
		args := []string{"release-asset", "upload", release, filePath, "--key", key}
		args = appendBoolFlag(args, r, "overwrite", "--overwrite")
		return runCmd(ctx, args)
	})

	// release-asset-delete -------------------------------------------------
	srv.AddTool(mcp.NewTool("release-asset-delete",
		mcp.WithDescription("Permanently delete a release asset. The CLI documents this as impossible to undo. Pure cloud call — it touches no local files, so it works whether or not BALENAMCP_ASSET_DIR is set. --yes is always passed to bypass the interactive confirmation."),
		destructive,
		mcp.WithString("release", mcp.Required(),
			mcp.Description("Release commit hash or numeric ID.")),
		mcp.WithString("key", mcp.Required(),
			mcp.Description("Asset key to delete, as listed by release-asset-list.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		release, key, errRes := assetTarget(r)
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"release-asset", "delete", release, "--key", key, "--yes"})
	})
}

// assetTarget runs the shared preamble for the release-asset tools: the
// confirm gate, then the release identifier and asset key that all three take.
func assetTarget(r mcp.CallToolRequest) (string, string, *mcp.CallToolResult) {
	if errRes := requireConfirm(r); errRes != nil {
		return "", "", errRes
	}
	release, errRes := requireIdentifier(r, "release", "release ID or commit")
	if errRes != nil {
		return "", "", errRes
	}
	key, errRes := requireIdentifier(r, "key", "asset key")
	if errRes != nil {
		return "", "", errRes
	}
	return release, key, nil
}

// registerMutatingDeviceEstate: device-rm, device-deactivate, device-move,
// device-register. Membership and existence of a device within balenaCloud,
// as opposed to what a device is doing.
//
// device rm and device deactivate both prompt for confirmation unless --yes is
// passed, and --yes must be passed or the call hangs — so guardDestructive is
// the only safety gate on the most irreversible operations in the surface.
// device move prompts for the target fleet when --fleet is omitted, so that
// argument is required here.
func registerMutatingDeviceEstate(srv *server.MCPServer) {
	// device-rm ------------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-rm",
		mcp.WithDescription("Permanently remove a device from balenaCloud. Irreversible: the device and its history are gone, and the hardware must be re-provisioned to come back. One device per call — comma-separated lists are rejected. --yes is always passed to bypass the CLI's interactive confirmation."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID to remove. One device per call; comma-separated lists are rejected.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "rm", uuid, "--yes"})
	})

	// device-deactivate ----------------------------------------------------
	//
	// Unlike rm and move, the CLI resolves a single UUID here and never splits
	// on "," — the guard is applied anyway so a list produces a clear error
	// rather than an unresolvable-UUID failure from the backend.
	srv.AddTool(mcp.NewTool("device-deactivate",
		mcp.WithDescription("Deactivate a device, releasing it from its fleet while keeping it in the account. BILLING: on paid plans balena charges a fee equivalent to one month's normal cost for the device, and it is not charged again until it comes back online. Free-tier accounts are not charged, though the CLI prints the warning either way. --yes is always passed to bypass the CLI's interactive confirmation."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID to deactivate. One device per call.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "deactivate", uuid, "--yes"})
	})

	// device-move ----------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-move",
		mcp.WithDescription("Move a device to another fleet. The target fleet is required (the CLI would otherwise block on an interactive prompt) and must accept the device's device type. Reversible by moving it back. One device per call — comma-separated lists are rejected."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID to move. One device per call; comma-separated lists are rejected.")),
		mcp.WithString("fleet", mcp.Required(),
			mcp.Description("Target fleet name or org/fleet slug, as listed by fleet-list.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		fleet, errRes := requireIdentifier(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "move", uuid, "--fleet", fleet})
	})

	// device-register ------------------------------------------------------
	//
	// Note --deviceType is camelCase, unlike almost every other flag in the
	// CLI. The MCP argument is device_type, matching our snake_case convention.
	srv.AddTool(mcp.NewTool("device-register",
		mcp.WithDescription("Register a new device with a fleet, reserving a UUID before the hardware is provisioned. Additive rather than destructive, but it does change fleet membership. Omit uuid to have balenaCloud assign one."),
		destructive,
		mcp.WithString("fleet", mcp.Required(),
			mcp.Description("Fleet name or org/fleet slug to register the device into.")),
		mcp.WithString("uuid",
			mcp.Description("Custom device UUID. Omit to have balenaCloud generate one.")),
		mcp.WithString("device_type",
			mcp.Description("Device type slug (e.g. 'raspberrypi4-64'), as listed by device-type-list. Defaults to the fleet's device type.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		uuid, errRes := getIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		deviceType, errRes := getIdentifier(r, "device_type", "device type")
		if errRes != nil {
			return errRes, nil
		}
		args := []string{"device", "register", fleet}
		if uuid != "" {
			args = append(args, "--uuid", uuid)
		}
		if deviceType != "" {
			args = append(args, "--deviceType", deviceType)
		}
		return runCmd(ctx, args)
	})
}

// registerMutatingDeviceIdentity: device-identify, device-rename, device-note,
// device-public-url-set. Field-work tools that label, locate or expose a single
// device. The read side of public-url lives with the other read-only device
// tools as device-public-url.
//
// device-identify is registered here for topical grouping but is annotated
// read-only: blinking an LED leaves no state behind, so gating it behind a
// confirmation would train operators to acknowledge without reading.
func registerMutatingDeviceIdentity(srv *server.MCPServer) {
	// device-identify ------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-identify",
		mcp.WithDescription("Make a device physically identify itself by blinking its ACT LED (Raspberry Pi). Useful for locating one board among many on site. Annotated read-only: the LED stops on its own and no device or cloud state changes, so it needs no confirmation. The device must be online or the CLI reports it as unreachable."),
		readOnly,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "identify", uuid})
	})

	// device-rename --------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-rename",
		mcp.WithDescription("Rename a device. Requires the new name (the CLI would otherwise block on an interactive prompt). Reversible by renaming again."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
		mcp.WithString("new_name", mcp.Required(),
			mcp.Description("New name for the device.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		newName, errRes := requireIdentifier(r, "new_name", "new device name")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "rename", uuid, newName})
	})

	// device-note ----------------------------------------------------------
	//
	// The CLI's argument shape is inverted relative to every other device
	// command: the note is positional and the device is a --device flag. Both
	// are required here. Upstream documents that an omitted note is read from
	// stdin, but the v25.2.5 implementation does not do that — it writes
	// `params.note ?? ''`, silently CLEARING the note. Requiring the argument
	// avoids relying on either behavior.
	srv.AddTool(mcp.NewTool("device-note",
		mcp.WithDescription("Set or replace a device's note (free-text annotation). Replaces any existing note rather than appending. Read notes back with device-info. Note text cannot start with '-', which the CLI would parse as a flag."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
		mcp.WithString("note", mcp.Required(),
			mcp.Description("Note content. Replaces the existing note. Cannot be empty or start with '-'.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		// The note is free-form text, but it lands in argv as a positional, so
		// a leading dash would be parsed as a flag. Rejecting it is clearer
		// than letting the CLI fail on an unknown flag.
		note, errRes := requireIdentifier(r, "note", "note")
		if errRes != nil {
			return errRes, nil
		}
		if note == "" {
			return mcp.NewToolResultError(
				"note is empty: pass the note text, or use device-info to read the current note"), nil
		}
		return runCmd(ctx, []string{"device", "note", note, "--device", uuid})
	})

	// device-public-url-set ------------------------------------------------
	//
	// Split from the read path so the reader can carry ReadOnlyHint honestly.
	// The split also makes the CLI's three mutually exclusive flags
	// unrepresentable: --status belongs to device-public-url, and this tool
	// derives exactly one of --enable/--disable from a required bool.
	srv.AddTool(mcp.NewTool("device-public-url-set",
		mcp.WithDescription("Enable or disable a device's public device URL. Enabling EXPOSES the device's web service to the public internet at a guessable-by-nobody but unauthenticated URL — anyone holding the URL can reach the device. Read the current state with device-public-url."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID.")),
		mcp.WithBoolean("enable", mcp.Required(),
			mcp.Description("true to expose the device on a public URL, false to disable it.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := guardDestructive(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		enable, err := r.RequireBool("enable")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		flag := "--disable"
		if enable {
			flag = "--enable"
		}
		return runCmd(ctx, []string{"device", "public-url", uuid, flag})
	})
}

// registerMutatingServices: device-start-service, device-stop-service.
// Per-service container control, the pair that makes "take this container out
// of service while I investigate" possible without falling back to device-ssh.
//
// The CLI accepts comma-separated lists for BOTH the device and the service
// argument and acts on every combination. Both tools here take exactly one of
// each (see requireSingleTarget) so a single call cannot fan out across an
// estate. Neither command has a --yes flag or an interactive prompt, so
// guardDestructive is the only confirmation layer.
func registerMutatingServices(srv *server.MCPServer) {
	// device-start-service -------------------------------------------------
	srv.AddTool(mcp.NewTool("device-start-service",
		mcp.WithDescription("Start a stopped service container on a device. The restorative counterpart to device-stop-service. One device and one service per call — pass a comma-separated list and the call is rejected."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. One device per call; comma-separated lists are rejected.")),
		mcp.WithString("service", mcp.Required(),
			mcp.Description("Service name to start. One service per call; comma-separated lists are rejected.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, service, errRes := serviceTarget(r)
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "start-service", uuid, service})
	})

	// device-stop-service --------------------------------------------------
	srv.AddTool(mcp.NewTool("device-stop-service",
		mcp.WithDescription("Stop a service container on a device and leave it stopped. Unlike device-restart the container does not come back on its own; use device-start-service to restore it. One device and one service per call — pass a comma-separated list and the call is rejected."),
		destructive,
		mcp.WithString("uuid", mcp.Required(),
			mcp.Description("Device UUID. One device per call; comma-separated lists are rejected.")),
		mcp.WithString("service", mcp.Required(),
			mcp.Description("Service name to stop. One service per call; comma-separated lists are rejected.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, service, errRes := serviceTarget(r)
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "stop-service", uuid, service})
	})
}

// serviceTarget runs the shared preamble for the two per-service tools: the
// confirm gate, then a single-target lookup of each positional argument.
// guardDestructive is not usable here because both arguments are list-accepting
// and must go through requireSingleTarget.
func serviceTarget(r mcp.CallToolRequest) (string, string, *mcp.CallToolResult) {
	if errRes := requireConfirm(r); errRes != nil {
		return "", "", errRes
	}
	uuid, errRes := requireSingleTarget(r, "uuid", "device UUID")
	if errRes != nil {
		return "", "", errRes
	}
	service, errRes := requireSingleTarget(r, "service", "service name")
	if errRes != nil {
		return "", "", errRes
	}
	return uuid, service, nil
}

// registerMutatingFleetCreation: fleet-create, fleet-rename. Fleet lifecycle
// that does not touch the fleet's devices — kept apart from
// registerMutatingFleetLifecycle, whose tools all act on EVERY device.
//
// Both tools require every argument the CLI would otherwise prompt for.
// `fleet create` renders an interactive dropdown when --organization or --type
// is omitted and the account has more than one candidate; `fleet rename`
// prompts when newName is omitted. Under MCP a prompt is not a question, it is
// a hang until BALENAMCP_EXEC_TIMEOUT fires, so these arguments are mandatory
// here even though the CLI treats them as optional.
func registerMutatingFleetCreation(srv *server.MCPServer) {
	// fleet-create ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-create",
		mcp.WithDescription("Create a new fleet. Requires the owning organization's handle (its slug, not its display name — list them with organization-list) and the fleet's default device type (list them with device-type-list). Both are required because the CLI would otherwise show an interactive dropdown and hang the call."),
		destructive,
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Name for the new fleet.")),
		mcp.WithString("organization", mcp.Required(),
			mcp.Description("Handle/slug of the owning organization (e.g. 'myorg'), as listed by organization-list. Not the display name.")),
		mcp.WithString("type", mcp.Required(),
			mcp.Description("Default device type slug for the fleet (e.g. 'raspberrypi4-64'), as listed by device-type-list.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, errRes := guardDestructive(r, "name", "fleet name")
		if errRes != nil {
			return errRes, nil
		}
		org, errRes := requireIdentifier(r, "organization", "organization handle")
		if errRes != nil {
			return errRes, nil
		}
		deviceType, errRes := requireIdentifier(r, "type", "device type")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "create", name,
			"--organization", org, "--type", deviceType})
	})

	// fleet-rename ---------------------------------------------------------
	srv.AddTool(mcp.NewTool("fleet-rename",
		mcp.WithDescription("Rename a fleet. Requires the current fleet name/slug and the new name (the CLI would otherwise block on an interactive prompt). Reversible by renaming again. Fleets of the legacy application type cannot be renamed."),
		destructive,
		mcp.WithString("fleet", mcp.Required(),
			mcp.Description("Current fleet name or org/fleet slug (e.g. 'MyFleet' or 'myorg/myfleet').")),
		mcp.WithString("new_name", mcp.Required(),
			mcp.Description("New name for the fleet.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		fleet, errRes := guardDestructive(r, "fleet", "fleet slug")
		if errRes != nil {
			return errRes, nil
		}
		newName, errRes := requireIdentifier(r, "new_name", "new fleet name")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"fleet", "rename", fleet, newName})
	})
}

// registerMutatingOrgs: organization-create, organization-rename,
// organization-rm.
func registerMutatingOrgs(srv *server.MCPServer) {
	// organization-create --------------------------------------------------
	srv.AddTool(mcp.NewTool("organization-create",
		mcp.WithDescription("Create a new organization. The argument is the display name; balenaCloud auto-generates the handle/slug (it cannot be set at creation time)."),
		destructive,
		mcp.WithString("name", mcp.Required(), mcp.Description("Display name for the new organization.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, errRes := guardDestructive(r, "name", "organization name")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"organization", "create", name})
	})

	// organization-rename --------------------------------------------------
	srv.AddTool(mcp.NewTool("organization-rename",
		mcp.WithDescription("Rename an organization. Requires the current handle/slug and the new display name. The new name must be provided (the CLI would otherwise block on an interactive prompt)."),
		destructive,
		mcp.WithString("handle", mcp.Required(), mcp.Description("Current organization handle/slug.")),
		mcp.WithString("new_name", mcp.Required(), mcp.Description("New display name for the organization.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		handle, errRes := guardDestructive(r, "handle", "organization handle")
		if errRes != nil {
			return errRes, nil
		}
		newName, errRes := requireIdentifier(r, "new_name", "new organization name")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"organization", "rename", handle, newName})
	})

	// organization-rm ------------------------------------------------------
	srv.AddTool(mcp.NewTool("organization-rm",
		mcp.WithDescription("Permanently delete an organization by its handle/slug. Irreversible. --yes is always passed to bypass the interactive confirmation."),
		destructive,
		mcp.WithString("handle", mcp.Required(), mcp.Description("Organization handle/slug to delete.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		handle, errRes := guardDestructive(r, "handle", "organization handle")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"organization", "rm", handle, "--yes"})
	})
}

// registerMutatingSSHKeys: ssh-key-add, ssh-key-rm. The read side is
// ssh-key-info, registered with the other read-only account tools.
//
// `api-key generate` is deliberately not wrapped. It prints a live, long-lived
// balenaCloud credential to stdout and this server returns stdout verbatim to
// the MCP client, where it may be persisted, summarized or forwarded. See the
// deliberate-exclusions table in README.
func registerMutatingSSHKeys(srv *server.MCPServer) {
	// ssh-key-add ----------------------------------------------------------
	//
	// The key file is read from the host, so the path goes through the same
	// BALENAMCP_ASSET_DIR confinement as the release-asset tools: without it,
	// an arbitrary readable path would be an exfiltration channel, since the
	// file contents are uploaded to balenaCloud.
	srv.AddTool(mcp.NewTool("ssh-key-add",
		mcp.WithDescription("Register an SSH PUBLIC key with the balenaCloud account. Requires BALENAMCP_ASSET_DIR to be set on the server; path is relative to it, so only key files placed in that directory can be added. Upload the .pub file only — balenaCloud never needs the private key, and a private key placed in the asset directory would be uploaded verbatim."),
		destructive,
		mcp.WithString("name", mcp.Required(),
			mcp.Description("Label for the key in balenaCloud.")),
		mcp.WithString("path", mcp.Required(),
			mcp.Description("Public key file (e.g. 'id_rsa.pub'), relative to BALENAMCP_ASSET_DIR. Absolute paths and '..' are rejected.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, errRes := guardDestructive(r, "name", "SSH key name")
		if errRes != nil {
			return errRes, nil
		}
		path, errRes := resolveAssetPath(r.GetString("path", ""), "key path")
		if errRes != nil {
			return errRes, nil
		}
		info, err := os.Stat(path)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%q is not readable: %v", r.GetString("path", ""), err)), nil
		}
		if info.IsDir() {
			return mcp.NewToolResultError(fmt.Sprintf(
				"%q is a directory, not a public key file", r.GetString("path", ""))), nil
		}
		return runCmd(ctx, []string{"ssh-key", "add", name, path})
	})

	// ssh-key-rm -----------------------------------------------------------
	srv.AddTool(mcp.NewTool("ssh-key-rm",
		mcp.WithDescription("Remove an SSH key from the balenaCloud account by its numeric ID. Irreversible, and removing the wrong key can lock a user out of their own devices — confirm the target with ssh-key-info first. --yes is always passed to bypass the interactive confirmation."),
		destructive,
		mcp.WithNumber("id", mcp.Required(),
			mcp.Description("Numeric balenaCloud ID of the SSH key to remove (from ssh-key-list).")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		if errRes := requireConfirm(r); errRes != nil {
			return errRes, nil
		}
		id, err := r.RequireInt("id")
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return runCmd(ctx, []string{"ssh-key", "rm", fmt.Sprintf("%d", id), "--yes"})
	})
}

// registerMutatingKeys: api-key-revoke.
func registerMutatingKeys(srv *server.MCPServer) {
	// api-key-revoke -------------------------------------------------------
	srv.AddTool(mcp.NewTool("api-key-revoke",
		mcp.WithDescription("Revoke one or more balenaCloud API keys by numeric ID. Pass a single ID or a comma-separated list with no spaces, e.g. \"123,456\" (IDs come from api-key-list). Irreversible."),
		destructive,
		mcp.WithString("ids", mcp.Required(),
			mcp.Description("Numeric API key ID, or a comma-separated list of IDs (no spaces), e.g. \"123,456\".")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// The CLI takes a single positional that is itself a comma-separated
		// list, so the whole thing is one argv element — never split it.
		ids, errRes := guardDestructive(r, "ids", "API key ID(s)")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"api-key", "revoke", ids})
	})
}

// registerReadOnlyDiagnostics: device-detect, device-local-mode-get. Read-only
// device discovery/inspection that doesn't fit the fleet/release/tag families.
func registerReadOnlyDiagnostics(srv *server.MCPServer) {
	// device-detect --------------------------------------------------------
	srv.AddTool(mcp.NewTool("device-detect",
		mcp.WithDescription("Scan the local network (LAN) for balenaOS devices and report what is found. Read-only. Devices running production OS images expose less detail than development images."),
		readOnly,
		mcp.WithBoolean("json", mcp.Description("Output as JSON.")),
		mcp.WithNumber("timeout", mcp.Description("Scan timeout in seconds.")),
		mcp.WithBoolean("verbose", mcp.Description("Include extra detail in the scan output.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		args := []string{"device", "detect"}
		if v := r.GetInt("timeout", -1); v >= 0 {
			args = append(args, "--timeout", fmt.Sprintf("%d", v))
		}
		args = appendBoolFlag(args, r, "verbose", "--verbose")
		args = appendBoolFlag(args, r, "json", "--json")
		return runCmd(ctx, args)
	})

	// device-local-mode-get ------------------------------------------------
	srv.AddTool(mcp.NewTool("device-local-mode-get",
		mcp.WithDescription("Report whether local mode is enabled on a device. Read-only. Change it with device-local-mode-set."),
		readOnly,
		mcp.WithString("uuid", mcp.Required(), mcp.Description("Device UUID.")),
	), func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		uuid, errRes := requireIdentifier(r, "uuid", "device UUID")
		if errRes != nil {
			return errRes, nil
		}
		return runCmd(ctx, []string{"device", "local-mode", uuid, "--status"})
	})
}

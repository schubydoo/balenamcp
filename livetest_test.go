//go:build integration

// livetest_test.go — end-to-end sweep against real balenaCloud.
//
// Gated on the `integration` build tag so `go test ./...` never picks it up.
// Run explicitly with:
//
//	BALENA_LIVE_FLEET=myorg/myfleet \
//	BALENA_LIVE_DEVICE=<uuid>       \
//	BALENA_LIVE_RELEASE=<commit>    \
//	BALENA_LIVE_RELEASE_ALT=<other-commit> \
//	    go test -tags=integration -v -count=1 -run TestLiveSweep .
//
// Required env vars:
//
//	BALENA_LIVE_FLEET        org/fleet slug
//	BALENA_LIVE_DEVICE       device UUID (short or full)
//
// Optional env vars (sub-tests skip without them):
//
//	BALENA_LIVE_DEVICE_TYPE  default: raspberrypi4-64 (for os-versions)
//	BALENA_LIVE_RELEASE      a final release commit on the fleet
//	BALENA_LIVE_RELEASE_ALT  a *different* final release commit on the fleet
//	                         (PinLifecycle round-trips between these)
//
// Opt-in env vars for irreversible sub-tests (default: skipped):
//
//	BALENA_LIVE_ALLOW_PURGE=1       enable device-purge (wipes /data)
//	BALENA_LIVE_ALLOW_SHUTDOWN=1    enable device-shutdown (manual recovery)
//
// The release-finalize sub-test always runs — it exercises only the error
// branch (against a bogus commit) since intentionally producing a draft
// release from inside this test is out of scope.
//
// Additional optional env vars for the newer tools (sub-tests skip without):
//
//	BALENA_LIVE_SERVICE             a service name on the device (stop/start round trip)
//	BALENA_LIVE_SSH_KEY_ID          numeric ssh key id (ssh-key-info read)
//	BALENA_LIVE_SSH_PUBKEY          literal public key TEXT to add+remove (ssh-key round trip)
//	BALENA_LIVE_ALLOW_REGISTER=1    enable device-register + device-rm of the phantom device
//	BALENA_LIVE_ALLOW_FLEET_CREATE=1 enable fleet-create + fleet-rename + fleet-rm round trip
//	BALENA_LIVE_ALLOW_ASSET=1       enable release-asset upload/download/delete round trip
//	                                (mutates BALENA_LIVE_RELEASE's asset set)
//
// Deliberately not live-tested, with reasons — this list is a decision, not
// an accident:
//
//	device-os-update       queues a host OS update; takeover targets erase the
//	                       device. No safe round trip exists.
//	device-move            requires a second fleet accepting the device type;
//	                       moving the only live test device mid-sweep breaks
//	                       every later sub-test.
//	device-deactivate      charges a fee on paid plans and needs the device to
//	                       come back online to re-register.
//	device-public-url-set  --enable exposes the device to the public internet;
//	                       the read side (device-public-url) is covered.
//	fleet-purge/restart/track-latest, org create/rename/rm, api-key-revoke,
//	release-invalidate/validate, env-rename
//	                       fleet-/org-/account-wide blast radius on shared
//	                       infrastructure; the dry-run argv tests plus the
//	                       upstream parity audit cover their construction.

package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/schubydoo/balenamcp/server"
	"github.com/stretchr/testify/require"
)

// newLiveClient spins up the MCP server with DryRun explicitly off so calls
// shell out to the real balena binary. Mirrors newTestClient but for live use.
func newLiveClient(t *testing.T) (*mcpclient.Client, context.Context) {
	t.Helper()
	// SetupServer runs loadConfigFromEnv which does NOT touch DryRun. Force
	// it off here so a stray env or flag elsewhere can't leave us in dry-run.
	server.Config.DryRun = false
	// Generous timeout — `device-logs` and `device-type-list` can take ~10s
	// under cloud load; the default 60s is already plenty but we re-assert.
	t.Setenv("BALENAMCP_EXEC_TIMEOUT", "120")

	srv := server.SetupServer()
	c, err := mcpclient.NewInProcessClient(srv)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, c.Start(ctx))

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "livetest", Version: "0.0.0"}
	_, err = c.Initialize(ctx, initReq)
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })
	return c, ctx
}

// callRaw invokes a tool and returns (text, isError). Unlike main_test.go's
// helpers, we don't assert pass/fail here — sub-tests decide what counts as
// success against the real balena CLI's many quirks.
func callRaw(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any) (string, bool) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	require.NoError(t, err, "transport error calling %s", name)
	require.NotEmpty(t, res.Content, "tool %s returned no content", name)
	text, ok := mcp.AsTextContent(res.Content[0])
	require.True(t, ok, "tool %s returned non-text content: %T", name, res.Content[0])
	return text.Text, res.IsError
}

// mustOK calls a tool and fails the test if it returned an error result. Used
// for tools that should succeed against any live balena. Returns the text for
// optional further assertions.
func mustOK(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	text, isErr := callRaw(t, c, ctx, name, args)
	if isErr {
		t.Fatalf("tool %s returned error: %s", name, truncate(text, 400))
	}
	t.Logf("[OK]   %s\n       %s", name, truncate(text, 200))
	return text
}

// mustErrorContaining asserts the tool returns an error result whose text
// contains `needle`. Used for exercise-the-error-path scenarios.
func mustErrorContaining(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any, needle string) {
	t.Helper()
	text, isErr := callRaw(t, c, ctx, name, args)
	if !isErr {
		t.Fatalf("tool %s expected error, got success: %s", name, truncate(text, 400))
	}
	if !strings.Contains(text, needle) {
		t.Fatalf("tool %s error did not contain %q; got: %s", name, needle, truncate(text, 400))
	}
	t.Logf("[OK errpath] %s\n       %s", name, truncate(text, 200))
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func TestLiveSweep(t *testing.T) {
	fleet := os.Getenv("BALENA_LIVE_FLEET")
	device := os.Getenv("BALENA_LIVE_DEVICE")
	if fleet == "" || device == "" {
		t.Skip("set BALENA_LIVE_FLEET and BALENA_LIVE_DEVICE to enable; see file header for full env contract")
	}
	deviceType := envOr("BALENA_LIVE_DEVICE_TYPE", "raspberrypi4-64")
	release := os.Getenv("BALENA_LIVE_RELEASE")
	releaseAlt := os.Getenv("BALENA_LIVE_RELEASE_ALT")

	c, ctx := newLiveClient(t)

	// ----- read-only ------------------------------------------------------
	t.Run("ReadOnly", func(t *testing.T) {
		// These should succeed against any authenticated balenaCloud session.
		// Failures here usually mean the local balena binary is stale, the
		// CLI flag surface drifted, or the user isn't logged in.
		mustOK(t, c, ctx, "version", nil)
		mustOK(t, c, ctx, "whoami", nil)
		mustOK(t, c, ctx, "fleet-list", nil)
		mustOK(t, c, ctx, "fleet-info", map[string]any{"fleet": fleet})
		mustOK(t, c, ctx, "device-list", map[string]any{"fleet": fleet})
		mustOK(t, c, ctx, "device-info", map[string]any{"uuid": device})
		// device-logs may legitimately return empty on an idle device.
		mustOK(t, c, ctx, "device-logs", map[string]any{"device": device})
		// device-type-list is rate-limit-prone; one call here keeps the test fast.
		mustOK(t, c, ctx, "device-type-list", nil)
		mustOK(t, c, ctx, "release-list", map[string]any{"fleet": fleet})
		if release != "" {
			mustOK(t, c, ctx, "release-info", map[string]any{"id": release})
			mustOK(t, c, ctx, "release-asset-list", map[string]any{"id": release})
		} else {
			t.Log("BALENA_LIVE_RELEASE not set — skipping release-info / release-asset-list")
		}
		// tag-list on a device with zero tags exercises the empty-state
		// remap (runCmdAllowingBenignError). Tag the device in TagRoundTrip
		// below if you want this case to return actual tags.
		mustOK(t, c, ctx, "tag-list", map[string]any{"device": device})
		mustOK(t, c, ctx, "env-list", map[string]any{"fleet": fleet})
		mustOK(t, c, ctx, "os-versions", map[string]any{"type": deviceType})
		mustOK(t, c, ctx, "organization-list", nil)
		mustOK(t, c, ctx, "ssh-key-list", nil)
		mustOK(t, c, ctx, "api-key-list", nil)
		// Newer read-only device tools.
		mustOK(t, c, ctx, "device-local-mode-get", map[string]any{"uuid": device})
		mustOK(t, c, ctx, "device-public-url", map[string]any{"uuid": device})
		mustOK(t, c, ctx, "device-public-url", map[string]any{"uuid": device, "status": true})
		// LAN scan runs on THIS host; an empty result is fine, a non-zero
		// exit is not. Short timeout keeps the sweep fast.
		mustOK(t, c, ctx, "device-detect", map[string]any{"timeout": float64(5)})
		if id := os.Getenv("BALENA_LIVE_SSH_KEY_ID"); id != "" {
			var f float64
			_, err := fmt.Sscanf(id, "%f", &f)
			require.NoError(t, err, "BALENA_LIVE_SSH_KEY_ID must be numeric")
			mustOK(t, c, ctx, "ssh-key-info", map[string]any{"id": f})
		} else {
			t.Log("BALENA_LIVE_SSH_KEY_ID not set — skipping ssh-key-info")
		}
	})

	t.Run("Identify", func(t *testing.T) {
		// Blinks the ACT LED. Read-only-annotated and harmless, but it needs
		// the device online; accept the CLI's explicit not-online error as a
		// pass so a sweep against a rebooting device doesn't flap.
		text, isErr := callRaw(t, c, ctx, "device-identify", map[string]any{"uuid": device})
		if isErr && !strings.Contains(text, "not online") {
			t.Fatalf("device-identify failed for a reason other than offline: %s", truncate(text, 300))
		}
	})

	// ----- destructive: reversible ----------------------------------------
	t.Run("TagRoundTrip", func(t *testing.T) {
		key := "livetest-" + time.Now().UTC().Format("20060102T150405")
		mustOK(t, c, ctx, "tag-set", map[string]any{
			"device": device, "key": key, "value": "sweep-marker",
		})
		out := mustOK(t, c, ctx, "tag-list", map[string]any{"device": device})
		require.Contains(t, out, key, "tag-set didn't surface in subsequent tag-list")
		mustOK(t, c, ctx, "tag-rm", map[string]any{"device": device, "key": key})
	})

	t.Run("EnvRoundTrip", func(t *testing.T) {
		name := "LIVETEST_" + time.Now().UTC().Format("20060102T150405")
		mustOK(t, c, ctx, "env-set", map[string]any{
			"device": device, "name": name, "value": "sweep-marker",
		})
		// env-list with --json so we can grab the numeric id reliably; the
		// text format hides some device-scoped vars depending on the CLI
		// release. We don't parse here — env-rm by id below proves the var
		// existed by its successful removal.
		mustOK(t, c, ctx, "env-list", map[string]any{"device": device, "json": true})
		// Find the numeric id via a direct balena call. We could parse JSON
		// out of env-list, but shelling out to `balena env list --json` and
		// grepping is simpler and doesn't bind this test to JSON shape.
		idText := findEnvID(t, device, name)
		mustOK(t, c, ctx, "env-rm", map[string]any{
			"id": idText, "device": true,
		})
	})

	t.Run("PinLifecycle", func(t *testing.T) {
		if release == "" || releaseAlt == "" {
			t.Skip("set BALENA_LIVE_RELEASE and BALENA_LIVE_RELEASE_ALT to two distinct final commits")
		}
		// Pin to the alt release, then back to the primary, then drop the
		// pin entirely. Exercises device-pin (twice — different release) and
		// device-track-fleet.
		mustOK(t, c, ctx, "device-pin", map[string]any{
			"uuid": device, "release": releaseAlt,
		})
		mustOK(t, c, ctx, "device-pin", map[string]any{
			"uuid": device, "release": release,
		})
		mustOK(t, c, ctx, "device-track-fleet", map[string]any{"uuid": device})
		// fleet-pin query mode — read current fleet pin without changing it.
		mustOK(t, c, ctx, "fleet-pin", map[string]any{"fleet": fleet})
	})

	t.Run("RenameRoundTrip", func(t *testing.T) {
		// Rename to a marker name and back. The original name comes from the
		// device JSON so the round trip restores exactly what was there.
		orig := deviceJSONField(t, device, "device_name")
		if orig == "" {
			t.Skip("could not read current device name from device JSON")
		}
		tmp := "livetest-rename-" + time.Now().UTC().Format("150405")
		mustOK(t, c, ctx, "device-rename", map[string]any{"uuid": device, "new_name": tmp})
		mustOK(t, c, ctx, "device-rename", map[string]any{"uuid": device, "new_name": orig})
	})

	t.Run("NoteRoundTrip", func(t *testing.T) {
		// device-note REPLACES the existing note, so capture and restore it.
		// An empty prior note cannot be restored through the tool (empty text
		// is rejected by design), so skip rather than clobber silently.
		orig := deviceJSONField(t, device, "note")
		if orig == "" {
			t.Skip("device has no note to restore afterwards; skipping rather than leaving a livetest marker behind")
		}
		mustOK(t, c, ctx, "device-note", map[string]any{
			"uuid": device, "note": "livetest marker " + time.Now().UTC().Format(time.RFC3339),
		})
		out := mustOK(t, c, ctx, "device-info", map[string]any{"uuid": device, "json": true})
		require.Contains(t, out, "livetest marker", "note did not surface in device-info")
		mustOK(t, c, ctx, "device-note", map[string]any{"uuid": device, "note": orig})
	})

	t.Run("LocalModeIdempotent", func(t *testing.T) {
		// Read the current state and set it to the same value: exercises the
		// real cloud round trip of device-local-mode-set without changing
		// anything. The get tool prints the CLI's status line.
		out := mustOK(t, c, ctx, "device-local-mode-get", map[string]any{"uuid": device})
		enabled := strings.Contains(strings.ToLower(out), "enabled") &&
			!strings.Contains(strings.ToLower(out), "disabled")
		mustOK(t, c, ctx, "device-local-mode-set", map[string]any{"uuid": device, "enable": enabled})
	})

	t.Run("ServiceStopStart", func(t *testing.T) {
		service := os.Getenv("BALENA_LIVE_SERVICE")
		if service == "" {
			t.Skip("set BALENA_LIVE_SERVICE to a service name on the device")
		}
		// Stop, then start — the container is down for the gap between the
		// two calls. Start is the restorative half, so run it even if the
		// assertion on stop's output were to fail later.
		mustOK(t, c, ctx, "device-stop-service", map[string]any{"uuid": device, "service": service})
		mustOK(t, c, ctx, "device-start-service", map[string]any{"uuid": device, "service": service})
	})

	t.Run("RegisterRoundTrip", func(t *testing.T) {
		if os.Getenv("BALENA_LIVE_ALLOW_REGISTER") != "1" {
			t.Skip("set BALENA_LIVE_ALLOW_REGISTER=1 to enable; registers then removes a phantom device")
		}
		// Register a phantom device (no hardware behind it), then remove it —
		// the only full lifecycle in the surface that is safe to round-trip.
		out := mustOK(t, c, ctx, "device-register", map[string]any{"fleet": fleet})
		uuid := lastField(out)
		require.NotEmpty(t, uuid, "device-register output carried no uuid: %s", truncate(out, 200))
		mustOK(t, c, ctx, "device-rm", map[string]any{"uuid": uuid})
	})

	t.Run("FleetCreateRenameRm", func(t *testing.T) {
		if os.Getenv("BALENA_LIVE_ALLOW_FLEET_CREATE") != "1" {
			t.Skip("set BALENA_LIVE_ALLOW_FLEET_CREATE=1 to enable; creates, renames and deletes a throwaway fleet")
		}
		org, _, ok := strings.Cut(fleet, "/")
		require.True(t, ok, "BALENA_LIVE_FLEET must be org/fleet to derive the org handle")
		name := "livetest-" + time.Now().UTC().Format("20060102t150405")
		mustOK(t, c, ctx, "fleet-create", map[string]any{
			"name": name, "organization": org, "type": deviceType,
		})
		renamed := name + "-r"
		mustOK(t, c, ctx, "fleet-rename", map[string]any{
			"fleet": org + "/" + name, "new_name": renamed,
		})
		mustOK(t, c, ctx, "fleet-rm", map[string]any{"fleet": org + "/" + renamed})
	})

	t.Run("AssetRoundTrip", func(t *testing.T) {
		if os.Getenv("BALENA_LIVE_ALLOW_ASSET") != "1" {
			t.Skip("set BALENA_LIVE_ALLOW_ASSET=1 to enable; mutates BALENA_LIVE_RELEASE's asset set")
		}
		if release == "" {
			t.Skip("set BALENA_LIVE_RELEASE to a final release commit")
		}
		// The filesystem tools are disabled without an asset root; Config is
		// package state, so point it at a temp dir for this sub-test only.
		dir := t.TempDir()
		origRoot := server.Config.AssetDir
		server.Config.AssetDir = dir
		defer func() { server.Config.AssetDir = origRoot }()

		key := "livetest-" + time.Now().UTC().Format("150405")
		require.NoError(t, os.WriteFile(dir+"/payload.txt", []byte("livetest asset\n"), 0o600))
		mustOK(t, c, ctx, "release-asset-upload", map[string]any{
			"release": release, "key": key, "file_path": "payload.txt",
		})
		out := mustOK(t, c, ctx, "release-asset-list", map[string]any{"id": release})
		require.Contains(t, out, key, "uploaded asset missing from release-asset-list")
		mustOK(t, c, ctx, "release-asset-download", map[string]any{
			"release": release, "key": key, "output": "roundtrip.txt",
		})
		back, err := os.ReadFile(dir + "/roundtrip.txt")
		require.NoError(t, err)
		require.Equal(t, "livetest asset\n", string(back), "downloaded asset differs from upload")
		mustOK(t, c, ctx, "release-asset-delete", map[string]any{"release": release, "key": key})
	})

	t.Run("SSHKeyRoundTrip", func(t *testing.T) {
		pub := os.Getenv("BALENA_LIVE_SSH_PUBKEY")
		if pub == "" {
			t.Skip("set BALENA_LIVE_SSH_PUBKEY to a public key line to add and remove")
		}
		dir := t.TempDir()
		origRoot := server.Config.AssetDir
		server.Config.AssetDir = dir
		defer func() { server.Config.AssetDir = origRoot }()
		require.NoError(t, os.WriteFile(dir+"/livetest.pub", []byte(pub+"\n"), 0o600))

		name := "livetest-" + time.Now().UTC().Format("150405")
		mustOK(t, c, ctx, "ssh-key-add", map[string]any{"name": name, "path": "livetest.pub"})
		// Find the new key's id via the list output and remove it again.
		out := mustOK(t, c, ctx, "ssh-key-list", nil)
		id := idNearName(out, name)
		require.NotZero(t, id, "added key %q not found in ssh-key-list output: %s", name, truncate(out, 300))
		mustOK(t, c, ctx, "ssh-key-info", map[string]any{"id": id})
		mustOK(t, c, ctx, "ssh-key-rm", map[string]any{"id": id})
	})

	t.Run("Restart", func(t *testing.T) {
		// device-restart kicks the containers — short interruption, ~10s recovery.
		mustOK(t, c, ctx, "device-restart", map[string]any{"uuid": device})
	})

	t.Run("Reboot", func(t *testing.T) {
		// device-reboot — longer interruption, ~30-60s recovery. We don't
		// poll for online state here; the next sub-test (or a subsequent
		// run) catches a stuck device by failing read-only calls.
		mustOK(t, c, ctx, "device-reboot", map[string]any{"uuid": device})
		t.Log("device-reboot dispatched — device will reconnect in ~30-60s")
	})

	// ----- destructive: irreversible (opt-in) -----------------------------
	t.Run("Purge", func(t *testing.T) {
		if os.Getenv("BALENA_LIVE_ALLOW_PURGE") != "1" {
			t.Skip("set BALENA_LIVE_ALLOW_PURGE=1 to enable; wipes /data on the device")
		}
		mustOK(t, c, ctx, "device-purge", map[string]any{"uuid": device})
	})

	t.Run("ReleaseFinalize_ErrorPath", func(t *testing.T) {
		// Always run — uses a bogus commit so it exercises only the error
		// branch in executeCommand. Producing a real draft release for this
		// test would require pushing a buildable Dockerfile to the fleet,
		// which is out of scope. The error path is the more interesting one
		// anyway since it's where one of the previously not-covered
		// gremlins mutants lives.
		mustErrorContaining(t, c, ctx, "release-finalize",
			map[string]any{"id": "0000000000000000000000000000000000000000"},
			"BalenaReleaseNotFound")
	})

	t.Run("Shutdown", func(t *testing.T) {
		// Last — once this fires, the device is offline until manual power
		// cycle. Sub-tests after this would have no live device to talk to.
		if os.Getenv("BALENA_LIVE_ALLOW_SHUTDOWN") != "1" {
			t.Skip("set BALENA_LIVE_ALLOW_SHUTDOWN=1 to enable; requires manual power cycle to recover")
		}
		mustOK(t, c, ctx, "device-shutdown", map[string]any{"uuid": device})
	})
}

// findEnvID shells out to `balena env list --device <uuid> --json` and
// returns the numeric id of the env var with the given name. Helper for
// EnvRoundTrip — we don't want to depend on the JSON shape balena's env-list
// emits to test env-rm, so we let a real balena call do the lookup.
//
// Returns an int (as required by env-rm's numeric "id" arg, which the JSON
// unmarshal in mcp-go will see as float64 anyway).
func findEnvID(t *testing.T, device, name string) float64 {
	t.Helper()
	// Direct exec rather than going through our MCP server again — keeps
	// the test focused on env-rm and avoids parsing env-list's table output.
	// We don't unmarshal the JSON because we'd have to bind to balena's
	// exact field shape; a targeted string search is robust enough for a
	// test helper.
	cmd := exec.Command("balena", "env", "list", "--device", device, "--json")
	raw, err := cmd.Output()
	require.NoError(t, err, "balena env list --json failed")
	out := string(raw)

	marker := fmt.Sprintf("%q:%q", "name", name)
	idx := strings.Index(out, marker)
	require.GreaterOrEqual(t, idx, 0, "env var %q not found in env-list JSON", name)

	// balena's JSON typically puts "id" before "name" in each object. Walk
	// backwards from the name marker to the nearest preceding "id" field.
	prefix := out[:idx]
	idIdx := strings.LastIndex(prefix, "\"id\":")
	require.GreaterOrEqual(t, idIdx, 0, "no id field preceding env var %q", name)

	rest := prefix[idIdx+len("\"id\":"):]
	end := strings.IndexAny(rest, ",}")
	require.GreaterOrEqual(t, end, 0, "malformed id field in env-list JSON")
	idStr := strings.TrimSpace(rest[:end])
	var id float64
	_, err = fmt.Sscanf(idStr, "%f", &id)
	require.NoError(t, err, "id %q is not numeric", idStr)
	return id
}

// deviceJSONField shells out to `balena device <uuid> --json` and pulls a
// top-level string field. Same rationale as findEnvID: a targeted string
// search instead of binding the test to balena's full JSON shape.
func deviceJSONField(t *testing.T, device, field string) string {
	t.Helper()
	cmd := exec.Command("balena", "device", device, "--json")
	raw, err := cmd.Output()
	require.NoError(t, err, "balena device --json failed")
	out := string(raw)
	marker := fmt.Sprintf("%q:", field)
	idx := strings.Index(out, marker)
	if idx < 0 {
		return ""
	}
	rest := out[idx+len(marker):]
	rest = strings.TrimLeft(rest, " \t")
	if !strings.HasPrefix(rest, "\"") {
		return "" // null or non-string
	}
	rest = rest[1:]
	end := strings.Index(rest, "\"")
	if end < 0 {
		return ""
	}
	return rest[:end]
}

// lastField returns the last whitespace-separated token of the last non-empty
// line — where `balena device register` prints the new device's uuid.
func lastField(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) == 0 {
		return ""
	}
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) == 0 {
		return ""
	}
	return fields[len(fields)-1]
}

// idNearName scans a ssh-key-list table for the row containing name and
// returns the first numeric token on that row (the key id), or 0.
func idNearName(out, name string) float64 {
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, name) {
			continue
		}
		for _, f := range strings.Fields(line) {
			var id float64
			if _, err := fmt.Sscanf(f, "%f", &id); err == nil && id > 0 {
				return id
			}
		}
	}
	return 0
}

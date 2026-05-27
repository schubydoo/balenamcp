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
			"id": idText, "device": true, "yes": true,
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

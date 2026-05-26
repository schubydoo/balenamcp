package main

import (
	"context"
	"strings"
	"testing"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/schubydoo/balenamcp/server"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newTestClient spins up the MCP server in dry-run mode and returns an
// in-process client ready to call tools.
func newTestClient(t *testing.T) (*mcpclient.Client, context.Context) {
	t.Helper()
	server.Config.DryRun = true
	srv := server.SetupServer()
	c, err := mcpclient.NewInProcessClient(srv)
	require.NoError(t, err)

	ctx := context.Background()
	require.NoError(t, c.Start(ctx))

	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{Name: "test", Version: "0.0.0"}
	_, err = c.Initialize(ctx, initReq)
	require.NoError(t, err)

	t.Cleanup(func() { _ = c.Close() })
	return c, ctx
}

// callTool invokes a tool and returns the resulting text payload. In dry-run
// mode the server returns "[DRY RUN] balena <argv...>" which we assert against.
func callTool(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any) string {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	require.NoError(t, err)
	require.NotEmpty(t, res.Content, "tool returned no content")

	text, ok := mcp.AsTextContent(res.Content[0])
	require.True(t, ok, "first content is not text: %T", res.Content[0])
	return text.Text
}

// expect asserts the dry-run output of a tool call contains the expected argv string.
func expect(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any, expectedArgv string) {
	t.Helper()
	got := callTool(t, c, ctx, name, args)
	assert.Contains(t, got, expectedArgv,
		"tool %q with args %v should produce CLI argv %q; got: %s", name, args, expectedArgv, got)
}

// expectNot is the companion to expect: assert the dry-run output does NOT
// contain a substring. Useful for catching mutations that silently widen the
// argv — e.g., a flipped default value that causes an optional flag to
// always be appended.
func expectNot(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any, forbiddenArgv string) {
	t.Helper()
	got := callTool(t, c, ctx, name, args)
	assert.NotContains(t, got, forbiddenArgv,
		"tool %q with args %v should NOT produce %q in argv; got: %s", name, args, forbiddenArgv, got)
}

// expectError asserts the tool returns a structured error containing `msg`.
func expectError(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any, msg string) {
	t.Helper()
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = args
	res, err := c.CallTool(ctx, req)
	require.NoError(t, err)
	require.True(t, res.IsError, "expected tool error, got success: %v", res.Content)

	text, ok := mcp.AsTextContent(res.Content[0])
	require.True(t, ok)
	assert.Contains(t, strings.ToLower(text.Text), strings.ToLower(msg))
}

func TestReadOnlyTools(t *testing.T) {
	c, ctx := newTestClient(t)

	expect(t, c, ctx, "version", nil, "balena version")
	expect(t, c, ctx, "whoami", nil, "balena whoami")
	expect(t, c, ctx, "organization-list", nil, "balena organization list")
	expect(t, c, ctx, "ssh-key-list", nil, "balena ssh-key list")

	// fleet-list
	expect(t, c, ctx, "fleet-list", nil, "balena fleet list")
	expect(t, c, ctx, "fleet-list", map[string]any{"json": true}, "balena fleet list --json")

	// fleet-info
	expect(t, c, ctx, "fleet-info",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet myorg/myfleet")
	expect(t, c, ctx, "fleet-info",
		map[string]any{"fleet": "myorg/myfleet", "json": true},
		"balena fleet myorg/myfleet --json")

	// device-list
	expect(t, c, ctx, "device-list", nil, "balena device list")
	expect(t, c, ctx, "device-list",
		map[string]any{"fleet": "my-fleet"},
		"balena device list --fleet my-fleet")
	expect(t, c, ctx, "device-list",
		map[string]any{"json": true},
		"balena device list --json")

	// device-info
	expect(t, c, ctx, "device-info",
		map[string]any{"uuid": "7cf02a6"},
		"balena device 7cf02a6")

	// device-logs
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device"},
		"balena device logs my-device")
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "service": "my-service"},
		"balena device logs my-device --service my-service")
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "system": true},
		"balena device logs my-device --system")
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "tail": true},
		"balena device logs my-device --tail")
	// max_retry boundary cases — gremlins caught that no test exercised the
	// `if v >= 0` arm. 0 is the documented "disable auto-reconnect" sentinel
	// (still >= 0, distinct from the absent case), 5 is a typical positive.
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "max_retry": float64(0)},
		"balena device logs my-device --max-retry 0")
	expect(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "max_retry": float64(5)},
		"balena device logs my-device --max-retry 5")
	// Negative assertion: when max_retry is absent, --max-retry must NOT
	// appear in argv. Catches mutations to the `-1` default sentinel that
	// would flip it positive and silently always-append the flag.
	expectNot(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device"},
		"--max-retry")

	// device-type-list
	expect(t, c, ctx, "device-type-list",
		map[string]any{"all": true, "json": true},
		"balena device-type list --all --json")

	// os-versions
	expect(t, c, ctx, "os-versions",
		map[string]any{"type": "raspberrypi4"},
		"balena os versions raspberrypi4")
	expect(t, c, ctx, "os-versions",
		map[string]any{"type": "raspberrypi4", "esr": true},
		"balena os versions raspberrypi4 --esr")
	expect(t, c, ctx, "os-versions",
		map[string]any{"type": "raspberrypi4", "include_draft": true},
		"balena os versions raspberrypi4 --include-draft")

	// release-list / release-info
	expect(t, c, ctx, "release-list",
		map[string]any{"fleet": "my-fleet"},
		"balena release list my-fleet")
	expect(t, c, ctx, "release-info",
		map[string]any{"id": "123"},
		"balena release 123")
	expect(t, c, ctx, "release-info",
		map[string]any{"id": "123", "composition": true},
		"balena release 123 --composition")

	// release-asset-list
	expect(t, c, ctx, "release-asset-list",
		map[string]any{"id": "123"},
		"balena release-asset list 123")

	// tag-list — exercises the flag-based form (was positional in the old fork)
	expect(t, c, ctx, "tag-list",
		map[string]any{"device": "7cf02a6"},
		"balena tag list --device 7cf02a6")
	expect(t, c, ctx, "tag-list",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena tag list --fleet myorg/myfleet")
	expect(t, c, ctx, "tag-list",
		map[string]any{"release": "1234"},
		"balena tag list --release 1234")

	// env-list
	expect(t, c, ctx, "env-list",
		map[string]any{"fleet": "my-fleet"},
		"balena env list --fleet my-fleet")
	expect(t, c, ctx, "env-list",
		map[string]any{"device": "7cf02a6", "service": "my-service"},
		"balena env list --device 7cf02a6 --service my-service")

	// api-key-list
	expect(t, c, ctx, "api-key-list", nil, "balena api-key list")
	expect(t, c, ctx, "api-key-list",
		map[string]any{"fleet": "my-fleet"},
		"balena api-key list --fleet my-fleet")
}

func TestMutatingTools(t *testing.T) {
	c, ctx := newTestClient(t)

	// device lifecycle
	expect(t, c, ctx, "device-reboot",
		map[string]any{"uuid": "7cf02a6"},
		"balena device reboot 7cf02a6")
	expect(t, c, ctx, "device-reboot",
		map[string]any{"uuid": "7cf02a6", "force": true},
		"balena device reboot 7cf02a6 --force")
	expect(t, c, ctx, "device-restart",
		map[string]any{"uuid": "7cf02a6", "service": "my-service"},
		"balena device restart 7cf02a6 --service my-service")
	expect(t, c, ctx, "device-shutdown",
		map[string]any{"uuid": "7cf02a6"},
		"balena device shutdown 7cf02a6")
	expect(t, c, ctx, "device-purge",
		map[string]any{"uuid": "7cf02a6"},
		"balena device purge 7cf02a6")

	// pin
	expect(t, c, ctx, "device-pin",
		map[string]any{"uuid": "7cf02a6", "release": "abc123"},
		"balena device pin 7cf02a6 abc123")
	expect(t, c, ctx, "fleet-pin",
		map[string]any{"fleet": "myorg/myfleet", "release": "abc123"},
		"balena fleet pin myorg/myfleet abc123")

	// release finalize
	expect(t, c, ctx, "release-finalize",
		map[string]any{"id": "123"},
		"balena release finalize 123")

	// tag-set / tag-rm
	expect(t, c, ctx, "tag-set",
		map[string]any{"key": "owner", "value": "ops", "fleet": "my-fleet"},
		"balena tag set owner ops --fleet my-fleet")
	expect(t, c, ctx, "tag-set",
		map[string]any{"key": "owner", "device": "7cf02a6"},
		"balena tag set owner --device 7cf02a6")
	expect(t, c, ctx, "tag-rm",
		map[string]any{"key": "owner", "fleet": "my-fleet"},
		"balena tag rm owner --fleet my-fleet")

	// env-set / env-rm
	expect(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "value": "1", "fleet": "my-fleet"},
		"balena env set DEBUG 1 --fleet my-fleet")
	// env-set with --service — gremlins flagged the `if service != "" {`
	// branch as lived because nothing exercised the truthy arm.
	expect(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "value": "1", "fleet": "my-fleet", "service": "api"},
		"balena env set DEBUG 1 --fleet my-fleet --service api")
	expect(t, c, ctx, "env-rm",
		map[string]any{"id": float64(42), "yes": true},
		"balena env rm 42 --yes")

	// Optional-arg branches: device-pin and fleet-pin both accept an optional
	// release; omitting it should produce just the verb + identifier.
	expect(t, c, ctx, "device-pin",
		map[string]any{"uuid": "7cf02a6"},
		"balena device pin 7cf02a6")
	expect(t, c, ctx, "fleet-pin",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet pin myorg/myfleet")
}

// TestConfirmGate_AllDestructiveTools sweeps the BALENAMCP_REQUIRE_CONFIRM
// gate across every destructive tool to confirm the requireConfirm guard is
// wired into each closure (and not just device-reboot, which the focused
// TestConfirmGate case covers). Cheaper than 11 hand-written subtests and
// guards against the "I added a new destructive tool but forgot the guard"
// regression class.
func TestConfirmGate_AllDestructiveTools(t *testing.T) {
	t.Setenv("BALENAMCP_REQUIRE_CONFIRM", "1")
	c, ctx := newTestClient(t)

	// Minimum args to get each tool past its own validation but still hit the
	// gate. Args intentionally avoid mutual-exclusion errors so the only
	// rejection cause should be the confirm gate.
	cases := []struct {
		tool string
		args map[string]any
	}{
		{"device-reboot", map[string]any{"uuid": "7cf02a6"}},
		{"device-restart", map[string]any{"uuid": "7cf02a6"}},
		{"device-shutdown", map[string]any{"uuid": "7cf02a6"}},
		{"device-purge", map[string]any{"uuid": "7cf02a6"}},
		{"device-pin", map[string]any{"uuid": "7cf02a6"}},
		{"fleet-pin", map[string]any{"fleet": "myorg/myfleet"}},
		{"release-finalize", map[string]any{"id": "123"}},
		{"tag-set", map[string]any{"key": "owner", "fleet": "my-fleet"}},
		{"tag-rm", map[string]any{"key": "owner", "fleet": "my-fleet"}},
		{"env-set", map[string]any{"name": "DEBUG", "fleet": "my-fleet"}},
		{"env-rm", map[string]any{"id": float64(42)}},
	}
	for _, tc := range cases {
		t.Run(tc.tool, func(t *testing.T) {
			expectError(t, c, ctx, tc.tool, tc.args, "requires explicit confirmation")
		})
	}
}

func TestErrors(t *testing.T) {
	c, ctx := newTestClient(t)

	// Missing required arg
	expectError(t, c, ctx, "device-info", nil, "uuid")
	expectError(t, c, ctx, "release-info", nil, "id")
	expectError(t, c, ctx, "device-reboot", nil, "uuid")

	// tag-list / tag-set / tag-rm — exactly-one-of fleet|device|release
	expectError(t, c, ctx, "tag-list", nil, "one of")
	expectError(t, c, ctx, "tag-list",
		map[string]any{"fleet": "f", "device": "d"}, "exactly one")
	expectError(t, c, ctx, "tag-set",
		map[string]any{"key": "owner"}, "one of")
	expectError(t, c, ctx, "tag-set",
		map[string]any{"key": "owner", "fleet": "f", "release": "r"}, "exactly one")
	expectError(t, c, ctx, "tag-rm",
		map[string]any{"key": "owner"}, "one of")

	// env-list / env-set — exactly-one-of fleet|device
	expectError(t, c, ctx, "env-list", nil, "one of")
	expectError(t, c, ctx, "env-list",
		map[string]any{"fleet": "f", "device": "d"}, "exactly one")
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "value": "1"}, "one of")
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "fleet": "f", "device": "d"}, "exactly one")

	// Flag-shape guard: identifiers that start with '-' must be rejected, both
	// on positional args and on flag-value args (via pickResource).
	expectError(t, c, ctx, "device-info",
		map[string]any{"uuid": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "tag-list",
		map[string]any{"fleet": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "release-finalize",
		map[string]any{"id": "-1"}, "cannot start with '-'")

	// env-list: --config + --service is rejected at the server, mirroring the
	// balena CLI's own exclusion rule.
	expectError(t, c, ctx, "env-list",
		map[string]any{"fleet": "my-fleet", "service": "svc", "config": true},
		"mutually exclusive")
}

// TestConfirmGate exercises BALENAMCP_REQUIRE_CONFIRM end-to-end via the env
// var (not by poking server.Config directly — newTestClient calls
// SetupServer which reloads from env, which is the realistic startup path).
// Each subtest gets its own client built under the env it cares about.
func TestConfirmGate(t *testing.T) {
	t.Run("gate-on-without-confirm-refused", func(t *testing.T) {
		t.Setenv("BALENAMCP_REQUIRE_CONFIRM", "1")
		c, ctx := newTestClient(t)
		expectError(t, c, ctx, "device-reboot",
			map[string]any{"uuid": "7cf02a6"},
			"requires explicit confirmation")
	})

	t.Run("gate-on-with-confirm-proceeds", func(t *testing.T) {
		t.Setenv("BALENAMCP_REQUIRE_CONFIRM", "1")
		c, ctx := newTestClient(t)
		expect(t, c, ctx, "device-reboot",
			map[string]any{"uuid": "7cf02a6", "confirm": true},
			"balena device reboot 7cf02a6")
	})

	t.Run("gate-off-confirm-irrelevant", func(t *testing.T) {
		t.Setenv("BALENAMCP_REQUIRE_CONFIRM", "0")
		c, ctx := newTestClient(t)
		expect(t, c, ctx, "device-reboot",
			map[string]any{"uuid": "7cf02a6"},
			"balena device reboot 7cf02a6")
	})
}

// TestAnnotationsInvariant asserts that every advertised tool carries exactly
// one of the read-only / destructive hints. mcp-go's default annotations have
// both fields preset (ReadOnlyHint=false, DestructiveHint=true), so a tool
// registered without going through our readOnly/destructive helpers would slip
// through as "destructive" even if it shouldn't be. This test catches that.
func TestAnnotationsInvariant(t *testing.T) {
	c, ctx := newTestClient(t)
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools, "no tools advertised")

	for _, tool := range res.Tools {
		ro := tool.Annotations.ReadOnlyHint
		de := tool.Annotations.DestructiveHint
		if ro == nil || de == nil {
			t.Errorf("tool %q has unset annotation hint (readOnly=%v destructive=%v)",
				tool.Name, ro, de)
			continue
		}
		if *ro == *de {
			t.Errorf("tool %q must have exactly one of readOnlyHint/destructiveHint true (got both=%v)",
				tool.Name, *ro)
		}
	}
}

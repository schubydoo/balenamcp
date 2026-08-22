package main

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
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

// expect asserts the dry-run output of a tool call is EXACTLY the expected
// argv. Exactness matters: a Contains assertion cannot see argv *widening*, so
// a regression that unconditionally appends --force to device-reboot — or any
// other stray flag — would pass a substring check. The behavioral audit built
// exactly those mutants and 31 of them survived the old Contains form.
func expect(t *testing.T, c *mcpclient.Client, ctx context.Context, name string, args map[string]any, expectedArgv string) {
	t.Helper()
	got := callTool(t, c, ctx, name, args)
	assert.Equal(t, "[DRY RUN] "+expectedArgv, got,
		"tool %q with args %v should produce exactly this CLI argv", name, args)
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

	// device-detect — LAN scan; optional --timeout/--verbose/--json.
	expect(t, c, ctx, "device-detect", nil, "balena device detect")
	expect(t, c, ctx, "device-detect",
		map[string]any{"json": true}, "balena device detect --json")
	expect(t, c, ctx, "device-detect",
		map[string]any{"timeout": float64(120), "verbose": true},
		"balena device detect --timeout 120 --verbose")
	// timeout absent must NOT emit --timeout (guards the -1 sentinel).
	expectNot(t, c, ctx, "device-detect", nil, "--timeout")
	// timeout=0 is a valid value and must be forwarded — pins the >= 0
	// boundary that a gremlins CONDITIONALS_BOUNDARY mutant flips to > 0.
	expect(t, c, ctx, "device-detect",
		map[string]any{"timeout": float64(0)},
		"balena device detect --timeout 0")

	// device-local-mode-get — read-only status query.
	expect(t, c, ctx, "device-local-mode-get",
		map[string]any{"uuid": "7cf02a6"},
		"balena device local-mode 7cf02a6 --status")
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
	// device-track-fleet is the inverse of device-pin — exercises the unpin
	// path the live sweep flagged as missing.
	expect(t, c, ctx, "device-track-fleet",
		map[string]any{"uuid": "7cf02a6"},
		"balena device track-fleet 7cf02a6")

	// device-ssh: one-shot command over the SSH gateway. The command travels
	// via stdin (rendered as <<<"...\nexit\n" in dry-run), not argv. Exact
	// assertions pin the argv AND the piped payload with its auto-appended
	// `exit` (which keeps the remote shell from hanging on stdin EOF), and
	// prove the command string never leaks into argv.
	expect(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "7cf02a6", "command": "cat /proc/meminfo"},
		`balena device ssh 7cf02a6 <<<"cat /proc/meminfo\nexit\n"`)
	// service-container target appends the service name as the second arg.
	expect(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "7cf02a6", "command": "ls", "service": "main"},
		`balena device ssh 7cf02a6 main <<<"ls\nexit\n"`)

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
	// env-rm — --yes is always passed (the CLI's confirm prompt cannot be
	// answered over MCP); selector booleans disambiguate the variable type.
	expect(t, c, ctx, "env-rm",
		map[string]any{"id": float64(42)},
		"balena env rm 42 --yes")
	expect(t, c, ctx, "env-rm",
		map[string]any{"id": float64(42), "device": true},
		"balena env rm 42 --device --yes")
	expect(t, c, ctx, "env-rm",
		map[string]any{"id": float64(42), "config": true},
		"balena env rm 42 --config --yes")

	// Optional-arg branches: device-pin and fleet-pin both accept an optional
	// release; omitting it should produce just the verb + identifier.
	expect(t, c, ctx, "device-pin",
		map[string]any{"uuid": "7cf02a6"},
		"balena device pin 7cf02a6")
	expect(t, c, ctx, "fleet-pin",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet pin myorg/myfleet")

	// release invalidate / validate
	expect(t, c, ctx, "release-invalidate",
		map[string]any{"id": "abc123"}, "balena release invalidate abc123")
	expect(t, c, ctx, "release-validate",
		map[string]any{"id": "abc123"}, "balena release validate abc123")

	// fleet lifecycle
	expect(t, c, ctx, "fleet-track-latest",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet track-latest myorg/myfleet")
	expect(t, c, ctx, "fleet-purge",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet purge myorg/myfleet")
	expect(t, c, ctx, "fleet-restart",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet restart myorg/myfleet")
	// fleet-rm always passes --yes (no interactive prompt over MCP).
	expect(t, c, ctx, "fleet-rm",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena fleet rm myorg/myfleet --yes")

	// ssh-key-rm always passes --yes.
	expect(t, c, ctx, "ssh-key-rm",
		map[string]any{"id": float64(17)}, "balena ssh-key rm 17 --yes")

	// device estate: rm / deactivate always pass --yes, move always sends
	// --fleet, register builds its optional flags.
	expect(t, c, ctx, "device-rm",
		map[string]any{"uuid": "7cf02a6"},
		"balena device rm 7cf02a6 --yes")
	expect(t, c, ctx, "device-deactivate",
		map[string]any{"uuid": "7cf02a6"},
		"balena device deactivate 7cf02a6 --yes")
	expect(t, c, ctx, "device-move",
		map[string]any{"uuid": "7cf02a6", "fleet": "myorg/newfleet"},
		"balena device move 7cf02a6 --fleet myorg/newfleet")
	expect(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet"},
		"balena device register myorg/myfleet")
	// --deviceType is camelCase upstream, unlike almost every other CLI flag.
	expect(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet", "uuid": "7cf02a6", "device_type": "raspberrypi4-64"},
		"balena device register myorg/myfleet --uuid 7cf02a6 --deviceType raspberrypi4-64")
	// each optional flag independently
	expect(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet", "uuid": "7cf02a6"},
		"balena device register myorg/myfleet --uuid 7cf02a6")
	expect(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet", "device_type": "raspberrypi4-64"},
		"balena device register myorg/myfleet --deviceType raspberrypi4-64")

	// device-os-update: --version is always sent (omitting it makes the CLI
	// render an interactive picker) and --yes is always sent (it confirms
	// twice, once more for takeover updates).
	expect(t, c, ctx, "device-os-update",
		map[string]any{"uuid": "7cf02a6", "version": "2.101.7"},
		"balena device os-update 7cf02a6 --version 2.101.7 --yes")

	// device-identify only blinks an LED, so it is read-only and needs no
	// confirmation even with BALENAMCP_REQUIRE_CONFIRM on.
	expect(t, c, ctx, "device-identify",
		map[string]any{"uuid": "7cf02a6"},
		"balena device identify 7cf02a6")

	// ssh-key-info takes a numeric ID, like env-rm.
	expect(t, c, ctx, "ssh-key-info",
		map[string]any{"id": float64(17)}, "balena ssh-key 17")

	// device-public-url read path: bare form prints the URL, --status reports
	// whether it is enabled.
	expect(t, c, ctx, "device-public-url",
		map[string]any{"uuid": "7cf02a6"},
		"balena device public-url 7cf02a6")
	expect(t, c, ctx, "device-public-url",
		map[string]any{"uuid": "7cf02a6", "status": true},
		"balena device public-url 7cf02a6 --status")

	// field-work device tools. device-note's argument shape is inverted: the
	// note is positional and the device is a flag.
	expect(t, c, ctx, "device-rename",
		map[string]any{"uuid": "7cf02a6", "new_name": "MyPi"},
		"balena device rename 7cf02a6 MyPi")
	expect(t, c, ctx, "device-note",
		map[string]any{"uuid": "7cf02a6", "note": "swapped the SD card"},
		"balena device note swapped the SD card --device 7cf02a6")
	// enable/disable are derived from one required bool, so the CLI's three
	// mutually exclusive flags cannot both be sent.
	expect(t, c, ctx, "device-public-url-set",
		map[string]any{"uuid": "7cf02a6", "enable": true},
		"balena device public-url 7cf02a6 --enable")
	expect(t, c, ctx, "device-public-url-set",
		map[string]any{"uuid": "7cf02a6", "enable": false},
		"balena device public-url 7cf02a6 --disable")

	// per-service container control
	expect(t, c, ctx, "device-start-service",
		map[string]any{"uuid": "7cf02a6", "service": "myService"},
		"balena device start-service 7cf02a6 myService")
	expect(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "7cf02a6", "service": "myService"},
		"balena device stop-service 7cf02a6 myService")

	// fleet create / rename. Both --organization and --type are always sent:
	// the CLI shows an interactive dropdown when either is omitted and the
	// account has more than one candidate, which would hang the call.
	expect(t, c, ctx, "fleet-create",
		map[string]any{"name": "MyFleet", "organization": "myorg", "type": "raspberrypi4-64"},
		"balena fleet create MyFleet --organization myorg --type raspberrypi4-64")
	expect(t, c, ctx, "fleet-rename",
		map[string]any{"fleet": "myorg/oldname", "new_name": "NewName"},
		"balena fleet rename myorg/oldname NewName")

	// organization create / rename / rm
	expect(t, c, ctx, "organization-create",
		map[string]any{"name": "acme"}, "balena organization create acme")
	expect(t, c, ctx, "organization-rename",
		map[string]any{"handle": "acme", "new_name": "acme2"},
		"balena organization rename acme acme2")
	expect(t, c, ctx, "organization-rm",
		map[string]any{"handle": "acme"}, "balena organization rm acme --yes")

	// device-local-mode-set — enable vs disable map to distinct flags.
	expect(t, c, ctx, "device-local-mode-set",
		map[string]any{"uuid": "7cf02a6", "enable": true},
		"balena device local-mode 7cf02a6 --enable")
	expect(t, c, ctx, "device-local-mode-set",
		map[string]any{"uuid": "7cf02a6", "enable": false},
		"balena device local-mode 7cf02a6 --disable")
	expectNot(t, c, ctx, "device-local-mode-set",
		map[string]any{"uuid": "7cf02a6", "enable": true},
		"--disable")

	// env-rename — updates value by numeric ID; selector booleans.
	expect(t, c, ctx, "env-rename",
		map[string]any{"id": float64(42), "value": "newval"},
		"balena env rename 42 newval")
	expect(t, c, ctx, "env-rename",
		map[string]any{"id": float64(42), "value": "newval", "device": true, "config": true},
		"balena env rename 42 newval --device --config")

	// api-key-revoke — single comma-separated positional, never split. This is
	// the one deliberate exception to the one-device-per-call rule enforced by
	// rejectMultiTarget: the CLI command is inherently list-shaped and its
	// targets are credentials, not devices, so batching buys no safety. If the
	// guard is ever applied blanket-wise, this assertion fails first.
	expect(t, c, ctx, "api-key-revoke",
		map[string]any{"ids": "123,456"}, "balena api-key revoke 123,456")
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
		{"device-track-fleet", map[string]any{"uuid": "7cf02a6"}},
		{"device-ssh", map[string]any{"uuid": "7cf02a6", "command": "ls"}},
		{"fleet-pin", map[string]any{"fleet": "myorg/myfleet"}},
		{"release-finalize", map[string]any{"id": "123"}},
		{"tag-set", map[string]any{"key": "owner", "fleet": "my-fleet"}},
		{"tag-rm", map[string]any{"key": "owner", "fleet": "my-fleet"}},
		{"env-set", map[string]any{"name": "DEBUG", "value": "1", "fleet": "my-fleet"}},
		{"env-rm", map[string]any{"id": float64(42)}},
		{"env-rename", map[string]any{"id": float64(42), "value": "x"}},
		{"release-invalidate", map[string]any{"id": "abc123"}},
		{"release-validate", map[string]any{"id": "abc123"}},
		{"fleet-track-latest", map[string]any{"fleet": "myorg/myfleet"}},
		{"fleet-purge", map[string]any{"fleet": "myorg/myfleet"}},
		{"fleet-restart", map[string]any{"fleet": "myorg/myfleet"}},
		{"fleet-rm", map[string]any{"fleet": "myorg/myfleet"}},
		{"ssh-key-add", map[string]any{"name": "Main", "path": "id_rsa.pub"}},
		{"ssh-key-rm", map[string]any{"id": float64(17)}},
		{"release-asset-download", map[string]any{"release": "abc123", "key": "cfg", "output": "a.bin"}},
		{"release-asset-upload", map[string]any{"release": "abc123", "key": "cfg", "file_path": "a.bin"}},
		{"release-asset-delete", map[string]any{"release": "abc123", "key": "cfg"}},
		{"device-rm", map[string]any{"uuid": "7cf02a6"}},
		{"device-deactivate", map[string]any{"uuid": "7cf02a6"}},
		{"device-move", map[string]any{"uuid": "7cf02a6", "fleet": "myorg/newfleet"}},
		{"device-register", map[string]any{"fleet": "myorg/myfleet"}},
		{"device-os-update", map[string]any{"uuid": "7cf02a6", "version": "2.101.7"}},
		{"device-rename", map[string]any{"uuid": "7cf02a6", "new_name": "MyPi"}},
		{"device-note", map[string]any{"uuid": "7cf02a6", "note": "hi"}},
		{"device-public-url-set", map[string]any{"uuid": "7cf02a6", "enable": true}},
		{"device-start-service", map[string]any{"uuid": "7cf02a6", "service": "myService"}},
		{"device-stop-service", map[string]any{"uuid": "7cf02a6", "service": "myService"}},
		{"fleet-create", map[string]any{"name": "MyFleet", "organization": "myorg", "type": "raspberrypi4-64"}},
		{"fleet-rename", map[string]any{"fleet": "myorg/oldname", "new_name": "NewName"}},
		{"organization-create", map[string]any{"name": "acme"}},
		{"organization-rename", map[string]any{"handle": "acme", "new_name": "acme2"}},
		{"organization-rm", map[string]any{"handle": "acme"}},
		{"device-local-mode-set", map[string]any{"uuid": "7cf02a6", "enable": true}},
		{"api-key-revoke", map[string]any{"ids": "123"}},
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

	// device-ssh: uuid present but command empty/missing is a handler error,
	// and a flag-shaped uuid is rejected like any other identifier.
	expectError(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "7cf02a6"}, "command")
	expectError(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "7cf02a6", "command": "   "}, "command")
	expectError(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "--help", "command": "ls"}, "cannot start with '-'")

	// env-rename: --config and --service are mutually exclusive; empty value
	// is rejected; flag-shaped value is fine (free-form) but missing value errors.
	expectError(t, c, ctx, "env-rename",
		map[string]any{"id": float64(42), "value": "x", "config": true, "service": true},
		"mutually exclusive")
	expectError(t, c, ctx, "env-rename",
		map[string]any{"id": float64(42)}, "value")

	// device-local-mode-set: enable is required.
	expectError(t, c, ctx, "device-local-mode-set",
		map[string]any{"uuid": "7cf02a6"}, "enable")

	// flag-shape guard reaches the new identifier-shaped args too.
	expectError(t, c, ctx, "fleet-rm",
		map[string]any{"fleet": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "api-key-revoke",
		map[string]any{"ids": "-1"}, "cannot start with '-'")
	// optional/second identifiers on new tools must also be flag-shape guarded
	// (these are the branches that survive mutation when only the first guard
	// is tested).
	expectError(t, c, ctx, "device-ssh",
		map[string]any{"uuid": "7cf02a6", "command": "ls", "service": "-foo"},
		"cannot start with '-'")
	expectError(t, c, ctx, "organization-rename",
		map[string]any{"handle": "acme", "new_name": "-foo"},
		"cannot start with '-'")
	// fleet-create guards all three identifiers independently; each is its own
	// branch and each survives mutation if only the first is exercised.
	expectError(t, c, ctx, "fleet-create",
		map[string]any{"name": "-f", "organization": "myorg", "type": "raspberrypi4-64"},
		"cannot start with '-'")
	expectError(t, c, ctx, "fleet-create",
		map[string]any{"name": "MyFleet", "organization": "-o", "type": "raspberrypi4-64"},
		"cannot start with '-'")
	expectError(t, c, ctx, "fleet-create",
		map[string]any{"name": "MyFleet", "organization": "myorg", "type": "-t"},
		"cannot start with '-'")
	// ...and organization/type are required, so the CLI never reaches an
	// interactive dropdown.
	expectError(t, c, ctx, "fleet-create",
		map[string]any{"name": "MyFleet"}, "organization")
	expectError(t, c, ctx, "fleet-create",
		map[string]any{"name": "MyFleet", "organization": "myorg"}, "type")
	// The one-device-per-call guard covers the estate tools too.
	expectError(t, c, ctx, "device-rm",
		map[string]any{"uuid": "7cf02a6,dc39e52"}, "single target per call")
	expectError(t, c, ctx, "device-deactivate",
		map[string]any{"uuid": "7cf02a6,dc39e52"}, "single target per call")
	expectError(t, c, ctx, "device-move",
		map[string]any{"uuid": "7cf02a6,dc39e52", "fleet": "myorg/newfleet"},
		"single target per call")
	// device-move: the target fleet is required, or the CLI prompts.
	expectError(t, c, ctx, "device-move",
		map[string]any{"uuid": "7cf02a6"}, "fleet")
	expectError(t, c, ctx, "device-move",
		map[string]any{"uuid": "7cf02a6", "fleet": "-f"},
		"cannot start with '-'")
	// device-register: both optional identifiers are flag-shape guarded, and
	// each is its own branch.
	expectError(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet", "uuid": "-u"},
		"cannot start with '-'")
	expectError(t, c, ctx, "device-register",
		map[string]any{"fleet": "myorg/myfleet", "device_type": "-t"},
		"cannot start with '-'")

	// ssh-key ids are numeric, so a non-integer hits RequireInt's branch.
	expectError(t, c, ctx, "ssh-key-info", map[string]any{}, "id")
	expectError(t, c, ctx, "ssh-key-rm", map[string]any{}, "id")

	// device-os-update: version is required, or the CLI prompts.
	expectError(t, c, ctx, "device-os-update",
		map[string]any{"uuid": "7cf02a6"}, "version")
	expectError(t, c, ctx, "device-os-update",
		map[string]any{"uuid": "7cf02a6", "version": "-v"},
		"cannot start with '-'")

	// device-rename / device-note / device-public-url-set: every argument the
	// CLI would otherwise prompt for, infer, or misparse is required here.
	expectError(t, c, ctx, "device-rename",
		map[string]any{"uuid": "7cf02a6"}, "new_name")
	expectError(t, c, ctx, "device-rename",
		map[string]any{"uuid": "7cf02a6", "new_name": "-n"},
		"cannot start with '-'")
	expectError(t, c, ctx, "device-note",
		map[string]any{"uuid": "7cf02a6"}, "note")
	// a note is free-form text, but it lands in argv as a positional, so a
	// leading dash would be parsed as a flag rather than content.
	expectError(t, c, ctx, "device-note",
		map[string]any{"uuid": "7cf02a6", "note": "-- see ticket 12"},
		"cannot start with '-'")
	expectError(t, c, ctx, "device-note",
		map[string]any{"uuid": "7cf02a6", "note": ""}, "note is empty")
	expectError(t, c, ctx, "device-public-url-set",
		map[string]any{"uuid": "7cf02a6"}, "enable")
	expectError(t, c, ctx, "device-identify",
		map[string]any{"uuid": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "device-public-url",
		map[string]any{"uuid": "--help"}, "cannot start with '-'")

	// The multi-target guard also covers the two tools that predated it.
	// device-purge is the sharp one: without this, a single confirmation could
	// wipe /data on every device in the list.
	expectError(t, c, ctx, "device-purge",
		map[string]any{"uuid": "7cf02a6,55d43b3"},
		"single target per call")
	expectError(t, c, ctx, "device-restart",
		map[string]any{"uuid": "7cf02a6,55d43b3"},
		"single target per call")
	// device-restart's service arg is optional, so it goes through the
	// getSingleTarget branch rather than requireSingleTarget.
	expectError(t, c, ctx, "device-restart",
		map[string]any{"uuid": "7cf02a6", "service": "svc1,svc2"},
		"single target per call")

	// The multi-target guard rejects comma-separated lists on BOTH positional
	// args of both per-service tools. Each is a separate branch.
	expectError(t, c, ctx, "device-start-service",
		map[string]any{"uuid": "7cf02a6,55d43b3", "service": "myService"},
		"single target per call")
	expectError(t, c, ctx, "device-start-service",
		map[string]any{"uuid": "7cf02a6", "service": "svc1,svc2"},
		"single target per call")
	expectError(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "7cf02a6,55d43b3", "service": "myService"},
		"single target per call")
	expectError(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "7cf02a6", "service": "svc1,svc2"},
		"single target per call")
	// ...and both args stay required and flag-shape guarded.
	expectError(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "7cf02a6"}, "service")
	expectError(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "-h", "service": "myService"},
		"cannot start with '-'")
	expectError(t, c, ctx, "device-stop-service",
		map[string]any{"uuid": "7cf02a6", "service": "-s"},
		"cannot start with '-'")

	// fleet-rename: new_name is required (the CLI would otherwise prompt) and
	// flag-shape guarded.
	expectError(t, c, ctx, "fleet-rename",
		map[string]any{"fleet": "myorg/oldname"}, "new_name")
	expectError(t, c, ctx, "fleet-rename",
		map[string]any{"fleet": "myorg/oldname", "new_name": "-foo"},
		"cannot start with '-'")
	expectError(t, c, ctx, "device-local-mode-get",
		map[string]any{"uuid": "--help"}, "cannot start with '-'")
	// env-rename: missing numeric id hits the RequireInt error branch.
	expectError(t, c, ctx, "env-rename",
		map[string]any{"value": "x"}, "id")
	// env-rm: same RequireInt branch, plus the config/service exclusion that
	// env-rename has always carried.
	expectError(t, c, ctx, "env-rm", map[string]any{}, "id")
	expectError(t, c, ctx, "env-rm",
		map[string]any{"id": float64(42), "config": true, "service": true},
		"mutually exclusive")
	// env-set: value is required and non-empty — an omitted/empty value would
	// make the CLI copy the SERVER's own env var of that name to balenaCloud.
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "fleet": "my-fleet"}, "non-empty 'value'")
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "value": "", "fleet": "my-fleet"},
		"non-empty 'value'")
	// release-info: composition output is always YAML, so json cannot be
	// combined with it.
	expectError(t, c, ctx, "release-info",
		map[string]any{"id": "abc123", "composition": true, "json": true},
		"mutually exclusive")

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

	// Flag-shape guard, per-tool wiring. The helper itself is unit-tested, but
	// the behavioral audit showed 18 of its callsites could be replaced with
	// guard-free lookups and no test failed — i.e. each tool's own wiring was
	// unpinned. One row per audited callsite; every input is an identifier an
	// agent could plausibly pass ("--help", "-f", …) that must never reach the
	// balena CLI as argv.
	expectError(t, c, ctx, "fleet-info",
		map[string]any{"fleet": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "device-list",
		map[string]any{"fleet": "-f"}, "cannot start with '-'")
	expectError(t, c, ctx, "device-logs",
		map[string]any{"device": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "device-logs",
		map[string]any{"device": "7cf02a6", "service": "-s"}, "cannot start with '-'")
	expectError(t, c, ctx, "os-versions",
		map[string]any{"type": "--all"}, "cannot start with '-'")
	expectError(t, c, ctx, "release-list",
		map[string]any{"fleet": "-f"}, "cannot start with '-'")
	expectError(t, c, ctx, "release-info",
		map[string]any{"id": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "release-asset-list",
		map[string]any{"id": "--help"}, "cannot start with '-'")
	expectError(t, c, ctx, "env-list",
		map[string]any{"fleet": "f", "service": "-s"}, "cannot start with '-'")
	expectError(t, c, ctx, "api-key-list",
		map[string]any{"fleet": "-f"}, "cannot start with '-'")
	expectError(t, c, ctx, "device-pin",
		map[string]any{"uuid": "7cf02a6", "release": "-r"}, "cannot start with '-'")
	expectError(t, c, ctx, "fleet-pin",
		map[string]any{"fleet": "myorg/myfleet", "release": "-r"}, "cannot start with '-'")
	expectError(t, c, ctx, "tag-set",
		map[string]any{"key": "-k", "fleet": "my-fleet"}, "cannot start with '-'")
	expectError(t, c, ctx, "tag-rm",
		map[string]any{"key": "-k", "fleet": "my-fleet"}, "cannot start with '-'")
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "-n", "value": "1", "fleet": "my-fleet"}, "cannot start with '-'")
	expectError(t, c, ctx, "env-set",
		map[string]any{"name": "DEBUG", "value": "1", "fleet": "my-fleet", "service": "-s"},
		"cannot start with '-'")
	// assetTarget is the shared preamble for all three release-asset tools, so
	// these two rows close the release and key guards across the family.
	expectError(t, c, ctx, "release-asset-delete",
		map[string]any{"release": "-r", "key": "k"}, "cannot start with '-'")
	expectError(t, c, ctx, "release-asset-delete",
		map[string]any{"release": "abc123", "key": "-k"}, "cannot start with '-'")

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

	// device-logs: tail:true is refused server-side. The schema deliberately
	// omits `tail`, but a non-compliant client could still send it; the guard
	// converts it to a structured error pointing the caller at the right path.
	expectError(t, c, ctx, "device-logs",
		map[string]any{"device": "my-device", "tail": true},
		"does not support streaming")
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

// TestReleaseAssetTools covers the only tools that touch the host filesystem.
// The path confinement is the whole security boundary, so the refusals matter
// at least as much as the happy path.
func TestReleaseAssetTools(t *testing.T) {
	root := t.TempDir()
	t.Setenv("BALENAMCP_ASSET_DIR", root)
	c, ctx := newTestClient(t)

	// Expectations must use the canonical form of the root: macOS resolves
	// /var to /private/var and Windows expands 8.3 short names, so the argv
	// the server builds will not match a raw t.TempDir() string.
	canonical, err := filepath.EvalSymlinks(root)
	require.NoError(t, err)

	// delete is a pure cloud call and always passes --yes.
	expect(t, c, ctx, "release-asset-delete",
		map[string]any{"release": "abc123", "key": "config.json"},
		"balena release-asset delete abc123 --key config.json --yes")

	// download resolves output against the root and only sends --overwrite
	// when asked.
	expect(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "config.json", "output": "cfg.json"},
		"balena release-asset download abc123 --key config.json --output "+filepath.Join(canonical, "cfg.json"))
	expect(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "config.json", "output": "sub/cfg.json", "overwrite": true},
		"balena release-asset download abc123 --key config.json --output "+
			filepath.Join(canonical, "sub/cfg.json")+" --overwrite")

	// upload requires the file to exist inside the root.
	require.NoError(t, os.WriteFile(filepath.Join(root, "app.tar.gz"), []byte("x"), 0o600))
	payload := filepath.Join(canonical, "app.tar.gz")
	expect(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "app", "file_path": "app.tar.gz"},
		"balena release-asset upload abc123 "+payload+" --key app")
	expect(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "app", "file_path": "app.tar.gz", "overwrite": true},
		"balena release-asset upload abc123 "+payload+" --key app --overwrite")

	// ssh-key-add reads a host file, so it shares the release-asset boundary.
	require.NoError(t, os.WriteFile(filepath.Join(root, "id_rsa.pub"), []byte("ssh-rsa AAAA"), 0o600))
	expect(t, c, ctx, "ssh-key-add",
		map[string]any{"name": "Main", "path": "id_rsa.pub"},
		"balena ssh-key add Main "+filepath.Join(canonical, "id_rsa.pub"))

	// ----- refusals -----

	// download will not silently clobber an existing file.
	require.NoError(t, os.WriteFile(filepath.Join(root, "taken.bin"), []byte("x"), 0o600))
	expectError(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "k", "output": "taken.bin"},
		"already exists")

	// upload refuses a missing file and a directory.
	expectError(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "k", "file_path": "nope.bin"},
		"not readable")
	require.NoError(t, os.Mkdir(filepath.Join(root, "adir"), 0o700))
	expectError(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "k", "file_path": "adir"},
		"is a directory")

	// path confinement, exercised through a real tool rather than only the
	// helper: absolute paths, traversal, and symlink escape.
	expectError(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "k", "output": filepath.Join(root, "abs.bin")},
		"must be relative")
	expectError(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "k", "output": "../escape.bin"},
		"escapes BALENAMCP_ASSET_DIR")
	expectError(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "k", "file_path": "../../etc/passwd"},
		"escapes BALENAMCP_ASSET_DIR")

	expectError(t, c, ctx, "ssh-key-add",
		map[string]any{"name": "Main", "path": "../../.ssh/id_rsa"},
		"escapes BALENAMCP_ASSET_DIR")
	expectError(t, c, ctx, "ssh-key-add",
		map[string]any{"name": "Main", "path": "missing.pub"},
		"not readable")
	expectError(t, c, ctx, "ssh-key-add",
		map[string]any{"name": "Main", "path": "adir"},
		"is a directory")

	// A regular file used as a directory component. On POSIX EvalSymlinks
	// fails with ENOTDIR (non-NotExist), so the resolver's "cannot be
	// resolved" branch fires; on Windows the same input is PATH_NOT_FOUND
	// (IsNotExist), the walk-up succeeds, and the upload handler's os.Stat
	// backstop rejects it as unreadable instead. Fail-closed either way —
	// assert the layer each platform actually uses.
	fileAsDirErr := "cannot be resolved"
	if runtime.GOOS == "windows" {
		fileAsDirErr = "not readable"
	}
	expectError(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "k", "file_path": "app.tar.gz/child"},
		fileAsDirErr)
	// A symlink loop inside the root is the other deterministic trigger.
	if err := os.Symlink(filepath.Join(root, "loop-b"), filepath.Join(root, "loop-a")); err == nil {
		require.NoError(t, os.Symlink(filepath.Join(root, "loop-a"), filepath.Join(root, "loop-b")))
		expectError(t, c, ctx, "release-asset-download",
			map[string]any{"release": "abc123", "key": "k", "output": "loop-a/x.bin"},
			"cannot be resolved")
	}
	// "." resolves to the asset root itself. Pinned as ALLOWED: it stays
	// inside the boundary, and the CLI fails cleanly on a directory target,
	// so refusing it here would add a rule without adding safety.
	expect(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "k", "output": ".", "overwrite": true},
		"balena release-asset download abc123 --key k --output "+canonical+" --overwrite")

	outside := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(outside, "secret"), []byte("s"), 0o600))
	if err := os.Symlink(outside, filepath.Join(root, "link")); err == nil {
		expectError(t, c, ctx, "release-asset-upload",
			map[string]any{"release": "abc123", "key": "k", "file_path": "link/secret"},
			"symbolic link")
	}
}

// TestReleaseAssetToolsDisabled asserts the filesystem tools fail closed when
// BALENAMCP_ASSET_DIR is unset, while the pure cloud one keeps working.
func TestReleaseAssetToolsDisabled(t *testing.T) {
	t.Setenv("BALENAMCP_ASSET_DIR", "")
	c, ctx := newTestClient(t)

	expectError(t, c, ctx, "release-asset-download",
		map[string]any{"release": "abc123", "key": "k", "output": "cfg.json"},
		"filesystem access is disabled")
	expectError(t, c, ctx, "release-asset-upload",
		map[string]any{"release": "abc123", "key": "k", "file_path": "cfg.json"},
		"filesystem access is disabled")
	expectError(t, c, ctx, "ssh-key-add",
		map[string]any{"name": "Main", "path": "id_rsa.pub"},
		"filesystem access is disabled")
	expect(t, c, ctx, "release-asset-delete",
		map[string]any{"release": "abc123", "key": "k"},
		"balena release-asset delete abc123 --key k --yes")
}

// TestConfirmSchemaAdvertised pins the `confirm` discovery contract: every
// destructive tool must advertise the confirm bool in its input schema (that
// is how clients learn about BALENAMCP_REQUIRE_CONFIRM without reading
// source), and no read-only tool may carry one. Renaming the field in the
// destructive() helper previously survived both the Go suite and CI's
// mcp-smoke, which only spot-checks two tools' hints.
func TestConfirmSchemaAdvertised(t *testing.T) {
	c, ctx := newTestClient(t)
	res, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	require.NoError(t, err)
	require.NotEmpty(t, res.Tools)

	for _, tool := range res.Tools {
		de := tool.Annotations.DestructiveHint
		require.NotNil(t, de, "tool %q missing destructive hint", tool.Name)
		_, hasConfirm := tool.InputSchema.Properties["confirm"]
		if *de && !hasConfirm {
			t.Errorf("destructive tool %q does not advertise the confirm field", tool.Name)
		}
		if !*de && hasConfirm {
			t.Errorf("read-only tool %q advertises a confirm field it never reads", tool.Name)
		}
	}
}

// TestPromptRegistrationWiring drives prompts/get through the real server for
// every advertised prompt, asserting a substring unique to that prompt's
// template. Handlers were previously tested only in isolation, so swapping
// two handlers in the registration list (or dropping a required-argument
// marker) passed the Go suite.
func TestPromptRegistrationWiring(t *testing.T) {
	c, ctx := newTestClient(t)

	cases := []struct {
		name   string
		args   map[string]string
		unique string // substring that exists in this prompt's template only
	}{
		{"diagnose-device", map[string]string{"uuid": "u1"}, "Do not take any destructive action"},
		{"deep-diagnose-device", map[string]string{"uuid": "u1"}, "Do not run mutating shell commands"},
		{"fleet-health-report", map[string]string{"fleet": "o/f"}, "Produce a health report"},
		{"safe-release-rollout", map[string]string{"fleet": "o/f", "release": "r1"}, "canary"},
		{"rollback-device", map[string]string{"uuid": "u1"}, "Do not apply the pin without explicit user approval"},
		{"audit-config", map[string]string{"fleet": "o/f"}, "Never print secret values in full; mask them"},
		{"compare-releases", map[string]string{"release_a": "a", "release_b": "b"}, "does NOT include per-service image sizes"},
		{"replicate-config", map[string]string{"source": "o/a", "target": "o/b"}, "MASKING any secret-shaped values"},
		{"bulk-tag", map[string]string{"fleet": "o/f", "key": "k"}, "tag-set"},
		{"prepare-local-dev", map[string]string{"uuid": "u1"}, "local mode"},
		{"rotate-api-keys", nil, "Never revoke a key the user has not explicitly named"},
	}

	listed, err := c.ListPrompts(ctx, mcp.ListPromptsRequest{})
	require.NoError(t, err)
	require.Len(t, listed.Prompts, len(cases), "prompt inventory drifted; update this table")

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := mcp.GetPromptRequest{}
			req.Params.Name = tc.name
			req.Params.Arguments = tc.args
			res, err := c.GetPrompt(ctx, req)
			require.NoError(t, err)
			require.NotEmpty(t, res.Messages)
			txt, ok := mcp.AsTextContent(res.Messages[0].Content)
			require.True(t, ok)
			// The unique substring proves name→handler binding; several of
			// them are the prompt's SAFETY clause, so deleting a guardrail
			// sentence fails here too.
			assert.Contains(t, txt.Text, tc.unique)
		})
	}
}

// TestResourceRegistrationWiring reads every static resource and template
// instance through the real server in dry-run mode. Dry-run section output
// embeds the argv each section would run, so asserting argv substrings pins
// the URI→handler binding: the fleet/releases template swapped to the full
// fleet handler previously survived the Go suite AND CI's smoke probes.
func TestResourceRegistrationWiring(t *testing.T) {
	c, ctx := newTestClient(t)

	cases := []struct {
		uri      string
		contains []string
		absent   []string
	}{
		{"balena://account", []string{"whoami", "organization list"}, nil},
		{"balena://account/keys", []string{"ssh-key list", "api-key list"}, nil},
		{"balena://fleets", []string{"fleet list --json"}, nil},
		{"balena://device-types", []string{"device-type list --json"}, nil},
		{"balena://gotchas", []string{"balena CLI gotchas"}, nil},
		{"balena://device/dev1", []string{"device dev1 --json", "device logs dev1", "env list --device dev1"}, nil},
		{"balena://fleet/o/f", []string{"fleet o/f --json", "device list --fleet o/f", "release list o/f"}, nil},
		// The /releases template must serve ONLY the release history — the
		// absent list is what distinguishes it from the full fleet snapshot.
		{"balena://fleet/o/f/releases", []string{"release list o/f"}, []string{"device list --fleet"}},
		{"balena://release/rel9", []string{"release rel9 --json", "release rel9 --composition", "release-asset list rel9"}, nil},
		{"balena://os-versions/raspberrypi4", []string{"os versions raspberrypi4", "--esr", "--include-draft"}, nil},
	}

	for _, tc := range cases {
		t.Run(tc.uri, func(t *testing.T) {
			req := mcp.ReadResourceRequest{}
			req.Params.URI = tc.uri
			res, err := c.ReadResource(ctx, req)
			require.NoError(t, err)
			require.NotEmpty(t, res.Contents)
			txt, ok := res.Contents[0].(mcp.TextResourceContents)
			require.True(t, ok, "contents not text: %T", res.Contents[0])
			for _, want := range tc.contains {
				assert.Contains(t, txt.Text, want)
			}
			for _, no := range tc.absent {
				assert.NotContains(t, txt.Text, no)
			}
		})
	}
}

// TestSetupFlagParsing covers the flag-parse → config → SetupServer path that
// main() delegates to. main itself is a two-line shim around ServeStdio,
// which cannot run under go test.
func TestSetupFlagParsing(t *testing.T) {
	origDry := server.Config.DryRun
	t.Cleanup(func() { server.Config.DryRun = origDry })

	var buf strings.Builder

	srv, err := setup([]string{"-dry-run"}, &buf)
	require.NoError(t, err)
	require.NotNil(t, srv)
	assert.True(t, server.Config.DryRun, "-dry-run must set Config.DryRun")

	srv, err = setup(nil, &buf)
	require.NoError(t, err)
	require.NotNil(t, srv)
	assert.False(t, server.Config.DryRun, "no flag must leave dry-run off")

	// An unknown flag is a parse error, reported on the provided writer.
	_, err = setup([]string{"-no-such-flag"}, &buf)
	require.Error(t, err)
	assert.Contains(t, buf.String(), "no-such-flag")
}

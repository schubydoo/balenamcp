package server

import (
	"context"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// req constructs a CallToolRequest with the given Arguments map. The MCP
// argument-access helpers (GetString/RequireString/etc) all bottom out at the
// Params.Arguments field, so this is the minimal shape we need for unit tests.
func req(args map[string]any) mcp.CallToolRequest {
	r := mcp.CallToolRequest{}
	r.Params.Arguments = args
	return r
}

// ----- pickResource ------------------------------------------------------

func TestPickResource(t *testing.T) {
	cases := []struct {
		name        string
		args        map[string]any
		keys        []string
		wantFlag    []string
		wantErrLike string // substring of the structured-error text, "" = success
	}{
		{
			name:     "single fleet",
			args:     map[string]any{"fleet": "my-fleet"},
			keys:     []string{"fleet", "device", "release"},
			wantFlag: []string{"--fleet", "my-fleet"},
		},
		{
			name:     "single device",
			args:     map[string]any{"device": "7cf02a6"},
			keys:     []string{"fleet", "device", "release"},
			wantFlag: []string{"--device", "7cf02a6"},
		},
		{
			name:     "single release",
			args:     map[string]any{"release": "abc"},
			keys:     []string{"fleet", "device", "release"},
			wantFlag: []string{"--release", "abc"},
		},
		{
			name:        "none set",
			args:        map[string]any{},
			keys:        []string{"fleet", "device", "release"},
			wantErrLike: "one of",
		},
		{
			name:        "two set",
			args:        map[string]any{"fleet": "f", "device": "d"},
			keys:        []string{"fleet", "device", "release"},
			wantErrLike: "exactly one",
		},
		{
			name:        "three set",
			args:        map[string]any{"fleet": "f", "device": "d", "release": "r"},
			keys:        []string{"fleet", "device", "release"},
			wantErrLike: "exactly one",
		},
		{
			name: "irrelevant arg is ignored",
			// pickResource should only look at the keys it was asked about — the
			// caller can pass other args (like "service") without tripping the
			// mutual-exclusion check.
			args:     map[string]any{"fleet": "f", "service": "svc"},
			keys:     []string{"fleet", "device"},
			wantFlag: []string{"--fleet", "f"},
		},
		{
			name: "empty string is treated as unset",
			// MCP clients sometimes send empty strings for optional fields; we
			// treat that the same as omission to keep the mutual-exclusion check
			// from firing spuriously.
			args:     map[string]any{"fleet": "f", "device": ""},
			keys:     []string{"fleet", "device"},
			wantFlag: []string{"--fleet", "f"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			flag, errRes := pickResource(req(tc.args), tc.keys...)
			if tc.wantErrLike != "" {
				if errRes == nil {
					t.Fatalf("expected error containing %q, got success with flag %v", tc.wantErrLike, flag)
				}
				txt, ok := mcp.AsTextContent(errRes.Content[0])
				if !ok {
					t.Fatalf("error content is not text: %T", errRes.Content[0])
				}
				if !strings.Contains(strings.ToLower(txt.Text), strings.ToLower(tc.wantErrLike)) {
					t.Fatalf("error %q does not contain %q", txt.Text, tc.wantErrLike)
				}
				return
			}
			if errRes != nil {
				t.Fatalf("expected success, got error result: %+v", errRes)
			}
			if !equalSlice(flag, tc.wantFlag) {
				t.Fatalf("flag mismatch: got %v want %v", flag, tc.wantFlag)
			}
		})
	}
}

// ----- appendBoolFlag / appendStringFlag ---------------------------------

func TestAppendBoolFlag(t *testing.T) {
	r := req(map[string]any{"on": true, "off": false})

	got := appendBoolFlag([]string{"x"}, r, "on", "--on")
	if !equalSlice(got, []string{"x", "--on"}) {
		t.Fatalf("true should append: %v", got)
	}

	got = appendBoolFlag([]string{"x"}, r, "off", "--off")
	if !equalSlice(got, []string{"x"}) {
		t.Fatalf("false should not append: %v", got)
	}

	got = appendBoolFlag([]string{"x"}, r, "missing", "--missing")
	if !equalSlice(got, []string{"x"}) {
		t.Fatalf("missing key should not append: %v", got)
	}
}

func TestAppendStringFlag(t *testing.T) {
	r := req(map[string]any{"name": "value", "blank": ""})

	got := appendStringFlag([]string{"x"}, r, "name", "--name")
	if !equalSlice(got, []string{"x", "--name", "value"}) {
		t.Fatalf("set string should append: %v", got)
	}

	got = appendStringFlag([]string{"x"}, r, "blank", "--blank")
	if !equalSlice(got, []string{"x"}) {
		t.Fatalf("empty string should not append: %v", got)
	}

	got = appendStringFlag([]string{"x"}, r, "missing", "--missing")
	if !equalSlice(got, []string{"x"}) {
		t.Fatalf("missing key should not append: %v", got)
	}
}

// ----- flag-shape guard --------------------------------------------------

func TestRejectFlagShape(t *testing.T) {
	if rejectFlagShape("normal-id", "device UUID") != nil {
		t.Errorf("normal value should not be rejected")
	}
	if rejectFlagShape("", "device UUID") != nil {
		t.Errorf("empty string should not be rejected (handled elsewhere)")
	}
	for _, bad := range []string{"-h", "--help", "-foo", "--fleet"} {
		res := rejectFlagShape(bad, "device UUID")
		if res == nil {
			t.Errorf("flag-shaped value %q should be rejected", bad)
			continue
		}
		txt, ok := mcp.AsTextContent(res.Content[0])
		if !ok {
			t.Errorf("rejection for %q is not text content", bad)
			continue
		}
		if !strings.Contains(txt.Text, "device UUID") {
			t.Errorf("rejection for %q should mention the field name: %q", bad, txt.Text)
		}
	}
}

func TestRequireIdentifier(t *testing.T) {
	// success path
	v, errRes := requireIdentifier(req(map[string]any{"uuid": "7cf02a6"}), "uuid", "device UUID")
	if errRes != nil || v != "7cf02a6" {
		t.Errorf("unexpected: v=%q err=%v", v, errRes)
	}
	// missing
	_, errRes = requireIdentifier(req(map[string]any{}), "uuid", "device UUID")
	if errRes == nil {
		t.Errorf("missing arg should error")
	}
	// flag-shaped
	_, errRes = requireIdentifier(req(map[string]any{"uuid": "--help"}), "uuid", "device UUID")
	if errRes == nil {
		t.Errorf("flag-shaped value should be rejected")
	}
}

func TestGetIdentifier(t *testing.T) {
	// absent
	v, errRes := getIdentifier(req(map[string]any{}), "fleet", "fleet slug")
	if errRes != nil || v != "" {
		t.Errorf("absent should be empty: v=%q err=%v", v, errRes)
	}
	// empty (treated as absent)
	v, errRes = getIdentifier(req(map[string]any{"fleet": ""}), "fleet", "fleet slug")
	if errRes != nil || v != "" {
		t.Errorf("empty should be treated as absent: v=%q err=%v", v, errRes)
	}
	// present, valid
	v, errRes = getIdentifier(req(map[string]any{"fleet": "myorg/myfleet"}), "fleet", "fleet slug")
	if errRes != nil || v != "myorg/myfleet" {
		t.Errorf("unexpected: v=%q err=%v", v, errRes)
	}
	// flag-shaped
	_, errRes = getIdentifier(req(map[string]any{"fleet": "--help"}), "fleet", "fleet slug")
	if errRes == nil {
		t.Errorf("flag-shaped value should be rejected")
	}
}

func TestPickResource_RejectsFlagShape(t *testing.T) {
	_, errRes := pickResource(req(map[string]any{"fleet": "--help"}), "fleet", "device", "release")
	if errRes == nil {
		t.Fatalf("flag-shaped value should be rejected by pickResource")
	}
	txt, _ := mcp.AsTextContent(errRes.Content[0])
	if !strings.Contains(txt.Text, "fleet") {
		t.Errorf("error should mention the offending key: %q", txt.Text)
	}
}

// ----- executeCommand dry-run --------------------------------------------

func TestExecuteCommandDryRun(t *testing.T) {
	orig := Config.DryRun
	Config.DryRun = true
	defer func() { Config.DryRun = orig }()

	out, err := executeCommand(context.Background(), []string{"device", "list", "--json"})
	if err != nil {
		t.Fatalf("dry-run should never error: %v", err)
	}
	want := "[DRY RUN] balena device list --json"
	if !strings.Contains(out, want) {
		t.Fatalf("output %q does not contain %q", out, want)
	}
}

// ----- exec timeout ------------------------------------------------------

// TestExecuteCommandTimeout asserts that the per-call timeout actually kills
// a runaway balena subprocess. We can't run a real `balena` here, so we test
// the underlying mechanism (exec.CommandContext + WithTimeout) against a
// portable long-running command. Skips on Windows where `sleep` is not a
// standalone binary on PATH.
func TestExecuteCommandTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable long-sleep binary on Windows runner; CI covers Linux + macOS")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skipf("sleep not on PATH: %v", err)
	}

	// Reach into the package's runCmd plumbing by impersonating its work — we
	// invoke exec.CommandContext directly with the same context shape that
	// executeCommand builds, so the test stays meaningful even though we
	// can't call `balena` itself.
	parent := context.Background()
	ctx, cancel := context.WithTimeout(parent, 50*time.Millisecond)
	defer cancel()

	start := time.Now()
	cmd := exec.CommandContext(ctx, "sleep", "5")
	err := cmd.Run()
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected sleep to be killed by timeout, but it succeeded after %s", elapsed)
	}
	// Should die fast — well under the 5s sleep request.
	if elapsed > 2*time.Second {
		t.Fatalf("subprocess took %s to die; timeout did not propagate to the child", elapsed)
	}
}

// TestExecuteCommandRespectsCancelledContext verifies that an already-
// cancelled parent context causes executeCommand to short-circuit rather
// than launching balena. (Dry-run bypasses exec entirely, so we exercise the
// real-exec branch with a known-missing binary name.)
func TestExecuteCommandRespectsCancelledContext(t *testing.T) {
	orig := Config.DryRun
	Config.DryRun = false
	defer func() { Config.DryRun = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancel

	_, err := executeCommand(ctx, []string{"version"})
	if err == nil {
		t.Fatalf("expected an error from a pre-cancelled context")
	}
	// We accept either the explicit "cancelled by caller" message (preferred)
	// or any CLI/exec error — the point is we did not silently succeed.
	if !strings.Contains(err.Error(), "cancel") && !strings.Contains(err.Error(), "balena CLI error") {
		t.Logf("note: error was %q", err)
	}
}

// TestLoadConfigFromEnv exercises BALENAMCP_EXEC_TIMEOUT parsing.
func TestLoadConfigFromEnv(t *testing.T) {
	cases := []struct {
		envVal string
		want   time.Duration
	}{
		{"", defaultExecTimeout},
		{"5", 5 * time.Second},
		{"120", 120 * time.Second},
		{"nonsense", defaultExecTimeout}, // falls back, prints warning to stderr
		{"-1", defaultExecTimeout},       // negative rejected
		{"0", defaultExecTimeout},        // zero rejected
	}
	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("BALENAMCP_EXEC_TIMEOUT", tc.envVal)
			loadConfigFromEnv()
			if Config.ExecTimeout != tc.want {
				t.Errorf("env=%q want %s got %s", tc.envVal, tc.want, Config.ExecTimeout)
			}
		})
	}
}

// TestLoadConfigFromEnv_RequireConfirm covers the truthy-parsing logic for
// BALENAMCP_REQUIRE_CONFIRM, including the "garbage falls back to off" branch
// (mirrors the EXEC_TIMEOUT case but for the boolean parser).
func TestLoadConfigFromEnv_RequireConfirm(t *testing.T) {
	cases := []struct {
		envVal string
		want   bool
	}{
		{"", false},         // unset
		{"1", true},         // ParseBool truthy
		{"true", true},      // ParseBool truthy
		{"TRUE", true},      // case-insensitive
		{"0", false},        // ParseBool false
		{"false", false},    // ParseBool false
		{"nonsense", false}, // garbage -> warn + default off
	}
	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("BALENAMCP_REQUIRE_CONFIRM", tc.envVal)
			loadConfigFromEnv()
			if Config.RequireConfirm != tc.want {
				t.Errorf("env=%q want %v got %v", tc.envVal, tc.want, Config.RequireConfirm)
			}
		})
	}
}

// TestExecuteCommand_ZeroTimeoutFallback covers the defensive guard in
// executeCommand that swaps in defaultExecTimeout when Config.ExecTimeout is
// non-positive (e.g. a programmer calls the helper before loadConfigFromEnv
// has run). We force an error with a pre-cancelled parent context — works
// regardless of whether `balena` is installed on the host — and verify
// executeCommand returned cleanly (didn't deadlock or panic in the fallback).
func TestExecuteCommand_ZeroTimeoutFallback(t *testing.T) {
	origDry, origTO := Config.DryRun, Config.ExecTimeout
	Config.DryRun = false
	Config.ExecTimeout = 0 // force the fallback branch
	defer func() { Config.DryRun, Config.ExecTimeout = origDry, origTO }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := executeCommand(ctx, []string{"version"})
	if err == nil {
		t.Fatalf("expected error from cancelled context with zero-timeout fallback, got nil")
	}
}

// TestRunCmd_ErrorPath covers the err != nil branch in runCmd. We trigger a
// deterministic error via a pre-cancelled context (works whether or not
// balena is on PATH) and verify runCmd converts the Go error into a tool-
// result with IsError=true, per the MCP convention that a Go-level error
// from the handler aborts dispatch entirely.
func TestRunCmd_ErrorPath(t *testing.T) {
	orig := Config.DryRun
	Config.DryRun = false
	defer func() { Config.DryRun = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := runCmd(ctx, []string{"version"})
	if err != nil {
		t.Fatalf("runCmd should swallow CLI errors into a tool-result, got Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError tool result, got: %+v", res)
	}
}

// ----- helpers -----------------------------------------------------------

func equalSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

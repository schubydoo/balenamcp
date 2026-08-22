package server

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
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

func TestRequireSingleTarget(t *testing.T) {
	cases := []struct {
		name        string
		args        map[string]any
		want        string
		wantErrLike string // substring of the structured-error text, "" = success
	}{
		{
			name: "single target passes through",
			args: map[string]any{"uuid": "7cf02a6"},
			want: "7cf02a6",
		},
		{
			name:        "comma-separated list is rejected",
			args:        map[string]any{"uuid": "7cf02a6,55d43b3"},
			wantErrLike: "single target per call",
		},
		{
			name:        "trailing comma is still a list",
			args:        map[string]any{"uuid": "7cf02a6,"},
			wantErrLike: "single target per call",
		},
		{
			name:        "missing arg errors before the list check",
			args:        map[string]any{},
			wantErrLike: "uuid",
		},
		{
			name:        "flag shape errors before the list check",
			args:        map[string]any{"uuid": "--help,x"},
			wantErrLike: "cannot start with '-'",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, errRes := requireSingleTarget(req(tc.args), "uuid", "device UUID")
			if tc.wantErrLike == "" {
				if errRes != nil {
					t.Fatalf("unexpected error: %v", errRes)
				}
				if got != tc.want {
					t.Errorf("got %q, want %q", got, tc.want)
				}
				return
			}
			if errRes == nil {
				t.Fatalf("expected error containing %q, got value %q", tc.wantErrLike, got)
			}
			if got != "" {
				t.Errorf("error path must return an empty value, got %q", got)
			}
			txt, ok := mcp.AsTextContent(errRes.Content[0])
			if !ok {
				t.Fatalf("error content is not text: %T", errRes.Content[0])
			}
			if !strings.Contains(txt.Text, tc.wantErrLike) {
				t.Errorf("error %q does not contain %q", txt.Text, tc.wantErrLike)
			}
		})
	}
}

// TestEvalExistingPrefix pins the walk-up resolver directly. The distinction
// that matters is NotExist (keep walking to an existing ancestor) versus any
// other error (abort): a gremlins mutant inverting !os.IsNotExist turns the
// loop into a wrong walk on exactly the input an attacker controls, and only
// a non-NotExist case kills it.
func TestEvalExistingPrefix(t *testing.T) {
	dir := t.TempDir()
	real, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "plain"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	t.Run("existing dir resolves", func(t *testing.T) {
		got, err := evalExistingPrefix(dir)
		if err != nil || got != real {
			t.Fatalf("got %q, %v; want %q", got, err, real)
		}
	})
	t.Run("nonexistent tail rejoined onto existing ancestor", func(t *testing.T) {
		got, err := evalExistingPrefix(filepath.Join(dir, "sub", "new.bin"))
		if err != nil || got != filepath.Join(real, "sub", "new.bin") {
			t.Fatalf("got %q, %v; want %q", got, err, filepath.Join(real, "sub", "new.bin"))
		}
	})
	t.Run("file as directory component", func(t *testing.T) {
		// Platform divergence, both fail closed but at different layers:
		// POSIX returns ENOTDIR (not IsNotExist), so the resolver aborts
		// here; Windows returns PATH_NOT_FOUND, which IS IsNotExist, so the
		// walk-up succeeds and the caller's os.Stat backstop rejects the
		// path instead. Pin each platform's actual behavior so a change on
		// either side is visible.
		got, err := evalExistingPrefix(filepath.Join(dir, "plain", "child"))
		if runtime.GOOS == "windows" {
			if err != nil {
				t.Fatalf("windows: expected the walk-up to succeed, got %v", err)
			}
			if got != filepath.Join(real, "plain", "child") {
				t.Errorf("windows: got %q, want rejoined path under the file", got)
			}
			return
		}
		if err == nil {
			t.Fatalf("expected a non-NotExist resolution error")
		}
		if os.IsNotExist(err) {
			t.Fatalf("error must not be NotExist — that would keep the walk going: %v", err)
		}
	})
	t.Run("symlink loop aborts", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(dir, "lb"), filepath.Join(dir, "la")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		if err := os.Symlink(filepath.Join(dir, "la"), filepath.Join(dir, "lb")); err != nil {
			t.Fatal(err)
		}
		if _, err := evalExistingPrefix(filepath.Join(dir, "la", "x")); err == nil {
			t.Fatalf("expected a resolution error from the symlink loop")
		}
	})
}

func TestLoadAssetDirFromEnv(t *testing.T) {
	t.Run("unset disables filesystem tools", func(t *testing.T) {
		t.Setenv("BALENAMCP_ASSET_DIR", "")
		if got := loadAssetDirFromEnv(); got != "" {
			t.Errorf("unset should yield empty, got %q", got)
		}
	})
	t.Run("missing directory fails closed", func(t *testing.T) {
		t.Setenv("BALENAMCP_ASSET_DIR", filepath.Join(t.TempDir(), "nope"))
		if got := loadAssetDirFromEnv(); got != "" {
			t.Errorf("missing dir should fail closed, got %q", got)
		}
	})
	t.Run("a file is not a directory", func(t *testing.T) {
		f := filepath.Join(t.TempDir(), "file")
		if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		t.Setenv("BALENAMCP_ASSET_DIR", f)
		if got := loadAssetDirFromEnv(); got != "" {
			t.Errorf("a regular file should fail closed, got %q", got)
		}
	})
	t.Run("valid directory resolves absolute", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("BALENAMCP_ASSET_DIR", dir)
		got := loadAssetDirFromEnv()
		if got == "" || !filepath.IsAbs(got) {
			t.Errorf("valid dir should resolve to an absolute path, got %q", got)
		}
	})
}

func TestWithinRoot(t *testing.T) {
	root := filepath.Join("/srv", "assets")
	cases := []struct {
		p    string
		want bool
	}{
		{root, true},
		{filepath.Join(root, "a", "b"), true},
		// a sibling sharing a name prefix must not count as inside; this is
		// what a plain strings.HasPrefix check would get wrong.
		{filepath.Join("/srv", "assets-evil"), false},
		{filepath.Join("/srv"), false},
		{filepath.Join("/etc", "passwd"), false},
		// filepath.Rel errors on a relative path vs an absolute root; the
		// predicate must fail CLOSED (false), not fall through to inside.
		{"relative/path", false},
	}
	for _, tc := range cases {
		if got := withinRoot(root, tc.p); got != tc.want {
			t.Errorf("withinRoot(%q, %q) = %v, want %v", root, tc.p, got, tc.want)
		}
	}
}

func TestResolveAssetPath(t *testing.T) {
	root := t.TempDir()
	Config.AssetDir = root
	t.Cleanup(func() { Config.AssetDir = "" })

	// resolveAssetPath canonicalizes the root, so expectations must too:
	// macOS maps /var to /private/var and Windows expands 8.3 short names.
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}

	// a path that does not exist yet still resolves — the download case.
	got, errRes := resolveAssetPath("sub/new.bin", "output path")
	if errRes != nil {
		t.Fatalf("unexpected error: %v", errRes)
	}
	if got != filepath.Join(canonical, "sub", "new.bin") {
		t.Errorf("got %q, want %q", got, filepath.Join(canonical, "sub", "new.bin"))
	}

	for _, tc := range []struct{ name, in, wantErrLike string }{
		{"empty", "", "required"},
		{"leading dash", "-o", "cannot start with '-'"},
		{"traversal", "../x", "escapes"},
		{"nested traversal", "a/../../x", "escapes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, errRes := resolveAssetPath(tc.in, "output path")
			if errRes == nil {
				t.Fatalf("expected refusal for %q", tc.in)
			}
			txt, ok := mcp.AsTextContent(errRes.Content[0])
			if !ok {
				t.Fatalf("error content is not text")
			}
			if !strings.Contains(txt.Text, tc.wantErrLike) {
				t.Errorf("error %q does not contain %q", txt.Text, tc.wantErrLike)
			}
		})
	}

	t.Run("in-root symlink returns the caller's path, not the resolved one", func(t *testing.T) {
		// The containment check runs on the RESOLVED path, but the returned
		// argv deliberately carries the caller-shaped join — the CLI should
		// see the path the agent asked for. Every prior case had full == real,
		// so a mutant returning `real` instead survived.
		if err := os.Mkdir(filepath.Join(root, "realdir"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(filepath.Join(root, "realdir"), filepath.Join(root, "alias")); err != nil {
			t.Skipf("symlinks unavailable: %v", err)
		}
		got, errRes := resolveAssetPath(filepath.Join("alias", "f.bin"), "output path")
		if errRes != nil {
			t.Fatalf("in-root symlink must be allowed: %v", errRes)
		}
		if got != filepath.Join(canonical, "alias", "f.bin") {
			t.Errorf("got %q, want the unresolved alias path %q",
				got, filepath.Join(canonical, "alias", "f.bin"))
		}
	})

	t.Run("root deleted after startup fails closed", func(t *testing.T) {
		// loadAssetDirFromEnv validated the root once; if it disappears
		// afterwards every call must become a refusal, not a fallthrough.
		Config.AssetDir = filepath.Join(t.TempDir(), "gone")
		defer func() { Config.AssetDir = root }()
		_, errRes := resolveAssetPath("ok.bin", "output path")
		if errRes == nil {
			t.Fatalf("expected refusal when the asset root no longer resolves")
		}
		txt, ok := mcp.AsTextContent(errRes.Content[0])
		if !ok || !strings.Contains(txt.Text, "BALENAMCP_ASSET_DIR") ||
			!strings.Contains(txt.Text, "cannot be resolved") {
			t.Errorf("refusal should name the root and the cause, got %#v", errRes.Content[0])
		}
	})

	if runtime.GOOS == "windows" {
		t.Run("volume-relative path is refused on windows", func(t *testing.T) {
			// C:foo has a volume name but is not absolute; without the
			// VolumeName check it would sail through the IsAbs rejection.
			// The doc comment promises this refusal — this is the only place
			// it is verified, and only the Windows CI leg can do it.
			_, errRes := resolveAssetPath(`C:evil.bin`, "output path")
			if errRes == nil {
				t.Fatalf("expected volume-relative path to be refused")
			}
		})
	}

	t.Run("disabled when root unset", func(t *testing.T) {
		Config.AssetDir = ""
		defer func() { Config.AssetDir = root }()
		if _, errRes := resolveAssetPath("ok.bin", "output path"); errRes == nil {
			t.Errorf("should refuse when BALENAMCP_ASSET_DIR is unset")
		}
	})
}

func TestGetSingleTarget(t *testing.T) {
	// absent — no error, empty value (the optional-arg contract).
	v, errRes := getSingleTarget(req(map[string]any{}), "service", "service name")
	if errRes != nil || v != "" {
		t.Errorf("absent arg: got v=%q err=%v, want empty and no error", v, errRes)
	}
	// present and single
	v, errRes = getSingleTarget(req(map[string]any{"service": "api"}), "service", "service name")
	if errRes != nil || v != "api" {
		t.Errorf("single value: got v=%q err=%v", v, errRes)
	}
	// present and a list
	v, errRes = getSingleTarget(req(map[string]any{"service": "api,web"}), "service", "service name")
	if errRes == nil {
		t.Fatalf("comma-separated value should be rejected, got %q", v)
	}
	if v != "" {
		t.Errorf("error path must return an empty value, got %q", v)
	}
	txt, ok := mcp.AsTextContent(errRes.Content[0])
	if !ok {
		t.Fatalf("error content is not text: %T", errRes.Content[0])
	}
	if !strings.Contains(txt.Text, "single target per call") {
		t.Errorf("unexpected error text: %q", txt.Text)
	}
	// flag-shaped values are still rejected by the inner guard, and that
	// check runs before the list check.
	_, errRes = getSingleTarget(req(map[string]any{"service": "-s,x"}), "service", "service name")
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

	// Call the REAL function under test. An earlier version of this test
	// re-implemented exec.CommandContext + WithTimeout inline and asserted on
	// its own re-implementation, leaving the production deadline branch, its
	// message, and the DeadlineExceeded discrimination entirely untested.
	origDry, origTO, origBin := Config.DryRun, Config.ExecTimeout, execBinary
	Config.DryRun = false
	Config.ExecTimeout = 50 * time.Millisecond
	execBinary = "sleep"
	defer func() { Config.DryRun, Config.ExecTimeout, execBinary = origDry, origTO, origBin }()

	start := time.Now()
	_, err := executeCommand(context.Background(), []string{"5"})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatalf("expected the deadline to kill sleep, but it succeeded after %s", elapsed)
	}
	// The production message names the configured timeout and the env knob —
	// asserting both pins that Config.ExecTimeout (not some constant) fed the
	// deadline, and that the caller is told how to raise it.
	if !strings.Contains(err.Error(), "timed out after 50ms") ||
		!strings.Contains(err.Error(), "BALENAMCP_EXEC_TIMEOUT") {
		t.Fatalf("timeout error should name the configured timeout and the env knob, got: %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("subprocess took %s to die; timeout did not propagate to the child", elapsed)
	}
}

// TestExecuteCommand_CLIErrorBranch pins the plain CLI-failure path: a live
// context and a binary that exits non-zero must yield the "balena CLI error"
// message, distinct from the timeout and cancellation branches.
func TestExecuteCommand_CLIErrorBranch(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable false binary on Windows runner; CI covers Linux + macOS")
	}
	if _, err := exec.LookPath("false"); err != nil {
		t.Skipf("false not on PATH: %v", err)
	}
	origDry, origBin := Config.DryRun, execBinary
	Config.DryRun = false
	execBinary = "false"
	defer func() { Config.DryRun, execBinary = origDry, origBin }()

	_, err := executeCommand(context.Background(), []string{"anything"})
	if err == nil {
		t.Fatalf("expected an error from a failing binary")
	}
	if !strings.HasPrefix(err.Error(), "balena CLI error:") {
		t.Fatalf("expected the CLI-error message, got: %v", err)
	}
}

// TestRunCmdAllowingBenignError_RealExec drives both error branches of
// runCmdAllowingBenignError through a real failing subprocess. Before this
// test every mutant in that function survived: inverting the Contains check,
// swapping the two returns, or replacing the benign result with an error all
// passed, because only the success path (and a separate reimplementation in
// the resources composite) was ever exercised.
func TestRunCmdAllowingBenignError_RealExec(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable false binary on Windows runner; CI covers Linux + macOS")
	}
	if _, err := exec.LookPath("false"); err != nil {
		t.Skipf("false not on PATH: %v", err)
	}
	origDry, origBin := Config.DryRun, execBinary
	Config.DryRun = false
	execBinary = "false" // exits 1 with no output → err is "balena CLI error: exit status 1"
	defer func() { Config.DryRun, execBinary = origDry, origBin }()

	// Marker matches the error → benign: success result whose text IS the marker.
	res, err := runCmdAllowingBenignError(context.Background(), []string{"tag", "list"}, "exit status 1")
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if res.IsError {
		t.Fatalf("matching benign marker must convert the failure to success, got error result")
	}
	txt, ok := mcp.AsTextContent(res.Content[0])
	if !ok || txt.Text != "exit status 1" {
		t.Fatalf("benign result must be exactly the marker text, got %#v", res.Content[0])
	}

	// Marker doesn't match → the real failure propagates as an error result.
	res, err = runCmdAllowingBenignError(context.Background(), []string{"tag", "list"}, "No tags found")
	if err != nil {
		t.Fatalf("unexpected transport error: %v", err)
	}
	if !res.IsError {
		t.Fatalf("non-matching marker must propagate the failure, got success")
	}
	txt, ok = mcp.AsTextContent(res.Content[0])
	if !ok || !strings.Contains(txt.Text, "balena CLI error") {
		t.Fatalf("propagated failure should carry the CLI error text, got %#v", res.Content[0])
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
	// Require the exact cancellation message. The old form accepted either
	// this branch or the generic CLI error and only t.Logf'd on mismatch, so
	// deleting the Canceled arm entirely (falling through to "balena CLI
	// error: context canceled") passed unnoticed.
	if err.Error() != "balena CLI cancelled by caller" {
		t.Fatalf("expected the cancellation message, got: %v", err)
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

// TestRunCmdStdin_ErrorPath mirrors TestRunCmd_ErrorPath for the stdin variant.
// A pre-cancelled context drives executeCommandStdin into an error on the
// real-exec path, exercising the err != nil branch in runCmdStdin that dry-run
// tests never reach (device-ssh is only ever called in dry-run elsewhere).
func TestRunCmdStdin_ErrorPath(t *testing.T) {
	orig := Config.DryRun
	Config.DryRun = false
	defer func() { Config.DryRun = orig }()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := runCmdStdin(ctx, []string{"version"}, "hello\nexit\n")
	if err != nil {
		t.Fatalf("runCmdStdin should swallow CLI errors into a tool-result, got Go error: %v", err)
	}
	if res == nil || !res.IsError {
		t.Fatalf("expected IsError tool result, got: %+v", res)
	}
}

// TestExecuteCommandStdinWiring proves the real-exec path actually delivers the
// stdin payload to the subprocess — the one behavior the dry-run tests cannot
// cover (dry-run returns before cmd.Stdin is set). It points execBinary at
// `cat`, which echoes stdin, and asserts the payload round-trips. Without this,
// a regression dropping `cmd.Stdin = ...` would pass every other test while
// silently sending nothing to the device.
func TestExecuteCommandStdinWiring(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no portable stdin-echo binary on Windows runner; CI covers Linux + macOS")
	}
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skipf("cat not on PATH: %v", err)
	}
	origDry, origBin := Config.DryRun, execBinary
	Config.DryRun = false
	execBinary = "cat" // cat with no args reads stdin and echoes it to stdout
	defer func() { Config.DryRun, execBinary = origDry, origBin }()

	out, err := executeCommandStdin(context.Background(), nil, "hello from stdin\nexit\n")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out, "hello from stdin") {
		t.Fatalf("stdin was not delivered to subprocess; got: %q", out)
	}

	// Companion negative: with no stdin payload, cmd.Stdin stays nil and cat
	// reads from the (empty) test stdin, returning empty — confirms we only
	// wire stdin when a payload is present.
	out, err = executeCommandStdin(context.Background(), nil, "")
	if err != nil {
		t.Fatalf("unexpected error on empty-stdin call: %v", err)
	}
	if strings.Contains(out, "hello from stdin") {
		t.Fatalf("empty-stdin call should not echo prior payload; got: %q", out)
	}
}

// ----- guardDestructive --------------------------------------------------

// TestGuardDestructive exercises both layers in sequence: the
// BALENAMCP_REQUIRE_CONFIRM gate AND the identifier-resolution + flag-shape
// guard. Each subtest pins one variable while varying the other so failures
// localize cleanly.
func TestGuardDestructive(t *testing.T) {
	orig := Config.RequireConfirm
	defer func() { Config.RequireConfirm = orig }()

	t.Run("gate_off_identifier_present", func(t *testing.T) {
		Config.RequireConfirm = false
		id, errRes := guardDestructive(req(map[string]any{"uuid": "abc123"}), "uuid", "device UUID")
		if errRes != nil {
			t.Fatalf("unexpected errRes: %+v", errRes)
		}
		if id != "abc123" {
			t.Fatalf("want id=abc123 got %q", id)
		}
	})

	t.Run("gate_on_no_confirm", func(t *testing.T) {
		Config.RequireConfirm = true
		id, errRes := guardDestructive(req(map[string]any{"uuid": "abc123"}), "uuid", "device UUID")
		if errRes == nil {
			t.Fatalf("expected errRes when confirm is required but missing")
		}
		if id != "" {
			t.Fatalf("want empty id on gate failure, got %q", id)
		}
	})

	t.Run("gate_on_with_confirm", func(t *testing.T) {
		Config.RequireConfirm = true
		id, errRes := guardDestructive(req(map[string]any{"uuid": "abc123", "confirm": true}), "uuid", "device UUID")
		if errRes != nil {
			t.Fatalf("unexpected errRes: %+v", errRes)
		}
		if id != "abc123" {
			t.Fatalf("want id=abc123 got %q", id)
		}
	})

	t.Run("identifier_missing", func(t *testing.T) {
		Config.RequireConfirm = false
		id, errRes := guardDestructive(req(map[string]any{}), "uuid", "device UUID")
		if errRes == nil {
			t.Fatalf("expected errRes when required identifier is missing")
		}
		if id != "" {
			t.Fatalf("want empty id on identifier failure, got %q", id)
		}
	})

	t.Run("identifier_flag_shaped_rejected", func(t *testing.T) {
		Config.RequireConfirm = false
		id, errRes := guardDestructive(req(map[string]any{"uuid": "--help"}), "uuid", "device UUID")
		if errRes == nil {
			t.Fatalf("expected errRes when identifier starts with '-'")
		}
		if id != "" {
			t.Fatalf("want empty id on flag-shape rejection, got %q", id)
		}
	})
}

// ----- env-var loaders ---------------------------------------------------

// TestLoadExecTimeoutFromEnv tests the extracted helper directly (the same
// shape that TestLoadConfigFromEnv covers via the outer dispatcher, but
// targets the helper so gremlins can find mutants against it).
func TestLoadExecTimeoutFromEnv(t *testing.T) {
	cases := []struct {
		envVal string
		want   time.Duration
	}{
		{"", defaultExecTimeout},
		{"5", 5 * time.Second},
		{"120", 120 * time.Second},
		{"nonsense", defaultExecTimeout},
		{"-1", defaultExecTimeout},
		{"0", defaultExecTimeout},
	}
	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("BALENAMCP_EXEC_TIMEOUT", tc.envVal)
			got := loadExecTimeoutFromEnv()
			if got != tc.want {
				t.Errorf("env=%q want %s got %s", tc.envVal, tc.want, got)
			}
		})
	}
}

// TestLoadRequireConfirmFromEnv mirrors the above for the bool helper.
func TestLoadRequireConfirmFromEnv(t *testing.T) {
	cases := []struct {
		envVal string
		want   bool
	}{
		{"", false},
		{"1", true},
		{"true", true},
		{"TRUE", true},
		{"0", false},
		{"false", false},
		{"nonsense", false},
	}
	for _, tc := range cases {
		t.Run(tc.envVal, func(t *testing.T) {
			t.Setenv("BALENAMCP_REQUIRE_CONFIRM", tc.envVal)
			got := loadRequireConfirmFromEnv()
			if got != tc.want {
				t.Errorf("env=%q want %v got %v", tc.envVal, tc.want, got)
			}
		})
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

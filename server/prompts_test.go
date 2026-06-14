package server

import (
	"context"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// promptReq constructs a GetPromptRequest with the given string arguments.
// Prompt arguments are map[string]string in mcp-go, so this mirrors the `req`
// helper used for tool tests.
func promptReq(args map[string]string) mcp.GetPromptRequest {
	r := mcp.GetPromptRequest{}
	r.Params.Arguments = args
	return r
}

// promptText asserts the result carries exactly one user-role text message and
// returns its text.
func promptText(t *testing.T, res *mcp.GetPromptResult) string {
	t.Helper()
	if res == nil {
		t.Fatal("nil GetPromptResult")
	}
	if len(res.Messages) != 1 {
		t.Fatalf("want 1 message, got %d", len(res.Messages))
	}
	msg := res.Messages[0]
	if msg.Role != mcp.RoleUser {
		t.Fatalf("want RoleUser, got %q", msg.Role)
	}
	tc, ok := msg.Content.(mcp.TextContent)
	if !ok {
		t.Fatalf("want TextContent, got %T", msg.Content)
	}
	return tc.Text
}

type promptHandler func(context.Context, mcp.GetPromptRequest) (*mcp.GetPromptResult, error)

// ----- happy path: each prompt interpolates its args and names its tools ---

func TestPromptHandlers(t *testing.T) {
	cases := []struct {
		name       string
		handler    promptHandler
		args       map[string]string
		wantDesc   string   // substring expected in the result Description
		wantInText []string // substrings expected in the message body
	}{
		{
			name:     "diagnose-device",
			handler:  handleDiagnoseDevice,
			args:     map[string]string{"uuid": "abc123dev"},
			wantDesc: "abc123dev",
			wantInText: []string{
				"abc123dev",
				"device-info", "device-logs", "env-list", "tag-list", "device-pin",
			},
		},
		{
			name:     "fleet-health-report",
			handler:  handleFleetHealthReport,
			args:     map[string]string{"fleet": "myorg/myfleet"},
			wantDesc: "myorg/myfleet",
			wantInText: []string{
				"myorg/myfleet",
				"device-list", "fleet-info", "release-list",
			},
		},
		{
			name:     "safe-release-rollout",
			handler:  handleSafeReleaseRollout,
			args:     map[string]string{"fleet": "myorg/fl", "release": "def456rel"},
			wantDesc: "def456rel",
			wantInText: []string{
				"myorg/fl", "def456rel",
				"fleet-pin", "device-list", "device-pin", "device-info",
				"device-track-fleet", "release-invalidate", "fleet-track-latest",
				"confirm:true", "canary",
			},
		},
		{
			name:     "rollback-device",
			handler:  handleRollbackDevice,
			args:     map[string]string{"uuid": "dev9uuid"},
			wantDesc: "dev9uuid",
			wantInText: []string{
				"dev9uuid",
				"device-info", "device-pin", "release-list", "device-logs",
				"release-invalidate",
			},
		},
		{
			name:     "audit-config",
			handler:  handleAuditConfig,
			args:     map[string]string{"fleet": "myorg/audit"},
			wantDesc: "myorg/audit",
			wantInText: []string{
				"myorg/audit",
				"env-list", "device-list", "config=true",
			},
		},
		{
			name:     "compare-releases",
			handler:  handleCompareReleases,
			args:     map[string]string{"release_a": "aaa111", "release_b": "bbb222"},
			wantDesc: "aaa111",
			wantInText: []string{
				"aaa111", "bbb222",
				"release-info", "release-asset-list", "image-size", "composition=true",
			},
		},
		{
			name:     "replicate-config",
			handler:  handleReplicateConfig,
			args:     map[string]string{"source": "org/src", "target": "org/dst"},
			wantDesc: "org/src",
			wantInText: []string{
				"org/src", "org/dst",
				"env-list", "env-set", "approval", "MASKING",
			},
		},
		{
			name:     "bulk-tag with value",
			handler:  handleBulkTag,
			args:     map[string]string{"fleet": "org/bt", "key": "site", "value": "warehouse"},
			wantDesc: "org/bt",
			wantInText: []string{
				"org/bt", "site", "warehouse",
				"device-list", "tag-set", "approval",
			},
		},
		{
			name:     "bulk-tag empty value",
			handler:  handleBulkTag,
			args:     map[string]string{"fleet": "org/bt", "key": "flagged"},
			wantDesc: "org/bt",
			wantInText: []string{
				"flagged", "(empty value)", "tag-set",
			},
		},
		{
			name:     "deep-diagnose-device",
			handler:  handleDeepDiagnoseDevice,
			args:     map[string]string{"uuid": "deep9dev"},
			wantDesc: "deep9dev",
			wantInText: []string{
				"deep9dev",
				"device-info", "device-ssh", "device-logs",
				"/proc/meminfo", "confirm:true",
			},
		},
		{
			name:     "prepare-local-dev with device",
			handler:  handlePrepareLocalDev,
			args:     map[string]string{"device": "loc4dev"},
			wantDesc: "loc4dev",
			wantInText: []string{
				"loc4dev",
				"device-local-mode-get", "device-local-mode-set", "enable=true",
			},
		},
		{
			name:     "prepare-local-dev without device steers to detect",
			handler:  handlePrepareLocalDev,
			args:     map[string]string{},
			wantDesc: "local development",
			wantInText: []string{
				"device-detect", "device-local-mode-get", "device-local-mode-set",
			},
		},
		{
			name:     "rotate-api-keys all keys",
			handler:  handleRotateApiKeys,
			args:     map[string]string{},
			wantDesc: "Review and revoke API keys",
			wantInText: []string{
				"api-key-list", "api-key-revoke", "user-level", "confirm:true",
			},
		},
		{
			name:     "rotate-api-keys scoped to fleet",
			handler:  handleRotateApiKeys,
			args:     map[string]string{"fleet": "org/keys"},
			wantDesc: "org/keys",
			wantInText: []string{
				"org/keys", "api-key-list", "api-key-revoke", "fleet=org/keys",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.handler(context.Background(), promptReq(tc.args))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !strings.Contains(res.Description, tc.wantDesc) {
				t.Errorf("description %q does not contain %q", res.Description, tc.wantDesc)
			}
			text := promptText(t, res)
			for _, want := range tc.wantInText {
				if !strings.Contains(text, want) {
					t.Errorf("message text missing %q\n---\n%s", want, text)
				}
			}
		})
	}
}

// ----- error path: a missing required arg fails the handler ----------------

func TestPromptHandlersMissingArg(t *testing.T) {
	cases := []struct {
		name       string
		handler    promptHandler
		args       map[string]string
		wantErrSub string
	}{
		{"diagnose-device no uuid", handleDiagnoseDevice, map[string]string{}, "uuid"},
		{"fleet-health-report no fleet", handleFleetHealthReport, map[string]string{}, "fleet"},
		{"rollback-device no uuid", handleRollbackDevice, map[string]string{}, "uuid"},
		{"audit-config no fleet", handleAuditConfig, map[string]string{}, "fleet"},
		{"safe-release-rollout no fleet", handleSafeReleaseRollout, map[string]string{"release": "r"}, "fleet"},
		{"safe-release-rollout no release", handleSafeReleaseRollout, map[string]string{"fleet": "f"}, "release"},
		{"compare-releases no release_a", handleCompareReleases, map[string]string{"release_b": "b"}, "release_a"},
		{"compare-releases no release_b", handleCompareReleases, map[string]string{"release_a": "a"}, "release_b"},
		{"replicate-config no source", handleReplicateConfig, map[string]string{"target": "t"}, "source"},
		{"replicate-config no target", handleReplicateConfig, map[string]string{"source": "s"}, "target"},
		{"bulk-tag no fleet", handleBulkTag, map[string]string{"key": "k"}, "fleet"},
		{"bulk-tag no key", handleBulkTag, map[string]string{"fleet": "f"}, "key"},
		{"deep-diagnose-device no uuid", handleDeepDiagnoseDevice, map[string]string{}, "uuid"},
		// empty-string value is treated as missing, not as a valid identifier.
		{"diagnose-device empty uuid", handleDiagnoseDevice, map[string]string{"uuid": ""}, "uuid"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			res, err := tc.handler(context.Background(), promptReq(tc.args))
			if err == nil {
				t.Fatalf("want error, got nil (res=%v)", res)
			}
			if res != nil {
				t.Errorf("want nil result on error, got %v", res)
			}
			if !strings.Contains(err.Error(), tc.wantErrSub) {
				t.Errorf("error %q does not mention %q", err.Error(), tc.wantErrSub)
			}
		})
	}
}

// ----- requirePromptArg --------------------------------------------------

func TestRequirePromptArg(t *testing.T) {
	cases := []struct {
		name    string
		args    map[string]string
		key     string
		want    string
		wantErr bool
	}{
		{"present", map[string]string{"k": "v"}, "k", "v", false},
		{"empty", map[string]string{"k": ""}, "k", "", true},
		{"absent", map[string]string{}, "k", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := requirePromptArg(promptReq(tc.args), tc.key, "thing")
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got nil")
				}
				if !strings.Contains(err.Error(), tc.key) {
					t.Errorf("error %q does not mention key %q", err.Error(), tc.key)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// ----- registration smoke test -------------------------------------------

// TestRegisterPromptsViaSetup ensures SetupServer wires the prompts in without
// panicking. registerPrompts has no externally observable return, so this is a
// construction smoke test; the per-handler behavior is covered above.
func TestRegisterPromptsViaSetup(t *testing.T) {
	if srv := SetupServer(); srv == nil {
		t.Fatal("SetupServer returned nil")
	}
}

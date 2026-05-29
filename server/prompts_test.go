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
				"device-track-fleet", "confirm:true", "canary",
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

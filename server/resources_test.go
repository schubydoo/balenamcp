package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
)

// readReq constructs a ReadResourceRequest for the given URI.
func readReq(uri string) mcp.ReadResourceRequest {
	r := mcp.ReadResourceRequest{}
	r.Params.URI = uri
	return r
}

// resourceText asserts a single text content item and returns (text, uri, mime).
func resourceText(t *testing.T, cs []mcp.ResourceContents) (text, uri, mime string) {
	t.Helper()
	if len(cs) != 1 {
		t.Fatalf("want 1 content item, got %d", len(cs))
	}
	tc, ok := cs[0].(mcp.TextResourceContents)
	if !ok {
		t.Fatalf("want TextResourceContents, got %T", cs[0])
	}
	return tc.Text, tc.URI, tc.MIMEType
}

// fakeRunner returns a cliRunner keyed by the space-joined argv. A key present
// in errs yields an error; a key present in out yields that output; anything
// else fails the test-by-returning an error the caller can assert on.
func fakeRunner(out, errs map[string]string) cliRunner {
	return func(_ context.Context, args []string) (string, error) {
		key := strings.Join(args, " ")
		if e, ok := errs[key]; ok {
			return "", fmt.Errorf("%s", e)
		}
		if r, ok := out[key]; ok {
			return r, nil
		}
		return "", fmt.Errorf("unstubbed argv: %s", key)
	}
}

// ----- parseDeviceURI ----------------------------------------------------

func TestParseDeviceURI(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		want    string
		wantErr bool
	}{
		{"valid", "balena://device/7cf02a6", "7cf02a6", false},
		{"wrong scheme", "balena://fleet/o/f", "", true},
		{"empty uuid", "balena://device/", "", true},
		{"extra segment", "balena://device/a/b", "", true},
		{"flag shaped", "balena://device/-rf", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseDeviceURI(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
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

// ----- parseFleetURI -----------------------------------------------------

func TestParseFleetURI(t *testing.T) {
	cases := []struct {
		name    string
		uri     string
		want    string
		wantErr bool
	}{
		{"valid", "balena://fleet/myorg/myfleet", "myorg/myfleet", false},
		{"valid with releases suffix", "balena://fleet/myorg/myfleet/releases", "myorg/myfleet", false},
		{"wrong scheme", "balena://device/abc", "", true},
		{"single segment", "balena://fleet/onlyone", "", true},
		{"three segments", "balena://fleet/a/b/c", "", true},
		{"empty org", "balena://fleet//myfleet", "", true},
		{"flag shaped org", "balena://fleet/-x/myfleet", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseFleetURI(tc.uri)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("want error, got %q", got)
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

// ----- composite ---------------------------------------------------------

func TestCompositeHappyPath(t *testing.T) {
	run := fakeRunner(map[string]string{
		"a --json": `{"x":1}`, // parses as JSON -> embedded object
		"b":        "plain text",
	}, nil)
	out := composite(context.Background(), run, map[string]any{"id": "z"}, []sectionSpec{
		{key: "a", args: []string{"a", "--json"}},
		{key: "b", args: []string{"b"}},
	})
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("composite output is not valid JSON: %v\n%s", err, out)
	}
	if doc["id"] != "z" {
		t.Errorf("base field id = %v, want z", doc["id"])
	}
	if a, ok := doc["a"].(map[string]any); !ok || a["x"] != float64(1) {
		t.Errorf("section a not embedded as JSON object: %v", doc["a"])
	}
	if doc["b"] != "plain text" {
		t.Errorf("section b = %v, want plain text", doc["b"])
	}
	if _, ok := doc["partial"]; ok {
		t.Errorf("did not expect partial on all-success doc")
	}
}

func TestCompositePartialFailure(t *testing.T) {
	run := fakeRunner(
		map[string]string{"ok": "fine"},
		map[string]string{"bad": "boom: device offline"},
	)
	out := composite(context.Background(), run, nil, []sectionSpec{
		{key: "ok", args: []string{"ok"}},
		{key: "bad", args: []string{"bad"}},
	})
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc["ok"] != "fine" {
		t.Errorf("successful section missing: %v", doc["ok"])
	}
	if doc["partial"] != true {
		t.Errorf("want partial=true, got %v", doc["partial"])
	}
	errs, ok := doc["errors"].(map[string]any)
	if !ok {
		t.Fatalf("want errors object, got %T", doc["errors"])
	}
	if s, _ := errs["bad"].(string); !strings.Contains(s, "device offline") {
		t.Errorf("error for 'bad' not captured: %v", errs["bad"])
	}
	if _, present := doc["bad"]; present {
		t.Errorf("failed section should not be in doc body")
	}
}

func TestCompositeBenignError(t *testing.T) {
	run := fakeRunner(nil, map[string]string{
		"tag list --device d": "balena CLI error: No tags found",
	})
	out := composite(context.Background(), run, nil, []sectionSpec{
		{key: "tags", args: []string{"tag", "list", "--device", "d"}, benign: "No tags found"},
	})
	var doc map[string]any
	if err := json.Unmarshal([]byte(out), &doc); err != nil {
		t.Fatalf("not valid JSON: %v", err)
	}
	if doc["tags"] != "No tags found" {
		t.Errorf("benign empty-state not mapped to success: %v", doc["tags"])
	}
	if _, ok := doc["partial"]; ok {
		t.Errorf("benign error should not mark the doc partial")
	}
}

// ----- composer functions (parse + compose) ------------------------------

func TestResourceComposers(t *testing.T) {
	run := fakeRunner(map[string]string{
		"whoami":                         "user: schuby",
		"organization list":              "org table",
		"fleet list --json":              `[{"slug":"o/f"}]`,
		"device-type list --json":        `[{"slug":"raspberrypi4"}]`,
		"device dev1 --json":             `{"uuid":"dev1","status":"online"}`,
		"device logs dev1":               "log line 1",
		"env list --device dev1 --json":  `[]`,
		"tag list --device dev1":         "tag table",
		"fleet o/f --json":               `{"slug":"o/f"}`,
		"device list --fleet o/f --json": `[]`,
		"env list --fleet o/f --json":    `[]`,
		"release list o/f --json":        `[{"commit":"abc"}]`,
	}, nil)

	cases := []struct {
		name     string
		call     func() ([]mcp.ResourceContents, error)
		wantURI  string
		contains []string // substrings expected in the JSON text
	}{
		{"account", func() ([]mcp.ResourceContents, error) {
			return accountResource(context.Background(), run, "balena://account")
		}, "balena://account", []string{"schuby", "organizations"}},
		{"fleets", func() ([]mcp.ResourceContents, error) {
			return fleetsResource(context.Background(), run, "balena://fleets")
		}, "balena://fleets", []string{"o/f", "fleets"}},
		{"device-types", func() ([]mcp.ResourceContents, error) {
			return deviceTypesResource(context.Background(), run, "balena://device-types")
		}, "balena://device-types", []string{"raspberrypi4", "device_types"}},
		{"device", func() ([]mcp.ResourceContents, error) {
			return deviceResource(context.Background(), run, "balena://device/dev1")
		}, "balena://device/dev1", []string{"dev1", "online", "log line 1", "info", "logs", "env", "tags"}},
		{"fleet", func() ([]mcp.ResourceContents, error) {
			return fleetResource(context.Background(), run, "balena://fleet/o/f")
		}, "balena://fleet/o/f", []string{"devices", "releases", "abc"}},
		{"fleet releases", func() ([]mcp.ResourceContents, error) {
			return fleetReleasesResource(context.Background(), run, "balena://fleet/o/f/releases")
		}, "balena://fleet/o/f/releases", []string{"releases", "abc"}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := tc.call()
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text, uri, mime := resourceText(t, cs)
			if uri != tc.wantURI {
				t.Errorf("uri = %q, want %q", uri, tc.wantURI)
			}
			if mime != resourceMIME {
				t.Errorf("mime = %q, want %q", mime, resourceMIME)
			}
			var doc any
			if err := json.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatalf("resource text is not valid JSON: %v\n%s", err, text)
			}
			for _, want := range tc.contains {
				if !strings.Contains(text, want) {
					t.Errorf("resource text missing %q\n---\n%s", want, text)
				}
			}
		})
	}
}

// ----- composer parse-error paths ----------------------------------------

func TestResourceComposersParseError(t *testing.T) {
	run := fakeRunner(map[string]string{}, nil) // never called; parse fails first
	if _, err := deviceResource(context.Background(), run, "balena://device/"); err == nil {
		t.Error("deviceResource: want error on malformed URI")
	}
	if _, err := fleetResource(context.Background(), run, "balena://fleet/bad"); err == nil {
		t.Error("fleetResource: want error on malformed URI")
	}
	if _, err := fleetReleasesResource(context.Background(), run, "balena://fleet/bad/releases"); err == nil {
		t.Error("fleetReleasesResource: want error on malformed URI")
	}
}

// ----- exported handlers in dry-run --------------------------------------

// TestResourceHandlersDryRun exercises the thin exported handlers end to end
// through the real executeCommand in dry-run mode, confirming each returns a
// valid-JSON document for the right URI.
func TestResourceHandlersDryRun(t *testing.T) {
	prev := Config.DryRun
	Config.DryRun = true
	defer func() { Config.DryRun = prev }()

	type handler func(context.Context, mcp.ReadResourceRequest) ([]mcp.ResourceContents, error)
	cases := []struct {
		name string
		h    handler
		uri  string
	}{
		{"account", handleAccountResource, "balena://account"},
		{"fleets", handleFleetsResource, "balena://fleets"},
		{"device-types", handleDeviceTypesResource, "balena://device-types"},
		{"device", handleDeviceResource, "balena://device/abc123"},
		{"fleet", handleFleetResource, "balena://fleet/myorg/myfleet"},
		{"fleet releases", handleFleetReleasesResource, "balena://fleet/myorg/myfleet/releases"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cs, err := tc.h(context.Background(), readReq(tc.uri))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			text, uri, _ := resourceText(t, cs)
			if uri != tc.uri {
				t.Errorf("uri = %q, want %q", uri, tc.uri)
			}
			var doc any
			if err := json.Unmarshal([]byte(text), &doc); err != nil {
				t.Fatalf("dry-run resource text not valid JSON: %v\n%s", err, text)
			}
			if !strings.Contains(text, "[DRY RUN]") {
				t.Errorf("expected dry-run markers in output\n%s", text)
			}
		})
	}
}

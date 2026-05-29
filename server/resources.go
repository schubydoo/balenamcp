package server

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// Resources expose read-only balena state under the balena:// URI scheme.
// Unlike tools (one CLI call each, invoked by the model) a resource is
// application/user-attached context: the user drops a fleet or device into the
// conversation and the model reads a single coherent document.
//
// The value over the equivalent read-only tools is COMPOSITION — a resource
// handler aggregates several balena CLI calls into one JSON document. The
// device snapshot, for example, folds device status, recent logs, env/config
// variables, and tags into a single read rather than four separate tool calls.
//
// Failure handling differs from tools too: a tool fails atomically, but a
// resource degrades gracefully. If one sub-call fails (e.g. logs for an
// offline device) the document still returns with the sections that succeeded,
// and records the failures under an "errors" object with "partial": true.

const resourceMIME = "application/json"

// cliRunner mirrors executeCommand's signature. Composition is written against
// it (rather than calling executeCommand directly) so unit tests can inject a
// stub and exercise the graceful-degradation path without shelling out.
type cliRunner func(ctx context.Context, args []string) (string, error)

// sectionSpec is one named sub-call in a composite document.
type sectionSpec struct {
	key  string
	args []string
	// benign, when set, maps a CLI error containing this substring to an
	// empty-state success (used for `tag list`, which exits non-zero with
	// "No tags found" on an empty tag set). Mirrors runCmdAllowingBenignError.
	benign string
}

// composite runs each section through run and assembles a JSON document. base
// seeds top-level fields (e.g. the identifier). A section whose CLI call
// emits JSON is embedded as parsed JSON; otherwise its trimmed text is
// embedded as a string. A failing section is recorded under "errors" and sets
// "partial": true rather than failing the whole read.
//
// No error is returned: the document only ever holds JSON-decoded values and
// strings, both of which marshal unconditionally, so json.MarshalIndent here
// cannot fail and its error is intentionally discarded.
func composite(ctx context.Context, run cliRunner, base map[string]any, specs []sectionSpec) string {
	doc := map[string]any{}
	for k, v := range base {
		doc[k] = v
	}
	errs := map[string]string{}
	for _, s := range specs {
		out, err := run(ctx, s.args)
		if err != nil {
			if s.benign != "" && strings.Contains(err.Error(), s.benign) {
				doc[s.key] = s.benign
				continue
			}
			errs[s.key] = err.Error()
			continue
		}
		var parsed any
		if json.Unmarshal([]byte(out), &parsed) == nil {
			doc[s.key] = parsed
		} else {
			doc[s.key] = strings.TrimSpace(out)
		}
	}
	if len(errs) > 0 {
		doc["errors"] = errs
		doc["partial"] = true
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	return string(b)
}

// textContents wraps a composed JSON document as the single content item of a
// resources/read response.
func textContents(uri, text string) []mcp.ResourceContents {
	return []mcp.ResourceContents{
		mcp.TextResourceContents{URI: uri, MIMEType: resourceMIME, Text: text},
	}
}

// ----- URI parsing --------------------------------------------------------

// parseDeviceURI extracts the uuid from "balena://device/<uuid>".
func parseDeviceURI(uri string) (string, error) {
	const prefix = "balena://device/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("not a device URI: %q", uri)
	}
	uuid := strings.TrimPrefix(uri, prefix)
	if uuid == "" || strings.Contains(uuid, "/") {
		return "", fmt.Errorf("malformed device URI %q: expected balena://device/<uuid>", uri)
	}
	if strings.HasPrefix(uuid, "-") {
		return "", fmt.Errorf("invalid device UUID %q: identifiers cannot start with '-'", uuid)
	}
	return uuid, nil
}

// parseFleetURI extracts the org/fleet slug from "balena://fleet/<org>/<fleet>",
// optionally suffixed with "/releases". Fleet slugs contain a slash, so the
// template uses two path params reassembled here.
func parseFleetURI(uri string) (string, error) {
	const prefix = "balena://fleet/"
	if !strings.HasPrefix(uri, prefix) {
		return "", fmt.Errorf("not a fleet URI: %q", uri)
	}
	rest := strings.TrimPrefix(uri, prefix)
	rest = strings.TrimSuffix(rest, "/releases")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", fmt.Errorf("malformed fleet URI %q: expected balena://fleet/<org>/<fleet>", uri)
	}
	if strings.HasPrefix(parts[0], "-") {
		return "", fmt.Errorf("invalid fleet slug %q: identifiers cannot start with '-'", parts[0])
	}
	return parts[0] + "/" + parts[1], nil
}

// ----- registration -------------------------------------------------------

// registerResources wires the read-only balena state resources onto srv:
// three static resources and three URI templates. Kept as a thin dispatcher so
// each definition stays greppable and the function stays under gocyclo's
// complexity ceiling.
func registerResources(srv *server.MCPServer) {
	srv.AddResource(mcp.NewResource("balena://account", "balenaCloud account",
		mcp.WithResourceDescription("The authenticated user (whoami) and the organizations they belong to."),
		mcp.WithMIMEType(resourceMIME),
	), handleAccountResource)

	srv.AddResource(mcp.NewResource("balena://fleets", "balena fleets",
		mcp.WithResourceDescription("All fleets the authenticated user can access."),
		mcp.WithMIMEType(resourceMIME),
	), handleFleetsResource)

	srv.AddResource(mcp.NewResource("balena://device-types", "balena device types",
		mcp.WithResourceDescription("Supported balena device types."),
		mcp.WithMIMEType(resourceMIME),
	), handleDeviceTypesResource)

	srv.AddResourceTemplate(mcp.NewResourceTemplate("balena://device/{uuid}", "Device snapshot",
		mcp.WithTemplateDescription("Status, recent logs, env/config variables, and tags for one device, aggregated into a single document."),
		mcp.WithTemplateMIMEType(resourceMIME),
	), handleDeviceResource)

	srv.AddResourceTemplate(mcp.NewResourceTemplate("balena://fleet/{org}/{fleet}", "Fleet snapshot",
		mcp.WithTemplateDescription("Fleet metadata, its devices, its env/config variables, and its releases, aggregated into a single document."),
		mcp.WithTemplateMIMEType(resourceMIME),
	), handleFleetResource)

	srv.AddResourceTemplate(mcp.NewResourceTemplate("balena://fleet/{org}/{fleet}/releases", "Fleet releases",
		mcp.WithTemplateDescription("The release history of a fleet."),
		mcp.WithTemplateMIMEType(resourceMIME),
	), handleFleetReleasesResource)
}

// ----- handlers -----------------------------------------------------------
//
// Each thin handler delegates to a function taking an injectable cliRunner so
// the composition logic is unit-testable. The exported handlers bind the real
// executeCommand.

func handleAccountResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return accountResource(ctx, executeCommand, req.Params.URI)
}

func accountResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	doc := composite(ctx, run, nil, []sectionSpec{
		{key: "whoami", args: []string{"whoami"}},
		{key: "organizations", args: []string{"organization", "list"}},
	})
	return textContents(uri, doc), nil
}

func handleFleetsResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return fleetsResource(ctx, executeCommand, req.Params.URI)
}

func fleetsResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	doc := composite(ctx, run, nil, []sectionSpec{
		{key: "fleets", args: []string{"fleet", "list", "--json"}},
	})
	return textContents(uri, doc), nil
}

func handleDeviceTypesResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return deviceTypesResource(ctx, executeCommand, req.Params.URI)
}

func deviceTypesResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	doc := composite(ctx, run, nil, []sectionSpec{
		{key: "device_types", args: []string{"device-type", "list", "--json"}},
	})
	return textContents(uri, doc), nil
}

func handleDeviceResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return deviceResource(ctx, executeCommand, req.Params.URI)
}

func deviceResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	uuid, err := parseDeviceURI(uri)
	if err != nil {
		return nil, err
	}
	doc := composite(ctx, run, map[string]any{"uuid": uuid}, []sectionSpec{
		{key: "info", args: []string{"device", uuid, "--json"}},
		{key: "logs", args: []string{"device", "logs", uuid}},
		{key: "env", args: []string{"env", "list", "--device", uuid, "--json"}},
		{key: "tags", args: []string{"tag", "list", "--device", uuid}, benign: "No tags found"},
	})
	return textContents(uri, doc), nil
}

func handleFleetResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return fleetResource(ctx, executeCommand, req.Params.URI)
}

func fleetResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	slug, err := parseFleetURI(uri)
	if err != nil {
		return nil, err
	}
	doc := composite(ctx, run, map[string]any{"fleet": slug}, []sectionSpec{
		{key: "info", args: []string{"fleet", slug, "--json"}},
		{key: "devices", args: []string{"device", "list", "--fleet", slug, "--json"}},
		{key: "env", args: []string{"env", "list", "--fleet", slug, "--json"}},
		{key: "releases", args: []string{"release", "list", slug, "--json"}},
	})
	return textContents(uri, doc), nil
}

func handleFleetReleasesResource(ctx context.Context, req mcp.ReadResourceRequest) ([]mcp.ResourceContents, error) {
	return fleetReleasesResource(ctx, executeCommand, req.Params.URI)
}

func fleetReleasesResource(ctx context.Context, run cliRunner, uri string) ([]mcp.ResourceContents, error) {
	slug, err := parseFleetURI(uri)
	if err != nil {
		return nil, err
	}
	doc := composite(ctx, run, map[string]any{"fleet": slug}, []sectionSpec{
		{key: "releases", args: []string{"release", "list", slug, "--json"}},
	})
	return textContents(uri, doc), nil
}

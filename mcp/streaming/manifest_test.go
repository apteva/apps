package main

import (
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// The embedded manifest must always parse — it's the source of truth
// the binary advertises. If this test fails, the binary won't survive
// sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "streaming" {
		t.Errorf("manifest.Name=%q, want streaming", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 11 {
		t.Errorf("expected 11 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	gotScopes := map[string]bool{}
	for _, s := range m.Scopes {
		gotScopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !gotScopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}

// Manifest's mcp_tools list must agree with MCPTools() on count + names.
// CRM has the same drift guard.
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares tool %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

// Names alone aren't enough: a tool whose schema doesn't describe its
// arguments is invisible to the agent even though every name-level
// check passes. Assert the shape of every declared schema.
func TestMCPTools_SchemasWellFormed(t *testing.T) {
	app := &App{}
	// Args the platform contract says are mandatory, per tool.
	wantRequired := map[string][]string{
		"streams_create":         {"name"},
		"streams_get":            {"id"},
		"streams_stop":           {"id"},
		"streams_delete":         {"id"},
		"streams_rotate_key":     {"id"},
		"streams_get_metrics":    {"id"},
		"streams_replay_url":     {"id"},
		"streams_signed_url":     {"id", "expires_in_seconds"},
		"streams_set_url_policy": {"id", "require_signed_urls"},
		"streams_load_test":      {"id"},
	}

	for _, tool := range app.MCPTools() {
		if strings.TrimSpace(tool.Description) == "" {
			t.Errorf("%s: empty description", tool.Name)
		}
		if tool.Handler == nil {
			t.Errorf("%s: nil handler", tool.Name)
		}
		if tool.InputSchema == nil {
			t.Fatalf("%s: nil InputSchema", tool.Name)
		}
		if got := tool.InputSchema["type"]; got != "object" {
			t.Errorf("%s: schema type=%v, want object", tool.Name, got)
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("%s: schema has no properties", tool.Name)
			continue
		}
		for pname, praw := range props {
			p, ok := praw.(map[string]any)
			if !ok {
				t.Errorf("%s.%s: property is not an object", tool.Name, pname)
				continue
			}
			switch p["type"] {
			case "string", "integer", "number", "boolean", "array", "object":
			default:
				t.Errorf("%s.%s: bad property type %v", tool.Name, pname, p["type"])
			}
		}

		required := map[string]bool{}
		if raw, ok := tool.InputSchema["required"].([]string); ok {
			for _, r := range raw {
				required[r] = true
				if _, declared := props[r]; !declared {
					t.Errorf("%s: required arg %q isn't in properties", tool.Name, r)
				}
			}
		}
		for _, want := range wantRequired[tool.Name] {
			if !required[want] {
				t.Errorf("%s: %q should be required", tool.Name, want)
			}
		}
	}
}

// apteva.yaml on disk is what the platform installs from; the
// embedded manifestYAML is what the running binary reports. A
// version mismatch between them is a known release-process failure in
// this workspace, and a tool declared in only one of the two means the
// installed app advertises a surface it doesn't have (or vice versa).
func TestEmbeddedManifest_MatchesAptevaYAML(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatalf("read apteva.yaml: %v", err)
	}
	onDisk, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatalf("parse apteva.yaml: %v", err)
	}
	embedded := (&App{}).Manifest()

	if onDisk.Version != embedded.Version {
		t.Errorf("version drift: apteva.yaml=%q, embedded=%q", onDisk.Version, embedded.Version)
	}
	if onDisk.Name != embedded.Name {
		t.Errorf("name drift: apteva.yaml=%q, embedded=%q", onDisk.Name, embedded.Name)
	}

	diskTools := map[string]bool{}
	for _, tool := range onDisk.Provides.MCPTools {
		diskTools[tool.Name] = true
	}
	embeddedTools := map[string]bool{}
	for _, tool := range embedded.Provides.MCPTools {
		embeddedTools[tool.Name] = true
	}
	for name := range diskTools {
		if !embeddedTools[name] {
			t.Errorf("apteva.yaml declares %q, embedded manifest doesn't", name)
		}
	}
	for name := range embeddedTools {
		if !diskTools[name] {
			t.Errorf("embedded manifest declares %q, apteva.yaml doesn't", name)
		}
	}

	diskScopes := map[string]bool{}
	for _, s := range onDisk.Scopes {
		diskScopes[string(s)] = true
	}
	for _, s := range embedded.Scopes {
		if !diskScopes[string(s)] {
			t.Errorf("scope %q missing from apteva.yaml", s)
		}
	}
}

// The SDK owns /health, /manifest, /mcp, /events and /ui/ — an app
// route that collides with one of those panics the sidecar at boot
// ("Waiting for health check…" forever).
func TestHTTPRoutes_AvoidReservedPrefixes(t *testing.T) {
	reserved := []string{"/health", "/manifest", "/mcp", "/events", "/ui/"}
	app := &App{}
	for _, r := range app.HTTPRoutes() {
		if r.Pattern == "" {
			t.Error("route with empty pattern")
			continue
		}
		if r.Handler == nil {
			t.Errorf("route %q has nil handler", r.Pattern)
		}
		for _, res := range reserved {
			if r.Pattern == res || strings.HasPrefix(r.Pattern, res) ||
				(strings.HasSuffix(res, "/") && r.Pattern == strings.TrimSuffix(res, "/")) {
				t.Errorf("route %q collides with SDK-reserved prefix %q", r.Pattern, res)
			}
		}
	}
}

// Workers the app declares must all be wired — a nil Run is a
// scheduler panic at boot.
func TestWorkers_Wired(t *testing.T) {
	app := &App{}
	names := map[string]bool{}
	for _, w := range app.Workers() {
		if w.Run == nil {
			t.Errorf("worker %q has nil Run", w.Name)
		}
		if w.Schedule == "" {
			t.Errorf("worker %q has no schedule", w.Name)
		}
		names[w.Name] = true
	}
	for _, want := range []string{"viewer-counter", "runner-watchdog", "retention-sweeper"} {
		if !names[want] {
			t.Errorf("missing worker %q", want)
		}
	}
}

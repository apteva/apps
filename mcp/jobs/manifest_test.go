package main

import "testing"

// The embedded manifest must always parse — otherwise sdk.Run's
// ValidateManifest fails at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "jobs" {
		t.Errorf("manifest.Name=%q, want jobs", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 6 {
		t.Errorf("expected 6 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	if len(m.Provides.Workers) != 1 || m.Provides.Workers[0].Name != "dispatcher" {
		t.Errorf("expected one dispatcher worker, got %+v", m.Provides.Workers)
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

// MCPTools() and the manifest's mcp_tools list must agree on count
// and names. A common mistake is adding a tool to one and forgetting
// the other; this test catches it.
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

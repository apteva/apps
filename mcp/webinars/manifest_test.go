package main

import "testing"

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "webinars" {
		t.Errorf("manifest.Name=%q, want webinars", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 14 {
		t.Errorf("expected 14 MCP tools, got %d", len(m.Provides.MCPTools))
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

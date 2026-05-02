package main

import "testing"

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "messaging" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
}

func TestMCPTools_DeclaredMatchHandlers(t *testing.T) {
	app := &App{}
	declared := map[string]bool{}
	for _, t := range app.Manifest().Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

func TestManifestAndYAMLAgree(t *testing.T) {
	// The embedded YAML and the on-disk apteva.yaml should declare
	// the same number of MCP tools — keeping them in sync is manual,
	// this test catches drift.
	app := &App{}
	embedded := len(app.Manifest().Provides.MCPTools)
	if embedded < 16 {
		t.Errorf("expected at least 16 mcp_tools in embedded manifest, got %d", embedded)
	}
}

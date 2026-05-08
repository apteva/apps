package main

import "testing"

// Tier 1 — embedded manifest must parse and match the binary's
// MCP surface. If the embed drifts from MCPTools(), the binary
// won't survive sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "instances" {
		t.Errorf("manifest.Name=%q, want instances", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if got := len(m.Provides.MCPTools); got != 8 {
		t.Errorf("expected 8 MCP tools in manifest, got %d", got)
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	scopes := map[string]bool{}
	for _, s := range m.Scopes {
		scopes[string(s)] = true
	}
	if !scopes["global"] {
		t.Error("instances must declare scope 'global'")
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
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

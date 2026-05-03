package main

import "testing"

// The embedded manifest must always parse — it's our single source of
// truth for the manifest the binary advertises.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "trading" {
		t.Errorf("manifest.Name=%q, want trading", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 17 {
		t.Errorf("expected 17 MCP tools, got %d", len(m.Provides.MCPTools))
	}
}

// Tool list parity — adding a tool to the handler list without
// declaring it (or vice versa) is the most common single drift.
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
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
			t.Errorf("manifest declares tool %q but no handler implements it", name)
		}
	}
	for name := range implemented {
		if !declared[name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", name)
		}
	}
}

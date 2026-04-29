package main

import "testing"

// TestEmbeddedManifest_Valid sanity-checks the YAML the binary
// embeds — same shape we ship in apteva.yaml. If they drift, this
// breaks before anyone tries to install.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "media" {
		t.Errorf("name=%q", m.Name)
	}
	if m.Version == "" {
		t.Error("version empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Error("db.migrations missing")
	}
	// Four read-side tools is the contract. Ops endpoints (status,
	// reindex) live as plain HTTP routes, not MCP — agents query the
	// catalog, the panel does ops.
	if len(m.Provides.MCPTools) != 4 {
		t.Errorf("expected 4 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if len(m.Provides.Workers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(m.Provides.Workers))
	}
	if m.Provides.Workers[0].Schedule == "" {
		t.Error("indexer worker missing schedule")
	}
	if len(m.Requires.Apps) != 1 || m.Requires.Apps[0].Name != "storage" {
		t.Errorf("expected requires.apps=[storage], got %#v", m.Requires.Apps)
	}
}

// TestMCPTools_DeclaredMatchHandlers — manifest names and handler
// names must agree, otherwise the platform exposes a tool with no
// implementation and the SDK panics on dispatch.
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

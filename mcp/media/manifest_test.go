package main

import (
	"testing"

	sdk "github.com/apteva/app-sdk"
)

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
	// v0.9 surface: 6 catalog read + 2 folder ops + 7 render submit
	// + 3 render manage + 1 description setter + 3 transcript tools
	// + 1 describe = 23.
	if len(m.Provides.MCPTools) != 23 {
		t.Errorf("expected 23 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if len(m.Provides.Workers) != 1 {
		t.Errorf("expected 1 worker, got %d", len(m.Provides.Workers))
	}
	if m.Provides.Workers[0].Schedule == "" {
		t.Error("indexer worker missing schedule")
	}
	// Storage is required; jobs is an optional companion for scheduled renders.
	gotApps := map[string]sdk.RequiredAppRef{}
	for _, a := range m.Requires.Apps {
		gotApps[a.Name] = a
	}
	if _, ok := gotApps["storage"]; !ok {
		t.Errorf("expected requires.apps to include storage, got %#v", m.Requires.Apps)
	}
	if jobs, ok := gotApps["jobs"]; !ok || !jobs.Optional {
		t.Errorf("expected requires.apps to include optional jobs, got %#v", m.Requires.Apps)
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

package main

import "testing"

// The embedded manifest must always parse and round-trip the surface
// the binary exposes. If this drifts the binary won't survive sdk.Run's
// ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "simulator" {
		t.Errorf("manifest.Name=%q, want simulator", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	gotScopes := map[string]bool{}
	for _, s := range m.Scopes {
		gotScopes[string(s)] = true
	}
	if !gotScopes["project"] {
		t.Errorf("manifest missing scope project")
	}
	// Tool list count — bump when adding/removing tools. Keeping this
	// pinned is the cheapest way to catch "I added a tool to the
	// manifest but forgot the handler" (and vice versa) across chunks.
	wantTools := 12
	if got := len(m.Provides.MCPTools); got != wantTools {
		t.Errorf("expected %d MCP tools in manifest, got %d", wantTools, got)
	}
}

// The manifest's mcp_tools list and MCPTools() must agree on names in
// both directions — a handler with no spec won't show in the
// marketplace; a spec with no handler 500s when invoked.
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	declared := map[string]bool{}
	for _, t := range m.Provides.MCPTools {
		declared[t.Name] = true
	}
	implemented := map[string]bool{}
	for _, tool := range app.MCPTools() {
		implemented[tool.Name] = true
		if !declared[tool.Name] {
			t.Errorf("handler implements %q but manifest doesn't declare it", tool.Name)
		}
	}
	for name := range declared {
		if !implemented[name] {
			t.Errorf("manifest declares %q but no handler implements it", name)
		}
	}
}

// Every tool registered in MCPTools() needs an InputSchema with
// properties + a Handler. Catches the "schemaObject(props, nil)"
// copy-paste mistake. No-op while MCPTools() is empty.
func TestMCPTools_AllHaveSchemas(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.InputSchema == nil {
			t.Errorf("tool %q has no InputSchema", tool.Name)
			continue
		}
		props, ok := tool.InputSchema["properties"].(map[string]any)
		if !ok || len(props) == 0 {
			t.Errorf("tool %q has empty/missing properties", tool.Name)
		}
		if tool.Handler == nil && tool.HandlerCtx == nil {
			t.Errorf("tool %q has nil Handler / HandlerCtx", tool.Name)
		}
	}
}

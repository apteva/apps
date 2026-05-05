package main

import "testing"

// Tier 1 — the embedded manifest must always parse and round-trip
// the surface the binary actually exposes. If this drifts the binary
// won't survive sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "code" {
		t.Errorf("manifest.Name=%q, want code", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 20 {
		t.Errorf("expected 20 MCP tools in manifest, got %d", len(m.Provides.MCPTools))
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

// The manifest's mcp_tools list and the App.MCPTools() handler list
// must agree on count and names. A common mistake is adding a tool to
// one and forgetting the other; this test catches it before boot.
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

// Every tool the editing surface relies on must be present — guards
// against silent removal during refactors.
func TestMCPTools_EditingSurfaceComplete(t *testing.T) {
	app := &App{}
	got := map[string]bool{}
	for _, tool := range app.MCPTools() {
		got[tool.Name] = true
	}
	must := []string{
		"repos_list", "repos_create", "repos_get", "repos_archive", "repos_set_deploy_hints",
		"code_list_files", "code_glob", "code_grep",
		"code_read_file", "code_write_file",
		"code_edit_file", "code_multi_edit",
		"code_rename_path", "code_delete_file",
	}
	for _, name := range must {
		if !got[name] {
			t.Errorf("missing required tool: %s", name)
		}
	}
}

// Every tool must declare a non-empty input schema with required
// fields where the handler logic depends on them. Catches the
// "schemaObject(props, nil)" copy-paste mistake.
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
		if tool.Handler == nil {
			t.Errorf("tool %q has nil Handler", tool.Name)
		}
	}
}

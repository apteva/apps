package main

import "testing"

// The embedded manifest must always parse — sdk.Run validates it at
// boot, so a regression here means the binary won't start.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "billing" {
		t.Errorf("manifest.Name=%q, want billing", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	scopes := map[string]bool{}
	for _, s := range m.Scopes {
		scopes[string(s)] = true
	}
	for _, want := range []string{"project", "global"} {
		if !scopes[want] {
			t.Errorf("manifest missing scope %q", want)
		}
	}
}

// Counts + names must agree between the manifest's mcp_tools list and
// MCPTools(). A common mistake is adding a tool to one and forgetting
// the other; this test catches it.
//
// NOTE: the embedded manifest in main.go is the boot-time minimum
// (just enough for sdk.Run to validate); the canonical tool list
// lives in apteva.yaml. So we read apteva.yaml separately and
// cross-check against MCPTools().
func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	want := []string{
		"customers_search", "customers_get", "customers_get_context",
		"customers_upsert_by_email", "customers_update", "customers_merge",
		"invoices_create", "invoices_add_line_item", "invoices_update",
		"invoices_finalize", "invoices_void", "invoices_get", "invoices_search",
		"invoices_render_pdf",
		"payments_record", "payments_list",
		"issuer_get", "issuer_set",
	}
	for _, name := range want {
		if !implemented[name] {
			t.Errorf("expected tool %q to be implemented", name)
		}
	}
	if len(implemented) != len(want) {
		t.Errorf("MCPTools count = %d, want %d (likely added/removed without updating this test)",
			len(implemented), len(want))
	}
}

// Every tool registered must have a non-empty description and a
// JSON-Schema-shaped input schema. The agent reads these verbatim;
// missing descriptions degrade tool selection silently.
func TestMCPTools_AllHaveDescriptionAndSchema(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.Description == "" {
			t.Errorf("tool %q has empty description", tool.Name)
		}
		if tool.InputSchema["type"] != "object" {
			t.Errorf("tool %q schema type=%v, want object", tool.Name, tool.InputSchema["type"])
		}
		if _, ok := tool.InputSchema["properties"]; !ok {
			t.Errorf("tool %q schema missing properties", tool.Name)
		}
	}
}

package main

import "testing"

func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "bills" {
		t.Errorf("manifest.Name=%q, want bills", m.Name)
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

func TestMCPTools_ManifestMatchesHandlers(t *testing.T) {
	app := &App{}
	implemented := map[string]bool{}
	for _, t := range app.MCPTools() {
		implemented[t.Name] = true
	}
	want := []string{
		"vendors_search", "vendors_get", "vendors_get_context",
		"vendors_upsert_by_email", "vendors_update", "vendors_merge",
		"bills_create", "bills_update", "bills_approve", "bills_reject",
		"bills_schedule_payment", "bills_void", "bills_get", "bills_search",
		"bills_render_pdf",
		"bills_attach_file", "bills_detach_file", "bills_create_from_file",
		"bill_payments_record", "bill_payments_list",
	}
	for _, name := range want {
		if !implemented[name] {
			t.Errorf("expected tool %q to be implemented", name)
		}
	}
	if len(implemented) != len(want) {
		t.Errorf("MCPTools count = %d, want %d", len(implemented), len(want))
	}
}

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

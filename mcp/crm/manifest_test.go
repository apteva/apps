package main

import "testing"

// The embedded manifest must always parse — it's our single source of
// truth for the manifest the binary advertises. If this test fails,
// the binary won't survive sdk.Run's ValidateManifest at boot.
func TestEmbeddedManifest_Valid(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	if m.Name != "crm" {
		t.Errorf("manifest.Name=%q, want crm", m.Name)
	}
	if m.Version == "" {
		t.Error("manifest.Version is empty")
	}
	if len(m.Provides.MCPTools) != 42 {
		t.Errorf("expected 42 MCP tools, got %d", len(m.Provides.MCPTools))
	}
	if m.DB == nil || m.DB.Migrations == "" {
		t.Errorf("manifest.DB.Migrations missing")
	}
	if len(m.Provides.Publishes) != 16 {
		t.Errorf("expected 16 published event declarations, got %d", len(m.Provides.Publishes))
	}
	// Surfaces the embedded scopes — should accept project + global.
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

func TestEmbeddedManifest_PublishesCRMEvents(t *testing.T) {
	app := &App{}
	m := app.Manifest()
	got := map[string]bool{}
	for _, ev := range m.Provides.Publishes {
		got[ev.Name] = true
		if ev.Description == "" {
			t.Errorf("event %q missing description", ev.Name)
		}
	}
	for _, want := range []string{
		"contact.added",
		"contact.updated",
		"contact.deleted",
		"contact.merged",
		"contact.activity.added",
		"conversation.status.changed",
		"conversation.message.received",
		"list.created",
		"list.updated",
		"list.archived",
		"list.member.added",
		"list.member.removed",
		"segment.created",
		"segment.updated",
		"segment.archived",
		"segment.materialised",
	} {
		if !got[want] {
			t.Errorf("manifest missing published event %q", want)
		}
	}
}

// ToolHandlers and the manifest's mcp_tools list must agree on count
// and names. A common mistake is adding a tool to one and forgetting
// the other; this test catches it.
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

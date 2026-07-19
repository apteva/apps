package main

import (
	"os"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

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
	if len(m.Provides.MCPTools) != 37 {
		t.Errorf("expected 37 MCP tools in manifest, got %d", len(m.Provides.MCPTools))
	}
	if len(m.Provides.UIComponents) != 3 {
		t.Errorf("expected 3 UI components in manifest, got %d", len(m.Provides.UIComponents))
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

func TestUIComponents_CompleteAndDiskManifestMatches(t *testing.T) {
	embedded := (&App{}).Manifest()
	body, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	disk, err := sdk.ParseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if disk.Version != embedded.Version {
		t.Fatalf("disk version %q does not match embedded version %q", disk.Version, embedded.Version)
	}

	want := map[string]string{
		"repository-card":  "/ui/RepositoryCard.mjs",
		"source-file-card": "/ui/SourceFileCard.mjs",
		"issue-card":       "/ui/IssueCard.mjs",
	}
	for _, manifest := range []sdk.Manifest{embedded, *disk} {
		if len(manifest.Provides.UIComponents) != len(want) {
			t.Fatalf("manifest has %d components, want %d", len(manifest.Provides.UIComponents), len(want))
		}
		for _, component := range manifest.Provides.UIComponents {
			entry, ok := want[component.Name]
			if !ok {
				t.Errorf("unexpected component %q", component.Name)
				continue
			}
			if component.Entry != entry {
				t.Errorf("component %q entry=%q, want %q", component.Name, component.Entry, entry)
			}
			if _, err := os.Stat("." + component.Entry); err != nil {
				t.Errorf("component %q bundle is missing: %v", component.Name, err)
			}
			if len(component.Slots) != 1 || component.Slots[0] != "chat.message_attachment" {
				t.Errorf("component %q has invalid slots: %v", component.Name, component.Slots)
			}
			if len(component.PropsSchema) == 0 || component.PropsSchema["required"] == nil {
				t.Errorf("component %q has no required props schema", component.Name)
			}
			if component.PreviewProps["preview"] != true {
				t.Errorf("component %q has no live preview props", component.Name)
			}
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
		"repos_run_command",
		"code_list_files", "code_glob", "code_grep",
		"code_read_file", "code_read_excerpt", "code_file_outline",
		"code_write_file", "code_apply_patch", "code_edit_file", "code_multi_edit",
		"code_rename_path", "code_delete_file",
		"issues_list", "issues_search", "issues_get", "issues_create", "issues_update",
		"issues_comment", "issues_close", "issues_reopen", "issues_link_path",
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

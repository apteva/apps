package main

import (
	"reflect"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// Keep an explicit allowlist: a read from a provider can still mutate Catalog
// (hosting_check and hosting_link_existing), so name verbs are insufficient.
func catalogReadOnlyTools() map[string]bool {
	return map[string]bool{
		"content_catalog_overview":                true,
		"content_catalog_search":                  true,
		"content_catalog_brands_list":             true,
		"content_catalog_sessions_list":           true,
		"content_catalog_sessions_get":            true,
		"content_catalog_session_upload_target":   true,
		"content_catalog_import_preview":          true,
		"content_catalog_assets_list":             true,
		"content_catalog_assets_get":              true,
		"content_catalog_asset_publications_list": true,
		"content_catalog_posts_list":              true,
		"content_catalog_posts_get":               true,
		"content_catalog_hosting_list":            true,
		"content_catalog_assets_eligibility":      true,
		"content_catalog_lifecycle_history":       true,
	}
}

func TestCatalogReadOnlyToolAnnotations(t *testing.T) {
	readOnly := catalogReadOnlyTools()
	want := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}
	for _, tool := range (&App{}).MCPTools() {
		if readOnly[tool.Name] {
			if !reflect.DeepEqual(tool.Annotations, want) {
				t.Errorf("%s annotations = %#v; want %#v", tool.Name, tool.Annotations, want)
			}
			delete(readOnly, tool.Name)
		} else if tool.Annotations["readOnlyHint"] == true {
			t.Errorf("mutation %s must not advertise readOnlyHint", tool.Name)
		}
	}
	if len(readOnly) != 0 {
		t.Fatalf("missing read-only tools: %#v", readOnly)
	}
}

func TestCatalogToolsListEmitsReadOnlyAnnotations(t *testing.T) {
	sidecar := tk.SpawnSidecar(t, ".", tk.WithProjectID("project-a"))
	listed, err := sidecar.MCPRaw("tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	readOnly := catalogReadOnlyTools()
	want := map[string]any{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true}
	for _, item := range listed["tools"].([]any) {
		tool := item.(map[string]any)
		name := tool["name"].(string)
		if readOnly[name] {
			if !reflect.DeepEqual(tool["annotations"], want) {
				t.Errorf("tools/list %s annotations = %#v", name, tool["annotations"])
			}
			delete(readOnly, name)
		} else if annotations, ok := tool["annotations"].(map[string]any); ok && annotations["readOnlyHint"] == true {
			t.Errorf("tools/list mutation %s advertised read-only", name)
		}
	}
	if len(readOnly) != 0 {
		t.Fatalf("tools/list missing read-only tools: %#v", readOnly)
	}
}

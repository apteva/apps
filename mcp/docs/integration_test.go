//go:build integration

package main

// Tier 2: real binary, real HTTP, real storage. SpawnSidecar
// compiles + boots docs and the storage dep alongside it; calls go
// through the testkit's in-process platform proxy so cross-app
// CallApp("storage", ...) hits a real running storage sidecar with
// its own auth stack — no mocks.
//
// Run with:
//   go test -tags=integration ./...
//
// Skipped by default; the build tag keeps tier-1 fast.

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_HealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

// Templates CRUD without storage spawned — exercises the local DB
// path. Cheap (one sidecar) and confirms templates read/write loop.
func TestSidecar_TemplatesCRUD(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	created := sc.MCP("docs_create_template", map[string]any{
		"slug": "nda",
		"name": "Standard NDA",
		"body": "# NDA\n\nFor: {{.party.name}}\n",
	})
	if created["created"] != true {
		t.Fatalf("create: %+v", created)
	}
	tpl, ok := created["template"].(map[string]any)
	if !ok || tpl["slug"] != "nda" {
		t.Fatalf("created template malformed: %+v", created)
	}
	id := tpl["id"]

	got := sc.MCP("docs_get_template", map[string]any{"slug": "nda"})
	if got["found"] != true {
		t.Fatalf("get by slug: %+v", got)
	}

	listed := sc.MCP("docs_list_templates", map[string]any{})
	templates, _ := listed["templates"].([]any)
	if len(templates) != 1 {
		t.Errorf("list count = %d, want 1", len(templates))
	}

	updated := sc.MCP("docs_update_template", map[string]any{
		"id":   id,
		"name": "NDA (revised)",
	})
	if updated["updated"] != true {
		t.Fatalf("update: %+v", updated)
	}

	deleted := sc.MCP("docs_delete_template", map[string]any{"id": id})
	if deleted["deleted"] != true {
		t.Fatalf("delete: %+v", deleted)
	}
}

// End-to-end: create a template, render it with data, verify the
// returned file_id resolves through storage's URL machinery. The
// docs sidecar's CallApp goes through the testkit gateway to the
// real storage sidecar; on success docs_render returns the storage
// file id + absolute URL we can poke around with.
func TestSidecar_RenderRoundtrip(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithDependency("storage", "../storage"),
	)

	created := sc.MCP("docs_create_template", map[string]any{
		"slug": "invoice-test",
		"name": "Invoice (test)",
		"body": "# Invoice {{.invoice.number}}\n\n**Bill to:** {{.customer.name}}\n\nTotal: ${{.invoice.total}}\n",
	})
	if created["created"] != true {
		t.Fatalf("create: %+v", created)
	}

	rendered := sc.MCP("docs_render", map[string]any{
		"template_slug": "invoice-test",
		"data": map[string]any{
			"invoice":  map[string]any{"number": "INV-2026-001", "total": "1250.00"},
			"customer": map[string]any{"name": "Acme Corp"},
		},
		"output_name": "test-invoice.pdf",
	})
	fileID, ok := rendered["file_id"]
	if !ok || fileID == nil {
		t.Fatalf("docs_render missing file_id: %+v", rendered)
	}
	url, _ := rendered["url"].(string)
	if !strings.HasSuffix(url, ".pdf") {
		t.Errorf("URL doesn't end .pdf: %q", url)
	}
	renderID, _ := rendered["render_id"].(float64)
	if renderID == 0 {
		t.Errorf("render_id missing: %+v", rendered)
	}

	// Audit row was written.
	listed := sc.MCP("docs_list_renders", map[string]any{"limit": 5})
	rs, _ := listed["renders"].([]any)
	if len(rs) < 1 {
		t.Fatalf("audit empty after render: %+v", listed)
	}
	first, _ := rs[0].(map[string]any)
	if first["template_slug"] != "invoice-test" {
		t.Errorf("audit template_slug = %v", first["template_slug"])
	}
}

// Preview returns base64 PDF bytes without persisting — for the
// panel's editor live-preview pane. Validates the in-process render
// path independently of storage upload.
func TestSidecar_Preview(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	out := sc.MCP("docs_preview", map[string]any{
		"body": "# Hi {{.name}}\n\nA paragraph.",
		"data": map[string]any{"name": "World"},
	})
	if out["content_type"] != "application/pdf" {
		t.Errorf("content_type = %v", out["content_type"])
	}
	b64, _ := out["base64"].(string)
	if len(b64) < 100 {
		t.Errorf("preview base64 too short: %d chars", len(b64))
	}
	// %PDF- in base64 starts with "JVBERi0".
	if !strings.HasPrefix(b64, "JVBERi0") {
		t.Errorf("preview base64 doesn't start with PDF magic: %q", b64[:min(20, len(b64))])
	}
}

// Slug already taken → friendly error, not a SQL string.
func TestSidecar_DuplicateSlug(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))

	sc.MCP("docs_create_template", map[string]any{
		"slug": "dup", "name": "First", "body": "# x",
	})
	resp, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "docs_create_template",
		"arguments": map[string]any{"slug": "dup", "name": "Second", "body": "# x"},
	})
	if err == nil && resp["error"] == nil {
		t.Fatalf("duplicate slug should fail, got: %+v", resp)
	}
}

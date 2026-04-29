//go:build integration

package main

// Tier 2 — the real binary, real HTTP. Boot the sidecar via the SDK
// testkit, talk MCP + REST. Validates the full SDK wiring (manifest
// parse, migrations, JSON-RPC dispatch, route mounting, /health,
// auth header) and the edit/grep flows end-to-end against a real
// LocalFileStore on a real temp dir.
//
// Run with:  go test -tags integration ./...

import (
	"strconv"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_BootsAndHealthOK(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"))
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if got["ok"] != true {
		t.Errorf("/health body=%v", got)
	}
}

func TestSidecar_FullRepoLifecycle(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)

	// 1. Create a Next.js repo via MCP — template should populate it.
	r := sc.MCP("repos_create", map[string]any{
		"name":      "Marketing Site",
		"framework": "nextjs",
	})
	repo := r["repository"].(map[string]any)
	slug := repo["slug"].(string)
	if slug != "marketing-site" {
		t.Fatalf("slug=%q, want marketing-site", slug)
	}
	if r["files_created"].(float64) < 4 {
		t.Errorf("expected >=4 template files, got %v", r["files_created"])
	}

	// 2. The tree should contain the Next.js skeleton.
	tree := sc.MCP("code_list_files", map[string]any{"slug": slug})
	files := tree["files"].([]any)
	have := map[string]bool{}
	for _, f := range files {
		have[f.(map[string]any)["path"].(string)] = true
	}
	for _, want := range []string{"package.json", "next.config.js", "app/page.tsx"} {
		if !have[want] {
			t.Errorf("template missing %q after create", want)
		}
	}

	// 3. Read the page with line numbers.
	read := sc.MCP("code_read_file", map[string]any{
		"slug": slug,
		"path": "app/page.tsx",
	})
	content := read["content"].(string)
	if !strings.Contains(content, "Hello from Apteva") {
		t.Errorf("template page.tsx unexpected: %q", content)
	}
	if !strings.Contains(content, "\t") {
		t.Errorf("expected tab-separated cat -n line numbers in: %q", content)
	}

	// 4. Edit a unique line — must succeed.
	edit := sc.MCP("code_edit_file", map[string]any{
		"slug":       slug,
		"path":       "app/page.tsx",
		"old_string": "Hello from Apteva.",
		"new_string": "Hello from the integration test.",
	})
	if edit["replacements"].(float64) != 1 {
		t.Errorf("replacements=%v, want 1", edit["replacements"])
	}

	// 5. Edit something that appears multiple times — should fail with
	// uniqueness error including line numbers.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "code_edit_file",
		"arguments": map[string]any{
			"slug":       slug,
			"path":       "package.json",
			"old_string": ".",
			"new_string": ",",
		},
	})
	if err == nil {
		t.Error("expected uniqueness error on common substring")
	} else if !strings.Contains(err.Error(), "not unique") && !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error shape: %v", err)
	}

	// 6. Grep across the repo for the new content.
	grep := sc.MCP("code_grep", map[string]any{
		"slug":    slug,
		"pattern": "integration test",
	})
	matches := grep["matches"].([]any)
	if len(matches) != 1 {
		t.Errorf("grep matches=%d, want 1", len(matches))
	}

	// 7. Glob — find every TSX.
	glob := sc.MCP("code_glob", map[string]any{
		"slug":    slug,
		"pattern": "**/*.tsx",
	})
	if glob["count"].(float64) != 2 {
		t.Errorf("glob count=%v, want 2", glob["count"])
	}

	// 8. Multi-edit a single file atomically.
	multi := sc.MCP("code_multi_edit", map[string]any{
		"slug": slug,
		"path": "app/page.tsx",
		"edits": []any{
			map[string]any{"old_string": "main", "new_string": "section", "replace_all": true},
			map[string]any{"old_string": "fontFamily", "new_string": "font-family-stub"},
		},
	})
	if multi["operation_count"].(float64) != 2 {
		t.Errorf("multi-edit ops=%v", multi["operation_count"])
	}

	// 9. REST — fetch the tree via the HTTP mirror.
	var restTree map[string]any
	resp := sc.GET("/api/repos/"+slug+"/tree", &restTree)
	if resp.Status != 200 {
		t.Fatalf("REST /tree: %d body=%s", resp.Status, string(resp.Body))
	}
	if restTree["count"].(float64) < 4 {
		t.Errorf("REST tree count=%v, want >=4", restTree["count"])
	}

	// 10. List repos — should show our one.
	list := sc.MCP("repos_list", nil)
	if list["count"].(float64) != 1 {
		t.Errorf("repos_list count=%v", list["count"])
	}
}

func TestSidecar_PathTraversalRejected(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)
	sc.MCP("repos_create", map[string]any{"name": "x"})

	// Try to escape the repo via .. — must be rejected.
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name": "code_write_file",
		"arguments": map[string]any{
			"slug":    "x",
			"path":    "../../../etc/passwd",
			"content": "pwned",
		},
	})
	if err == nil {
		t.Fatal("expected path-escape rejection")
	}
	if !strings.Contains(err.Error(), "escape") && !strings.Contains(err.Error(), "path") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestSidecar_ProjectScopeIsolation(t *testing.T) {
	// Two sidecars on different projects must not see each other's
	// repos. Each gets its own DB so the project_id column is the
	// belt; the temp-dir isolation is the suspenders.
	a := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("proj-A"),
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)
	a.MCP("repos_create", map[string]any{"name": "secret"})
	out := a.MCP("repos_list", nil)
	if out["count"].(float64) != 1 {
		t.Errorf("project A: expected 1, got %v", out["count"])
	}

	b := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("proj-B"),
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)
	out2 := b.MCP("repos_list", nil)
	if out2["count"].(float64) != 0 {
		t.Errorf("project B should see 0 repos, got %v", out2["count"])
	}
}

func TestSidecar_GlobalScope_RequiresProjectIDPerCall(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	) // no project_id = global
	_, err := sc.MCPRaw("tools/call", map[string]any{
		"name":      "repos_list",
		"arguments": map[string]any{},
	})
	if err == nil {
		t.Fatal("expected project_id error when scope=global")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}

	out := sc.MCP("repos_list", map[string]any{"_project_id": "proj-X"})
	if out["count"].(float64) != 0 {
		t.Errorf("fresh project should be empty, got %v", out["count"])
	}
}

// keep strconv referenced — used by future asserts on returned ids.
var _ = strconv.Itoa

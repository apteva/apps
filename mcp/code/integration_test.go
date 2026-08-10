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
	"net/http"
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
	paths := grep["paths"].([]any)
	if len(paths) != 1 || paths[0].(string) != "app/page.tsx" {
		t.Errorf("grep paths=%v, want app/page.tsx", paths)
	}
	grepContent := sc.MCP("code_grep", map[string]any{
		"slug":        slug,
		"pattern":     "integration test",
		"output_mode": "content",
		"limit":       5,
	})
	matches := grepContent["matches"].([]any)
	if len(matches) != 1 {
		t.Errorf("grep content matches=%d, want 1", len(matches))
	}

	// 6b. Outline and excerpt avoid whole-file reads for orientation.
	outline := sc.MCP("code_file_outline", map[string]any{
		"slug": slug,
		"path": "app/page.tsx",
	})
	if outline["count"].(float64) == 0 {
		t.Errorf("outline unexpectedly empty: %+v", outline)
	}
	excerpt := sc.MCP("code_read_excerpt", map[string]any{
		"slug":   slug,
		"path":   "app/page.tsx",
		"around": 5,
		"before": 1,
		"after":  1,
	})
	if !strings.Contains(excerpt["content"].(string), "integration test") {
		t.Errorf("excerpt missing edited content: %q", excerpt["content"])
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

	// 8b. Apply a unified diff with dry-run preview first.
	patch := `--- a/app/page.tsx
+++ b/app/page.tsx
@@ -1,4 +1,4 @@
 export default function Home() {
   return (
-    <section style={{ font-family-stub: 'system-ui, sans-serif', padding: '4rem' }}>
+    <section style={{ font-family-stub: 'system-ui', padding: '4rem' }}>
       <h1>Hello from the integration test.</h1>
`
	dry := sc.MCP("code_apply_patch", map[string]any{
		"slug":    slug,
		"patch":   patch,
		"dry_run": true,
	})
	if dry["applied"].(bool) {
		t.Errorf("dry-run patch should not be applied: %+v", dry)
	}
	applied := sc.MCP("code_apply_patch", map[string]any{
		"slug":  slug,
		"patch": patch,
	})
	if !applied["applied"].(bool) {
		t.Errorf("patch not applied: %+v", applied)
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

func TestSidecar_ChatComponentDataEndpoints(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("component-project"),
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)
	sc.MCP("repos_create", map[string]any{"name": "Component Repo", "framework": "go"})
	sc.MCP("code_write_file", map[string]any{
		"slug":    "component-repo",
		"path":    "main.go",
		"content": "package main\n\nfunc main() {}\n",
	})
	sc.MCP("repos_set_deploy_hints", map[string]any{
		"slug":     "component-repo",
		"env_json": `{"SECRET":"must-not-leak"}`,
	})
	created := sc.MCP("issues_create", map[string]any{
		"slug":  "component-repo",
		"title": "Card issue",
		"body":  "Current issue body",
	})
	issue := created["issue"].(map[string]any)

	var summary map[string]any
	resp := sc.GET("/api/repos/component-repo/summary", &summary)
	if resp.Status != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", resp.Status, resp.Body)
	}
	repository := summary["repository"].(map[string]any)
	if repository["slug"] != "component-repo" || repository["file_count"].(float64) < 1 {
		t.Fatalf("unexpected repository summary: %+v", repository)
	}
	if _, leaked := repository["env_json"]; leaked {
		t.Fatalf("repository card leaked env_json: %+v", repository)
	}

	var excerpt map[string]any
	resp = sc.GET("/api/repos/component-repo/files/main.go?annotated=1&offset=2&limit=1", &excerpt)
	if resp.Status != http.StatusOK {
		t.Fatalf("excerpt status=%d body=%s", resp.Status, resp.Body)
	}
	if excerpt["start_line"].(float64) != 2 || excerpt["end_line"].(float64) != 2 {
		t.Fatalf("excerpt was not bounded to one line: %+v", excerpt)
	}
	if excerpt["sha256"] == "" || excerpt["total_lines"].(float64) != 3 {
		t.Fatalf("excerpt metadata incomplete: %+v", excerpt)
	}

	var issueSummary map[string]any
	issueNumber := int(issue["number"].(float64))
	resp = sc.GET("/api/repos/component-repo/issues/"+strconv.Itoa(issueNumber)+"?summary=1", &issueSummary)
	if resp.Status != http.StatusOK {
		t.Fatalf("issue summary status=%d body=%s", resp.Status, resp.Body)
	}
	if _, included := issueSummary["comments"]; included {
		t.Fatalf("summary response unexpectedly included comments: %+v", issueSummary)
	}
	gotIssue := issueSummary["issue"].(map[string]any)
	if gotIssue["title"] != "Card issue" || gotIssue["repo_slug"] != "component-repo" {
		t.Fatalf("unexpected issue summary: %+v", gotIssue)
	}
}

func TestSidecar_ChatComponentEndpointsAreProjectScoped(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".",
		tk.WithEnv("CODE_REPOS_DIR", t.TempDir()),
	)
	sc.MCP("repos_create", map[string]any{
		"_project_id": "project-a",
		"name":        "Private Repo",
	})

	var own map[string]any
	resp := sc.GET("/api/repos/private-repo/summary?project_id=project-a", &own)
	if resp.Status != http.StatusOK {
		t.Fatalf("own-project summary status=%d body=%s", resp.Status, resp.Body)
	}
	resp = sc.GET("/api/repos/private-repo/summary?project_id=project-b", nil)
	if resp.Status != http.StatusNotFound {
		t.Fatalf("cross-project summary status=%d, want 404; body=%s", resp.Status, resp.Body)
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

//go:build integration

package main

// Tier 2 coverage for the Code 0.16 merge engine through the real sidecar:
// repos_branch, repos_merge with a conflict resolved over MCP, the REST merge
// and blame routes, and the auto-stash on switch.
//
// Run with:  go test -tags integration -run TestSidecar_NativeMerge ./...

import (
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_NativeMerge_BranchConflictResolveContinue(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("native-merge"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	sc.MCP("repos_create", map[string]any{"name": "Merge Demo", "framework": "blank"})
	slug := "merge-demo"
	write := func(path, content string) {
		sc.MCP("code_write_file", map[string]any{"slug": slug, "path": path, "content": content})
	}
	write("app.txt", "title=Demo\ncolor=blue\nsize=10\n")
	sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "Base"})

	sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "create", "name": "feature"})
	// Unsaved work on main travels with main.
	write("draft.txt", "main draft\n")
	sw := sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "switch", "name": "feature"})
	if sw["branch"] != "feature" || sw["dirty"] != false || sw["switch"].(map[string]any)["stashed_revision"] == "" {
		t.Fatalf("switch to feature=%v", sw)
	}
	write("app.txt", "title=Demo\ncolor=green\nsize=10\n")
	write("feature.txt", "feature\n")
	sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "Green"})

	back := sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "switch", "name": "main"})
	if back["switch"].(map[string]any)["stash_restored"] != true || back["dirty"] != true {
		t.Fatalf("switch back=%v", back)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "draft.txt"}); !strings.Contains(read["content"].(string), "main draft") {
		t.Fatalf("draft not restored: %v", read)
	}
	sc.MCP("code_delete_file", map[string]any{"slug": slug, "path": "draft.txt"})
	write("app.txt", "title=Demo\ncolor=red\nsize=12\n")
	sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "Red and bigger"})

	cmp := sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "compare", "name": "feature"})
	if cmp["ahead"].(float64) != 1 || cmp["behind"].(float64) != 1 || cmp["fast_forward_possible"] != false {
		t.Fatalf("compare=%v", cmp)
	}

	started := sc.MCP("repos_merge", map[string]any{"slug": slug, "action": "start", "source": "feature"})
	if started["status"] != "conflicts" || started["unresolved"].(float64) != 1 {
		t.Fatalf("start=%v", started)
	}
	conflict := started["conflicts"].([]any)[0].(map[string]any)
	if conflict["path"] != "app.txt" || !strings.Contains(conflict["ours"].(string), "color=red") || !strings.Contains(conflict["theirs"].(string), "color=green") {
		t.Fatalf("conflict=%v", conflict)
	}
	// Checkpoints are refused while the merge is open.
	if err := nativeMCPError(sc, "repos_checkpoint", map[string]any{"slug": slug, "message": "x"}); err == nil || !strings.Contains(err.Error(), "merge is in progress") {
		t.Fatalf("checkpoint during merge: %v", err)
	}
	var restStatus map[string]any
	if resp := sc.GET("/api/repos/"+slug+"/version/merge", &restStatus); resp.Status != 200 || restStatus["status"] != "in_progress" {
		t.Fatalf("REST merge status=%d %v", resp.Status, restStatus)
	}
	if err := nativeMCPError(sc, "repos_merge", map[string]any{"slug": slug, "action": "resolve", "path": "app.txt"}); err == nil {
		t.Fatal("resolve without choice accepted")
	}
	sc.MCP("repos_merge", map[string]any{"slug": slug, "action": "resolve", "path": "app.txt", "content": "title=Demo\ncolor=green\nsize=12\n"})
	done := sc.MCP("repos_merge", map[string]any{"slug": slug, "action": "continue"})
	if done["status"] != "merged" {
		t.Fatalf("continue=%v", done)
	}
	rev := done["revision"].(map[string]any)
	if parents := rev["parents"].([]any); len(parents) != 2 {
		t.Fatalf("merge parents=%v", parents)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "feature.txt"}); !strings.Contains(read["content"].(string), "feature") {
		t.Fatalf("feature file not merged: %v", read)
	}

	// Blame over REST: size line from main, color line traced through the merge to feature's revision.
	var blame map[string]any
	if resp := sc.GET("/api/repos/"+slug+"/version/blame?path=app.txt", &blame); resp.Status != 200 {
		t.Fatalf("REST blame status=%d %v", resp.Status, blame)
	}
	lines := blame["lines"].([]any)
	if len(lines) != 3 || lines[2].(map[string]any)["message"] != "Red and bigger" || lines[1].(map[string]any)["message"] != "Green" {
		t.Fatalf("blame=%v", lines)
	}

	diff := sc.MCP("repos_diff", map[string]any{"slug": slug, "from_revision": "feature", "to_revision": "main", "lines": true})
	files := diff["files"].([]any)
	if len(files) != 1 || !strings.Contains(files[0].(map[string]any)["patch"].(string), "+size=12") {
		t.Fatalf("diff=%v", diff)
	}
	hist := sc.MCP("repos_history", map[string]any{"slug": slug, "path": "feature.txt"})
	if hist["count"].(float64) != 1 || hist["revisions"].([]any)[0].(map[string]any)["message"] != "Green" {
		t.Fatalf("path history=%v", hist)
	}
	del := sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "delete", "name": "feature"})
	if del["deleted"] != "feature" {
		t.Fatalf("delete=%v", del)
	}
	list := sc.MCP("repos_branch", map[string]any{"slug": slug, "action": "list"})
	if list["count"].(float64) != 1 {
		t.Fatalf("branches after delete=%v", list)
	}
}

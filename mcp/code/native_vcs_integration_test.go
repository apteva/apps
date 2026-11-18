//go:build integration

package main

// Tier 2 coverage for native storage format 2 through the real sidecar
// binary: MCP + REST for expected_head, repos_undo and the moves endpoint,
// and an in-place upgrade of a repository written in the 0.14 layout.
//
// Run with:  go test -tags integration -run TestSidecar_NativeV2 ./...

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func nativeMCPError(sc *tk.Sidecar, tool string, args map[string]any) error {
	_, err := sc.MCPRaw("tools/call", map[string]any{"name": tool, "arguments": args})
	return err
}

func revisionID(t *testing.T, result map[string]any) string {
	t.Helper()
	rev, ok := result["revision"].(map[string]any)
	if !ok {
		t.Fatalf("no revision in %v", result)
	}
	return rev["id"].(string)
}

func TestSidecar_NativeV2_ExpectedHeadUndoAndMoves(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("native-v2"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	sc.MCP("repos_create", map[string]any{"name": "Undo Demo", "framework": "blank"})
	slug := "undo-demo"

	initial := sc.MCP("repos_version_status", map[string]any{"slug": slug})
	base := initial["head"].(string)

	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "notes.txt", "content": "first\n"})
	first := revisionID(t, sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "First", "expected_head": base}))

	// A second agent still holding the old head is refused and nothing moves.
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "notes.txt", "content": "second\n"})
	err := nativeMCPError(sc, "repos_checkpoint", map[string]any{"slug": slug, "message": "Stale", "expected_head": base})
	if err == nil || !strings.Contains(err.Error(), "native ref moved") {
		t.Fatalf("stale expected_head: %v", err)
	}
	if status := sc.MCP("repos_version_status", map[string]any{"slug": slug}); status["head"] != first || status["dirty"] != true {
		t.Fatalf("status after refused checkpoint=%v", status)
	}
	second := revisionID(t, sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "Second", "expected_head": first}))

	// REST: the checkpoint route maps a stale expected_head to 409.
	var conflict map[string]any
	if resp := sc.POST("/api/repos/"+slug+"/version/checkpoint", map[string]any{"message": "x", "expected_head": base}, &conflict); resp.Status != 409 {
		t.Fatalf("REST stale checkpoint status=%d body=%v", resp.Status, conflict)
	}

	listed := sc.MCP("repos_undo", map[string]any{"slug": slug})
	moves := listed["moves"].([]any)
	if listed["branch"] != "main" || len(moves) != 3 {
		t.Fatalf("moves=%v", listed)
	}
	newest := moves[0].(map[string]any)
	if newest["to"] != second || newest["from"] != first || newest["action"] != "checkpoint" || newest["message"] != "Second" {
		t.Fatalf("newest move=%v", newest)
	}

	// Undo with unsaved edits is refused and leaves them alone.
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "notes.txt", "content": "unsaved\n"})
	if err := nativeMCPError(sc, "repos_undo", map[string]any{"slug": slug, "revision": first}); err == nil {
		t.Fatal("undo over unsaved edits accepted")
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "notes.txt"}); !strings.Contains(read["content"].(string), "unsaved") {
		t.Fatalf("refused undo changed the file: %v", read)
	}
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "notes.txt", "content": "second\n"})

	undone := sc.MCP("repos_undo", map[string]any{"slug": slug, "revision": first, "actor": "agent:test"})
	if undone["moved"] != true {
		t.Fatalf("undo=%v", undone)
	}
	if status := undone["status"].(map[string]any); status["head"] != first || status["dirty"] != false {
		t.Fatalf("status after undo=%v", status)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "notes.txt"}); !strings.Contains(read["content"].(string), "first") {
		t.Fatalf("undo did not restore the file: %v", read)
	}
	history := sc.MCP("repos_history", map[string]any{"slug": slug})
	if history["count"] != float64(2) {
		t.Fatalf("history after undo=%v", history)
	}

	// REST moves endpoint shows the undo, and the undo itself can be undone.
	var rest struct {
		Branch string          `json:"branch"`
		Moves  []nativeRefMove `json:"moves"`
	}
	if resp := sc.GET("/api/repos/"+slug+"/version/moves?limit=2", &rest); resp.Status != 200 {
		t.Fatalf("moves status=%d", resp.Status)
	}
	// The recorded actor is the authenticated caller; a client-supplied
	// "actor" argument cannot spoof it.
	if len(rest.Moves) != 2 || rest.Moves[0].Action != "undo" || rest.Moves[0].To != first || rest.Moves[0].Actor == "agent:test" || rest.Moves[0].Actor == "" {
		t.Fatalf("REST moves=%+v", rest)
	}
	redo := sc.MCP("repos_undo", map[string]any{"slug": slug, "revision": second})
	if redo["moved"] != true {
		t.Fatalf("redo=%v", redo)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "notes.txt"}); !strings.Contains(read["content"].(string), "second") {
		t.Fatalf("redo did not restore the file: %v", read)
	}

	// Diff between two format-2 revisions goes through folder trees.
	diff := sc.MCP("repos_diff", map[string]any{"slug": slug, "from_revision": first, "to_revision": second})
	if diff["count"] != float64(1) {
		t.Fatalf("diff=%v", diff)
	}
}

// downgradeToFormat1 rewrites a format-2 native directory into exactly what
// Code 0.14 left on disk: no format marker, logs, folder trees or index, and
// revisions without a root.
func downgradeToFormat1(t *testing.T, dir string) {
	t.Helper()
	for _, name := range []string{"format", "logs", "trees", "index.json", "gc-last"} {
		if err := os.RemoveAll(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := os.ReadDir(filepath.Join(dir, "revisions"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "tree-") {
			continue
		}
		path := filepath.Join(dir, "revisions", e.Name())
		body, _ := os.ReadFile(path)
		var rev map[string]any
		if err := json.Unmarshal(body, &rev); err != nil {
			t.Fatal(err)
		}
		delete(rev, "root")
		body, _ = json.Marshal(rev)
		if err := os.WriteFile(path, body, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSidecar_NativeV2_UpgradesFormat1RepositoryInPlace(t *testing.T) {
	state := t.TempDir()
	reposDir := filepath.Join(state, "repos")
	dbPath := filepath.Join(state, "app.db")
	spawn := func() *tk.Sidecar {
		return tk.SpawnSidecar(t, ".", tk.WithProjectID("native-upgrade"), tk.WithEnv("CODE_REPOS_DIR", reposDir), tk.WithEnv("DB_PATH", dbPath))
	}

	sc := spawn()
	created := sc.MCP("repos_create", map[string]any{"name": "Legacy", "framework": "blank"})
	repoID := int64(created["repository"].(map[string]any)["id"].(float64))
	slug := "legacy"
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "src/app.txt", "content": "v1\n"})
	one := revisionID(t, sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "One"}))
	sc.MCP("repos_tag_create", map[string]any{"slug": slug, "name": "release-1", "revision": one})
	sc.MCP("code_write_file", map[string]any{"slug": slug, "path": "src/app.txt", "content": "v2\n"})
	two := revisionID(t, sc.MCP("repos_checkpoint", map[string]any{"slug": slug, "message": "Two"}))
	before := sc.MCP("repos_history", map[string]any{"slug": slug})
	sc.Stop()

	nativeDir := filepath.Join(state, "native-vcs", jsonInt(repoID))
	if _, err := os.Stat(nativeDir); err != nil {
		t.Fatalf("native dir not where expected: %v", err)
	}
	downgradeToFormat1(t, nativeDir)

	sc = spawn()
	status := sc.MCP("repos_version_status", map[string]any{"slug": slug})
	if status["head"] != two || status["dirty"] != false || status["revision_count"] != float64(3) {
		t.Fatalf("status after upgrade=%v", status)
	}
	after := sc.MCP("repos_history", map[string]any{"slug": slug})
	beforeIDs, afterIDs := []string{}, []string{}
	for _, r := range before["revisions"].([]any) {
		beforeIDs = append(beforeIDs, r.(map[string]any)["id"].(string))
	}
	for _, r := range after["revisions"].([]any) {
		afterIDs = append(afterIDs, r.(map[string]any)["id"].(string))
	}
	if strings.Join(beforeIDs, ",") != strings.Join(afterIDs, ",") {
		t.Fatalf("revision IDs changed by upgrade:\nbefore %v\nafter  %v", beforeIDs, afterIDs)
	}
	if tags := sc.MCP("repos_tags_list", map[string]any{"slug": slug}); tags["tags"].(map[string]any)["release-1"] != one {
		t.Fatalf("tags after upgrade=%v", tags)
	}
	if body, err := os.ReadFile(filepath.Join(nativeDir, "format")); err != nil || strings.TrimSpace(string(body)) != "2" {
		t.Fatalf("format marker=%q err=%v", body, err)
	}
	moves := sc.MCP("repos_undo", map[string]any{"slug": slug})
	if moves["count"] != float64(1) || moves["moves"].([]any)[0].(map[string]any)["action"] != "migrate" {
		t.Fatalf("seeded moves=%v", moves)
	}
	if exported := sc.MCP("repos_export_ref", map[string]any{"slug": slug, "revision": one}); exported["size"].(float64) <= 0 {
		t.Fatalf("export of a format-1 revision=%v", exported)
	}

	// Undo across the upgrade: back to a pre-upgrade revision that is in
	// first-parent history but not in the (freshly seeded) log is refused...
	if err := nativeMCPError(sc, "repos_undo", map[string]any{"slug": slug, "revision": one}); err == nil {
		t.Fatal("undo to a revision outside the log accepted")
	}
	// ...restore still reaches it, and the restore is then undoable.
	restored := revisionID(t, sc.MCP("repos_restore", map[string]any{"slug": slug, "revision": one}))
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "src/app.txt"}); !strings.Contains(read["content"].(string), "v1") {
		t.Fatalf("restore after upgrade: %v", read)
	}
	if undo := sc.MCP("repos_undo", map[string]any{"slug": slug, "revision": two}); undo["moved"] != true {
		t.Fatalf("undo of restore=%v", undo)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": slug, "path": "src/app.txt"}); !strings.Contains(read["content"].(string), "v2") {
		t.Fatalf("undo of restore did not restore v2: %v", read)
	}
	_ = restored
}

func jsonInt(v int64) string {
	b, _ := json.Marshal(v)
	return string(b)
}

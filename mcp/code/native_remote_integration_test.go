//go:build integration

package main

// Tier 2 coverage for Code 0.18 native remotes through the real sidecar:
// clone from a repository in the same project, fetch + merge (pull), and a
// push over the HTTPS-style transport to the sidecar's own REST sync API
// (loopback http), with the token passed per call and never stored.
//
// Run with:  go test -tags integration -run TestSidecar_NativeRemote ./...

import (
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_NativeRemote_CloneFetchPushOverHTTP(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("native-remote"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()))
	sc.MCP("repos_create", map[string]any{"name": "Source", "framework": "blank"})
	sc.MCP("code_write_file", map[string]any{"slug": "source", "path": "app.txt", "content": "one\ntwo\nthree\n"})
	sc.MCP("repos_checkpoint", map[string]any{"slug": "source", "message": "Source base"})

	cloned := sc.MCP("repos_remote", map[string]any{"action": "clone", "from_slug": "source", "name": "Copy"})
	if cloned["repository"].(map[string]any)["slug"] != "copy" || cloned["status"].(map[string]any)["dirty"] != false {
		t.Fatalf("clone=%v", cloned)
	}
	if read := sc.MCP("code_read_file", map[string]any{"slug": "copy", "path": "app.txt"}); !strings.Contains(read["content"].(string), "three") {
		t.Fatalf("cloned file=%v", read)
	}
	srcHist := sc.MCP("repos_history", map[string]any{"slug": "source"})
	copyHist := sc.MCP("repos_history", map[string]any{"slug": "copy"})
	if srcHist["revisions"].([]any)[0].(map[string]any)["id"] != copyHist["revisions"].([]any)[0].(map[string]any)["id"] {
		t.Fatal("clone does not share revision IDs with its source")
	}

	// Source moves; copy fetches and merges origin/main while keeping its own edit.
	sc.MCP("code_write_file", map[string]any{"slug": "source", "path": "app.txt", "content": "ONE\ntwo\nthree\n"})
	sc.MCP("repos_checkpoint", map[string]any{"slug": "source", "message": "Source line 1"})
	sc.MCP("code_write_file", map[string]any{"slug": "copy", "path": "app.txt", "content": "one\ntwo\nTHREE\n"})
	sc.MCP("repos_checkpoint", map[string]any{"slug": "copy", "message": "Copy line 3"})
	fetched := sc.MCP("repos_remote", map[string]any{"slug": "copy", "action": "fetch", "name": "origin"})
	if fetched["revisions_received"].(float64) != 1 {
		t.Fatalf("fetch=%v", fetched)
	}
	merged := sc.MCP("repos_merge", map[string]any{"slug": "copy", "action": "start", "source": "origin/main"})
	if merged["status"] != "merged" {
		t.Fatalf("pull merge=%v", merged)
	}

	// Push over HTTP to the sidecar's own REST API, as another install would.
	sc.MCP("repos_remote", map[string]any{"slug": "copy", "action": "add", "name": "web", "url": sc.URL() + "/api/repos/source"})
	if err := nativeMCPError(sc, "repos_remote", map[string]any{"slug": "copy", "action": "push", "name": "web", "remote_branch": "from-copy", "token": "wrong"}); err == nil {
		t.Fatal("push with a wrong token accepted")
	}
	pushed := sc.MCP("repos_remote", map[string]any{"slug": "copy", "action": "push", "name": "web", "remote_branch": "from-copy", "token": sc.Token()})
	if pushed["status"] != "pushed" {
		t.Fatalf("push=%v", pushed)
	}
	var blame map[string]any
	if resp := sc.GET("/api/repos/source/version/blame?ref=from-copy&path=app.txt", &blame); resp.Status != 200 {
		t.Fatalf("blame on pushed branch status=%d %v", resp.Status, blame)
	}
	lines := blame["lines"].([]any)
	if lines[0].(map[string]any)["text"] != "ONE" || lines[2].(map[string]any)["text"] != "THREE" || lines[2].(map[string]any)["message"] != "Copy line 3" {
		t.Fatalf("pushed content=%v", lines)
	}
	// Fast-forward source main over HTTP too; it is checked out and clean.
	sc.MCP("repos_remote", map[string]any{"slug": "copy", "action": "push", "name": "web", "remote_branch": "main", "token": sc.Token()})
	if read := sc.MCP("code_read_file", map[string]any{"slug": "source", "path": "app.txt"}); !strings.Contains(read["content"].(string), "THREE") {
		t.Fatalf("source working tree not updated: %v", read)
	}

	var remotes map[string]any
	sc.GET("/api/repos/copy/version/remotes", &remotes)
	raw, _ := json.Marshal(remotes)
	if remotes["count"].(float64) != 2 || strings.Contains(string(raw), sc.Token()) || strings.Contains(string(raw), "wrong") {
		t.Fatalf("remotes=%s", raw)
	}
}

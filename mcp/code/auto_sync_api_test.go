package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestAutoSyncHTTPAndAgentControls(t *testing.T) {
	f := newSyncFixture(t)
	previous := globalCtx
	globalCtx = f.ctx
	t.Cleanup(func() { globalCtx = previous })
	a := &App{store: f.store, git: f.service, locks: f.service.locks, syncer: f.supervisor}
	t.Setenv("APTEVA_PROJECT_ID", "")
	call := func(method, path, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, path, bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		a.handleRepoItem(w, r)
		return w
	}
	w := call("GET", "/api/repos/demo/git/sync?project_id=p1", "")
	if w.Code != 200 {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	var state AutoSyncState
	if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil || !state.Enabled {
		t.Fatalf("bad state: %+v %v", state, err)
	}
	if w = call("GET", "/api/repos/demo/git/sync?project_id=another-project", ""); w.Code != 404 {
		t.Fatalf("cross-project state exposed: %d", w.Code)
	}
	if w = call("PATCH", "/api/repos/demo/git/sync?project_id=p1", "{}"); w.Code != 400 {
		t.Fatalf("missing enabled accepted: %d", w.Code)
	}
	if w = call("PATCH", "/api/repos/demo/git/sync?project_id=p1", `{"enabled":false}`); w.Code != 200 {
		t.Fatalf("pause: %s", w.Body)
	}
	if _, err := a.toolSyncNow(f.ctx, map[string]any{"slug": "demo", "_project_id": "p1"}); err == nil {
		t.Fatal("agent synced paused repository")
	}
	if _, err := a.toolSyncConfigure(f.ctx, map[string]any{"slug": "demo", "_project_id": "p1", "enabled": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.supervisor.configure(f.ctx, f.repo, true, "unexpected-branch"); err == nil {
		t.Fatal("enabled a different branch than confirmed")
	}

	f.write(t, "hello.txt", "from API")
	w = call("POST", "/api/repos/demo/git/sync/now?project_id=p1", "")
	if w.Code != 200 {
		t.Fatalf("sync now: %s", w.Body)
	}
	if got := testGit(t, f.remote, "show", "main:hello.txt"); got != "from API" {
		t.Fatalf("API sync failed: %q", got)
	}
}
func TestAutoSyncDeletedRemoteBranchRequiresAttention(t *testing.T) {
	f := newSyncFixture(t)
	testGit(t, f.remote, "update-ref", "-d", "refs/heads/main")
	f.write(t, "hello.txt", "retained locally")
	s := f.tick(t, time.Now(), true)
	if s.Status != "needs_attention" {
		t.Fatalf("deleted branch silently recreated: %+v", s)
	}
	if got := testGit(t, f.work, "show", "HEAD:hello.txt"); got != "retained locally" {
		t.Fatal("checkpoint lost")
	}
	if got := testGit(t, f.remote, "for-each-ref", "--format=%(refname)", "refs/heads/main"); got != "" {
		t.Fatal("deleted remote branch recreated")
	}
}

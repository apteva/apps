package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/3d-studio/engine"
)

func testApp(t *testing.T) *App {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	a := &App{}
	m := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&m, db, sdk.Config{}, nil, nil).WithProject("alpha")
	if err = a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return a
}
func invoke(t *testing.T, a *App, name string, args any) map[string]json.RawMessage {
	t.Helper()
	r, err := a.call(context.Background(), "alpha", name, []byte(encoded(args)))
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	var out map[string]json.RawMessage
	if err = json.Unmarshal([]byte(encoded(r)), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
func createBox(t *testing.T, a *App) Revision {
	t.Helper()
	out := invoke(t, a, "assets_create", map[string]any{"name": "Test box", "template": "box"})
	var r Revision
	json.Unmarshal(out["revision"], &r)
	return r
}
func savedTop(t *testing.T, a *App, r Revision) SavedSelection {
	t.Helper()
	out := invoke(t, a, "mesh_select", map[string]any{"asset_id": r.AssetID, "revision_id": r.ID, "query": map[string]any{"node_id": "body", "kind": "face", "normal": []float64{0, 1, 0}}})
	var s SavedSelection
	json.Unmarshal([]byte(encoded(out)), &s)
	return s
}
func editArgs(r Revision, s SavedSelection, mode string) map[string]any {
	return map[string]any{"asset_id": r.AssetID, "expected_revision_id": r.ID, "request_key": "roof-edit", "mode": mode, "selections": map[string]string{"roof": s.ID}, "commands": []any{map[string]any{"op": "extrude", "node_id": "body", "selection": "roof", "direction": []float64{0, 1, 0}, "distance": .2, "result_selection": "cap"}}}
}
func TestManifestMatchesToolsAndFiles(t *testing.T) {
	a := testApp(t)
	m := a.Manifest()
	if len(m.Provides.MCPTools) != len(a.MCPTools()) {
		t.Fatal("manifest tools out of sync")
	}
	names := map[string]bool{}
	for _, tool := range a.MCPTools() {
		names[tool.Name] = true
	}
	for _, tool := range m.Provides.MCPTools {
		if !names[tool.Name] {
			t.Fatalf("missing %s", tool.Name)
		}
	}
	for _, path := range []string{"ui/icon.svg", "skills/how-to-use-3d-studio.md"} {
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
}
func TestPreviewCommitRetryRestoreAndIsolation(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	s := savedTop(t, a, r)
	out := invoke(t, a, "mesh_edit", editArgs(r, s, "preview"))
	var candidate string
	json.Unmarshal(out["id"], &candidate)
	asset, _ := a.store.asset("alpha", r.AssetID)
	if asset.Head != r.ID {
		t.Fatal("preview advanced head")
	}
	if _, err := a.store.candidate("beta", candidate); !errors.Is(err, errNotFound) {
		t.Fatal("candidate leaked across projects")
	}
	if _, err := a.store.selection("beta", s.ID); !errors.Is(err, errNotFound) {
		t.Fatal("selection leaked across projects")
	}
	render := invoke(t, a, "assets_render", map[string]any{"asset_id": r.AssetID, "candidate_id": candidate, "view": "side"})
	var artifact Artifact
	json.Unmarshal(render["artifact"], &artifact)
	if artifact.CandidateID != candidate {
		t.Fatal("candidate provenance missing")
	}
	if _, _, err := a.store.artifactContent("beta", artifact.ID); !errors.Is(err, errNotFound) {
		t.Fatal("artifact leaked")
	}
	commit := map[string]any{"asset_id": r.AssetID, "candidate_id": candidate, "request_key": "commit-preview"}
	out = invoke(t, a, "mesh_edit_commit", commit)
	var revised Revision
	json.Unmarshal(out["revision"], &revised)
	if revised.ParentID != r.ID || revised.ID == r.ID {
		t.Fatal("revision chain broken")
	}
	again := invoke(t, a, "mesh_edit_commit", commit)
	var retry Revision
	json.Unmarshal(again["revision"], &retry)
	if retry.ID != revised.ID {
		t.Fatal("retry created another revision")
	}
	stale := editArgs(revised, s, "commit")
	if _, err := a.call(context.Background(), "alpha", "mesh_edit", []byte(encoded(stale))); err == nil {
		t.Fatal("stale selection accepted")
	}
	_, err := a.call(context.Background(), "alpha", "mesh_edit", []byte(encoded(editArgs(r, s, "commit"))))
	if !errors.Is(err, errConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
	restored := invoke(t, a, "revisions_restore", map[string]any{"asset_id": r.AssetID, "revision_id": r.ID, "expected_revision_id": revised.ID, "request_key": "restore"})
	var back Revision
	json.Unmarshal(restored["revision"], &back)
	if back.SourceHash != r.SourceHash || back.ParentID != revised.ID {
		t.Fatal("restore did not create correct child")
	}
}
func TestAtomicEditFailureAndRequestKeyReuse(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	s := savedTop(t, a, r)
	args := editArgs(r, s, "commit")
	out := invoke(t, a, "mesh_edit", args)
	var saved Revision
	json.Unmarshal(out["revision"], &saved)
	retry := invoke(t, a, "mesh_edit", args)
	var again Revision
	json.Unmarshal(retry["revision"], &again)
	if again.ID != saved.ID {
		t.Fatal("edit retry duplicated")
	}
	args["note"] = "different"
	if _, err := a.call(context.Background(), "alpha", "mesh_edit", []byte(encoded(args))); err == nil {
		t.Fatal("key reuse allowed")
	}
	bad := map[string]any{"asset_id": r.AssetID, "expected_revision_id": saved.ID, "request_key": "bad", "commands": []any{map[string]any{"op": "node.color", "node_id": "body", "color": []float64{1, 0, 0}}, map[string]any{"op": "vertices.patch", "node_id": "body", "positions": []any{map[string]any{"vertex_id": "missing", "position": []float64{0, 0, 0}}}}}}
	if _, err := a.call(context.Background(), "alpha", "mesh_edit", []byte(encoded(bad))); err == nil {
		t.Fatal("invalid edit succeeded")
	}
	asset, _ := a.store.asset("alpha", r.AssetID)
	if asset.Head != saved.ID {
		t.Fatal("failed batch advanced head")
	}
}
func TestConcurrentEditsOnlyOneAdvances(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, key := range []string{"one", "two"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, err := a.call(context.Background(), "alpha", "mesh_edit", []byte(encoded(map[string]any{"asset_id": r.AssetID, "expected_revision_id": r.ID, "request_key": key, "commands": []any{map[string]any{"op": "transform", "node_id": "body", "translation": []float64{.1, 0, 0}}}})))
			errs <- err
		}(key)
	}
	wg.Wait()
	close(errs)
	success, conflict := 0, 0
	for err := range errs {
		if err == nil {
			success++
		} else if errors.Is(err, errConflict) {
			conflict++
		} else {
			t.Fatal(err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
}
func TestCandidateExpiryAndHTTPBoundary(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	s := savedTop(t, a, r)
	out := invoke(t, a, "mesh_edit", editArgs(r, s, "preview"))
	var id string
	json.Unmarshal(out["id"], &id)
	a.store.db.Exec(`UPDATE studio_candidates SET expires_at=? WHERE id=?`, time.Now().Unix()-1, id)
	if _, err := a.store.candidate("alpha", id); !errors.Is(err, errNotFound) {
		t.Fatal("expired preview accessible")
	}
	cases := []struct {
		method, path, body, project string
		status                      int
	}{{"GET", "/api/tools/assets_list", "", "", 405}, {"POST", "/api/tools/assets_list", "{}", "beta", 404}, {"POST", "/api/tools/assets_list", "{} {}", "", 400}, {"POST", "/api/tools/assets_list", "{\"unknown\":true}", "", 400}, {"POST", "/api/tools/assets_list", "{}", "", 200}}
	for _, c := range cases {
		req := httptest.NewRequest(c.method, c.path, bytes.NewBufferString(c.body))
		if c.project != "" {
			req.Header.Set("X-Apteva-Project-ID", c.project)
		}
		w := httptest.NewRecorder()
		a.handleTool(w, req)
		if w.Code != c.status {
			t.Fatalf("%+v: %d %s", c, w.Code, w.Body.String())
		}
	}
	artifact := invoke(t, a, "assets_export", map[string]any{"asset_id": r.AssetID, "format": "glb"})
	var f Artifact
	json.Unmarshal(artifact["artifact"], &f)
	req := httptest.NewRequest(http.MethodGet, f.URL, nil)
	w := httptest.NewRecorder()
	a.handleArtifact(w, req)
	if w.Code != 200 || w.Header().Get("Content-Type") != "model/gltf-binary" {
		t.Fatal("artifact download failed")
	}
}
func TestMCPAndHTTPShareEngine(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	var tool sdk.Tool
	for _, candidate := range a.MCPTools() {
		if candidate.Name == "mesh_edit" {
			tool = candidate
		}
	}
	result, err := tool.HandlerCtx(context.Background(), a.ctx, map[string]any{"asset_id": r.AssetID, "expected_revision_id": r.ID, "request_key": "mcp", "commands": []any{map[string]any{"op": "transform", "node_id": "body", "translation": []float64{1, 0, 0}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Revision Revision `json:"revision"`
	}
	json.Unmarshal([]byte(encoded(result)), &out)
	report, err := engine.Validate(out.Revision.Document)
	if err != nil || report.Bounds[0][0] != .5 {
		t.Fatal("MCP did not edit geometry")
	}
	if _, err = tool.HandlerCtx(context.Background(), a.ctx.WithProject("beta"), map[string]any{}); !errors.Is(err, errNotFound) {
		t.Fatal("MCP project override accepted")
	}
}

func TestMalformedVectorsAndRequiredIDs(t *testing.T) {
	a := testApp(t)
	r := createBox(t, a)
	for _, vector := range []string{`[1,2]`, `[1,2,3,4]`, `[null,2,3]`, `[1,"2",3]`} {
		raw := []byte(`{"asset_id":1,"expected_revision_id":1,"request_key":"bad-vector","commands":[{"op":"transform","node_id":"body","translation":` + vector + `} ]}`)
		if _, err := a.call(context.Background(), "alpha", "mesh_edit", raw); err == nil {
			t.Fatalf("accepted %s", vector)
		}
	}
	for _, tool := range []string{"assets_get", "revisions_restore"} {
		if _, err := a.call(context.Background(), "alpha", tool, []byte(`{"asset_id":0,"revision_id":0}`)); err == nil {
			t.Fatal("zero IDs accepted")
		}
	}
	s := savedTop(t, a, r)
	args := editArgs(r, s, "commit")
	first := invoke(t, a, "mesh_edit", args)
	second := invoke(t, a, "mesh_edit", args)
	if !bytes.Equal(first["selections"], second["selections"]) {
		t.Fatal("retry lost or changed returned selection handles")
	}
}

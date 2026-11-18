package main

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type flakyPlatform struct {
	platformStub
	failRun    bool
	failImport bool
}

func (p *flakyPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	switch {
	case tool == "containers_run" && p.failRun:
		p.calls = append(p.calls, appCall{App: app, Tool: tool, Input: in})
		return errors.New("Cannot connect to the Docker daemon")
	case tool == "containers_volume_import" && p.failImport:
		p.calls = append(p.calls, appCall{App: app, Tool: tool, Input: in})
		return errors.New("import refused")
	}
	return p.platformStub.CallAppResult(app, tool, in, out)
}

func resourceArgs() map[string]any {
	return map[string]any{"name": "demo development", "resource_kind": "repo", "resource_id": "42"}
}

func TestFailedCreateDoesNotBlockRetry(t *testing.T) {
	platform := &flakyPlatform{failRun: true}
	ctx, db := newTestContext(t, platform)
	app := &App{}
	if _, err := app.toolCreateForResource(appCaller("code", 77, "call-1"), ctx, resourceArgs()); err == nil || !strings.Contains(err.Error(), "Docker daemon") {
		t.Fatalf("first create should fail with the runtime error, got %v", err)
	}
	var status, lastError string
	if err := db.QueryRow(`SELECT lifecycle_status, last_error FROM workspaces`).Scan(&status, &lastError); err != nil {
		t.Fatal(err)
	}
	if status != statusDestroyed || !strings.Contains(lastError, "Docker daemon") {
		t.Fatalf("failed create must release its name and keep the error: %s %q", status, lastError)
	}
	platform.failRun = false
	out, err := app.toolCreateForResource(appCaller("code", 77, "call-2"), ctx, resourceArgs())
	if err != nil {
		t.Fatalf("retry after the cause is fixed must succeed: %v", err)
	}
	if w := out.(map[string]any)["workspace"].(*Workspace); w.LifecycleStatus != statusRunning {
		t.Fatalf("retry status %s", w.LifecycleStatus)
	}
	// Agents retrying a plain create by name are covered too.
	platform.failRun = true
	if _, err := app.toolCreate(agentContext("p", 4, "t", "a1"), ctx, map[string]any{"name": "agent box"}); err == nil {
		t.Fatal("expected failure")
	}
	platform.failRun = false
	if _, err := app.toolCreate(agentContext("p", 4, "t", "a2"), ctx, map[string]any{"name": "agent box"}); err != nil {
		t.Fatalf("agent retry: %v", err)
	}
}

func TestImportFailureDestroysContainerAndReleasesName(t *testing.T) {
	platform := &flakyPlatform{failImport: true}
	ctx, db := newTestContext(t, platform)
	app := &App{}
	args := resourceArgs()
	args["source_archive_base64"] = sourceArchiveForTest(t, map[string]string{"a.go": "package a"})
	if _, err := app.toolCreateForResource(appCaller("code", 77, "c1"), ctx, args); err == nil {
		t.Fatal("expected import failure")
	}
	if len(callsTo(platform.calls, "containers_destroy")) != 1 {
		t.Fatal("the started container must be destroyed")
	}
	var status string
	_ = db.QueryRow(`SELECT lifecycle_status FROM workspaces`).Scan(&status)
	if status != statusDestroyed {
		t.Fatalf("status %s", status)
	}
}

func TestCreateForResourceReturnsExistingWorkspace(t *testing.T) {
	platform := &flakyPlatform{}
	ctx, db := newTestContext(t, platform)
	app := &App{}
	first, err := app.toolCreateForResource(appCaller("code", 77, "c1"), ctx, resourceArgs())
	if err != nil {
		t.Fatal(err)
	}
	w := first.(map[string]any)["workspace"].(*Workspace)
	again, err := app.toolCreateForResource(appCaller("code", 77, "c2"), ctx, resourceArgs())
	if err != nil {
		t.Fatal(err)
	}
	if got := again.(map[string]any); got["reused"] != true || got["workspace"].(*Workspace).ID != w.ID {
		t.Fatalf("running workspace must be returned: %+v", got)
	}
	if len(callsTo(platform.calls, "containers_run")) != 1 {
		t.Fatal("no second container may be started")
	}
	// A different app with the same resource ids gets its own workspace check
	// and then hits the name, which is reported by id.
	if _, err := app.toolCreateForResource(appCaller("other", 78, "o1"), ctx, resourceArgs()); err == nil || !strings.Contains(err.Error(), w.ID) {
		t.Fatalf("name clash must name the holder, got %v", err)
	}
	// Suspended: resumed.
	if _, err := app.toolStop(appCaller("code", 77, ""), ctx, map[string]any{"workspace_id": w.ID}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolCreateForResource(appCaller("code", 77, "c3"), ctx, resourceArgs())
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["workspace"].(*Workspace); got.ID != w.ID || got.LifecycleStatus != statusRunning {
		t.Fatalf("suspended workspace must be resumed: %+v", got)
	}
	// Expired: extended and resumed.
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := db.Exec(`UPDATE workspaces SET lifecycle_status='expired', runtime_status='stopped', expires_at=? WHERE id=?`, past, w.ID); err != nil {
		t.Fatal(err)
	}
	out, err = app.toolCreateForResource(appCaller("code", 77, "c4"), ctx, resourceArgs())
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["workspace"].(*Workspace)
	if got.ID != w.ID || got.LifecycleStatus != statusRunning || !parseTime(got.ExpiresAt).After(time.Now()) {
		t.Fatalf("expired workspace must be extended and resumed: %+v", got)
	}
	// Failed after it had run: resumed, never replaced (its volumes may hold work).
	if _, err := db.Exec(`UPDATE workspaces SET lifecycle_status='failed', runtime_status='error' WHERE id=?`, w.ID); err != nil {
		t.Fatal(err)
	}
	out, err = app.toolCreateForResource(appCaller("code", 77, "c5"), ctx, resourceArgs())
	if err != nil {
		t.Fatal(err)
	}
	if got := out.(map[string]any)["workspace"].(*Workspace); got.ID != w.ID || got.LifecycleStatus != statusRunning {
		t.Fatalf("failed-after-ready workspace must be resumed: %+v", got)
	}
	if len(callsTo(platform.calls, "containers_destroy")) != 0 {
		t.Fatal("a workspace that once ran must not be destroyed")
	}
}

// Records left by 0.8.0 (failed, never ready, still holding the name) are
// cleared by the next create from the same caller.
func TestLegacyFailedRecordIsRetiredOnRetry(t *testing.T) {
	platform := &flakyPlatform{failRun: true}
	ctx, db := newTestContext(t, platform)
	app := &App{}
	_, _ = app.toolCreateForResource(appCaller("code", 77, "c1"), ctx, resourceArgs())
	if _, err := db.Exec(`UPDATE workspaces SET lifecycle_status='failed', runtime_status='', destroyed_at=''`); err != nil {
		t.Fatal(err)
	}
	platform.failRun = false
	args := resourceArgs()
	delete(args, "resource_id") // no resource: the name path must handle it
	if _, err := app.toolCreateForResource(appCaller("code", 77, "c2"), ctx, args); err != nil {
		t.Fatalf("legacy failed record must not block the name: %v", err)
	}
	var destroyed int
	_ = db.QueryRow(`SELECT COUNT(*) FROM workspaces WHERE lifecycle_status='destroyed'`).Scan(&destroyed)
	if destroyed != 1 {
		t.Fatalf("legacy record not retired (%d destroyed)", destroyed)
	}
}

func TestReconcileRetiresStuckProvisioning(t *testing.T) {
	platform := &flakyPlatform{}
	ctx, db := newTestContext(t, platform)
	app := &App{}
	old := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	for _, row := range []struct{ id, updated string }{{"wsp_old", old}, {"wsp_new", nowUTC()}} {
		w := &Workspace{ID: row.id, ProjectID: "p", Name: row.id, Profile: "go", Image: "x", LifecycleStatus: statusProvisioning,
			ActivityStatus: activityIdle, CreatedAt: row.updated, UpdatedAt: row.updated, LastActivityAt: row.updated, ExpiresAt: nowUTC(), DeleteAt: nowUTC()}
		if err := insertWorkspace(db, w, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.reconcile(t.Context(), ctx); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]string{"wsp_old": statusDestroyed, "wsp_new": statusProvisioning} {
		var status string
		_ = db.QueryRow(`SELECT lifecycle_status FROM workspaces WHERE id=?`, id).Scan(&status)
		if status != want {
			t.Fatalf("%s = %s, want %s", id, status, want)
		}
	}
}

func TestFailedTestCreateReleasesName(t *testing.T) {
	platform := &flakyPlatform{failRun: true}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	args := func() map[string]any {
		return map[string]any{"name": "ci run", "source_archive_base64": testArchive(t, testEntry{name: "a.ts", body: "a"})}
	}
	if _, err := app.toolTestCreate(appCaller("ci", 50, "t1"), ctx, args()); err == nil {
		t.Fatal("expected failure")
	}
	platform.failRun = false
	if _, err := app.toolTestCreate(appCaller("ci", 50, "t2"), ctx, args()); err != nil {
		t.Fatalf("retry: %v", err)
	}
}

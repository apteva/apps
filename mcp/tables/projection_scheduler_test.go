package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func schedulerFixture(t *testing.T) (*App, *sdk.AppCtx, *projectionDefinition) {
	t.Helper()
	ctx, _, _ := newFileBackedTestCtx(t, "test-proj")
	ctx.SetEmitter(tk.NewEmitRecorder())
	a := &App{}
	t.Cleanup(a.closeProjectionReader)
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}}})
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	p, err := a.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, p
}
func totalWriterChanges(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	var n int64
	if err := ctx.AppDB().QueryRow(`SELECT total_changes()`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestProjectionIdleTicksAndEmptyCleanupDoNotWrite(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	before := totalWriterChanges(t, ctx)
	metrics := a.workerMetrics("test-proj")
	for i := 0; i < 100; i++ {
		runProjectionWorker(t, a, ctx)
	}
	if removed, err := a.cleanupProjectionGenerations(context.Background(), ctx, p); err != nil || removed {
		t.Fatal(removed, err)
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	after := a.workerMetrics("test-proj")
	if totalWriterChanges(t, ctx) != before {
		t.Fatal("idle refresh/cleanup performed writes")
	}
	if after.DefinitionLoads != metrics.DefinitionLoads || after.IdleTicks-metrics.IdleTicks != 100 || after.SQLNs != metrics.SQLNs {
		t.Fatal("idle path decoded definitions or calculated", metrics, after)
	}
	if after.DefinitionCacheHits <= metrics.DefinitionCacheHits {
		t.Fatal("maintenance did not reuse definitions")
	}
	// A refresh tick does not clean an obsolete generation even when work is due.
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET last_cleanup_ms=17 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "force": true})
	runProjectionWorker(t, a, ctx)
	if status := mustCall(t, a, ctx, "projections_status", map[string]any{"name": p.Name}); status["cleanup_ms"] != int64(17) {
		t.Fatal("refresh overwrote independent cleanup timing", status)
	}
	var generations int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_generations WHERE projection_id=?`, p.ID).Scan(&generations)
	if generations < 2 {
		t.Fatal("refresh reclaimed generations", generations)
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_generations WHERE projection_id=?`, p.ID).Scan(&generations)
	if generations != 1 {
		t.Fatal("separate maintenance did not reclaim old generation", generations)
	}
}
func TestProjectionDefinitionCacheTracksConfigurationNotWatermarks(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	defs, epoch, err := a.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || len(defs) != 1 {
		t.Fatal(defs, err)
	}
	if defs[0].Built || defs[0].Published != 0 {
		t.Fatal("mutable fields retained in definition cache")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET latest_relevant_change=3,published_change=3,published_at_ms=1,last_failure='test',last_calculation_ms=99 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	_, next, err := a.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || epoch != next {
		t.Fatal("runtime state invalidated definitions", epoch, next, err)
	}
	mustCall(t, a, ctx, "projections_update", map[string]any{"name": p.Name, "min_refresh_interval_seconds": 30})
	defs, next, err = a.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || next == epoch || defs[0].Options.Interval != 30 {
		t.Fatal("configuration cache stale", defs, next, err)
	}
	epoch = next
	mustCall(t, a, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": true})
	defs, next, err = a.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || next == epoch || defs[0].Status != "paused" {
		t.Fatal("pause cache stale", defs, next, err)
	}
	// Another process (including the previous blue-green sidecar) must invalidate it.
	var seq int
	var name, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if _, err := other.Exec(`UPDATE projection_definitions SET status='active',options=json_set(options,'$.min_refresh_interval_seconds',0) WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	defs, next, err = a.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || next == epoch || defs[0].Status != "active" {
		t.Fatal("external config cache stale", defs, next, err)
	}
	// Database identity matters even if both SDK test contexts start at generation zero.
	another := newTestCtx(t)
	defs, _, err = a.workerDefinitions(context.Background(), another, "test-proj")
	if err != nil || len(defs) != 0 {
		t.Fatal("cache crossed databases", defs, err)
	}
}
func TestProjectionCleanupPreservesPublishedAndLiveLeases(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "force": true})
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(item, ok, err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO projection_generations VALUES(?,'live_stage',?,0); INSERT INTO `+quote(projectionData(p))+` (centre_id,total,_projection_scope,_projection_generation) VALUES('unpublished',1,'unpublished','live_stage'); INSERT INTO projection_generations VALUES(?,'empty_orphan','',0)`, p.ID, item.LeaseToken, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var live, published int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p)) + ` WHERE _projection_generation='live_stage'`).Scan(&live)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionVisibleTable(p))).Scan(&published)
	if live != 1 || published != 1 {
		t.Fatal("cleanup reclaimed protected rows", live, published)
	}
	var empty int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_generations WHERE generation='empty_orphan'`).Scan(&empty)
	if empty != 0 {
		t.Fatal("empty orphan retained")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_queue SET claimed_until=datetime('now','-1 second') WHERE projection_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p)) + ` WHERE _projection_generation='live_stage'`).Scan(&live)
	if live != 0 || len(projectionRows(t, a, ctx)) != 1 {
		t.Fatal("expired work not reclaimed or published rows lost", live)
	}
}
func TestProjectionRetirementCancelsQueuesPauseRetainsAndRestartRecovers(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	args := watchedProjectionArgs(p.Name)
	delete(args, "source_dependencies")
	args["version"] = 2
	args["activate"] = false
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "version": 1, "force": true})
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok || item.ProjectionID != p.ID {
		t.Fatal(item, ok, err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO projection_generations VALUES(?,'retired_stage',?,0); INSERT INTO `+quote(projectionData(p))+` (centre_id,total,_projection_scope,_projection_generation) VALUES('staged',1,'staged','retired_stage')`, p.ID, item.LeaseToken); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "projections_activate", map[string]any{"name": p.Name, "version": 2})
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var retiredStage int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p)) + ` WHERE _projection_generation='retired_stage'`).Scan(&retiredStage)
	if retiredStage != 1 {
		t.Fatal("retirement discarded a still-valid staging lease")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_retired_leases SET claimed_until=datetime('now','-1 second') WHERE projection_id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p)) + ` WHERE _projection_generation='retired_stage'`).Scan(&retiredStage)
	if retiredStage != 0 {
		t.Fatal("expired retired staging not reclaimed")
	}

	old := mustCall(t, a, ctx, "projections_status", map[string]any{"name": p.Name, "version": 1})
	if old["pending_scopes"] != 0 || old["refresh_running"] != false || old["ready"] != false || old["next_scheduled_refresh"] != nil {
		t.Fatal("retired operational work remains", old)
	}
	if _, err := callTool(a, ctx, "projections_refresh", map[string]any{"name": p.Name, "version": 1, "rebuild": true}); err == nil {
		t.Fatal("retired queue re-created")
	}
	if err := publishProjectionRows(ctx, p, item, map[string][]map[string]any{}); err == nil {
		t.Fatal("retired worker published")
	}
	mustCall(t, a, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": true})
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 2}}})
	runProjectionWorker(t, a, ctx)
	paused := mustCall(t, a, ctx, "projections_status", map[string]any{"name": p.Name})
	if paused["paused_scopes"].(int) == 0 || paused["runnable_pending_scopes"] != 0 || paused["next_scheduled_refresh"] != nil {
		t.Fatal("pause did not retain resume work", paused)
	}
	a.closeProjectionReader()
	recovered := &App{}
	t.Cleanup(recovered.closeProjectionReader)
	mustCall(t, recovered, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": false})
	runProjectionWorker(t, recovered, ctx)
	if len(projectionRows(t, recovered, ctx)) != 2 {
		t.Fatal("restart/resume lost invalidation")
	}
	if err := recovered.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	mustCall(t, recovered, ctx, "projections_delete", map[string]any{"name": p.Name, "version": 1, "confirm": true})
	defs, _, err := recovered.workerDefinitions(context.Background(), ctx, "test-proj")
	if err != nil || len(defs) != 1 || defs[0].Version != 2 {
		t.Fatal("deletion cache stale", defs, err)
	}
}
func TestProjectionWakeupsCoalesceAndPeriodicFallbackFindsExternalChanges(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	select {
	case <-a.projectionWakeChannel():
	default:
	}
	lifetime, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- a.projectionWakeWorker(lifetime, ctx) }()
	for i := 0; i < 30; i++ {
		mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": 1, "fields": map[string]any{"centre_id": fmt.Sprintf("c%d", i%3)}})
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		status := mustCall(t, a, ctx, "projections_status", map[string]any{"name": p.Name})
		if status["ready"] == true {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("wake worker lost burst invalidations", status)
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Clear wakeups and modify via raw SQL: there is no local notification.
	select {
	case <-a.projectionWakeChannel():
	default:
	}
	source, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	if _, err := ctx.AppDB().Exec(`UPDATE ` + quote(source.PhysicalName) + ` SET centre_id='external' WHERE id=1`); err != nil {
		t.Fatal(err)
	}
	if len(a.projectionWakeChannel()) != 0 {
		t.Fatal("raw SQL unexpectedly sent local wakeup")
	}
	runProjectionWorker(t, a, ctx)
	rows := projectionRows(t, a, ctx)
	if len(rows) != 1 || rows[0]["centre_id"] != "external" {
		t.Fatal("fallback missed old/new scope move", rows)
	}
	status := mustCall(t, a, ctx, "projections_worker_status", map[string]any{})
	m := status["metrics"].(projectionWorkerMetrics)
	if m.RefreshJobs == 0 || m.SQLNs == 0 || m.RefreshNs == 0 || m.ConsumptionNs == 0 || m.EventNs == 0 || m.FailedRefreshes != 0 {
		t.Fatal("phase metrics missing or incorrect", m)
	}
	t.Setenv("APTEVA_PROJECT_ID", "")
	other := ctx.WithProject("other-proj")
	if got := mustCall(t, a, other, "projections_worker_status", map[string]any{"_project_id": "other-proj"})["metrics"].(projectionWorkerMetrics); got.RefreshJobs != 0 {
		t.Fatal("metrics leaked projects", got)
	}
	for _, tool := range a.MCPTools() {
		if tool.Name == "projections_worker_status" {
			if _, err := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{DefaultEffect: "deny"}), ctx, map[string]any{}); err == nil {
				t.Fatal("worker metrics bypassed permissions")
			}
		}
	}
}

func TestProjectionMaintenanceRetainsUnconsumedChangesAndDrainsInBatches(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	source, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	if _, err := ctx.AppDB().Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<10000) INSERT INTO projection_changes(project_id,table_id,row_id,operation,old_values,new_values) SELECT 'test-proj',?,1,'update','{"centre_id":"a"}','{"centre_id":"a"}' FROM seq`, source.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET latest_relevant_change=10000 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var cursor, remaining, minID int64
	ctx.AppDB().QueryRow(`SELECT last_change_id FROM projection_cursors WHERE projection_id=?`, p.ID).Scan(&cursor)
	ctx.AppDB().QueryRow(`SELECT COUNT(*),MIN(change_id) FROM projection_changes`).Scan(&remaining, &minID)
	if remaining >= 10000 || remaining < 10000-cursor || minID > cursor+1 {
		t.Fatal("pruning lost unconsumed changes or did no work", cursor, remaining, minID)
	}
	for i := 0; i < 20; i++ {
		if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 32; i++ {
		if err := a.projectionCleanupWorker(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
		ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_changes`).Scan(&remaining)
		if remaining == 0 {
			break
		}
	}
	if remaining != 0 || a.workerMetrics("test-proj").PrunedChanges == 0 || a.workerMetrics("test-proj").PrunedChanges > 10000 {
		t.Fatal("consumed log did not drain", remaining, a.workerMetrics("test-proj"))
	}
}

func TestProjectionStagingFenceStopsPausedAndRetiredBatches(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "force": true})
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(item, ok, err)
	}
	staging := context.WithValue(context.Background(), projectionStagingLeaseKey{}, item)
	called := false
	batch := func(context.Context, *sql.Tx) error { called = true; return nil }
	if err := publicationTxPhase(staging, ctx, p, "staging", batch); err != nil || !called {
		t.Fatal("live batch was fenced", err)
	}
	mustCall(t, a, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": true})
	called = false
	if err := publicationTxPhase(staging, ctx, p, "staging", batch); err == nil || called {
		t.Fatal("paused batch reached row staging")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET status='retired' WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	if err := publicationTxPhase(staging, ctx, p, "staging", batch); err == nil || called {
		t.Fatal("retired batch reached row staging")
	}
}

func TestProjectionWorkerMigrationPreservesResultsAndPausedWork(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "v0212.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 16; i++ {
		paths, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", i))
		if err != nil || len(paths) != 1 {
			t.Fatal(paths, err)
		}
		body, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	for i, status := range []string{"active", "paused", "retired"} {
		if _, err := db.Exec(`INSERT INTO projection_definitions(id,project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table) VALUES(?,'p',?,1,?,'SELECT 1','[]','[]','[]',?)`, i+1, fmt.Sprintf("stats_%d", i), status, fmt.Sprintf("p_%d", i+1)); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO projection_queue(projection_id,project_id,scope_key,lease_token,claimed_until) VALUES(?,'p','__all__',?,datetime('now','+30 seconds'))`, i+1, fmt.Sprintf("lease_%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`CREATE TABLE pd_1 (_projection_generation TEXT,value INTEGER); INSERT INTO pd_1 VALUES('published',42); CREATE INDEX pi_legacy ON pd_1(_projection_generation,value)`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/017_projection_worker_state.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var live, paused, retired, leases, value int
	db.QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=1`).Scan(&live)
	db.QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=2`).Scan(&paused)
	db.QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=3`).Scan(&retired)
	db.QueryRow(`SELECT COUNT(*) FROM projection_retired_leases WHERE projection_id=3`).Scan(&leases)
	db.QueryRow(`SELECT value FROM pd_1 WHERE _projection_generation='published'`).Scan(&value)
	if live != 1 || paused != 1 || retired != 0 || leases != 1 || value != 42 {
		t.Fatal("migration changed results or queue semantics", live, paused, retired, leases, value)
	}
	if _, err := db.Exec(`INSERT INTO projection_queue(projection_id,project_id,scope_key) VALUES(3,'p','stale_consumer')`); err != nil {
		t.Fatal(err)
	}
	db.QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=3`).Scan(&retired)
	if retired != 0 {
		t.Fatal("previous sidecar resurrected retired work")
	}
	var indexSQL string
	db.QueryRow(`SELECT sql FROM sqlite_master WHERE name='pi_legacy'`).Scan(&indexSQL)
	if indexSQL != "CREATE INDEX pi_legacy ON pd_1(_projection_generation,value)" {
		t.Fatal("migration changed physical index", indexSQL)
	}
}

func BenchmarkProjectionIdleTick100Definitions(b *testing.B) {
	ctx, _, _ := newFileBackedTestCtx(b, "bench")
	ctx.SetEmitter(tk.NewEmitRecorder())
	a := &App{}
	b.Cleanup(a.closeProjectionReader)
	benchmarkCall(b, a, ctx, "tables_create", map[string]any{"name": "events", "columns": []any{map[string]any{"name": "value", "type": "number"}}})
	for i := 0; i < 100; i++ {
		benchmarkCall(b, a, ctx, "projections_create", map[string]any{"name": fmt.Sprintf("idle_%d", i), "version": 1, "sql": "SELECT COUNT(*) AS total FROM {events}", "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "total", "type": "number"}}})
	}
	for i := 0; i < 200; i++ {
		if err := a.projectionWorker(context.Background(), ctx); err != nil {
			b.Fatal(err)
		}
		var pending int
		ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue`).Scan(&pending)
		if pending == 0 {
			break
		}
		if i == 199 {
			b.Fatal("build did not drain")
		}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := a.projectionWorker(context.Background(), ctx); err != nil {
			b.Fatal(err)
		}
	}
}

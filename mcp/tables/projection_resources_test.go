package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestProjectionBackgroundBudgetAndBoundedAtomicPublication(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	t.Cleanup(a.closeProjectionReader)
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "original", "value": 1}}})
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "event_copy", "version": 1, "sql": "SELECT centre_id,value FROM {events}", "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "value", "type": "number"}}, "publication_batch_rows": 8})
	runProjectionWorker(t, a, ctx)
	p, _ := a.loadProjection(ctx, "test-proj", "event_copy")
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "event_copy", "name": "by_centre", "columns": []any{"centre_id"}})
	// Holding the only background connection must not consume the four SDK
	// interactive read connections. Concurrent ticks are rejected by the mutex.
	bg, err := a.backgroundReader(context.Background(), ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bg == reader || bg == ctx.AppDB() || bg.Stats().MaxOpenConnections != 1 {
		t.Fatal("background query uses the interactive/writer pool")
	}
	held, err := bg.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": "SELECT value FROM {event_copy}"}); err != nil {
		t.Fatal("background saturation blocks interactive reads", err)
	}
	held.Close()
	source, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	if _, err := ctx.AppDB().Exec(`WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<20000) INSERT INTO ` + quote(source.PhysicalName) + ` (centre_id,value) SELECT 'new',n FROM seq`); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_copy", "rebuild": true, "force": true})
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(err)
	}
	parent, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.refreshProjectionScope(parent, ctx, item) }()
	deadline := time.Now().Add(10 * time.Second)
	sawStage := false
	for time.Now().Before(deadline) {
		var staged, visible int
		err := reader.QueryRow(`SELECT COUNT(*) FROM ` + quote(projectionData(p))).Scan(&staged)
		if err != nil {
			t.Fatal(err)
		}
		if staged > 1 {
			if err := reader.QueryRow(`SELECT COUNT(*) FROM ` + quote(p.ResultTable)).Scan(&visible); err != nil {
				t.Fatal(err)
			}
			if visible != 1 {
				t.Fatal("partial generation became visible", visible)
			}
			sawStage = true
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !sawStage {
		cancel()
		t.Fatal("never observed invisible staging")
	}
	started := time.Now()
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": int64(1), "fields": map[string]any{"value": 9}})
	writeElapsed := time.Since(started)
	if writeElapsed > time.Second {
		t.Fatalf("staging monopolized writer: %s", writeElapsed)
	}
	t.Logf("ordinary source update during indexed 20k-row staging: %s", writeElapsed)
	// Cancel during staging: no partial result or watermark may be published.
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled staging result: %v", err)
	}
	var visible int
	reader.QueryRow(`SELECT COUNT(*) FROM ` + quote(p.ResultTable)).Scan(&visible)
	if visible != 1 {
		t.Fatal("canceled generation published")
	}
	if err := failProjectionQueueAt(context.Background(), ctx, item, context.Canceled, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// Retrying publishes atomically and captures the write that happened during
	// the canceled calculation. Readers transition from 1 to 20,001 rows.
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_copy", "rebuild": true, "force": true})
	item, ok, err = claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(err)
	}
	done = make(chan error, 1)
	go func() { done <- a.refreshProjectionScope(context.Background(), ctx, item) }()
	for {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			goto complete
		default:
			reader.QueryRow(`SELECT COUNT(*) FROM ` + quote(p.ResultTable)).Scan(&visible)
			if visible != 1 && visible != 20001 {
				t.Fatal("reader saw partial publication", visible)
			}
			time.Sleep(time.Millisecond)
		}
	}
complete:
	var value float64
	reader.QueryRow(`SELECT value FROM ` + quote(p.ResultTable) + ` WHERE centre_id='original'`).Scan(&value)
	if value != 9 {
		t.Fatal("retry omitted source write during build", value)
	}
	// A forced request or newer captured change still needs a follow-up tick.
	for i := 0; i < 41; i++ {
		if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ {
		runProjectionWorker(t, a, ctx)
	}
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_copy"})
	if s["ready"] != true {
		t.Fatalf("changes arriving during build not processed: %v", s)
	}
	// Cleanup is bounded in each transaction and removes abandoned hidden rows.
	for i := 0; i < 20; i++ {
		removed, err := a.cleanupProjectionGenerations(context.Background(), ctx, p)
		if err != nil {
			t.Fatal(err)
		}
		if !removed {
			break
		}
	}
}
func TestProjectionDeadlineByteLimitsAndRetryBackoff(t *testing.T) {
	ctx := newTestCtx(t)
	now := time.Now()
	a := &App{projectionNow: func() time.Time { return now }}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	p, _ := a.loadProjection(ctx, "test-proj", "event_totals")
	p.Options.BatchBytes = 1024
	// Exercise the whole refresh deadline during SQL execution with a recursive
	// calculation. Definition validation still uses LIMIT 0, never a full build.
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "deadline_test", "version": 1, "sql": `WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<10000000) SELECT SUM(n) AS total FROM seq`, "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "total", "type": "number"}}, "max_refresh_ms": 10})
	started := time.Now()
	runProjectionWorker(t, a, ctx)
	if time.Since(started) > time.Second {
		t.Fatal("SQL deadline was not enforced")
	}
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "deadline_test"})
	if s["built"] != false || s["last_failure"] == nil || s["ready"] != false {
		t.Fatalf("failure readiness: %v", s)
	}
	var attempts int
	ctx.AppDB().QueryRow(`SELECT attempts FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='deadline_test')`).Scan(&attempts)
	runProjectionWorker(t, a, ctx)
	var after int
	ctx.AppDB().QueryRow(`SELECT attempts FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='deadline_test')`).Scan(&after)
	if after != attempts {
		t.Fatal("retry loop ignores backoff")
	}
	now = now.Add(time.Second)
	runProjectionWorker(t, a, ctx)
	var next int64
	ctx.AppDB().QueryRow(`SELECT attempts,due_at_ms FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='deadline_test')`).Scan(&after, &next)
	if after != attempts+1 || next != now.UnixMilli()+2000 {
		t.Fatalf("exponential retry backoff: %d %d", after, next)
	}
	// A row larger than a publication batch must leave the prior generation.
	mustCall(t, a, ctx, "tables_create", map[string]any{"name": "texts", "columns": []any{map[string]any{"name": "value", "type": "text"}}})
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "texts", "rows": []any{map[string]any{"value": "old"}}})
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "text_copy", "version": 1, "sql": "SELECT value FROM {texts}", "source_tables": []any{"texts"}, "result_columns": []any{map[string]any{"name": "value", "type": "text"}}, "publication_batch_bytes": 1024})
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "texts", "id": int64(1), "fields": map[string]any{"value": strings.Repeat("x", 2000)}})
	runProjectionWorker(t, a, ctx)
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT value FROM {text_copy}"})["rows"].([]map[string]any)
	if len(out) != 1 || out[0]["value"] != "old" {
		t.Fatal("oversized publication replaced old generation")
	}
}
func TestProjectionMigrationPreservesLegacyRows(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	// Install a v0.1.27-style physical result into the upgraded metadata, then
	// run the mount-time conversion. Legacy bytes remain queryable until rebuild.
	_, err := ctx.AppDB().Exec(`INSERT INTO projection_definitions(project_id,name,version,status,sql_text,source_tables,result_columns,scope_columns,result_table,is_current) VALUES('test-proj','legacy',1,'active','SELECT COUNT(*) AS total FROM {events}','["events"]','[{"name":"total","type":"number"}]','[]','p_1',1); CREATE TABLE p_1(id INTEGER PRIMARY KEY,created_at TEXT,updated_at TEXT,_revision INTEGER,total REAL); INSERT INTO p_1 VALUES(1,'old','old',1,42); INSERT INTO projection_cursors VALUES(1,'test-proj',0)`)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.ensureProjectionStorage(ctx); err != nil {
		t.Fatal(err)
	}
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT total FROM {legacy}"})["rows"].([]map[string]any)
	if out[0]["total"] != float64(42) {
		t.Fatal("migration lost legacy result")
	}
	runProjectionWorker(t, a, ctx)
	out = mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT total FROM {legacy}"})["rows"].([]map[string]any)
	if out[0]["total"] != float64(0) {
		t.Fatal("converted legacy projection not rebuilt")
	}
}
func TestProjectionReadIsolationAndPermissions(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	for _, name := range []string{"p_1", "pd_1", "ph_1", "projection_definitions", "projection_generations"} {
		if _, err := callTool(a, ctx, "tables_query", map[string]any{"sql": "SELECT * FROM " + name}); err == nil {
			t.Fatal("raw internal name exposed", name)
		}
	}
	parent := sdk.WithCaller(context.Background(), &sdk.Caller{DefaultEffect: "deny"})
	for _, tool := range a.MCPTools() {
		if tool.Name == "indexes_create" {
			if _, err := tool.HandlerCtx(parent, ctx, map[string]any{"table": "event_totals", "name": "forbidden", "columns": []any{"centre_id"}}); err == nil {
				t.Fatal("projection index bypasses permission")
			}
		}
	}
	// Query read_snapshot batches must resolve view roots without deadlocking
	// the single connection or leaking invisible rows.
	batch := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": "read_snapshot", "operations": []any{map[string]any{"id": "q", "operation": "tables_query", "args": map[string]any{"sql": "SELECT * FROM {event_totals}"}}}})
	for _, r := range batch["results"].(map[string]any) {
		if r.(map[string]any)["status"] != "ok" {
			t.Fatalf("projection batch failed: %v", batch)
		}
	}
}
func TestProjectionMigrationFromReleasedSchema(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "legacy-release")
	_ = ctx
	// Verify the migration is a genuine additive SQL migration, using the exact
	// prior migrations on a separate file rather than patched current fixtures.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 9; i++ {
		paths, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", i))
		if err != nil || len(paths) != 1 {
			t.Fatal(paths, err)
		}
		b, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(b)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO projection_definitions(project_id,name,version,result_table,sql_text,source_tables,result_columns,scope_columns) VALUES('p','old',1,'p_1','SELECT 0 AS total','[]','[{"name":"total","type":"number"}]','[]'); CREATE TABLE p_1(id INTEGER PRIMARY KEY,created_at TEXT,updated_at TEXT,_revision INTEGER,total REAL); INSERT INTO p_1 VALUES(1,'old','old',1,7); INSERT INTO projection_cursors VALUES(1,'p',0)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO projection_queue(projection_id,project_id,scope_key,pending_change_id) VALUES(1,'p','__all__',10); UPDATE projection_cursors SET last_change_id=10 WHERE projection_id=1`); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("migrations/010_projection_refresh.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(b)); err != nil {
		t.Fatal(err)
	}
	var relevant int64
	if err := db.QueryRow(`SELECT latest_relevant_change FROM projection_definitions WHERE id=1`).Scan(&relevant); err != nil || relevant != 10 {
		t.Fatal("migration lost pruned pending watermark", relevant, err)
	}
	appCtx := sdk.NewAppCtxForTest(func() *sdk.Manifest { m := (&App{}).Manifest(); return &m }(), db, ctx.Config(), nil, nil)
	a := &App{}
	if err := a.ensureProjectionStorage(appCtx); err != nil {
		t.Fatal(err)
	}
	var total float64
	if err := db.QueryRow(`SELECT total FROM p_1`).Scan(&total); err != nil || total != 7 {
		t.Fatal(total, err)
	}
}

func TestProjectionDeadlineIncludesExpensiveLaterRows(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 8}}})
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "later_rows", "version": 1, "sql": `SELECT CASE WHEN id=1 THEN value ELSE (WITH RECURSIVE seq(n) AS (VALUES(1) UNION ALL SELECT n+1 FROM seq WHERE n<10000000) SELECT SUM(n) FROM seq) END AS total FROM {events} ORDER BY id`, "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "total", "type": "number"}}, "max_refresh_ms": 20})
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 1}}})
	started := time.Now()
	runProjectionWorker(t, a, ctx)
	if time.Since(started) > time.Second {
		t.Fatal("later result row overran refresh deadline")
	}
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "later_rows"})
	if s["last_failure"] == nil || s["ready"] != false {
		t.Fatalf("later-row deadline did not fail safely: %v", s)
	}
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT total FROM {later_rows}"})["rows"].([]map[string]any)
	if len(out) != 1 || out[0]["total"] != float64(8) {
		t.Fatal("deadline replaced previous complete result")
	}
}

func TestProjectionPendingScopesSurviveDatabaseReopen(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "test-proj")
	now := time.Now()
	a := &App{projectionNow: func() time.Time { return now }}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_update", map[string]any{"name": "event_totals", "min_refresh_interval_seconds": 30})
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "restart", "value": 1}}})
	runProjectionWorker(t, a, ctx)
	var seq int
	var name, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &name, &path); err != nil {
		t.Fatal(err)
	}
	a.closeProjectionReader()
	reader.Close()
	ctx.AppDB().Close()
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	writer.SetMaxOpenConns(1)
	t.Cleanup(func() { writer.Close() })
	m := (&App{}).Manifest()
	reopened := sdk.NewAppCtxForTest(&m, writer, ctx.Config(), nil, nil).WithProject("test-proj")
	a = &App{projectionNow: func() time.Time { return now }}
	t.Cleanup(a.closeProjectionReader)
	now = now.Add(29 * time.Second)
	runProjectionWorker(t, a, reopened)
	if len(projectionRows(t, a, reopened)) != 0 {
		t.Fatal("reopen forgot the persisted interval")
	}
	s := projectionStatusFor(t, a, reopened, map[string]any{"name": "event_totals"})
	if s["pending_scopes"] != 1 {
		t.Fatal("reopen lost dirty scope", s)
	}
	now = now.Add(time.Second)
	runProjectionWorker(t, a, reopened)
	if projectionRows(t, a, reopened)[0]["centre_id"] != "restart" {
		t.Fatal("reopened worker failed to publish")
	}
}
func TestProjectionFailedReplacementLeavesCurrentReadable(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}, map[string]any{"centre_id": "b", "value": 2}}})
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "event_totals", "version": 2, "activate": false, "sql": "SELECT centre_id,COUNT(*)+10 AS total FROM {events} GROUP BY centre_id", "source_tables": []any{"events"}, "scope_columns": []any{"centre_id"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}}, "max_result_rows": 1})
	runProjectionWorker(t, a, ctx)
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "version": 2})
	if s["ready"] != false || s["built"] != false || s["last_failure"] == nil {
		t.Fatal("failed replacement readiness", s)
	}
	if _, err := callTool(a, ctx, "projections_activate", map[string]any{"name": "event_totals", "version": 2}); err == nil {
		t.Fatal("activated failed replacement")
	}
	rows := projectionRows(t, a, ctx)
	if len(rows) != 2 || rows[0]["total"] != float64(1) {
		t.Fatal("failed replacement affected current", rows)
	}
}

func TestProjectionQueryReturnsSnapshotFreshnessAndScopeStatus(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT * FROM {event_totals}"})
	metadata := out["projections"].([]map[string]any)
	if len(metadata) != 1 || metadata[0]["ready"] != false {
		t.Fatal("initial query omitted not-ready metadata", out)
	}
	runProjectionWorker(t, a, ctx)
	batch := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": "read_snapshot", "operations": []any{map[string]any{"id": "status", "operation": "projections_status", "args": map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "empty"}}}, map[string]any{"id": "q", "operation": "tables_query", "args": map[string]any{"sql": "SELECT * FROM {event_totals}"}}}})
	results := batch["results"].(map[string]any)
	for _, r := range results {
		if r.(map[string]any)["status"] != "ok" {
			t.Fatal("snapshot status/query batch failed", batch)
		}
	}
	if results["status"].(map[string]any)["result"].(map[string]any)["ready"] != true {
		t.Fatal("complete empty scope marked not ready")
	}
	// A batch's preloaded projection schema cannot become a writable table.
	bad := mustCall(t, a, ctx, "tables_batch", map[string]any{"mode": "best_effort", "operations": []any{map[string]any{"id": "q", "operation": "tables_query", "args": map[string]any{"sql": "SELECT * FROM {event_totals}"}}, map[string]any{"id": "write", "operation": "rows_insert", "args": map[string]any{"table": "event_totals", "rows": []any{map[string]any{"centre_id": "evil", "total": 1}}}}}})
	if bad["results"].(map[string]any)["write"].(map[string]any)["status"] != "error" {
		t.Fatal("batch cache allowed projection row write")
	}
}

func TestProjectionInvalidationFailureQueuesBoundedRebuild(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "lookup_fallback", "version": 1, "sql": "SELECT centre_id,COUNT(*) AS total FROM {events} GROUP BY centre_id", "source_tables": []any{"events"}, "scope_columns": []any{"centre_id"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}}, "scope_rules": []any{map[string]any{"source_table": "events", "sql": "SELECT centre_id FROM {events} WHERE json_extract(centre_id,'$.value')=?", "params": []any{"value"}, "values": map[string]any{"centre_id": map[string]any{"column": "centre_id"}}}}})
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "not-json", "value": 1}}})
	if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "lookup_fallback"})
	if s["last_failure"] == nil || s["pending_scopes"] != 1 || s["unconsumed_relevant_changes"] != false {
		t.Fatal("mapping failure lost event or cursor", s)
	}
	runProjectionWorker(t, a, ctx)
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT centre_id,total FROM {lookup_fallback}"})["rows"].([]map[string]any)
	if len(out) != 1 || out[0]["total"] != float64(1) {
		t.Fatal("fallback did not restore correctness", out)
	}
}

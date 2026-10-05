package main

import (
	"context"
	"fmt"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func projectionSourceTable(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "events",
		"columns": []any{
			map[string]any{"name": "centre_id", "type": "text", "nullable": false},
			map[string]any{"name": "value", "type": "number"},
		},
	})
}

func createProjection(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	mustCall(t, app, ctx, "projections_create", map[string]any{
		"name":          "event_totals",
		"version":       1,
		"sql":           "SELECT centre_id, COUNT(*) AS total FROM {events} GROUP BY centre_id",
		"source_tables": []any{"events"},
		"result_columns": []any{
			map[string]any{"name": "centre_id", "type": "text", "nullable": false},
			map[string]any{"name": "total", "type": "number", "nullable": false},
		},
		"scope_columns": []any{"centre_id"},
	})
}

func runProjectionWorker(t *testing.T, app *App, ctx *sdk.AppCtx) {
	t.Helper()
	if err := app.projectionWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
}

func projectionRows(t *testing.T, app *App, ctx *sdk.AppCtx) []map[string]any {
	t.Helper()
	out := mustCall(t, app, ctx, "tables_query", map[string]any{
		"sql": "SELECT centre_id,total FROM {event_totals} ORDER BY centre_id",
	})
	rows, ok := out["rows"].([]map[string]any)
	if ok {
		return rows
	}
	raw, ok := out["rows"].([]any)
	if !ok {
		t.Fatalf("unexpected projection rows type %T", out["rows"])
	}
	rows = make([]map[string]any, len(raw))
	for i, value := range raw {
		rows[i] = value.(map[string]any)
	}
	return rows
}

func TestProjectionInitialBuildAndTablesQuery(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{
		map[string]any{"centre_id": "a", "value": 1},
		map[string]any{"centre_id": "a", "value": 2},
		map[string]any{"centre_id": "b", "value": 3},
	}})
	createProjection(t, app, ctx)
	runProjectionWorker(t, app, ctx)
	rows := projectionRows(t, app, ctx)
	if len(rows) != 2 || rows[0]["centre_id"] != "a" || rows[0]["total"] != float64(2) || rows[1]["total"] != float64(1) {
		t.Fatalf("unexpected projection rows: %#v", rows)
	}
}

func TestProjectionCoalescesChangesAndRefreshesMovedScopes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	inserted := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{
		map[string]any{"centre_id": "a", "value": 1},
		map[string]any{"centre_id": "b", "value": 2},
	}})
	createProjection(t, app, ctx)
	runProjectionWorker(t, app, ctx)
	rows := projectionRows(t, app, ctx)
	if len(rows) != 2 {
		t.Fatalf("initial rows: %#v", rows)
	}

	ids, ok := inserted["ids"].([]int64)
	if !ok || len(ids) == 0 {
		t.Fatalf("unexpected inserted ids: %#v", inserted["ids"])
	}
	id := ids[0]
	mustCall(t, app, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"centre_id": "b"}})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{
		map[string]any{"centre_id": "b", "value": 4},
		map[string]any{"centre_id": "b", "value": 5},
	}})
	if err := app.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE scope_key <> ?`, projectionAllScope).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 2 {
		t.Fatalf("expected two coalesced affected scopes, got %d", queued)
	}
	runProjectionWorker(t, app, ctx)
	rows = projectionRows(t, app, ctx)
	if len(rows) != 1 || rows[0]["centre_id"] != "b" || rows[0]["total"] != float64(4) {
		t.Fatalf("move/delete scope refresh produced %#v", rows)
	}
}

func TestProjectionRollbackDoesNotCaptureChange(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	table, err := loadTable(ctx.AppDB(), "test-proj", "events")
	if err != nil {
		t.Fatal(err)
	}
	var before int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_changes`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(`INSERT INTO ` + quote(table.PhysicalName) + `(centre_id,value) VALUES('rollback',9)`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var after int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_changes`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatalf("rolled back source write left durable change: before=%d after=%d", before, after)
	}
}

func TestProjectionWritesAreNotExposedThroughRowTools(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	if _, err := callTool(app, ctx, "rows_insert", map[string]any{"table": "event_totals", "rows": []any{map[string]any{"centre_id": "x", "total": 1}}}); err == nil {
		t.Fatal("projection accepted a row write")
	}
}

func TestProjectionDeleteReleasesSourceTable(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	if _, err := callTool(app, ctx, "tables_drop", map[string]any{"name": "events", "confirm": true}); err == nil {
		t.Fatal("source table dropped while projection was active")
	}
	mustCall(t, app, ctx, "projections_delete", map[string]any{"name": "event_totals", "confirm": true})
	mustCall(t, app, ctx, "tables_drop", map[string]any{"name": "events", "confirm": true})
}

func TestProjectionSQLCannotReadOutsideDeclaredTables(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	if _, err := ctx.AppDB().Exec(`CREATE TABLE private_data(secret TEXT)`); err != nil {
		t.Fatal(err)
	}
	_, err := callTool(app, ctx, "projections_create", map[string]any{
		"name":          "unsafe_projection",
		"version":       1,
		"sql":           "SELECT secret AS centre_id FROM private_data",
		"source_tables": []any{"events"},
		"result_columns": []any{
			map[string]any{"name": "centre_id", "type": "text"},
		},
	})
	if err == nil {
		t.Fatal("projection accepted an undeclared SQLite table")
	}
}

func TestWholeProjectionRefreshesWithoutScopeColumns(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	mustCall(t, app, ctx, "projections_create", map[string]any{
		"name":          "event_count",
		"version":       1,
		"sql":           "SELECT COUNT(*) AS total FROM {events}",
		"source_tables": []any{"events"},
		"result_columns": []any{
			map[string]any{"name": "total", "type": "number", "nullable": false},
		},
	})
	runProjectionWorker(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "x", "value": 1}}})
	runProjectionWorker(t, app, ctx)
	out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": "SELECT total FROM {event_count}"})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["total"] != float64(1) {
		t.Fatalf("unexpected whole projection result: %#v", rows)
	}
}

func TestProjectionLeaseFencesStalePublisher(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	old, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatalf("first claim: %#v %v", old, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_queue SET claimed_until=datetime('now','-1 second') WHERE projection_id=? AND scope_key=?`, old.ProjectionID, old.ScopeKey); err != nil {
		t.Fatal(err)
	}
	newer, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok || newer.LeaseToken == old.LeaseToken {
		t.Fatalf("second claim did not fence first: old=%#v new=%#v err=%v", old, newer, err)
	}
	p, err := app.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	if err := publishProjectionRows(ctx, p, old, map[string][]map[string]any{projectionAllScope: {}}); err == nil {
		t.Fatal("stale worker published after losing its lease")
	}
	var token string
	if err := ctx.AppDB().QueryRow(`SELECT lease_token FROM projection_queue WHERE projection_id=? AND scope_key=?`, old.ProjectionID, old.ScopeKey).Scan(&token); err != nil {
		t.Fatal(err)
	}
	if token != newer.LeaseToken {
		t.Fatalf("stale publication changed lease token: %q != %q", token, newer.LeaseToken)
	}
}

func TestPausedProjectionCapturesAndResumes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	runProjectionWorker(t, app, ctx)
	mustCall(t, app, ctx, "projections_pause", map[string]any{"name": "event_totals", "paused": true})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "paused", "value": 1}}})
	var changes int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_changes`).Scan(&changes); err != nil || changes == 0 {
		t.Fatalf("paused projection lost durable change: %d %v", changes, err)
	}
	mustCall(t, app, ctx, "projections_pause", map[string]any{"name": "event_totals", "paused": false})
	runProjectionWorker(t, app, ctx)
	rows := projectionRows(t, app, ctx)
	if len(rows) != 1 || rows[0]["centre_id"] != "paused" {
		t.Fatalf("resume did not process captured change: %#v", rows)
	}
}

func TestProjectionVersionsBuildAlongsideAndActivate(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	projectionSourceTable(t, app, ctx)
	createProjection(t, app, ctx)
	mustCall(t, app, ctx, "projections_create", map[string]any{
		"name": "event_totals", "version": 2, "activate": false,
		"sql":            "SELECT centre_id, COUNT(*) + 10 AS total FROM {events} GROUP BY centre_id",
		"source_tables":  []any{"events"},
		"result_columns": []any{map[string]any{"name": "centre_id", "type": "text", "nullable": false}, map[string]any{"name": "total", "type": "number", "nullable": false}},
		"scope_columns":  []any{"centre_id"},
	})
	mustCall(t, app, ctx, "projections_activate", map[string]any{"name": "event_totals", "version": 2})
	runProjectionWorker(t, app, ctx)
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "v", "value": 1}}})
	runProjectionWorker(t, app, ctx)
	rows := projectionRows(t, app, ctx)
	if len(rows) != 1 || rows[0]["total"] != float64(11) {
		t.Fatalf("activated version was not read/refreshed: %#v", rows)
	}
}

func TestProjectionBoolResultsPublishOnce(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{"name": "flags", "columns": []any{map[string]any{"name": "enabled", "type": "bool"}}})
	mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "flags", "rows": []any{map[string]any{"enabled": true}}})
	mustCall(t, app, ctx, "projections_create", map[string]any{
		"name": "flag_counts", "version": 1, "sql": "SELECT enabled, COUNT(*) AS total FROM {flags} GROUP BY enabled", "source_tables": []any{"flags"},
		"result_columns": []any{map[string]any{"name": "enabled", "type": "bool", "nullable": false}, map[string]any{"name": "total", "type": "number", "nullable": false}},
	})
	runProjectionWorker(t, app, ctx)
	out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": "SELECT enabled,total FROM {flag_counts}"})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 1 || rows[0]["enabled"] != int64(1) || rows[0]["total"] != float64(1) {
		t.Fatalf("bool projection result: %#v", rows)
	}
}

func TestProjectionHighVolumeAggregateCoalescesAndRefreshes(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCall(t, app, ctx, "tables_create", map[string]any{
		"name": "sales_events",
		"columns": []any{
			map[string]any{"name": "centre_id", "type": "text", "nullable": false},
			map[string]any{"name": "caller_id", "type": "text", "nullable": false},
			map[string]any{"name": "converted", "type": "bool", "nullable": false},
			map[string]any{"name": "value", "type": "number", "nullable": false},
		},
	})
	seed := func(start, count int) {
		rows := make([]any, 0, count)
		for i := start; i < start+count; i++ {
			centre := i % 10
			caller := i % 1000
			if i >= 10000 {
				caller += 1000
			}
			rows = append(rows, map[string]any{
				"centre_id": fmt.Sprintf("centre-%02d", centre),
				"caller_id": fmt.Sprintf("caller-%04d", caller),
				"converted": i%4 == 0,
				"value":     float64(i % 97),
			})
		}
		for offset := 0; offset < len(rows); offset += 1000 {
			end := offset + 1000
			if end > len(rows) {
				end = len(rows)
			}
			mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "sales_events", "rows": rows[offset:end]})
		}
	}
	seed(0, 10000)
	mustCall(t, app, ctx, "projections_create", map[string]any{
		"name": "centre_conversion_summary", "version": 1,
		"sql": `SELECT centre_id,
  COUNT(*) AS event_count,
  COUNT(DISTINCT caller_id) AS unique_callers,
  SUM(CASE WHEN converted = 1 THEN 1 ELSE 0 END) AS conversions,
  AVG(value) AS average_value
FROM {sales_events} GROUP BY centre_id`,
		"source_tables": []any{"sales_events"},
		"result_columns": []any{
			map[string]any{"name": "centre_id", "type": "text", "nullable": false},
			map[string]any{"name": "event_count", "type": "number", "nullable": false},
			map[string]any{"name": "unique_callers", "type": "number", "nullable": false},
			map[string]any{"name": "conversions", "type": "number", "nullable": false},
			map[string]any{"name": "average_value", "type": "number", "nullable": false},
		},
		"scope_columns": []any{"centre_id"},
	})
	started := time.Now()
	runProjectionWorker(t, app, ctx)
	t.Logf("initial 10k-row aggregate refresh: %s", time.Since(started))
	seed(10000, 1000)
	if err := app.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	var queued int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='centre_conversion_summary')`).Scan(&queued); err != nil {
		t.Fatal(err)
	}
	if queued != 10 {
		t.Fatalf("expected one queued refresh per affected centre, got %d", queued)
	}
	started = time.Now()
	for i := 0; i < 3; i++ {
		runProjectionWorker(t, app, ctx)
	}
	t.Logf("coalesced 1k-event aggregate refresh: %s", time.Since(started))
	var remaining int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='centre_conversion_summary')`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("aggregate refresh left %d queued scopes", remaining)
	}
	out := mustCall(t, app, ctx, "tables_query", map[string]any{"sql": "SELECT centre_id,event_count,unique_callers,conversions FROM {centre_conversion_summary} ORDER BY centre_id"})
	rows := out["rows"].([]map[string]any)
	if len(rows) != 10 {
		t.Fatalf("expected ten centre summaries, got %d", len(rows))
	}
	for _, row := range rows {
		if row["event_count"] != float64(1100) || row["unique_callers"] != float64(200) {
			t.Fatalf("aggregate did not reflect high-volume update: %#v", row)
		}
	}
}

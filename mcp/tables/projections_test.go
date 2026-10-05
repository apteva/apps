package main

import (
	"context"
	"testing"

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

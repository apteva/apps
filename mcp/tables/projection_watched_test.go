package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func watchedDependency(table string, columns ...string) any {
	return map[string]any{"table": table, "watched_columns": columns}
}
func watchedProjectionArgs(name string, dependencies ...any) map[string]any {
	return map[string]any{"name": name, "version": 1, "sql": "SELECT centre_id,COALESCE(SUM(value),0) AS total FROM {events} GROUP BY centre_id", "scope_sql": "SELECT centre_id,COALESCE(SUM(value),0) AS total FROM {events} WHERE centre_id=? GROUP BY centre_id", "scope_params": []any{"centre_id"}, "scope_columns": []any{"centre_id"}, "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}}, "source_dependencies": dependencies}
}
func watchedFixture(t *testing.T, ctx *sdk.AppCtx, a *App) int64 {
	t.Helper()
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "tables_alter", map[string]any{"name": "events", "add": map[string]any{"name": "last_seen_at", "type": "text"}})
	id := mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 2}}})["ids"].([]int64)[0]
	mustCall(t, a, ctx, "projections_create", watchedProjectionArgs("event_totals", watchedDependency("events", "centre_id", "value")))
	runProjectionWorker(t, a, ctx)
	return id
}
func pendingWatched(t *testing.T, ctx *sdk.AppCtx, name string) int {
	t.Helper()
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name=?)`, name).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}
func consumeWatched(t *testing.T, a *App, ctx *sdk.AppCtx) {
	t.Helper()
	if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
}
func TestProjectionWatchedColumnsValuesScopesAndFreshness(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	id := watchedFixture(t, ctx, a)
	before := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})
	for _, fields := range []map[string]any{{"last_seen_at": "now"}, {"value": 2}, {"last_seen_at": nil}} {
		mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": fields})
		consumeWatched(t, a, ctx)
		after := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "a"}})
		if pendingWatched(t, ctx, "event_totals") != 0 || after["ready"] != true || after["latest_relevant_change"] != before["latest_relevant_change"] {
			t.Fatalf("irrelevant/no-op update dirtied result: %v", after)
		}
	}
	for _, value := range []any{5, nil, 7} {
		mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"value": value}})
		if projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})["ready"] != false {
			t.Fatal("watched value was marked ready before capture consumption")
		}
		consumeWatched(t, a, ctx)
		if pendingWatched(t, ctx, "event_totals") != 1 {
			t.Fatal("same-scope aggregate change lost")
		}
		runProjectionWorker(t, a, ctx)
		expected := float64(0)
		if value != nil {
			expected = float64(value.(int))
		}
		if rows := projectionRows(t, a, ctx); len(rows) != 1 || rows[0]["total"] != expected {
			t.Fatal("wrong same-scope aggregate", rows)
		}
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"centre_id": "b"}})
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "event_totals") != 2 {
		t.Fatal("old/new scope invalidation lost")
	}
	runProjectionWorker(t, a, ctx)
	if rows := projectionRows(t, a, ctx); len(rows) != 1 || rows[0]["centre_id"] != "b" {
		t.Fatal("old scope remains", rows)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 3}}})
	mustCall(t, a, ctx, "rows_delete", map[string]any{"table": "events", "id": id})
	runProjectionWorker(t, a, ctx)
	if rows := projectionRows(t, a, ctx); len(rows) != 1 || rows[0]["total"] != float64(3) {
		t.Fatal("insert/delete invalidation lost", rows)
	}
	desc := mustCall(t, a, ctx, "projections_describe", map[string]any{"name": "event_totals"})
	if len(desc["source_dependencies"].([]any)) != 1 {
		t.Fatal("watch configuration not exposed", desc)
	}
}
func TestProjectionWatchedColumnsSharedTriggersAndBuildingVersions(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	id := watchedFixture(t, ctx, a)
	mustCall(t, a, ctx, "projections_create", watchedProjectionArgs("legacy"))
	args := watchedProjectionArgs("event_totals", watchedDependency("events", "centre_id", "last_seen_at"))
	// A replacement has different SQL inputs and an independent watch list.
	args["version"] = 2
	args["activate"] = false
	args["sql"] = "SELECT centre_id,COUNT(last_seen_at) AS total FROM {events} GROUP BY centre_id"
	args["scope_sql"] = "SELECT centre_id,COUNT(last_seen_at) AS total FROM {events} WHERE centre_id=? GROUP BY centre_id"
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"last_seen_at": "seen"}})
	current := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "version": 1})
	building := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "version": 2})
	legacy := projectionStatusFor(t, a, ctx, map[string]any{"name": "legacy"})
	if current["ready"] != true || building["ready"] != false || legacy["ready"] != false {
		t.Fatal("shared triggers mixed relevance", current, building, legacy)
	}
	consumeWatched(t, a, ctx)
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue`).Scan(&count); err != nil || count != 2 {
		t.Fatal("incorrect per-version jobs", count, err)
	}
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_activate", map[string]any{"name": "event_totals", "version": 2})
	if rows := projectionRows(t, a, ctx); len(rows) != 1 || rows[0]["total"] != float64(1) {
		t.Fatal("watched replacement not published", rows)
	}
}
func TestProjectionWatchedColumnsConfigurationValidation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	for _, dependencies := range [][]any{
		{watchedDependency("missing", "value")}, {watchedDependency("events")}, {watchedDependency("events", "centre_id", "missing")},
		{watchedDependency("events", "centre_id", "value", "value")}, {watchedDependency("events", "value")},
		{watchedDependency("events", "centre_id"), watchedDependency("events", "centre_id")},
	} {
		if _, err := callTool(a, ctx, "projections_create", watchedProjectionArgs("invalid", dependencies...)); err == nil {
			t.Fatalf("invalid watch list accepted: %v", dependencies)
		}
	}
}
func TestProjectionWatchedColumnsMappingsAndDerivedInputs(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	watchedFixture(t, ctx, a)
	mustCall(t, a, ctx, "tables_create", map[string]any{"name": "owners", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "multiplier", "type": "number"}, map[string]any{"name": "metadata", "type": "text"}}})
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "owners", "rows": []any{map[string]any{"centre_id": "a", "multiplier": 2}}})
	args := watchedProjectionArgs("weighted", watchedDependency("events", "centre_id", "value"), watchedDependency("owners", "centre_id", "multiplier"))
	args["source_tables"] = []any{"events", "owners"}
	args["sql"] = "SELECT e.centre_id,SUM(e.value*o.multiplier) AS total FROM {events} e JOIN {owners} o ON e.centre_id=o.centre_id GROUP BY e.centre_id"
	args["scope_sql"] = "SELECT e.centre_id,SUM(e.value*o.multiplier) AS total FROM {events} e JOIN {owners} o ON e.centre_id=o.centre_id WHERE e.centre_id=? GROUP BY e.centre_id"
	args["scope_rules"] = []any{map[string]any{"source_table": "owners", "sql": "SELECT DISTINCT centre_id FROM {events} WHERE centre_id=?", "params": []any{"centre_id"}, "values": map[string]any{"centre_id": map[string]any{"column": "centre_id"}}}}
	args["source_dependencies"] = []any{watchedDependency("owners", "multiplier")}
	if _, err := callTool(a, ctx, "projections_create", args); err == nil || !strings.Contains(err.Error(), "mapping input") {
		t.Fatal("missing mapping input accepted", err)
	}
	args["source_dependencies"] = []any{watchedDependency("events", "centre_id", "value"), watchedDependency("owners", "centre_id", "multiplier")}
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "owners", "id": int64(1), "fields": map[string]any{"metadata": "irrelevant"}})
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "weighted") != 0 {
		t.Fatal("metadata ran dependency rules")
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "owners", "id": int64(1), "fields": map[string]any{"multiplier": 3}})
	runProjectionWorker(t, a, ctx)
	got := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT * FROM {weighted}"})["rows"].([]map[string]any)
	if len(got) != 1 || got[0]["total"] != float64(6) {
		t.Fatal("mapping missed aggregate change", got)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 4}}})
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "owners", "id": int64(1), "fields": map[string]any{"centre_id": "b"}})
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "weighted") != 2 {
		t.Fatal("remapping missed old/new dependent scopes")
	}
	runProjectionWorker(t, a, ctx)
	got = mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT centre_id,total FROM {weighted}"})["rows"].([]map[string]any)
	if len(got) != 1 || got[0]["centre_id"] != "b" || got[0]["total"] != float64(12) {
		t.Fatal("remapped result wrong", got)
	}
	// Direct derived scope fields are also required, even when result scope names differ.
	args = watchedProjectionArgs("derived", watchedDependency("events", "value"))
	args["scope_rules"] = []any{map[string]any{"source_table": "events", "values": map[string]any{"centre_id": map[string]any{"column": "centre_id"}}}}
	if _, err := callTool(a, ctx, "projections_create", args); err == nil {
		t.Fatal("missing derived scope input accepted")
	}
}
func TestProjectionWatchedColumnsBurstRestartAndInFlightFollowup(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	id := watchedFixture(t, ctx, a)
	table, err := loadTable(ctx.AppDB(), "test-proj", "events")
	if err != nil {
		t.Fatal(err)
	}
	// Hundreds of relevant and metadata changes share one scope. Leave part of
	// the log unconsumed and one refresh claimed at the restart boundary.
	for i := 0; i < 300; i++ {
		if _, err := ctx.AppDB().Exec(`UPDATE `+quote(table.PhysicalName)+` SET value=?,last_seen_at=? WHERE id=?`, i, fmt.Sprint(i), id); err != nil {
			t.Fatal(err)
		}
	}
	consumeWatched(t, a, ctx)
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	p, err := a.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"value": 1000}})
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "event_totals") != 1 {
		t.Fatal("in-flight change created duplicate jobs")
	}
	if err := publishProjectionRows(ctx, p, item, map[string][]map[string]any{item.ScopeKey: {{"centre_id": "a", "total": float64(299)}}}); err != nil {
		t.Fatal(err)
	}
	if pendingWatched(t, ctx, "event_totals") != 1 {
		t.Fatal("in-flight publication lost followup")
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"value": 1001}})
	var seq int
	var dbname, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &dbname, &path); err != nil {
		t.Fatal(err)
	}
	reader.Close()
	ctx.AppDB().Close()
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	reopened, err := sql.Open("sqlite", "file:"+path+"?mode=ro&_pragma=query_only(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	writer.SetMaxOpenConns(1)
	reopened.SetMaxOpenConns(4)
	manifest := a.Manifest()
	next := sdk.NewAppCtxForTest(&manifest, writer, sdk.Config{"max_query_ms": "5000"}, nil, nil).WithProject("test-proj")
	next.SetAppReadDBForTest(reopened)
	a = &App{}
	if err := a.rebuildAllProjectionTriggers(next); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		runProjectionWorker(t, a, next)
	}
	if rows := projectionRows(t, a, next); len(rows) != 1 || rows[0]["total"] != float64(1001) {
		t.Fatal("restart lost durable invalidation", rows)
	}
	before := projectionRows(t, a, next)
	mustCall(t, a, next, "projections_refresh", map[string]any{"name": "event_totals", "rebuild": true, "force": true})
	runProjectionWorker(t, a, next)
	if after := projectionRows(t, a, next); !reflect.DeepEqual(before, after) {
		t.Fatal("scoped result differs from full rebuild", before, after)
	}
}
func TestProjectionWatchedColumnsMigrationPreservesLegacyPendingChanges(t *testing.T) {
	// Apply the exact previously published schema, insert a pending event, then
	// apply the additive migration. Missing relevance must never discard it.
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for i := 1; i <= 14; i++ {
		paths, err := filepath.Glob(fmt.Sprintf("migrations/%03d_*.sql", i))
		if err != nil || len(paths) != 1 {
			t.Fatal(paths, err)
		}
		body, err := os.ReadFile(paths[0])
		if err != nil {
			t.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = db.Exec(`INSERT INTO projection_changes(project_id,table_id,row_id,operation,old_values,new_values) VALUES('test-proj',1,1,'update','{"centre_id":"a"}','{"centre_id":"b"}')`); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/015_projection_watched_columns.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	var relevance sql.NullString
	var old, next string
	if err := db.QueryRow(`SELECT relevant_projection_ids,old_values,new_values FROM projection_changes`).Scan(&relevance, &old, &next); err != nil || relevance.Valid || !json.Valid([]byte(old)) || !json.Valid([]byte(next)) {
		t.Fatal("migration changed pending capture", relevance, err)
	}
	ctx := newTestCtx(t)
	a := &App{}
	id := watchedFixture(t, ctx, a)
	// A previous binary's trigger does not populate the new column. The consumer
	// must treat that event as relevant even when a watch list exists.
	p, _ := a.loadProjection(ctx, "test-proj", "event_totals")
	table, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	if _, err := ctx.AppDB().Exec(`INSERT INTO projection_changes(project_id,table_id,row_id,operation,old_values,new_values) VALUES('test-proj',?,?,'update','{"centre_id":"a"}','{"centre_id":"b"}'); UPDATE projection_definitions SET latest_relevant_change=(SELECT MAX(change_id) FROM projection_changes) WHERE id=?`, table.ID, id, p.ID); err != nil {
		t.Fatal(err)
	}
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "event_totals") != 2 {
		t.Fatal("legacy pending change was skipped")
	}
}

func TestProjectionWatchedColumnsJoinedAggregateBurstMatchesFullRebuild(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	seedJoinedFixture(t, a, ctx, 10000)
	for _, table := range []string{"calls", "sales", "prospects", "campaigns", "offers"} {
		mustCall(t, a, ctx, "tables_alter", map[string]any{"name": table, "add": map[string]any{"name": "metadata", "type": "text"}})
	}
	desc := mustCall(t, a, ctx, "projections_describe", map[string]any{"name": "joined_stats"})
	encoded, _ := json.Marshal(desc)
	var args map[string]any
	if err := json.Unmarshal(encoded, &args); err != nil {
		t.Fatal(err)
	}
	args["version"] = 2
	args["activate"] = false
	args["min_refresh_interval_seconds"] = 0
	args["source_dependencies"] = []any{
		watchedDependency("calls", "id", "prospect_id", "centre_id", "agent", "success", "duration"),
		watchedDependency("sales", "id", "prospect_id", "amount"), watchedDependency("prospects", "id", "campaign_id"),
		watchedDependency("campaigns", "id", "offer_id"), watchedDependency("offers", "id", "discount"),
	}
	mustCall(t, a, ctx, "projections_create", args)
	// A worker tick deliberately stops after its time budget. Race builds can
	// spend that entire budget on the old version; drain until the replacement
	// is ready instead of assuming both builds fit into one tick.
	for i := 0; i < 8; i++ {
		runProjectionWorker(t, a, ctx)
		if projectionStatusFor(t, a, ctx, map[string]any{"name": "joined_stats", "version": 2})["ready"] == true {
			break
		}
	}
	if status := projectionStatusFor(t, a, ctx, map[string]any{"name": "joined_stats", "version": 2}); status["ready"] != true {
		t.Fatal("replacement never became ready", status)
	}
	mustCall(t, a, ctx, "projections_activate", map[string]any{"name": "joined_stats", "version": 2})
	mustCall(t, a, ctx, "projections_delete", map[string]any{"name": "joined_stats", "version": 1, "confirm": true})
	for _, name := range []string{"calls", "sales", "prospects", "campaigns", "offers"} {
		table, err := loadTable(ctx.AppDB(), "test-proj", name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := ctx.AppDB().Exec(`UPDATE ` + quote(table.PhysicalName) + ` SET metadata='synced'`); err != nil {
			t.Fatal(err)
		}
	}
	consumeWatched(t, a, ctx)
	if pendingWatched(t, ctx, "joined_stats") != 0 || projectionStatusFor(t, a, ctx, map[string]any{"name": "joined_stats"})["ready"] != true {
		t.Fatal("metadata burst dirtied joined aggregate")
	}
	sales, err := loadTable(ctx.AppDB(), "test-proj", "sales")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	stmt, err := tx.Prepare(`UPDATE ` + quote(sales.PhysicalName) + ` SET amount=? WHERE id=1`)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2000; i++ {
		if _, err := stmt.Exec(100 + i); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		consumeWatched(t, a, ctx)
	}
	if pendingWatched(t, ctx, "joined_stats") != 1 {
		t.Fatal("2k relevant events were not coalesced")
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "prospects", "id": int64(1), "fields": map[string]any{"campaign_id": 2}})
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "offers", "id": int64(2), "fields": map[string]any{"discount": 0.3}})
	runProjectionWorker(t, a, ctx)
	read := func() []map[string]any {
		return mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT prospect_id,centre_id,total_calls,agents,successes,avg_duration,revenue,discounted FROM {joined_stats} ORDER BY prospect_id,centre_id"})["rows"].([]map[string]any)
	}
	before := read()
	if len(before) != 2 || before[0]["total_calls"] != float64(20) || before[0]["revenue"] != float64(2099) {
		t.Fatal("wrong joined aggregates", before)
	}
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "joined_stats", "rebuild": true, "force": true})
	runProjectionWorker(t, a, ctx)
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("incremental aggregate differs from full rebuild", before, after)
	}
}

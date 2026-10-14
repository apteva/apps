package main

import (
	"context"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strings"
	"testing"
)

func headIndexPlan(t *testing.T, ctx *sdk.AppCtx, query string, args ...any) string {
	t.Helper()
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN `+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	details := []string{}
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}
func assertHeadGenerationIndex(t *testing.T, ctx *sdk.AppCtx, p *projectionDefinition) {
	t.Helper()
	name := fmt.Sprintf("phg_%d", p.ID)
	var count, unique int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM("unique"),0) FROM pragma_index_list(?) WHERE name=?`, projectionHeads(p), name).Scan(&count, &unique); err != nil || count != 1 || unique != 0 {
		t.Fatal("generation index absent or unique", count, unique, err)
	}
	var col string
	if err := ctx.AppDB().QueryRow(`SELECT name FROM pragma_index_info(?)`, name).Scan(&col); err != nil || col != "generation" {
		t.Fatal("wrong generation index", col, err)
	}
	var scopePrimary int
	if err := ctx.AppDB().QueryRow(`SELECT pk FROM pragma_table_info(?) WHERE name='scope_key'`, projectionHeads(p)).Scan(&scopePrimary); err != nil || scopePrimary != 1 {
		t.Fatal("scope primary key changed", scopePrimary, err)
	}
}

func TestProjectionHeadGenerationIndexMigrationAndPlans(t *testing.T) {
	a, ctx, p := schedulerFixture(t)
	assertHeadGenerationIndex(t, ctx, p)
	// The actual discovery shape and writer recheck must both seek the head index.
	discovery := `SELECT g.generation FROM projection_generations g WHERE g.projection_id=? AND NOT EXISTS(SELECT 1 FROM ` + quote(projectionHeads(p)) + ` h WHERE h.generation=g.generation) AND NOT EXISTS(SELECT 1 FROM projection_queue q WHERE q.projection_id=g.projection_id AND q.lease_token=g.lease_token AND q.claimed_until>CURRENT_TIMESTAMP) AND NOT EXISTS(SELECT 1 FROM projection_retired_leases l WHERE l.projection_id=g.projection_id AND l.lease_token=g.lease_token AND l.claimed_until>CURRENT_TIMESTAMP) ORDER BY g.created_at_ms,g.generation LIMIT 1`
	before := projectionRows(t, a, ctx)
	if _, err := ctx.AppDB().Exec(`DROP INDEX ` + quote(fmt.Sprintf("phg_%d", p.ID))); err != nil {
		t.Fatal(err)
	}
	if plan := headIndexPlan(t, ctx, discovery, p.ID); !strings.Contains(plan, "SCAN h") {
		t.Fatal("legacy fixture did not reproduce head scan", plan)
	}
	// A registered retired version must be upgraded too, without removing results.
	args := batchProjectionArgs("replacement")
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	retired := loadBatch(t, a, ctx, "replacement")
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET status='retired' WHERE id=?; DROP INDEX `+quote(fmt.Sprintf("phg_%d", retired.ID))+`; CREATE TABLE ph_999999(scope_key TEXT PRIMARY KEY,generation TEXT NOT NULL)`, retired.ID); err != nil {
		t.Fatal(err)
	}
	next := &App{}
	defer next.closeProjectionReader()
	if err := next.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	for _, definition := range []*projectionDefinition{p, retired} {
		assertHeadGenerationIndex(t, ctx, definition)
	}
	var schema int
	if err := ctx.AppDB().QueryRow(`PRAGMA schema_version`).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if err := next.ensureProjectionDataIndexes(ctx); err != nil {
		t.Fatal(err)
	}
	var repeat int
	ctx.AppDB().QueryRow(`PRAGMA schema_version`).Scan(&repeat)
	if schema != repeat {
		t.Fatal("migration repeated DDL", schema, repeat)
	}
	var unknown int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name='phg_999999'`).Scan(&unknown)
	if unknown != 0 {
		t.Fatal("migration indexed an unregistered head table")
	}
	if !reflect.DeepEqual(before, projectionRows(t, next, ctx)) {
		t.Fatal("migration changed visible rows")
	}
	for _, plan := range []string{headIndexPlan(t, ctx, discovery, p.ID), headIndexPlan(t, ctx, `DELETE FROM projection_generations WHERE projection_id=? AND generation=? AND NOT EXISTS(SELECT 1 FROM `+quote(projectionHeads(p))+` h WHERE h.generation=?)`, p.ID, "unused", "unused")} {
		if strings.Contains(plan, "SCAN h") || !strings.Contains(plan, "USING COVERING INDEX "+fmt.Sprintf("phg_%d", p.ID)) {
			t.Fatal("cleanup does not seek generation index", plan)
		}
	}
	// Idle storage checks use the mounted-database cache, never recurring DDL.
	for i := 0; i < 5; i++ {
		if err := next.ensureWorkerStorage(ctx); err != nil {
			t.Fatal(err)
		}
	}
	ctx.AppDB().QueryRow(`PRAGMA schema_version`).Scan(&repeat)
	if repeat != schema {
		t.Fatal("cached storage check changed schema")
	}
}

func TestProjectionHeadIndexSharedGenerationAndCrashCleanup(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", batchProjectionArgs("event_totals"))
	runProjectionWorker(t, a, ctx)
	ids := mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}, map[string]any{"centre_id": "b", "value": 2}}})["ids"].([]int64)
	drainBatchMappings(t, a, ctx)
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "a"}, "force": true})
	runProjectionWorker(t, a, ctx)
	p := loadBatch(t, a, ctx, "event_totals")
	assertHeadGenerationIndex(t, ctx, p)
	var shared string
	if err := ctx.AppDB().QueryRow(`SELECT generation FROM `+quote(projectionHeads(p))+` WHERE scope_key=?`, `{"centre_id":"a"}`).Scan(&shared); err != nil {
		t.Fatal(err)
	}
	var heads int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM `+quote(projectionHeads(p))+` WHERE generation=?`, shared).Scan(&heads)
	if heads != 2 {
		t.Fatal("fixture lacks shared generation", heads)
	}
	publishScope := func(id int64, scope string, value int) {
		mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"value": value}})
		drainBatchMappings(t, a, ctx)
		mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "scope": map[string]any{"centre_id": scope}, "force": true})
		runProjectionWorker(t, a, ctx)
	}
	publishScope(ids[0], "a", 10)
	before := projectionRows(t, a, ctx)
	for i := 0; i < 5; i++ {
		if _, err := a.cleanupProjectionGenerations(t.Context(), ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	var data int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM `+quote(projectionData(p))+` WHERE _projection_generation=?`, shared).Scan(&data)
	if data != 2 {
		t.Fatal("partly published generation reclaimed", data)
	}
	if !reflect.DeepEqual(before, projectionRows(t, a, ctx)) {
		t.Fatal("cleanup changed partially refreshed results")
	}
	publishScope(ids[1], "b", 20)
	before = projectionRows(t, a, ctx)
	for i := 0; i < 5; i++ {
		if _, err := a.cleanupProjectionGenerations(t.Context(), ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_generations WHERE projection_id=? AND generation=?`, p.ID, shared).Scan(&data)
	if data != 0 {
		t.Fatal("last-head replacement did not release generation")
	}
	if !reflect.DeepEqual(before, projectionRows(t, a, ctx)) {
		t.Fatal("cleanup changed fully refreshed results")
	}
	// An abandoned staging generation is discovered by a fresh worker after a crash.
	if _, err := ctx.AppDB().Exec(`INSERT INTO projection_generations VALUES(?,'crashed_generation','expired_token',0); INSERT INTO `+quote(projectionData(p))+` (centre_id,total,_projection_scope,_projection_generation) VALUES('hidden',99,'hidden','crashed_generation')`, p.ID); err != nil {
		t.Fatal(err)
	}
	a.closeProjectionReader()
	a = &App{}
	defer a.closeProjectionReader()
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := a.cleanupProjectionGenerations(context.Background(), ctx, p); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_generations WHERE projection_id=? AND generation='crashed_generation'`, p.ID).Scan(&data)
	if data != 0 {
		t.Fatal("crash recovery lost orphan discovery")
	}
	if !reflect.DeepEqual(before, projectionRows(t, a, ctx)) {
		t.Fatal("crash recovery changed visible results")
	}
}

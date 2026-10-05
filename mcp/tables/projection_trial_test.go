package main

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"math"
	"strings"
	"testing"
	"time"
)

func projectionStatusFor(t *testing.T, a *App, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	return mustCall(t, a, ctx, "projections_status", args)
}
func TestProjectionIntervalRestartForceAndRelevantFreshness(t *testing.T) {
	ctx := newTestCtx(t)
	now := time.Date(2026, 10, 5, 12, 0, 0, 0, time.UTC)
	a := &App{projectionNow: func() time.Time { return now }}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	if projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})["ready"] != false {
		t.Fatal("initial projection marked ready")
	}
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_update", map[string]any{"name": "event_totals", "min_refresh_interval_seconds": 30})
	// Another projection causes project changes that must not create lag here.
	mustCall(t, a, ctx, "tables_create", map[string]any{"name": "other", "columns": []any{map[string]any{"name": "value", "type": "number"}}})
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "other_total", "version": 1, "sql": "SELECT COUNT(*) AS total FROM {other}", "source_tables": []any{"other"}, "result_columns": []any{map[string]any{"name": "total", "type": "number"}}})
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "other", "rows": []any{map[string]any{"value": 1}}})
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})
	if s["ready"] != true || s["lag"] != int64(0) {
		t.Fatalf("unrelated changes affected freshness: %v", s)
	}
	for i := 0; i < 29; i++ {
		now = now.Add(time.Second)
		mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": i}}})
		runProjectionWorker(t, a, ctx)
	}
	if len(projectionRows(t, a, ctx)) != 0 {
		t.Fatal("interval was bypassed")
	}
	s = projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "a"}})
	if s["ready"] != false || s["pending_scopes"] != 1 || s["consumed_change_id"].(int64) <= s["published_change_id"].(int64) {
		t.Fatalf("consumption incorrectly declared publication: %v", s)
	}
	// Replace the App object to model worker restart: scheduling and dirty scope
	// survive in SQLite. Continuous changes have not moved the first deadline.
	a = &App{projectionNow: func() time.Time { return now }}
	now = now.Add(time.Second)
	runProjectionWorker(t, a, ctx)
	rows := projectionRows(t, a, ctx)
	if len(rows) != 1 || rows[0]["total"] != float64(29) {
		t.Fatalf("continuous traffic postponed refresh: %v", rows)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 1}}})
	runProjectionWorker(t, a, ctx)
	if projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "a"}})["ready"] != true {
		t.Fatal("clean scope marked stale")
	}
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_totals", "scope": map[string]any{"centre_id": "b"}, "force": true})
	runProjectionWorker(t, a, ctx)
	if len(projectionRows(t, a, ctx)) != 2 {
		t.Fatal("forced refresh did not bypass interval")
	}
}
func TestProjectionForcedDuringClaimKeepsRevision(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(err)
	}
	p, err := a.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "projections_update", map[string]any{"name": "event_totals", "min_refresh_interval_seconds": 30})
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_totals", "force": true})
	if err := publishProjectionRows(ctx, p, item, map[string][]map[string]any{projectionAllScope: nil}); err != nil {
		t.Fatal(err)
	}
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})
	if s["pending_scopes"] != 1 || s["ready"] != false {
		t.Fatalf("in-flight force request lost: %v", s)
	}
	runProjectionWorker(t, a, ctx)
	if projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})["ready"] != true {
		t.Fatal("pending request did not complete")
	}
}
func TestProjectionIndexesBuildFailureCoverageAndActivation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 3}}})
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "event_totals", "name": "by_centre", "columns": []any{"centre_id"}, "unique": true})
	replacement := map[string]any{"name": "event_totals", "version": 2, "activate": false, "sql": "SELECT centre_id,SUM(value) AS total FROM {events} GROUP BY centre_id", "source_tables": []any{"events"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}}, "scope_columns": []any{"centre_id"}, "coverage_from": "2026-10-01T00:00:00Z", "coverage_to": "2026-11-01T00:00:00Z"}
	mustCall(t, a, ctx, "projections_create", replacement)
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "event_totals", "version": 2, "name": "by_centre", "columns": []any{"centre_id"}})
	if projectionRows(t, a, ctx)[0]["total"] != float64(1) {
		t.Fatal("unbuilt replacement exposed")
	}
	runProjectionWorker(t, a, ctx)
	s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals", "version": 2})
	if s["ready"] != true || s["coverage_from"] != "2026-10-01T00:00:00Z" {
		t.Fatalf("building version never published: %v", s)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 7}}})
	if _, err := callTool(a, ctx, "projections_activate", map[string]any{"name": "event_totals", "version": 2}); err == nil {
		t.Fatal("activation ignored unconsumed changes")
	}
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "projections_activate", map[string]any{"name": "event_totals", "version": 2})
	if projectionRows(t, a, ctx)[0]["total"] != float64(10) {
		t.Fatal("activation did not switch generation")
	}
	published := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})["last_successful_publication_at"]
	// A valid immutable query can encounter a data-dependent failure. Preserve
	// the old generation and its coverage while recording/backing off the error.
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": int64(1), "fields": map[string]any{"value": 9}})
	p, _ := a.loadProjection(ctx, "test-proj", "event_totals")
	p.Options.MaxRows = 1
	raw, _ := jsonMarshal(p.Options)
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET options=? WHERE id=?`, raw, p.ID); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "b", "value": 1}}})
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_totals", "rebuild": true, "force": true})
	item, ok, err := claimProjectionQueue(context.Background(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(err)
	}
	refreshErr := a.refreshProjectionScope(context.Background(), ctx, item)
	if refreshErr == nil {
		t.Fatal("oversized rebuild succeeded")
	}
	if err := failProjectionQueueAt(context.Background(), ctx, item, refreshErr, time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	s = projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"})
	if s["last_failure"] == nil || s["last_successful_publication_at"] != published || s["coverage_to"] != "2026-11-01T00:00:00Z" || projectionRows(t, a, ctx)[0]["total"] != float64(10) {
		t.Fatalf("failure changed successful generation: %v", s)
	}
	ix := mustCall(t, a, ctx, "indexes_list", map[string]any{"table": "event_totals"})["indexes"].([]TableIndex)
	if len(ix) != 1 {
		t.Fatal("indexes lost across refresh/activation")
	}
	mustCall(t, a, ctx, "indexes_drop", map[string]any{"table": "event_totals", "name": "by_centre", "confirm": true})
}
func jsonMarshal(v any) (string, error) { b, err := json.Marshal(v); return string(b), err }

func TestProjectionDerivedDayScopesAndDST(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	mustCall(t, a, ctx, "tables_create", map[string]any{"name": "calls", "columns": []any{map[string]any{"name": "centre", "type": "text"}, map[string]any{"name": "event_at", "type": "datetime"}}})
	// The scoped SQL binds UTC bounds for a local day, including the 23-hour
	// daylight-saving day; the projection SQL owns its business aggregation.
	mustCall(t, a, ctx, "projections_create", map[string]any{"name": "day_calls", "version": 1, "sql": `SELECT centre,'2026-03-29' AS day,COUNT(*) AS total FROM {calls} WHERE event_at>='2026-03-28T23:00:00.000000000Z' AND event_at<'2026-03-29T22:00:00.000000000Z' GROUP BY centre`, "scope_sql": `SELECT centre,'2026-03-29' AS day,COUNT(*) AS total FROM {calls} WHERE centre=? AND event_at>=? AND event_at<? GROUP BY centre`, "scope_params": []any{"centre", map[string]any{"scope_column": "day", "boundary": "start", "timezone": "Europe/Madrid"}, map[string]any{"scope_column": "day", "boundary": "end", "timezone": "Europe/Madrid"}}, "source_tables": []any{"calls"}, "scope_columns": []any{"centre", "day"}, "result_columns": []any{map[string]any{"name": "centre", "type": "text"}, map[string]any{"name": "day", "type": "text"}, map[string]any{"name": "total", "type": "number"}}, "scope_rules": []any{map[string]any{"source_table": "calls", "values": map[string]any{"centre": map[string]any{"column": "centre"}, "day": map[string]any{"column": "event_at", "bucket": "day", "timezone": "Europe/Madrid"}}}}})
	p, _ := a.loadProjection(ctx, "test-proj", "day_calls")
	key, _ := projectionScopeKey(p, map[string]any{"centre": "a", "day": "2026-03-29"})
	bound, err := projectionScopeBindings(p, key)
	if err != nil || bound[1] != "2026-03-28T23:00:00.000000000Z" || bound[2] != "2026-03-29T22:00:00.000000000Z" {
		t.Fatalf("DST boundaries: %v %v", bound, err)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "calls", "rows": []any{map[string]any{"centre": "a", "event_at": "2026-03-28T23:00:00.123Z"}}})
	runProjectionWorker(t, a, ctx)
	initial := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT total FROM {day_calls}"})["rows"].([]map[string]any)
	if len(initial) != 1 || initial[0]["total"] != float64(1) {
		t.Fatal("fractional first second of local day was excluded", initial)
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "calls", "id": int64(1), "fields": map[string]any{"centre": "b", "event_at": "2026-03-29T21:30:00Z"}})
	if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	var scopes int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue`).Scan(&scopes); err != nil || scopes != 2 {
		t.Fatalf("old/new derived scopes: %d %v", scopes, err)
	}
	runProjectionWorker(t, a, ctx)
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT centre,total FROM {day_calls}"})["rows"].([]map[string]any)
	if len(out) != 1 || out[0]["centre"] != "b" {
		t.Fatalf("old/new centre invalidation: %v", out)
	}
}

// The scoped query filters before materialization and joins five source tables.
const joinedWholeSQL = `WITH relevant AS MATERIALIZED (SELECT * FROM {calls}), revenue AS MATERIALIZED (SELECT prospect_id,SUM(amount) AS amount FROM {sales} GROUP BY prospect_id)
SELECT c.prospect_id,c.centre_id,COUNT(*) AS total_calls,COUNT(DISTINCT c.agent) AS agents,SUM(CASE WHEN c.success=1 THEN 1 ELSE 0 END) AS successes,AVG(c.duration) AS avg_duration,COALESCE(MAX(r.amount),0) AS revenue,COALESCE(MAX(r.amount*(1-o.discount)),0) AS discounted
FROM relevant c JOIN {prospects} p ON p.id=c.prospect_id JOIN {campaigns} k ON k.id=p.campaign_id JOIN {offers} o ON o.id=k.offer_id LEFT JOIN revenue r ON r.prospect_id=c.prospect_id GROUP BY c.prospect_id,c.centre_id`
const joinedScopedSQL = `WITH relevant AS MATERIALIZED (SELECT * FROM {calls} WHERE prospect_id=? AND centre_id=?), revenue AS MATERIALIZED (SELECT prospect_id,SUM(amount) AS amount FROM {sales} WHERE prospect_id=? GROUP BY prospect_id)
SELECT c.prospect_id,c.centre_id,COUNT(*) AS total_calls,COUNT(DISTINCT c.agent) AS agents,SUM(CASE WHEN c.success=1 THEN 1 ELSE 0 END) AS successes,AVG(c.duration) AS avg_duration,COALESCE(MAX(r.amount),0) AS revenue,COALESCE(MAX(r.amount*(1-o.discount)),0) AS discounted
FROM relevant c JOIN {prospects} p ON p.id=c.prospect_id JOIN {campaigns} k ON k.id=p.campaign_id JOIN {offers} o ON o.id=k.offer_id LEFT JOIN revenue r ON r.prospect_id=c.prospect_id GROUP BY c.prospect_id,c.centre_id`

func seedJoinedFixture(t testing.TB, a *App, ctx *sdk.AppCtx, history int) {
	t.Helper()
	call := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		args["_project_id"] = ctx.CurrentProject()
		r, e := callTool(a, ctx, tool, args)
		if e != nil {
			t.Fatal(tool, e)
		}
		return r.(map[string]any)
	}
	col := func(n, typ string) any { return map[string]any{"name": n, "type": typ} }
	for name, cols := range map[string][]any{"calls": {col("prospect_id", "number"), col("centre_id", "text"), col("agent", "text"), col("success", "bool"), col("duration", "number")}, "sales": {col("prospect_id", "number"), col("amount", "number")}, "prospects": {col("campaign_id", "number")}, "campaigns": {col("offer_id", "number")}, "offers": {col("discount", "number")}} {
		call("tables_create", map[string]any{"name": name, "columns": cols})
	}
	for _, table := range []string{"calls", "sales"} {
		call("indexes_create", map[string]any{"table": table, "name": "by_prospect", "columns": []any{"prospect_id"}})
	}
	call("rows_insert", map[string]any{"table": "offers", "rows": []any{map[string]any{"discount": 0.2}, map[string]any{"discount": 0.5}}})
	call("rows_insert", map[string]any{"table": "campaigns", "rows": []any{map[string]any{"offer_id": 1}, map[string]any{"offer_id": 2}}})
	call("rows_insert", map[string]any{"table": "prospects", "rows": []any{map[string]any{"campaign_id": 1}, map[string]any{"campaign_id": 1}}})
	call("rows_insert", map[string]any{"table": "sales", "rows": []any{map[string]any{"prospect_id": 1, "amount": 100}, map[string]any{"prospect_id": 2, "amount": 7}}})
	table, _ := loadTable(ctx.AppDB(), ctx.CurrentProject(), "calls")
	tx, e := ctx.AppDB().Begin()
	if e != nil {
		t.Fatal(e)
	}
	stmt, e := tx.Prepare(`INSERT INTO ` + quote(table.PhysicalName) + ` (prospect_id,centre_id,agent,success,duration) VALUES(?,?,?,?,?)`)
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < history+20; i++ {
		prospect := 2
		if i < 20 {
			prospect = 1
		}
		if _, e := stmt.Exec(prospect, "a", fmt.Sprintf("agent%d", i%4), i%2, float64(i%10)); e != nil {
			t.Fatal(e)
		}
	}
	stmt.Close()
	if e := tx.Commit(); e != nil {
		t.Fatal(e)
	}
	values := map[string]any{"prospect_id": map[string]any{"column": "prospect_id"}, "centre_id": map[string]any{"column": "centre_id"}}
	rule := func(source, q string, params []any) any {
		return map[string]any{"source_table": source, "sql": q, "params": params, "values": values}
	}
	rules := []any{rule("calls", "", nil), rule("sales", `SELECT prospect_id,centre_id FROM {calls} WHERE prospect_id=?`, []any{"prospect_id"}), rule("prospects", `SELECT prospect_id,centre_id FROM {calls} WHERE prospect_id=?`, []any{"id"}), rule("campaigns", `SELECT c.prospect_id,c.centre_id FROM {calls} c JOIN {prospects} p ON p.id=c.prospect_id WHERE p.campaign_id=?`, []any{"id"}), rule("offers", `SELECT c.prospect_id,c.centre_id FROM {calls} c JOIN {prospects} p ON p.id=c.prospect_id JOIN {campaigns} k ON k.id=p.campaign_id WHERE k.offer_id=?`, []any{"id"})}
	// DISTINCT keeps dependency fan-out bounded by scopes instead of source rows.
	for _, r := range rules {
		m := r.(map[string]any)
		if q := m["sql"].(string); q != "" {
			m["sql"] = strings.Replace(q, "SELECT ", "SELECT DISTINCT ", 1)
		}
	}
	cols := []any{col("prospect_id", "number"), col("centre_id", "text")}
	for _, n := range []string{"total_calls", "agents", "successes", "avg_duration", "revenue", "discounted"} {
		cols = append(cols, col(n, "number"))
	}
	call("projections_create", map[string]any{"name": "joined_stats", "version": 1, "sql": joinedWholeSQL, "scope_sql": joinedScopedSQL, "scope_params": []any{"prospect_id", "centre_id", "prospect_id"}, "source_tables": []any{"calls", "sales", "prospects", "campaigns", "offers"}, "scope_columns": []any{"prospect_id", "centre_id"}, "scope_rules": rules, "result_columns": cols, "min_refresh_interval_seconds": 30})
}
func TestProjectionJoinedAggregateInnerRestrictionAndDependencyBurst(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	seedJoinedFixture(t, a, ctx, 100000)
	runProjectionWorker(t, a, ctx)
	p, _ := a.loadProjection(ctx, "test-proj", "joined_stats")
	resolved, _ := a.substitutePlaceholders(ctx, "test-proj", joinedScopedSQL)
	plans, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN `+resolved, 1, "a", 1)
	if err != nil {
		t.Fatal(err)
	}
	searches := 0
	for plans.Next() {
		var id, parent, unused int
		var detail string
		if err := plans.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH") && strings.Contains(detail, "prospect_id=?") {
			searches++
		}
		if strings.HasPrefix(detail, "SCAN t_") {
			t.Fatalf("scoped calculation scans historical source: %s", detail)
		}
	}
	plans.Close()
	if searches < 2 {
		t.Fatal("calls/sales scope indexes were not used")
	}
	// 10,000 relevant writes to one prospect coalesce to one dirty scope, with
	// deletion, old/new dependency fields and campaign/offer mapping exercised.
	sales, _ := loadTable(ctx.AppDB(), "test-proj", "sales")
	tx, _ := ctx.AppDB().Begin()
	stmt, _ := tx.Prepare(`UPDATE ` + quote(sales.PhysicalName) + ` SET amount=? WHERE id=1`)
	for i := 0; i < 10000; i++ {
		if _, err := stmt.Exec(100 + i); err != nil {
			t.Fatal(err)
		}
	}
	stmt.Close()
	tx.Commit()
	for i := 0; i < 21; i++ {
		if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	var queued int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=?`, p.ID).Scan(&queued)
	if queued != 1 {
		t.Fatalf("10k relevant changes produced %d scopes", queued)
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "prospects", "id": int64(1), "fields": map[string]any{"campaign_id": 2}})
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "offers", "id": int64(2), "fields": map[string]any{"discount": 0.3}})
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "joined_stats", "scope": map[string]any{"prospect_id": 1, "centre_id": "a"}, "force": true})
	started := time.Now()
	runProjectionWorker(t, a, ctx)
	t.Logf("indexed one-prospect refresh with 100k historical calls: %s", time.Since(started))
	out := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT * FROM {joined_stats} WHERE prospect_id=1"})["rows"].([]map[string]any)
	row := out[0]
	if len(out) != 1 || row["total_calls"] != float64(20) || row["agents"] != float64(4) || row["successes"] != float64(10) || row["avg_duration"] != 4.5 || row["revenue"] != float64(10099) || math.Abs(row["discounted"].(float64)-7069.3) > 1e-6 {
		t.Fatalf("complex aggregate differs: %v", out)
	}
	reference := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": joinedScopedSQL, "params": []any{1, "a", 1}})["rows"].([]map[string]any)[0]
	for _, n := range []string{"total_calls", "agents", "successes", "avg_duration", "revenue", "discounted"} {
		if fmt.Sprint(reference[n]) != fmt.Sprint(row[n]) {
			t.Fatalf("%s differs from direct query: %v %v", n, row, reference)
		}
	}
	mustCall(t, a, ctx, "rows_delete", map[string]any{"table": "sales", "id": int64(1)})
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "joined_stats", "scope": map[string]any{"prospect_id": 1, "centre_id": "a"}, "force": true})
	runProjectionWorker(t, a, ctx)
	out = mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT revenue FROM {joined_stats} WHERE prospect_id=1"})["rows"].([]map[string]any)
	if out[0]["revenue"] != float64(0) {
		t.Fatal("sale deletion did not invalidate dependent calls")
	}
}

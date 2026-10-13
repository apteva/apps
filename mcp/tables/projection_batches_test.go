package main

import (
	"context"
	"database/sql"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

const batchTotalsSQL = `WITH dirty AS MATERIALIZED (SELECT json_extract(value,'$.centre_id') AS centre_id FROM json_each(?)) SELECT e.centre_id,SUM(e.value) AS total FROM dirty d JOIN {events} e ON e.centre_id IS d.centre_id GROUP BY e.centre_id`

func batchProjectionArgs(name string) map[string]any {
	return map[string]any{"name": name, "version": 1, "sql": "SELECT centre_id,SUM(value) AS total FROM {events} GROUP BY centre_id", "batch_scope_sql": batchTotalsSQL, "refresh_mode": "projection_batch", "min_refresh_interval_seconds": 60, "source_tables": []any{"events"}, "scope_columns": []any{"centre_id"}, "result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}}}
}
func batchWindow(t *testing.T, ctx *sdk.AppCtx, id int64) (int64, int64, int64) {
	t.Helper()
	var start, due, next int64
	if err := ctx.AppDB().QueryRow(`SELECT queued_at_ms,due_at_ms,next_due_at_ms FROM projection_queue WHERE projection_id=? AND scope_key='__batch__'`, id).Scan(&start, &due, &next); err != nil {
		t.Fatal(err)
	}
	return start, due, next
}
func drainBatchMappings(t *testing.T, a *App, ctx *sdk.AppCtx) {
	t.Helper()
	for n := 0; n < 30; n++ {
		if err := a.consumeProjectionChanges(t.Context(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
		var pending bool
		if err := ctx.AppDB().QueryRow(`SELECT EXISTS(SELECT 1 FROM projection_cursors c JOIN projection_definitions p ON p.id=c.projection_id WHERE c.last_change_id<p.latest_relevant_change)`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if !pending {
			return
		}
	}
	t.Fatal("change consumption did not converge")
}
func loadBatch(t *testing.T, a *App, ctx *sdk.AppCtx, name string) *projectionDefinition {
	t.Helper()
	p, err := a.loadProjection(ctx, "test-proj", name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestProjectionBatchCaptureWindowAndGroupedCalculation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	t.Cleanup(a.closeProjectionReader)
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "events", "name": "by_centre", "columns": []any{"centre_id"}})
	mustCall(t, a, ctx, "projections_create", batchProjectionArgs("event_totals"))
	runProjectionWorker(t, a, ctx)
	p := loadBatch(t, a, ctx, "event_totals")
	// A long quiet period does not permit immediate calculation.
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET published_at_ms=1 WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}}})
	start, due, _ := batchWindow(t, ctx, p.ID)
	if due-start != 60000 {
		t.Fatal("window was not anchored in source transaction", start, due)
	}
	if item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due-1); err != nil || ok {
		t.Fatal("unmapped changes were claimable", item, ok, err)
	}
	rows := make([]any, 500)
	for i := range rows {
		rows[i] = map[string]any{"centre_id": fmt.Sprintf("centre-%03d", i%100), "value": i}
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": rows})
	_, after, _ := batchWindow(t, ctx, p.ID)
	if after != due {
		t.Fatal("traffic postponed deadline")
	}
	drainBatchMappings(t, a, ctx)
	status := projectionStatusFor(t, a, ctx, map[string]any{"name": p.Name})
	if status["pending_scopes"] != 101 {
		t.Fatal("dirty scopes not deduplicated", status)
	}
	if _, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due-1); err != nil || ok {
		t.Fatal("window refreshed early", ok, err)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM projection_event_outbox`); err != nil {
		t.Fatal(err)
	}
	item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok || len(item.CoveredScopes) != 101 || item.FullBatch {
		t.Fatal("scopes not claimed together", item, ok, err)
	}
	if _, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due); err != nil || ok {
		t.Fatal("running window claimed twice", ok, err)
	}
	if err := a.runProjectionRefresh(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	var generations, events int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(DISTINCT generation) FROM ` + quote(projectionHeads(p)) + ` WHERE scope_key<>'__all__'`).Scan(&generations); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&events); err != nil {
		t.Fatal(err)
	}
	if generations != 1 || events != 1 {
		t.Fatal("batch did not publish once", generations, events)
	}
	got := projectionRows(t, a, ctx)
	want := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": p.SQL + " ORDER BY centre_id"})["rows"]
	if !reflect.DeepEqual(got, want) {
		t.Fatal("batch differs from full aggregate", got, want)
	}
	if s := projectionStatusFor(t, a, ctx, map[string]any{"name": p.Name}); s["ready"] != true || s["pending_scopes"] != 0 {
		t.Fatal("batch not ready", s)
	}
	m := a.workerMetrics("test-proj")
	if m.RefreshJobs != 2 {
		t.Fatal("batch calculated per scope", m.RefreshJobs)
	}
	// The dirty-set join seeks the indexed source instead of visiting history.
	source, _ := loadTable(ctx.AppDB(), "test-proj", "events")
	plan, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN `+strings.ReplaceAll(batchTotalsSQL, "{events}", quote(source.PhysicalName)), `[{"centre_id":"a"}]`)
	if err != nil {
		t.Fatal(err)
	}
	defer plan.Close()
	search := false
	for plan.Next() {
		var id, parent, unused int
		var detail string
		if err := plan.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(detail, "SEARCH e USING INDEX") {
			search = true
		}
		if strings.Contains(detail, "SCAN e") {
			t.Fatal("scans source history", detail)
		}
	}
	if !search {
		t.Fatal("no indexed source seek")
	}
}

func TestProjectionBatchInFlightFollowupRestartAndEmptyScopes(t *testing.T) {
	ctx, reader, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", batchProjectionArgs("event_totals"))
	runProjectionWorker(t, a, ctx)
	ins := mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 3}, map[string]any{"centre_id": "b", "value": 4}}})
	id := ins["ids"].([]int64)[0]
	p := loadBatch(t, a, ctx, "event_totals")
	_, due, _ := batchWindow(t, ctx, p.ID)
	drainBatchMappings(t, a, ctx)
	item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Change both old/new scopes during calculation, and introduce a new scope.
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"centre_id": "c", "value": 10}})
	_, _, next := batchWindow(t, ctx, p.ID)
	if next == 0 {
		t.Fatal("no follow-up window")
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "c", "value": 2}}})
	_, _, nextAgain := batchWindow(t, ctx, p.ID)
	if next != nextAgain {
		t.Fatal("follow-up traffic extended deadline")
	}
	drainBatchMappings(t, a, ctx)
	if err := publishProjectionRows(ctx, p, item, map[string][]map[string]any{`{"centre_id":"a"}`: {{"centre_id": "a", "total": float64(3)}}, `{"centre_id":"b"}`: {{"centre_id": "b", "total": float64(4)}}}); err != nil {
		t.Fatal(err)
	}
	got := projectionRows(t, a, ctx)
	if len(got) != 2 || got[0]["centre_id"] != "a" {
		t.Fatal("previous complete batch unavailable", got)
	}
	_, actualNext, _ := batchWindow(t, ctx, p.ID)
	if actualNext != next {
		t.Fatal("publication reset follow-up deadline", next, actualNext)
	}
	// Reopen the actual SQLite file, rather than just resetting the App object.
	var seq int
	var dbname, path string
	if err := ctx.AppDB().QueryRow(`PRAGMA database_list`).Scan(&seq, &dbname, &path); err != nil {
		t.Fatal(err)
	}
	a.closeProjectionReader()
	reader.Close()
	ctx.AppDB().Close()
	writer, err := sql.Open("sqlite", "file:"+path+"?_pragma=journal_mode(WAL)&_pragma=foreign_keys(on)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	writer.SetMaxOpenConns(1)
	manifest := a.Manifest()
	ctx = sdk.NewAppCtxForTest(&manifest, writer, sdk.Config{"max_query_ms": "5000"}, nil, nil).WithProject("test-proj")
	a = &App{}
	defer a.closeProjectionReader()
	drainBatchMappings(t, a, ctx)
	if _, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", next-1); err != nil || ok {
		t.Fatal("restart lost window", ok, err)
	}
	item, ok, err = claimProjectionQueueAt(t.Context(), ctx, "test-proj", next)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	got = projectionRows(t, a, ctx)
	if len(got) != 2 || got[0]["centre_id"] != "b" || got[1]["centre_id"] != "c" || got[1]["total"] != float64(12) {
		t.Fatal("lost old/new invalidation or empty scope", got)
	}
	if s := projectionStatusFor(t, a, ctx, map[string]any{"name": p.Name}); s["ready"] != true {
		t.Fatal("follow-up never became ready", s)
	}
}

func TestProjectionBatchForcePauseRetryAndValidation(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	for _, edit := range []map[string]any{{"refresh_mode": "unknown"}, {"min_refresh_interval_seconds": 0}, {"batch_scope_sql": "SELECT centre_id,SUM(value) AS total FROM {events} GROUP BY centre_id"}, {"scope_columns": []any{}}, {"batch_scope_sql": "DELETE FROM {events} WHERE centre_id=?"}} {
		args := batchProjectionArgs("bad")
		for k, v := range edit {
			args[k] = v
		}
		if _, err := callTool(a, ctx, "projections_create", args); err == nil {
			t.Fatal("invalid batching accepted", edit)
		}
	}
	mustCall(t, a, ctx, "projections_create", batchProjectionArgs("event_totals"))
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}}})
	p := loadBatch(t, a, ctx, "event_totals")
	drainBatchMappings(t, a, ctx)
	mustCall(t, a, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": true})
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "scope": map[string]any{"centre_id": "a"}, "force": true})
	if _, ok, err := claimProjectionQueue(t.Context(), ctx, "test-proj"); err != nil || ok {
		t.Fatal("force bypassed pause", ok, err)
	}
	mustCall(t, a, ctx, "projections_pause", map[string]any{"name": p.Name, "paused": false})
	item, ok, err := claimProjectionQueue(t.Context(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal("force did not bypass window", ok, err)
	}
	if err := failProjectionQueueAt(t.Context(), ctx, item, fmt.Errorf("injected failure"), time.Now().UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := claimProjectionQueue(t.Context(), ctx, "test-proj"); err != nil || ok {
		t.Fatal("tight retry", ok, err)
	}
	item, ok, err = claimProjectionQueueAt(t.Context(), ctx, "test-proj", time.Now().Add(2*time.Second).UnixMilli())
	if err != nil || !ok {
		t.Fatal("retry did not recover", ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	if _, err := callTool(a, ctx, "projections_update", map[string]any{"name": p.Name, "min_refresh_interval_seconds": 0}); err == nil {
		t.Fatal("zero batch interval accepted")
	}
	// A malicious batch SQL can never replace unrelated scope heads.
	if _, err := ctx.AppDB().Exec(`UPDATE projection_definitions SET options=json_set(options,'$.batch_scope_sql','SELECT centre_id,SUM(value) AS total FROM {events},json_each(?) GROUP BY centre_id') WHERE id=?`, p.ID); err != nil {
		t.Fatal(err)
	}
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": p.Name, "scope": map[string]any{"centre_id": "other"}, "force": true})
	item, ok, err = claimProjectionQueue(t.Context(), ctx, "test-proj")
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err == nil {
		t.Fatal("out-of-scope result accepted")
	}
	if got := projectionRows(t, a, ctx); len(got) != 1 || got[0]["total"] != float64(1) {
		t.Fatal("failed batch changed published rows", got)
	}
}

func TestProjectionBatchFairScheduling42Projections(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	for i := 0; i < 42; i++ {
		mustCall(t, a, ctx, "projections_create", batchProjectionArgs(fmt.Sprintf("stats_%02d", i)))
	}
	for i := 0; i < 30; i++ {
		runProjectionWorker(t, a, ctx)
		var count int
		ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_definitions WHERE built=0`).Scan(&count)
		if count == 0 {
			break
		}
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}}})
	drainBatchMappings(t, a, ctx)
	now := time.Now().Add(61 * time.Second).UnixMilli()
	seen := map[int64]bool{}
	for i := 0; i < 42; i++ {
		item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", now)
		if err != nil || !ok {
			t.Fatal("projection starved", i, ok, err)
		}
		if seen[item.ProjectionID] {
			t.Fatal("busy projection overtook waiting projections")
		}
		seen[item.ProjectionID] = true
		if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
			t.Fatal(err)
		}
		// Make this already served projection immediately eligible again.
		p, err := loadProjectionWhere(ctx, `WHERE id=?`, item.ProjectionID)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.queueProjectionScope(ctx, p, `{"centre_id":"a"}`, item.PendingID, true); err != nil {
			t.Fatal(err)
		}
	}
	if len(seen) != 42 {
		t.Fatal("fair scheduler did not service all projections")
	}
}

func TestProjectionBatchMixedReadersSeeCompleteGeneration(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "test-proj")
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	args := batchProjectionArgs("event_totals")
	args["publication_batch_rows"] = 8
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	rows := make([]any, 400)
	for i := range rows {
		rows[i] = map[string]any{"centre_id": fmt.Sprintf("c%02d", i%40), "value": 1}
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": rows})
	drainBatchMappings(t, a, ctx)
	p := loadBatch(t, a, ctx, "event_totals")
	_, due, _ := batchWindow(t, ctx, p.ID)
	item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	var wg sync.WaitGroup
	failures := make(chan error, 8)
	start := make(chan struct{})
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for j := 0; j < 15; j++ {
				r, err := callTool(a, ctx, "tables_query", map[string]any{"sql": "SELECT COUNT(*) AS n,SUM(total) AS total FROM {event_totals}", "_request_context": context.Background()})
				if err != nil {
					failures <- err
					return
				}
				row := r.(map[string]any)["rows"].([]map[string]any)[0]
				if row["n"] != int64(0) && !(row["n"] == int64(40) && row["total"] == float64(400)) {
					failures <- fmt.Errorf("partial batch: %v", row)
					return
				}
			}
		}()
	}
	close(start)
	if err := a.runProjectionRefresh(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatal(err)
	}
	if m := a.workerMetrics("test-proj"); m.RefreshJobs != 2 {
		t.Fatal("scope loop replaced grouped SQL")
	}
}

func TestProjectionBatchFixedCutoffDoesNotFollowContinuousTraffic(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "indexes_create", map[string]any{"table": "events", "name": "by_centre", "columns": []any{"centre_id"}})
	mustCall(t, a, ctx, "projections_create", batchProjectionArgs("event_totals"))
	runProjectionWorker(t, a, ctx)
	for j := 0; j < 3; j++ {
		rows := make([]any, 500)
		for i := range rows {
			rows[i] = map[string]any{"centre_id": fmt.Sprintf("c%04d", j*500+i), "value": 1}
		}
		mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": rows})
	}
	p := loadBatch(t, a, ctx, "event_totals")
	_, due, _ := batchWindow(t, ctx, p.ID)
	if err := a.consumeProjectionChanges(t.Context(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due); err != nil || ok {
		t.Fatal("partial invalidations were claimed", ok, err)
	}
	var cutoff int64
	if err := ctx.AppDB().QueryRow(`SELECT batch_cutoff_id FROM projection_queue WHERE projection_id=? AND scope_key='__batch__'`, p.ID).Scan(&cutoff); err != nil {
		t.Fatal(err)
	}
	// New events must not move the cutoff while dependency mapping catches up.
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "later", "value": 7}}})
	_, _, next := batchWindow(t, ctx, p.ID)
	if next == 0 {
		t.Fatal("capture after cutoff lost follow-up")
	}
	for i := 0; i < 3; i++ {
		if err := a.consumeProjectionChanges(t.Context(), ctx, "test-proj"); err != nil {
			t.Fatal(err)
		}
	}
	item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok || item.PendingID != cutoff {
		t.Fatal("deadline followed latest traffic", item.PendingID, cutoff, ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	status := projectionStatusFor(t, a, ctx, map[string]any{"name": p.Name})
	if status["pending_scopes"] != 1 || status["ready"] != false {
		t.Fatal("publication swallowed later changes", status)
	}
	item, ok, err = claimProjectionQueueAt(t.Context(), ctx, "test-proj", next)
	if err != nil || !ok {
		t.Fatal("follow-up not eligible", ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	if s := projectionStatusFor(t, a, ctx, map[string]any{"name": p.Name}); s["ready"] != true {
		t.Fatal("continuous batch never caught up", s)
	}
}

func TestProjectionBatchWatchedMetadataDoesNotStartWindow(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "tables_alter", map[string]any{"name": "events", "add": map[string]any{"name": "metadata", "type": "text"}})
	args := batchProjectionArgs("event_totals")
	args["source_dependencies"] = []any{map[string]any{"table": "events", "watched_columns": []any{"centre_id", "value"}}}
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	r := mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": "a", "value": 1}}})
	id := r["ids"].([]int64)[0]
	drainBatchMappings(t, a, ctx)
	mustCall(t, a, ctx, "projections_refresh", map[string]any{"name": "event_totals", "force": true})
	runProjectionWorker(t, a, ctx)
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"metadata": "new"}})
	if s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"}); s["batch_deadline_at"] != nil || s["ready"] != true {
		t.Fatal("metadata started a refresh", s)
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "events", "id": id, "fields": map[string]any{"value": 2}})
	if s := projectionStatusFor(t, a, ctx, map[string]any{"name": "event_totals"}); s["batch_deadline_at"] == nil || s["ready"] != false {
		t.Fatal("same-scope value change missed", s)
	}
}

func TestProjectionBatchJoinedAggregateAndHeadCleanup(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	defer a.closeProjectionReader()
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "tables_create", map[string]any{"name": "weights", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "factor", "type": "number"}}})
	for _, table := range []string{"events", "weights"} {
		mustCall(t, a, ctx, "indexes_create", map[string]any{"table": table, "name": "by_centre", "columns": []any{"centre_id"}})
	}
	full := `WITH weighted AS MATERIALIZED (SELECT e.centre_id,e.value*COALESCE(w.factor,1) AS amount FROM {events} e LEFT JOIN {weights} w ON w.centre_id=e.centre_id) SELECT centre_id,SUM(amount) AS total FROM weighted GROUP BY centre_id`
	batch := `WITH dirty AS MATERIALIZED (SELECT json_extract(value,'$.centre_id') AS centre_id FROM json_each(?)), weighted AS MATERIALIZED (SELECT e.centre_id,e.value*COALESCE(w.factor,1) AS amount FROM dirty d JOIN {events} e ON e.centre_id IS d.centre_id LEFT JOIN {weights} w ON w.centre_id=e.centre_id) SELECT centre_id,SUM(amount) AS total FROM weighted GROUP BY centre_id`
	args := batchProjectionArgs("event_totals")
	args["sql"] = full
	args["batch_scope_sql"] = batch
	args["source_tables"] = []any{"events", "weights"}
	args["publication_batch_rows"] = 8
	mustCall(t, a, ctx, "projections_create", args)
	runProjectionWorker(t, a, ctx)
	events := make([]any, 600)
	for i := range events {
		events[i] = map[string]any{"centre_id": fmt.Sprintf("c%02d", i%40), "value": i}
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": events})
	weights := make([]any, 40)
	for i := range weights {
		weights[i] = map[string]any{"centre_id": fmt.Sprintf("c%02d", i), "factor": i + 1}
	}
	ids := mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "weights", "rows": weights})["ids"].([]int64)
	drainBatchMappings(t, a, ctx)
	p := loadBatch(t, a, ctx, "event_totals")
	_, due, _ := batchWindow(t, ctx, p.ID)
	item, ok, err := claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	// Failure of the atomic ready-outbox write must roll back every head switch.
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER fail_batch_ready BEFORE INSERT ON projection_event_outbox BEGIN SELECT RAISE(ABORT,'injected ready failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err == nil {
		t.Fatal("publication failure ignored")
	}
	if got := projectionRows(t, a, ctx); len(got) != 0 {
		t.Fatal("failed batch exposed staged rows", got)
	}
	var before, after int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_staged_heads`).Scan(&before)
	if removed, err := cleanupProjectionBatchHeads(t.Context(), ctx, ctx.AppReadDB(), p); err != nil || removed {
		t.Fatal("active staging lease was reclaimed", removed, err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_queue SET claimed_until=datetime('now','-1 second') WHERE projection_id=? AND scope_key='__batch__'`, p.ID); err != nil {
		t.Fatal(err)
	}
	if removed, err := cleanupProjectionBatchHeads(t.Context(), ctx, ctx.AppReadDB(), p); err != nil || !removed {
		t.Fatal("abandoned metadata not reclaimed", removed, err)
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_staged_heads`).Scan(&after)
	if before-after != 8 {
		t.Fatal("cleanup exceeded batch bound", before, after)
	}
	if _, err := ctx.AppDB().Exec(`DROP TRIGGER fail_batch_ready`); err != nil {
		t.Fatal(err)
	}
	item, ok, err = claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok {
		t.Fatal(ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	want := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": full + " ORDER BY centre_id"})["rows"]
	if !reflect.DeepEqual(projectionRows(t, a, ctx), want) {
		t.Fatal("joined aggregate parity failed")
	}
	mustCall(t, a, ctx, "rows_update", map[string]any{"table": "weights", "id": ids[0], "fields": map[string]any{"factor": 100}})
	drainBatchMappings(t, a, ctx)
	_, due, _ = batchWindow(t, ctx, p.ID)
	item, ok, err = claimProjectionQueueAt(t.Context(), ctx, "test-proj", due)
	if err != nil || !ok || len(item.CoveredScopes) != 1 {
		t.Fatal("joined dependency did not target one scope", ok, err)
	}
	if err := a.refreshProjectionScope(t.Context(), ctx, item); err != nil {
		t.Fatal(err)
	}
	want = mustCall(t, a, ctx, "tables_query", map[string]any{"sql": full + " ORDER BY centre_id"})["rows"]
	if !reflect.DeepEqual(projectionRows(t, a, ctx), want) {
		t.Fatal("dependency aggregate parity failed")
	}
}

func BenchmarkProjectionBatchScopeCalculation(b *testing.B) {
	ctx := benchmarkCtx(b).WithProject("bench")
	a := &App{}
	b.Cleanup(a.closeProjectionReader)
	benchmarkCall(b, a, ctx, "tables_create", map[string]any{"name": "events", "columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "value", "type": "number"}}})
	benchmarkCall(b, a, ctx, "indexes_create", map[string]any{"table": "events", "name": "by_centre", "columns": []any{"centre_id"}})
	rows := make([]any, 1000)
	for i := range rows {
		rows[i] = map[string]any{"centre_id": fmt.Sprintf("c%03d", i%100), "value": i}
	}
	benchmarkCall(b, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": rows})
	p := &projectionDefinition{ProjectID: "bench", Name: "bench", Options: defaultProjectionOptions(ctx), ResultCols: []Column{{Name: "centre_id", Type: "text"}, {Name: "total", Type: "number"}}}
	all := projectionQueueItem{CoveredScopes: map[string]projectionQueuedRevision{}}
	p.ScopeCols = []string{"centre_id"}
	for i := 0; i < 100; i++ {
		all.CoveredScopes[fmt.Sprintf(`{"centre_id":"c%03d"}`, i)] = projectionQueuedRevision{}
	}
	together, err := projectionBatchBindings(p, all)
	if err != nil {
		b.Fatal(err)
	}
	singles := make([]string, 0, 100)
	for key := range all.CoveredScopes {
		item := projectionQueueItem{CoveredScopes: map[string]projectionQueuedRevision{key: {}}}
		arg, err := projectionBatchBindings(p, item)
		if err != nil {
			b.Fatal(err)
		}
		singles = append(singles, arg)
	}
	for _, grouped := range []bool{false, true} {
		b.Run(map[bool]string{false: "100_individual_scope_calculations", true: "one_grouped_calculation"}[grouped], func(b *testing.B) {
			inputs := singles
			if grouped {
				inputs = []string{together}
			}
			b.ReportMetric(float64(len(inputs)), "calculations/op")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				for _, arg := range inputs {
					if _, _, err := a.readProjectionRows(context.Background(), ctx, p, batchTotalsSQL, []any{arg}, true); err != nil {
						b.Fatal(err)
					}
				}
			}
		})
	}
}

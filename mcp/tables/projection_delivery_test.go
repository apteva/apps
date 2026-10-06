package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestTablesCapacityReservesInteractiveAndProjectionSlots(t *testing.T) {
	ctx := newTestCtx(t, tk.WithConfig(map[string]string{
		"max_read_conns": "2", "max_projection_workers": "1", "max_total_concurrency": "3", "max_projection_queue_ms": "25",
	}))
	a := &App{}
	read1, _, err := a.acquireCapacity(context.Background(), ctx, interactiveCapacityKind)
	if err != nil {
		t.Fatal(err)
	}
	defer read1()
	read2, _, err := a.acquireCapacity(context.Background(), ctx, interactiveCapacityKind)
	if err != nil {
		t.Fatal(err)
	}
	defer read2()
	projection, _, err := a.acquireCapacity(context.Background(), ctx, projectionCapacityKind)
	if err != nil {
		t.Fatal(err)
	}
	defer projection()
	if _, _, err := a.acquireCapacity(context.Background(), ctx, projectionCapacityKind); err == nil {
		t.Fatal("projection capacity unexpectedly exceeded its reserved worker slot")
	}
}

func TestProjectionMixedWorkloadKeepsReadsAndInvalidationsBounded(t *testing.T) {
	ctx, _, _ := newFileBackedTestCtx(t, "mixed")
	ctx.Config()["max_read_conns"] = "2"
	ctx.Config()["max_projection_workers"] = "2"
	ctx.Config()["max_total_concurrency"] = "4"
	ctx.Config()["max_projection_queue_ms"] = "500"
	a := &App{}
	t.Cleanup(a.closeProjectionReader)
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", map[string]any{
		"name": "mixed_stats", "version": 1,
		"sql":           "SELECT centre_id,COUNT(*) AS total FROM {events} GROUP BY centre_id",
		"source_tables": []any{"events"}, "scope_columns": []any{"centre_id"},
		"result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}},
	})
	if err := a.projectionWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	seed := make([]any, 900)
	for i := range seed {
		seed[i] = map[string]any{"centre_id": fmt.Sprintf("centre-%d", i%4), "value": i}
	}
	if _, err := callTool(a, ctx, "rows_insert", map[string]any{"table": "events", "rows": seed}); err != nil {
		t.Fatal(err)
	}
	readErrs := make(chan error, 16)
	var reads sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		reads.Add(1)
		go func(worker int) {
			defer reads.Done()
			for i := 0; i < 8; i++ {
				sql := "SELECT COUNT(*) AS n FROM {events}"
				if worker%2 == 0 {
					// Exercise the large-list path while the projection worker is
					// calculating and publishing generations.
					sql = "SELECT centre_id,value FROM {events} ORDER BY id LIMIT 900"
				}
				_, err := callTool(a, ctx, "tables_query", map[string]any{"sql": sql})
				if err != nil {
					readErrs <- fmt.Errorf("read worker %d: %w", worker, err)
					return
				}
			}
		}(worker)
	}
	workerCtx, stopWorker := context.WithCancel(context.Background())
	var workerWG sync.WaitGroup
	workerWG.Add(1)
	go func() {
		defer workerWG.Done()
		for {
			select {
			case <-workerCtx.Done():
				return
			default:
			}
			_ = a.projectionWorker(workerCtx, ctx)
			time.Sleep(3 * time.Millisecond)
		}
	}()
	for i := 0; i < 200; i++ {
		if _, err := callTool(a, ctx, "rows_insert", map[string]any{"table": "events", "rows": []any{map[string]any{"centre_id": fmt.Sprintf("centre-%d", i%4), "value": i}}}); err != nil {
			t.Fatal(err)
		}
	}
	stopWorker()
	workerWG.Wait()
	reads.Wait()
	close(readErrs)
	for err := range readErrs {
		t.Fatal(err)
	}
	for i := 0; i < 100; i++ {
		if err := a.projectionWorker(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
		var pending int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='mixed_stats') AND (claimed_until IS NULL OR claimed_until<=CURRENT_TIMESTAMP)`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		if pending == 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	var pending int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_queue WHERE projection_id=(SELECT id FROM projection_definitions WHERE name='mixed_stats')`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 {
		t.Fatalf("projection left invalidations pending: %d", pending)
	}
	status := mustCall(t, a, ctx, "projections_status", map[string]any{"name": "mixed_stats"})
	if status["ready"] != true {
		t.Fatalf("mixed projection not ready: %#v", status)
	}
	if timings, ok := status["phase_timings_ms"].(map[string]any); !ok || timings["read_queue"] == nil || timings["write_lock"] == nil || timings["staging"] == nil {
		t.Fatalf("phase timings missing capacity/publication phases: %#v", status["phase_timings_ms"])
	}
	rows := mustCall(t, a, ctx, "tables_query", map[string]any{"sql": "SELECT centre_id,total FROM {mixed_stats}"})["rows"].([]map[string]any)
	if len(rows) != 4 {
		t.Fatalf("mixed projection rows=%d, want four affected scopes", len(rows))
	}
}

func TestProjectionQueueWaitUsesDurableMilliseconds(t *testing.T) {
	ctx := newTestCtx(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	p, err := a.loadProjection(ctx, "test-proj", "event_totals")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := ctx.AppDB().Exec(`UPDATE projection_queue SET due_at_ms=?,queued_at_ms=?,claimed_until=NULL WHERE projection_id=? AND scope_key=?`, now, now-2500, p.ID, projectionAllScope); err != nil {
		t.Fatal(err)
	}
	item, ok, err := claimProjectionQueueAt(context.Background(), ctx, "test-proj", now)
	if err != nil || !ok {
		t.Fatalf("claim: item=%+v ok=%v err=%v", item, ok, err)
	}
	if item.QueuedAtMs != now-2500 || item.QueueWaitMs != 2500 {
		t.Fatalf("queue timing = queued_at=%d wait=%d, want %d and 2500", item.QueuedAtMs, item.QueueWaitMs, now-2500)
	}
}

func TestProjectionWorkerBatchesScopesAndCoalescesReadyEvents(t *testing.T) {
	ctx, recorder := newTestCtxWithRecorder(t)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	mustCall(t, a, ctx, "projections_create", map[string]any{
		"name": "scoped", "version": 1,
		"sql":           "SELECT centre_id,COUNT(*) AS total FROM {events} GROUP BY centre_id",
		"source_tables": []any{"events"}, "scope_columns": []any{"centre_id"},
		"result_columns": []any{map[string]any{"name": "centre_id", "type": "text"}, map[string]any{"name": "total", "type": "number"}},
	})
	runProjectionWorker(t, a, ctx)
	rows := make([]any, 20)
	for i := range rows {
		rows[i] = map[string]any{"centre_id": "centre-" + string(rune('a'+i)), "value": 1}
	}
	mustCall(t, a, ctx, "rows_insert", map[string]any{"table": "events", "rows": rows})
	if err := a.consumeProjectionChanges(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	recorder.Reset()
	runProjectionWorker(t, a, ctx)
	ready := recorder.EventsByTopic(topicProjectionReady)
	if len(ready) != 1 {
		t.Fatalf("ready events=%d, want one coalesced event", len(ready))
	}
	payload := ready[0].Data.(map[string]any)
	if payload["scope_count"] != 20 {
		t.Fatalf("coalesced payload=%v", payload)
	}
	if len(payload["scope_keys"].([]string)) != 20 {
		t.Fatalf("scope_keys=%v", payload["scope_keys"])
	}
}

type flakyProjectionEmitter struct {
	mu       sync.Mutex
	failures int
	events   []any
}

func (e *flakyProjectionEmitter) EmitWithProject(topic, projectID string, data any) {}
func (e *flakyProjectionEmitter) EmitWithProjectAck(_ context.Context, _ string, _ string, data any) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.failures > 0 {
		e.failures--
		return errors.New("temporary event gateway failure")
	}
	e.events = append(e.events, data)
	return nil
}

func TestProjectionReadyOutboxRetriesAfterDeliveryFailure(t *testing.T) {
	ctx := newTestCtx(t)
	emitter := &flakyProjectionEmitter{failures: 1}
	ctx.SetEmitter(emitter)
	a := &App{}
	projectionSourceTable(t, a, ctx)
	createProjection(t, a, ctx)
	runProjectionWorker(t, a, ctx)
	var pending, attempts int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(MAX(attempts),0) FROM projection_event_outbox`).Scan(&pending, &attempts); err != nil {
		t.Fatal(err)
	}
	if pending != 1 || attempts != 1 {
		t.Fatalf("failed delivery outbox=%d attempts=%d", pending, attempts)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE projection_event_outbox SET next_attempt_ms=0`); err != nil {
		t.Fatal(err)
	}
	runProjectionWorker(t, a, ctx)
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM projection_event_outbox`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	if pending != 0 || len(emitter.events) != 1 {
		t.Fatalf("retry did not recover pending=%d events=%d", pending, len(emitter.events))
	}
}

func TestProjectionReadyCoalesceKeepsNewestWatermark(t *testing.T) {
	dst := map[string]any{"watermark": float64(12), "generation": "old", "published_at": "2026-10-06T10:00:00Z"}
	mergeProjectionReadyMetadata(dst, map[string]any{"watermark": float64(15), "generation": "new", "published_at": "2026-10-06T10:01:00Z"})
	if dst["watermark"] != float64(15) || dst["generation"] != "new" {
		t.Fatalf("coalesced metadata=%v", dst)
	}
	mergeProjectionReadyMetadata(dst, map[string]any{"watermark": float64(14), "generation": "stale", "published_at": "2026-10-06T10:02:00Z"})
	if dst["generation"] != "new" {
		t.Fatalf("stale metadata overwrote newest publication: %v", dst)
	}
}

var _ sdk.Emitter = (*flakyProjectionEmitter)(nil)

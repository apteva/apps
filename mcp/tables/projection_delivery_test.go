package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

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

package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestDurableWaitHoldReleasesAndRecoversWithoutReplay(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	actor := fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID)
	args := map[string]any{"state": "waiting", "output": "accepted-operation-123; inspect before any retry", "error": "missing provider capability", "wait": map[string]any{"kind": "hold"}}
	raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", args)
	if err != nil {
		t.Fatal(err)
	}
	ack := raw.(map[string]any)
	if ack["done"] != true {
		t.Fatal("hold retained its worker", ack)
	}
	saved := stepBy(t, a, r, s.Key)
	if saved.State != "blocked" || !saved.DeliverySuspended || saved.RetainWorker || saved.Output != args["output"] || saved.Error != args["error"] {
		t.Fatalf("checkpoint lost: %+v", saved)
	}
	retiring, err := a.workerRetiring(r.ID, s.Executor.AgentID, s.ThreadID)
	if err != nil || !retiring {
		t.Fatal("done lacked durable retirement", err)
	}
	// A lost acknowledgement is replayable, while late writes cannot reopen work.
	if _, err = a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", args); err != nil {
		t.Fatal("checkpoint retry", err)
	}
	if _, err = a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "running"}); err == nil {
		t.Fatal("retiring worker resumed itself")
	}
	before := len(f.events)
	a = &App{ctx: a.ctx, db: a.db}
	if err = a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if err = a.tickDirect(context.Background(), time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != 1 || len(f.events) != before {
		t.Fatal("hold replayed or leaked", f.kills)
	}
	fresh, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || fresh.State != "blocked" || !fresh.DeliverySuspended {
		t.Fatal("parent not parked", fresh, err)
	}
	if _, err = a.stepAction(p.ProjectID, "operator", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "running"}); err != nil {
		t.Fatal(err)
	}
	resumed := stepBy(t, a, r, s.Key)
	if resumed.ThreadID == s.ThreadID || resumed.Output != saved.Output || resumed.Wait != nil {
		t.Fatal("recovery lost checkpoint or reused worker", resumed)
	}
}

func TestDurableWaitTimerSurvivesRestartAndCleanupFailure(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	actor := "agent:7:" + s.ThreadID
	deadline := time.Now().UTC().Add(time.Hour).Truncate(time.Second).Add(250 * time.Millisecond)
	args := map[string]any{"state": "waiting", "output": "message-already-sent-123; continue with the reply check", "error": "follow up at saved deadline", "wait": map[string]any{"kind": "timer", "wake_at": deadline.Format(time.RFC3339Nano)}}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", args); err != nil {
		t.Fatal(err)
	}
	next, err := a.nextRunWake(context.Background(), p.ProjectID, time.Now())
	if err != nil || !next.Equal(deadline) {
		t.Fatal("dispatcher lost exact timer", next, deadline, err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	// Simulate a restart after checkpoint commit but before retirement reservation.
	if _, err = a.db.Exec(`DELETE FROM process_worker_cleanup WHERE run_id=? AND reason='idle'`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err = a.tickDirect(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if stepBy(t, a, r, s.Key).State != "blocked" || len(f.threads) != 1 {
		t.Fatal("timer bypassed missing retirement reservation")
	}
	f.killError = errors.New("ack lost")
	if err = a.cleanupWorkers(context.Background(), time.Now()); err == nil {
		t.Fatal("expected kill failure")
	}
	if err = a.tickDirect(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 1 {
		t.Fatal("timer raced unacknowledged retirement")
	}
	f.killError = nil
	if err = a.cleanupWorkers(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if err = a.tickDirect(context.Background(), deadline.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 1 {
		t.Fatal("early timer dispatch")
	}
	if err = a.tickDirect(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	resumed := stepBy(t, a, r, s.Key)
	if len(f.threads) != 2 || resumed.ThreadID == s.ThreadID || resumed.Output != args["output"] || resumed.Wait != nil || resumed.DeliverySuspended {
		t.Fatal("timer failed continuation", resumed)
	}
	before := len(f.events)
	if err = a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before {
		t.Fatal("timer redelivered")
	}
	// Old generation's runtime callback cannot block the new continuation.
	if err = a.stepLifecycle(sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 7}, &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ThreadID: s.ThreadID, Sequence: 9, Type: "event.error", Reason: "late"}); err != nil {
		t.Fatal(err)
	}
	if stepBy(t, a, r, s.Key).DeliverySuspended {
		t.Fatal("old callback suspended resumed timer")
	}
}

func TestDurableWaitPreservesApprovalsCallbacksAndNativeTimers(t *testing.T) {
	for _, kind := range []string{"approval", "callback", "thread_timer"} {
		t.Run(kind, func(t *testing.T) {
			a, f, p, r := sequentialSetup(t)
			s := stepBy(t, a, r, "research")
			wait := map[string]any{"kind": kind, "reference": "existing-record-123"}
			if kind == "thread_timer" {
				wait["wake_at"] = time.Now().Add(time.Hour).UTC().Format(time.RFC3339Nano)
			}
			raw, err := a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "dependency pending", "output": "existing receipt", "wait": wait})
			if err != nil {
				t.Fatal(err)
			}
			if raw.(map[string]any)["done"] != false {
				t.Fatal("live dependency released")
			}
			if err = a.cleanupWorkers(context.Background(), time.Now().Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			a = &App{ctx: a.ctx, db: a.db}
			before := len(f.events)
			if err = a.tickDirect(context.Background(), time.Now().Add(48*time.Hour)); err != nil {
				t.Fatal(err)
			}
			if len(f.kills) != 0 || len(f.events) != before || stepBy(t, a, r, s.Key).Wait.Kind != kind {
				t.Fatal("dependency expired due to age")
			}
			// A provider error must not discard a genuine approval/callback/timer.
			saved := stepBy(t, a, r, s.Key)
			if err = a.holdWorkerExecutionError(saved, "provider quota"); err != nil {
				t.Fatal(err)
			}
			if err = a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
				t.Fatal("runtime failure lost dependency", err)
			}
		})
	}
}

func TestDurableWaitLegacyRevalidationIsBoundedAndFailClosed(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	if _, err := a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "output": "existing business receipt", "error": "unknown legacy wait"}); err != nil {
		t.Fatal(err)
	}
	before := len(f.events)
	now := time.Now().Add(11 * time.Minute)
	f.lose = true
	if err := a.tickDirect(context.Background(), now); err == nil {
		t.Fatal("lost revalidation receipt ignored")
	}
	if len(f.kills) != 0 {
		t.Fatal("unknown wait killed")
	}
	id := f.events[len(f.events)-1].SourceEventID
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 {
		t.Fatal("retried before persisted backoff")
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if f.events[len(f.events)-1].SourceEventID != id || !strings.HasPrefix(id, "process-wait:") {
		t.Fatal("revalidation duplicated identity")
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), now.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+2 {
		t.Fatal("legacy wait polled after acknowledgement")
	}
	if err := a.cleanupWorkers(context.Background(), now); err != nil || len(f.kills) != 0 {
		t.Fatal("unknown timeout treated as permission", err)
	}
	// Revalidation lets the old worker explicitly surrender a stale wait.
	if _, err := a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "blocked", "output": "existing business receipt", "error": "callback already settled; manual repair required", "wait": map[string]any{"kind": "hold"}}); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), now); err != nil || len(f.kills) != 1 {
		t.Fatal("revalidated stale worker retained", err)
	}
}

func TestDurableWaitValidationDoesNotMutateCheckpoint(t *testing.T) {
	for _, wait := range []any{nil, map[string]any{"kind": "typo"}, map[string]any{"kind": "approval"}, map[string]any{"kind": "hold", "wake_at": "tomorrow"}, map[string]any{"kind": "timer", "wake_at": "2020-01-01T00:00:00Z"}, map[string]any{"kind": "callback", "reference": "x", "unknown": true}} {
		t.Run(fmt.Sprint(wait), func(t *testing.T) {
			a, _, p, r := sequentialSetup(t)
			s := stepBy(t, a, r, "research")
			_, err := a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "test", "output": "saved evidence", "wait": wait})
			if err == nil {
				t.Fatal("invalid wait accepted")
			}
			if fresh := stepBy(t, a, r, s.Key); fresh.Revision != s.Revision || fresh.Wait != nil {
				t.Fatal("invalid wait partially saved")
			}
		})
	}
}

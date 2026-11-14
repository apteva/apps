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

func legacyWaitBatch(t *testing.T, agents []int64) (*App, *directPlatform, *Process, []StepRun) {
	t.Helper()
	a, f, _ := directSetup(t)
	a.ctx.Config()["max_workers_per_agent"] = "100"
	d := workflowDefinition()
	d.Steps = d.Steps[:1]
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	var out []StepRun
	for i, agent := range agents {
		x := addAssignment(t, a, p, fmt.Sprintf("batch-%d", i), agent, "agent", nil, nil)
		x = enableAssignment(t, a, p, x)
		r := startOn(t, a, p, x, fmt.Sprintf("batch-%d", i), nil)
		s := stepBy(t, a, r, "research")
		if _, err := a.stepAction(p.ProjectID, fmt.Sprintf("agent:%d:%s", agent, s.ThreadID), p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "verify original dependency before recovery", "output": "accepted-operation-" + s.ID}); err != nil {
			t.Fatal(err)
		}
		if _, err := a.db.Exec(`UPDATE process_step_runs SET updated_at=? WHERE id=?`, time.Now().Add(-24*time.Hour).Add(time.Duration(i)*time.Second).UTC().Format(time.RFC3339Nano), s.ID); err != nil {
			t.Fatal(err)
		}
		s = stepBy(t, a, r, "research")
		out = append(out, s)
	}
	return a, f, p, out
}

func TestWaitBatchStoppedOwnerDoesNotBlockHealthyOwner(t *testing.T) {
	a, f, p, steps := legacyWaitBatch(t, []int64{7, 8})
	f.instanceStatuses = map[int64]string{7: "stopped"}
	before := len(f.events)
	now := time.Now().UTC()
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 || f.events[before].AgentID != 8 {
		t.Fatal("stopped owner woke or blocked healthy owner", f.events[before:])
	}
	var acknowledged int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_wait_checks`).Scan(&acknowledged); err != nil || acknowledged != 1 {
		t.Fatal("stopped wait falsely acknowledged", acknowledged, err)
	}
	var retry string
	if err := a.db.QueryRow(`SELECT next_attempt_at FROM process_wait_retries WHERE step_id=?`, steps[0].ID).Scan(&retry); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 || len(f.kills) != 0 {
		t.Fatal("deferred wait replayed or killed")
	}
	f.instanceStatuses[7] = "running"
	at, _ := time.Parse(time.RFC3339Nano, retry)
	if err := a.tickDirect(context.Background(), at); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+2 || f.events[len(f.events)-1].ThreadID != steps[0].ThreadID {
		t.Fatal("restarted owner not revalidated")
	}
	// Checkpoint a verified stale hold through the real API, then normal cleanup.
	r, err := a.getRun(p.ProjectID, p.ID, steps[1].RunID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = a.stepAction(p.ProjectID, "agent:8:"+steps[1].ThreadID, p.ID, r.ID, steps[1].ID, "step_update", map[string]any{"state": "blocked", "error": "original dependency settled; operator repair needed", "output": steps[1].Output, "wait": map[string]any{"kind": "hold"}}); err != nil {
		t.Fatal(err)
	}
	if err = a.cleanupWorkers(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(f.kills) != 1 || f.kills[0].ThreadID != steps[1].ThreadID {
		t.Fatal("did not release only the checkpointed stale worker", f.kills)
	}
}

func TestWaitBatchFailedDeliveryContinuesAndRetriesSameIdentity(t *testing.T) {
	a, f, p, steps := legacyWaitBatch(t, []int64{7, 8})
	f.eventErrors = map[string]error{steps[0].ThreadID: errors.New("HTTP 502: owner unavailable")}
	before := len(f.events)
	now := time.Now().UTC()
	if err := a.tickDirect(context.Background(), now); err == nil {
		t.Fatal("delivery failure hidden")
	}
	if len(f.events) != before+2 || f.events[before+1].ThreadID != steps[1].ThreadID {
		t.Fatal("failure aborted rest of batch")
	}
	id := f.events[before].SourceEventID
	next, err := a.nextRunWake(context.Background(), p.ProjectID, now)
	if err != nil || !next.Equal(now.Add(time.Minute)) {
		t.Fatal("retry deadline lost", next, err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	delete(f.eventErrors, steps[0].ThreadID)
	if err = a.tickDirect(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+2 {
		t.Fatal("lost response retried before backoff")
	}
	if err = a.tickDirect(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+3 || f.events[before+2].SourceEventID != id {
		t.Fatal("retry changed identity or repeated healthy delivery")
	}
	var left int
	if err = a.db.QueryRow(`SELECT count(*) FROM process_wait_retries`).Scan(&left); err != nil || left != 0 {
		t.Fatal("successful retry not cleared", err)
	}
}

func TestWaitBatchUnavailableFirstPageCannotStarveLaterPage(t *testing.T) {
	agents := make([]int64, 11)
	for i := range agents {
		agents[i] = 7
	}
	agents[10] = 8
	a, f, p, _ := legacyWaitBatch(t, agents)
	f.instanceStatuses = map[int64]string{7: "stopped"}
	before := len(f.events)
	now := time.Now().UTC()
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before {
		t.Fatal("woke unavailable first page")
	}
	next, err := a.nextRunWake(context.Background(), p.ProjectID, now)
	if err != nil || !next.Equal(now.Add(2*time.Second)) {
		t.Fatal("later page waits for old failures", next, err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err = a.tickDirect(context.Background(), next); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 || f.events[before].AgentID != 8 {
		t.Fatal("healthy next page starved")
	}
}

func TestWaitBatchDeliveryDoesNotHoldActionMutex(t *testing.T) {
	a, f, _, _ := legacyWaitBatch(t, []int64{7})
	locked := false
	f.afterTracked = func(_ sdk.AgentEventRequest) {
		if a.mu.TryLock() {
			a.mu.Unlock()
		} else {
			locked = true
		}
	}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if locked {
		t.Fatal("a slow revalidation blocked worker checkpoint updates")
	}
}

func TestWaitBatchRevisionChangeDiscardsOldFailureBackoff(t *testing.T) {
	a, f, _, steps := legacyWaitBatch(t, []int64{7})
	f.eventErrors = map[string]error{steps[0].ThreadID: errors.New("temporary failure")}
	now := time.Now()
	before := len(f.events)
	if err := a.tickDirect(context.Background(), now); err == nil {
		t.Fatal("failure hidden")
	}
	// A new saved checkpoint invalidates the previous revision's deferred request.
	if _, err := a.db.Exec(`UPDATE process_step_runs SET revision=revision+1 WHERE id=?`, steps[0].ID); err != nil {
		t.Fatal(err)
	}
	delete(f.eventErrors, steps[0].ThreadID)
	if err := a.tickDirect(context.Background(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+2 || f.events[before].SourceEventID == f.events[before+1].SourceEventID || !strings.HasPrefix(f.events[before+1].SourceEventID, "process-wait:") {
		t.Fatal("new checkpoint inherited stale retry")
	}
}

func TestWaitBatchUnavailableMiddleOwnerDoesNotSkipFollowingOwner(t *testing.T) {
	a, f, _, steps := legacyWaitBatch(t, []int64{7, 8, 10})
	f.eventErrors = map[string]error{steps[1].ThreadID: errors.New("HTTP 502")}
	before := len(f.events)
	if err := a.tickDirect(context.Background(), time.Now()); err == nil {
		t.Fatal("failure hidden")
	}
	if len(f.events) != before+3 || f.events[before+2].ThreadID != steps[2].ThreadID {
		t.Fatal("middle failure skipped later owner")
	}
	var checks int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_wait_checks`).Scan(&checks); err != nil || checks != 2 {
		t.Fatal("failed request acknowledged or healthy request skipped", checks, err)
	}
}

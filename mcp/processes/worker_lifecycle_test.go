package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestRuntimeHoldUpdatesParentWithoutDispatch(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	first := stepBy(t, a, r, "research")
	before := len(f.events)
	event := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: first.Executor.AgentID}
	lifecycle := &sdk.AgentEventLifecycle{SourceEventID: first.DeliveryEventID, ThreadID: first.ThreadID, Sequence: 1, Type: "event.error", Reason: "provider_retry_budget_exhausted"}
	if err := a.stepLifecycle(event, lifecycle); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || fresh.State != "blocked" || !fresh.DeliverySuspended || len(f.events) != before {
		t.Fatalf("runtime failure did not update parent without dispatch: %+v %v", fresh, err)
	}
	// Repair a row written by older releases without replaying its step.
	if _, err := a.db.Exec(`UPDATE process_runs SET state='running' WHERE id=?`, r.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	fresh, err = a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || fresh.State != "blocked" || len(f.events) != before {
		t.Fatalf("persisted stale status not repaired: %+v %v", fresh, err)
	}
}

func TestRetiredWorkerResumesAfterDelayWithNewIdentity(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:2]
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "minutes"}
	a, f, p, r := executorSetup(t, d, "per_executor", nil)
	first := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, first.Key, "agent:7:"+first.ThreadID, "accepted-operation-123", "")
	retiring, err := a.workerRetiring(r.ID, first.Executor.AgentID, first.ThreadID)
	if err != nil || !retiring {
		t.Fatalf("done response did not durably reserve retirement: %t %v", retiring, err)
	}
	delayed := stepBy(t, a, r, "write")
	deadline, err := time.Parse(time.RFC3339Nano, delayed.StartAt)
	if err != nil {
		t.Fatal(err)
	}
	f.killError = errors.New("retirement acknowledgement unavailable")
	if err := a.cleanupWorkers(context.Background(), time.Now()); err == nil {
		t.Fatal("lost retirement acknowledgement was ignored")
	}
	if err := a.tickDirect(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 1 {
		t.Fatal("continued onto a worker still being retired")
	}
	f.killError = nil
	if err := a.cleanupWorkers(context.Background(), time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), deadline); err != nil {
		t.Fatal(err)
	}
	next := stepBy(t, a, r, "write")
	if len(f.threads) != 2 || next.ThreadID == first.ThreadID || next.DeliveredAt == "" || next.StartAt != delayed.StartAt {
		t.Fatalf("continuation lost its new identity or timer: %+v", next)
	}
	if got := stepBy(t, a, r, "research"); got.Output != "accepted-operation-123" {
		t.Fatal("retirement changed the completed receipt")
	}
}

func TestBusinessWaitRetainsApprovalOwner(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	actor := fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID)
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "approval callback is addressed to this thread", "output": "approval-id-123"}); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
		t.Fatalf("approval owner was retired: %+v %v", f.kills, err)
	}
}

func TestSharedWorkerAdmissionQueuesAcrossRunsAndAssignments(t *testing.T) {
	a, f, p, first := executorSetup(t, workflowDefinition(), "per_executor", nil)
	a.ctx.Config()["max_workers_per_agent"] = "2"
	x := addAssignment(t, a, p, "Other", 7, "agent", nil, nil)
	cfg := x.AssignmentConfig
	cfg.WorkerContinuity = "per_executor"
	x, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, cfg)
	if err != nil {
		t.Fatal(err)
	}
	x = enableAssignment(t, a, p, x)
	startOn(t, a, p, x, "second", nil)
	raw, err := a.start(p.ProjectID, p.ID, "queued", "")
	if err != nil {
		t.Fatal(err)
	}
	queued := raw.(map[string]any)["run"].(Run)
	fresh, err := a.getRun(p.ProjectID, p.ID, queued.ID)
	if err != nil || fresh.State != "queued" || len(f.threads) != 2 {
		t.Fatalf("excess run was spawned or blocked: %+v %v", fresh, err)
	}
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, first.ID, "release capacity"); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE run_id=?`, queued.ID); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 3 {
		t.Fatal("queued run did not use released capacity")
	}
}

func TestRuntimeHoldRetirementAndExplicitRecoveryKeepsEvidence(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	if err := a.writeStep(s, "running", 40, "accepted-operation-456", "", "agent:7:"+s.ThreadID); err != nil {
		t.Fatal(err)
	}
	ev := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 7}
	l := &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ThreadID: s.ThreadID, Sequence: 1, Type: "event.error", Reason: "provider retries exhausted"}
	if err := a.stepLifecycle(ev, l); err != nil {
		t.Fatal(err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := a.stepAction(p.ProjectID, "operator", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "running"}); err != nil {
		t.Fatal(err)
	}
	fresh := stepBy(t, a, r, "research")
	if fresh.ThreadID == s.ThreadID || fresh.Output != "accepted-operation-456" || len(f.threads) != 2 {
		t.Fatalf("explicit recovery lost evidence or reused old thread: %+v", fresh)
	}
	l.Sequence = 2
	if err := a.stepLifecycle(ev, l); err != nil {
		t.Fatal(err)
	}
	after := stepBy(t, a, r, "research")
	if after.DeliverySuspended || after.ThreadID != fresh.ThreadID || after.Output != fresh.Output {
		t.Fatal("retired worker callback altered the new continuation")
	}
}

func TestDelegatingWorkerRetainsOwnershipDuringDurableDelay(t *testing.T) {
	a, f, p, r, actor := parallelSetup(t, 2, func(d *Definition) {
		for i := 1; i < len(d.Steps); i++ {
			d.Steps[i].StartAfter = &TimingRule{After: "run_start", Offset: 1, Unit: "days"}
		}
	})
	if _, err := parallelAction(a, p, r, actor, "inventory", "step_claim", nil); err != nil {
		t.Fatal(err)
	}
	finishStep(t, a, p, r, "inventory", actor, "durable receipt", "")
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
		t.Fatalf("delegated children could be interrupted: %v", err)
	}
	var slots int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_worker_slots WHERE run_id=?`, r.ID).Scan(&slots); err != nil || slots != 1 {
		t.Fatalf("delegating owner's reservation was lost: %d %v", slots, err)
	}
}

func TestSingleStepRouterCompletionPreservesIndependentHandlingApproval(t *testing.T) {
	a, f, handling, child := sequentialSetup(t)
	held := stepBy(t, a, child, "research")
	if _, err := a.stepAction(handling.ProjectID, "agent:7:"+held.ThreadID, handling.ID, child.ID, held.ID, "step_update", map[string]any{"state": "waiting", "error": "approval callback pending", "output": "draft-123 task-456"}); err != nil {
		t.Fatal(err)
	}
	d := workflowDefinition()
	d.Name = "Route inbound event"
	d.Steps = d.Steps[:1]
	router := create(t, a, d)
	router = status(t, a, router.ID, "active")
	raw, err := a.start(router.ProjectID, router.ID, "inbound-event-123", "")
	if err != nil {
		t.Fatal(err)
	}
	parent := raw.(map[string]any)["run"].(Run)
	step := stepBy(t, a, parent, "research")
	finishStep(t, a, router, parent, "research", "agent:7:"+step.ThreadID, "Durable handling run: "+child.ID, "")
	done, err := a.getRun(router.ProjectID, router.ID, parent.ID)
	if err != nil || done.State != "completed" {
		t.Fatalf("router retained downstream work: %+v %v", done, err)
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	after := stepBy(t, a, child, "research")
	if after.State != "waiting" || after.ThreadID != held.ThreadID || after.Output != "draft-123 task-456" {
		t.Fatal("handoff changed handling approval")
	}
	for _, kill := range f.kills {
		if kill.ThreadID == held.ThreadID {
			t.Fatal("router cleanup killed handling worker")
		}
	}
}

func TestRuntimeErrorPreservesExistingApprovalCallbackOwner(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	actor := "agent:7:" + s.ThreadID
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "waiting", "error": "await original task callback", "output": "approval-task-123"}); err != nil {
		t.Fatal(err)
	}
	ev := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 7}
	lifecycle := &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ThreadID: s.ThreadID, Sequence: 1, Type: "event.error", Reason: "provider retries exhausted while waiting"}
	if err := a.stepLifecycle(ev, lifecycle); err != nil {
		t.Fatal(err)
	}
	after := stepBy(t, a, r, "research")
	if !after.RetainWorker || !workerNeedsContinuation(after) {
		t.Fatal("runtime error lost callback affinity")
	}
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil || len(f.kills) != 0 {
		t.Fatalf("approval callback owner killed: %v", err)
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "running", "output": "approval-task-123 resolved"}); err != nil {
		t.Fatal(err)
	}
	resumed := stepBy(t, a, r, "research")
	if resumed.DeliverySuspended || resumed.RetainWorker || resumed.ThreadID != s.ThreadID {
		t.Fatal("original callback could not resume its owner")
	}
}

func TestWorkerRetirementUpgradeRecoversApprovalAffinityFromHistory(t *testing.T) {
	a, _, _, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	if err := a.writeStep(s, "waiting", 10, "approval-task-123", "approval pending", "agent:7:"+s.ThreadID); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	if err := a.holdWorkerExecutionError(s, "provider retries exhausted"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET retain_worker=0 WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/025_worker_retirement.sql")
	if err != nil {
		t.Fatal(err)
	}
	// Replay the upgrade's data backfill against a row shaped like an older runtime hold.
	backfill := string(migration)[strings.Index(string(migration), "UPDATE process_step_runs SET retain_worker=1"):]
	if _, err = a.db.Exec(backfill); err != nil {
		t.Fatal(err)
	}
	recovered := stepBy(t, a, r, "research")
	if !recovered.RetainWorker || !workerNeedsContinuation(recovered) {
		t.Fatal("upgrade would retire original approval owner")
	}
}

func cleanupWorkersNow(t *testing.T, a *App) {
	t.Helper()
	if err := a.cleanupWorkers(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
}

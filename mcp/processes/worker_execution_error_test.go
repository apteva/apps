package main

import (
	sdk "github.com/apteva/app-sdk"
	"testing"
	"time"
)

func TestWorkerExecutionErrorHoldsWithoutReminderOrReplay(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	// The business receipt must survive a later runtime failure.
	if err := a.writeStep(s, "running", 45, "accepted external operation: immutable-123", "", "agent:8:worker"); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	source := s.DeliveryEventID
	if source == "" {
		source = "process-step:" + s.ID
	}
	event := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 8}
	lifecycle := &sdk.AgentEventLifecycle{SourceEventID: source, Sequence: 1, Type: "event.error", ExecutionID: "failed-execution", ThreadID: s.ThreadID, Reason: "provider_retry_budget_exhausted: context deadline exceeded"}
	if err := a.stepLifecycle(event, lifecycle); err != nil {
		t.Fatal(err)
	}
	held := stepBy(t, a, r, "research")
	if held.State != "blocked" || !held.DeliverySuspended || held.Progress != 45 || held.Output != "accepted external operation: immutable-123" {
		t.Fatalf("lost hold/evidence: %+v", held)
	}
	if held.Error == "" {
		t.Fatal("missing runtime reason")
	}
	count := len(f.events)
	all, err := a.steps(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err = a.staleStepReminder(p, r, held, all, time.Now().Add(24*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != count {
		t.Fatal("runtime error was reminded/replayed")
	}
	// A later settled notification is transport bookkeeping, not permission
	// to clear the business hold.
	lifecycle.Sequence = 2
	lifecycle.Type = "event.processed"
	if err = a.stepLifecycle(event, lifecycle); err != nil {
		t.Fatal(err)
	}
	held = stepBy(t, a, r, "research")
	if held.State != "blocked" || !held.DeliverySuspended {
		t.Fatal("settlement removed explicit hold")
	}
}

func TestLateWorkerErrorPreservesCompletedBusinessStep(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if err := a.writeStep(s, "completed", 100, "verified success", "", "agent:8:worker"); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "research")
	source := s.DeliveryEventID
	if source == "" {
		source = "process-step:" + s.ID
	}
	event := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 8}
	l := &sdk.AgentEventLifecycle{SourceEventID: source, Sequence: 1, Type: "event.error", ExecutionID: "late", ThreadID: s.ThreadID}
	if err := a.stepLifecycle(event, l); err != nil {
		t.Fatal(err)
	}
	got := stepBy(t, a, r, "research")
	if got.State != "completed" || got.Output != "verified success" || got.DeliverySuspended {
		t.Fatalf("late error changed success: %+v", got)
	}
}

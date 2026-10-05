package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestUnclaimedStepRecoveryUsesClaimEvidenceAndBoundedWakes(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	// Settlement is delivery evidence only; it must not mark a claim.
	if err := a.stepLifecycle(sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: s.Executor.AgentID}, &sdk.AgentEventLifecycle{SourceEventID: s.DeliveryEventID, ThreadID: s.ThreadID, Sequence: 1, Type: "settled"}); err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, s.Key)
	delivered, _ := time.Parse(time.RFC3339Nano, s.DeliveredAt)
	now := delivered.Add(unclaimedStepAfter + time.Second)
	for attempt := 1; attempt <= maxClaimRecoveryAttempts; attempt++ {
		if err := a.tickDirect(context.Background(), now); err != nil {
			t.Fatal(err)
		}
		current := stepBy(t, a, r, s.Key)
		if current.ClaimedAt != "" || current.ClaimAttempts != attempt {
			t.Fatalf("settlement became claim: %+v", current)
		}
		wake := f.events[len(f.events)-1]
		if wake.ThreadID != s.ThreadID || !strings.Contains(wake.Message.(string), "takes precedence") {
			t.Fatalf("bad recovery handoff: %+v", wake)
		}
		if err := a.stepLifecycle(sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: s.Executor.AgentID}, &sdk.AgentEventLifecycle{SourceEventID: current.DeliveryEventID, ThreadID: s.ThreadID, Sequence: uint64(attempt + 1), Type: "settled"}); err != nil {
			t.Fatal(err)
		}
		now = now.Add(unclaimedStepCooldown + time.Second)
	}
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	current := stepBy(t, a, r, s.Key)
	if !current.DeliverySuspended || !strings.Contains(current.DeliveryWarning, "Run stalled") {
		t.Fatalf("missing bounded stall: %+v", current)
	}
	// A late valid claim is accepted and clears the recovery state.
	actor := fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID)
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_claim", nil); err != nil {
		t.Fatal(err)
	}
	current = stepBy(t, a, r, s.Key)
	if current.ClaimedAt == "" || current.State != "running" || current.DeliverySuspended || current.DeliveryWarning != "" {
		t.Fatalf("late claim did not recover: %+v", current)
	}
}

func TestUnclaimedRecoveryLeavesBusyParallelBranchesForWorkerChoice(t *testing.T) {
	a, f, p, r, actor := parallelSetup(t, 2)
	if _, err := parallelAction(a, p, r, actor, "inventory", "step_claim", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := parallelAction(a, p, r, actor, "inventory", "step_update", map[string]any{"state": "completed", "output": "source"}); err != nil {
		t.Fatal(err)
	}
	s := stepBy(t, a, r, "portrait_3")
	all, _ := a.steps(r.ID)
	delivered, _ := time.Parse(time.RFC3339Nano, s.DeliveredAt)
	if err := a.recoverUnclaimedStep(p, r, s, all, delivered.Add(unclaimedStepAfter+time.Second)); err != nil {
		t.Fatal(err)
	}
	if stepBy(t, a, r, s.Key).ClaimAttempts != 0 || len(f.events) == 0 {
		t.Fatal("parallel choice was incorrectly recovered")
	}
}

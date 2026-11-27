package main

import (
	"strconv"
	"testing"
)

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// Any agent in the project can stop a single-agent run through run_cancel,
// not only its owner through run_update.
func TestRunCancelStopsSingleAgentRunForAnyProjectAgent(t *testing.T) {
	a, _, p := directSetup(t)
	raw, err := a.start("project-a", p.ID, "cancel-me", "input")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	owner := p.OwnerAgentID
	other := owner + 50
	if _, err := a.execute("project-a", "agent:"+itoa(other)+":main", "run_cancel", map[string]any{"process_id": p.ID, "run_id": r.ID}); err == nil {
		t.Fatal("cancel without a reason was accepted")
	}
	if _, err := a.execute("other-project", "agent:"+itoa(other)+":main", "run_cancel", map[string]any{"process_id": p.ID, "run_id": r.ID, "reason": "stop"}); err == nil {
		t.Fatal("agent from another project cancelled the run")
	}
	if _, err := a.execute("project-a", "agent:"+itoa(other)+":main", "run_cancel", map[string]any{"process_id": p.ID, "run_id": r.ID, "reason": "user asked to stop"}); err != nil {
		t.Fatal(err)
	}
	got, err := a.getRun("project-a", p.ID, r.ID)
	if err != nil || got.State != "cancelled" || got.Error != "user asked to stop" {
		t.Fatalf("run not cancelled: %+v err=%v", got, err)
	}
	// Retrying the same cancellation is a no-op, not an error.
	if _, err := a.execute("project-a", "agent:"+itoa(owner)+":main", "run_cancel", map[string]any{"process_id": p.ID, "run_id": r.ID, "reason": "user asked to stop"}); err != nil {
		t.Fatalf("idempotent cancel failed: %v", err)
	}
}

package main

import (
	"context"
	"strings"
	"testing"
	"time"
)

func sequentialSetup(t *testing.T) (*App, *directPlatform, *Process, Run) {
	t.Helper()
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	d.DefaultInputs = "Standing Barcelona context"
	d.CompletionCriteria = "Save all receipts"
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "sequential", "Use Celsius today")
	if err != nil {
		t.Fatal(err)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}

func TestRunWorkerClaimsReuseAcrossGenericSteps(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	actor := "agent:7:" + stepBy(t, a, r, "research").ThreadID
	claim := func(key string) map[string]any {
		t.Helper()
		s := stepBy(t, a, r, key)
		raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_claim", nil)
		if err != nil {
			t.Fatal(err)
		}
		return raw.(map[string]any)
	}
	first := stepBy(t, a, r, "research")
	future := stepBy(t, a, r, "write")
	for _, candidate := range []string{"operator", "agent:8:wrong", "agent:7:main"} {
		if _, err := a.stepAction(p.ProjectID, candidate, p.ID, r.ID, first.ID, "step_claim", nil); err == nil {
			t.Fatal("unauthorized claim", candidate)
		}
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, future.ID, "step_claim", nil); err == nil {
		t.Fatal("claimed pending work")
	}
	result := claim("research")
	if result["default_inputs"] != "Standing Barcelona context" || result["inputs"] != "Use Celsius today" || result["completion_criteria"] != "Save all receipts" || result["assignment"].(AssignmentConfig).OwnerAgentID != r.Binding.OwnerAgentID {
		t.Fatal("claim lost execution context", result)
	}
	if result["step"].(StepRun).State != "running" || result["worker"].(map[string]any)["done"] != false {
		t.Fatal("claim did not mark running", result)
	}
	if _, err := a.stepAction(p.ProjectID, "agent:7:competitor", p.ID, r.ID, first.ID, "step_claim", nil); err == nil {
		t.Fatal("worker ownership stolen")
	}
	claim("research") // lost claim response is safe to retry
	finishStep(t, a, p, r, "research", actor, "research", "")
	if len(f.events) != 2 || f.events[1].ThreadID != strings.TrimPrefix(actor, "agent:7:") {
		t.Fatal("next step returned to coordinator", f.events)
	}
	if strings.Contains(f.events[1].Message.(string), "Shared procedure") || len(f.events[1].Message.(string)) > 800 {
		t.Fatal("repeated full context")
	}
	// Recreate the app receiver against the same storage, as after a sidecar restart.
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 {
		t.Fatal("restart duplicated delivery")
	}
	claim("write")
	finishStep(t, a, p, r, "write", actor, "draft", "")
	review := stepBy(t, a, r, "review")
	publish := stepBy(t, a, r, "publish")
	if review.State != "ready" || publish.State != "pending" || len(f.events) != 3 {
		t.Fatal("review step not delivered generically")
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, publish.ID, "step_claim", nil); err == nil {
		t.Fatal("claimed before review")
	}
	claim("review")
	finishStep(t, a, p, r, "review", actor, "reviewed", "")
	if len(f.events) != 4 || f.events[3].ThreadID != strings.TrimPrefix(actor, "agent:7:") {
		t.Fatal("review lost run worker")
	}
	claim("publish")
	raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, publish.ID, "step_update", map[string]any{"state": "completed", "output": "receipt"})
	if err != nil {
		t.Fatal(err)
	}
	if raw.(map[string]any)["done"] != true || raw.(map[string]any)["worker"].(map[string]any)["done"] != true || !strings.Contains(raw.(map[string]any)["next_action"].(string), "done tool immediately") {
		t.Fatal("worker not released on completion")
	}
	before := len(f.events)
	claim("publish")
	if len(f.events) != before {
		t.Fatal("terminal retry dispatched work")
	}
}

func TestWorkerRecoversCanonicalProcessAndIdentifiesStep(t *testing.T) {
	a, _, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	worker := s.ThreadID
	result, err := a.execute(p.ProjectID, "agent:7:"+worker, "step_get", map[string]any{
		"process_id": "process-run-" + r.ID + "-worker", // common worker-name guess
		"run_id":     r.ID,
		"step_id":    s.ID,
	})
	if err != nil {
		t.Fatalf("worker identity recovery failed: %v", err)
	}
	out := result.(map[string]any)
	if out["process_id"] != p.ID || out["run_id"] != r.ID || out["step_id"] != s.ID {
		t.Fatalf("worker result did not identify canonical records: %#v", out)
	}
	run := out["run"].(map[string]any)
	if run["process_id"] != p.ID {
		t.Fatalf("run result omitted canonical process id: %#v", run)
	}
}

func TestStaleSequentialStepRemindsSameWorker(t *testing.T) {
	a, f, p, r := sequentialSetup(t)
	s := stepBy(t, a, r, "research")
	actor := "agent:7:" + s.ThreadID
	if _, err := a.execute(p.ProjectID, actor, "step_claim", map[string]any{"run_id": r.ID, "step_id": s.ID}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := a.db.Exec(`UPDATE process_step_runs SET updated_at=?,next_attempt_at='' WHERE id=?`, now.Add(-staleStepReminderAfter-time.Second).Format(time.RFC3339Nano), s.ID); err != nil {
		t.Fatal(err)
	}
	before := len(f.events)
	if err := a.tickDirect(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 {
		t.Fatalf("stale step did not send one reminder: before=%d after=%d", before, len(f.events))
	}
	reminder := f.events[len(f.events)-1]
	if reminder.ThreadID != s.ThreadID || !strings.Contains(reminder.Message.(string), "process_id="+p.ID) || !strings.Contains(reminder.Message.(string), "run_id="+r.ID) || !strings.Contains(reminder.Message.(string), "step_id="+s.ID) {
		t.Fatalf("reminder lost worker identity: %#v", reminder)
	}
	if err := a.tickDirect(context.Background(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before+1 {
		t.Fatal("stale reminder was not throttled")
	}
}

func TestRunWorkerEligibilityPreservesIndependentExecution(t *testing.T) {
	a, _, _, r := sequentialSetup(t)
	all, err := a.steps(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if sequentialAgent(r, all) != 7 {
		t.Fatal("sequential run not eligible")
	}
	for _, kind := range []string{"branch", "other-agent", "tasks"} {
		t.Run(kind, func(t *testing.T) {
			steps := append([]StepRun(nil), all...)
			run := r
			switch kind {
			case "branch":
				steps[1].Definition.DependsOn = nil
			case "other-agent":
				steps[1].Executor.AgentID = 8
			case "tasks":
				run.Backend = "tasks"
			}
			if sequentialAgent(run, steps) != 0 {
				t.Fatal("independent execution collapsed")
			}
		})
	}
}

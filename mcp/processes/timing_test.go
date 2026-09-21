package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func timedSetup(t *testing.T, human bool) (*App, *directPlatform, *Process, Run) {
	t.Helper()
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	d.Steps = d.Steps[:2]
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 10, Unit: "minutes"}
	d.Steps[1].DueAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 30, Unit: "minutes"}
	if human {
		d.Steps[1].Kind = "approval"
	}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "timed", "")
	if err != nil {
		t.Fatal(err)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}
func TestTimedStepPersistsAndDispatchesAtBoundary(t *testing.T) {
	a, f, p, r := timedSetup(t, false)
	if len(f.events) != 1 || strings.Contains(f.events[0].Message.(string), "persistent worker") {
		t.Fatal("timed workflow retained worker")
	}
	before := stepBy(t, a, r, "write")
	if before.StartAt != "" || before.DueAt != "" {
		t.Fatal("unresolved anchor received timer")
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "First email sent", "")
	first := stepBy(t, a, r, "research")
	step := stepBy(t, a, r, "write")
	completed, err := time.Parse(time.RFC3339Nano, first.CompletedAt)
	if err != nil {
		t.Fatal(err)
	}
	start, _ := time.Parse(time.RFC3339Nano, step.StartAt)
	due, _ := time.Parse(time.RFC3339Nano, step.DueAt)
	if step.State != "scheduled" || start.Sub(completed) != 10*time.Minute || due.Sub(completed) != 30*time.Minute || len(f.events) != 1 {
		t.Fatalf("incorrect timer: %+v", step)
	}
	for _, state := range []string{"running", "completed"} {
		if _, err := a.stepAction(p.ProjectID, "agent:7:t", p.ID, r.ID, step.ID, "step_update", map[string]any{"state": state, "output": "too early"}); err == nil {
			t.Fatal("early step update allowed")
		}
	}
	restarted := &App{ctx: a.ctx, db: a.db}
	if err := restarted.tickDirect(context.Background(), start.Add(-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 {
		t.Fatal("early notification")
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "First email sent", "")
	if stepBy(t, a, r, "write").StartAt != step.StartAt {
		t.Fatal("duplicate completion moved timer")
	}
	if err := restarted.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || !strings.HasPrefix(f.events[1].SourceEventID, "process-step:"+step.ID+":assignment:") {
		t.Fatalf("incorrect wake-up: %+v", f.events)
	}
	if err := restarted.tickDirect(context.Background(), start.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 {
		t.Fatal("duplicate notification")
	}
	if stepBy(t, a, r, "write").StartAt != step.StartAt {
		t.Fatal("restart reset timer")
	}
}
func TestTimedHumanApproval(t *testing.T) {
	a, f, p, r := timedSetup(t, true)
	finishStep(t, a, p, r, "research", "agent:7:t", "sent", "")
	step := stepBy(t, a, r, "write")
	start, _ := time.Parse(time.RFC3339Nano, step.StartAt)
	if _, err := a.stepAction(p.ProjectID, "operator", p.ID, r.ID, step.ID, "step_update", map[string]any{"state": "completed", "output": "early approval", "decision": "approved"}); err == nil {
		t.Fatal("early human approval")
	}
	var approvals int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_event_outbox WHERE topic='task.approval_requested' AND json_extract(payload_json,'$.task_id')=?`, step.ID).Scan(&approvals); err != nil {
		t.Fatal(err)
	}
	if approvals != 0 {
		t.Fatal("approval requested before timer")
	}
	if err := a.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if stepBy(t, a, r, "write").State != "waiting" || len(f.events) != 1 {
		t.Fatal("timed human step not released correctly")
	}
}
func TestTimingValidation(t *testing.T) {
	for _, rule := range []TimingRule{
		{After: "step_completed", StepKey: "write", Offset: 10, Unit: "minutes"},
		{After: "step_completed", StepKey: "missing", Offset: 10, Unit: "minutes"},
		{After: "run_start", StepKey: "research", Offset: 10, Unit: "minutes"},
		{After: "run_start", Offset: -1, Unit: "minutes"},
		{After: "run_start", Offset: 366, Unit: "days"},
		{After: "run_start", Offset: 1, Unit: "seconds"},
		{After: "unknown", Offset: 1, Unit: "hours"},
	} {
		d := workflowDefinition()
		d.Steps[1].StartAfter = &rule
		if validateSteps(d.Steps) == nil {
			t.Fatalf("accepted %+v", rule)
		}
	}
	d := workflowDefinition()
	d.Steps[3].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "days"}
	if err := validateSteps(d.Steps); err != nil {
		t.Fatal("ancestor timing rejected", err)
	}
	d.Steps[3].DueAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "minutes"}
	if validateSteps(d.Steps) == nil {
		t.Fatal("deadline before start accepted")
	}
	var s Step
	if json.Unmarshal([]byte(`{"start_after":{"after":"run_start","offset":1.5,"unit":"minutes"}}`), &s) == nil {
		t.Fatal("fractional offset accepted")
	}
}
func TestRunRelativeDeadlineDoesNotDelayExecution(t *testing.T) {
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	d.Steps = d.Steps[:1]
	d.Steps[0].DueAfter = &TimingRule{After: "run_start", Offset: 0, Unit: "minutes"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "deadline", "")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	s := stepBy(t, a, r, "research")
	if len(f.events) != 1 || s.State != "ready" || s.DueAt != r.CreatedAt {
		t.Fatalf("deadline blocked execution: %+v", s)
	}
	overview, err := a.overview(p.ProjectID)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Counts.Attention != 1 || overview.Active[0].Steps[0].DueAt != s.DueAt {
		t.Fatal("overdue step missing from overview")
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "late but successful", "")
	if stepBy(t, a, r, "research").State != "completed" {
		t.Fatal("overdue completion rejected")
	}
}

func TestTimedRunCancellationStopsWakeup(t *testing.T) {
	a, f, p, r := timedSetup(t, false)
	finishStep(t, a, p, r, "research", "agent:7:t", "sent", "")
	s := stepBy(t, a, r, "write")
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "No follow-up"); err != nil {
		t.Fatal(err)
	}
	if err := a.tickDirect(context.Background(), start.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 1 || stepBy(t, a, r, "write").State != "cancelled" {
		t.Fatal("cancelled timer dispatched")
	}
}
func TestTimedDeliveryRetryKeepsIdentity(t *testing.T) {
	a, f, p, r := timedSetup(t, false)
	finishStep(t, a, p, r, "research", "agent:7:t", "sent", "")
	s := stepBy(t, a, r, "write")
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	f.lose = true
	if err := a.tickDirect(context.Background(), start); err == nil {
		t.Fatal("lost response not recorded")
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	restarted := &App{ctx: a.ctx, db: a.db}
	if err := restarted.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(f.threads) != 3 || f.threads[1].ThreadID != f.threads[2].ThreadID || len(f.events) != 2 || f.events[1].ThreadID != f.threads[2].ThreadID || !strings.HasPrefix(f.events[1].SourceEventID, "process-step:"+s.ID+":assignment:") {
		t.Fatalf("retry identity changed: threads=%+v events=%+v step=%+v", f.threads, f.events, s)
	}
}
func TestTimedParallelJoinWaitsForAllInputs(t *testing.T) {
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	d.Steps = d.Steps[:3]
	d.Steps[1].DependsOn = nil
	d.Steps[2].Kind = "work"
	d.Steps[2].DependsOn = []string{"research", "write"}
	d.Steps[2].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 10, Unit: "minutes"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "join", "")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	finishStep(t, a, p, r, "research", "agent:7:t", "first", "")
	s := stepBy(t, a, r, "review")
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	if err := a.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || stepBy(t, a, r, "review").State != "pending" {
		t.Fatal("timer bypassed second input")
	}
	finishStep(t, a, p, r, "write", "agent:7:t", "second", "")
	if err := a.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 3 {
		t.Fatal("join did not release")
	}
}
func TestTimedTasksBackend(t *testing.T) {
	t.Skip("legacy Tasks backend removed in Processes 0.14")
	a, f := setup(t)
	d := workflowDefinition()
	d.ExecutionMode = "tasks"
	d.Steps = d.Steps[:2]
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 10, Unit: "minutes"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "tasks-delay", "")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	if f.creates != 1 {
		t.Fatal("first task missing")
	}
	f.tasks["task-1"]["state"] = "completed"
	f.tasks["task-1"]["result"] = "first sent"
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	s := stepBy(t, a, r, "write")
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	if f.creates != 1 || s.State != "scheduled" {
		t.Fatal("Tasks handoff too early")
	}
	if err := a.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if f.creates != 2 {
		t.Fatal("delayed Tasks handoff missing")
	}
}

func TestTimedReassignmentCannotReleaseEarly(t *testing.T) {
	a, f, p, r := timedSetup(t, false)
	finishStep(t, a, p, r, "research", "agent:7:t", "sent", "")
	s := stepBy(t, a, r, "write")
	detail, err := a.taskDetails(p.ProjectID, "agent:7:t", s.ID)
	if err != nil || detail["can_update"] != false {
		t.Fatal("scheduled task actionable", err)
	}
	_, err = a.changeTask(p.ProjectID, "operator", s.ID, "task_update", map[string]any{"expected_revision": float64(s.Revision), "executor": map[string]any{"kind": "agent", "agent_id": float64(8)}})
	if err != nil {
		t.Fatal(err)
	}
	s = stepBy(t, a, r, "write")
	if s.State != "scheduled" || s.Executor.AgentID != 8 || len(f.events) != 1 {
		t.Fatal("reassignment bypassed timer")
	}
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	if err := a.tickDirect(context.Background(), start); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != 2 || f.events[1].AgentID != 8 {
		t.Fatal("timer ignored new assignee")
	}
}
func TestTimedRunKeepsOriginalProcedureRules(t *testing.T) {
	a, f, p, r := timedSetup(t, false)
	p = status(t, a, p.ID, "paused")
	d := p.Definition
	// Copy the slice and rule so the old test snapshot is not mutated.
	d.Steps = append([]Step(nil), d.Steps...)
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "minutes"}
	if _, err := a.save(p.ProjectID, p.ID, "operator", p.Version, d); err != nil {
		t.Fatal(err)
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "sent", "")
	s := stepBy(t, a, r, "write")
	first := stepBy(t, a, r, "research")
	start, _ := time.Parse(time.RFC3339Nano, s.StartAt)
	completed, _ := time.Parse(time.RFC3339Nano, first.CompletedAt)
	if start.Sub(completed) != 10*time.Minute || len(f.events) != 1 {
		t.Fatal("new version changed an existing timer")
	}
}

package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
	"time"
)

func workflowDefinition() Definition {
	d := def()
	d.ExecutionMode = "agent"
	d.Steps = []Step{
		{Key: "research", Name: "Research", Role: "researcher", Kind: "work", Instructions: "Research topic", ExpectedOutput: "Research notes"},
		{Key: "write", Name: "Write", Role: "writer", Kind: "work", Instructions: "Write draft", ExpectedOutput: "Draft text", DependsOn: []string{"research"}},
		{Key: "review", Name: "Review", Role: "reviewer", Kind: "approval", Instructions: "Review draft", ExpectedOutput: "Decision and reason", DependsOn: []string{"write"}},
		{Key: "publish", Name: "Publish", Role: "publisher", Kind: "work", Instructions: "Publish approved draft", ExpectedOutput: "Published URL", DependsOn: []string{"review"}},
	}
	return d
}
func workflowSetup(t *testing.T) (*App, *directPlatform, *Process, Run) {
	a, f, _ := directSetup(t)
	p := create(t, a, workflowDefinition())
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.Roles = map[string]Executor{"researcher": {Kind: "agent", AgentID: 8}, "writer": {Kind: "agent", AgentID: 7}, "reviewer": {Kind: "human"}, "publisher": {Kind: "agent", AgentID: 8}}
	if _, e := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "one", "")
	if e != nil {
		t.Fatal(e)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}
func stepBy(t *testing.T, a *App, r Run, key string) StepRun {
	t.Helper()
	all, e := a.steps(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, s := range all {
		if s.Key == key {
			return s
		}
	}
	t.Fatal("missing step", key)
	return StepRun{}
}
func finishStep(t *testing.T, a *App, p *Process, r Run, key, actor, output, decision string) {
	t.Helper()
	s := stepBy(t, a, r, key)
	args := map[string]any{"state": "completed", "output": output}
	if decision != "" {
		args["decision"] = decision
	}
	if _, e := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", args); e != nil {
		t.Fatal(e)
	}
}
func TestWorkflowHandoffsApprovalAndOutputs(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	if !r.Workflow || len(f.events) != 1 || f.events[0].AgentID != 8 {
		t.Fatal("first role not dispatched")
	}
	pending := stepBy(t, a, r, "publish")
	if _, e := a.stepAction(p.ProjectID, "agent:8:t", p.ID, r.ID, pending.ID, "step_update", map[string]any{"state": "completed", "output": "bypass"}); e == nil {
		t.Fatal("dependency bypass")
	}
	if _, e := a.directRun(p.ProjectID, "agent:7:t", p.ID, r.ID, "run_update", map[string]any{"state": "completed", "result": "bypass"}); e == nil {
		t.Fatal("coordinator bypass")
	}
	s := stepBy(t, a, r, "research")
	if _, e := a.stepAction(p.ProjectID, "agent:7:t", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "wrong role"}); e == nil {
		t.Fatal("wrong role updated")
	}
	finishStep(t, a, p, r, "research", "agent:8:t", "Research evidence", "")
	if len(f.events) != 2 || f.events[1].AgentID != 7 || !strings.Contains(f.events[1].Message.(string), "Research evidence") {
		t.Fatal("handoff input missing")
	}
	finishStep(t, a, p, r, "write", "agent:7:t", "Draft v1", "")
	if len(f.events) != 2 || stepBy(t, a, r, "review").State != "waiting" {
		t.Fatal("human review sent to agent")
	}
	review := stepBy(t, a, r, "review")
	if _, e := a.stepAction(p.ProjectID, "agent:7:t", p.ID, r.ID, review.ID, "step_update", map[string]any{"state": "completed", "output": "approved", "decision": "approved"}); e == nil {
		t.Fatal("agent impersonated human approver")
	}
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	if len(f.events) != 3 || f.events[2].AgentID != 8 || !strings.Contains(f.events[2].Message.(string), "Draft v1") {
		t.Fatal("approval did not release publisher")
	}
	// The draft used by approval cannot be edited after publication was released.
	write := stepBy(t, a, r, "write")
	if _, e := a.stepAction(p.ProjectID, "agent:7:t", p.ID, r.ID, write.ID, "step_update", map[string]any{"state": "completed", "output": "Changed draft"}); e == nil {
		t.Fatal("approved content mutable")
	}
	finishStep(t, a, p, r, "publish", "agent:8:t", "https://patreon.com/posts/123", "")
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "completed" || fresh.Progress != 100 || !strings.Contains(fresh.Result, "123") {
		t.Fatal("aggregate completion missing", fresh)
	}
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	if len(f.events) != 3 {
		t.Fatal("duplicate approval dispatched twice")
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM process_step_events WHERE step_id=? AND decision='approved'`, review.ID).Scan(&n)
	if n != 1 {
		t.Fatal("approval audit duplicated")
	}
}
func TestWorkflowRejectPreventsDownstream(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	finishStep(t, a, p, r, "research", "agent:8:t", "notes", "")
	finishStep(t, a, p, r, "write", "agent:7:t", "draft", "")
	finishStep(t, a, p, r, "review", "operator", "Needs changes", "rejected")
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "failed" || len(f.events) != 2 || stepBy(t, a, r, "publish").State != "pending" {
		t.Fatal("rejected run advanced")
	}
	a.tickDirect(context.Background(), time.Now())
	if len(f.events) != 2 {
		t.Fatal("worker restarted rejected run")
	}
}
func TestWorkflowParallelJoinAndValidation(t *testing.T) {
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	d.Steps = d.Steps[:3]
	d.Steps[1].DependsOn = nil
	d.Steps[2].DependsOn = []string{"research", "write"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "parallel", "")
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	if len(f.events) != 2 {
		t.Fatal("parallel steps not dispatched")
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "notes", "")
	if stepBy(t, a, r, "review").State != "pending" {
		t.Fatal("join released early")
	}
	finishStep(t, a, p, r, "write", "agent:7:t", "draft", "")
	if stepBy(t, a, r, "review").State != "waiting" {
		t.Fatal("join not released")
	}
	d.Steps[0].DependsOn = []string{"review"}
	if e = d.validate(); e == nil {
		t.Fatal("cycle accepted")
	}
	d.Steps[0].DependsOn = []string{"missing"}
	if e = d.validate(); e == nil {
		t.Fatal("unknown dependency accepted")
	}
}
func TestWorkflowTasksScheduleAndStepCompletion(t *testing.T) {
	a, f := setup(t)
	d := workflowDefinition()
	d.ExecutionMode = "tasks"
	d.Schedule = &Schedule{Kind: "interval", Every: "1m"}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	if p.SyncPending || f.creates != 0 {
		t.Fatal("workflow used Tasks as whole-run scheduler")
	}
	if e := a.tickDirect(context.Background(), time.Now().Add(2*time.Minute)); e != nil {
		t.Fatal(e)
	}
	records, _ := a.dispatches(p.ID)
	if len(records) != 1 || !records[0].Workflow || records[0].Backend != "tasks" || f.creates != 1 {
		t.Fatal("scheduled workflow not created")
	}
	r := records[0]
	f.tasks["task-1"]["state"] = "completed"
	f.tasks["task-1"]["result"] = "research from Tasks"
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if f.creates != 2 || !strings.Contains(f.tasks["task-2"]["description"].(string), "research from Tasks") {
		t.Fatal("Tasks handoff failed")
	}
	f.tasks["task-2"]["state"] = "completed"
	f.tasks["task-2"]["result"] = "draft"
	a.tickDirect(context.Background(), time.Now())
	if f.creates != 2 || stepBy(t, a, r, "review").State != "waiting" {
		t.Fatal("approval bypassed")
	}
	finishStep(t, a, p, r, "review", "operator", "approved", "approved")
	if f.creates != 3 {
		t.Fatal("publisher task missing")
	}
	f.tasks["task-3"]["state"] = "completed"
	f.tasks["task-3"]["result"] = "published URL"
	a.tickDirect(context.Background(), time.Now())
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "completed" {
		t.Fatal("Tasks workflow not complete", fresh)
	}
}
func TestWorkflowLostDeliveryAndFrozenRoles(t *testing.T) {
	a, f, _ := directSetup(t)
	d := workflowDefinition()
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	f.lose = true
	if _, e := a.start(p.ProjectID, p.ID, "lost", ""); e == nil {
		t.Fatal("expected lost response")
	}
	records, _ := a.dispatches(p.ID)
	r := records[0]
	s := stepBy(t, a, r, "research")
	a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID)
	restarted := &App{ctx: a.ctx, db: a.db}
	if e := restarted.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != 2 || f.events[0].SourceEventID != f.events[1].SourceEventID {
		t.Fatal("retry identity changed")
	}
	x, e := a.assignmentStatus(p.ProjectID, p.ID, r.AssignmentID, "paused")
	if e != nil {
		t.Fatal(e)
	}
	c := x.AssignmentConfig
	c.Roles = map[string]Executor{"writer": {Kind: "agent", AgentID: 8}}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	finishStep(t, a, p, r, "research", "agent:7:t", "notes", "")
	if f.events[len(f.events)-1].AgentID != 7 {
		t.Fatal("in-flight writer changed with assignment")
	}
}

func TestWorkflowCancellationAndLifecycle(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	lifecycle := &sdk.AgentEventLifecycle{SourceEventID: "process-step:" + s.ID, Sequence: 1, Type: "settled", ExecutionID: "execution"}
	event := sdk.Event{ProjectID: p.ProjectID, SourceApp: "apteva-server", InstanceID: 8}
	if e := a.stepLifecycle(event, lifecycle); e != nil {
		t.Fatal(e)
	}
	if stepBy(t, a, r, "research").State == "completed" || stepBy(t, a, r, "write").State != "pending" {
		t.Fatal("settlement completed business work")
	}
	event.InstanceID = 7
	if e := a.stepLifecycle(event, lifecycle); e == nil {
		t.Fatal("wrong agent lifecycle accepted")
	}
	event.InstanceID = 8
	event.ProjectID = "other"
	if e := a.stepLifecycle(event, lifecycle); e == nil {
		t.Fatal("wrong project lifecycle accepted")
	}
	if _, e := a.cancelWorkflow(p.ProjectID, "agent:8:t", p.ID, r.ID, "stop"); e == nil {
		t.Fatal("non-coordinator cancelled")
	}
	if _, e := a.cancelWorkflow(p.ProjectID, "agent:7:t", p.ID, r.ID, "stop"); e != nil {
		t.Fatal(e)
	}
	if _, e := a.stepAction(p.ProjectID, "agent:8:t", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "late"}); e == nil {
		t.Fatal("cancelled run accepted update")
	}
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	fresh, _ := a.getRun(p.ProjectID, p.ID, r.ID)
	if fresh.State != "cancelled" || len(f.events) != 1 || stepBy(t, a, r, "write").State != "cancelled" {
		t.Fatal("cancel did not stop handoffs")
	}
}
func TestWorkflowEvidenceAndApprovalRequired(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	if _, e := a.stepAction(p.ProjectID, "agent:8:t", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "  "}); e == nil {
		t.Fatal("blank evidence accepted")
	}
	finishStep(t, a, p, r, "research", "agent:8:t", "notes", "")
	finishStep(t, a, p, r, "write", "agent:7:t", "draft", "")
	s = stepBy(t, a, r, "review")
	if _, e := a.stepAction(p.ProjectID, "operator", p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "reviewed"}); e == nil {
		t.Fatal("missing decision accepted")
	}
	d := workflowDefinition()
	d.Steps[0].Role = "reviewer"
	roles := resolvedRoles(d, AssignmentConfig{OwnerAgentID: 7})
	if roles["reviewer"].Kind != "human" {
		t.Fatal("mixed approval role did not default human")
	}
}

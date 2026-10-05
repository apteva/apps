package main

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func parallelSetup(t *testing.T, limit int, configure ...func(*Definition)) (*App, *directPlatform, *Process, Run, string) {
	t.Helper()
	a, f, _ := directSetup(t)
	d := mediaContinuityDefinition()
	d.Steps = append(d.Steps[:3], append([]Step{{Key: "third", Name: "Third", Role: "media", Instructions: "Independent", ExpectedOutput: "Receipt", DependsOn: []string{"inventory"}}}, d.Steps[3:]...)...)
	d.Steps[4].DependsOn = append(d.Steps[4].DependsOn, "third")
	for _, edit := range configure {
		edit(&d)
	}
	p := create(t, a, d)
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.WorkerContinuity, c.ParallelExecution, c.MaxParallelSteps = "per_executor", "auto", limit
	c.Roles = map[string]Executor{"operator": {Kind: "human"}}
	if _, e := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "parallel", "")
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	s := stepBy(t, a, r, "inventory")
	return a, f, p, r, fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID)
}
func parallelAction(a *App, p *Process, r Run, actor, key, action string, args map[string]any) (any, error) {
	all, e := a.steps(r.ID)
	if e != nil {
		return nil, e
	}
	for _, s := range all {
		if s.Key == key {
			return a.executeMCP(p.ProjectID, actor, action, mergeParallelArgs(p.ID, r.ID, s.ID, args))
		}
	}
	return nil, errNotFound
}
func mergeParallelArgs(p, r, s string, args map[string]any) map[string]any {
	out := map[string]any{"process_id": p, "run_id": r, "step_id": s}
	for k, v := range args {
		out[k] = v
	}
	return out
}
func TestParallelClaimsRecoverExactCheckpointsAndKeepApproval(t *testing.T) {
	a, f, p, r, actor := parallelSetup(t, 2)
	act := func(key, action string, args map[string]any) map[string]any {
		t.Helper()
		v, e := parallelAction(a, p, r, actor, key, action, args)
		if e != nil {
			t.Fatal(key, e)
		}
		return v.(map[string]any)
	}
	act("inventory", "step_claim", nil)
	ack := act("inventory", "step_update", map[string]any{"state": "completed", "output": "source:exact"})
	if len(ack["ready_steps"].([]WorkerWorkItem)) != 3 || len(f.threads) != 1 {
		t.Fatal("branches not exposed to one owner", ack)
	}
	if !strings.Contains(strings.Join(f.threads[0].Tools, ","), "spawn") || strings.Contains(f.threads[0].DirectiveSuffix, "Do not execute unassigned work or create another worker") {
		t.Fatal("owner cannot delegate")
	}
	act("portrait_3", "step_claim", nil)
	act("portrait_4", "step_claim", nil)
	if _, e := parallelAction(a, p, r, actor, "third", "step_claim", nil); e == nil {
		t.Fatal("exceeded cap")
	}
	act("portrait_3", "step_update", map[string]any{"state": "waiting", "error": "Child still working", "output": "child=render-a;operation=artifact-3"})
	if _, e := parallelAction(a, p, r, actor, "third", "step_claim", nil); e == nil {
		t.Fatal("waiting released a live slot")
	}
	if _, e := parallelAction(a, p, r, "agent:7:child", "portrait_3", "step_update", map[string]any{"state": "completed", "output": "stolen"}); e == nil {
		t.Fatal("child stole step")
	}
	if _, e := parallelAction(a, p, r, actor, "validate", "step_claim", nil); e == nil {
		t.Fatal("joined early")
	}
	before := len(f.events)
	a = &App{ctx: a.ctx, db: a.db}
	if e := a.tickDirect(context.Background(), time.Now()); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != before || len(f.threads) != 1 {
		t.Fatal("restart redelivered or reprovisioned")
	}
	recovered := act("portrait_3", "step_claim", map[string]any{"include_context": true})
	if recovered["step"].(WorkerStep).Output != "child=render-a;operation=artifact-3" || len(recovered["active_steps"].([]WorkerWorkItem)) != 2 {
		t.Fatal("lost live checkpoints")
	}
	act("portrait_3", "step_update", map[string]any{"state": "completed", "output": "artifact:3", "error": ""})
	changes := totalChanges(t, a)
	act("portrait_3", "step_update", map[string]any{"state": "completed", "output": "artifact:3", "error": ""})
	if totalChanges(t, a) != changes {
		t.Fatal("replay mutated receipts")
	}
	if _, e := parallelAction(a, p, r, actor, "third", "step_update", map[string]any{"state": "completed", "output": "unclaimed"}); e == nil {
		t.Fatal("updated without claim")
	}
	act("third", "step_claim", nil)
	act("third", "step_update", map[string]any{"state": "completed", "output": "artifact:third"})
	act("portrait_4", "step_update", map[string]any{"state": "completed", "output": "artifact:4"})
	join := act("validate", "step_claim", nil)["dependencies"].(map[string]DependencyEvidence)
	if join["portrait_3"].Output != "artifact:3" || join["portrait_4"].Output != "artifact:4" || join["third"].Output != "artifact:third" {
		t.Fatal("lost distinct outputs")
	}
	act("validate", "step_update", map[string]any{"state": "completed", "output": "valid"})
	if _, e := parallelAction(a, p, r, actor, "approve", "step_update", map[string]any{"state": "completed", "output": "self"}); e == nil {
		t.Fatal("self approved")
	}
	if _, e := parallelAction(a, p, r, actor, "publish", "step_claim", nil); e == nil {
		t.Fatal("published before approval")
	}
	if _, e := parallelAction(a, p, r, "operator", "approve", "step_update", map[string]any{"state": "completed", "output": "operator proof"}); e != nil {
		t.Fatal(e)
	}
	act("publish", "step_claim", nil)
	final := act("publish", "step_update", map[string]any{"state": "completed", "output": "publication receipt"})
	if final["done"] != true || len(final["ready_steps"].([]WorkerWorkItem)) != 0 {
		t.Fatal("incorrect final gate", final)
	}
}
func TestParallelConcurrentClaimsEnforceFrozenLimit(t *testing.T) {
	a, _, p, r, actor := parallelSetup(t, 2)
	parallelAction(a, p, r, actor, "inventory", "step_claim", nil)
	if _, e := parallelAction(a, p, r, actor, "inventory", "step_update", map[string]any{"state": "completed", "output": "source"}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	accepted := 0
	for _, key := range []string{"portrait_3", "portrait_4", "third"} {
		wg.Add(1)
		go func(k string) {
			defer wg.Done()
			_, e := parallelAction(a, p, r, actor, k, "step_claim", nil)
			if e == nil {
				mu.Lock()
				accepted++
				mu.Unlock()
			}
		}(key)
	}
	wg.Wait()
	if accepted != 2 {
		t.Fatal("non-atomic concurrency cap", accepted)
	}
	x, e := a.assignmentStatus(p.ProjectID, p.ID, r.AssignmentID, "paused")
	if e != nil {
		t.Fatal(e)
	}
	c := x.AssignmentConfig
	c.MaxParallelSteps = 8
	if _, e = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e != nil {
		t.Fatal(e)
	}
	frozen, e := a.getRun(p.ProjectID, p.ID, r.ID)
	if e != nil || frozen.Binding.MaxParallelSteps != 2 {
		t.Fatal("changed frozen limit")
	}
}
func TestParallelOptionsAndTerminalRun(t *testing.T) {
	base := AssignmentConfig{OwnerAgentID: 7, WorkerContinuity: "per_executor", ParallelExecution: "auto"}
	for _, change := range []func(*AssignmentConfig){func(c *AssignmentConfig) { c.ParallelExecution = "typo" }, func(c *AssignmentConfig) { c.WorkerContinuity = "isolated" }, func(c *AssignmentConfig) { c.MaxParallelSteps = 9 }, func(c *AssignmentConfig) { c.MaxParallelSteps = -1 }, func(c *AssignmentConfig) { c.ParallelExecution = "sequential"; c.MaxParallelSteps = 2 }} {
		c := base
		change(&c)
		if validateExecution(c) == nil {
			t.Fatal("invalid options accepted", c)
		}
	}
	if e := validateExecution(base); e != nil {
		t.Fatal(e)
	}
	a, _, p, r, actor := parallelSetup(t, 2)
	if _, e := a.cancelWorkflow(p.ProjectID, actor, p.ID, r.ID, "stopped"); e != nil {
		t.Fatal(e)
	}
	if _, e := parallelAction(a, p, r, actor, "inventory", "step_claim", nil); e == nil {
		t.Fatal("claimed terminal run")
	}
}

func TestParallelTimedBranchWaitsWithoutLosingOwner(t *testing.T) {
	a, f, p, r, actor := parallelSetup(t, 2, func(d *Definition) {
		d.Steps[3].StartAfter = &TimingRule{After: "step_completed", StepKey: "inventory", Offset: 10, Unit: "minutes"}
	})
	if _, e := parallelAction(a, p, r, actor, "inventory", "step_claim", nil); e != nil {
		t.Fatal(e)
	}
	raw, e := parallelAction(a, p, r, actor, "inventory", "step_update", map[string]any{"state": "completed", "output": "source"})
	if e != nil {
		t.Fatal(e)
	}
	if len(raw.(map[string]any)["ready_steps"].([]WorkerWorkItem)) != 2 {
		t.Fatal("scheduled branch exposed as ready")
	}
	step := stepBy(t, a, r, "third")
	if step.State != "scheduled" || step.ThreadID != "" {
		t.Fatal("timed branch dispatched early", step)
	}
	if _, e = parallelAction(a, p, r, actor, "third", "step_claim", nil); e == nil {
		t.Fatal("early timed claim accepted")
	}
	start, e := time.Parse(time.RFC3339Nano, step.StartAt)
	if e != nil {
		t.Fatal(e)
	}
	before := len(f.events)
	if e = a.tickDirect(context.Background(), start.Add(-time.Nanosecond)); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != before {
		t.Fatal("timer woke early")
	}
	if e = a.tickDirect(context.Background(), start); e != nil {
		t.Fatal(e)
	}
	if len(f.events) != before+1 || len(f.threads) != 1 || stepBy(t, a, r, "third").ThreadID != stepBy(t, a, r, "inventory").ThreadID {
		t.Fatal("timer lost owner or wake identity")
	}
}
func TestParallelAssignmentRejectsDirectProcedures(t *testing.T) {
	a, _, p := directSetup(t)
	x, e := a.assignmentStatus(p.ProjectID, p.ID, p.Assignments[0].ID, "paused")
	if e != nil {
		t.Fatal(e)
	}
	c := x.AssignmentConfig
	c.WorkerContinuity = "per_executor"
	c.ParallelExecution = "auto"
	if _, e = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); e == nil {
		t.Fatal("parallel assignment without steps accepted")
	}
}

func TestParallelDefaultCapacityIsStoredInFrozenBinding(t *testing.T) {
	a, _, p, r, _ := parallelSetup(t, 0)
	x, e := a.assignment(p.ProjectID, p.ID, r.AssignmentID)
	if e != nil || x.MaxParallelSteps != 4 || r.Binding.MaxParallelSteps != 4 {
		t.Fatal("default capacity was not frozen", x.MaxParallelSteps, r.Binding.MaxParallelSteps, e)
	}
}

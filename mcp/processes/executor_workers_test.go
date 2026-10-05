package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"
)

func executorSetup(t *testing.T, d Definition, mode string, roles map[string]Executor) (*App, *directPlatform, *Process, Run) {
	t.Helper()
	a, f, _ := directSetup(t)
	p := create(t, a, d)
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.WorkerContinuity, c.Roles = mode, roles
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	raw, err := a.start(p.ProjectID, p.ID, "continuity", "")
	if err != nil {
		t.Fatal(err)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}

func mediaContinuityDefinition() Definition {
	d := workflowDefinition()
	d.Steps = []Step{
		{Key: "inventory", Name: "Inventory", Role: "media", Instructions: "Record source identity", ExpectedOutput: "Source receipt"},
		{Key: "portrait_3", Name: "Portrait 3", Role: "media", Instructions: "Render third portrait", ExpectedOutput: "Exact artifact receipt", DependsOn: []string{"inventory"}},
		{Key: "portrait_4", Name: "Portrait 4", Role: "media", Instructions: "Render fourth portrait", ExpectedOutput: "Exact artifact receipt", DependsOn: []string{"inventory"}},
		{Key: "validate", Name: "Validate", Role: "media", Instructions: "Validate both exact artifacts", ExpectedOutput: "Validation receipt", DependsOn: []string{"inventory", "portrait_3", "portrait_4"}},
		{Key: "approve", Name: "Approval", Role: "operator", Instructions: "Approve validated outputs", ExpectedOutput: "Operator approval", DependsOn: []string{"validate"}},
		{Key: "publish", Name: "Publish", Role: "media", Instructions: "Publish approved artifacts", ExpectedOutput: "Publication receipt", DependsOn: []string{"approve"}},
	}
	return d
}

func TestExecutorWorkerSerializesBranchesKeepsJoinAndApproval(t *testing.T) {
	a, f, p, r := executorSetup(t, mediaContinuityDefinition(), "per_executor", map[string]Executor{"operator": {Kind: "human"}})
	first := stepBy(t, a, r, "inventory")
	actor := fmt.Sprintf("agent:%d:%s", first.Executor.AgentID, first.ThreadID)
	claim := func(key string) map[string]any {
		t.Helper()
		s := stepBy(t, a, r, key)
		raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_claim", nil)
		if err != nil {
			t.Fatal(key, err)
		}
		return raw.(map[string]any)
	}
	claim("inventory")
	finishStep(t, a, p, r, "inventory", actor, "source:asset-42", "")
	changes := totalChanges(t, a)
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, first.ID, "step_update", map[string]any{"state": "completed", "output": "source:asset-42"}); err != nil {
		t.Fatal("lost completion response wasn't safely retryable", err)
	}
	if totalChanges(t, a) != changes {
		t.Fatal("completion replay changed durable records")
	}
	three, four := stepBy(t, a, r, "portrait_3"), stepBy(t, a, r, "portrait_4")
	if len(f.threads) != 1 || three.ThreadID != first.ThreadID || four.State != "ready" || four.ThreadID != "" {
		t.Fatalf("branches weren't serialized: %+v %+v threads=%d", three, four, len(f.threads))
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, four.ID, "step_claim", nil); err == nil {
		t.Fatal("claimed a second active branch")
	}
	if _, err := a.stepAction(p.ProjectID, "agent:7:competitor", p.ID, r.ID, three.ID, "step_update", map[string]any{"state": "completed", "output": "stolen"}); err == nil {
		t.Fatal("wrong thread updated step")
	}
	claim("portrait_3")
	finishStep(t, a, p, r, "portrait_3", actor, "artifact:portrait-3.png|source:asset-42", "")
	// A sidecar restart must retain ownership and the exact completed checkpoint.
	before := len(f.events)
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if len(f.events) != before {
		t.Fatal("restart duplicated accepted deliveries")
	}
	claim("portrait_4")
	finishStep(t, a, p, r, "portrait_4", actor, "artifact:portrait-4.png|source:asset-42", "")
	validation := claim("validate")
	outputs := validation["dependencies"].(map[string]DependencyEvidence)
	if outputs["portrait_3"].Output != "artifact:portrait-3.png|source:asset-42" || outputs["portrait_4"].Output != "artifact:portrait-4.png|source:asset-42" {
		t.Fatal("artifact identities lost", outputs)
	}
	finishStep(t, a, p, r, "validate", actor, "validated:portrait-3.png,portrait-4.png", "")
	approval, publish := stepBy(t, a, r, "approve"), stepBy(t, a, r, "publish")
	if approval.State != "waiting" || publish.State != "pending" || publish.ThreadID != "" {
		t.Fatal("approval gate bypassed")
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, publish.ID, "step_claim", nil); err == nil {
		t.Fatal("claimed before approval")
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, approval.ID, "step_update", map[string]any{"state": "completed", "output": "self-approved"}); err == nil {
		t.Fatal("worker impersonated operator")
	}
	finishStep(t, a, p, r, "approve", "operator", "approved:portrait-3.png,portrait-4.png", "")
	claim("publish")
	raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, publish.ID, "step_update", map[string]any{"state": "completed", "output": "receipt:exact-artifacts"})
	if err != nil || raw.(map[string]any)["done"] != true {
		t.Fatal("worker didn't finish", raw, err)
	}
	if len(f.threads) != 1 {
		t.Fatal("provisioned another worker", f.threads)
	}
	all, _ := a.steps(r.ID)
	for _, s := range all {
		if s.Executor.Kind == "agent" && s.ThreadID != first.ThreadID {
			t.Fatal("step lost worker", s)
		}
	}
	if !strings.Contains(first.ThreadID, "agent-7-worker") {
		t.Fatal("executor identity absent", first.ThreadID)
	}
}

func TestExecutorWorkersRemainSeparateAcrossAgentsAndRuns(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:3]
	for i := range d.Steps {
		d.Steps[i].DependsOn = nil
	}
	a, f, p, r := executorSetup(t, d, "per_executor", map[string]Executor{"reviewer": {Kind: "agent", AgentID: 8}})
	one, two, other := stepBy(t, a, r, "research"), stepBy(t, a, r, "write"), stepBy(t, a, r, "review")
	if len(f.threads) != 2 || one.ThreadID == other.ThreadID || other.ThreadID == "" || two.ThreadID != "" {
		t.Fatalf("executor isolation failed: %+v %+v %+v", one, two, other)
	}
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	two = stepBy(t, a, r, "write")
	raw, err := a.stepAction(p.ProjectID, "agent:7:"+one.ThreadID, p.ID, r.ID, two.ID, "step_update", map[string]any{"state": "completed", "output": "draft"})
	if err != nil || raw.(map[string]any)["done"] != true {
		t.Fatal("finished executor was kept alive", raw, err)
	}
	raw, err = a.start(p.ProjectID, p.ID, "another-run", "")
	if err != nil {
		t.Fatal(err)
	}
	next := raw.(map[string]any)["run"].(Run)
	if stepBy(t, a, next, "research").ThreadID == one.ThreadID {
		t.Fatal("context leaked across runs")
	}
}

func TestExplicitIsolationKeepsStrictChainWorkersSeparate(t *testing.T) {
	a, f, p, r := executorSetup(t, workflowDefinition(), "isolated", nil)
	one := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "research", "")
	two := stepBy(t, a, r, "write")
	if len(f.threads) != 2 || two.ThreadID == one.ThreadID {
		t.Fatal("isolated assignment reused worker")
	}
}

func TestExecutorContinuityRetainsTimedWaitAndRejectsEarlyClaim(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:2]
	d.Steps[1].StartAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 1, Unit: "minutes"}
	a, f, p, r := executorSetup(t, d, "per_executor", nil)
	one := stepBy(t, a, r, "research")
	actor := "agent:7:" + one.ThreadID
	finishStep(t, a, p, r, "research", actor, "saved checkpoint", "")
	two := stepBy(t, a, r, "write")
	if two.State != "scheduled" || len(f.events) != 1 {
		t.Fatal("timer dispatched early")
	}
	if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, two.ID, "step_claim", nil); err == nil {
		t.Fatal("claimed timed step early")
	}
	if _, err := a.db.Exec(`UPDATE process_step_runs SET start_at=? WHERE id=?`, time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano), two.ID); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	if err := a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	two = stepBy(t, a, r, "write")
	if two.ThreadID != one.ThreadID || len(f.threads) != 1 {
		t.Fatal("timed recovery replaced worker")
	}
}

func TestWorkerContinuityValidationAndFrozenBinding(t *testing.T) {
	a, _, p, r := executorSetup(t, mediaContinuityDefinition(), "per_executor", map[string]Executor{"operator": {Kind: "human"}})
	x, err := a.assignmentStatus(p.ProjectID, p.ID, r.AssignmentID, "paused")
	if err != nil {
		t.Fatal(err)
	}
	c := x.AssignmentConfig
	c.WorkerContinuity = "typo"
	if _, err = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err == nil {
		t.Fatal("invalid continuity accepted")
	}
	c.WorkerContinuity = "isolated"
	if _, err = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	saved, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || saved.Binding.WorkerContinuity != "per_executor" {
		t.Fatal("running policy changed", saved, err)
	}
}

func TestExecutorWorkerDoesNotReprovisionWhenStoredOrderDiffers(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:2]
	d.Steps[0], d.Steps[1] = d.Steps[1], d.Steps[0]
	a, f, p, r := executorSetup(t, d, "per_executor", nil)
	one := stepBy(t, a, r, "research")
	finishStep(t, a, p, r, "research", "agent:7:"+one.ThreadID, "checkpoint", "")
	if len(f.threads) != 1 || stepBy(t, a, r, "write").ThreadID != one.ThreadID {
		t.Fatal("stored order caused extra provisioning")
	}
}

func TestExecutorWorkerRetryKeepsFrozenEnvelopeAndReservesBranch(t *testing.T) {
	d := mediaContinuityDefinition()
	a, f, p := reliabilitySetup(t, d)
	x, err := a.assignmentStatus(p.ProjectID, p.ID, p.Assignments[0].ID, "paused")
	if err != nil {
		t.Fatal(err)
	}
	c := x.AssignmentConfig
	c.WorkerContinuity = "per_executor"
	c.Roles = map[string]Executor{"operator": {Kind: "human"}}
	if _, err = a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	x = enableAssignment(t, a, p, x)
	r := startOn(t, a, p, x, "retry", nil)
	one := stepBy(t, a, r, "inventory")
	actor := "agent:7:" + one.ThreadID
	f.loseSend = true
	finishStep(t, a, p, r, "inventory", actor, "source:asset-42", "")
	three, four := stepBy(t, a, r, "portrait_3"), stepBy(t, a, r, "portrait_4")
	if three.DeliveredAt != "" || three.ThreadID != one.ThreadID || four.ThreadID != "" {
		t.Fatal("ambiguous branch didn't retain reservation")
	}
	original := jsonText(f.events[len(f.events)-1])
	if _, err = a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, three.ID); err != nil {
		t.Fatal(err)
	}
	a = &App{ctx: a.ctx, db: a.db}
	f.a = a
	if err = a.tickDirect(context.Background(), time.Now()); err != nil {
		t.Fatal(err)
	}
	if jsonText(f.events[len(f.events)-1]) != original || len(f.threads) != 1 || stepBy(t, a, r, "portrait_4").ThreadID != "" {
		t.Fatal("retry changed envelope or dispatched concurrent branch")
	}
}

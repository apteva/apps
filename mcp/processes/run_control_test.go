package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func controlledSetup(t *testing.T, definition Definition, config func(*AssignmentConfig)) (*App, *directPlatform, *Process, Run) {
	t.Helper()
	a, f, _ := directSetup(t)
	p := create(t, a, definition)
	x := p.Assignments[0]
	c := x.AssignmentConfig
	c.WorkerContinuity = "per_executor"
	if config != nil {
		config(&c)
	}
	if _, err := a.saveAssignment(p.ProjectID, p.ID, x.ID, x.Revision, c); err != nil {
		t.Fatal(err)
	}
	p = status(t, a, p.ID, "active")
	raw, err := a.executeMCP(p.ProjectID, "agent:7:main", "start", map[string]any{"process_id": p.ID, "assignment_id": x.ID, "idempotency_key": "controlled", "control_mode": "step_by_step"})
	if err != nil {
		t.Fatal(err)
	}
	return a, f, p, raw.(map[string]any)["run"].(Run)
}
func advance(t *testing.T, a *App, p *Process, r Run, key string) map[string]any {
	t.Helper()
	s := stepBy(t, a, r, key)
	raw, err := a.executeMCP(p.ProjectID, "agent:7:main", "run_advance", map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID, "idempotency_key": "release-" + key})
	if err != nil {
		t.Fatal(err)
	}
	return raw.(map[string]any)
}
func claimAndFinish(t *testing.T, a *App, p *Process, r Run, key, output string) {
	t.Helper()
	s := stepBy(t, a, r, key)
	actor := fmt.Sprintf("agent:%d:%s", s.Executor.AgentID, s.ThreadID)
	if s.Executor.Kind == "human" {
		actor = "operator"
	} else {
		if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_claim", nil); err != nil {
			t.Fatal(err)
		}
	}
	finishStep(t, a, p, r, key, actor, output, "")
}
func TestStepByStepReleaseContinuityAndApproval(t *testing.T) {
	a, f, p, r := controlledSetup(t, workflowDefinition(), func(c *AssignmentConfig) { c.Roles = map[string]Executor{"reviewer": {Kind: "human"}} })
	first := stepBy(t, a, r, "research")
	if r.Binding.ControlMode != "step_by_step" || r.ControlMode != "step_by_step" || !r.WaitingForAdvance || len(r.EligibleSteps) != 1 || len(f.events) != 0 || first.State != "ready" || first.ThreadID != "" {
		t.Fatalf("run was not held: %+v %+v", r, first)
	}
	if _, err := a.stepAction(p.ProjectID, "agent:7:worker", p.ID, r.ID, first.ID, "step_claim", nil); err == nil {
		t.Fatal("held claim allowed")
	}
	if _, err := a.stepAction(p.ProjectID, "agent:7:worker", p.ID, r.ID, first.ID, "step_update", map[string]any{"state": "completed", "output": "bypass"}); err == nil {
		t.Fatal("held update allowed")
	}
	if _, err := a.executeMCP(p.ProjectID, "agent:7:worker", "run_advance", map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": first.ID, "idempotency_key": "worker"}); err == nil {
		t.Fatal("worker advanced")
	}
	restarted := &App{ctx: a.ctx, db: a.db}
	for i := 0; i < 3; i++ {
		if err := restarted.tickDirect(context.Background(), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	if len(f.events) != 0 {
		t.Fatal("reconciliation released held step")
	}
	receipt := advance(t, a, p, r, "research")
	if receipt["step_id"] != first.ID || receipt["control_mode"] != "step_by_step" || receipt["duplicate"] != false || len(f.events) != 1 {
		t.Fatal(receipt, len(f.events))
	}
	worker := stepBy(t, a, r, "research").ThreadID
	if !strings.Contains(worker, "process-run-") {
		t.Fatal("missing worker")
	}
	if advance(t, a, p, r, "research")["duplicate"] != true || len(f.events) != 1 {
		t.Fatal("duplicate delivery")
	}
	claimAndFinish(t, a, p, r, "research", "exact receipt alpha.png")
	next := stepBy(t, a, r, "write")
	if next.State != "ready" || next.ReleasedAt != "" || next.ThreadID != "" || len(f.events) != 1 {
		t.Fatal("downstream auto released", next)
	}
	// Freeze survives later edits to the saved assignment.
	if _, err := a.db.Exec(`UPDATE process_assignments SET body_json=json_set(body_json,'$.control_mode','automatic') WHERE id=?`, r.AssignmentID); err != nil {
		t.Fatal(err)
	}
	fresh, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || controlMode(fresh) != "step_by_step" {
		t.Fatal(fresh, err)
	}
	advance(t, a, p, r, "write")
	if stepBy(t, a, r, "write").ThreadID != worker {
		t.Fatal("worker continuity lost")
	}
	claimAndFinish(t, a, p, r, "write", "exact draft beta.png")
	human := stepBy(t, a, r, "review")
	if human.State != "ready" {
		t.Fatal(human)
	}
	if _, err := a.stepAction(p.ProjectID, "operator", p.ID, r.ID, human.ID, "step_update", map[string]any{"state": "completed", "output": "premature approval"}); err == nil {
		t.Fatal("held human step bypassed")
	}
	advance(t, a, p, r, "review")
	if stepBy(t, a, r, "review").State != "waiting" {
		t.Fatal("approval unavailable")
	}
	publish := stepBy(t, a, r, "publish")
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, publish.ID, "premature"); err == nil {
		t.Fatal("approval dependency bypass")
	}
	request := httptest.NewRequest("POST", fmt.Sprintf("/processes/%s/runs/%s/steps/%s?project_id=%s", p.ID, r.ID, human.ID, p.ProjectID), strings.NewReader(`{"state":"completed","output":"Operator approved exact draft beta.png"}`))
	response := httptest.NewRecorder()
	a.handleHTTP(response, request)
	if response.Code != 200 {
		t.Fatal(response.Code, response.Body.String())
	}
	if stepBy(t, a, r, "publish").ReleasedAt != "" || len(f.events) != 2 {
		t.Fatal("publication auto dispatched")
	}
	advance(t, a, p, r, "publish")
	claimAndFinish(t, a, p, r, "publish", "published exact beta.png")
	fresh, err = a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil || fresh.State != "completed" || !strings.Contains(fresh.Result, "exact draft beta.png") {
		t.Fatal(fresh, err)
	}
	if advance(t, a, p, r, "publish")["duplicate"] != true || len(f.events) != 3 {
		t.Fatal("terminal retry not idempotent")
	}
	var count int
	a.db.QueryRow(`SELECT count(*) FROM process_run_advances WHERE run_id=?`, r.ID).Scan(&count)
	if count != 4 {
		t.Fatal(count)
	}
}
func TestStepByStepBranchesConcurrentRetryAndEligibility(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:3]
	d.Steps[1].DependsOn = nil
	d.Steps[2].DependsOn = []string{"research", "write"}
	a, f, p, r := controlledSetup(t, d, func(c *AssignmentConfig) { c.ParallelExecution = "auto"; c.MaxParallelSteps = 2 })
	s := stepBy(t, a, r, "research")
	var wg sync.WaitGroup
	errors := make(chan error, 10)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := a.executeMCP(p.ProjectID, "operator", "run_advance", map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID, "idempotency_key": "same"})
			errors <- err
		}()
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(f.events) != 1 {
		t.Fatal("duplicate dispatch", len(f.events))
	}
	other := stepBy(t, a, r, "write")
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, other.ID, "same"); err == nil {
		t.Fatal("reused key")
	}
	join := stepBy(t, a, r, "review")
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, join.ID, "join"); err == nil {
		t.Fatal("pending join released")
	}
	if _, err := a.advanceRun(p.ProjectID, "agent:8:main", p.ID, r.ID, other.ID, "wrong-owner"); err == nil {
		t.Fatal("wrong coordinator")
	}
	advance(t, a, p, r, "write")
	if len(f.events) != 2 || stepBy(t, a, r, "write").ThreadID != stepBy(t, a, r, "research").ThreadID {
		t.Fatal("branch ownership lost")
	}
	claimAndFinish(t, a, p, r, "research", "first exact output")
	if stepBy(t, a, r, "review").State != "pending" {
		t.Fatal("premature join")
	}
	claimAndFinish(t, a, p, r, "write", "second exact output")
	if stepBy(t, a, r, "review").State != "ready" || len(f.events) != 2 {
		t.Fatal("join dispatched without advance")
	}
	read, err := a.executeMCP(p.ProjectID, "operator", "run_get", map[string]any{"process_id": p.ID, "run_id": r.ID})
	if err != nil {
		t.Fatal(err)
	}
	rr := read.(map[string]any)["run"].(Run)
	if !rr.WaitingForAdvance || len(rr.EligibleSteps) != 1 || rr.EligibleSteps[0].ID != join.ID {
		t.Fatal(rr)
	}
	advance(t, a, p, r, "review")
	claimAndFinish(t, a, p, r, "review", "joined exact outputs")
}
func TestStepByStepTimingAndInvalidMode(t *testing.T) {
	d := workflowDefinition()
	d.Steps[0].StartAfter = &TimingRule{After: "run_start", Offset: 1, Unit: "hours"}
	a, f, p, r := controlledSetup(t, d, nil)
	s := stepBy(t, a, r, "research")
	if s.State != "scheduled" || s.StartAt == "" {
		t.Fatal(s)
	}
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, s.ID, "timed"); err == nil {
		t.Fatal("scheduled step released")
	}
	if len(f.events) != 0 {
		t.Fatal("timed delivery")
	}
	if _, err := a.startAssignment(p.ProjectID, p.ID, r.AssignmentID, "invalid", "", nil, "invalid"); err == nil {
		t.Fatal("invalid mode")
	}
	if _, err := a.startAssignment(p.ProjectID, p.ID, r.AssignmentID, "controlled", "", nil, "automatic"); err == nil {
		t.Fatal("changed start mode reused key")
	}
}

func TestStepByStepAmbiguousDeliveryKeepsReleaseAndEnvelope(t *testing.T) {
	a, f, p := reliabilitySetup(t, workflowDefinition())
	raw, err := a.startAssignment(p.ProjectID, p.ID, p.Assignments[0].ID, "controlled", "", nil, "step_by_step")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	f.loseSend = true
	receipt := advance(t, a, p, r, "research")
	if receipt["delivery_warning"] == nil {
		t.Fatal("lost delivery not reported")
	}
	s := stepBy(t, a, r, "research")
	if s.ReleasedAt == "" || len(f.ledger) != 1 {
		t.Fatal("authorization not committed")
	}
	// Simulate a restart after remote acceptance but before the response was saved.
	a = &App{ctx: a.ctx, db: a.db}
	f.a = a
	if _, err = a.db.Exec(`UPDATE process_step_runs SET next_attempt_at='' WHERE id=?`, s.ID); err != nil {
		t.Fatal(err)
	}
	retry := advance(t, a, p, r, "research")
	if retry["duplicate"] != true || len(f.ledger) != 1 || stepBy(t, a, r, "research").DeliveredAt == "" {
		t.Fatal(retry)
	}
	var n int
	a.db.QueryRow(`SELECT count(*) FROM process_run_advances WHERE run_id=?`, r.ID).Scan(&n)
	if n != 1 {
		t.Fatal("duplicate durable release")
	}
}
func TestStepByStepRejectsDirectAutomaticCancelledAndWrongProject(t *testing.T) {
	a, _, p := directSetup(t)
	if _, err := a.startAssignment(p.ProjectID, p.ID, p.Assignments[0].ID, "direct", "", nil, "step_by_step"); err == nil {
		t.Fatal("direct control accepted")
	}
	a, _, p, r := workflowSetup(t)
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, stepBy(t, a, r, "research").ID, "auto"); err == nil {
		t.Fatal("automatic advance accepted")
	}
	a, _, p, r = controlledSetup(t, workflowDefinition(), nil)
	if _, err := a.advanceRun("other", "operator", p.ID, r.ID, stepBy(t, a, r, "research").ID, "scope"); err == nil {
		t.Fatal("cross-project release")
	}
	if _, err := a.cancelWorkflow(p.ProjectID, "operator", p.ID, r.ID, "stop test"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.advanceRun(p.ProjectID, "operator", p.ID, r.ID, stepBy(t, a, r, "research").ID, "cancelled"); err == nil {
		t.Fatal("cancelled advance accepted")
	}
}

func TestStepByStepHTTPAdvanceSelectsBodyStepAndProtectsRouteScope(t *testing.T) {
	a, f, p, r := controlledSetup(t, workflowDefinition(), nil)
	first := stepBy(t, a, r, "research")
	path := fmt.Sprintf("/processes/%s/runs/%s/advance?project_id=%s", p.ID, r.ID, p.ProjectID)
	body := fmt.Sprintf(`{"step_id":%q,"idempotency_key":"http-release","process_id":"spoof","run_id":"spoof","project_id":"spoof"}`, first.ID)
	for i := 0; i < 2; i++ {
		response := httptest.NewRecorder()
		a.handleHTTP(response, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if response.Code != 200 || !strings.Contains(response.Body.String(), first.ID) || strings.Contains(response.Body.String(), "spoof") {
			t.Fatal(response.Code, response.Body.String())
		}
	}
	if len(f.events) != 1 || stepBy(t, a, r, "research").ReleasedAt == "" {
		t.Fatal("HTTP release missing or duplicated")
	}
	var n int
	if err := a.db.QueryRow(`SELECT count(*) FROM process_event_outbox WHERE topic='step.updated' AND json_extract(payload_json,'$.step_id')=? AND json_extract(payload_json,'$.released_at')<>''`, first.ID).Scan(&n); err != nil || n == 0 {
		t.Fatal("release omitted live UI event", n, err)
	}

}

func TestStepByStepIndependentReleaseDuringDeliveryFailure(t *testing.T) {
	d := workflowDefinition()
	d.Steps = d.Steps[:2]
	d.Steps[1].DependsOn = nil
	a, f, p := reliabilitySetup(t, d)
	raw, err := a.startAssignment(p.ProjectID, p.ID, p.Assignments[0].ID, "controlled", "", nil, "step_by_step")
	if err != nil {
		t.Fatal(err)
	}
	r := raw.(map[string]any)["run"].(Run)
	f.sendError = fmt.Errorf("temporary delivery unavailable")
	advance(t, a, p, r, "research")
	receipt := advance(t, a, p, r, "write")
	if receipt["step_id"] != stepBy(t, a, r, "write").ID || stepBy(t, a, r, "write").ReleasedAt == "" {
		t.Fatal("unrelated failure blocked branch authorization")
	}
}

func TestStepByStepAdvanceIsCompactAndFrozenReadIsExact(t *testing.T) {
	d := workflowDefinition()
	policy := strings.Repeat("EXACT_FROZEN_POLICY ", 1000)
	d.Instructions = policy
	d.Steps[0].Instructions = policy
	a, f, p, r := controlledSetup(t, d, nil)
	receipt := advance(t, a, p, r, "research")
	raw, err := json.Marshal(receipt)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw) > 3000 || strings.Contains(string(raw), "EXACT_FROZEN_POLICY") {
		t.Fatal("advance echoed procedure context", len(raw))
	}
	if !strings.Contains(f.events[0].Message.(string), "explicit controller advancement") {
		t.Fatal("worker delivery omitted control contract")
	}
	reread := receipt["reread"].(RereadReference)
	if reread.Tool != "processes_run_get" || reread.Args["process_id"] != p.ID || reread.Args["run_id"] != r.ID {
		t.Fatal(reread)
	}
	s := stepBy(t, a, r, "research")
	read, err := a.stepAction(p.ProjectID, fmt.Sprintf("agent:7:%s", s.ThreadID), p.ID, r.ID, s.ID, "step_claim", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx := read.(map[string]any)
	frozen, err := a.runDefinition(r)
	if err != nil {
		t.Fatal(err)
	}
	if ctx["instructions"] != frozen.Instructions || ctx["step"].(WorkerStep).Definition.Instructions != frozen.Steps[0].Instructions || len(frozen.Steps[0].Instructions) != len(policy) {
		t.Fatal("frozen instructions changed")
	}
}

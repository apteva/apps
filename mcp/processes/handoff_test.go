package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func TestStructuredHandoffEvidenceAndTargetedRecovery(t *testing.T) {
	a, _, p, r := executorSetup(t, workflowDefinition(), "per_executor", nil)
	first := stepBy(t, a, r, "research")
	thread := first.ThreadID
	base := func(step string) map[string]any {
		return map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": step}
	}
	mcpOK(t, a, p.ProjectID, thread, "step_claim", base(first.ID))
	diagnostics := strings.Repeat("Exact source:9007199254740993; digest:abc; detail😀;", 130)
	args := base(first.ID)
	args["key"] = "render-details"
	args["artifact_id"] = "portrait-3.png"
	args["content"] = diagnostics
	args["references"] = []string{"source:9007199254740993"}
	ack := mcpOK(t, a, p.ProjectID, thread, "step_evidence_save", args)
	if len(responseJSON(t, ack)) > 1024 || strings.Contains(responseJSON(t, ack), "detail😀") {
		t.Fatal("evidence acknowledgement echoed diagnostics")
	}
	replay := mcpOK(t, a, p.ProjectID, thread, "step_evidence_save", args)
	if responseJSON(t, ack) != responseJSON(t, replay) {
		t.Fatal("evidence retry was not idempotent")
	}
	var count int
	a.db.QueryRow("SELECT COUNT(*) FROM process_step_evidence").Scan(&count)
	if count != 1 {
		t.Fatal("duplicate evidence persisted")
	}
	bad := base(first.ID)
	for k, v := range args {
		bad[k] = v
	}
	bad["content"] = "different"
	if _, e := mcpAs(t, a, p.ProjectID, 7, thread, "step_evidence_save", bad); e == nil {
		t.Fatal("immutable diagnostic overwritten")
	}
	if _, e := mcpAs(t, a, p.ProjectID, 8, "other", "step_evidence_save", args); e == nil {
		t.Fatal("wrong executor saved diagnostics")
	}
	updates := base(first.ID)
	updates["state"] = "completed"
	updates["handoff"] = map[string]any{"outcome": "Rendered portrait", "decision": "ready for review", "artifact_ids": []string{"portrait-3.png"}, "validations": []string{"source:9007199254740993; digest:abc"}, "evidence_refs": []string{ack["evidence_id"].(string)}}
	mcpOK(t, a, p.ProjectID, thread, "step_update", updates)
	second := stepBy(t, a, r, "write")
	claim := mcpOK(t, a, p.ProjectID, thread, "step_claim", base(second.ID))
	if strings.Contains(responseJSON(t, claim), "detail😀") {
		t.Fatal("claim copied diagnostics")
	}
	deps := claim["dependencies"].(map[string]DependencyEvidence)
	var h StepHandoff
	if e := json.Unmarshal([]byte(deps["research"].Output), &h); e != nil || h.ArtifactIDs[0] != "portrait-3.png" || h.EvidenceRefs[0] != ack["evidence_id"] {
		t.Fatal("handoff lost identity", e)
	}
	// Fresh receiver, no retained model/app memory: fetch one artifact in one call.
	a = &App{ctx: a.ctx, db: a.db}
	read := base(second.ID)
	read["target"] = "evidence"
	read["artifact_id"] = "portrait-3.png"
	recovered := mcpOK(t, a, p.ProjectID, thread, "step_read", read)
	saved := recovered["evidence"].(SavedStepEvidence)
	http := httpObject(t, a, "/processes/"+p.ID+"/runs/"+r.ID+"/steps/"+second.ID+"/read?target=evidence&evidence_id="+saved.ID)
	if !strings.Contains(http, saved.SHA256) {
		t.Fatal("operator HTTP evidence inspection failed")
	}
	if saved.Content != diagnostics || saved.References[0] != "source:9007199254740993" {
		t.Fatal("exact artifact evidence lost after restart")
	}
	instructionArgs := base(second.ID)
	instructionArgs["target"] = "instructions"
	instructions := mcpOK(t, a, p.ProjectID, thread, "step_read", instructionArgs)
	if instructions["instructions"] != second.Definition.Instructions || instructions["expected_output"] != second.Definition.ExpectedOutput || instructions["procedure_version"] != r.Version {
		t.Fatal("targeted current instructions changed")
	}
	retry := mcpOK(t, a, p.ProjectID, thread, "step_evidence_save", args)
	if retry["evidence_id"] != ack["evidence_id"] {
		t.Fatal("lost evidence acknowledgement not recoverable after completion")
	}
	t.Logf("diagnostics %d bytes, compact handoff %d bytes, claim %d bytes, save acknowledgement %d bytes", len(diagnostics), len(deps["research"].Output), len(responseJSON(t, claim)), len(responseJSON(t, ack)))
}
func TestCompactOutputValidationAndAtomicFailure(t *testing.T) {
	cases := []map[string]any{
		{"output": strings.Repeat("x", handoffBudget+1)},
		{"handoff": map[string]any{"outcome": strings.Repeat("x", handoffBudget)}},
		{"handoff": map[string]any{"outcome": ""}},
		{"handoff": map[string]any{"outcome": "done", "unknown": "bad"}},
		{"handoff": map[string]any{"outcome": "done", "artifact_ids": []any{float64(9007199254740992)}}},
		{"handoff": map[string]any{"outcome": "done", "blockers": []string{""}}},
		{"handoff": map[string]any{"outcome": "done", "evidence_refs": []string{"step-evidence-missing"}}},
		{"handoff": map[string]any{"outcome": "done"}, "output": "also done"},
	}
	for i, bad := range cases {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			a, _, p, r := executorSetup(t, workflowDefinition(), "per_executor", nil)
			s := stepBy(t, a, r, "research")
			mcpOK(t, a, p.ProjectID, s.ThreadID, "step_claim", map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID})
			before := totalChanges(t, a)
			args := map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID, "state": "completed"}
			for k, v := range bad {
				args[k] = v
			}
			if _, e := mcpAs(t, a, p.ProjectID, 7, s.ThreadID, "step_update", args); e == nil {
				t.Fatal("invalid handoff accepted")
			}
			if totalChanges(t, a) != before || stepBy(t, a, r, "research").State != "running" {
				t.Fatal("invalid update mutated workflow")
			}
		})
	}
}
func TestWorkerDeduplicatesContextWithoutChangingOriginalValues(t *testing.T) {
	policy := strings.Repeat("Exact operator authorization applies to this run; ", 100)
	a, _, _ := directSetup(t)
	d := workflowDefinition()
	d.ApprovalRequirements = policy
	d.Parameters = []Parameter{{Key: "policy_a", Type: "string", Default: policy}, {Key: "policy_b", Type: "string", Default: policy}}
	p := create(t, a, d)
	p = status(t, a, p.ID, "active")
	raw, e := a.start(p.ProjectID, p.ID, "dedup", "")
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	s := stepBy(t, a, r, "research")
	result, e := a.stepAction(p.ProjectID, "agent:7:"+s.ThreadID, p.ID, r.ID, s.ID, "step_get", nil)
	if e != nil {
		t.Fatal(e)
	}
	response := result.(map[string]any)
	if strings.Count(responseJSON(t, response), policy) != 1 {
		t.Fatal("repeated policy not interned exactly once")
	}
	if len(response["context_values"].(map[string]string)) != 1 {
		t.Fatal("duplicate shared context entries")
	}
	for _, key := range []string{"policy_a", "policy_b"} {
		recovered, e := a.stepRead(p.ProjectID, p.ID, map[string]any{"run_id": r.ID, "step_id": s.ID, "target": "parameter", "parameter_key": key})
		if e != nil || recovered.(map[string]any)["value"] != policy {
			t.Fatal("targeted original parameter changed", e)
		}
	}
	http := httpObject(t, a, "/processes/"+p.ID)
	if strings.Count(http, policy) < 3 {
		t.Fatal("HTTP audit data rewritten")
	}
	if strings.Contains(a.stepContext(p, r, s, r.Steps), policy) {
		t.Fatal("dispatch copied shared authorization")
	}
}
func TestTargetedEvidenceScopeAndLegacyOutput(t *testing.T) {
	a, _, p, r := executorSetup(t, workflowDefinition(), "per_executor", nil)
	first := stepBy(t, a, r, "research")
	thread := first.ThreadID
	finishStep(t, a, p, r, "research", "agent:7:"+thread, "small", "")
	legacy := strings.Repeat("legacy exact source:9007199254740993😀;", 300)
	a.db.Exec("UPDATE process_step_runs SET output=? WHERE id=?", legacy, first.ID)
	second := stepBy(t, a, r, "write")
	args := map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": second.ID}
	claim := mcpOK(t, a, p.ProjectID, thread, "step_claim", args)
	dep := claim["dependencies"].(map[string]DependencyEvidence)["research"]
	if !dep.Deferred || dep.Output != "" || dep.Reread == nil || strings.Contains(responseJSON(t, claim), legacy) {
		t.Fatal("oversized legacy output copied or silently shortened")
	}
	reread, e := a.stepRead(p.ProjectID, p.ID, dep.Reread.Args)
	if e != nil || reread.(map[string]any)["dependency"].(DependencyEvidence).Output != legacy {
		t.Fatal("legacy receipt not recovered exactly", e)
	}
	for _, bad := range []map[string]any{
		{"process_id": p.ID, "run_id": r.ID, "step_id": second.ID, "target": "dependency", "dependency_key": "publish"},
		{"process_id": p.ID, "run_id": r.ID, "step_id": first.ID, "target": "dependency", "dependency_key": "write"},
		{"process_id": p.ID, "run_id": r.ID, "step_id": "other-step", "target": "instructions"},
	} {
		if _, e := mcpAs(t, a, p.ProjectID, 7, thread, "step_read", bad); e == nil {
			t.Fatal("unrelated/future step read accepted")
		}
	}
	if _, e := mcpAs(t, a, "other-project", 7, thread, "step_read", args); e == nil {
		t.Fatal("cross-project read accepted")
	}
}

func TestManyDirectDependenciesStayBoundedWithoutLosingEvidence(t *testing.T) {
	current := StepRun{ID: "current", Key: "current", Definition: Step{Instructions: strings.Repeat("current step instruction; ", 200)}, State: "running"}
	steps := []StepRun{}
	for i := 0; i < 29; i++ {
		key := fmt.Sprintf("source_%02d_%s", i, strings.Repeat("x", 45))
		current.Definition.DependsOn = append(current.Definition.DependsOn, key)
		steps = append(steps, StepRun{ID: "step-" + strings.Repeat(fmt.Sprint(i%10), 32), Key: key, State: "completed", Output: strings.Repeat("exact receipt; ", 250)})
	}
	r := Run{ID: "run-" + strings.Repeat("r", 32), Binding: AssignmentConfig{Parameters: map[string]any{}}}
	response := map[string]any{"step": workerStep(current), "dependencies": workerDependencyEvidence("process-"+strings.Repeat("p", 32), r, current, steps), "instructions": strings.Repeat("frozen shared policy ", 2000), "parameters": map[string]any{}}
	a, _ := setup(t)
	result, e := a.boundWorkerResponse("process-"+strings.Repeat("p", 32), r, current, response, steps)
	if e != nil || len(responseJSON(t, result)) > workerDetailBudget {
		t.Fatal("many-source worker response exceeded budget", e, len(responseJSON(t, result)))
	}
	if _, ok := result["dependencies_ref"]; !ok {
		for _, dep := range result["dependencies"].(map[string]DependencyEvidence) {
			if !dep.Deferred || dep.Reread == nil {
				t.Fatal("required exact receipt lost instead of deferred")
			}
		}
	}
}

func TestArtifactEvidencePreservesExactIdentity(t *testing.T) {
	a, _, p, r := executorSetup(t, workflowDefinition(), "per_executor", nil)
	s := stepBy(t, a, r, "research")
	args := map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID}
	mcpOK(t, a, p.ProjectID, s.ThreadID, "step_claim", args)
	args["key"] = "identity"
	args["content"] = "Exact source:9007199254740993"
	args["artifact_id"] = " portrait-😀-9007199254740993.png "
	saved := mcpOK(t, a, p.ProjectID, s.ThreadID, "step_evidence_save", args)
	read := map[string]any{"process_id": p.ID, "run_id": r.ID, "step_id": s.ID, "target": "evidence", "artifact_id": args["artifact_id"]}
	result := mcpOK(t, a, p.ProjectID, s.ThreadID, "step_read", read)["evidence"].(SavedStepEvidence)
	if result.ID != saved["evidence_id"] || result.ArtifactID != args["artifact_id"] {
		t.Fatal("artifact identity normalized or rounded")
	}
	args["key"] = "numeric"
	args["artifact_id"] = float64(9007199254740992)
	if _, e := mcpAs(t, a, p.ProjectID, 7, s.ThreadID, "step_evidence_save", args); e == nil {
		t.Fatal("numeric artifact identity accepted")
	}
}

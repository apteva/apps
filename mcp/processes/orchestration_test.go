package main

import (
	"strings"
	"testing"
)

func TestProcessMainCoordinatorContract(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	s := stepBy(t, a, r, "research")
	got := a.stepContext(p, r, s, []StepRun{s})
	for _, want := range []string{"agent main thread", "platform spawn", "process-run-" + r.ID + "-step-" + s.Key, "step_update"} {
		if !strings.Contains(got, want) {
			t.Fatalf("step context missing %q: %s", want, got)
		}
	}
}

func TestIndependentRootsReadyForParallelWorkers(t *testing.T) {
	d := workflowDefinition()
	d.Steps[1].DependsOn = nil
	if !dependenciesReady(StepRun{Definition: d.Steps[0]}, nil) || !dependenciesReady(StepRun{Definition: d.Steps[1]}, nil) {
		t.Fatal("independent roots must be ready")
	}
}

func TestJoinWaitsForAllPredecessors(t *testing.T) {
	d := workflowDefinition()
	join := StepRun{Key: "join", Definition: Step{Key: "join", DependsOn: []string{"research", "write"}}}
	roots := []StepRun{{Key: "research", State: "completed", Definition: d.Steps[0]}, {Key: "write", State: "waiting", Definition: d.Steps[1]}}
	if dependenciesReady(join, roots) {
		t.Fatal("join released before all predecessors completed")
	}
	roots[1].State = "completed"
	if !dependenciesReady(join, roots) {
		t.Fatal("join did not release after all predecessors completed")
	}
}

func TestStepReadIncludesApprovalEvidenceAndAncestors(t *testing.T) {
	a, _, p, r := workflowSetup(t)
	finishStep(t, a, p, r, "research", "agent:8:t", "Research evidence", "")
	finishStep(t, a, p, r, "write", "agent:7:t", "Draft v1", "")
	finishStep(t, a, p, r, "review", "operator", "Approved", "approved")
	publish := stepBy(t, a, r, "publish")
	raw, err := a.stepAction(p.ProjectID, "agent:8:worker", p.ID, r.ID, publish.ID, "step_get", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := raw.(map[string]any)
	deps := got["dependencies"].(map[string]DependencyEvidence)
	review := stepBy(t, a, r, "review")
	if len(deps) != 3 || deps["review"].ID != review.ID || deps["review"].Kind != "approval" || deps["review"].Decision != "approved" || deps["review"].State != "completed" || !deps["review"].Direct {
		t.Fatalf("incomplete review evidence: %#v", deps)
	}
	if deps["write"].Direct || deps["write"].Output != "Draft v1" || deps["research"].Output != "Research evidence" {
		t.Fatalf("missing ancestor evidence: %#v", deps)
	}
	if got["dependency_outputs"].(map[string]string)["write"] != "Draft v1" {
		t.Fatal("legacy outputs changed")
	}
}

func TestDependencyEvidencePreservesPendingAndRejectedStates(t *testing.T) {
	target := StepRun{Key: "publish", Definition: Step{DependsOn: []string{"review", "draft"}}}
	all := []StepRun{
		{ID: "review-id", Key: "review", State: "completed", Decision: "rejected", Output: "needs work", Definition: Step{Kind: "approval", DependsOn: []string{"draft"}}},
		{ID: "draft-id", Key: "draft", State: "pending", Definition: Step{Kind: "work"}},
		{ID: "other-id", Key: "unrelated", State: "completed", Output: "not an ancestor"},
	}
	deps := dependencyEvidence(target, all)
	if len(deps) != 2 || deps["review"].Decision != "rejected" || deps["draft"].State != "pending" || !deps["draft"].Direct {
		t.Fatalf("incorrect evidence: %#v", deps)
	}
	if _, ok := dependencyOutputs(target, all)["draft"]; ok {
		t.Fatal("pending dependency exposed as completed output")
	}
	if dependenciesReady(target, all) {
		t.Fatal("rejected approval released work")
	}
}

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

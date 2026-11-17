package main

import (
	"strings"
	"testing"
)

func TestWorkerBriefInlinesEarlierResultsAndForgivingReads(t *testing.T) {
	a, f, p, r := workflowSetup(t)
	research := stepBy(t, a, r, "research")
	actor := "agent:8:" + research.ThreadID
	// An independent worker that claims gets its step back instead of an error.
	got, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, research.ID, "step_claim", nil)
	if err != nil || got.(map[string]any)["step"].(WorkerStep).ID != research.ID {
		t.Fatalf("independent claim was not answered as a read: %v %v", got, err)
	}
	if _, err = a.stepAction(p.ProjectID, actor, p.ID, r.ID, research.ID, "step_update", map[string]any{"state": "completed", "outcome": "R-RESULT"}); err != nil {
		t.Fatal(err)
	}
	if s := stepBy(t, a, r, "research"); s.Output != "R-RESULT" {
		t.Fatalf("outcome not saved as output: %+v", s)
	}
	finishStep(t, a, p, r, "write", "agent:7:t", "W-RESULT", "")
	finishStep(t, a, p, r, "review", "operator", "V-RESULT", "")
	// publish depends only on review, but its brief carries the earlier results too.
	directive := f.threads[len(f.threads)-1].DirectiveSuffix
	for _, want := range []string{"- review: V-RESULT", "- research (earlier): R-RESULT", "- write (earlier): W-RESULT"} {
		if !strings.Contains(directive, want) {
			t.Fatalf("brief missing %q: %s", want, directive)
		}
	}
	publish := stepBy(t, a, r, "publish")
	read, err := a.stepRead(p.ProjectID, p.ID, map[string]any{"run_id": r.ID, "step_id": publish.ID, "target": "dependency"})
	if err != nil {
		t.Fatal(err)
	}
	deps := read.(map[string]any)["dependencies"].(map[string]DependencyEvidence)
	if deps["research"].Output != "R-RESULT" || deps["review"].Output != "V-RESULT" {
		t.Fatalf("keyless dependency read: %+v", deps)
	}
}

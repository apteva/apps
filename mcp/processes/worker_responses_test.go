package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func responseJSON(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWorkerReadsKeepOneExactManifestAndRecoverSharedContext(t *testing.T) {
	for _, mode := range []string{"per_executor", "isolated"} {
		t.Run(mode, func(t *testing.T) {
			d := workflowDefinition()
			d.Instructions = strings.Repeat("shared frozen policy;", 500)
			d.ApprovalRequirements = "Require final validation and approval"
			d.Steps[3].Instructions = strings.Repeat("unrelated downstream instructions;", 1500)
			a, _, p, r := executorSetup(t, d, mode, nil)
			receipt := "exact-artifact:portrait-3.png|digest:abc|" + strings.Repeat("receipt-evidence;", 800)
			one := stepBy(t, a, r, "research")
			finishStep(t, a, p, r, one.Key, "agent:7:"+one.ThreadID, receipt, "")
			two := stepBy(t, a, r, "write")
			finishStep(t, a, p, r, two.Key, "agent:7:"+two.ThreadID, "draft-receipt", "")
			target := stepBy(t, a, r, "review")
			actor := "agent:7:" + target.ThreadID
			read := func(action string, args map[string]any) map[string]any {
				t.Helper()
				v, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, target.ID, action, args)
				if err != nil {
					t.Fatal(err)
				}
				return v.(map[string]any)
			}
			actions := []string{"step_get"}
			if mode == "per_executor" {
				actions = append(actions, "step_claim")
			}
			for _, action := range actions {
				full := read(action, nil)
				text := responseJSON(t, full)
				if _, ok := full["definition"]; ok {
					t.Fatal("full procedure in worker read")
				}
				if _, ok := full["dependency_outputs"]; ok {
					t.Fatal("duplicated dependency manifest")
				}
				if strings.Contains(text, "unrelated downstream instructions") || strings.Count(text, receipt) != 1 {
					t.Fatal("unrelated instructions or duplicated/lost receipt")
				}
				deps := full["dependencies"].(map[string]DependencyEvidence)
				if len(deps) != 2 || deps["research"].ID != one.ID || deps["research"].State != "completed" || deps["research"].Direct || deps["research"].Output != receipt || !deps["write"].Direct {
					t.Fatal("ancestor identities lost", deps)
				}
				if full["instructions"] != d.Instructions || full["approval_requirements"] != d.ApprovalRequirements {
					t.Fatal("missing default policy")
				}
				compact := read(action, map[string]any{"include_context": false})
				for _, key := range []string{"instructions", "parameters", "inputs", "assignment", "approval_requirements"} {
					if _, ok := compact[key]; ok {
						t.Fatal("opt-out retained", key)
					}
				}
				if responseJSON(t, compact["dependencies"]) != responseJSON(t, deps) || responseJSON(t, compact["step"]) != responseJSON(t, full["step"]) || compact["context_ref"] == nil {
					t.Fatal("opt-out lost checkpoint or manifest")
				}
				legacy, err := a.stepAction(p.ProjectID, "operator", p.ID, r.ID, target.ID, "step_get", nil)
				if err != nil {
					t.Fatal(err)
				}
				legacyJSON := responseJSON(t, legacy)
				if !strings.Contains(legacyJSON, "unrelated downstream instructions") || legacy.(map[string]any)["definition"] == nil {
					t.Fatal("operator inspection was compacted")
				}
				if len(text)*2 >= len(legacyJSON) {
					t.Fatalf("worker snapshot unexpectedly large: worker=%d operator=%d", len(text), len(legacyJSON))
				}
				t.Logf("%s: full inspection %d bytes, worker read %d bytes, retained-context read %d bytes", action, len(legacyJSON), len(text), len(responseJSON(t, compact)))
			}
			checkpoint := "recoverable intermediate output"
			if _, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, target.ID, "step_update", map[string]any{"state": "running", "progress": float64(40), "output": checkpoint}); err != nil {
				t.Fatal(err)
			}
			// A new sidecar instance must not assume an earlier response reached the worker.
			a = &App{ctx: a.ctx, db: a.db}
			for _, args := range []map[string]any{nil, {"include_context": true}, {"include_context": nil}, {"include_context": "invalid"}} {
				recovered := read("step_get", args)
				if recovered["instructions"] != d.Instructions || recovered["step"].(WorkerStep).Output != checkpoint || recovered["step"].(WorkerStep).Progress != 40 {
					t.Fatal("restart lost policy/checkpoint")
				}
			}
			inspected, err := a.directRun(p.ProjectID, actor, p.ID, r.ID, "run_get", nil)
			if err != nil || !strings.Contains(responseJSON(t, inspected), "unrelated downstream instructions") {
				t.Fatal("explicit run inspection lost procedure", err)
			}
		})
	}
}

func TestWorkerAcknowledgementsRemainSmallAndReplayable(t *testing.T) {
	for _, mode := range []string{"per_executor", "isolated"} {
		t.Run(mode, func(t *testing.T) {
			d := workflowDefinition()
			d.Instructions = strings.Repeat("shared-policy;", 2000)
			a, _, p, r := executorSetup(t, d, mode, nil)
			first := stepBy(t, a, r, "research")
			actor := "agent:7:" + first.ThreadID
			output := strings.Repeat("large-exact-output-receipt;", 500)
			update := func(state string, progress int, reason string) map[string]any {
				t.Helper()
				raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, first.ID, "step_update", map[string]any{"state": state, "progress": float64(progress), "output": output, "error": reason})
				if err != nil {
					t.Fatal(err)
				}
				ack := raw.(map[string]any)
				saved := stepBy(t, a, r, "research")
				if ack["state"] != saved.State || ack["revision"] != saved.Revision || ack["progress"] != saved.Progress || ack["run_state"] == nil || ack["next_action"] == "" {
					t.Fatal("ack lost accepted state", ack)
				}
				text := responseJSON(t, ack)
				if len(text) > 1000 || strings.Contains(text, "large-exact-output-receipt") || strings.Contains(text, "shared-policy") {
					t.Fatal("ack echoed large context", len(text))
				}
				for _, key := range []string{"step", "run", "definition", "dependencies", "dependency_outputs", "output", "worker"} {
					if _, ok := ack[key]; ok {
						t.Fatal("ack contains", key)
					}
				}
				return ack
			}
			progress := update("running", 30, "")
			if progress["done"] != false || !strings.Contains(progress["next_action"].(string), "Continue") {
				t.Fatal("progress stopped worker", progress)
			}
			blocked := update("blocked", 30, "await source permission")
			if blocked["done"] != false || blocked["error"] != "await source permission" || !strings.Contains(blocked["next_action"].(string), "await resolution") {
				t.Fatal("blocker lost", blocked)
			}
			update("running", 60, "")
			completed := update("completed", 100, "")
			if completed["done"] != (mode == "isolated") {
				t.Fatal("wrong worker lifetime", completed)
			}
			before := totalChanges(t, a)
			replay := update("completed", 100, "")
			if responseJSON(t, completed) != responseJSON(t, replay) || totalChanges(t, a) != before {
				t.Fatal("lost completion reply replay changed state")
			}
			if stepBy(t, a, r, "research").Output != output {
				t.Fatal("compact ack truncated durable receipt")
			}
			t.Logf("%s completion ack %d bytes for %d-byte output", mode, len(responseJSON(t, completed)), len(output))
		})
	}
}

func TestWorkerAcknowledgementKeepsApprovalGateAndFinalDone(t *testing.T) {
	a, _, p, r := executorSetup(t, mediaContinuityDefinition(), "per_executor", map[string]Executor{"operator": {Kind: "human"}})
	actor := "agent:7:" + stepBy(t, a, r, "inventory").ThreadID
	for _, key := range []string{"inventory", "portrait_3", "portrait_4", "validate"} {
		s := stepBy(t, a, r, key)
		raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "receipt:" + key})
		if err != nil {
			t.Fatal(err)
		}
		ack := raw.(map[string]any)
		if ack["done"] != false || !strings.Contains(ack["next_action"].(string), "Wait for the next Processes event") {
			t.Fatal("worker stopped before approval", ack)
		}
		if key == "validate" && ack["run_state"] != "waiting" {
			t.Fatal("lost approval gate state", ack)
		}
	}
	finishStep(t, a, p, r, "approve", "operator", "approved exact receipts", "")
	s := stepBy(t, a, r, "publish")
	raw, err := a.stepAction(p.ProjectID, actor, p.ID, r.ID, s.ID, "step_update", map[string]any{"state": "completed", "output": "published exact artifacts"})
	if err != nil {
		t.Fatal(err)
	}
	ack := raw.(map[string]any)
	if ack["done"] != true || ack["run_state"] != "completed" || !strings.Contains(fmt.Sprint(ack["next_action"]), "done tool immediately") {
		t.Fatal("lost final done", ack)
	}
}

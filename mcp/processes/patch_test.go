package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func patchArgs(p *Process, changes string) map[string]any {
	var fields map[string]any
	if err := json.Unmarshal([]byte(changes), &fields); err != nil {
		panic(err)
	}
	return map[string]any{"process_id": p.ID, "expected_version": float64(p.Version), "changes": fields}
}
func patchFixture(t *testing.T) (*App, *Process) {
	t.Helper()
	a, _ := setup(t)
	d := workflowDefinition()
	d.Instructions = strings.Repeat("KEEP SHARED POLICY; ", 2000)
	d.Parameters = []Parameter{{Key: "campaign", Type: "string", Default: "October", Label: "Campaign"}, {Key: "obsolete", Type: "boolean", Default: false}}
	p, err := a.save("project-a", "", "operator", 0, d)
	if err != nil {
		t.Fatal(err)
	}
	return a, p
}

func TestPatchSchemaUsesValidRequiredArrays(t *testing.T) {
	var schema any
	if err := json.Unmarshal([]byte(responseJSON(t, patchSchema())), &schema); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(value any) {
		switch node := value.(type) {
		case map[string]any:
			if required, exists := node["required"]; exists && node["type"] == "object" {
				if _, ok := required.([]any); !ok {
					t.Fatal("required must be an array, including on optional objects", node)
				}
			}
			for _, child := range node {
				visit(child)
			}
		case []any:
			for _, child := range node {
				visit(child)
			}
		}
	}
	visit(schema)
}
func TestPatchSingleStepCompactReceiptAndExactVersions(t *testing.T) {
	a, p := patchFixture(t)
	original := responseJSON(t, p.Definition)
	ack := mcpOK(t, a, p.ProjectID, "main", "patch", patchArgs(p, `{"steps":{"update":[{"key":"write","instructions":"Updated writing instructions"}]}}`))
	if ack["version"] != 2 || ack["previous_version"] != 1 || ack["status"] != "draft" {
		t.Fatal(ack)
	}
	if len(responseJSON(t, ack)) > 1024 || strings.Contains(responseJSON(t, ack), "KEEP SHARED") {
		t.Fatal("receipt echoed context")
	}
	current, err := a.get(p.ProjectID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := p.Definition
	want.Steps = append([]Step(nil), want.Steps...)
	want.Steps[1].Instructions = "Updated writing instructions"
	if !reflect.DeepEqual(current.Definition, want) {
		t.Fatal("unrelated content changed")
	}
	version, err := a.definition(p.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	if responseJSON(t, version) != original {
		t.Fatal("historical definition changed")
	}
	ref := ack["reread"].(map[string]any)
	if ref["tool"] != "processes_get" {
		t.Fatal(ref)
	}
	args := ref["args"].(map[string]any)
	args["version"] = float64(args["version"].(int))
	if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "get", args); err != nil {
		t.Fatal("invalid reread", err)
	}
	// HTTP continues to expose complete objects.
	if !strings.Contains(httpObject(t, a, "/processes/"+p.ID), "KEEP SHARED") {
		t.Fatal("HTTP object compacted")
	}
	t.Logf("full definition %d bytes; patch request %d bytes; receipt %d bytes", len(original), len(responseJSON(t, patchArgs(p, `{"steps":{"update":[{"key":"write","instructions":"Updated writing instructions"}]}}`))), len(responseJSON(t, ack)))
}
func TestPatchMixedEditsAndClearingValues(t *testing.T) {
	a, p := patchFixture(t)
	changes := `{"description":"","tags":[],"steps":{"remove":["publish"],"add":[{"key":"archive","name":"Archive","role":"writer","instructions":"Archive result","expected_output":"Archive receipt","depends_on":["review"]}],"update":[{"key":"write","depends_on":[],"due_after":null}]},"parameters":{"remove":["obsolete"],"update":[{"key":"campaign","label":"Campaign name","default":null}],"add":[{"key":"count","type":"number","default":3}]}}`
	mcpOK(t, a, p.ProjectID, "main", "patch", patchArgs(p, changes))
	got, err := a.get(p.ProjectID, p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Version != 2 || got.Description != "" || len(got.Tags) != 0 || len(got.Steps) != 4 || got.Steps[3].Key != "archive" || len(got.Steps[1].DependsOn) != 0 {
		t.Fatal(got)
	}
	if len(got.Parameters) != 2 || got.Parameters[0].Default != nil || got.Parameters[0].Type != "string" || got.Parameters[1].Default != float64(3) {
		t.Fatal(got.Parameters)
	}
}

func TestPatchReplacesNestedTimingAndPreservesExactIntegers(t *testing.T) {
	a, p := patchFixture(t)
	d := p.Definition
	d.Steps[1].DueAfter = &TimingRule{After: "step_completed", StepKey: "research", Offset: 3, Unit: "hours"}
	var err error
	p, err = a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if err != nil {
		t.Fatal(err)
	}
	mcpOK(t, a, p.ProjectID, "main", "patch", patchArgs(p, `{"steps":{"update":[{"key":"write","due_after":{"after":"run_start","offset":1,"unit":"days"}}]}}`))
	got, err := a.get(p.ProjectID, p.ID)
	if err != nil || got.Steps[1].DueAfter.StepKey != "" || got.Steps[1].DueAfter.After != "run_start" {
		t.Fatal("nested value retained obsolete fields", err)
	}
	base := Definition{Name: "Before", OwnerAgentID: 9007199254740993}
	merged, err := mergePatch(base, map[string]json.RawMessage{"name": json.RawMessage(`"After"`)}, definitionSchema())
	if err != nil || merged.OwnerAgentID != base.OwnerAgentID {
		t.Fatal("unchanged numeric identity rounded", err)
	}
}

func TestPatchConcurrentVersionConflict(t *testing.T) {
	a, p := patchFixture(t)
	errors := make(chan error, 2)
	start := make(chan struct{})
	for _, changes := range []string{`{"description":"First"}`, `{"description":"Second"}`} {
		args := patchArgs(p, changes)
		go func() {
			<-start
			_, err := a.executeMCP(p.ProjectID, "agent:7:main", "patch", args)
			errors <- err
		}()
	}
	close(start)
	first, second := <-errors, <-errors
	if (first == nil) == (second == nil) {
		t.Fatal("exactly one concurrent edit must succeed", first, second)
	}
	got, err := a.get(p.ProjectID, p.ID)
	if err != nil || got.Version != 2 {
		t.Fatal("concurrent edit created extra revision", err)
	}
}
func TestPatchRejectsInvalidEditsAtomically(t *testing.T) {
	cases := []string{
		`{}`, `{"unknown":"bad"}`, `{"owner_agent_id":9}`, `{"instructions":null}`, `{"name":3}`, `{"instructions":""}`,
		`{"steps":[]}`, `{"steps":{}}`, `{"steps":{"update":null}}`, `{"steps":{"upsert":[]}}`,
		`{"steps":{"update":[{"key":"missing","name":"Missing"}]}}`,
		`{"steps":{"update":[{"key":"write"}]}}`,
		`{"steps":{"update":[{"key":"write","instructions":"x"},{"key":"write","name":"x"}]}}`,
		`{"steps":{"remove":["write"],"update":[{"key":"write","instructions":"x"}]}}`,
		`{"steps":{"add":[{"key":"write","instructions":"x"}]}}`,
		`{"steps":{"add":[{"key":"new"}]}}`,
		`{"steps":{"remove":["missing"]}}`, `{"steps":{"remove":["research"]}}`,
		`{"steps":{"update":[{"key":"write","depends_on":["review"]}]}}`,
		`{"steps":{"update":[{"key":"write","position":{"x":1,"y":2}}]}}`,
		`{"steps":{"update":[{"key":"write","kind":"approval"}]}}`,
		`{"parameters":{"update":[{"key":"campaign","type":"invalid"}]}}`,
		`{"parameters":{"add":[{"key":"count","type":"number","default":"bad"}]}}`,
		`{"steps":{"update":[{"key":"write","start_after":{"unexpected":true}}]}}`,
	}
	for _, changes := range cases {
		t.Run(changes, func(t *testing.T) {
			a, p := patchFixture(t)
			before := httpObject(t, a, "/processes/"+p.ID)
			if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "patch", patchArgs(p, changes)); err == nil {
				t.Fatal("invalid patch accepted")
			}
			after := httpObject(t, a, "/processes/"+p.ID)
			if before != after {
				t.Fatal("failed patch mutated procedure")
			}
			var count int
			a.db.QueryRow("SELECT COUNT(*) FROM process_versions WHERE process_id=?", p.ID).Scan(&count)
			if count != 1 {
				t.Fatal("failed patch persisted version")
			}
		})
	}
}
func TestPatchVersionAndScopeGuards(t *testing.T) {
	a, p := patchFixture(t)
	args := patchArgs(p, `{"description":"Changed"}`)
	mcpOK(t, a, p.ProjectID, "main", "patch", args)
	if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "patch", args); err == nil {
		t.Fatal("stale patch accepted")
	}
	args["expected_version"] = 2.5
	if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "patch", args); err == nil {
		t.Fatal("fractional version accepted")
	}
	args["expected_version"] = float64(2)
	if _, err := mcpAs(t, a, "other-project", 7, "main", "patch", args); err == nil {
		t.Fatal("cross-project patch accepted")
	}
	for _, state := range []string{"active", "archived"} {
		a, p := patchFixture(t)
		status(t, a, p.ID, state)
		if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "patch", patchArgs(p, `{"description":"Changed"}`)); err == nil {
			t.Fatal("state guard bypassed", state)
		}
	}
	a, p = patchFixture(t)
	a.db.Exec("UPDATE processes SET sync_pending=1 WHERE id=?", p.ID)
	if _, err := mcpAs(t, a, p.ProjectID, 7, "main", "patch", patchArgs(p, `{"description":"Changed"}`)); err == nil {
		t.Fatal("sync guard bypassed")
	}
}
func TestPatchPreservesFrozenRunAndAssignmentVersionBindings(t *testing.T) {
	d := workflowDefinition()
	a, _, p, r := executorSetup(t, d, "per_executor", nil)
	original, err := a.definition(p.ID, r.Version)
	if err != nil {
		t.Fatal(err)
	}
	before := responseJSON(t, original)
	status(t, a, p.ID, "paused")
	// A second assignment pins the old revision while the fixture follows latest.
	pinned, err := a.saveAssignment(p.ProjectID, p.ID, "", 0, AssignmentConfig{Name: "Pinned", OwnerAgentID: 7, ProcedureVersion: 1, FollowLatest: false})
	if err != nil {
		t.Fatal(err)
	}
	mcpOK(t, a, p.ProjectID, "main", "patch", patchArgs(p, `{"steps":{"update":[{"key":"write","instructions":"new draft instructions"}]}}`))
	frozen, err := a.definition(p.ID, r.Version)
	if err != nil || responseJSON(t, frozen) != before {
		t.Fatal("frozen run definition changed", err)
	}
	run, err := a.getRun(p.ProjectID, p.ID, r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range run.Steps {
		if s.Key == "write" && s.Definition.Instructions == "new draft instructions" {
			t.Fatal("active step definition changed")
		}
	}
	assignments, err := a.assignments(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range assignments {
		if x.ID == pinned.ID && x.ProcedureVersion != 1 || x.FollowLatest && x.ProcedureVersion != 2 {
			t.Fatal("assignment version drift", x)
		}
	}
}

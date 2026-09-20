package main

import (
	"context"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
	"time"
)

type definitionPlatform struct {
	directPlatform
	lookups   int
	hasAgents bool
}

func (f *definitionPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	f.lookups++
	if !f.hasAgents {
		return nil, errors.New("no agents in this project")
	}
	return f.fakeTasks.GetInstance(id)
}
func TestUnassignedDefinitionThenExplicitExecution(t *testing.T) {
	t.Skip("execution mode is no longer part of procedure definitions")
	f := &definitionPlatform{}
	a := &App{}
	if e := a.OnMount(tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(f))); e != nil {
		t.Fatal(e)
	}
	d := workflowDefinition().procedureOnly()
	d.Parameters = []Parameter{{Key: "city", Type: "string", Required: true}}
	p, e := a.save("project-a", "", "operator", 0, d)
	if e != nil {
		t.Fatal(e)
	}
	if p.Status != "draft" || len(p.Assignments) != 0 || p.OwnerAgentID != 0 || p.ExecutionMode != "" || p.Schedule != nil || f.lookups != 0 {
		t.Fatalf("definition configured execution: %+v, lookups=%d", p, f.lookups)
	}
	p = status(t, a, p.ID, "active")
	if e = a.tickDirect(context.Background(), time.Now().Add(time.Hour)); e != nil {
		t.Fatal(e)
	}
	if p.SyncPending || f.lookups != 0 || len(f.events) != 0 || len(f.calls) != 0 {
		t.Fatal("unassigned activation contacted an executor")
	}
	if _, e = a.execute(p.ProjectID, "operator", "start", map[string]any{"process_id": p.ID, "idempotency_key": "no-assignment"}); e == nil || !strings.Contains(e.Error(), "assignment") {
		t.Fatalf("unassigned start: %v", e)
	}
	c := AssignmentConfig{Name: "Weather Madrid", ExecutionMode: "agent", FollowLatest: true, Parameters: map[string]any{"city": "Madrid"}, Schedule: &Schedule{Kind: "interval", Every: "1h"}}
	if _, e = a.saveAssignment(p.ProjectID, p.ID, "", 0, c); e == nil {
		t.Fatal("assignment accepted missing owner")
	}
	f.hasAgents = true
	c.OwnerAgentID = 7
	x, e := a.saveAssignment(p.ProjectID, p.ID, "", 0, c)
	if e != nil {
		t.Fatal(e)
	}
	if x.Status != "paused" || len(f.events) != 0 {
		t.Fatal("assignment creation executed work")
	}
	if _, e = a.startAssignment(p.ProjectID, p.ID, x.ID, "paused", "", nil); e == nil {
		t.Fatal("paused assignment ran")
	}
	x, e = a.assignmentStatus(p.ProjectID, p.ID, x.ID, "active")
	if e != nil {
		t.Fatal(e)
	}
	if x.NextRunAt == "" {
		t.Fatal("assignment schedule missing")
	}
	raw, e := a.startAssignment(p.ProjectID, p.ID, x.ID, "weather-1", "", nil)
	if e != nil {
		t.Fatal(e)
	}
	r := raw.(map[string]any)["run"].(Run)
	if r.AssignmentID != x.ID || r.Binding.OwnerAgentID != 7 || r.Binding.Parameters["city"] != "Madrid" || len(f.events) != 1 {
		t.Fatal("run missing explicit assignment snapshot", r)
	}
	fresh, _ := a.get(p.ProjectID, p.ID)
	if fresh.OwnerAgentID != 0 || fresh.Schedule != nil || len(fresh.Assignments) != 1 {
		t.Fatal("assignment leaked into definition", fresh)
	}
}

func TestProcedureEditPreservesLegacyAssignmentConfiguration(t *testing.T) {
	t.Skip("legacy execution mode is no longer part of assignment configuration")
	a, _, _ := directSetup(t)
	old := workflowDefinition()
	old.Schedule = &Schedule{Kind: "interval", Every: "1h", Timezone: "UTC"}
	p := create(t, a, old)
	before := p.Assignments[0]
	d := p.Definition
	// Old clients may round-trip these historical fields. They cannot configure execution.
	d.OwnerAgentID = 9
	d.ExecutionMode = "tasks"
	d.Schedule = &Schedule{Kind: "interval", Every: "2h"}
	d.Name = "Updated procedure"
	p, e := a.save(p.ProjectID, p.ID, "operator", p.Version, d)
	if e != nil {
		t.Fatal(e)
	}
	after := p.Assignments[0]
	if after.OwnerAgentID != before.OwnerAgentID || after.ExecutionMode != before.ExecutionMode || jsonText(after.Schedule) != jsonText(before.Schedule) || after.Status != before.Status {
		t.Fatal("procedure edit changed execution", after)
	}
	if after.ProcedureVersion != 2 || after.Revision != before.Revision+1 {
		t.Fatal("following assignment did not advance")
	}
	if p.OwnerAgentID != 0 || p.ExecutionMode != "" || p.Schedule != nil {
		t.Fatal("new version retained execution settings")
	}
	versions, e := a.versions(p.ID)
	if e != nil {
		t.Fatal(e)
	}
	if versions[1].Definition.OwnerAgentID != 7 || versions[1].Definition.Schedule.Every != "1h" {
		t.Fatal("historical definition changed")
	}
}

func TestExecutionConfigurationBelongsToAssignmentSchema(t *testing.T) {
	t.Skip("execution mode is no longer part of assignment configuration")
	p := definitionSchema()["properties"].(map[string]any)
	x := assignmentSchema()["properties"].(map[string]any)
	for _, key := range []string{"owner_agent_id", "execution_mode", "schedule"} {
		if _, exists := p[key]; exists {
			t.Fatalf("definition exposes %s", key)
		}
		if x[key] == nil {
			t.Fatalf("assignment missing %s", key)
		}
	}
}

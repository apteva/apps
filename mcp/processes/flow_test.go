package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestFlowPositionsAreStrippedFromNewVersions(t *testing.T) {
	a, _, _ := directSetup(t)
	d := workflowDefinition()
	d.Steps[0].Position = &StepPosition{X: -125.5, Y: 275}
	p := create(t, a, d)
	d.Steps[0].Position = &StepPosition{X: 340, Y: 90}
	if _, err := a.save(p.ProjectID, p.ID, "operator", p.Version, d); err != nil {
		t.Fatal(err)
	}
	versions, err := a.versions(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 2 {
		t.Fatalf("versions=%d", len(versions))
	}
	for _, version := range versions {
		for _, step := range version.Definition.Steps {
			if step.Position != nil {
				t.Fatalf("version retained presentation data: %+v", version)
			}
		}
	}
}

func TestAgentSchemaDoesNotExposeGraphCoordinates(t *testing.T) {
	properties := stepSchema()["properties"].(map[string]any)
	if _, ok := properties["position"]; ok {
		t.Fatal("agent-facing step schema exposes position")
	}
	raw, err := json.Marshal(definitionSchema())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"position"`) {
		t.Fatal("definition schema contains graphical coordinates")
	}
}

func TestValidateDefinitionReportsUnenforcedApproval(t *testing.T) {
	a, _, _ := directSetup(t)
	d := workflowDefinition()
	d.ApprovalRequirements = "A manager must approve before sending."
	for i := range d.Steps {
		d.Steps[i].Kind = "work"
	}
	d.Steps[0].Position = &StepPosition{X: 0, Y: 0}
	result, err := a.execute("project-a", "agent:7:thread", "validate_definition", map[string]any{"definition": d})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(map[string]any)
	normalized := out["definition"].(Definition)
	if normalized.Steps[0].Position != nil {
		t.Fatal("validation retained legacy position")
	}
	r := out["readiness"].(Readiness)
	if !r.Valid || len(r.Warnings) != 1 || !strings.Contains(r.Warnings[0], "no enforced approval step") {
		t.Fatalf("readiness=%+v", r)
	}
}

func TestAgentCreationRecipeIsExplicit(t *testing.T) {
	a, _, _ := directSetup(t)
	descriptions := map[string]string{}
	for _, tool := range a.MCPTools() {
		descriptions[tool.Name] = tool.Description
	}
	for name, want := range map[string]string{
		"create":              "No assignment or run is created",
		"assignment_create":   "always created paused",
		"activate":            "explicit user authorization",
		"assignment_activate": "explicit user authorization",
		"start":               "explicit user authorization",
	} {
		if !strings.Contains(descriptions[name], want) {
			t.Fatalf("%s description does not explain safe deployment: %q", name, descriptions[name])
		}
	}
}

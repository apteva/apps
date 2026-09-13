package main

import (
	"math"
	"testing"
)

func TestFlowPositionsPersistPerVersion(t *testing.T) {
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
	if len(versions) != 2 || *versions[0].Definition.Steps[0].Position != *d.Steps[0].Position || *versions[1].Definition.Steps[0].Position != (StepPosition{X: -125.5, Y: 275}) {
		t.Fatalf("layout did not round trip in immutable versions: %+v", versions)
	}
	if versions[0].Definition.Steps[1].Position != nil {
		t.Fatal("legacy auto layout was replaced")
	}
}
func TestFlowRejectsInvalidCoordinates(t *testing.T) {
	for _, position := range []StepPosition{{X: math.NaN()}, {Y: math.Inf(1)}, {X: 100001}, {Y: -100001}} {
		d := workflowDefinition()
		d.Steps[0].Position = &position
		if err := validateSteps(d.Steps); err == nil {
			t.Fatalf("accepted invalid position: %+v", position)
		}
	}
}

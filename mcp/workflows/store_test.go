package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const testProj = "test-proj"

func TestCreateAndGet(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	src := []byte(validYAML)
	wf, err := dbCreateWorkflow(ctx.AppDB(), testProj, &Workflow{
		Name:       "f",
		SourceKind: "inline",
		Source:     string(src),
		SourceHash: hashSource(src),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if wf.ID == 0 {
		t.Error("zero id")
	}

	byID, err := dbGetWorkflow(ctx.AppDB(), testProj, wf.ID, "")
	if err != nil || byID == nil {
		t.Fatalf("get id: %v / %v", err, byID)
	}
	byName, err := dbGetWorkflow(ctx.AppDB(), testProj, 0, "f")
	if err != nil || byName == nil {
		t.Fatalf("get name: %v / %v", err, byName)
	}
}

func TestNameValidation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	bad := []string{"", "Has-Caps", "with/slash", "with space"}
	for _, n := range bad {
		_, err := dbCreateWorkflow(ctx.AppDB(), testProj, &Workflow{
			Name: n, SourceKind: "inline", Source: "x", SourceHash: "h",
		})
		if err == nil {
			t.Errorf("expected error for name %q", n)
		}
	}
}

func TestProjectPartition(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("a"))
	_, err := dbCreateWorkflow(ctx.AppDB(), "a", &Workflow{
		Name: "wf", SourceKind: "inline", Source: "x", SourceHash: "h",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, _ := dbGetWorkflow(ctx.AppDB(), "b", 0, "wf")
	if got != nil {
		t.Error("cross-project lookup should return nil")
	}
}

func TestUpdateBumpsVersion(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	wf, err := dbCreateWorkflow(ctx.AppDB(), testProj, &Workflow{
		Name: "v", SourceKind: "inline", Source: "x", SourceHash: hashSource([]byte("x")),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if wf.Version != 1 {
		t.Errorf("initial version = %d", wf.Version)
	}

	updated, err := dbUpdateWorkflow(ctx.AppDB(), testProj, wf.ID, map[string]any{"source": "y"}, hashSource([]byte("y")))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("after source update version = %d, want 2", updated.Version)
	}

	// Non-source update should NOT bump version.
	updated, err = dbUpdateWorkflow(ctx.AppDB(), testProj, wf.ID, map[string]any{"status": "disabled"}, "")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Version != 2 {
		t.Errorf("status-only update bumped version to %d, want 2", updated.Version)
	}
}

package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func linkedAssemblyDefinition(t *testing.T, source *Design, translateX float64) []byte {
	t.Helper()
	definition := DesignDefinition{
		Schema: designSchema, Units: "mm", Operations: []map[string]any{},
		Parts: []DesignPart{{ID: "linked_plate", Name: "Linked plate", Source: &PartSourceReference{
			DesignID: source.ID, RevisionID: source.CurrentRevision.ID, SourceSHA256: source.CurrentRevision.SourceSHA256,
		}}},
		Assembly: &AssemblySpec{Name: "Linked assembly", Instances: []AssemblyInstance{
			{ID: "plate_a", PartID: "linked_plate"},
			{ID: "plate_b", PartID: "linked_plate", Transform: AssemblyTransform{Translate: []any{translateX, 0, 0}}},
		}},
	}
	body, err := json.Marshal(definition)
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := normalizeDefinition(body, 256)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func TestLinkedComponentBuildPinsAndRefreshesRevision(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	store := testStore(t)
	var componentDocument map[string]any
	if err := json.Unmarshal(testDefinition(), &componentDocument); err != nil {
		t.Fatal(err)
	}
	componentDocument["materials"] = []any{map[string]any{"id": "petg", "name": "PETG", "kind": "polymer", "density_g_cm3": 1.24}}
	componentDocument["parts"] = []any{map[string]any{"id": "plate", "name": "Reusable plate", "output": "result", "material_id": "petg"}}
	componentBody, _ := json.Marshal(componentDocument)
	componentDefinition, componentSpec, err := normalizeDefinition(componentBody, 256)
	if err != nil {
		t.Fatal(err)
	}
	componentParameters, err := normalizeParameters([]byte(`{"width":40}`), componentSpec)
	if err != nil {
		t.Fatal(err)
	}
	component, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Reusable plate", Definition: componentDefinition, Parameters: componentParameters})
	if err != nil {
		t.Fatal(err)
	}
	assemblyDefinition := linkedAssemblyDefinition(t, component, 60)
	assembly, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Plate assembly", Definition: assemblyDefinition, Parameters: []byte(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	engine, err := NewEngine(filepath.Join(root, "engine"), bun, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, engine: engine, project: "project-a", artifactRoot: filepath.Join(root, "artifacts"), maxOperations: 256}
	first, err := service.Build(context.Background(), assembly.ID, 0, []string{"mesh-json", "step"})
	if err != nil {
		t.Fatal(err)
	}
	if first.Report.BodyCount != 2 || first.Report.MassG <= 0 || len(first.Dependencies) != 1 || first.Dependencies[0].SourceRevisionID != component.CurrentRevisionID {
		t.Fatalf("linked build did not retain component identity: %#v", first)
	}
	firstWidth := first.Report.Bounds.Size[0]

	updatedParameters, _ := normalizeParameters([]byte(`{"width":55}`), componentSpec)
	componentRevision, err := store.CreateRevision("project-a", CreateRevisionInput{
		DesignID: component.ID, ExpectedParent: component.CurrentRevisionID, Definition: componentDefinition, Parameters: updatedParameters,
	})
	if err != nil {
		t.Fatal(err)
	}
	pinned, err := service.Build(context.Background(), assembly.ID, 0, []string{"mesh-json"})
	if err != nil {
		t.Fatal(err)
	}
	if pinned.Report.Bounds.Size[0] != firstWidth || pinned.Dependencies[0].SourceRevisionID == componentRevision.ID {
		t.Fatal("existing assembly revision followed a moving component revision")
	}
	refreshed, updates, err := service.RefreshComponentSources(assembly.ID, assembly.CurrentRevisionID, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if len(updates) != 1 || refreshed.RevisionNumber != 2 || updates[0].ToRevisionID != componentRevision.ID {
		t.Fatalf("unexpected refresh: revision=%#v updates=%#v", refreshed, updates)
	}
	next, err := service.Build(context.Background(), assembly.ID, refreshed.ID, []string{"mesh-json"})
	if err != nil {
		t.Fatal(err)
	}
	if next.Report.Bounds.Size[0] <= firstWidth || next.Dependencies[0].SourceRevisionID != componentRevision.ID {
		t.Fatalf("refreshed assembly did not use new geometry: old=%v new=%v dependencies=%#v", firstWidth, next.Report.Bounds.Size[0], next.Dependencies)
	}
	manufacturing, err := service.ManufacturingPackage(context.Background(), assembly.ID, refreshed.ID)
	if err != nil {
		t.Fatal(err)
	}
	packageBody, err := os.ReadFile(manufacturing.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(packageBody), int64(len(packageBody)))
	if err != nil {
		t.Fatal(err)
	}
	var packaged []ComponentDependency
	for _, file := range archive.File {
		if file.Name != "dependencies.json" {
			continue
		}
		reader, openErr := file.Open()
		if openErr != nil {
			t.Fatal(openErr)
		}
		body, readErr := io.ReadAll(reader)
		reader.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		if err := json.Unmarshal(body, &packaged); err != nil {
			t.Fatal(err)
		}
	}
	if len(packaged) != 1 || packaged[0].SourceRevisionID != componentRevision.ID {
		t.Fatalf("manufacturing package lost dependency lock: %#v", packaged)
	}
}

func TestLinkedComponentRejectsHashMismatchAndCrossProjectReference(t *testing.T) {
	store := testStore(t)
	componentDefinition, componentSpec, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, _ := normalizeParameters(nil, componentSpec)
	component, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Part", Definition: componentDefinition, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, project: "project-a", maxOperations: 256}
	definition := linkedAssemblyDefinition(t, component, 60)
	var raw map[string]any
	_ = json.Unmarshal(definition, &raw)
	parts := raw["parts"].([]any)
	parts[0].(map[string]any)["source"].(map[string]any)["source_sha256"] = strings.Repeat("0", 64)
	tampered, _ := json.Marshal(raw)
	canonical, _, err := normalizeDefinition(tampered, 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := service.materializeComponentSources(0, canonical); err == nil || !strings.Contains(err.Error(), "hash mismatch") {
		t.Fatalf("expected hash mismatch, got %v", err)
	}
	crossProject := &Service{store: store, project: "project-b", maxOperations: 256}
	if _, _, err := crossProject.materializeComponentSources(0, definition); err == nil {
		t.Fatal("cross-project component reference was accepted")
	}
}

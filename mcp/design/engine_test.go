package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestEngineBuildsRealBRepAndExports(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	root := t.TempDir()
	engine, err := NewEngine(root, bun, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters([]byte(`{"width":40}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Build(context.Background(), "Test plate", canonical, parameters, []string{"mesh-json", "step", "stl"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.Valid || result.Report.VolumeMM3 <= 0 || result.Report.TriangleCount <= 0 {
		t.Fatalf("invalid report: %#v", result.Report)
	}
	formats := map[string]string{}
	for _, artifact := range result.Artifacts {
		formats[artifact.Format] = artifact.Path
	}
	for _, format := range []string{"mesh-json", "step", "stl"} {
		path := formats[format]
		if path == "" {
			t.Errorf("missing %s artifact", format)
			continue
		}
		info, err := os.Stat(path)
		if err != nil || info.Size() == 0 {
			t.Errorf("empty %s artifact at %s", format, path)
		}
	}
	meshBody, _ := os.ReadFile(formats["mesh-json"])
	var mesh meshDocument
	if err := json.Unmarshal(meshBody, &mesh); err != nil || len(mesh.Triangles) == 0 {
		t.Fatalf("invalid mesh artifact: %v", err)
	}
	if filepath.Ext(formats["step"]) != ".step" {
		t.Fatalf("unexpected STEP path %s", formats["step"])
	}
}

func TestServiceBuildAndManufacturingPackage(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	store := testStore(t)
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters([]byte(`{"width":48}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Production plate", Definition: canonical, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	engine, err := NewEngine(filepath.Join(root, "engine"), bun, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, engine: engine, project: "project-a", artifactRoot: filepath.Join(root, "artifacts"), maxOperations: 256}
	result, err := service.Build(context.Background(), design.ID, 0, []string{"step", "stl", "3mf", "glb"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "passed" || len(result.Artifacts) != 5 {
		t.Fatalf("unexpected build result: status=%s artifacts=%d", result.Run.Status, len(result.Artifacts))
	}
	for _, artifact := range result.Artifacts {
		if artifact.SHA256 == "" || artifact.SizeBytes == 0 {
			t.Errorf("invalid persisted artifact: %#v", artifact)
		}
	}
	manufacturing, err := service.ManufacturingPackage(context.Background(), design.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if manufacturing.Kind != "manufacturing-package" || manufacturing.Format != "zip" {
		t.Fatalf("unexpected manufacturing artifact: %#v", manufacturing)
	}
	body, err := os.ReadFile(manufacturing.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, file := range archive.File {
		found[file.Name] = true
	}
	for _, name := range []string{"manifest.json", "design.json", "parameters.json", "validation/report.json", "validation/checks.json"} {
		if !found[name] {
			t.Errorf("manufacturing package missing %s", name)
		}
	}
}

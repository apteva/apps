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

func TestOpenRoverDefinitionContract(t *testing.T) {
	raw, err := json.Marshal(openRoverExample())
	if err != nil {
		t.Fatal(err)
	}
	canonical, definition, err := normalizeDefinition(raw, 256)
	if err != nil {
		t.Fatal(err)
	}
	if len(canonical) == 0 || definition.OpenHardware == nil || definition.OpenHardware.License != "CERN-OHL-S-2.0" {
		t.Fatalf("open hardware metadata=%#v", definition.OpenHardware)
	}
	if len(definition.Parts) < 8 || definition.Assembly == nil || len(definition.Assembly.Instances) < 16 {
		t.Fatalf("incomplete rover assembly: parts=%d assembly=%#v", len(definition.Parts), definition.Assembly)
	}
	if len(definition.Assembly.Interfaces) < 5 || len(definition.Assembly.Joints) != 4 || len(definition.BOM) < 4 {
		t.Fatalf("incomplete interfaces, joints, or BOM")
	}
}

func TestOpenRoverBuildsAssemblyAndManufacturingPackage(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	raw, _ := json.Marshal(openRoverExample())
	canonical, definition, err := normalizeDefinition(raw, 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters(nil, definition)
	if err != nil {
		t.Fatal(err)
	}
	store := testStore(t)
	design, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Apteva Open Rover", Kind: "parametric", Definition: canonical, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	engine, err := NewEngine(filepath.Join(root, "engine"), bun, 90*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, engine: engine, project: "project-a", artifactRoot: filepath.Join(root, "artifacts"), maxOperations: 256}
	result, err := service.Build(context.Background(), design.ID, 0, []string{"step", "stl", "3mf", "glb"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Run.Status != "passed" || result.Report.BodyCount < 16 || len(result.Report.Parts) < 16 {
		t.Fatalf("unexpected rover build: status=%s bodies=%d parts=%d checks=%#v", result.Run.Status, result.Report.BodyCount, len(result.Report.Parts), result.Checks)
	}
	if result.Report.MassG <= 0 || result.Report.CenterOfMass == [3]float64{} {
		t.Fatalf("missing mass properties: %#v", result.Report)
	}
	partExports := 0
	for _, artifact := range result.Artifacts {
		var metadata map[string]any
		_ = json.Unmarshal(artifact.Metadata, &metadata)
		if metadata["part_id"] != nil {
			partExports++
		}
	}
	if partExports < 20 {
		t.Fatalf("got %d per-part exports, want at least 20", partExports)
	}

	manufacturing, err := service.ManufacturingPackage(context.Background(), design.ID, 0)
	if err != nil {
		t.Fatal(err)
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
	for _, name := range []string{"README.md", "ASSEMBLY.md", "LICENSE.spdx", "open-hardware.json", "assembly.json", "dependencies.json", "bom.csv", "print-profiles.json"} {
		if !found[name] {
			t.Errorf("open-hardware package missing %s", name)
		}
	}
}

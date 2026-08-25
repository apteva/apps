package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"sort"
	"strings"
	"testing"
)

func TestDeterministicNativeArtifacts(t *testing.T) {
	def := testDefinition()
	svgA, svgB := renderSVG(&def), renderSVG(&def)
	if !bytes.Equal(svgA, svgB) || !bytes.Contains(svgA, []byte("trace-signal")) {
		t.Fatal("SVG rendering is not deterministic or lacks native IDs")
	}
	bom := renderBOM(&def)
	if !bytes.Contains(bom, []byte("RC0805FR-0710KL")) || !bytes.Contains(bom, []byte("R1")) {
		t.Fatalf("unexpected BOM: %s", bom)
	}
}
func TestNativeManufacturingSetIsDeterministic(t *testing.T) {
	def := manufacturablePairDefinition()
	a, err := zipManufacturingSet(&def)
	if err != nil {
		t.Fatal(err)
	}
	b, err := zipManufacturingSet(&def)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatal("manufacturing ZIP is not deterministic")
	}
	files := manufacturingFiles(&def)
	for _, name := range []string{"gerbers/F_Cu.gbr", "gerbers/B_Cu.gbr", "gerbers/Edge_Cuts.gbr", "drill/board.drl", "board.gbrjob"} {
		if len(files[name]) == 0 {
			t.Errorf("missing manufacturing file %s", name)
		}
	}
	if !bytes.Contains(files["gerbers/F_Cu.gbr"], []byte("G36*")) || !bytes.Contains(files["drill/board.drl"], []byte("M48")) {
		t.Fatal("manufacturing files do not contain native Gerber/Excellon commands")
	}
}
func TestReleaseIsStableAndContainsTraceability(t *testing.T) {
	store := testStore(t)
	d := createTestDesign(t, store, "project-a")
	s := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	manufacturing, err := s.Manufacturing(d.ID, 0)
	if err != nil {
		t.Fatal(err)
	}
	first, err := s.Release(context.Background(), d.ID, 0, "release candidate")
	if err != nil {
		t.Fatal(err)
	}
	if manufacturing.LocalPath == first.LocalPath || manufacturing.Name == first.Name {
		t.Fatalf("manufacturing and release artifacts collide: %q / %q", manufacturing.LocalPath, first.LocalPath)
	}
	if !strings.HasSuffix(manufacturing.Name, "-r1-manufacturing.zip") || !strings.HasSuffix(first.Name, "-r1-release.zip") {
		t.Fatalf("unexpected package names: manufacturing=%q release=%q", manufacturing.Name, first.Name)
	}
	firstBody, err := os.ReadFile(first.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.Release(context.Background(), d.ID, 0, "release candidate")
	if err != nil {
		t.Fatal(err)
	}
	secondBody, err := os.ReadFile(second.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(firstBody, secondBody) || first.SHA256 != second.SHA256 {
		t.Fatal("same immutable revision produced a different release")
	}
	zr, err := zip.NewReader(bytes.NewReader(firstBody), int64(len(firstBody)))
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, f := range zr.File {
		names = append(names, f.Name)
	}
	sort.Strings(names)
	want := []string{"manifest.json", "manufacturing/board.gbrjob", "manufacturing/drill/board.drl", "manufacturing/gerbers/B_Cu.gbr", "manufacturing/gerbers/Edge_Cuts.gbr", "manufacturing/gerbers/F_Cu.gbr", "outputs/board.svg", "outputs/bom.csv", "source/pcb.json", "validation/report.json", "verification/fabrication.json"}
	if len(names) != len(want) {
		t.Fatalf("release files: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("release files: %v", names)
		}
	}
}

func TestWiringServiceExportsAndRelease(t *testing.T) {
	store := testStore(t)
	def := arduinoLEDExample()
	canonical, _, hash, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("project-a", def.Name, canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	for _, format := range []string{"svg", "png", "tutorial-json", "tutorial-zip"} {
		artifact, err := service.WiringExport(design.ID, 0, format)
		if err != nil {
			t.Fatalf("%s: %v", format, err)
		}
		if artifact.SizeBytes == 0 {
			t.Fatalf("%s empty", format)
		}
	}
	run, err := service.WiringSimulate(design.ID, 0, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	if active, _ := run.Result.PartStates["led1"]["active"].(bool); !active {
		t.Fatal("wired LED inactive")
	}
	release, err := service.Release(context.Background(), design.ID, 0, "wiring tutorial")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(release.LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, name := range []string{"wiring/illustration.svg", "wiring/illustration.png", "wiring/tutorial.json"} {
		if !names[name] {
			t.Errorf("release missing %s", name)
		}
	}
}

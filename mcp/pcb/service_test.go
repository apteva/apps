package main

import (
	"archive/zip"
	"bytes"
	"context"
	"os"
	"sort"
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
func TestReleaseIsStableAndContainsTraceability(t *testing.T) {
	store := testStore(t)
	d := createTestDesign(t, store, "project-a")
	s := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	first, err := s.Release(context.Background(), d.ID, 0, "release candidate")
	if err != nil {
		t.Fatal(err)
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
	want := []string{"manifest.json", "outputs/board.svg", "outputs/bom.csv", "source/pcb.json", "validation/report.json"}
	if len(names) != len(want) {
		t.Fatalf("release files: %v", names)
	}
	for i := range want {
		if names[i] != want[i] {
			t.Fatalf("release files: %v", names)
		}
	}
}

package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestRaspberryPiMechanicalExampleIsHandoffReady(t *testing.T) {
	def := raspberryPi4MechanicalExample()
	canonical, normalized, _, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(canonical, []byte(`"schema":"apteva-pcb-mechanical/v1"`)) {
		t.Fatal("canonical definition lost mechanical schema")
	}
	report := validateMechanical(normalized)
	if report.Status != "passed" || report.Errors != 0 || report.Warnings != 0 {
		t.Fatalf("validation=%s errors=%d warnings=%d checks=%#v", report.Status, report.Errors, report.Warnings, report.Checks)
	}
	if report.Metrics["mounting_holes"] != 4 || report.Metrics["component_envelopes"] < 10 || report.Metrics["panel_openings"] < 8 || report.Metrics["clearance_zones"] < 8 {
		t.Fatalf("mechanical metrics=%#v", report.Metrics)
	}
	if normalized.Board.WidthNM != 85_000_000 || normalized.Board.HeightNM != 56_000_000 || normalized.Board.ThicknessNM != 1_600_000 {
		t.Fatalf("board=%#v", normalized.Board)
	}
	if len(normalized.Board.Outline) != 12 || report.Metrics["modeled_components"] < 10 || normalized.Mechanical.Tolerances.XYNM == 0 {
		t.Fatalf("0.7 mechanical detail missing: outline=%d metrics=%#v tolerances=%#v", len(normalized.Board.Outline), report.Metrics, normalized.Mechanical.Tolerances)
	}
}

func TestMechanicalEnvelopeAndPreviewAreDeterministic(t *testing.T) {
	def := raspberryPi4MechanicalExample()
	design := &Design{ID: 42, Name: def.Name}
	revision := &Revision{ID: 7, DesignID: 42, Number: 3, SourceSHA256: "abc123"}
	one, two := mechanicalJSON(design, revision, &def), mechanicalJSON(design, revision, &def)
	if !bytes.Equal(one, two) {
		t.Fatal("mechanical envelope is not deterministic")
	}
	var envelope MechanicalEnvelopeArtifact
	if err := json.Unmarshal(one, &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.Schema != mechanicalEnvelopeSchema || envelope.SourceDesignID != 42 || envelope.SourceRevisionID != 7 || envelope.SourceSHA256 != "abc123" {
		t.Fatalf("envelope provenance design=%d revision=%d hash=%q", envelope.SourceDesignID, envelope.SourceRevisionID, envelope.SourceSHA256)
	}
	svg := string(renderMechanicalSVG(&def))
	for _, want := range []string{"PCB mechanical 3D envelope", "Ethernet", "GPIO", "Validated mechanical producer model"} {
		if !strings.Contains(svg, want) {
			t.Fatalf("preview missing %q", want)
		}
	}
}

func TestMechanicalMountingHolesReachFabricationOutputs(t *testing.T) {
	def := raspberryPi4MechanicalExample()
	edge := string(renderEdgeGerber(&def))
	if !strings.Contains(edge, "X3000000Y0") {
		t.Fatal("custom board outline missing from Edge.Cuts")
	}
	drill := string(renderExcellon(&def))
	if !strings.Contains(drill, "C2.700000") {
		t.Fatal("2.7 mm mounting drill missing from Excellon")
	}
	if got := strings.Count(drill, "X3.500000Y3.500000") + strings.Count(drill, "X61.500000Y3.500000") + strings.Count(drill, "X3.500000Y52.500000") + strings.Count(drill, "X61.500000Y52.500000"); got != 4 {
		t.Fatalf("mounting holes in Excellon=%d", got)
	}
}

func TestMechanicalValidationRejectsInvalidEnvelope(t *testing.T) {
	def := raspberryPi4MechanicalExample()
	def.Mechanical.ComponentEnvelopes[0].ZMaxNM = def.Mechanical.ComponentEnvelopes[0].ZMinNM
	report := validateMechanical(&def)
	if report.Status != "failed" || report.Errors == 0 {
		t.Fatalf("report=%#v", report)
	}
}

func TestMechanicalSolidExportsAreDeterministic(t *testing.T) {
	def := raspberryPi4MechanicalExample()
	stl := renderMechanicalSTL(&def)
	if !bytes.Contains(stl, []byte("solid apteva_pcb_mechanical_assembly")) || !bytes.Contains(stl, []byte("facet normal")) {
		t.Fatal("STL reference assembly is incomplete")
	}
	threeMF, err := renderMechanical3MF(&def)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := renderMechanical3MF(&def)
	if !bytes.Equal(threeMF, again) {
		t.Fatal("3MF output is not deterministic")
	}
	zr, err := zip.NewReader(bytes.NewReader(threeMF), int64(len(threeMF)))
	if err != nil || len(zr.File) != 3 {
		t.Fatalf("invalid 3MF package: files=%d err=%v", len(zr.File), err)
	}
	dxf := renderMechanicalDXF(&def)
	for _, want := range [][]byte{[]byte("BOARD_OUTLINE"), []byte("MOUNTING_HOLES"), []byte("PANEL_OPENING")} {
		if !bytes.Contains(dxf, want) {
			t.Fatalf("DXF missing %q", want)
		}
	}
	step := renderMechanicalSTEP(&def)
	if err := verifyMechanicalSTEP(&def, step); err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(step, []byte("AP242_MANAGED_MODEL_BASED_3D_ENGINEERING")) || !bytes.Contains(step, []byte("FACETED_BREP('PCB'")) {
		t.Fatal("STEP AP242 assembly is incomplete")
	}
}

func TestMechanicalCollisionUsesRotation(t *testing.T) {
	a := ComponentEnvelope{XNM: 10_000_000, YNM: 10_000_000, WidthNM: 12_000_000, DepthNM: 2_000_000, RotationUdeg: 45_000_000}
	b := ComponentEnvelope{XNM: 10_000_000, YNM: 14_000_000, WidthNM: 12_000_000, DepthNM: 2_000_000, RotationUdeg: -45_000_000}
	if !orientedRectanglesOverlap(a, b) {
		t.Fatal("rotated collision was not detected")
	}
	b.YNM = 30_000_000
	if orientedRectanglesOverlap(a, b) {
		t.Fatal("separated rotated solids reported a collision")
	}
}

func TestDHT22SensorNodeIsElectricalAndMechanical(t *testing.T) {
	def := dht22SensorNodeExample()
	_, normalized, _, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	mechanical := validateMechanical(normalized)
	if mechanical.Status != "passed" || mechanical.Errors != 0 {
		t.Fatalf("mechanical validation=%#v", mechanical)
	}
	electrical := validateDefinition(normalized)
	if electrical.Errors != 0 {
		t.Fatalf("electrical errors=%d checks=%#v", electrical.Errors, electrical.Checks)
	}
	foundDHT, foundUSB, foundService := false, false, false
	for _, component := range normalized.Components {
		switch component.ID {
		case "u4":
			foundDHT = component.Value == "DHT22" && len(component.Pads) == 4
		case "j1":
			foundUSB = true
		case "j2":
			foundService = len(component.Pads) == 4
		}
	}
	if !foundDHT || !foundUSB || !foundService {
		t.Fatalf("missing physical parts: dht=%v usb=%v service=%v", foundDHT, foundUSB, foundService)
	}
	readiness := productionReadiness(normalized)
	if readiness.Status != "ready_for_prototype" || readiness.Errors != 0 || readiness.Metrics["test_points"] != 4 {
		t.Fatalf("production readiness=%#v", readiness)
	}
}

package main

import (
	"bytes"
	"testing"
)

func TestFabricationVerificationParsesAndReconcilesNativeOutputs(t *testing.T) {
	def := sensorNodeExample()
	report := verifyManufacturingFiles(&def, manufacturingFiles(&def))
	if report.Status != "passed" || report.Errors != 0 {
		t.Fatalf("verification failed: %#v", report)
	}
	if report.Summary["holes"] != expectedDrillCount(&def) || report.Summary["draws"] == 0 || len(report.Files) < 5 {
		t.Fatalf("verification metrics do not reconcile: %#v", report)
	}
}

func TestFabricationVerificationRejectsCorruptedIndependentParse(t *testing.T) {
	def := sensorNodeExample()
	files := manufacturingFiles(&def)
	files["gerbers/F_Cu.gbr"] = bytes.Replace(files["gerbers/F_Cu.gbr"], []byte("M02*"), []byte("G04 missing end*"), 1)
	files["drill/board.drl"] = bytes.Replace(files["drill/board.drl"], []byte("M30"), []byte("X999.000000Y999.000000"), 1)
	report := verifyManufacturingFiles(&def, files)
	if report.Status != "failed" || report.Errors < 2 {
		t.Fatalf("corruption was accepted: %#v", report)
	}
	codes := map[string]bool{}
	for _, check := range report.Checks {
		codes[check.Code] = true
	}
	if !codes["FAB_GERBER_EOF"] || !codes["FAB_EXCELLON_EOF"] || !codes["FAB_EXCELLON_BOUNDS"] {
		t.Fatalf("expected independent parser failures, got %#v", report.Checks)
	}
}

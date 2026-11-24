package main

import (
	"bytes"
	"testing"
)

func TestKiCadExchangeIsDeterministicAndReconciled(t *testing.T) {
	def := dht22SensorNodeExample()
	one, two := renderKiCadPCB(&def), renderKiCadPCB(&def)
	if !bytes.Equal(one, two) {
		t.Fatal("KiCad exchange is not deterministic")
	}
	report := verifyKiCadPCB(&def, one)
	if report.Status != "passed" || report.Errors != 0 {
		t.Fatalf("KiCad verification=%#v", report)
	}
	if report.Counts["footprints"] != len(def.Components) || report.Counts["edge_lines"] != len(def.Board.Outline) {
		t.Fatalf("KiCad counts=%#v", report.Counts)
	}
	imported, err := importKiCadPCB("Round trip", one)
	if err != nil {
		t.Fatal(err)
	}
	if len(imported.Components) != len(def.Components) || len(imported.Nets) != len(def.Nets) || len(imported.Vias) != len(def.Vias) {
		t.Fatalf("round trip components=%d/%d nets=%d/%d vias=%d/%d", len(imported.Components), len(def.Components), len(imported.Nets), len(def.Nets), len(imported.Vias), len(def.Vias))
	}
}

func TestKiCadParserRejectsMalformedInput(t *testing.T) {
	if _, err := importKiCadPCB("Broken", []byte(`(kicad_pcb (version 20240108)`)); err == nil {
		t.Fatal("malformed KiCad source was accepted")
	}
}

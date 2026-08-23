package main

import "testing"

func TestNativeValidationPassesExample(t *testing.T) {
	def := testDefinition()
	report := validateDefinition(&def)
	if report.Status == "failed" {
		t.Fatalf("example failed native validation: %#v", report.Checks)
	}
	if report.Metrics.Components != 2 || report.Metrics.Pins != 4 {
		t.Fatalf("unexpected metrics: %#v", report.Metrics)
	}
}
func TestNativeValidationFindsElectricalAndCopperErrors(t *testing.T) {
	def := testDefinition()
	def.Nets = append(def.Nets, Net{ID: "other", Name: "OTHER", Nodes: []Node{{ComponentID: "missing", PinID: "1"}}})
	def.Traces = append(def.Traces, Trace{ID: "other-trace", NetID: "other", Layer: "F.Cu", WidthNM: 100_000, Points: []Point{{XNM: 12_000_000, YNM: 12_100_000}, {XNM: 28_000_000, YNM: 12_100_000}}})
	report := validateDefinition(&def)
	if report.Status != "failed" || report.Errors < 3 {
		t.Fatalf("expected several native violations, got %#v", report)
	}
	codes := map[string]bool{}
	for _, c := range report.Checks {
		codes[c.Code] = true
	}
	for _, code := range []string{"NET_COMPONENT_MISSING", "TRACE_WIDTH", "TRACE_CLEARANCE"} {
		if !codes[code] {
			t.Errorf("missing %s", code)
		}
	}
}
func TestSegmentClearanceGeometry(t *testing.T) {
	a := Trace{Points: []Point{{0, 0}, {10_000_000, 0}}}
	b := Trace{Points: []Point{{5_000_000, -1_000_000}, {5_000_000, 1_000_000}}}
	if !tracesCloserThan(a, b, 1) {
		t.Fatal("crossing segments should have zero clearance")
	}
	b.Points = []Point{{0, 2_000_000}, {10_000_000, 2_000_000}}
	if tracesCloserThan(a, b, 1_000_000) {
		t.Fatal("separated segments reported too close")
	}
}

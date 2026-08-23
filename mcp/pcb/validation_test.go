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

func TestManufacturingValidationChecksPadsDifferentialPairZoneAndKeepout(t *testing.T) {
	def := manufacturablePairDefinition()
	report := validateDefinition(&def)
	if report.Errors != 0 {
		t.Fatalf("manufacturable pair failed: %#v", report.Checks)
	}
	if report.Metrics.Pads != 4 || report.Metrics.Zones != 1 || report.Metrics.Keepouts != 1 || report.Metrics.DifferentialPairs != 1 {
		t.Fatalf("missing manufacturing metrics: %#v", report.Metrics)
	}

	def.Traces[1].Points[0].YNM += 2_000_000
	def.Traces[1].Points[1].YNM += 2_000_000
	report = validateDefinition(&def)
	codes := map[string]bool{}
	for _, check := range report.Checks {
		codes[check.Code] = true
	}
	for _, code := range []string{"PAD_UNROUTED", "DIFF_PAIR_GAP"} {
		if !codes[code] {
			t.Errorf("missing %s after routing defect: %#v", code, report.Checks)
		}
	}
}

func TestSensorNodeAcceptanceDesignIsManufacturable(t *testing.T) {
	def := sensorNodeExample()
	report := validateDefinition(&def)
	if report.Status == "failed" {
		t.Fatalf("sensor node failed manufacturing validation (%d errors): %#v", report.Errors, report.Checks)
	}
	if report.Metrics.Components < 10 || report.Metrics.Pads < 30 || report.Metrics.DifferentialPairs != 1 || report.Metrics.Zones != 1 {
		t.Fatalf("sensor acceptance design is not a meaningful manufacturing fixture: %#v", report.Metrics)
	}
	files := manufacturingFiles(&def)
	if len(files["gerbers/F_Cu.gbr"]) < 1_000 || len(files["drill/board.drl"]) < 100 {
		t.Fatalf("sensor manufacturing output is unexpectedly small")
	}
}

func manufacturablePairDefinition() Definition {
	def := emptyDefinition("Differential pair fixture")
	def.Board.WidthNM, def.Board.HeightNM = 30_000_000, 20_000_000
	def.Components = []Component{
		{ID: "j1", Designator: "J1", Name: "USB", Footprint: "USB", Position: Position{XNM: 5_000_000, YNM: 10_000_000, Side: "front"}, Body: &Body{WidthNM: 3_000_000, HeightNM: 3_000_000}, Pins: []Pin{{ID: "dp", Pad: "dp"}, {ID: "dm", Pad: "dm"}}, Pads: []Pad{{ID: "dp", PinID: "dp", Shape: "rect", YNM: -350_000, WidthNM: 400_000, HeightNM: 200_000, Layers: []string{"F.Cu"}}, {ID: "dm", PinID: "dm", Shape: "rect", YNM: 350_000, WidthNM: 400_000, HeightNM: 200_000, Layers: []string{"F.Cu"}}}},
		{ID: "u1", Designator: "U1", Name: "MCU", Footprint: "QFN", Position: Position{XNM: 25_000_000, YNM: 10_000_000, Side: "front"}, Body: &Body{WidthNM: 3_000_000, HeightNM: 3_000_000}, Pins: []Pin{{ID: "dp", Pad: "dp"}, {ID: "dm", Pad: "dm"}}, Pads: []Pad{{ID: "dp", PinID: "dp", Shape: "rect", YNM: -350_000, WidthNM: 400_000, HeightNM: 200_000, Layers: []string{"F.Cu"}}, {ID: "dm", PinID: "dm", Shape: "rect", YNM: 350_000, WidthNM: 400_000, HeightNM: 200_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{
		{ID: "usb_dp", Name: "USB_D+", Nodes: []Node{{ComponentID: "j1", PinID: "dp"}, {ComponentID: "u1", PinID: "dp"}}},
		{ID: "usb_dm", Name: "USB_D-", Nodes: []Node{{ComponentID: "j1", PinID: "dm"}, {ComponentID: "u1", PinID: "dm"}}},
		{ID: "gnd", Name: "GND"},
	}
	def.Traces = []Trace{
		{ID: "dp", NetID: "usb_dp", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{XNM: 5_000_000, YNM: 9_650_000}, {XNM: 25_000_000, YNM: 9_650_000}}},
		{ID: "dm", NetID: "usb_dm", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{XNM: 5_000_000, YNM: 10_350_000}, {XNM: 25_000_000, YNM: 10_350_000}}},
	}
	def.Zones = []Zone{{ID: "gnd_zone", NetID: "gnd", Layer: "B.Cu", ClearanceNM: 200_000, Polygon: []Point{{XNM: 1_000_000, YNM: 15_000_000}, {XNM: 29_000_000, YNM: 15_000_000}, {XNM: 29_000_000, YNM: 19_000_000}, {XNM: 1_000_000, YNM: 19_000_000}}}}
	def.Keepouts = []Keepout{{ID: "antenna", Kind: "antenna", Polygon: []Point{{XNM: 10_000_000, YNM: 1_000_000}, {XNM: 20_000_000, YNM: 1_000_000}, {XNM: 20_000_000, YNM: 4_000_000}, {XNM: 10_000_000, YNM: 4_000_000}}}}
	def.DifferentialPairs = []DifferentialPair{{ID: "usb", PositiveNetID: "usb_dp", NegativeNetID: "usb_dm", TargetOhms: 90, GapNM: 500_000, GapToleranceNM: 50_000, MaxSkewNM: 100_000}}
	return def
}

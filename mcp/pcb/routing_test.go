package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestSuggestRoutesDeterministicAndApplicable(t *testing.T) {
	def := emptyDefinition("route test")
	def.Components = []Component{
		{ID: "j1", Designator: "J1", Name: "source", Position: Position{XNM: 5_000_000, YNM: 10_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1", Pad: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "roundrect", WidthNM: 800_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
		{ID: "u1", Designator: "U1", Name: "target", Position: Position{XNM: 40_000_000, YNM: 20_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1", Pad: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "roundrect", WidthNM: 800_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{{ID: "signal", Name: "SIGNAL", Nodes: []Node{{ComponentID: "j1", PinID: "1"}, {ComponentID: "u1", PinID: "1"}}}}
	options := RouteOptions{GridNM: 500_000, Layers: []string{"F.Cu"}}
	first, err := suggestRoutes(&def, options)
	if err != nil {
		t.Fatal(err)
	}
	second, err := suggestRoutes(&def, options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Status != "passed" || len(first.Operations) == 0 || first.Metrics.RoutedNets != 1 {
		t.Fatalf("unexpected route plan: %#v", first)
	}
	if !reflect.DeepEqual(first, second) {
		a, _ := json.Marshal(first)
		b, _ := json.Marshal(second)
		t.Fatalf("router is not deterministic:\n%s\n%s", a, b)
	}
	_, routed, _, err := applyOperations(&def, first.Operations)
	if err != nil {
		t.Fatal(err)
	}
	pads := padsForRoutingNet(routed, routed.Nets[0], routingPads(routed))
	if !netCopperConnected(routed, "signal", pads) {
		t.Fatal("applied route does not connect the net")
	}
}

func TestSuggestRoutesRespectsCopperKeepout(t *testing.T) {
	def := emptyDefinition("keepout route")
	def.Components = []Component{
		{ID: "a", Designator: "A", Name: "a", Position: Position{XNM: 5_000_000, YNM: 15_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", WidthNM: 500_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
		{ID: "b", Designator: "B", Name: "b", Position: Position{XNM: 45_000_000, YNM: 15_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", WidthNM: 500_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{{ID: "n", Name: "N", Nodes: []Node{{ComponentID: "a", PinID: "1"}, {ComponentID: "b", PinID: "1"}}}}
	def.Keepouts = []Keepout{{ID: "barrier", Kind: "copper", Layer: "F.Cu", Polygon: []Point{{XNM: 20_000_000, YNM: 8_000_000}, {XNM: 30_000_000, YNM: 8_000_000}, {XNM: 30_000_000, YNM: 22_000_000}, {XNM: 20_000_000, YNM: 22_000_000}}}}
	plan, err := suggestRoutes(&def, RouteOptions{GridNM: 500_000, Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, op := range plan.Operations {
		if op.Trace == nil {
			continue
		}
		if traceIntersectsPolygon(*op.Trace, def.Keepouts[0].Polygon) {
			t.Fatalf("route crosses keepout: %#v", op.Trace)
		}
	}
}

func TestFailedReplacementKeepsExistingRouteOutOfPlan(t *testing.T) {
	def := emptyDefinition("failed replacement")
	def.Board.WidthNM, def.Board.HeightNM = 10_000_000, 10_000_000
	def.Components = []Component{
		{ID: "a", Designator: "A", Name: "a", Position: Position{XNM: 2_000_000, YNM: 5_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", WidthNM: 500_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
		{ID: "b", Designator: "B", Name: "b", Position: Position{XNM: 8_000_000, YNM: 5_000_000, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", WidthNM: 500_000, HeightNM: 500_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{{ID: "n", Name: "N", Nodes: []Node{{ComponentID: "a", PinID: "1"}, {ComponentID: "b", PinID: "1"}}}}
	def.Traces = []Trace{{ID: "manual", NetID: "n", Layer: "F.Cu", WidthNM: 250_000, Points: []Point{{XNM: 2_000_000, YNM: 5_000_000}, {XNM: 8_000_000, YNM: 5_000_000}}}}
	def.Keepouts = []Keepout{{ID: "wall", Kind: "copper", Layer: "F.Cu", Polygon: []Point{{XNM: 4_000_000, YNM: 0}, {XNM: 6_000_000, YNM: 0}, {XNM: 6_000_000, YNM: 10_000_000}, {XNM: 4_000_000, YNM: 10_000_000}}}}
	plan, err := suggestRoutes(&def, RouteOptions{GridNM: 500_000, Layers: []string{"F.Cu"}, ReplaceExisting: true})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "failed" {
		t.Fatalf("expected failed replacement, got %s", plan.Status)
	}
	for _, op := range plan.Operations {
		if op.Type == "trace.remove" && op.TraceID == "manual" {
			t.Fatal("failed replacement must not schedule removal of the existing route")
		}
	}
}

func TestDifferentialPairConstraintBlocksIndependentRoute(t *testing.T) {
	def := emptyDefinition("differential pair route")
	def.Components = []Component{
		{ID: "j", Designator: "J1", Name: "connector", Position: Position{XNM: 5_000_000, YNM: 10_000_000, Side: "front"}, Pins: []Pin{{ID: "p", Number: "1"}, {ID: "n", Number: "2"}}, Pads: []Pad{{ID: "p", PinID: "p", Shape: "rect", YNM: -500_000, WidthNM: 300_000, HeightNM: 300_000, Layers: []string{"F.Cu"}}, {ID: "n", PinID: "n", Shape: "rect", YNM: 500_000, WidthNM: 300_000, HeightNM: 300_000, Layers: []string{"F.Cu"}}}},
		{ID: "u", Designator: "U1", Name: "controller", Position: Position{XNM: 40_000_000, YNM: 10_000_000, Side: "front"}, Pins: []Pin{{ID: "p", Number: "1"}, {ID: "n", Number: "2"}}, Pads: []Pad{{ID: "p", PinID: "p", Shape: "rect", YNM: -500_000, WidthNM: 300_000, HeightNM: 300_000, Layers: []string{"F.Cu"}}, {ID: "n", PinID: "n", Shape: "rect", YNM: 500_000, WidthNM: 300_000, HeightNM: 300_000, Layers: []string{"F.Cu"}}}},
	}
	def.Nets = []Net{{ID: "dp", Name: "D+", Nodes: []Node{{ComponentID: "j", PinID: "p"}, {ComponentID: "u", PinID: "p"}}}, {ID: "dm", Name: "D-", Nodes: []Node{{ComponentID: "j", PinID: "n"}, {ComponentID: "u", PinID: "n"}}}}
	def.DifferentialPairs = []DifferentialPair{{ID: "usb", PositiveNetID: "dp", NegativeNetID: "dm", GapNM: 2_000_000, GapToleranceNM: 10_000, MaxSkewNM: 100_000}}
	plan, err := suggestRoutes(&def, RouteOptions{GridNM: 500_000, Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status == "passed" {
		t.Fatal("an independently generated pair that misses the configured gap must not be reported as passed")
	}
	found := false
	for _, failure := range plan.Failures {
		if failure.NetID == "usb" {
			found = true
		}
	}
	if !found {
		t.Fatalf("missing differential-pair constraint failure: %#v", plan.Failures)
	}
}

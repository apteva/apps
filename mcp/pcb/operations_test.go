package main

import "testing"

func TestTypedOperationsAreAtomicAndNative(t *testing.T) {
	base := testDefinition()
	position := Position{XNM: 20_000_000, YNM: 8_000_000, RotationUdeg: 45_000_000, Side: "back"}
	trace := Trace{ID: "return", NetID: "signal", Layer: "B.Cu", WidthNM: 300_000, Points: []Point{{10_000_000, 8_000_000}, {20_000_000, 8_000_000}}}
	_, next, _, err := applyOperations(&base, []Operation{{Type: "placement.set", ComponentID: "r1", Position: &position}, {Type: "trace.add", Trace: &trace}})
	if err != nil {
		t.Fatal(err)
	}
	if next.Components[0].Position != position || findTrace(next.Traces, "return") < 0 {
		t.Fatalf("operations not applied: %#v", next)
	}
	if base.Components[0].Position == position || findTrace(base.Traces, "return") >= 0 {
		t.Fatal("base revision was mutated")
	}
}
func TestTypedOperationsRejectWholeBatch(t *testing.T) {
	base := testDefinition()
	component := base.Components[0]
	_, _, _, err := applyOperations(&base, []Operation{{Type: "component.add", Component: &component}, {Type: "via.add"}})
	if err == nil {
		t.Fatal("expected duplicate component error")
	}
	if len(base.Components) != 2 {
		t.Fatal("failed batch mutated source definition")
	}
}

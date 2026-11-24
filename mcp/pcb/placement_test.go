package main

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// scrambledDHT22 returns the DHT22 sensor node with all routing copper removed
// and every non-connector, non-fixed component moved to a deterministic
// scrambled position. Attached geometry (owned keepouts, mechanical envelopes,
// panel openings, clearance volumes) moves rigidly with its component, as it
// would for any placement edit.
//
// The B.Cu ground pour is removed together with traces and vias: its outline
// (with an antenna notch) was drawn for the 60 x 36 mm hand placement, so it
// is routing copper tied to the old layout rather than a placement input.
func scrambledDHT22(t *testing.T) *Definition {
	t.Helper()
	def := dht22SensorNodeExample()
	def.Traces, def.Vias, def.Zones = nil, nil, nil
	from, to := map[string]Position{}, map[string]Position{}
	ops := []Operation{}
	k := int64(0)
	for _, component := range def.Components {
		if component.Fixed || strings.HasPrefix(strings.ToUpper(component.Designator), "J") {
			continue
		}
		pose := component.Position
		pose.XNM = 8_000_000 + (k*13_000_000)%56_000_000
		pose.YNM = 6_000_000 + (k*7_000_000)%32_000_000
		k++
		from[component.ID], to[component.ID] = component.Position, pose
		position := pose
		ops = append(ops, Operation{Type: "placement.set", ComponentID: component.ID, Position: &position})
	}
	ops = append(ops, placementAttachedOperations(&def, from, to)...)
	_, scrambled, _, err := applyOperations(&def, ops)
	if err != nil {
		t.Fatal(err)
	}
	return scrambled
}

func errorChecks(checks []Check) []string {
	out := []string{}
	for _, check := range checks {
		if check.Severity == "error" {
			out = append(out, check.Code+" "+strings.Join(check.ObjectIDs, ","))
		}
	}
	return out
}

func TestPlacementAcceptanceDHT22AutoPlaceThenRoute(t *testing.T) {
	def := scrambledDHT22(t)
	plan, err := suggestPlacement(def, PlacementOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Before.Errors == 0 {
		t.Fatal("scrambled input should start with placement errors")
	}
	if plan.Status != "passed" || len(plan.Failures) > 0 || plan.After.Errors != 0 {
		t.Fatalf("placement status=%s failures=%v errors=%v", plan.Status, plan.Failures, errorChecks(plan.After.Checks))
	}
	if plan.After.RatsnestLengthNM >= plan.Before.RatsnestLengthNM {
		t.Errorf("ratsnest did not improve: %d -> %d", plan.Before.RatsnestLengthNM, plan.After.RatsnestLengthNM)
	}
	_, placed, _, err := applyOperations(def, plan.Operations)
	if err != nil {
		t.Fatal(err)
	}
	if analysis := analyzePlacement(placed, PlacementOptions{}); analysis.Errors != 0 {
		t.Fatalf("placement analysis after apply: %v", errorChecks(analysis.Checks))
	}
	if mechanical := validateMechanical(placed); mechanical.Errors != 0 {
		t.Fatalf("mechanical validation after placement: %v", errorChecks(mechanical.Checks))
	}

	routes, err := suggestRoutes(placed, RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(routes.Failures) > 0 {
		t.Fatalf("router failures after auto-placement: %v", routes.Failures)
	}
	for _, op := range routes.Operations {
		if err := applyOperation(placed, op); err != nil {
			t.Fatal(err)
		}
	}
	all := routingPads(placed)
	for _, net := range placed.Nets {
		pads := padsForRoutingNet(placed, net, all)
		if len(pads) >= 2 && !netCopperConnected(placed, net.ID, pads) {
			t.Errorf("net %s is not fully routed", net.ID)
		}
	}
	report := validateDefinition(placed)
	if report.Errors != 0 {
		t.Fatalf("DRC after auto-placement and routing: %d errors: %v", report.Errors, errorChecks(report.Checks))
	}
	t.Run("info_with_original_ground_pour_kept", func(t *testing.T) {
		// Informational only: the hand-drawn B.Cu pour is not re-shaped by
		// the placer, and the grid router cannot fully route this board
		// around it (it cannot fully route the original hand placement
		// around it either). Logged, not asserted.
		withPour := cloneDefinition(placed)
		withPour.Traces, withPour.Vias = nil, nil
		withPour.Zones = dht22SensorNodeExample().Zones
		pourRoutes, err := suggestRoutes(withPour, RouteOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, op := range pourRoutes.Operations {
			_ = applyOperation(withPour, op)
		}
		t.Logf("with original ground pour: routed %d/%d nets, %d route failures, %d DRC errors", pourRoutes.Metrics.RoutedNets, pourRoutes.Metrics.RequestedNets, len(pourRoutes.Failures), validateDefinition(withPour).Errors)
	})
	t.Logf("DHT22: moved %d parts, ratsnest %.1f -> %.1f mm, routed %d nets with %d traces / %d vias, DRC errors 0",
		plan.Metrics.Moved, float64(plan.Before.RatsnestLengthNM)/1e6, float64(plan.After.RatsnestLengthNM)/1e6, routes.Metrics.RoutedNets, routes.Metrics.TraceCount, routes.Metrics.ViaCount)
}

func TestPlacementIsDeterministic(t *testing.T) {
	def := scrambledDHT22(t)
	first, err := suggestPlacement(def, PlacementOptions{AllowRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	second, err := suggestPlacement(cloneDefinition(def), PlacementOptions{AllowRotation: true})
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(first.Operations)
	b, _ := json.Marshal(second.Operations)
	if !bytes.Equal(a, b) {
		t.Fatalf("placement operations differ between runs:\n%s\n%s", a, b)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("placement plans differ between runs")
	}
}

// edgeKeepoutBoard is a 40 x 30 mm board with an east-facing connector parked
// mid-board, two resistors and an LED, a component keepout across the middle,
// a fixed test point, a mounting hole, and an antenna keepout owned by u1.
func edgeKeepoutBoard() Definition {
	def := emptyDefinition("placement keepout")
	def.Board.WidthNM, def.Board.HeightNM, def.Board.ThicknessNM = 40_000_000, 30_000_000, 1_600_000
	twoPad := func(id, designator string, x, y int64) Component {
		return Component{ID: id, Designator: designator, Name: id, Footprint: "R_0603", Position: Position{XNM: x, YNM: y, Side: "front"}, Body: &Body{WidthNM: 1_600_000, HeightNM: 2_400_000},
			Pins: []Pin{{ID: "1", Number: "1", Pad: "1"}, {ID: "2", Number: "2", Pad: "2"}},
			Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", YNM: -800_000, WidthNM: 800_000, HeightNM: 600_000, Layers: []string{"F.Cu"}}, {ID: "2", PinID: "2", Shape: "rect", YNM: 800_000, WidthNM: 800_000, HeightNM: 600_000, Layers: []string{"F.Cu"}}}}
	}
	connector := Component{ID: "j1", Designator: "J1", Name: "connector", Footprint: "CONN_2", Position: Position{XNM: 20_000_000, YNM: 15_000_000, Side: "front"}, Body: &Body{WidthNM: 4_000_000, HeightNM: 6_000_000},
		Pins: []Pin{{ID: "1", Number: "1", Pad: "1"}, {ID: "2", Number: "2", Pad: "2"}},
		Pads: []Pad{{ID: "1", PinID: "1", Shape: "rect", XNM: -1_000_000, YNM: -1_250_000, WidthNM: 1_000_000, HeightNM: 1_000_000, Layers: []string{"F.Cu"}}, {ID: "2", PinID: "2", Shape: "rect", XNM: -1_000_000, YNM: 1_250_000, WidthNM: 1_000_000, HeightNM: 1_000_000, Layers: []string{"F.Cu"}}}}
	module := twoPad("u1", "U1", 30_000_000, 20_000_000)
	tp := Component{ID: "tp1", Designator: "TP1", Name: "test point", Footprint: "TP", Fixed: true, Position: Position{XNM: 6_000_000, YNM: 24_000_000, Side: "front"}, Body: &Body{WidthNM: 1_600_000, HeightNM: 1_600_000},
		Pins: []Pin{{ID: "1", Number: "1", Pad: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "circle", WidthNM: 1_200_000, HeightNM: 1_200_000, Layers: []string{"F.Cu"}}}}
	def.Components = []Component{connector, twoPad("r1", "R1", 20_000_000, 15_500_000), twoPad("r2", "R2", 21_000_000, 14_000_000), module, tp}
	def.Nets = []Net{
		{ID: "a", Name: "A", Nodes: []Node{{ComponentID: "j1", PinID: "1"}, {ComponentID: "r1", PinID: "1"}, {ComponentID: "tp1", PinID: "1"}}},
		{ID: "b", Name: "B", Nodes: []Node{{ComponentID: "j1", PinID: "2"}, {ComponentID: "r2", PinID: "1"}}},
		{ID: "c", Name: "C", Nodes: []Node{{ComponentID: "r1", PinID: "2"}, {ComponentID: "r2", PinID: "2"}, {ComponentID: "u1", PinID: "1"}}},
	}
	def.Keepouts = []Keepout{
		{ID: "middle", Kind: "component", Polygon: []Point{{XNM: 14_000_000, YNM: 8_000_000}, {XNM: 26_000_000, YNM: 8_000_000}, {XNM: 26_000_000, YNM: 22_000_000}, {XNM: 14_000_000, YNM: 22_000_000}}},
		{ID: "u1-antenna", Kind: "antenna", OwnerID: "u1", Polygon: []Point{{XNM: 33_000_000, YNM: 18_000_000}, {XNM: 36_000_000, YNM: 18_000_000}, {XNM: 36_000_000, YNM: 22_000_000}, {XNM: 33_000_000, YNM: 22_000_000}}},
	}
	def.Mechanical = &MechanicalSpec{
		Schema: mechanicalSchema, Datum: defaultMechanicalDatum(),
		MountingHoles: []MechanicalHole{{ID: "mh1", XNM: 36_000_000, YNM: 4_000_000, DiameterNM: 3_200_000, HeadClearanceNM: 6_000_000}},
		ComponentEnvelopes: []ComponentEnvelope{
			{ID: "env-j1", ComponentID: "j1", Kind: "connector", XNM: 20_000_000, YNM: 15_000_000, ZMinNM: 1_600_000, ZMaxNM: 6_000_000, WidthNM: 4_000_000, DepthNM: 6_000_000, Shape: "connector"},
		},
		PanelOpenings: []PanelOpening{{ID: "opening-j1", ComponentID: "j1", Kind: "header", Face: "east", XNM: 40_000_000, YNM: 15_000_000, ZNM: 3_000_000, WidthNM: 7_000_000, HeightNM: 4_000_000}},
	}
	return def
}

func TestPlacementHandlesKeepoutsAndEdgeConnectors(t *testing.T) {
	def := edgeKeepoutBoard()
	before := analyzePlacement(&def, PlacementOptions{})
	codes := strings.Join(errorChecks(before.Checks), "|")
	for _, want := range []string{"PLACEMENT_PANEL_EDGE j1", "PLACEMENT_KEEPOUT middle,j1", "PLACEMENT_OVERLAP"} {
		if !strings.Contains(codes, want) {
			t.Errorf("analysis missing %q in %s", want, codes)
		}
	}
	plan, err := suggestPlacement(&def, PlacementOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "passed" || plan.After.Errors != 0 {
		t.Fatalf("placement status=%s failures=%v errors=%v", plan.Status, plan.Failures, errorChecks(plan.After.Checks))
	}
	if !reflect.DeepEqual(plan.EdgePinned, []string{"j1"}) || !reflect.DeepEqual(plan.FixedComponents, []string{"tp1"}) {
		t.Fatalf("pinned=%v fixed=%v", plan.EdgePinned, plan.FixedComponents)
	}
	_, placed, _, err := applyOperations(&def, plan.Operations)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Component{}
	for _, component := range placed.Components {
		byID[component.ID] = component
	}
	if byID["tp1"].Position != def.Components[4].Position {
		t.Error("fixed component moved")
	}
	j1 := byID["j1"]
	if j1.Position.XNM+2_000_000 < 40_000_000-3_000_000 {
		t.Errorf("east connector not at the east edge: %+v", j1.Position)
	}
	opening := placed.Mechanical.PanelOpenings[0]
	if opening.XNM != 40_000_000 || opening.YNM != j1.Position.YNM {
		t.Errorf("panel opening did not follow its connector: opening=%+v j1=%+v", opening, j1.Position)
	}
	envelope := placed.Mechanical.ComponentEnvelopes[0]
	if envelope.XNM != j1.Position.XNM || envelope.YNM != j1.Position.YNM {
		t.Errorf("envelope did not follow its connector: %+v", envelope)
	}
	for _, component := range placed.Components {
		body := *component.Body
		if polygonsIntersect(bodyPolygon(component, body), placed.Keepouts[0].Polygon) {
			t.Errorf("%s body inside the component keepout", component.ID)
		}
	}
	// The owned antenna keepout keeps its offset from u1.
	var antenna Keepout
	for _, keepout := range placed.Keepouts {
		if keepout.ID == "u1-antenna" {
			antenna = keepout
		}
	}
	u1 := byID["u1"]
	if antenna.Polygon[0].XNM-u1.Position.XNM != 3_000_000 || antenna.Polygon[0].YNM-u1.Position.YNM != -2_000_000 {
		t.Errorf("owned keepout did not move with u1: keepout=%v u1=%+v", antenna.Polygon[0], u1.Position)
	}
	if mechanical := validateMechanical(placed); mechanical.Errors != 0 {
		t.Errorf("mechanical validation: %v", errorChecks(mechanical.Checks))
	}
}

func TestPlacementOptionsAndSubsets(t *testing.T) {
	def := edgeKeepoutBoard()
	if _, err := suggestPlacement(&def, PlacementOptions{ComponentIDs: []string{"nope"}}); err == nil {
		t.Fatal("unknown component id must be rejected")
	}
	if _, err := suggestPlacement(&def, PlacementOptions{GridNM: 10}); err == nil {
		t.Fatal("grid below 0.1 mm must be rejected")
	}
	plan, err := suggestPlacement(&def, PlacementOptions{ComponentIDs: []string{"r1", "r2"}, FixedComponentIDs: []string{"r2"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range plan.MovedComponents {
		if id != "r1" {
			t.Fatalf("only r1 may move, moved %v", plan.MovedComponents)
		}
	}
	for _, op := range plan.Operations {
		if op.Type != "placement.set" {
			t.Fatalf("unexpected operation %s for a part without attached geometry", op.Type)
		}
	}
}

func TestPlacementClearRoutingRemovesStaleCopper(t *testing.T) {
	def := dht22SensorNodeExample()
	plan, err := suggestPlacement(&def, PlacementOptions{ComponentIDs: []string{"r5"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.StaleRoutingNets) == 0 {
		t.Fatal("moving r5 should strand its existing routing")
	}
	for _, op := range plan.Operations {
		if op.Type == "trace.remove" || op.Type == "via.remove" {
			t.Fatal("routing removed without clear_routing")
		}
	}
	cleared, err := suggestPlacement(&def, PlacementOptions{ComponentIDs: []string{"r5"}, ClearRouting: true})
	if err != nil {
		t.Fatal(err)
	}
	_, next, _, err := applyOperations(&def, cleared.Operations)
	if err != nil {
		t.Fatal(err)
	}
	for _, trace := range next.Traces {
		for _, netID := range cleared.StaleRoutingNets {
			if trace.NetID == netID {
				t.Fatalf("stale trace %s kept on %s", trace.ID, netID)
			}
		}
	}
}

// Regression for the router fixes made alongside the placer: terminals are
// entered on a copper layer the pad occupies, vias are checked with their own
// diameter and never dropped on a terminal pad, and multi-pad nets route as a
// spanning tree. Before them, re-routing the hand-placed DHT22 node without its
// pour left PAD_UNROUTED and VIA_TRACE_CLEARANCE errors.
func TestRouterReroutesHandPlacedDHT22Cleanly(t *testing.T) {
	def := dht22SensorNodeExample()
	def.Traces, def.Vias, def.Zones = nil, nil, nil
	plan, err := suggestRoutes(&def, RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Failures) > 0 {
		t.Fatalf("route failures: %v", plan.Failures)
	}
	for _, op := range plan.Operations {
		if err := applyOperation(&def, op); err != nil {
			t.Fatal(err)
		}
	}
	if report := validateDefinition(&def); report.Errors != 0 {
		t.Fatalf("DRC errors: %v", errorChecks(report.Checks))
	}
}

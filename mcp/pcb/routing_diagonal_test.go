package main

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"
	"time"
)

func routeTestPad(id string, x, y int64) Component {
	return Component{ID: id, Designator: id, Name: id, Position: Position{XNM: x, YNM: y, Side: "front"}, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "circle", WidthNM: 400_000, HeightNM: 400_000, Layers: []string{"F.Cu"}}}}
}

func routePlanTraces(plan *RoutePlan) []Trace {
	out := []Trace{}
	for _, op := range plan.Operations {
		if op.Trace != nil {
			out = append(out, *op.Trace)
		}
	}
	return out
}

// maxBendDegrees is the sharpest bend between consecutive segments of a trace.
func maxBendDegrees(trace Trace) float64 {
	worst := 0.0
	for i := 2; i < len(trace.Points); i++ {
		a, b, c := trace.Points[i-2], trace.Points[i-1], trace.Points[i]
		ux, uy := float64(b.XNM-a.XNM), float64(b.YNM-a.YNM)
		vx, vy := float64(c.XNM-b.XNM), float64(c.YNM-b.YNM)
		lu, lv := math.Hypot(ux, uy), math.Hypot(vx, vy)
		if lu == 0 || lv == 0 {
			continue
		}
		cos := math.Max(-1, math.Min(1, (ux*vx+uy*vy)/(lu*lv)))
		worst = math.Max(worst, math.Acos(cos)*180/math.Pi)
	}
	return worst
}

func applyRoutePlan(t *testing.T, def *Definition, plan *RoutePlan) {
	t.Helper()
	for _, op := range plan.Operations {
		if err := applyOperation(def, op); err != nil {
			t.Fatal(err)
		}
	}
}

func assertNetsConnected(t *testing.T, def *Definition) {
	t.Helper()
	all := routingPads(def)
	for _, net := range def.Nets {
		if pads := padsForRoutingNet(def, net, all); len(pads) >= 2 && !netCopperConnected(def, net.ID, pads) {
			t.Fatalf("net %s is not fully routed", net.ID)
		}
	}
}

// A 45-degree channel between two existing traces of another net: every grid
// point on the channel's centre line is clear, but any orthogonal step lands
// too close to a wall. Only the diagonal router gets through.
func TestDiagonalRoutingThroughFortyFiveDegreeChannel(t *testing.T) {
	def := emptyDefinition("diagonal channel")
	def.Board.WidthNM, def.Board.HeightNM = 12_000_000, 12_000_000
	def.Components = []Component{routeTestPad("a", 2_000_000, 2_000_000), routeTestPad("b", 10_000_000, 10_000_000)}
	def.Nets = []Net{{ID: "sig", Name: "SIG", Nodes: []Node{{ComponentID: "a", PinID: "1"}, {ComponentID: "b", PinID: "1"}}}, {ID: "wall", Name: "WALL"}}
	const c = 750_000 // walls along y = x ± c, 0.53 mm from the centre line
	def.Traces = []Trace{
		{ID: "wall-upper", NetID: "wall", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{XNM: 500_000, YNM: 500_000 + c}, {XNM: 11_500_000 - c, YNM: 11_500_000}}},
		{ID: "wall-lower", NetID: "wall", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{XNM: 500_000 + c, YNM: 500_000}, {XNM: 11_500_000, YNM: 11_500_000 - c}}},
	}
	orthogonal := false
	ortho, err := suggestRoutes(&def, RouteOptions{NetIDs: []string{"sig"}, Layers: []string{"F.Cu"}, AllowDiagonal: &orthogonal})
	if err != nil {
		t.Fatal(err)
	}
	if ortho.Status != "failed" || ortho.Metrics.RoutedNets != 0 {
		t.Fatalf("orthogonal router should not fit the 45-degree channel: %+v %v", ortho.Metrics, ortho.Failures)
	}
	plan, err := suggestRoutes(&def, RouteOptions{NetIDs: []string{"sig"}, Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "passed" || plan.Metrics.RoutedNets != 1 {
		t.Fatalf("diagonal router failed: %+v %v", plan.Metrics, plan.Failures)
	}
	traces := routePlanTraces(plan)
	if len(traces) != 1 || len(traces[0].Points) != 2 || plan.Metrics.DiagonalSegments != 1 {
		t.Fatalf("expected one merged diagonal segment, got %+v (metrics %+v)", traces, plan.Metrics)
	}
	applyRoutePlan(t, &def, plan)
	if codes := errorChecks(validateDefinition(&def).Checks); len(codes) != 0 {
		t.Fatalf("DRC errors after diagonal route: %v", codes)
	}
}

func TestDiagonalRoutingIsShorterWithFortyFiveDegreeBends(t *testing.T) {
	def := emptyDefinition("diagonal open board")
	def.Components = []Component{routeTestPad("a", 5_000_000, 5_000_000), routeTestPad("b", 35_000_000, 20_000_000), routeTestPad("c", 20_000_000, 25_000_000)}
	def.Nets = []Net{{ID: "sig", Name: "SIG", Nodes: []Node{{ComponentID: "a", PinID: "1"}, {ComponentID: "b", PinID: "1"}, {ComponentID: "c", PinID: "1"}}}}
	orthogonal := false
	ortho, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}, AllowDiagonal: &orthogonal})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	if ortho.Status != "passed" || plan.Status != "passed" {
		t.Fatalf("both routers should route the open board: %v / %v", ortho.Failures, plan.Failures)
	}
	if ortho.Metrics.DiagonalSegments != 0 || plan.Metrics.DiagonalSegments == 0 {
		t.Fatalf("diagonal segment counts: orthogonal %d, diagonal %d", ortho.Metrics.DiagonalSegments, plan.Metrics.DiagonalSegments)
	}
	if float64(plan.Metrics.TotalLengthNM) > 0.85*float64(ortho.Metrics.TotalLengthNM) {
		t.Fatalf("45-degree routing should be much shorter: %d vs orthogonal %d", plan.Metrics.TotalLengthNM, ortho.Metrics.TotalLengthNM)
	}
	for _, trace := range routePlanTraces(plan) {
		if bend := maxBendDegrees(trace); bend > 45.5 {
			t.Fatalf("trace %s bends %.1f degrees at once: %v", trace.ID, bend, trace.Points)
		}
		for i := 2; i < len(trace.Points); i++ {
			a, b, c := trace.Points[i-2], trace.Points[i-1], trace.Points[i]
			if (b.XNM-a.XNM)*(c.YNM-b.YNM) == (b.YNM-a.YNM)*(c.XNM-b.XNM) {
				t.Fatalf("trace %s keeps a collinear point: %v", trace.ID, trace.Points)
			}
		}
	}
	applyRoutePlan(t, &def, plan)
	assertNetsConnected(t, &def)
	t.Logf("open board: orthogonal %.2f mm, 45-degree %.2f mm", float64(ortho.Metrics.TotalLengthNM)/1e6, float64(plan.Metrics.TotalLengthNM)/1e6)
}

// Net "a" spans the full width of a single-layer board and is routed first;
// its straight route cuts net "b" off. Only ripping "a" up, routing "b", and
// re-routing "a" around one of b's pads routes both.
func congestionDefinition() Definition {
	def := emptyDefinition("rip-up congestion")
	def.Board.WidthNM, def.Board.HeightNM = 30_000_000, 20_000_000
	def.Components = []Component{routeTestPad("a1", 800_000, 10_000_000), routeTestPad("a2", 29_200_000, 10_000_000), routeTestPad("b1", 15_000_000, 5_000_000), routeTestPad("b2", 15_000_000, 15_000_000)}
	def.Nets = []Net{
		{ID: "a", Name: "A", Nodes: []Node{{ComponentID: "a1", PinID: "1"}, {ComponentID: "a2", PinID: "1"}}},
		{ID: "b", Name: "B", Nodes: []Node{{ComponentID: "b1", PinID: "1"}, {ComponentID: "b2", PinID: "1"}}},
	}
	return def
}

func TestRipUpAndRetryResolvesGreedyOrderConflict(t *testing.T) {
	def := congestionDefinition()
	greedy, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}, MaxRipupIterations: -1})
	if err != nil {
		t.Fatal(err)
	}
	if greedy.Status != "partial" || len(greedy.Failures) != 1 || greedy.Failures[0].NetID != "b" || greedy.Metrics.Ripups != 0 {
		t.Fatalf("greedy order should strand net b: status=%s failures=%v", greedy.Status, greedy.Failures)
	}
	plan, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Status != "passed" || plan.Metrics.RoutedNets != 2 || plan.Metrics.Ripups == 0 || plan.Metrics.Iterations == 0 {
		t.Fatalf("rip-up should route both nets: status=%s metrics=%+v failures=%v", plan.Status, plan.Metrics, plan.Failures)
	}
	again, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(plan)
	second, _ := json.Marshal(again)
	if !bytes.Equal(first, second) {
		t.Fatal("rip-up routing is not deterministic")
	}
	applyRoutePlan(t, &def, plan)
	assertNetsConnected(t, &def)
	if codes := errorChecks(validateDefinition(&def).Checks); len(codes) != 0 {
		t.Fatalf("DRC errors after rip-up routing: %v", codes)
	}
	t.Logf("rip-up: %d ripups over %d iterations, %.2f mm", plan.Metrics.Ripups, plan.Metrics.Iterations, float64(plan.Metrics.TotalLengthNM)/1e6)
}

func TestRipUpNeverRemovesPreexistingCopper(t *testing.T) {
	def := congestionDefinition()
	def.Traces = []Trace{{ID: "manual-a", NetID: "a", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{XNM: 800_000, YNM: 10_000_000}, {XNM: 29_200_000, YNM: 10_000_000}}}}
	plan, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}})
	if err != nil {
		t.Fatal(err)
	}
	// Net a is already connected by hand (skipped); b cannot cross it.
	if plan.Status != "failed" || plan.Metrics.SkippedNets != 1 || plan.Metrics.Ripups != 0 || len(plan.Operations) != 0 {
		t.Fatalf("hand-routed copper must stay fixed: status=%s metrics=%+v ops=%d", plan.Status, plan.Metrics, len(plan.Operations))
	}
	// replace_existing hands the selected nets' copper to the router, which
	// may then lay both nets out again.
	replaced, err := suggestRoutes(&def, RouteOptions{Layers: []string{"F.Cu"}, ReplaceExisting: true})
	if err != nil {
		t.Fatal(err)
	}
	if replaced.Status != "passed" || replaced.Operations[0].Type != "trace.remove" || replaced.Operations[0].TraceID != "manual-a" {
		t.Fatalf("replace_existing should re-route both nets: status=%s failures=%v", replaced.Status, replaced.Failures)
	}
}

// The hand-placed DHT22 node with all copper cleared routes completely and
// DRC-clean with 45-degree routing, no longer than the orthogonal router, and
// deterministically.
func TestDHT22DiagonalRouteBeatsOrthogonal(t *testing.T) {
	base := dht22SensorNodeExample()
	base.Traces, base.Vias, base.Zones = nil, nil, nil
	route := func(options RouteOptions) (*RoutePlan, *Definition, time.Duration) {
		def := cloneDefinition(&base)
		start := time.Now()
		plan, err := suggestRoutes(def, options)
		elapsed := time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		if len(plan.Failures) > 0 || plan.Metrics.RoutedNets != plan.Metrics.RequestedNets {
			t.Fatalf("route failures: %v", plan.Failures)
		}
		applyRoutePlan(t, def, plan)
		assertNetsConnected(t, def)
		return plan, def, elapsed
	}
	orthogonal := false
	ortho, _, orthoTime := route(RouteOptions{AllowDiagonal: &orthogonal})
	diag, routed, diagTime := route(RouteOptions{})
	if codes := errorChecks(validateDefinition(routed).Checks); len(codes) != 0 {
		t.Fatalf("DRC errors after 45-degree routing: %v", codes)
	}
	if diag.Metrics.TotalLengthNM > ortho.Metrics.TotalLengthNM {
		t.Fatalf("45-degree routing is longer than orthogonal: %d > %d", diag.Metrics.TotalLengthNM, ortho.Metrics.TotalLengthNM)
	}
	if diag.Metrics.DiagonalSegments == 0 {
		t.Fatal("45-degree routing produced no diagonal segments")
	}
	again, err := suggestRoutes(cloneDefinition(&base), RouteOptions{})
	if err != nil {
		t.Fatal(err)
	}
	first, _ := json.Marshal(diag.Operations)
	second, _ := json.Marshal(again.Operations)
	if !bytes.Equal(first, second) {
		t.Fatal("DHT22 routing is not deterministic")
	}
	t.Logf("DHT22 orthogonal: %.1f mm, %d traces, %d vias, %v", float64(ortho.Metrics.TotalLengthNM)/1e6, ortho.Metrics.TraceCount, ortho.Metrics.ViaCount, orthoTime)
	t.Logf("DHT22 45-degree:  %.1f mm, %d traces, %d vias, %d diagonal segments, %d ripups, %v", float64(diag.Metrics.TotalLengthNM)/1e6, diag.Metrics.TraceCount, diag.Metrics.ViaCount, diag.Metrics.DiagonalSegments, diag.Metrics.Ripups, diagTime)
}

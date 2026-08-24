package main

import (
	"container/heap"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

const routingSchema = "apteva-pcb-route/v1"

type RouteOptions struct {
	NetIDs          []string `json:"net_ids,omitempty"`
	Layers          []string `json:"layers,omitempty"`
	GridNM          int64    `json:"grid_nm,omitempty"`
	TraceWidthNM    int64    `json:"trace_width_nm,omitempty"`
	ClearanceNM     int64    `json:"clearance_nm,omitempty"`
	ViaDiameterNM   int64    `json:"via_diameter_nm,omitempty"`
	ViaDrillNM      int64    `json:"via_drill_nm,omitempty"`
	ReplaceExisting bool     `json:"replace_existing,omitempty"`
	MaxVisited      int      `json:"max_visited,omitempty"`
}

type RouteFailure struct {
	NetID   string `json:"net_id"`
	Message string `json:"message"`
}

type RouteMetrics struct {
	RequestedNets int   `json:"requested_nets"`
	RoutedNets    int   `json:"routed_nets"`
	SkippedNets   int   `json:"skipped_nets"`
	TraceCount    int   `json:"trace_count"`
	ViaCount      int   `json:"via_count"`
	TotalLengthNM int64 `json:"total_length_nm"`
	VisitedNodes  int   `json:"visited_nodes"`
}

type RoutePlan struct {
	Schema     string         `json:"schema"`
	Engine     string         `json:"engine"`
	Status     string         `json:"status"`
	GridNM     int64          `json:"grid_nm"`
	Layers     []string       `json:"layers"`
	RoutedNets []string       `json:"routed_nets"`
	Skipped    []string       `json:"skipped_nets,omitempty"`
	Failures   []RouteFailure `json:"failures,omitempty"`
	Operations []Operation    `json:"operations"`
	Metrics    RouteMetrics   `json:"metrics"`
}

type routeGridState struct {
	X, Y, Layer, Dir int
}

type routeQueueItem struct {
	State    routeGridState
	Priority int64
	Cost     int64
	Sequence int64
	Index    int
}

type routePriorityQueue []*routeQueueItem

func (q routePriorityQueue) Len() int { return len(q) }
func (q routePriorityQueue) Less(i, j int) bool {
	if q[i].Priority != q[j].Priority {
		return q[i].Priority < q[j].Priority
	}
	if q[i].Cost != q[j].Cost {
		return q[i].Cost < q[j].Cost
	}
	return q[i].Sequence < q[j].Sequence
}
func (q routePriorityQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i]; q[i].Index, q[j].Index = i, j }
func (q *routePriorityQueue) Push(v any) {
	item := v.(*routeQueueItem)
	item.Index = len(*q)
	*q = append(*q, item)
}
func (q *routePriorityQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

type routeObstacleSet struct {
	def     *Definition
	pads    []*placedPad
	gridNM  int64
	widthNM int64
	clearNM int64
}

func suggestRoutes(def *Definition, options RouteOptions) (*RoutePlan, error) {
	if def == nil {
		return nil, fmt.Errorf("definition required")
	}
	if options.GridNM <= 0 {
		options.GridNM = 500_000
	}
	if options.GridNM < 100_000 || options.GridNM > 5_000_000 {
		return nil, fmt.Errorf("grid_nm must be between 100000 and 5000000")
	}
	if options.MaxVisited <= 0 {
		options.MaxVisited = 250_000
	}
	layers := routingLayers(def, options.Layers)
	if len(layers) == 0 {
		return nil, fmt.Errorf("no routable copper layers selected")
	}
	targets, err := routingTargets(def, options.NetIDs)
	if err != nil {
		return nil, err
	}
	plan := &RoutePlan{Schema: routingSchema, Engine: engineVersion, Status: "passed", GridNM: options.GridNM, Layers: layers, Operations: []Operation{}, RoutedNets: []string{}, Skipped: []string{}, Failures: []RouteFailure{}}
	plan.Metrics.RequestedNets = len(targets)
	working := cloneDefinition(def)
	removeOperations := map[string][]Operation{}
	if options.ReplaceExisting {
		selected := map[string]bool{}
		for _, net := range targets {
			selected[net.ID] = true
		}
		keptTraces := working.Traces[:0]
		for _, trace := range working.Traces {
			if selected[trace.NetID] {
				removeOperations[trace.NetID] = append(removeOperations[trace.NetID], Operation{Type: "trace.remove", TraceID: trace.ID})
			} else {
				keptTraces = append(keptTraces, trace)
			}
		}
		working.Traces = keptTraces
		keptVias := working.Vias[:0]
		for _, via := range working.Vias {
			if selected[via.NetID] {
				removeOperations[via.NetID] = append(removeOperations[via.NetID], Operation{Type: "via.remove", ViaID: via.ID})
			} else {
				keptVias = append(keptVias, via)
			}
		}
		working.Vias = keptVias
	}

	allPads := routingPads(working)
	for _, net := range targets {
		beforeNet := cloneDefinition(working)
		traceCountBefore, viaCountBefore := plan.Metrics.TraceCount, plan.Metrics.ViaCount
		lengthBefore := plan.Metrics.TotalLengthNM
		pads := padsForRoutingNet(working, net, allPads)
		if len(pads) < 2 {
			plan.Skipped = append(plan.Skipped, net.ID)
			plan.Metrics.SkippedNets++
			continue
		}
		if !options.ReplaceExisting && netCopperConnected(working, net.ID, pads) {
			plan.Skipped = append(plan.Skipped, net.ID)
			plan.Metrics.SkippedNets++
			continue
		}
		width, clearance, viaDiameter, viaDrill := routingRules(working, net.ID, options)
		obstacles := routeObstacleSet{def: working, pads: allPads, gridNM: options.GridNM, widthNM: width, clearNM: clearance}
		netOps := []Operation{}
		failed := ""
		for branch := 1; branch < len(pads); branch++ {
			path, visited, pathErr := findGridRoute(obstacles, net.ID, pads[0].Center, pads[branch].Center, layers, options.MaxVisited)
			plan.Metrics.VisitedNodes += visited
			if pathErr != nil {
				failed = fmt.Sprintf("%s to %s: %v", pads[0].ComponentID+":"+pads[0].Pad.ID, pads[branch].ComponentID+":"+pads[branch].Pad.ID, pathErr)
				break
			}
			ops, traces, vias := routePathOperations(working, net.ID, path, pads[0].Center, pads[branch].Center, layers, options.GridNM, width, viaDiameter, viaDrill, branch)
			netOps = append(netOps, ops...)
			for _, trace := range traces {
				working.Traces = append(working.Traces, trace)
				plan.Metrics.TotalLengthNM += traceLengthNM(trace)
			}
			working.Vias = append(working.Vias, vias...)
			plan.Metrics.TraceCount += len(traces)
			plan.Metrics.ViaCount += len(vias)
		}
		if failed != "" {
			working = beforeNet
			plan.Metrics.TraceCount, plan.Metrics.ViaCount = traceCountBefore, viaCountBefore
			plan.Metrics.TotalLengthNM = lengthBefore
			plan.Failures = append(plan.Failures, RouteFailure{NetID: net.ID, Message: failed})
			continue
		}
		plan.Operations = append(plan.Operations, removeOperations[net.ID]...)
		plan.Operations = append(plan.Operations, netOps...)
		plan.RoutedNets = append(plan.RoutedNets, net.ID)
		plan.Metrics.RoutedNets++
	}
	plan.Failures = append(plan.Failures, routedDifferentialPairFailures(working, targets)...)
	if len(plan.Failures) > 0 {
		plan.Status = "partial"
		if plan.Metrics.RoutedNets == 0 {
			plan.Status = "failed"
		}
	}
	return plan, nil
}

func routedDifferentialPairFailures(def *Definition, targets []Net) []RouteFailure {
	targeted := map[string]bool{}
	for _, net := range targets {
		targeted[net.ID] = true
	}
	nets := map[string]Net{}
	for _, net := range def.Nets {
		nets[net.ID] = net
	}
	failures := []RouteFailure{}
	for _, pair := range def.DifferentialPairs {
		if !targeted[pair.PositiveNetID] && !targeted[pair.NegativeNetID] {
			continue
		}
		checks := []Check{}
		validateDifferentialPair(def, pair, nets, &checks)
		messages := []string{}
		for _, check := range checks {
			if check.Severity == "error" {
				messages = append(messages, check.Message)
			}
		}
		if len(messages) > 0 {
			failures = append(failures, RouteFailure{NetID: pair.ID, Message: "differential-pair constraint: " + strings.Join(messages, "; ")})
		}
	}
	return failures
}

func cloneDefinition(def *Definition) *Definition {
	body, _ := jsonMarshal(def)
	var out Definition
	_ = jsonUnmarshal(body, &out)
	return &out
}

// Small indirections keep the router's cloning path explicit and make fuzzing
// it independently from canonicalization straightforward.
func jsonMarshal(v any) ([]byte, error)      { return json.Marshal(v) }
func jsonUnmarshal(body []byte, v any) error { return json.Unmarshal(body, v) }

func routingLayers(def *Definition, requested []string) []string {
	allowed := map[string]bool{}
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" {
			allowed[layer.ID] = true
		}
	}
	if len(requested) == 0 {
		requested = []string{"F.Cu", "B.Cu"}
	}
	out := []string{}
	seen := map[string]bool{}
	for _, layer := range requested {
		if allowed[layer] && !seen[layer] {
			out = append(out, layer)
			seen[layer] = true
		}
	}
	return out
}

func routingTargets(def *Definition, requested []string) ([]Net, error) {
	want := map[string]bool{}
	for _, id := range requested {
		want[id] = true
	}
	out := []Net{}
	for _, net := range def.Nets {
		if len(want) == 0 || want[net.ID] {
			out = append(out, net)
			delete(want, net.ID)
		}
	}
	if len(want) > 0 {
		missing := make([]string, 0, len(want))
		for id := range want {
			missing = append(missing, id)
		}
		sort.Strings(missing)
		return nil, fmt.Errorf("unknown net_ids: %s", strings.Join(missing, ", "))
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, pj := netPriority(def, out[i].ID), netPriority(def, out[j].ID)
		if pi != pj {
			return pi > pj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func netPriority(def *Definition, netID string) int {
	for _, class := range def.NetClasses {
		if containsString(class.NetIDs, netID) {
			return class.Priority
		}
	}
	return 0
}

func routingRules(def *Definition, netID string, options RouteOptions) (width, clearance, viaDiameter, viaDrill int64) {
	width, clearance = def.Rules.MinTraceWidthNM, def.Rules.MinClearanceNM
	viaDiameter, viaDrill = 650_000, 300_000
	for _, class := range def.NetClasses {
		if !containsString(class.NetIDs, netID) {
			continue
		}
		if class.TraceWidthNM > 0 {
			width = class.TraceWidthNM
		}
		if class.ClearanceNM > 0 {
			clearance = class.ClearanceNM
		}
		if class.ViaDiameterNM > 0 {
			viaDiameter = class.ViaDiameterNM
		}
		if class.ViaDrillNM > 0 {
			viaDrill = class.ViaDrillNM
		}
		break
	}
	if options.TraceWidthNM > 0 {
		width = options.TraceWidthNM
	}
	if options.ClearanceNM > 0 {
		clearance = options.ClearanceNM
	}
	if options.ViaDiameterNM > 0 {
		viaDiameter = options.ViaDiameterNM
	}
	if options.ViaDrillNM > 0 {
		viaDrill = options.ViaDrillNM
	}
	return
}

func routingPads(def *Definition) []*placedPad {
	netByNode := map[string]string{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			netByNode[node.ComponentID+":"+node.PinID] = net.ID
		}
	}
	out := []*placedPad{}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			placed := placePad(component, pad)
			placed.NetID = netByNode[component.ID+":"+pad.PinID]
			out = append(out, placed)
		}
	}
	return out
}

func padsForRoutingNet(def *Definition, net Net, all []*placedPad) []*placedPad {
	nodes := map[string]bool{}
	for _, node := range net.Nodes {
		nodes[node.ComponentID+":"+node.PinID] = true
	}
	out := []*placedPad{}
	for _, pad := range all {
		if nodes[pad.ComponentID+":"+pad.PinID] {
			out = append(out, pad)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := out[i], out[j]
		if a.Center.XNM != b.Center.XNM {
			return a.Center.XNM < b.Center.XNM
		}
		if a.Center.YNM != b.Center.YNM {
			return a.Center.YNM < b.Center.YNM
		}
		return a.ComponentID+":"+a.Pad.ID < b.ComponentID+":"+b.Pad.ID
	})
	return out
}

func findGridRoute(obstacles routeObstacleSet, netID string, start, goal Point, layers []string, maxVisited int) ([]routeGridState, int, error) {
	grid := obstacles.gridNM
	toGrid := func(v int64) int { return int(math.Round(float64(v) / float64(grid))) }
	startState := routeGridState{X: toGrid(start.XNM), Y: toGrid(start.YNM), Layer: 0, Dir: 4}
	goalX, goalY := toGrid(goal.XNM), toGrid(goal.YNM)
	q := &routePriorityQueue{}
	heap.Init(q)
	sequence := int64(0)
	heuristic := func(s routeGridState) int64 { return int64(absInt(s.X-goalX)+absInt(s.Y-goalY))*10 + int64(s.Layer)*3 }
	heap.Push(q, &routeQueueItem{State: startState, Cost: 0, Priority: heuristic(startState), Sequence: sequence})
	cost := map[routeGridState]int64{startState: 0}
	came := map[routeGridState]routeGridState{}
	visited := 0
	directions := [][2]int{{1, 0}, {0, 1}, {-1, 0}, {0, -1}}
	for q.Len() > 0 && visited < maxVisited {
		item := heap.Pop(q).(*routeQueueItem)
		current := item.State
		if best, ok := cost[current]; ok && item.Cost != best {
			continue
		}
		visited++
		if current.X == goalX && current.Y == goalY {
			path := []routeGridState{current}
			for current != startState {
				current = came[current]
				path = append(path, current)
			}
			for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
				path[i], path[j] = path[j], path[i]
			}
			return path, visited, nil
		}
		for direction, delta := range directions {
			next := routeGridState{X: current.X + delta[0], Y: current.Y + delta[1], Layer: current.Layer, Dir: direction}
			point := Point{XNM: int64(next.X) * grid, YNM: int64(next.Y) * grid}
			if (next.X != goalX || next.Y != goalY) && obstacles.blocked(netID, layers[next.Layer], point) {
				continue
			}
			step := int64(10)
			if current.Dir < 4 && current.Dir != direction {
				step += 4
			}
			updateRouteNode(q, cost, came, current, next, item.Cost+step, heuristic(next), &sequence)
		}
		if len(layers) > 1 {
			for layer := range layers {
				if layer == current.Layer {
					continue
				}
				next := routeGridState{X: current.X, Y: current.Y, Layer: layer, Dir: 4}
				point := Point{XNM: int64(next.X) * grid, YNM: int64(next.Y) * grid}
				if obstacles.blocked(netID, layers[next.Layer], point) {
					continue
				}
				updateRouteNode(q, cost, came, current, next, item.Cost+80, heuristic(next), &sequence)
			}
		}
	}
	if visited >= maxVisited {
		return nil, visited, fmt.Errorf("search exceeded %d visited nodes", maxVisited)
	}
	return nil, visited, fmt.Errorf("no clearance-safe path found")
}

func updateRouteNode(q *routePriorityQueue, costs map[routeGridState]int64, came map[routeGridState]routeGridState, from, to routeGridState, nextCost, heuristic int64, sequence *int64) {
	if old, ok := costs[to]; ok && old <= nextCost {
		return
	}
	costs[to] = nextCost
	came[to] = from
	*sequence++
	heap.Push(q, &routeQueueItem{State: to, Cost: nextCost, Priority: nextCost + heuristic, Sequence: *sequence})
}

func (o routeObstacleSet) blocked(netID, layer string, point Point) bool {
	margin := o.widthNM/2 + o.clearNM
	if !inside(o.def.Board, point.XNM, point.YNM, margin) {
		return true
	}
	for _, keepout := range o.def.Keepouts {
		kind := strings.ToLower(keepout.Kind)
		if keepout.Layer != "" && keepout.Layer != layer {
			continue
		}
		if (kind == "antenna" || kind == "copper" || kind == "all") && pointInPolygon(point, keepout.Polygon) {
			return true
		}
	}
	for _, pad := range o.pads {
		if pad.NetID == "" || pad.NetID == netID || !containsString(pad.Pad.Layers, layer) {
			continue
		}
		if pointDistance(point, pad.Center) < padRadius(pad.Pad)+float64(margin) {
			return true
		}
	}
	for _, trace := range o.def.Traces {
		if trace.NetID == netID || trace.Layer != layer {
			continue
		}
		if pointToTraceDistance(point, trace) < float64(trace.WidthNM/2+margin) {
			return true
		}
	}
	for _, via := range o.def.Vias {
		if via.NetID == netID || !layerBetween(o.def, layer, via.FromLayer, via.ToLayer) {
			continue
		}
		if pointDistance(point, Point{XNM: via.XNM, YNM: via.YNM}) < float64(via.DiameterNM/2+margin) {
			return true
		}
	}
	for _, zone := range o.def.Zones {
		if zone.NetID != netID && zone.Layer == layer && pointInPolygon(point, zone.Polygon) {
			return true
		}
	}
	return false
}

func routePathOperations(def *Definition, netID string, path []routeGridState, start, goal Point, layers []string, gridNM, width, viaDiameter, viaDrill int64, branch int) ([]Operation, []Trace, []Via) {
	if len(path) == 0 {
		return nil, nil, nil
	}
	base := safeNativeID("auto-" + netID)
	traceSeq, viaSeq := nextRouteSequence(def, base), nextViaSequence(def, base)
	segments := []struct {
		layer  string
		points []Point
	}{}
	current := struct {
		layer  string
		points []Point
	}{layer: layers[path[0].Layer], points: []Point{start}}
	vias := []Via{}
	for i := 1; i < len(path); i++ {
		previous, state := path[i-1], path[i]
		point := routePathGridPoint(path, i, gridNM)
		if state.Layer != previous.Layer {
			transition := routePathGridPoint(path, i-1, gridNM)
			if len(current.points) == 0 || current.points[len(current.points)-1] != transition {
				current.points = append(current.points, transition)
			}
			segments = append(segments, current)
			viaID := uniqueViaID(def, fmt.Sprintf("%s-v%d", base, viaSeq))
			viaSeq++
			vias = append(vias, Via{ID: viaID, NetID: netID, XNM: transition.XNM, YNM: transition.YNM, DiameterNM: viaDiameter, DrillNM: viaDrill, FromLayer: layers[previous.Layer], ToLayer: layers[state.Layer]})
			current = struct {
				layer  string
				points []Point
			}{layer: layers[state.Layer], points: []Point{transition}}
			continue
		}
		if len(current.points) == 0 || current.points[len(current.points)-1] != point {
			current.points = append(current.points, point)
		}
	}
	if len(current.points) == 0 || current.points[len(current.points)-1] != goal {
		current.points = append(current.points, goal)
	}
	segments = append(segments, current)
	operations := []Operation{}
	traces := []Trace{}
	for _, segment := range segments {
		points := simplifyRoutePoints(segment.points)
		if len(points) < 2 {
			continue
		}
		id := uniqueTraceID(def, fmt.Sprintf("%s-t%d-b%d", base, traceSeq, branch))
		traceSeq++
		trace := Trace{ID: id, NetID: netID, Layer: segment.layer, WidthNM: width, Points: points}
		traces = append(traces, trace)
		operations = append(operations, Operation{Type: "trace.add", Trace: &trace})
	}
	for i := range vias {
		via := vias[i]
		operations = append(operations, Operation{Type: "via.add", Via: &via})
	}
	return operations, traces, vias
}

func routePathGridPoint(path []routeGridState, index int, gridNM int64) Point {
	if index < 0 {
		index = 0
	}
	if index >= len(path) {
		index = len(path) - 1
	}
	return Point{XNM: int64(path[index].X) * gridNM, YNM: int64(path[index].Y) * gridNM}
}

func simplifyRoutePoints(points []Point) []Point {
	if len(points) < 3 {
		return points
	}
	out := []Point{points[0]}
	for i := 1; i < len(points)-1; i++ {
		a, b, c := out[len(out)-1], points[i], points[i+1]
		if (b.XNM-a.XNM)*(c.YNM-b.YNM) == (b.YNM-a.YNM)*(c.XNM-b.XNM) {
			continue
		}
		out = append(out, b)
	}
	out = append(out, points[len(points)-1])
	return out
}

func traceLengthNM(trace Trace) int64 {
	var total float64
	for i := 1; i < len(trace.Points); i++ {
		total += pointDistance(trace.Points[i-1], trace.Points[i])
	}
	return int64(math.Round(total))
}

func safeNativeID(value string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(value) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.' || r == ':' {
			b.WriteRune(r)
		} else {
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-._:")
	if out == "" {
		return "auto-route"
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

func nextRouteSequence(def *Definition, base string) int { return len(def.Traces) + 1 }
func nextViaSequence(def *Definition, base string) int   { return len(def.Vias) + 1 }
func uniqueTraceID(def *Definition, candidate string) string {
	for suffix := 0; ; suffix++ {
		id := candidate
		if suffix > 0 {
			id = fmt.Sprintf("%s-%d", candidate, suffix)
		}
		if findTrace(def.Traces, id) < 0 {
			return id
		}
	}
}
func uniqueViaID(def *Definition, candidate string) string {
	for suffix := 0; ; suffix++ {
		id := candidate
		if suffix > 0 {
			id = fmt.Sprintf("%s-%d", candidate, suffix)
		}
		if findVia(def.Vias, id) < 0 {
			return id
		}
	}
}
func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

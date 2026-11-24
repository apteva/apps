package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

const routingSchema = "apteva-pcb-route/v1"

// defaultMaxRipupIterations bounds the negotiated rip-up-and-retry loop.
const defaultMaxRipupIterations = 8

// routeRipupBudgetFactor caps rip-up search effort at this many max_visited
// budgets on top of the greedy pass.
const routeRipupBudgetFactor = 20

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
	// AllowDiagonal enables 45-degree routing (default true). False keeps the
	// orthogonal-only router.
	AllowDiagonal *bool `json:"allow_diagonal,omitempty"`
	// MaxRipupIterations bounds rip-up and retry. 0 uses the default (8); a
	// negative value disables rip-up so the plan is the single greedy pass.
	MaxRipupIterations int `json:"max_ripup_iterations,omitempty"`
}

func (o RouteOptions) diagonal() bool { return o.AllowDiagonal == nil || *o.AllowDiagonal }

func (o RouteOptions) ripupIterations() int {
	if o.MaxRipupIterations < 0 {
		return 0
	}
	if o.MaxRipupIterations == 0 {
		return defaultMaxRipupIterations
	}
	return o.MaxRipupIterations
}

type RouteFailure struct {
	NetID   string `json:"net_id"`
	Message string `json:"message"`
}

type RouteMetrics struct {
	RequestedNets    int   `json:"requested_nets"`
	RoutedNets       int   `json:"routed_nets"`
	SkippedNets      int   `json:"skipped_nets"`
	TraceCount       int   `json:"trace_count"`
	ViaCount         int   `json:"via_count"`
	TotalLengthNM    int64 `json:"total_length_nm"`
	VisitedNodes     int   `json:"visited_nodes"`
	DiagonalSegments int   `json:"diagonal_segments"`
	Ripups           int   `json:"ripups"`
	Iterations       int   `json:"iterations"`
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

// routeNetResult is the committed copper of one routed net. It is immutable
// once built, so rip-up snapshots can share it.
type routeNetResult struct {
	ops    []Operation
	traces []Trace
	vias   []Via
}

type routeNetFailure struct {
	message  string
	from, to *placedPad // the branch whose search failed
}

type routeSnapshot struct {
	routed   []*routeNetResult
	failures map[int]string
}

// suggestRoutes plans copper for the requested nets on a grid.
//
// Each two-pad connection is an A* search over (x, y, layer, heading) states
// with 8-direction moves (45-degree routing; orthogonal only when
// allow_diagonal is false). Every move is clearance-checked as a real
// centreline segment against pad polygons, traces, vias, keepouts and zones,
// using the trace width and clearance. Multi-pad nets are routed as a spanning
// tree. Bends are at most 45° where geometry allows (a 90° corner costs far
// more than two 45° bends; sharper turns are never taken), and collinear grid
// points are merged so traces are minimal polylines.
//
// Nets are first routed greedily in priority order (differential-pair members
// first within a priority). Nets that fail then enter
// a negotiated rip-up-and-retry loop (PathFinder style): a relaxed search that
// may cross copper this run placed (never pre-existing copper) finds the
// cheapest conflicting path; the nets it crosses are ripped up, the failed net
// is routed, the ripped nets are re-routed, and the cells of the conflict gain
// history cost so later searches spread away from them. The best state seen
// (most routed nets, then shortest copper) is returned. Everything is
// deterministic: same input, identical operations.
func suggestRoutes(def *Definition, options RouteOptions) (*RoutePlan, error) {
	if def == nil {
		return nil, fmt.Errorf("definition required")
	}
	def = withFabProfile(def)
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

	r := newAutorouter(working, targets, layers, options)
	skipped := make([]bool, len(targets))
	failures := map[int]string{}
	for ti, net := range targets {
		pads := r.pads[ti]
		if len(pads) < 2 || (!options.ReplaceExisting && netCopperConnected(working, net.ID, pads)) {
			skipped[ti] = true
		}
	}
	for _, ti := range greedyRouteOrder(working, targets) {
		if skipped[ti] {
			continue
		}
		if result, failure := r.routeNet(ti); failure != nil {
			failures[ti] = failure.message
			r.failedAt[ti] = r.version
			r.lastFailure[ti] = failure
		} else {
			r.commit(ti, result)
		}
	}

	best := r.snapshot(failures)
	dead := map[int]bool{}
	// Bound the total search effort of rip-up so a hopeless board degrades
	// to the greedy result instead of stalling.
	budget := r.visited + routeRipupBudgetFactor*options.MaxVisited
	for iteration := 1; iteration <= options.ripupIterations() && r.visited < budget; iteration++ {
		pending := pendingRipupNets(failures, dead)
		if len(pending) == 0 {
			break
		}
		plan.Metrics.Iterations = iteration
		softPenalty := int64(1_000 + 500*iteration)
		for _, ti := range pending {
			if r.routed[ti] != nil || r.visited >= budget {
				continue
			}
			// The surroundings changed since this net last failed: a plain
			// retry may now succeed without ripping anything up.
			if r.failedAt[ti] != r.version {
				if result, failure := r.routeNet(ti); failure == nil {
					r.commit(ti, result)
					delete(failures, ti)
					continue
				} else {
					failures[ti] = failure.message
					r.failedAt[ti] = r.version
					r.lastFailure[ti] = failure
				}
			}
			blockers := r.relaxedBlockers(ti, r.lastFailure[ti], softPenalty)
			if len(blockers) == 0 {
				dead[ti] = true
				continue
			}
			for _, blocker := range blockers {
				r.ripUp(blocker)
				failures[blocker] = "ripped up to make room for " + targets[ti].ID + " and not re-routed"
				plan.Metrics.Ripups++
			}
			if result, failure := r.routeNet(ti); failure == nil {
				r.commit(ti, result)
				delete(failures, ti)
			} else {
				failures[ti] = failure.message
				r.failedAt[ti] = r.version
				r.lastFailure[ti] = failure
			}
			for _, blocker := range blockers {
				if result, failure := r.routeNet(blocker); failure == nil {
					r.commit(blocker, result)
					delete(failures, blocker)
				} else {
					failures[blocker] = failure.message
					r.failedAt[blocker] = r.version
					r.lastFailure[blocker] = failure
				}
			}
		}
		if r.betterThan(best) {
			best = r.snapshot(failures)
		}
	}
	r.routed = best.routed
	failures = best.failures
	r.repairDifferentialPairs()
	plan.Metrics.VisitedNodes = r.visited

	for ti, net := range targets {
		switch {
		case skipped[ti]:
			plan.Skipped = append(plan.Skipped, net.ID)
			plan.Metrics.SkippedNets++
		case r.routed[ti] != nil:
			result := r.routed[ti]
			plan.Operations = append(plan.Operations, removeOperations[net.ID]...)
			plan.Operations = append(plan.Operations, result.ops...)
			plan.RoutedNets = append(plan.RoutedNets, net.ID)
			plan.Metrics.RoutedNets++
			plan.Metrics.TraceCount += len(result.traces)
			plan.Metrics.ViaCount += len(result.vias)
			for _, trace := range result.traces {
				plan.Metrics.TotalLengthNM += traceLengthNM(trace)
				plan.Metrics.DiagonalSegments += diagonalSegmentCount(trace)
			}
		default:
			plan.Failures = append(plan.Failures, RouteFailure{NetID: net.ID, Message: failures[ti]})
		}
	}
	plan.Failures = append(plan.Failures, routedDifferentialPairFailures(r.compose(-1), targets)...)
	if len(plan.Failures) > 0 {
		plan.Status = "partial"
		if plan.Metrics.RoutedNets == 0 {
			plan.Status = "failed"
		}
	}
	return plan, nil
}

// greedyRouteOrder is the target order (net-class priority, then ID) with
// differential-pair members moved ahead of other nets of the same priority,
// so pairs are laid down side by side before the board fills up around them.
func greedyRouteOrder(def *Definition, targets []Net) []int {
	paired := map[string]bool{}
	for _, pair := range def.DifferentialPairs {
		paired[pair.PositiveNetID], paired[pair.NegativeNetID] = true, true
	}
	order := make([]int, len(targets))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, b := targets[order[i]], targets[order[j]]
		if pa, pb := netPriority(def, a.ID), netPriority(def, b.ID); pa != pb {
			return pa > pb
		}
		return paired[a.ID] && !paired[b.ID]
	})
	return order
}

func pendingRipupNets(failures map[int]string, dead map[int]bool) []int {
	out := []int{}
	for ti := range failures {
		if !dead[ti] {
			out = append(out, ti)
		}
	}
	sort.Ints(out)
	return out
}

func diagonalSegmentCount(trace Trace) int {
	count := 0
	for i := 1; i < len(trace.Points); i++ {
		dx, dy := absInt64(trace.Points[i].XNM-trace.Points[i-1].XNM), absInt64(trace.Points[i].YNM-trace.Points[i-1].YNM)
		if dx != 0 && dx == dy {
			count++
		}
	}
	return count
}

func (r *autorouter) commit(ti int, result *routeNetResult) {
	r.routed[ti] = result
	r.version++
}

func (r *autorouter) ripUp(ti int) {
	r.routed[ti] = nil
	r.version++
	r.failedAt[ti] = -1
}

func (r *autorouter) snapshot(failures map[int]string) routeSnapshot {
	out := routeSnapshot{routed: append([]*routeNetResult(nil), r.routed...), failures: map[int]string{}}
	for k, v := range failures {
		out.failures[k] = v
	}
	return out
}

func (r *autorouter) score(routed []*routeNetResult) (int, int64) {
	count, length := 0, int64(0)
	for _, result := range routed {
		if result == nil {
			continue
		}
		count++
		for _, trace := range result.traces {
			length += traceLengthNM(trace)
		}
	}
	return count, length
}

func (r *autorouter) betterThan(best routeSnapshot) bool {
	count, length := r.score(r.routed)
	bestCount, bestLength := r.score(best.routed)
	return count > bestCount || (count == bestCount && length < bestLength)
}

// repairDifferentialPairs re-routes both nets of a pair whose independently
// routed copper misses the pair's gap/skew constraint, trying the other net
// order and orthogonal-only routing (whose parallel runs keep equal lengths
// more often). The first variant that routes both nets and satisfies the
// constraint is kept; otherwise the original copper stays and the pair is
// reported as a failure. The router is not a coupled pair router.
func (r *autorouter) repairDifferentialPairs() {
	index := map[string]int{}
	for ti, net := range r.targets {
		index[net.ID] = ti
	}
	nets := map[string]Net{}
	for _, net := range r.base.Nets {
		nets[net.ID] = net
	}
	pairOK := func(pair DifferentialPair) bool {
		checks := []Check{}
		validateDifferentialPair(r.compose(-1), pair, nets, &checks)
		for _, check := range checks {
			if check.Severity == "error" {
				return false
			}
		}
		return true
	}
	diagonal := r.diagonal
	defer func() { r.diagonal = diagonal }()
	for _, pair := range r.base.DifferentialPairs {
		pi, pok := index[pair.PositiveNetID]
		ni, nok := index[pair.NegativeNetID]
		if !pok || !nok || r.routed[pi] == nil || r.routed[ni] == nil || pairOK(pair) {
			continue
		}
		savedP, savedN := r.routed[pi], r.routed[ni]
		// First try to shadow one net's copper onto the other at the pads'
		// common offset: identical geometry gives zero skew and a constant gap.
		shadowed := false
		for _, direction := range [][2]int{{pi, ni}, {ni, pi}} {
			src, dst := direction[0], direction[1]
			saved := r.routed[dst]
			r.routed[dst] = nil
			if shadow := r.shadowRoute(src, dst); shadow != nil {
				r.routed[dst] = shadow
				if pairOK(pair) {
					shadowed = true
					r.version++
					break
				}
			}
			r.routed[dst] = saved
		}
		if shadowed {
			continue
		}
		variants := []struct {
			diagonal    bool
			first, then int
		}{{diagonal, ni, pi}, {false, pi, ni}, {false, ni, pi}}
		repaired := false
		for _, variant := range variants {
			r.routed[pi], r.routed[ni] = nil, nil
			r.diagonal = variant.diagonal
			if first, failure := r.routeNet(variant.first); failure == nil {
				r.routed[variant.first] = first
				if then, failure := r.routeNet(variant.then); failure == nil {
					r.routed[variant.then] = then
					repaired = pairOK(pair)
				}
			}
			if repaired {
				r.version++
				break
			}
		}
		r.diagonal = diagonal
		if !repaired {
			r.routed[pi], r.routed[ni] = savedP, savedN
			repaired = r.rerouteWithPairFirst(pi, ni, func() bool { return pairOK(pair) })
		}
		if repaired {
			r.version++
		}
	}
}

// rerouteWithPairFirst re-lays every net this run routed, starting with the
// pair, so other nets cannot fence the pair onto unequal detours. It keeps the
// result only if every previously routed net still routes and accept holds.
func (r *autorouter) rerouteWithPairFirst(pi, ni int, accept func() bool) bool {
	saved := append([]*routeNetResult(nil), r.routed...)
	order := []int{pi, ni}
	for _, ti := range greedyRouteOrder(r.base, r.targets) {
		if ti != pi && ti != ni && saved[ti] != nil {
			order = append(order, ti)
		}
	}
	for i := range r.routed {
		r.routed[i] = nil
	}
	ok := true
	for _, ti := range order {
		result, failure := r.routeNet(ti)
		if failure != nil {
			ok = false
			break
		}
		r.routed[ti] = result
	}
	if ok && accept() {
		return true
	}
	copy(r.routed, saved)
	return false
}

// shadowRoute copies src's routed copper onto dst, translated by the offset
// between their pads, when every dst pad sits at the same offset from its src
// counterpart (the usual differential-pair footprint pattern) and the copy
// clears everything, including src itself. It returns nil otherwise.
func (r *autorouter) shadowRoute(src, dst int) *routeNetResult {
	from, to := r.pads[src], r.pads[dst]
	if r.routed[src] == nil || len(from) == 0 || len(from) != len(to) {
		return nil
	}
	dx, dy := to[0].Center.XNM-from[0].Center.XNM, to[0].Center.YNM-from[0].Center.YNM
	for i := range from {
		if to[i].Center.XNM-from[i].Center.XNM != dx || to[i].Center.YNM-from[i].Center.YNM != dy {
			return nil
		}
	}
	shift := func(p Point) Point { return Point{XNM: p.XNM + dx, YNM: p.YNM + dy} }
	layerIndex := map[string]int{}
	for i, layer := range r.layers {
		layerIndex[layer] = i
	}
	ctx := r.newNetContext(dst)
	def := r.compose(dst)
	net := r.targets[dst]
	base := safeNativeID("auto-" + net.ID)
	result := &routeNetResult{}
	for _, trace := range r.routed[src].traces {
		layer, ok := layerIndex[trace.Layer]
		if !ok {
			return nil
		}
		points := make([]Point, len(trace.Points))
		for i, p := range trace.Points {
			points[i] = shift(p)
			if !ctx.onBoard(points[i], ctx.traceMargin) {
				return nil
			}
			if i > 0 && ctx.query(layer, points[i-1], points[i], ctx.traceMargin, false, nil) != routeFree {
				return nil
			}
		}
		copied := Trace{ID: uniqueTraceID(def, fmt.Sprintf("%s-t%d-s", base, len(def.Traces)+1)), NetID: net.ID, Layer: trace.Layer, WidthNM: ctx.width, Points: points}
		def.Traces = append(def.Traces, copied)
		result.traces = append(result.traces, copied)
		result.ops = append(result.ops, Operation{Type: "trace.add", Trace: &copied})
	}
	for _, via := range r.routed[src].vias {
		center := shift(Point{XNM: via.XNM, YNM: via.YNM})
		if !ctx.onBoard(center, ctx.viaMargin) {
			return nil
		}
		for layer := range r.layers {
			if ctx.query(layer, center, center, ctx.viaMargin, false, nil) != routeFree {
				return nil
			}
		}
		copied := Via{ID: uniqueViaID(def, fmt.Sprintf("%s-v%d-s", base, len(def.Vias)+1)), NetID: net.ID, XNM: center.XNM, YNM: center.YNM, DiameterNM: ctx.viaDiameter, DrillNM: ctx.viaDrill, FromLayer: via.FromLayer, ToLayer: via.ToLayer}
		def.Vias = append(def.Vias, copied)
		result.vias = append(result.vias, copied)
		result.ops = append(result.ops, Operation{Type: "via.add", Via: &copied})
	}
	return result
}

// routeNet routes every spanning-tree branch of one net against the current
// copper. It never mutates the router state; the caller commits the result.
func (r *autorouter) routeNet(ti int) (*routeNetResult, *routeNetFailure) {
	net := r.targets[ti]
	ctx := r.newNetContext(ti)
	def := r.compose(ti)
	result := &routeNetResult{}
	pads := r.pads[ti]
	for branchIndex, pair := range routingTreePairs(pads) {
		from, to := pads[pair[0]], pads[pair[1]]
		path, err := ctx.search(from, to, false, 0)
		if err != nil {
			return nil, &routeNetFailure{message: fmt.Sprintf("%s to %s: %v", from.ComponentID+":"+from.Pad.ID, to.ComponentID+":"+to.Pad.ID, err), from: from, to: to}
		}
		ops, traces, vias := routePathOperations(def, net.ID, path, from.Center, to.Center, r.layers, r.options.GridNM, ctx.width, ctx.viaDiameter, ctx.viaDrill, branchIndex+1)
		result.ops = append(result.ops, ops...)
		result.traces = append(result.traces, traces...)
		result.vias = append(result.vias, vias...)
		def.Traces = append(def.Traces, traces...)
		def.Vias = append(def.Vias, vias...)
	}
	return result, nil
}

// relaxedBlockers searches the failed branch again while letting it cross
// copper placed by this run (at softPenalty per conflicting move), then
// returns the nets that path crosses and adds history cost on its conflict
// cells. Pre-existing copper, pads, keepouts and zones stay hard obstacles.
func (r *autorouter) relaxedBlockers(ti int, failure *routeNetFailure, softPenalty int64) []int {
	if failure == nil || failure.from == nil || failure.to == nil {
		return nil
	}
	ctx := r.newNetContext(ti)
	path, err := ctx.search(failure.from, failure.to, true, softPenalty)
	if err != nil {
		return nil
	}
	owners := map[int32]bool{}
	for i := 1; i < len(path); i++ {
		prev, cur := path[i-1], path[i]
		hit := false
		collect := func(owner int32) { owners[owner], hit = true, true }
		if prev.Layer == cur.Layer {
			if ctx.moveStatus(prev.Layer, prev.X, prev.Y, cur.Dir) == routeSoft {
				ctx.edgeQuery(prev.Layer, prev.X, prev.Y, cur.X, cur.Y, collect)
			}
		} else if ctx.viaStatus(cur.X, cur.Y) == routeSoft {
			ctx.viaQuery(cur.X, cur.Y, collect)
		}
		if hit {
			r.addHistory(r.node(prev.Layer, prev.X, prev.Y))
			r.addHistory(r.node(cur.Layer, cur.X, cur.Y))
		}
	}
	out := make([]int, 0, len(owners))
	for owner := range owners {
		if int(owner) != ti && r.routed[owner] != nil {
			out = append(out, int(owner))
		}
	}
	sort.Ints(out)
	return out
}

// compose returns the working definition plus every committed net except
// exclude (-1 keeps all), in target order.
func (r *autorouter) compose(exclude int) *Definition {
	def := *r.base
	def.Traces = append([]Trace(nil), r.base.Traces...)
	def.Vias = append([]Via(nil), r.base.Vias...)
	for ti, result := range r.routed {
		if result == nil || ti == exclude {
			continue
		}
		def.Traces = append(def.Traces, result.traces...)
		def.Vias = append(def.Vias, result.vias...)
	}
	return &def
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

// routingTreePairs returns deterministic Prim-order (from, to) pad index pairs
// over Manhattan distance, starting from pad 0. Ties break on lower index.
func routingTreePairs(pads []*placedPad) [][2]int {
	n := len(pads)
	if n < 2 {
		return nil
	}
	connected := make([]bool, n)
	best := make([]int64, n)
	parent := make([]int, n)
	connected[0] = true
	manhattan := func(a, b Point) int64 { return absInt64(a.XNM-b.XNM) + absInt64(a.YNM-b.YNM) }
	for i := 1; i < n; i++ {
		best[i], parent[i] = manhattan(pads[0].Center, pads[i].Center), 0
	}
	pairs := make([][2]int, 0, n-1)
	for len(pairs) < n-1 {
		next := -1
		for i := 1; i < n; i++ {
			if !connected[i] && (next < 0 || best[i] < best[next]) {
				next = i
			}
		}
		connected[next] = true
		pairs = append(pairs, [2]int{parent[next], next})
		for i := 1; i < n; i++ {
			if !connected[i] {
				if d := manhattan(pads[next].Center, pads[i].Center); d < best[i] {
					best[i], parent[i] = d, next
				}
			}
		}
	}
	return pairs
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
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

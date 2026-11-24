package main

import (
	"fmt"
	"math"
	"strings"
)

// Grid search engine for the native autorouter (see suggestRoutes).
//
// Costs are integers in hundredths of a grid step so the search is exact and
// deterministic: an orthogonal step costs 100, a diagonal step 141 (~100*√2).

const (
	routeStepOrtho    = 100
	routeStepDiag     = 141
	routeTurn45       = 20  // small penalty per 45° bend (diagonal mode)
	routeTurn90Diag   = 250 // a 90° corner in diagonal mode; only where two 45° bends do not fit
	routeTurnOrtho    = 40  // any bend in orthogonal mode (historical 4/10 of a step)
	routeViaCost      = 800
	routeHistoryCost  = 60 // per unit of negotiated congestion history on a node
	routeLayerBias    = 30
	routeDirNone      = 8
	routeDirStates    = 9
	routeBucketNM     = 2_000_000
	routeEpsNM        = 1_000 // keep a micron of slack over the DRC limits
	routeDenseStates  = 6_000_000
	routeFree         = uint8(0)
	routeSoft         = uint8(1) // conflicts only with copper this run placed (rippable)
	routeHard         = uint8(2)
	routeObstacleSeg  = uint8(0) // segment or via disc with a radius
	routeObstaclePad  = uint8(1) // exact pad polygon, clearance-checked
	routeObstacleArea = uint8(2) // keepout or zone: the centreline may not enter
)

// routeDirs lists the 8 headings clockwise from east (+y is down the board),
// so a heading and its reverse differ by 4 and adjacent headings by 45°.
var routeDirs = [8][2]int{{1, 0}, {1, 1}, {0, 1}, {-1, 1}, {-1, 0}, {-1, -1}, {0, -1}, {1, -1}}

type autorouter struct {
	base        *Definition
	targets     []Net
	layers      []string
	options     RouteOptions
	diagonal    bool
	allPads     []*placedPad
	pads        [][]*placedPad
	routed      []*routeNetResult
	failedAt    []int
	lastFailure []*routeNetFailure
	version     int
	visited     int
	w, h        int
	history     map[int64]int32
	widths      map[string]int64
	boardPoly   []Point

	// Search storage, reused across searches with a generation stamp.
	dense    bool
	gen      uint32
	stamp    []uint32
	gCost    []int64
	came     []int64
	sCost    map[int64]int64
	sCame    map[int64]int64
	heap     routeHeap
	sequence int64
}

func newAutorouter(working *Definition, targets []Net, layers []string, options RouteOptions) *autorouter {
	grid := options.GridNM
	r := &autorouter{base: working, targets: targets, layers: layers, options: options, diagonal: options.diagonal(), history: map[int64]int32{}, widths: map[string]int64{}}
	r.w, r.h = int(working.Board.WidthNM/grid)+2, int(working.Board.HeightNM/grid)+2
	if r.w < 2 {
		r.w = 2
	}
	if r.h < 2 {
		r.h = 2
	}
	if len(working.Board.Outline) >= 3 {
		r.boardPoly = working.Board.Outline
	}
	r.allPads = routingPads(working)
	r.pads = make([][]*placedPad, len(targets))
	for ti, net := range targets {
		r.pads[ti] = padsForRoutingNet(working, net, r.allPads)
	}
	r.routed = make([]*routeNetResult, len(targets))
	r.failedAt = make([]int, len(targets))
	r.lastFailure = make([]*routeNetFailure, len(targets))
	states := int64(r.w) * int64(r.h) * int64(len(layers)) * routeDirStates
	r.dense = states <= routeDenseStates
	if r.dense {
		r.stamp = make([]uint32, states)
		r.gCost = make([]int64, states)
		r.came = make([]int64, states)
	}
	return r
}

// netWidth is the trace width the router uses for a net.
func (r *autorouter) netWidth(netID string) int64 {
	if width, ok := r.widths[netID]; ok {
		return width
	}
	width, _, _, _ := routingRules(r.base, netID, r.options)
	r.widths[netID] = width
	return width
}

func (r *autorouter) nodeCount() int64 { return int64(r.w) * int64(r.h) * int64(len(r.layers)) }
func (r *autorouter) node(layer, x, y int) int64 {
	return (int64(layer)*int64(r.h)+int64(y))*int64(r.w) + int64(x)
}
func (r *autorouter) decode(node int64) (layer, x, y int) {
	x = int(node % int64(r.w))
	node /= int64(r.w)
	return int(node / int64(r.h)), x, int(node % int64(r.h))
}
func (r *autorouter) inGrid(x, y int) bool { return x >= 0 && y >= 0 && x < r.w && y < r.h }
func (r *autorouter) toGrid(v int64) int {
	return int(math.Round(float64(v) / float64(r.options.GridNM)))
}
func (r *autorouter) point(x, y int) Point {
	return Point{XNM: int64(x) * r.options.GridNM, YNM: int64(y) * r.options.GridNM}
}
func (r *autorouter) addHistory(node int64) { r.history[node]++ }
func (r *autorouter) historyCost(node int64) int64 {
	if len(r.history) == 0 {
		return 0
	}
	return int64(r.history[node]) * routeHistoryCost
}

func (r *autorouter) beginSearch() {
	r.heap = r.heap[:0]
	r.sequence = 0
	if !r.dense {
		r.sCost, r.sCame = map[int64]int64{}, map[int64]int64{}
		return
	}
	r.gen++
	if r.gen == 0 {
		clear(r.stamp)
		r.gen = 1
	}
}

func (r *autorouter) costOf(state int64) (int64, bool) {
	if r.dense {
		if r.stamp[state] != r.gen {
			return 0, false
		}
		return r.gCost[state], true
	}
	cost, ok := r.sCost[state]
	return cost, ok
}

func (r *autorouter) cameFrom(state int64) int64 {
	if r.dense {
		return r.came[state]
	}
	return r.sCame[state]
}

func (r *autorouter) relax(state, from, cost, heuristic int64) {
	if old, ok := r.costOf(state); ok && old <= cost {
		return
	}
	if r.dense {
		r.stamp[state], r.gCost[state], r.came[state] = r.gen, cost, from
	} else {
		r.sCost[state], r.sCame[state] = cost, from
	}
	r.sequence++
	r.heap.push(routeHeapItem{priority: cost + heuristic, cost: cost, sequence: r.sequence, state: state})
}

// routeCache memoises a per-node or per-edge status (stored +1; 0 = unknown).
type routeCache struct {
	dense  []uint8
	sparse map[int64]uint8
}

func newRouteCache(size int64, dense bool) routeCache {
	if dense {
		return routeCache{dense: make([]uint8, size)}
	}
	return routeCache{sparse: map[int64]uint8{}}
}

func (c *routeCache) get(i int64) (uint8, bool) {
	var v uint8
	if c.dense != nil {
		v = c.dense[i]
	} else {
		v = c.sparse[i]
	}
	return v - 1, v != 0
}

func (c *routeCache) set(i int64, status uint8) {
	if c.dense != nil {
		c.dense[i] = status + 1
	} else {
		c.sparse[i] = status + 1
	}
}

type routeObstacle struct {
	kind                   uint8
	pad                    bool  // pad copper: relaxed for the terminal steps of a route
	owner                  int32 // -1: fixed; otherwise the target index whose routed copper this is
	a, b                   Point
	radius                 float64
	poly                   []Point
	minX, minY, maxX, maxY int64
}

func (o *routeObstacle) hits(p, q Point, margin float64) bool {
	switch o.kind {
	case routeObstacleSeg:
		return segmentDistance(p, q, o.a, o.b) < o.radius+margin+routeEpsNM
	case routeObstaclePad:
		return segmentPolygonDistance(p, q, o.poly) < margin+routeEpsNM
	default:
		return segmentTouchesPolygon(p, q, o.poly)
	}
}

func segmentPolygonDistance(p, q Point, polygon []Point) float64 {
	if len(polygon) == 0 {
		return math.MaxFloat64
	}
	if pointInPolygon(p, polygon) || pointInPolygon(q, polygon) {
		return 0
	}
	minimum := math.MaxFloat64
	for i := range polygon {
		minimum = math.Min(minimum, segmentDistance(p, q, polygon[i], polygon[(i+1)%len(polygon)]))
	}
	return minimum
}

func segmentTouchesPolygon(p, q Point, polygon []Point) bool {
	if len(polygon) < 3 {
		return false
	}
	if pointInPolygon(p, polygon) || pointInPolygon(q, polygon) {
		return true
	}
	for i := range polygon {
		if segmentsIntersect(p, q, polygon[i], polygon[(i+1)%len(polygon)]) {
			return true
		}
	}
	return false
}

// routeNetContext holds the obstacles seen by one net (everything that is not
// its own copper) in a per-layer bucket index, plus memoised clearance
// results for nodes, edges and via sites.
type routeNetContext struct {
	r                                       *autorouter
	ti                                      int
	netID                                   string
	width, clearance, viaDiameter, viaDrill int64
	traceMargin, viaMargin                  int64
	obstacles                               []routeObstacle
	buckets                                 [][][]int32
	bw, bh                                  int
	seen                                    []uint32
	seenGen                                 uint32
	nodes, edges, viaSites                  routeCache
	sx, sy, gx, gy                          int
}

func (r *autorouter) newNetContext(ti int) *routeNetContext {
	net := r.targets[ti]
	c := &routeNetContext{r: r, ti: ti, netID: net.ID}
	c.width, c.clearance, c.viaDiameter, c.viaDrill = routingRules(r.base, net.ID, r.options)
	c.traceMargin = c.width/2 + c.clearance
	c.viaMargin = c.viaDiameter/2 + c.clearance
	c.bw, c.bh = int(r.base.Board.WidthNM/routeBucketNM)+1, int(r.base.Board.HeightNM/routeBucketNM)+1
	c.buckets = make([][][]int32, len(r.layers))
	for li := range c.buckets {
		c.buckets[li] = make([][]int32, c.bw*c.bh)
	}
	def := r.base
	for _, pad := range r.allPads {
		if pad.NetID == "" || pad.NetID == net.ID {
			continue
		}
		onPadLayer := func(layer string) bool { return containsString(pad.Pad.Layers, layer) }
		c.add(routeObstacle{kind: routeObstaclePad, pad: true, owner: -1, poly: pad.Polygon}, onPadLayer)
		// The pad's own route will end at its centre with its net's trace
		// width, so keep that disc clear as well as the pad polygon.
		c.add(routeObstacle{kind: routeObstacleSeg, pad: true, owner: -1, a: pad.Center, b: pad.Center, radius: float64(r.netWidth(pad.NetID)) / 2}, onPadLayer)
	}
	for _, keepout := range def.Keepouts {
		kind := keepoutKind(keepout)
		if kind != "antenna" && kind != "copper" && kind != "all" {
			continue
		}
		c.add(routeObstacle{kind: routeObstacleArea, owner: -1, poly: keepout.Polygon}, func(layer string) bool { return keepout.Layer == "" || keepout.Layer == layer })
	}
	for _, zone := range def.Zones {
		if zone.NetID == net.ID {
			continue
		}
		c.add(routeObstacle{kind: routeObstacleArea, owner: -1, poly: zone.Polygon}, func(layer string) bool { return zone.Layer == layer })
	}
	addCopper := func(traces []Trace, vias []Via, owner int32) {
		for _, trace := range traces {
			if trace.NetID == net.ID {
				continue
			}
			for i := 1; i < len(trace.Points); i++ {
				c.add(routeObstacle{kind: routeObstacleSeg, owner: owner, a: trace.Points[i-1], b: trace.Points[i], radius: float64(trace.WidthNM) / 2}, func(layer string) bool { return trace.Layer == layer })
			}
		}
		for _, via := range vias {
			if via.NetID == net.ID {
				continue
			}
			center := Point{XNM: via.XNM, YNM: via.YNM}
			c.add(routeObstacle{kind: routeObstacleSeg, owner: owner, a: center, b: center, radius: float64(via.DiameterNM) / 2}, func(layer string) bool { return layerBetween(def, layer, via.FromLayer, via.ToLayer) })
		}
	}
	addCopper(def.Traces, def.Vias, -1)
	for owner, result := range r.routed {
		if result != nil && owner != ti {
			addCopper(result.traces, result.vias, int32(owner))
		}
	}
	c.seen = make([]uint32, len(c.obstacles))
	nodes := r.nodeCount()
	c.nodes = newRouteCache(nodes, r.dense)
	c.edges = newRouteCache(nodes*4, r.dense)
	c.viaSites = newRouteCache(int64(r.w)*int64(r.h), r.dense)
	return c
}

func keepoutKind(keepout Keepout) string { return strings.ToLower(keepout.Kind) }

func (c *routeNetContext) bucket(v int64, limit int) int {
	b := int(v / routeBucketNM)
	if v < 0 || b < 0 {
		return 0
	}
	if b >= limit {
		return limit - 1
	}
	return b
}

func (c *routeNetContext) add(o routeObstacle, onLayer func(string) bool) {
	switch {
	case len(o.poly) > 0:
		o.minX, o.minY, o.maxX, o.maxY = o.poly[0].XNM, o.poly[0].YNM, o.poly[0].XNM, o.poly[0].YNM
		for _, p := range o.poly[1:] {
			o.minX, o.minY = min(o.minX, p.XNM), min(o.minY, p.YNM)
			o.maxX, o.maxY = max(o.maxX, p.XNM), max(o.maxY, p.YNM)
		}
	default:
		pad := int64(math.Ceil(o.radius))
		o.minX, o.minY = min(o.a.XNM, o.b.XNM)-pad, min(o.a.YNM, o.b.YNM)-pad
		o.maxX, o.maxY = max(o.a.XNM, o.b.XNM)+pad, max(o.a.YNM, o.b.YNM)+pad
	}
	id := int32(len(c.obstacles))
	placed := false
	for li, layer := range c.r.layers {
		if !onLayer(layer) {
			continue
		}
		placed = true
		for by := c.bucket(o.minY, c.bh); by <= c.bucket(o.maxY, c.bh); by++ {
			for bx := c.bucket(o.minX, c.bw); bx <= c.bucket(o.maxX, c.bw); bx++ {
				cell := by*c.bw + bx
				c.buckets[li][cell] = append(c.buckets[li][cell], id)
			}
		}
	}
	if placed {
		c.obstacles = append(c.obstacles, o)
	}
}

// query classifies the centreline segment p-q (a point when p == q) of
// half-width margin-clearance on one layer: free, soft (touches only copper
// this run placed) or hard. collect, when set, receives every rippable owner.
func (c *routeNetContext) query(layer int, p, q Point, margin int64, ignorePads bool, collect func(int32)) uint8 {
	m := margin + routeEpsNM
	minX, minY := min(p.XNM, q.XNM)-m, min(p.YNM, q.YNM)-m
	maxX, maxY := max(p.XNM, q.XNM)+m, max(p.YNM, q.YNM)+m
	c.seenGen++
	if c.seenGen == 0 {
		clear(c.seen)
		c.seenGen = 1
	}
	status := routeFree
	buckets := c.buckets[layer]
	for by := c.bucket(minY, c.bh); by <= c.bucket(maxY, c.bh); by++ {
		for bx := c.bucket(minX, c.bw); bx <= c.bucket(maxX, c.bw); bx++ {
			for _, id := range buckets[by*c.bw+bx] {
				if c.seen[id] == c.seenGen {
					continue
				}
				c.seen[id] = c.seenGen
				o := &c.obstacles[id]
				if ignorePads && o.pad {
					continue
				}
				if o.maxX < minX || o.minX > maxX || o.maxY < minY || o.minY > maxY {
					continue
				}
				if !o.hits(p, q, float64(margin)) {
					continue
				}
				if o.owner < 0 {
					if collect == nil {
						return routeHard
					}
					status = routeHard
					continue
				}
				if status == routeFree {
					status = routeSoft
				}
				if collect != nil {
					collect(o.owner)
				}
			}
		}
	}
	return status
}

func (c *routeNetContext) onBoard(p Point, margin int64) bool {
	return inside(c.r.base.Board, p.XNM, p.YNM, margin)
}

func (c *routeNetContext) nodeStatus(layer, x, y int) uint8 {
	key := c.r.node(layer, x, y)
	if status, ok := c.nodes.get(key); ok {
		return status
	}
	p := c.r.point(x, y)
	status := routeHard
	if c.onBoard(p, c.traceMargin) {
		status = c.query(layer, p, p, c.traceMargin, false, nil)
	}
	c.nodes.set(key, status)
	return status
}

// edgeStatus is the clearance status of the straight move from (x, y) in
// direction dir. Edges are undirected, so each is cached once under its
// east/south-east/south/south-west orientation.
func (c *routeNetContext) edgeStatus(layer, x, y, dir int) uint8 {
	nx, ny := x+routeDirs[dir][0], y+routeDirs[dir][1]
	bx, by, forward := x, y, dir
	if dir >= 4 {
		bx, by, forward = nx, ny, dir-4
	}
	key := c.r.node(layer, bx, by)*4 + int64(forward)
	if status, ok := c.edges.get(key); ok {
		return status
	}
	p, q := c.r.point(x, y), c.r.point(nx, ny)
	status := routeHard
	if c.onBoard(p, c.traceMargin) && c.onBoard(q, c.traceMargin) {
		status = c.query(layer, p, q, c.traceMargin, false, nil)
	}
	c.edges.set(key, status)
	return status
}

func (c *routeNetContext) edgeQuery(layer, x, y, nx, ny int, collect func(int32)) {
	c.query(layer, c.r.point(x, y), c.r.point(nx, ny), c.traceMargin, false, collect)
}

func (c *routeNetContext) terminal(x, y int) bool {
	return (x == c.sx && y == c.sy) || (x == c.gx && y == c.gy)
}

// moveStatus is edgeStatus with the historical terminal relaxation: an
// orthogonal step out of the start or into the goal grid point (which sit on
// pads, often off-grid and close to fine-pitch neighbours) only needs its
// non-terminal end clear of pads; the segment must still clear every trace,
// via, keepout and zone.
func (c *routeNetContext) moveStatus(layer, x, y, dir int) uint8 {
	status := c.edgeStatus(layer, x, y, dir)
	if status == routeFree || dir%2 != 0 {
		return status
	}
	nx, ny := x+routeDirs[dir][0], y+routeDirs[dir][1]
	curTerminal, nextTerminal := c.terminal(x, y), c.terminal(nx, ny)
	if !curTerminal && !nextTerminal {
		return status
	}
	alternative := c.query(layer, c.r.point(x, y), c.r.point(nx, ny), c.traceMargin, true, nil)
	if !nextTerminal && alternative != routeHard {
		alternative = max(alternative, c.nodeStatus(layer, nx, ny))
	}
	return min(status, alternative)
}

// viaStatus checks a via at a grid point with its own diameter on every
// routed layer it spans, plus the board edge.
func (c *routeNetContext) viaStatus(x, y int) uint8 {
	key := int64(y)*int64(c.r.w) + int64(x)
	if status, ok := c.viaSites.get(key); ok {
		return status
	}
	p := c.r.point(x, y)
	status := routeFree
	if !c.onBoard(p, c.viaMargin) {
		status = routeHard
	} else if poly := c.r.boardPoly; poly != nil && (!pointInPolygonOrBoundary(p, poly) || pointToPolygonEdgeDistance(p, poly) < float64(c.viaDiameter/2+c.r.base.Rules.MinEdgeClearanceNM)) {
		status = routeHard
	}
	for layer := range c.r.layers {
		if status == routeHard {
			break
		}
		status = max(status, c.query(layer, p, p, c.viaMargin, false, nil))
	}
	c.viaSites.set(key, status)
	return status
}

func (c *routeNetContext) viaQuery(x, y int, collect func(int32)) {
	p := c.r.point(x, y)
	for layer := range c.r.layers {
		c.query(layer, p, p, c.viaMargin, false, collect)
	}
}

// search runs A* between two pads. In relaxed mode, moves that only conflict
// with copper placed by this run are allowed at softPenalty each.
func (c *routeNetContext) search(from, to *placedPad, relaxed bool, softPenalty int64) ([]routeGridState, error) {
	r := c.r
	c.sx, c.sy = r.toGrid(from.Center.XNM), r.toGrid(from.Center.YNM)
	c.gx, c.gy = r.toGrid(to.Center.XNM), r.toGrid(to.Center.YNM)
	if !r.inGrid(c.sx, c.sy) || !r.inGrid(c.gx, c.gy) {
		return nil, fmt.Errorf("pad lies outside the routing grid")
	}
	// A route may only leave its start pad, and finish at its goal pad, on a
	// copper layer that pad actually occupies. Pads with no declared layers
	// keep the historical behaviour (any routing layer).
	layerAllowed := func(padLayers []string, index int) bool {
		return len(padLayers) == 0 || containsString(padLayers, r.layers[index])
	}
	heuristic := func(x, y, layer int) int64 {
		dx, dy := int64(absInt(x-c.gx)), int64(absInt(y-c.gy))
		h := (dx + dy) * routeStepOrtho
		if r.diagonal {
			h = max(dx, dy)*routeStepOrtho + min(dx, dy)*(routeStepDiag-routeStepOrtho)
		}
		return h + int64(layer)*routeLayerBias
	}
	// The stubs between an off-grid pad centre and its grid point are part of
	// the trace too; they only need to clear non-pad copper.
	stubAllowed := func(center Point, x, y, layer int) bool {
		status := c.query(layer, center, r.point(x, y), c.traceMargin, true, nil)
		return status == routeFree || (relaxed && status == routeSoft)
	}
	r.beginSearch()
	started := false
	for layer := range r.layers {
		if layerAllowed(from.Pad.Layers, layer) && stubAllowed(from.Center, c.sx, c.sy, layer) {
			r.relax(r.node(layer, c.sx, c.sy)*routeDirStates+routeDirNone, -1, 0, heuristic(c.sx, c.sy, layer))
			started = true
		}
	}
	if !started && len(from.Pad.Layers) == 0 {
		r.relax(r.node(0, c.sx, c.sy)*routeDirStates+routeDirNone, -1, 0, heuristic(c.sx, c.sy, 0))
	}
	dirStep := 1
	if !r.diagonal {
		dirStep = 2
	}
	maxVisited := r.options.MaxVisited
	visited := 0
	defer func() { r.visited += visited }()
	for r.heap.len() > 0 && visited < maxVisited {
		item := r.heap.pop()
		if best, ok := r.costOf(item.state); !ok || best != item.cost {
			continue
		}
		visited++
		node, dir := item.state/routeDirStates, int(item.state%routeDirStates)
		layer, x, y := r.decode(node)
		if x == c.gx && y == c.gy && layerAllowed(to.Pad.Layers, layer) && stubAllowed(to.Center, c.gx, c.gy, layer) {
			return r.reconstruct(item.state), nil
		}
		for next := 0; next < 8; next += dirStep {
			turnCost := int64(0)
			if dir != routeDirNone && dir != next {
				turn := absInt(dir - next)
				turn = min(turn, 8-turn)
				switch {
				case !r.diagonal:
					turnCost = routeTurnOrtho
				case turn == 1:
					turnCost = routeTurn45
				case turn == 2:
					turnCost = routeTurn90Diag
				default:
					continue // never turn sharper than 90°
				}
			}
			nx, ny := x+routeDirs[next][0], y+routeDirs[next][1]
			if !r.inGrid(nx, ny) {
				continue
			}
			status := c.moveStatus(layer, x, y, next)
			if status == routeHard || (status == routeSoft && !relaxed) {
				continue
			}
			step := int64(routeStepOrtho)
			if next%2 == 1 {
				step = routeStepDiag
			}
			nextNode := r.node(layer, nx, ny)
			cost := item.cost + step + turnCost + r.historyCost(nextNode)
			if status == routeSoft {
				cost += softPenalty
			}
			r.relax(nextNode*routeDirStates+int64(next), item.state, cost, heuristic(nx, ny, layer))
		}
		// Never drop a via on the start or goal pad itself: the pad would then
		// be joined only by a via-in-pad with no same-layer trace touching it.
		if len(r.layers) > 1 && !c.terminal(x, y) {
			status := c.viaStatus(x, y)
			if status == routeHard || (status == routeSoft && !relaxed) {
				continue
			}
			for other := range r.layers {
				if other == layer {
					continue
				}
				nextNode := r.node(other, x, y)
				cost := item.cost + routeViaCost + r.historyCost(nextNode)
				if status == routeSoft {
					cost += softPenalty
				}
				r.relax(nextNode*routeDirStates+routeDirNone, item.state, cost, heuristic(x, y, other))
			}
		}
	}
	if visited >= maxVisited {
		return nil, fmt.Errorf("search exceeded %d visited nodes", maxVisited)
	}
	return nil, fmt.Errorf("no clearance-safe path found")
}

func (r *autorouter) reconstruct(state int64) []routeGridState {
	path := []routeGridState{}
	for state >= 0 {
		layer, x, y := r.decode(state / routeDirStates)
		path = append(path, routeGridState{X: x, Y: y, Layer: layer, Dir: int(state % routeDirStates)})
		state = r.cameFrom(state)
	}
	for i, j := 0, len(path)-1; i < j; i, j = i+1, j-1 {
		path[i], path[j] = path[j], path[i]
	}
	return path
}

type routeHeapItem struct {
	priority, cost, sequence int64
	state                    int64
}

// routeHeap is a binary min-heap ordered by (priority, cost, sequence), so
// ties resolve in insertion order and the search is deterministic.
type routeHeap []routeHeapItem

func (h routeHeap) len() int { return len(h) }

func (h routeHeap) less(i, j int) bool {
	a, b := h[i], h[j]
	if a.priority != b.priority {
		return a.priority < b.priority
	}
	if a.cost != b.cost {
		return a.cost < b.cost
	}
	return a.sequence < b.sequence
}

func (h *routeHeap) push(item routeHeapItem) {
	*h = append(*h, item)
	q := *h
	for i := len(q) - 1; i > 0; {
		parent := (i - 1) / 2
		if !q.less(i, parent) {
			break
		}
		q[i], q[parent] = q[parent], q[i]
		i = parent
	}
}

func (h *routeHeap) pop() routeHeapItem {
	q := *h
	top := q[0]
	last := len(q) - 1
	q[0] = q[last]
	q = q[:last]
	for i := 0; ; {
		left, right, smallest := 2*i+1, 2*i+2, i
		if left < len(q) && q.less(left, smallest) {
			smallest = left
		}
		if right < len(q) && q.less(right, smallest) {
			smallest = right
		}
		if smallest == i {
			break
		}
		q[i], q[smallest] = q[smallest], q[i]
		i = smallest
	}
	*h = q
	return top
}

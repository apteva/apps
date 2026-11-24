package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const placementSchema = "apteva-pcb-placement/v1"

// PlacementOptions controls the deterministic native placer. Component IDs
// select the subset that may move (default: every non-fixed component);
// fixed_component_ids and Component.Fixed pin components in place.
type PlacementOptions struct {
	ComponentIDs         []string `json:"component_ids,omitempty"`
	FixedComponentIDs    []string `json:"fixed_component_ids,omitempty"`
	GridNM               int64    `json:"grid_nm,omitempty"`
	AllowRotation        bool     `json:"allow_rotation,omitempty"`
	AllowBackSide        bool     `json:"allow_back_side,omitempty"`
	EdgeMarginNM         int64    `json:"edge_margin_nm,omitempty"`
	SpacingNM            int64    `json:"spacing_nm,omitempty"`
	PanelEdgeToleranceNM int64    `json:"panel_edge_tolerance_nm,omitempty"`
	ImprovementPasses    int      `json:"improvement_passes,omitempty"`
	ClearRouting         bool     `json:"clear_routing,omitempty"`
}

type PlacementNetLength struct {
	NetID    string `json:"net_id"`
	Pads     int    `json:"pads"`
	LengthNM int64  `json:"length_nm"`
}

// PlacementAnalysis is a read-only report over real component geometry
// (courtyards, bodies, pads, mechanical envelopes). Errors mirror conditions
// the validator or mechanical handoff would reject.
type PlacementAnalysis struct {
	Schema           string               `json:"schema"`
	Engine           string               `json:"engine"`
	Status           string               `json:"status"`
	Errors           int                  `json:"errors"`
	Warnings         int                  `json:"warnings"`
	Checks           []Check              `json:"checks"`
	RatsnestLengthNM int64                `json:"ratsnest_length_nm"`
	NetRatsnest      []PlacementNetLength `json:"net_ratsnest"`
}

type PlacementFailure struct {
	ComponentID string `json:"component_id"`
	Message     string `json:"message"`
}

type PlacementMetrics struct {
	Components       int `json:"components"`
	Movable          int `json:"movable"`
	Moved            int `json:"moved"`
	EdgePinned       int `json:"edge_pinned"`
	Evaluations      int `json:"evaluations"`
	ImprovementMoves int `json:"improvement_moves"`
	Passes           int `json:"passes"`
}

type PlacementPlan struct {
	Schema           string             `json:"schema"`
	Engine           string             `json:"engine"`
	Status           string             `json:"status"`
	GridNM           int64              `json:"grid_nm"`
	MovedComponents  []string           `json:"moved_components"`
	EdgePinned       []string           `json:"edge_pinned_components"`
	FixedComponents  []string           `json:"fixed_components"`
	Failures         []PlacementFailure `json:"failures,omitempty"`
	StaleRoutingNets []string           `json:"stale_routing_nets,omitempty"`
	Operations       []Operation        `json:"operations"`
	Before           PlacementAnalysis  `json:"before"`
	After            PlacementAnalysis  `json:"after"`
	Metrics          PlacementMetrics   `json:"metrics"`
}

// ---------------------------------------------------------------------------
// Geometry helpers

type placeRect struct{ MinX, MinY, MaxX, MaxY int64 }

func (r placeRect) inflate(d int64) placeRect {
	return placeRect{r.MinX - d, r.MinY - d, r.MaxX + d, r.MaxY + d}
}
func (r placeRect) polygon() []Point {
	return []Point{{XNM: r.MinX, YNM: r.MinY}, {XNM: r.MaxX, YNM: r.MinY}, {XNM: r.MaxX, YNM: r.MaxY}, {XNM: r.MinX, YNM: r.MaxY}}
}
func (r placeRect) center() Point {
	return Point{XNM: (r.MinX + r.MaxX) / 2, YNM: (r.MinY + r.MaxY) / 2}
}

// rectsCloser reports whether two rectangles are closer than gap (overlap
// counts as distance zero).
func rectsCloser(a, b placeRect, gap int64) bool {
	return a.MinX < b.MaxX+gap && b.MinX < a.MaxX+gap && a.MinY < b.MaxY+gap && b.MinY < a.MaxY+gap
}

func pointsRect(points []Point) placeRect {
	r := placeRect{math.MaxInt64, math.MaxInt64, math.MinInt64, math.MinInt64}
	for _, p := range points {
		r.MinX, r.MinY = min64(r.MinX, p.XNM), min64(r.MinY, p.YNM)
		r.MaxX, r.MaxY = max64(r.MaxX, p.XNM), max64(r.MaxY, p.YNM)
	}
	return r
}

func placementTransform(pose Position, local Point) Point {
	angle := float64(pose.RotationUdeg) / 1e6 * math.Pi / 180
	fx, fy := float64(local.XNM), float64(local.YNM)
	return Point{XNM: pose.XNM + int64(math.Round(fx*math.Cos(angle)-fy*math.Sin(angle))), YNM: pose.YNM + int64(math.Round(fx*math.Sin(angle)+fy*math.Cos(angle)))}
}

func placementInverse(pose Position, abs Point) Point {
	angle := -float64(pose.RotationUdeg) / 1e6 * math.Pi / 180
	fx, fy := float64(abs.XNM-pose.XNM), float64(abs.YNM-pose.YNM)
	return Point{XNM: int64(math.Round(fx*math.Cos(angle) - fy*math.Sin(angle))), YNM: int64(math.Round(fx*math.Sin(angle) + fy*math.Cos(angle)))}
}

// moveRigid maps an absolute point attached to a component from its old pose
// to its new pose.
func moveRigid(from, to Position, abs Point) Point {
	return placementTransform(to, placementInverse(from, abs))
}

func transformedRect(local placeRect, pose Position) placeRect {
	points := local.polygon()
	for i := range points {
		points[i] = placementTransform(pose, points[i])
	}
	return pointsRect(points)
}

// rectInsidePolygon is true when the rectangle lies entirely inside a simple
// polygon: every corner inside, no edge crossings, no polygon vertex within.
func rectInsidePolygon(r placeRect, polygon []Point) bool {
	corners := r.polygon()
	for _, c := range corners {
		if !pointInPolygonOrBoundary(c, polygon) {
			return false
		}
	}
	for _, v := range polygon {
		if v.XNM > r.MinX && v.XNM < r.MaxX && v.YNM > r.MinY && v.YNM < r.MaxY {
			return false
		}
	}
	for i := range corners {
		for j := range polygon {
			if segmentsIntersect(corners[i], corners[(i+1)%4], polygon[j], polygon[(j+1)%len(polygon)]) {
				return false
			}
		}
	}
	return true
}

func rectHitsPolygon(r placeRect, polygon []Point) bool {
	if len(polygon) < 3 {
		return false
	}
	if polygonsIntersect(r.polygon(), polygon) {
		return true
	}
	for _, v := range polygon {
		if v.XNM >= r.MinX && v.XNM <= r.MaxX && v.YNM >= r.MinY && v.YNM <= r.MaxY {
			return true
		}
	}
	return false
}

func normalizeRotation(udeg int64) int64 {
	udeg %= 360_000_000
	if udeg < 0 {
		udeg += 360_000_000
	}
	return udeg
}

func snapToGrid(v, grid int64) int64 {
	return int64(math.Round(float64(v)/float64(grid))) * grid
}

// ---------------------------------------------------------------------------
// Analysis

func placementPadNets(def *Definition) map[string]string {
	out := map[string]string{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			out[node.ComponentID+":"+node.PinID] = net.ID
		}
	}
	return out
}

// placementRatsnest returns the per-net Euclidean minimum-spanning-tree length
// over pad centres, in definition net order.
func placementRatsnest(def *Definition) (int64, []PlacementNetLength) {
	centres := map[string][]Point{}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			key := component.ID + ":" + pad.PinID
			centres[key] = append(centres[key], placePad(component, pad).Center)
		}
	}
	total := int64(0)
	out := []PlacementNetLength{}
	for _, net := range def.Nets {
		points := []Point{}
		for _, node := range net.Nodes {
			points = append(points, centres[node.ComponentID+":"+node.PinID]...)
		}
		length := int64(math.Round(mstLength(points)))
		total += length
		out = append(out, PlacementNetLength{NetID: net.ID, Pads: len(points), LengthNM: length})
	}
	return total, out
}

func mstLength(points []Point) float64 {
	total, _ := mstLengthSkew(points, 0)
	return total
}

// mstLengthSkew returns the Euclidean MST length and the sum over MST edges of
// the offset along axis (1 = |dy|, 2 = |dx|, otherwise 0): how far each
// connection is from a straight run perpendicular to that axis.
func mstLengthSkew(points []Point, axis int) (float64, float64) {
	n := len(points)
	if n < 2 {
		return 0, 0
	}
	parent := make([]int, n)
	inTree := make([]bool, n)
	best := make([]float64, n)
	for i := range best {
		best[i] = math.MaxFloat64
	}
	best[0] = 0
	parent[0] = -1
	total, skew := 0.0, 0.0
	for step := 0; step < n; step++ {
		next := -1
		for i := 0; i < n; i++ {
			if !inTree[i] && (next < 0 || best[i] < best[next]) {
				next = i
			}
		}
		inTree[next] = true
		total += best[next]
		if parent[next] >= 0 {
			skew += axisOffset(points[parent[next]], points[next], axis)
		}
		for i := 0; i < n; i++ {
			if !inTree[i] {
				if d := pointDistance(points[next], points[i]); d < best[i] {
					best[i], parent[i] = d, next
				}
			}
		}
	}
	return total, skew
}

type analysisShape struct {
	component Component
	courtyard []Point
	body      []Point
	pads      []*placedPad
	extent    placeRect
	sides     int
}

func componentSides(component Component) int {
	for _, pad := range component.Pads {
		if pad.DrillNM > 0 || pad.DrillWidthNM > 0 {
			return 3
		}
	}
	if component.Position.Side == "back" {
		return 2
	}
	return 1
}

func analysisShapes(def *Definition) []analysisShape {
	envelopes := map[string][]ComponentEnvelope{}
	if def.Mechanical != nil {
		for _, envelope := range def.Mechanical.ComponentEnvelopes {
			if envelope.ComponentID != "" {
				envelopes[envelope.ComponentID] = append(envelopes[envelope.ComponentID], envelope)
			}
		}
	}
	out := make([]analysisShape, 0, len(def.Components))
	for _, component := range def.Components {
		shape := analysisShape{component: component, sides: componentSides(component)}
		body := inferredBody(component)
		if component.Body != nil && component.Body.WidthNM > 0 && component.Body.HeightNM > 0 {
			body = *component.Body
		}
		shape.body = bodyPolygon(component, body)
		points := append([]Point(nil), shape.body...)
		if component.FootprintSpec != nil && len(component.FootprintSpec.Courtyard) >= 3 {
			for _, p := range component.FootprintSpec.Courtyard {
				shape.courtyard = append(shape.courtyard, placementTransform(component.Position, p))
			}
			points = append(points, shape.courtyard...)
		}
		for _, pad := range component.Pads {
			placed := placePad(component, pad)
			shape.pads = append(shape.pads, placed)
			points = append(points, placed.Polygon...)
		}
		for _, envelope := range envelopes[component.ID] {
			for _, c := range orientedRectangleCorners(envelope) {
				points = append(points, Point{XNM: int64(math.Round(c.x)), YNM: int64(math.Round(c.y))})
			}
		}
		shape.extent = pointsRect(points)
		out = append(out, shape)
	}
	return out
}

func placementFaceGap(face string, extent, board placeRect) (int64, bool) {
	switch face {
	case "west":
		return extent.MinX - board.MinX, true
	case "east":
		return board.MaxX - extent.MaxX, true
	case "south":
		return extent.MinY - board.MinY, true
	case "north":
		return board.MaxY - extent.MaxY, true
	}
	return 0, false
}

func analyzePlacement(def *Definition, options PlacementOptions) PlacementAnalysis {
	out := PlacementAnalysis{Schema: placementSchema, Engine: engineVersion, Status: "passed", Checks: []Check{}}
	add := func(code, severity, message string, ids ...string) {
		out.Checks = append(out.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
		if severity == "error" {
			out.Errors++
		} else if severity == "warning" {
			out.Warnings++
		}
	}
	if def == nil {
		add("PLACEMENT_DEFINITION_MISSING", "error", "Definition is required")
		out.Status = "failed"
		return out
	}
	tolerance := options.PanelEdgeToleranceNM
	if tolerance <= 0 {
		tolerance = 3_000_000
	}
	board := boardPolygon(def.Board)
	bbox := pointsRect(board)
	shapes := analysisShapes(def)
	padNets := placementPadNets(def)
	outline := func(s analysisShape) []Point {
		if len(s.courtyard) >= 3 {
			return s.courtyard
		}
		return s.body
	}

	for _, s := range shapes {
		id := s.component.ID
		offBoard := !pointInPolygonOrBoundary(Point{XNM: s.component.Position.XNM, YNM: s.component.Position.YNM}, board)
		for _, p := range s.body {
			if !pointInPolygonOrBoundary(p, board) {
				offBoard = true
			}
		}
		for _, pad := range s.pads {
			for _, p := range pad.Polygon {
				if !pointInPolygonOrBoundary(p, board) {
					offBoard = true
				}
			}
		}
		if offBoard {
			add("PLACEMENT_OFF_BOARD", "error", "Component body, pads, or origin lie outside the board outline", id)
		} else {
			for _, pad := range s.pads {
				if polygonEdgeDistance(pad.Polygon, board) < float64(def.Rules.MinEdgeClearanceNM) {
					add("PLACEMENT_EDGE_CLEARANCE", "error", "Component pad is closer to the board edge than the edge-clearance rule", id, pad.Pad.ID)
					break
				}
			}
		}
		for _, cutout := range def.Board.Cutouts {
			contour := boardCutoutContour(cutout)
			if polygonsIntersect(outline(s), contour) || polygonsIntersect(s.body, contour) {
				add("PLACEMENT_CUTOUT_CONFLICT", "error", "Component overlaps a board cutout", id, cutout.ID)
			}
		}
		if def.Mechanical != nil {
			for _, hole := range def.Mechanical.MountingHoles {
				radius := max64(hole.DiameterNM, hole.HeadClearanceNM) / 2
				circle := circlePolygon(Point{XNM: hole.XNM, YNM: hole.YNM}, radius, 24)
				if polygonsIntersect(outline(s), circle) || polygonsIntersect(s.body, circle) {
					add("PLACEMENT_HOLE_CONFLICT", "error", "Component overlaps a mounting hole or its fastener head clearance", id, hole.ID)
				}
			}
		}
		for _, keepout := range def.Keepouts {
			kind := strings.ToLower(strings.TrimSpace(keepout.Kind))
			if len(keepout.Polygon) < 3 {
				continue
			}
			if (kind == "antenna" || kind == "component" || kind == "all") && keepout.OwnerID != id && polygonsIntersect(s.body, keepout.Polygon) {
				add("PLACEMENT_KEEPOUT", "error", "Component body enters a keepout", keepout.ID, id)
			}
			if kind == "antenna" || kind == "copper" || kind == "all" {
				for _, pad := range s.pads {
					if keepout.Layer != "" && !containsString(pad.Pad.Layers, keepout.Layer) {
						continue
					}
					if pointInPolygon(pad.Center, keepout.Polygon) {
						add("PLACEMENT_KEEPOUT_PAD", "error", "Component pad enters a copper keepout", keepout.ID, id+":"+pad.Pad.ID)
					}
				}
			}
		}
		for _, zone := range def.Zones {
			for _, pad := range s.pads {
				net := padNets[id+":"+pad.Pad.PinID]
				if net != "" && net != zone.NetID && containsString(pad.Pad.Layers, zone.Layer) && pointInPolygon(pad.Center, zone.Polygon) {
					add("PLACEMENT_ZONE_PAD", "error", "Pad lies inside a different-net copper zone", zone.ID, id+":"+pad.Pad.ID)
				}
			}
		}
	}

	for i := 0; i < len(shapes); i++ {
		for j := i + 1; j < len(shapes); j++ {
			a, b := shapes[i], shapes[j]
			if a.sides&b.sides == 0 {
				continue
			}
			if polygonsIntersect(outline(a), outline(b)) || polygonsIntersect(a.body, b.body) {
				add("PLACEMENT_OVERLAP", "error", "Component courtyards or bodies overlap", a.component.ID, b.component.ID)
			}
		}
	}

	if def.Mechanical != nil {
		envelopes := def.Mechanical.ComponentEnvelopes
		for i := 0; i < len(envelopes); i++ {
			for j := i + 1; j < len(envelopes); j++ {
				a, b := envelopes[i], envelopes[j]
				if a.ZMinNM < b.ZMaxNM && b.ZMinNM < a.ZMaxNM && orientedRectanglesOverlap(a, b) {
					add("PLACEMENT_ENVELOPE_COLLISION", "error", "Component mechanical envelopes collide", a.ID, b.ID)
				}
			}
		}
		byID := map[string]analysisShape{}
		for _, s := range shapes {
			byID[s.component.ID] = s
		}
		for _, opening := range def.Mechanical.PanelOpenings {
			s, ok := byID[opening.ComponentID]
			if opening.ComponentID == "" || !ok {
				continue
			}
			gap, edgeFace := placementFaceGap(opening.Face, s.extent, bbox)
			if !edgeFace {
				continue
			}
			if gap > tolerance {
				add("PLACEMENT_PANEL_EDGE", "error", fmt.Sprintf("Component with a %s panel opening sits %.3f mm from that board edge (tolerance %.3f mm)", opening.Face, float64(gap)/1e6, float64(tolerance)/1e6), opening.ComponentID, opening.ID)
			}
			along, lo, hi := opening.YNM, s.extent.MinY, s.extent.MaxY
			if opening.Face == "north" || opening.Face == "south" {
				along, lo, hi = opening.XNM, s.extent.MinX, s.extent.MaxX
			}
			if along < lo || along > hi {
				add("PLACEMENT_PANEL_OPENING_MISALIGNED", "error", "Panel opening is not aligned with its component", opening.ComponentID, opening.ID)
			}
		}
	}

	out.RatsnestLengthNM, out.NetRatsnest = placementRatsnest(def)
	if out.Errors > 0 {
		out.Status = "failed"
	} else if out.Warnings > 0 {
		out.Status = "warning"
	}
	return out
}

// ---------------------------------------------------------------------------
// Placer model

type placeLocalPad struct {
	center Point
	rect   placeRect
	layers []string
	netID  string
	net    int
}

type placeLocalKeepout struct {
	index   int
	kind    string
	layer   string
	polygon []Point // local frame
}

type placeItem struct {
	index     int
	id        string
	local     placeRect
	pads      []placeLocalPad
	sides     int
	keepouts  []placeLocalKeepout
	face      string
	movable   bool
	original  Position
	area      int64
	tht       bool
	failed    bool
	relaxedSp bool
}

func axisOffset(a, b Point, axis int) float64 {
	switch axis {
	case 1:
		return math.Abs(float64(a.YNM - b.YNM))
	case 2:
		return math.Abs(float64(a.XNM - b.XNM))
	}
	return 0
}

type placeNet struct {
	id     string
	weight float64
	pads   [][2]int // item index, pad index
	// straightAxis marks differential-pair nets: 1 penalises Y offsets (runs
	// along X), 2 penalises X offsets. The axis is the direction in which the
	// pair's two pads sit side by side on the first part carrying both.
	straightAxis int
}

// placementStraightWeight scales the misalignment penalty applied to
// differential-pair nets so matched, parallel runs remain routable.
const placementStraightWeight = 10.0

type placer struct {
	def      *Definition
	opts     PlacementOptions
	board    []Point
	bbox     placeRect
	items    []*placeItem
	placed   []bool
	poses    []Position
	holes    []placeRect
	cutouts  []placeRect
	keepouts []Keepout // keepouts not owned by any placeable item
	nets     []placeNet
	spacing  int64
	padEdge  int64
	evals    int
}

func placementDefaults(def *Definition, options PlacementOptions) (PlacementOptions, error) {
	if options.GridNM <= 0 {
		options.GridNM = 500_000
	}
	if options.GridNM < 100_000 || options.GridNM > 5_000_000 {
		return options, fmt.Errorf("grid_nm must be between 100000 and 5000000")
	}
	if options.EdgeMarginNM < 0 || options.SpacingNM < 0 || options.PanelEdgeToleranceNM < 0 {
		return options, fmt.Errorf("edge_margin_nm, spacing_nm, and panel_edge_tolerance_nm cannot be negative")
	}
	if options.EdgeMarginNM == 0 {
		options.EdgeMarginNM = max64(def.Rules.MinEdgeClearanceNM, 1_000_000)
	}
	if options.SpacingNM == 0 {
		options.SpacingNM = 1_000_000
	}
	if options.PanelEdgeToleranceNM == 0 {
		options.PanelEdgeToleranceNM = 3_000_000
	}
	if options.ImprovementPasses == 0 {
		options.ImprovementPasses = 4
	}
	if options.ImprovementPasses < 0 || options.ImprovementPasses > 20 {
		return options, fmt.Errorf("improvement_passes must be between 0 and 20")
	}
	return options, nil
}

func newPlacer(def *Definition, options PlacementOptions) (*placer, error) {
	p := &placer{def: def, opts: options, board: boardPolygon(def.Board), spacing: options.SpacingNM}
	p.bbox = pointsRect(p.board)
	p.padEdge = max64(def.Rules.MinEdgeClearanceNM, 600_000)
	ids := map[string]int{}
	for i, component := range def.Components {
		ids[component.ID] = i
	}
	for _, list := range [][]string{options.ComponentIDs, options.FixedComponentIDs} {
		missing := []string{}
		for _, id := range list {
			if _, ok := ids[id]; !ok {
				missing = append(missing, id)
			}
		}
		if len(missing) > 0 {
			sort.Strings(missing)
			return nil, fmt.Errorf("unknown component ids: %s", strings.Join(missing, ", "))
		}
	}
	selected := map[string]bool{}
	for _, id := range options.ComponentIDs {
		selected[id] = true
	}
	fixed := map[string]bool{}
	for _, id := range options.FixedComponentIDs {
		fixed[id] = true
	}
	faces := map[string]string{}
	envelopes := map[string][]ComponentEnvelope{}
	if def.Mechanical != nil {
		for _, opening := range def.Mechanical.PanelOpenings {
			if opening.ComponentID == "" {
				continue
			}
			switch opening.Face {
			case "north", "south", "east", "west":
				if _, ok := faces[opening.ComponentID]; !ok {
					faces[opening.ComponentID] = opening.Face
				}
			}
		}
		for _, envelope := range def.Mechanical.ComponentEnvelopes {
			if envelope.ComponentID != "" {
				envelopes[envelope.ComponentID] = append(envelopes[envelope.ComponentID], envelope)
			}
		}
		for _, hole := range def.Mechanical.MountingHoles {
			r := max64(hole.DiameterNM, hole.HeadClearanceNM)/2 + defaultCourtyardGap
			p.holes = append(p.holes, placeRect{hole.XNM - r, hole.YNM - r, hole.XNM + r, hole.YNM + r})
		}
	}
	for _, cutout := range def.Board.Cutouts {
		p.cutouts = append(p.cutouts, pointsRect(boardCutoutContour(cutout)).inflate(defaultCourtyardGap))
	}
	padNets := placementPadNets(def)
	netIndex := map[string]int{}
	for i, net := range def.Nets {
		netIndex[net.ID] = i
		p.nets = append(p.nets, placeNet{id: net.ID})
	}
	owned := map[int]bool{}
	for ci, component := range def.Components {
		item := &placeItem{index: ci, id: component.ID, original: component.Position, face: faces[component.ID]}
		item.movable = !component.Fixed && !fixed[component.ID] && (len(selected) == 0 || selected[component.ID])
		points := []Point{}
		body := inferredBody(component)
		if component.Body != nil && component.Body.WidthNM > 0 && component.Body.HeightNM > 0 {
			body = *component.Body
		}
		points = append(points, placeRect{-body.WidthNM / 2, -body.HeightNM / 2, body.WidthNM / 2, body.HeightNM / 2}.polygon()...)
		if component.FootprintSpec != nil {
			points = append(points, component.FootprintSpec.Courtyard...)
		}
		for _, pad := range component.Pads {
			rect := placeRect{pad.XNM - pad.WidthNM/2, pad.YNM - pad.HeightNM/2, pad.XNM + pad.WidthNM/2, pad.YNM + pad.HeightNM/2}
			points = append(points, rect.polygon()...)
			lp := placeLocalPad{center: Point{XNM: pad.XNM, YNM: pad.YNM}, rect: rect, layers: pad.Layers, netID: padNets[component.ID+":"+pad.PinID], net: -1}
			if index, ok := netIndex[lp.netID]; ok && lp.netID != "" {
				lp.net = index
				p.nets[index].pads = append(p.nets[index].pads, [2]int{len(p.items), len(item.pads)})
			}
			if pad.DrillNM > 0 || pad.DrillWidthNM > 0 {
				item.tht = true
			}
			item.pads = append(item.pads, lp)
		}
		for _, envelope := range envelopes[component.ID] {
			for _, c := range orientedRectangleCorners(envelope) {
				points = append(points, placementInverse(component.Position, Point{XNM: int64(math.Round(c.x)), YNM: int64(math.Round(c.y))}))
			}
		}
		item.local = pointsRect(points)
		item.area = (item.local.MaxX - item.local.MinX) / 1000 * ((item.local.MaxY - item.local.MinY) / 1000)
		item.sides = componentSides(component)
		for ki, keepout := range def.Keepouts {
			if keepout.OwnerID == component.ID && len(keepout.Polygon) >= 3 {
				lk := placeLocalKeepout{index: ki, kind: strings.ToLower(strings.TrimSpace(keepout.Kind)), layer: keepout.Layer}
				for _, point := range keepout.Polygon {
					lk.polygon = append(lk.polygon, placementInverse(component.Position, point))
				}
				item.keepouts = append(item.keepouts, lk)
				owned[ki] = true
			}
		}
		p.items = append(p.items, item)
		p.poses = append(p.poses, component.Position)
		p.placed = append(p.placed, false)
	}
	for ki, keepout := range def.Keepouts {
		if !owned[ki] {
			p.keepouts = append(p.keepouts, keepout)
		}
	}
	for _, pair := range def.DifferentialPairs {
		pi, pok := netIndex[pair.PositiveNetID]
		ni, nok := netIndex[pair.NegativeNetID]
		if !pok || !nok {
			continue
		}
		axis := 0
		for _, item := range p.items {
			var pos, neg *placeLocalPad
			for k := range item.pads {
				if item.pads[k].net == pi && pos == nil {
					pos = &item.pads[k]
				} else if item.pads[k].net == ni && neg == nil {
					neg = &item.pads[k]
				}
			}
			if pos == nil || neg == nil {
				continue
			}
			a, b := placementTransform(item.original, pos.center), placementTransform(item.original, neg.center)
			axis = 2
			if math.Abs(float64(a.YNM-b.YNM)) >= math.Abs(float64(a.XNM-b.XNM)) {
				axis = 1
			}
			break
		}
		p.nets[pi].straightAxis, p.nets[ni].straightAxis = axis, axis
	}
	for i := range p.nets {
		n := len(p.nets[i].pads)
		if n > 1 {
			p.nets[i].weight = math.Min(1, 2/float64(n))
		}
	}
	return p, nil
}

func (p *placer) occupancy(i int, pose Position) placeRect {
	return transformedRect(p.items[i].local, pose)
}

func (p *placer) padRect(i, pad int, pose Position) placeRect {
	return transformedRect(p.items[i].pads[pad].rect, pose)
}

func (p *placer) keepoutPolygon(k placeLocalKeepout, pose Position) []Point {
	out := make([]Point, len(k.polygon))
	for i, point := range k.polygon {
		out[i] = placementTransform(pose, point)
	}
	return out
}

func keepoutBlocksBodies(kind string) bool {
	return kind == "antenna" || kind == "component" || kind == "all"
}
func keepoutBlocksCopper(kind string) bool {
	return kind == "antenna" || kind == "copper" || kind == "all"
}

// sidesFor returns the board sides a component occupies at a pose.
func (p *placer) sidesFor(i int, pose Position) int {
	if p.items[i].tht {
		return 3
	}
	if pose.Side == "back" {
		return 2
	}
	return 1
}

// itemHitsKeepout tests component i at pose against one keepout polygon.
func (p *placer) itemHitsKeepout(i int, pose Position, occ placeRect, kind, layer string, polygon []Point) bool {
	if keepoutBlocksBodies(kind) && rectHitsPolygon(occ, polygon) {
		return true
	}
	if keepoutBlocksCopper(kind) {
		for pi, pad := range p.items[i].pads {
			if layer != "" && !containsString(pad.layers, layer) {
				continue
			}
			if rectHitsPolygon(p.padRect(i, pi, pose), polygon) {
				return true
			}
		}
	}
	return false
}

func (p *placer) feasible(i int, pose Position, spacing int64) bool {
	p.evals++
	item := p.items[i]
	occ := p.occupancy(i, pose)
	if item.face != "" {
		if !rectInsidePolygon(occ, p.board) {
			return false
		}
		for pi := range item.pads {
			if !rectInsidePolygon(p.padRect(i, pi, pose).inflate(p.padEdge), p.board) {
				return false
			}
		}
	} else if !rectInsidePolygon(occ.inflate(p.opts.EdgeMarginNM), p.board) {
		return false
	}
	for _, hole := range p.holes {
		if rectsCloser(occ, hole, 0) {
			return false
		}
	}
	for _, cutout := range p.cutouts {
		if rectsCloser(occ, cutout, 0) {
			return false
		}
	}
	sides := p.sidesFor(i, pose)
	for _, keepout := range p.keepouts {
		if p.itemHitsKeepout(i, pose, occ, strings.ToLower(strings.TrimSpace(keepout.Kind)), keepout.Layer, keepout.Polygon) {
			return false
		}
	}
	ownPolygons := make([][]Point, len(item.keepouts))
	for k, keepout := range item.keepouts {
		ownPolygons[k] = p.keepoutPolygon(keepout, pose)
		for _, point := range ownPolygons[k] {
			if !pointInPolygonOrBoundary(point, p.board) {
				return false
			}
		}
	}
	for j := range p.items {
		if j == i || !p.placed[j] {
			continue
		}
		other := p.poses[j]
		otherOcc := p.occupancy(j, other)
		if sides&p.sidesFor(j, other) != 0 && rectsCloser(occ, otherOcc, spacing) {
			return false
		}
		for _, keepout := range p.items[j].keepouts {
			if p.itemHitsKeepout(i, pose, occ, keepout.kind, keepout.layer, p.keepoutPolygon(keepout, other)) {
				return false
			}
		}
		for k, keepout := range item.keepouts {
			if p.itemHitsKeepout(j, other, otherOcc, keepout.kind, keepout.layer, ownPolygons[k]) {
				return false
			}
		}
	}
	for _, zone := range p.def.Zones {
		for pi, pad := range item.pads {
			if pad.netID != "" && pad.netID != zone.NetID && containsString(pad.layers, zone.Layer) && rectHitsPolygon(p.padRect(i, pi, pose), zone.Polygon) {
				return false
			}
		}
	}
	return true
}

func (p *placer) padCenter(i, pad int, pose Position) Point {
	return placementTransform(pose, p.items[i].pads[pad].center)
}

// constructiveCost is the weighted distance from each of the component's pads
// to the nearest already-placed pad on the same net, plus a weak pull toward
// the board centre so unconnected parts do not drift into corners.
func (p *placer) constructiveCost(i int, pose Position) float64 {
	cost := 0.0
	for pi, pad := range p.items[i].pads {
		if pad.net < 0 {
			continue
		}
		net := p.nets[pad.net]
		centre := p.padCenter(i, pi, pose)
		best := math.MaxFloat64
		for _, ref := range net.pads {
			if ref[0] == i || !p.placed[ref[0]] {
				continue
			}
			if d := pointDistance(centre, p.padCenter(ref[0], ref[1], p.poses[ref[0]])); d < best {
				best = d
			}
		}
		if best < math.MaxFloat64 {
			cost += net.weight * best
		}
		if net.straightAxis != 0 {
			bestSkew := math.MaxFloat64
			for _, ref := range net.pads {
				if ref[0] == i || !p.placed[ref[0]] {
					continue
				}
				bestSkew = math.Min(bestSkew, axisOffset(centre, p.padCenter(ref[0], ref[1], p.poses[ref[0]]), net.straightAxis))
			}
			if bestSkew < math.MaxFloat64 {
				cost += placementStraightWeight * bestSkew
			}
		}
	}
	occ := p.occupancy(i, pose)
	cost += 0.05 * pointDistance(occ.center(), p.bbox.center())
	if face := p.items[i].face; face != "" {
		// Edge-pinned parts prefer to sit as close to their face as is legal.
		gap, _ := placementFaceGap(face, occ, p.bbox)
		cost += float64(gap)
	}
	return cost
}

// totalCost is the weighted ratsnest over every net with at least two pads.
func (p *placer) totalCost() float64 {
	total := 0.0
	for _, net := range p.nets {
		if len(net.pads) < 2 {
			continue
		}
		points := make([]Point, len(net.pads))
		for k, ref := range net.pads {
			points[k] = p.padCenter(ref[0], ref[1], p.poses[ref[0]])
		}
		length, skew := mstLengthSkew(points, net.straightAxis)
		total += net.weight*length + placementStraightWeight*skew
	}
	return total
}

func (p *placer) rotations(i int) []int64 {
	base := normalizeRotation(p.items[i].original.RotationUdeg)
	if !p.opts.AllowRotation || p.items[i].face != "" {
		return []int64{base}
	}
	return []int64{base, normalizeRotation(base + 90_000_000), normalizeRotation(base + 180_000_000), normalizeRotation(base + 270_000_000)}
}

func (p *placer) sideChoices(i int) []string {
	side := p.items[i].original.Side
	if side != "back" {
		side = "front"
	}
	// Placement only changes Position; it never mirrors pads onto the other
	// copper layer. Only through-hole parts can therefore change side safely.
	if p.opts.AllowBackSide && p.items[i].tht && p.items[i].face == "" {
		if side == "front" {
			return []string{"front", "back"}
		}
		return []string{"back", "front"}
	}
	return []string{side}
}

// candidates enumerates deterministic candidate poses for component i.
func (p *placer) candidates(i int) []Position {
	item := p.items[i]
	grid := p.opts.GridNM
	out := []Position{}
	if item.face != "" {
		rotation := item.original.RotationUdeg
		local := transformedRect(item.local, Position{RotationUdeg: rotation})
		tolerance := p.opts.PanelEdgeToleranceNM
		gaps := []int64{}
		originalGap, _ := placementFaceGap(item.face, transformedRect(item.local, item.original), p.bbox)
		if originalGap >= 0 && originalGap <= tolerance {
			gaps = append(gaps, originalGap)
		}
		for gap := int64(0); gap <= tolerance; gap += grid {
			if gap != originalGap {
				gaps = append(gaps, gap)
			}
		}
		vertical := item.face == "west" || item.face == "east"
		lo, hi := p.bbox.MinX, p.bbox.MaxX
		if vertical {
			lo, hi = p.bbox.MinY, p.bbox.MaxY
		}
		for along := snapToGrid(lo, grid); along <= hi; along += grid {
			for _, gap := range gaps {
				pose := Position{RotationUdeg: rotation, Side: item.original.Side}
				switch item.face {
				case "west":
					pose.XNM, pose.YNM = p.bbox.MinX+gap-local.MinX, along
				case "east":
					pose.XNM, pose.YNM = p.bbox.MaxX-gap-local.MaxX, along
				case "south":
					pose.XNM, pose.YNM = along, p.bbox.MinY+gap-local.MinY
				case "north":
					pose.XNM, pose.YNM = along, p.bbox.MaxY-gap-local.MaxY
				}
				out = append(out, pose)
			}
		}
		return out
	}
	step := grid
	for step < 1_000_000 {
		step += grid
	}
	for _, side := range p.sideChoices(i) {
		for _, rotation := range p.rotations(i) {
			for y := snapToGrid(p.bbox.MinY, step); y <= p.bbox.MaxY; y += step {
				for x := snapToGrid(p.bbox.MinX, step); x <= p.bbox.MaxX; x += step {
					out = append(out, Position{XNM: x, YNM: y, RotationUdeg: rotation, Side: side})
				}
			}
		}
	}
	return out
}

func (p *placer) placeOne(i int) bool {
	cands := p.candidates(i)
	for _, spacing := range []int64{p.spacing, p.spacing / 2, 0} {
		best, bestCost := -1, math.MaxFloat64
		for c, pose := range cands {
			if !p.feasible(i, pose, spacing) {
				continue
			}
			if cost := p.constructiveCost(i, pose); cost < bestCost-1e-6 {
				best, bestCost = c, cost
			}
		}
		if best >= 0 {
			p.poses[i] = cands[best]
			p.placed[i] = true
			p.items[i].relaxedSp = spacing != p.spacing
			return true
		}
	}
	return false
}

func (p *placer) connectivityToPlaced(i int) float64 {
	score := 0.0
	for _, pad := range p.items[i].pads {
		if pad.net < 0 {
			continue
		}
		for _, ref := range p.nets[pad.net].pads {
			if ref[0] != i && p.placed[ref[0]] {
				weight := p.nets[pad.net].weight
				if p.nets[pad.net].straightAxis != 0 {
					// Differential-pair partners are placed right after the
					// part they hang off so the pair can run straight.
					weight *= 4
				}
				score += weight
				break
			}
		}
	}
	return score
}

func (p *placer) totalConnectivity(i int) float64 {
	score := 0.0
	for _, pad := range p.items[i].pads {
		if pad.net >= 0 {
			score += p.nets[pad.net].weight
		}
	}
	return score
}

// placeStraightGroup places a differential-pair chain so the pair pads of every
// member share one coordinate on the pair's alignment axis. It scans that
// coordinate over the board, greedily placing members (edge-pinned first) at
// their cheapest feasible position on the shared line, and keeps the cheapest
// complete arrangement. Members keep their original rotation and side. It
// returns false, leaving the members unplaced, when no line fits them all.
func (p *placer) placeStraightGroup(group []int) bool {
	ref, axis := -1, 0
	for _, pad := range p.items[group[0]].pads {
		if pad.net >= 0 && p.nets[pad.net].straightAxis != 0 {
			ref, axis = pad.net, p.nets[pad.net].straightAxis
			break
		}
	}
	if ref < 0 {
		return false
	}
	members := append([]int(nil), group...)
	sort.SliceStable(members, func(a, b int) bool {
		ia, ib := p.items[members[a]], p.items[members[b]]
		if (ia.face != "") != (ib.face != "") {
			return ia.face != ""
		}
		if ca, cb := p.totalConnectivity(members[a]), p.totalConnectivity(members[b]); ca != cb {
			return ca > cb
		}
		return ia.id < ib.id
	})
	offsets := make([]int64, len(members))
	for k, i := range members {
		item := p.items[i]
		chosen := -1
		for pi, pad := range item.pads {
			if pad.net == ref {
				chosen = pi
				break
			}
		}
		if chosen < 0 {
			for pi, pad := range item.pads {
				if pad.net >= 0 && p.nets[pad.net].straightAxis != 0 {
					chosen = pi
					break
				}
			}
		}
		if chosen < 0 {
			return false
		}
		c := placementTransform(Position{RotationUdeg: item.original.RotationUdeg}, item.pads[chosen].center)
		offsets[k] = c.XNM
		if axis == 1 {
			offsets[k] = c.YNM
		}
	}
	pinnedCands := make([][]Position, len(members))
	for k, i := range members {
		if p.items[i].face != "" {
			pinnedCands[k] = p.candidates(i)
		}
	}
	grid := p.opts.GridNM
	lo, hi, crossLo, crossHi := p.bbox.MinX, p.bbox.MaxX, p.bbox.MinY, p.bbox.MaxY
	if axis == 1 {
		lo, hi, crossLo, crossHi = p.bbox.MinY, p.bbox.MaxY, p.bbox.MinX, p.bbox.MaxX
	}
	var bestPoses []Position
	bestCost := math.MaxFloat64
	for base := snapToGrid(lo, grid); base <= hi; base += grid {
		target := base + offsets[0]
		poses := make([]Position, len(members))
		total, ok := 0.0, true
		done := make([]bool, len(members))
		for step := range members {
			// Walk the chain outward from the anchor: next is the member most
			// strongly connected to those already placed.
			k := -1
			for c := range members {
				if done[c] {
					continue
				}
				if step == 0 {
					k = c
					break
				}
				if k < 0 || p.connectivityToPlaced(members[c]) > p.connectivityToPlaced(members[k]) {
					k = c
				}
			}
			done[k] = true
			i := members[k]
			item := p.items[i]
			origin := target - offsets[k]
			cands := []Position{}
			if item.face != "" {
				for _, pose := range pinnedCands[k] {
					if (axis == 1 && pose.YNM == origin) || (axis == 2 && pose.XNM == origin) {
						cands = append(cands, pose)
					}
				}
			} else {
				for cross := snapToGrid(crossLo, grid); cross <= crossHi; cross += grid {
					pose := Position{XNM: cross, YNM: origin, RotationUdeg: item.original.RotationUdeg, Side: item.original.Side}
					if axis == 2 {
						pose.XNM, pose.YNM = origin, cross
					}
					cands = append(cands, pose)
				}
			}
			best, bestMember := -1, math.MaxFloat64
			for c, pose := range cands {
				if !p.feasible(i, pose, p.spacing) {
					continue
				}
				if cost := p.constructiveCost(i, pose); cost < bestMember-1e-6 {
					best, bestMember = c, cost
				}
			}
			if best < 0 {
				ok = false
				break
			}
			poses[k] = cands[best]
			p.poses[i], p.placed[i] = cands[best], true
			total += bestMember
		}
		for _, i := range members {
			p.placed[i] = false
		}
		if ok && total < bestCost-1e-6 {
			bestCost, bestPoses = total, poses
		}
	}
	if bestPoses == nil {
		for _, i := range members {
			p.poses[i] = p.items[i].original
		}
		return false
	}
	for k, i := range members {
		p.poses[i], p.placed[i] = bestPoses[k], true
	}
	return true
}

func (p *placer) construct(failures *[]PlacementFailure) {
	for i, item := range p.items {
		if !item.movable {
			p.placed[i] = true
		}
	}
	// Edge-pinned parts first: they have the fewest legal positions.
	pinned := []int{}
	for i, item := range p.items {
		if item.movable && item.face != "" {
			pinned = append(pinned, i)
		}
	}
	sort.SliceStable(pinned, func(a, b int) bool {
		if p.items[pinned[a]].area != p.items[pinned[b]].area {
			return p.items[pinned[a]].area > p.items[pinned[b]].area
		}
		return p.items[pinned[a]].id < p.items[pinned[b]].id
	})
	fail := func(i int) {
		p.items[i].failed = true
		p.poses[i] = p.items[i].original
		p.placed[i] = true
		*failures = append(*failures, PlacementFailure{ComponentID: p.items[i].id, Message: "no feasible position found; component left at its current position"})
	}
	// Differential-pair chains next, jointly, so every member shares one
	// pair coordinate and the pair can run straight between them.
	movable := []int{}
	for i, item := range p.items {
		if item.movable {
			movable = append(movable, i)
		}
	}
	for _, group := range p.straightGroups(movable) {
		p.placeStraightGroup(group)
	}
	for _, i := range pinned {
		if p.placed[i] {
			continue
		}
		if !p.placeOne(i) {
			fail(i)
		}
	}
	for {
		next := -1
		var nextConn, nextTotal float64
		for i, item := range p.items {
			if p.placed[i] || !item.movable {
				continue
			}
			conn, total := p.connectivityToPlaced(i), p.totalConnectivity(i)
			better := next < 0
			if !better {
				switch {
				case conn != nextConn:
					better = conn > nextConn
				case item.area != p.items[next].area:
					better = item.area > p.items[next].area
				case total != nextTotal:
					better = total > nextTotal
				default:
					better = item.id < p.items[next].id
				}
			}
			if better {
				next, nextConn, nextTotal = i, conn, total
			}
		}
		if next < 0 {
			return
		}
		if !p.placeOne(next) {
			fail(next)
		}
	}
}

// straightGroups returns, in deterministic order, the sets (size > 1) of
// movable components joined by differential-pair nets.
func (p *placer) straightGroups(movable []int) [][]int {
	isMovable := make([]bool, len(p.items))
	for _, i := range movable {
		isMovable[i] = true
	}
	u := newUnionFind(len(p.items))
	for _, net := range p.nets {
		if net.straightAxis == 0 {
			continue
		}
		first := -1
		for _, ref := range net.pads {
			if !isMovable[ref[0]] {
				continue
			}
			if first < 0 {
				first = ref[0]
			} else {
				u.join(first, ref[0])
			}
		}
	}
	byRoot := map[int][]int{}
	roots := []int{}
	for _, i := range movable {
		root := u.find(i)
		if _, ok := byRoot[root]; !ok {
			roots = append(roots, root)
		}
		byRoot[root] = append(byRoot[root], i)
	}
	groups := [][]int{}
	for _, root := range roots {
		if len(byRoot[root]) > 1 {
			groups = append(groups, byRoot[root])
		}
	}
	return groups
}

func (p *placer) improve() (moves, passes int) {
	grid := p.opts.GridNM
	movable := []int{}
	for i, item := range p.items {
		if item.movable && !item.failed {
			movable = append(movable, i)
		}
	}
	current := p.totalCost()
	for pass := 0; pass < p.opts.ImprovementPasses; pass++ {
		passes++
		improved := false
		for _, i := range movable {
			item := p.items[i]
			spacing := p.spacing
			if item.relaxedSp {
				spacing = 0
			}
			start := p.poses[i]
			bestPose, bestCost := start, current
			trial := func(pose Position) {
				if pose == start || !p.feasible(i, pose, spacing) {
					return
				}
				p.poses[i] = pose
				cost := p.totalCost()
				p.poses[i] = start
				if cost < bestCost-1 {
					bestPose, bestCost = pose, cost
				}
			}
			directions := [][2]int64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}}
			if item.face == "west" || item.face == "east" {
				directions = [][2]int64{{0, 1}, {0, -1}}
			} else if item.face == "north" || item.face == "south" {
				directions = [][2]int64{{1, 0}, {-1, 0}}
			}
			for _, scale := range []int64{16, 8, 4, 2, 1} {
				for _, d := range directions {
					pose := start
					pose.XNM += d[0] * scale * grid
					pose.YNM += d[1] * scale * grid
					trial(pose)
				}
			}
			for _, rotation := range p.rotations(i) {
				for _, side := range p.sideChoices(i) {
					pose := start
					pose.RotationUdeg, pose.Side = rotation, side
					trial(pose)
				}
			}
			if bestPose != start {
				p.poses[i] = bestPose
				current = bestCost
				moves++
				improved = true
			}
		}
		// Rigid group shifts for parts chained by differential pairs: moving
		// one member alone would break the pair's alignment, so the whole
		// chain is translated together.
		subsets := [][]int{}
		for _, group := range p.straightGroups(movable) {
			subsets = append(subsets, group)
			if len(group) >= 3 {
				// Leave-one-out subsets realign a chain whose members
				// disagree with a single outlier.
				for skip := range group {
					subset := []int{}
					for k, i := range group {
						if k != skip {
							subset = append(subset, i)
						}
					}
					subsets = append(subsets, subset)
				}
			}
		}
		for _, group := range subsets {
			for _, scale := range []int64{8, 4, 2, 1} {
				for _, d := range [][2]int64{{1, 0}, {-1, 0}, {0, 1}, {0, -1}} {
					dx, dy := d[0]*scale*grid, d[1]*scale*grid
					allowed := true
					for _, i := range group {
						face := p.items[i].face
						if (dx != 0 && (face == "west" || face == "east")) || (dy != 0 && (face == "north" || face == "south")) {
							allowed = false
						}
					}
					if !allowed {
						continue
					}
					saved := make([]Position, len(group))
					for k, i := range group {
						saved[k] = p.poses[i]
						p.poses[i].XNM += dx
						p.poses[i].YNM += dy
					}
					ok := true
					for _, i := range group {
						spacing := p.spacing
						if p.items[i].relaxedSp {
							spacing = 0
						}
						if !p.feasible(i, p.poses[i], spacing) {
							ok = false
							break
						}
					}
					if ok {
						if cost := p.totalCost(); cost < current-1 {
							current = cost
							moves++
							improved = true
							continue
						}
					}
					for k, i := range group {
						p.poses[i] = saved[k]
					}
				}
			}
		}
		// Pairwise swaps between free (non-edge) components.
		for a := 0; a < len(movable); a++ {
			for b := a + 1; b < len(movable); b++ {
				i, j := movable[a], movable[b]
				if p.items[i].face != "" || p.items[j].face != "" {
					continue
				}
				pi, pj := p.poses[i], p.poses[j]
				ni, nj := pi, pj
				ni.XNM, ni.YNM, nj.XNM, nj.YNM = pj.XNM, pj.YNM, pi.XNM, pi.YNM
				p.placed[j] = false
				okI := p.feasible(i, ni, p.spacing)
				p.placed[j] = true
				if !okI {
					continue
				}
				p.poses[i] = ni
				okJ := p.feasible(j, nj, p.spacing)
				if okJ {
					p.poses[j] = nj
					if cost := p.totalCost(); cost < current-1 {
						current = cost
						moves++
						improved = true
						continue
					}
				}
				p.poses[i], p.poses[j] = pi, pj
			}
		}
		if !improved {
			break
		}
	}
	return moves, passes
}

// ---------------------------------------------------------------------------
// Plan construction

func suggestPlacement(def *Definition, options PlacementOptions) (*PlacementPlan, error) {
	if def == nil {
		return nil, fmt.Errorf("definition required")
	}
	opts, err := placementDefaults(def, options)
	if err != nil {
		return nil, err
	}
	working := cloneDefinition(def)
	p, err := newPlacer(working, opts)
	if err != nil {
		return nil, err
	}
	plan := &PlacementPlan{Schema: placementSchema, Engine: engineVersion, Status: "passed", GridNM: opts.GridNM, MovedComponents: []string{}, EdgePinned: []string{}, FixedComponents: []string{}, Failures: []PlacementFailure{}, StaleRoutingNets: []string{}, Operations: []Operation{}}
	plan.Before = analyzePlacement(def, opts)
	for _, item := range p.items {
		if item.movable {
			plan.Metrics.Movable++
			if item.face != "" {
				plan.EdgePinned = append(plan.EdgePinned, item.id)
			}
		} else {
			plan.FixedComponents = append(plan.FixedComponents, item.id)
		}
	}
	plan.Metrics.Components = len(p.items)
	plan.Metrics.EdgePinned = len(plan.EdgePinned)
	p.construct(&plan.Failures)
	plan.Metrics.ImprovementMoves, plan.Metrics.Passes = p.improve()
	plan.Metrics.Evaluations = p.evals

	moved := map[string]bool{}
	placementOps := []Operation{}
	for i, item := range p.items {
		pose := p.poses[i]
		if pose.Side == "" {
			pose.Side = item.original.Side
		}
		if pose == item.original {
			continue
		}
		moved[item.id] = true
		plan.MovedComponents = append(plan.MovedComponents, item.id)
		position := pose
		placementOps = append(placementOps, Operation{Type: "placement.set", ComponentID: item.id, Position: &position})
	}
	plan.Metrics.Moved = len(plan.MovedComponents)

	// Existing copper attached to moved parts no longer reaches its pads.
	staleNets := map[string]bool{}
	for _, net := range def.Nets {
		for _, node := range net.Nodes {
			if moved[node.ComponentID] {
				staleNets[net.ID] = true
			}
		}
	}
	routingOps := []Operation{}
	for _, net := range def.Nets {
		if !staleNets[net.ID] || !netHasCopperOrVia(def, net.ID) {
			continue
		}
		plan.StaleRoutingNets = append(plan.StaleRoutingNets, net.ID)
		if !opts.ClearRouting {
			continue
		}
		for _, trace := range def.Traces {
			if trace.NetID == net.ID {
				routingOps = append(routingOps, Operation{Type: "trace.remove", TraceID: trace.ID})
			}
		}
		for _, via := range def.Vias {
			if via.NetID == net.ID {
				routingOps = append(routingOps, Operation{Type: "via.remove", ViaID: via.ID})
			}
		}
	}
	plan.Operations = append(plan.Operations, routingOps...)
	plan.Operations = append(plan.Operations, placementOps...)
	from, to := map[string]Position{}, map[string]Position{}
	for i, item := range p.items {
		if moved[item.id] {
			from[item.id], to[item.id] = item.original, p.poses[i]
		}
	}
	plan.Operations = append(plan.Operations, placementAttachedOperations(def, from, to)...)

	after := cloneDefinition(def)
	for _, op := range plan.Operations {
		if err := applyOperation(after, op); err != nil {
			return nil, fmt.Errorf("placement plan does not apply: %w", err)
		}
	}
	plan.After = analyzePlacement(after, opts)
	if len(plan.Failures) > 0 || plan.After.Errors > 0 {
		plan.Status = "partial"
		if plan.Metrics.Moved == 0 {
			plan.Status = "failed"
		}
	}
	return plan, nil
}

func netHasCopperOrVia(def *Definition, netID string) bool {
	if netHasCopper(def, netID) {
		return true
	}
	for _, via := range def.Vias {
		if via.NetID == netID {
			return true
		}
	}
	return false
}

// placementAttachedOperations moves geometry rigidly attached to a moved
// component: owned keepouts (remove + add, preserving the ID) and mechanical
// envelopes, panel openings, and clearance volumes (one mechanical.set).
func placementAttachedOperations(def *Definition, from, to map[string]Position) []Operation {
	ops := []Operation{}
	for _, keepout := range def.Keepouts {
		a, ok := from[keepout.OwnerID]
		if !ok {
			continue
		}
		b := to[keepout.OwnerID]
		moved := keepout
		moved.Polygon = make([]Point, len(keepout.Polygon))
		for i, point := range keepout.Polygon {
			moved.Polygon[i] = moveRigid(a, b, point)
		}
		ops = append(ops, Operation{Type: "keepout.remove", KeepoutID: keepout.ID}, Operation{Type: "keepout.add", Keepout: &moved})
	}
	if def.Mechanical == nil {
		return ops
	}
	mechanical := cloneDefinition(&Definition{Mechanical: def.Mechanical}).Mechanical
	changed := false
	for i := range mechanical.ComponentEnvelopes {
		envelope := &mechanical.ComponentEnvelopes[i]
		if a, ok := from[envelope.ComponentID]; ok {
			b := to[envelope.ComponentID]
			point := moveRigid(a, b, Point{XNM: envelope.XNM, YNM: envelope.YNM})
			envelope.XNM, envelope.YNM = point.XNM, point.YNM
			envelope.RotationUdeg = normalizeRotation(envelope.RotationUdeg + b.RotationUdeg - a.RotationUdeg)
			changed = true
		}
	}
	for i := range mechanical.PanelOpenings {
		opening := &mechanical.PanelOpenings[i]
		a, ok := from[opening.ComponentID]
		if !ok {
			continue
		}
		b := to[opening.ComponentID]
		point := moveRigid(a, b, Point{XNM: opening.XNM, YNM: opening.YNM})
		switch opening.Face {
		case "west", "east":
			opening.YNM = point.YNM // the face plane coordinate stays on the board edge
		case "north", "south":
			opening.XNM = point.XNM
		default:
			opening.XNM, opening.YNM = point.XNM, point.YNM
		}
		changed = true
	}
	for i := range mechanical.ClearanceZones {
		zone := &mechanical.ClearanceZones[i]
		if a, ok := from[zone.OwnerID]; ok {
			b := to[zone.OwnerID]
			point := moveRigid(a, b, Point{XNM: zone.XNM, YNM: zone.YNM})
			zone.XNM, zone.YNM = point.XNM, point.YNM
			zone.RotationUdeg = normalizeRotation(zone.RotationUdeg + b.RotationUdeg - a.RotationUdeg)
			changed = true
		}
	}
	if changed {
		ops = append(ops, Operation{Type: "mechanical.set", Mechanical: mechanical})
	}
	return ops
}

// ---------------------------------------------------------------------------
// Service and MCP surface

func (s *Service) PlacementAnalyze(designID, revisionID int64, options PlacementOptions) (*PlacementAnalysis, error) {
	_, _, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	opts, err := placementDefaults(def, options)
	if err != nil {
		return nil, err
	}
	analysis := analyzePlacement(def, opts)
	return &analysis, nil
}

func (s *Service) PlacementSuggest(designID, revisionID int64, options PlacementOptions) (*PlacementPlan, error) {
	_, _, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, err
	}
	return suggestPlacement(def, options)
}

func (s *Service) PlacementApply(designID, revisionID int64, options PlacementOptions, note, author string) (*Revision, *PlacementPlan, error) {
	design, parent, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	plan, err := suggestPlacement(def, options)
	if err != nil {
		return nil, nil, err
	}
	if plan.Status == "failed" {
		return nil, plan, fmt.Errorf("placement plan failed with %d failures and %d placement errors", len(plan.Failures), plan.After.Errors)
	}
	if len(plan.Operations) == 0 {
		return nil, plan, errors.New("placement plan contains no changes")
	}
	canonical, _, hash, err := applyOperations(def, plan.Operations)
	if err != nil {
		return nil, plan, err
	}
	operations, _ := json.Marshal(plan.Operations)
	if strings.TrimSpace(note) == "" {
		note = "Automatic placement"
	}
	revision, err := s.store.CreateRevision(s.project, design.ID, parent.ID, canonical, operations, hash, strings.TrimSpace(note), author)
	if err == nil && s.ctx != nil {
		s.ctx.Emit("pcb.revision.created", revisionEvent(revision))
		s.ctx.Emit("pcb.placement.completed", map[string]any{"design_id": design.ID, "revision_id": revision.ID, "status": plan.Status, "moved_components": plan.MovedComponents, "ratsnest_before_nm": plan.Before.RatsnestLengthNM, "ratsnest_after_nm": plan.After.RatsnestLengthNM, "placement_errors": plan.After.Errors, "stale_routing_nets": plan.StaleRoutingNets})
	}
	return revision, plan, err
}

func placementOptionsArg(args map[string]any) (PlacementOptions, error) {
	var options PlacementOptions
	err := decodeOptionalArg(args, "options", &options)
	return options, err
}

func (a *App) toolPlacementAnalyze(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options, err := placementOptionsArg(args)
	if err != nil {
		return nil, err
	}
	analysis, err := s.PlacementAnalyze(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
	return map[string]any{"analysis": analysis}, err
}

func (a *App) toolPlacementSuggest(_ context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options, err := placementOptionsArg(args)
	if err != nil {
		return nil, err
	}
	plan, err := s.PlacementSuggest(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options)
	return map[string]any{"plan": plan}, err
}

func (a *App) toolPlacementApply(ctx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	s, err := a.service(app)
	if err != nil {
		return nil, err
	}
	options, err := placementOptionsArg(args)
	if err != nil {
		return nil, err
	}
	revision, plan, err := s.PlacementApply(int64Arg(args, "design_id"), int64Arg(args, "revision_id"), options, stringArg(args, "note"), callerName(ctx))
	return map[string]any{"revision": revision, "plan": plan}, err
}

// placementTools returns the placement MCP tools; tools.go appends them.
func (a *App) placementTools(id map[string]any) []sdk.Tool {
	options := map[string]any{"type": "object", "description": "Deterministic placement options: component_ids, fixed_component_ids, grid_nm, allow_rotation, allow_back_side, edge_margin_nm, spacing_nm, panel_edge_tolerance_nm, improvement_passes, clear_routing."}
	return []sdk.Tool{
		{Name: "pcb_placement_analyze", Description: "Report component overlaps, keepout and edge violations, panel-opening edge alignment, and ratsnest length without changing the design.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": options}, []string{"design_id"}), HandlerCtx: a.toolPlacementAnalyze},
		{Name: "pcb_placement_suggest", Description: "Suggest a deterministic automatic placement as typed placement.set operations with before/after analysis, without changing the design.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": options}, []string{"design_id"}), HandlerCtx: a.toolPlacementSuggest},
		{Name: "pcb_placement_apply", Description: "Run automatic placement and create an immutable revision; owned keepouts and mechanical envelopes, panel openings, and clearance volumes move with their components.", InputSchema: schemaObject(map[string]any{"design_id": id, "revision_id": id, "options": options, "note": map[string]any{"type": "string"}}, []string{"design_id"}), HandlerCtx: a.toolPlacementApply},
	}
}

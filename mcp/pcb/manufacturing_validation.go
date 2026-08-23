package main

import (
	"fmt"
	"math"
	"strings"
)

type placedPad struct {
	ComponentID string
	PinID       string
	Pad         Pad
	Center      Point
	Corners     []Point
	NetID       string
}

func validateManufacturing(def *Definition) []Check {
	checks := []Check{}
	add := func(code, severity, message string, ids ...string) {
		checks = append(checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	layers := map[string]Layer{}
	for _, layer := range def.Board.Layers {
		layers[layer.ID] = layer
	}
	nets := map[string]Net{}
	for _, net := range def.Nets {
		nets[net.ID] = net
	}

	components := map[string]Component{}
	padsByNode := map[string][]*placedPad{}
	allPads := []*placedPad{}
	for _, component := range def.Components {
		components[component.ID] = component
		pinIDs := map[string]bool{}
		for _, pin := range component.Pins {
			pinIDs[pin.ID] = true
		}
		if len(component.Pads) == 0 {
			add("FOOTPRINT_PAD_GEOMETRY_MISSING", "warning", "Component footprint has no native pad geometry; manufacturability and routing connectivity are incomplete", component.ID)
			continue
		}
		seen := map[string]bool{}
		for _, pad := range component.Pads {
			if !idValid(pad.ID) || seen[pad.ID] {
				code := "PAD_ID_INVALID"
				if seen[pad.ID] {
					code = "PAD_ID_DUPLICATE"
				}
				add(code, "error", "Footprint pad needs a unique stable ID", component.ID, pad.ID)
			}
			seen[pad.ID] = true
			if !pinIDs[pad.PinID] {
				add("PAD_PIN_MISSING", "error", "Footprint pad references a missing component pin", component.ID, pad.ID, pad.PinID)
			}
			if pad.WidthNM <= 0 || pad.HeightNM <= 0 {
				add("PAD_DIMENSIONS", "error", "Pad width and height must be positive", component.ID, pad.ID)
			}
			shape := strings.ToLower(strings.TrimSpace(pad.Shape))
			if shape != "rect" && shape != "roundrect" && shape != "circle" && shape != "oval" {
				add("PAD_SHAPE_UNKNOWN", "error", "Pad shape must be rect, roundrect, circle, or oval", component.ID, pad.ID)
			}
			if len(pad.Layers) == 0 {
				add("PAD_LAYERS_MISSING", "error", "Pad must declare at least one copper layer", component.ID, pad.ID)
			}
			for _, layerID := range pad.Layers {
				layer, ok := layers[layerID]
				if !ok || layer.Kind != "copper" {
					add("PAD_LAYER_INVALID", "error", "Pad layer is missing or is not copper", component.ID, pad.ID, layerID)
				}
			}
			if pad.DrillNM > 0 {
				if pad.DrillNM < def.Rules.MinDrillNM {
					add("PAD_DRILL_TOO_SMALL", "error", "Pad drill is below the minimum drill rule", component.ID, pad.ID)
				}
				if (min64(pad.WidthNM, pad.HeightNM)-pad.DrillNM)/2 < def.Rules.MinAnnularRingNM {
					add("PAD_ANNULAR_RING", "error", "Pad annular ring is below the minimum rule", component.ID, pad.ID)
				}
			}
			placed := placePad(component, pad)
			outside := false
			for _, corner := range placed.Corners {
				if !inside(def.Board, corner.XNM, corner.YNM, 0) {
					outside = true
					break
				}
			}
			if outside {
				add("PAD_OUTSIDE_BOARD", "error", "Pad copper lies outside the board", component.ID, pad.ID)
			}
			allPads = append(allPads, placed)
			padsByNode[component.ID+":"+pad.PinID] = append(padsByNode[component.ID+":"+pad.PinID], placed)
		}
	}

	for _, net := range def.Nets {
		geometricNodes := 0
		netPads := []*placedPad{}
		for _, node := range net.Nodes {
			key := node.ComponentID + ":" + node.PinID
			pads := padsByNode[key]
			if len(pads) == 0 {
				if component, ok := components[node.ComponentID]; ok && len(component.Pads) > 0 {
					add("NET_PAD_MAPPING_MISSING", "error", "Net pin has no matching native footprint pad", net.ID, node.ComponentID, node.PinID)
				}
				continue
			}
			geometricNodes++
			for _, pad := range pads {
				pad.NetID = net.ID
				netPads = append(netPads, pad)
				if !padConnected(def, net.ID, pad) {
					add("PAD_UNROUTED", "error", "Pad is not touched by a same-net trace or copper zone", net.ID, node.ComponentID, pad.Pad.ID)
				}
			}
		}
		if geometricNodes >= 2 && !netHasCopper(def, net.ID) {
			add("NET_UNROUTED", "error", "Net has native pad geometry but no trace or copper zone", net.ID)
		} else if geometricNodes >= 2 && !netCopperConnected(def, net.ID, netPads) {
			add("NET_COPPER_DISCONNECTED", "error", "Same-net copper does not form one connected path between all pads", net.ID)
		}
	}

	// Approximate pad/trace clearance with each pad's circumscribed radius.
	// It is conservative and deterministic; polygon-level clearance can replace
	// this without changing the source model.
	for i := 0; i < len(allPads); i++ {
		for j := i + 1; j < len(allPads); j++ {
			a, b := allPads[i], allPads[j]
			if a.NetID == "" || b.NetID == "" || a.NetID == b.NetID || !shareLayer(a.Pad.Layers, b.Pad.Layers) {
				continue
			}
			minimum := padRadius(a.Pad) + padRadius(b.Pad) + float64(def.Rules.MinClearanceNM)
			if pointDistance(a.Center, b.Center) < minimum {
				add("PAD_CLEARANCE", "error", "Different-net pads violate copper clearance", a.ComponentID+":"+a.Pad.ID, b.ComponentID+":"+b.Pad.ID)
			}
		}
	}
	for _, pad := range allPads {
		if pad.NetID == "" {
			continue
		}
		for _, trace := range def.Traces {
			if trace.NetID == pad.NetID || !containsString(pad.Pad.Layers, trace.Layer) {
				continue
			}
			minimum := padRadius(pad.Pad) + float64(trace.WidthNM)/2 + float64(def.Rules.MinClearanceNM)
			if pointToTraceDistance(pad.Center, trace) < minimum {
				add("PAD_TRACE_CLEARANCE", "error", "Trace violates clearance to a different-net pad", pad.ComponentID+":"+pad.Pad.ID, trace.ID)
			}
		}
	}

	for _, zone := range def.Zones {
		if !idValid(zone.ID) {
			add("ZONE_ID_INVALID", "error", "Copper zone has an invalid stable ID", zone.ID)
		}
		if _, ok := nets[zone.NetID]; !ok {
			add("ZONE_NET_MISSING", "error", "Copper zone references a missing net", zone.ID, zone.NetID)
		}
		if layer, ok := layers[zone.Layer]; !ok || layer.Kind != "copper" {
			add("ZONE_LAYER_INVALID", "error", "Copper zone layer is missing or not copper", zone.ID, zone.Layer)
		}
		if len(zone.Polygon) < 3 {
			add("ZONE_GEOMETRY", "error", "Copper zone needs at least three polygon points", zone.ID)
		}
		for _, point := range zone.Polygon {
			if !inside(def.Board, point.XNM, point.YNM, def.Rules.MinEdgeClearanceNM) {
				add("ZONE_EDGE_CLEARANCE", "error", "Copper zone violates board-edge clearance", zone.ID)
				break
			}
		}
		for _, pad := range allPads {
			if pad.NetID != "" && pad.NetID != zone.NetID && containsString(pad.Pad.Layers, zone.Layer) && pointInPolygon(pad.Center, zone.Polygon) {
				add("ZONE_PAD_CLEARANCE", "error", "Copper zone overlaps a different-net pad", zone.ID, pad.ComponentID+":"+pad.Pad.ID)
			}
		}
		for _, trace := range def.Traces {
			if trace.Layer == zone.Layer && trace.NetID != zone.NetID && traceIntersectsPolygon(trace, zone.Polygon) {
				add("ZONE_TRACE_CLEARANCE", "error", "Copper zone overlaps a different-net trace", zone.ID, trace.ID)
			}
		}
	}
	for i := 0; i < len(def.Zones); i++ {
		for j := i + 1; j < len(def.Zones); j++ {
			a, b := def.Zones[i], def.Zones[j]
			if a.Layer == b.Layer && a.NetID != b.NetID && polygonsIntersect(a.Polygon, b.Polygon) {
				add("ZONE_CLEARANCE", "error", "Different-net copper zones overlap", a.ID, b.ID)
			}
		}
	}

	for _, keepout := range def.Keepouts {
		validateKeepout(def, keepout, allPads, &checks)
	}
	for _, pair := range def.DifferentialPairs {
		validateDifferentialPair(def, pair, nets, &checks)
	}
	return checks
}

func placePad(component Component, pad Pad) *placedPad {
	angle := float64(component.Position.RotationUdeg) / 1e6 * math.Pi / 180
	rotate := func(x, y int64) Point {
		fx, fy := float64(x), float64(y)
		return Point{XNM: component.Position.XNM + int64(math.Round(fx*math.Cos(angle)-fy*math.Sin(angle))), YNM: component.Position.YNM + int64(math.Round(fx*math.Sin(angle)+fy*math.Cos(angle)))}
	}
	hw, hh := pad.WidthNM/2, pad.HeightNM/2
	return &placedPad{
		ComponentID: component.ID, PinID: pad.PinID, Pad: pad,
		Center:  rotate(pad.XNM, pad.YNM),
		Corners: []Point{rotate(pad.XNM-hw, pad.YNM-hh), rotate(pad.XNM+hw, pad.YNM-hh), rotate(pad.XNM+hw, pad.YNM+hh), rotate(pad.XNM-hw, pad.YNM+hh)},
	}
}

func padConnected(def *Definition, netID string, pad *placedPad) bool {
	for _, trace := range def.Traces {
		if trace.NetID == netID && containsString(pad.Pad.Layers, trace.Layer) && pointToTraceDistance(pad.Center, trace) <= padRadius(pad.Pad)+float64(trace.WidthNM)/2 {
			return true
		}
	}
	for _, zone := range def.Zones {
		if zone.NetID == netID && containsString(pad.Pad.Layers, zone.Layer) && pointInPolygon(pad.Center, zone.Polygon) {
			return true
		}
	}
	return false
}

func netHasCopper(def *Definition, netID string) bool {
	for _, trace := range def.Traces {
		if trace.NetID == netID {
			return true
		}
	}
	for _, zone := range def.Zones {
		if zone.NetID == netID {
			return true
		}
	}
	return false
}

type unionFind struct{ parent []int }

func newUnionFind(n int) *unionFind {
	u := &unionFind{parent: make([]int, n)}
	for i := range u.parent {
		u.parent[i] = i
	}
	return u
}
func (u *unionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}
func (u *unionFind) join(a, b int) {
	a, b = u.find(a), u.find(b)
	if a != b {
		u.parent[b] = a
	}
}

func netCopperConnected(def *Definition, netID string, pads []*placedPad) bool {
	traces, zones, vias := []Trace{}, []Zone{}, []Via{}
	for _, trace := range def.Traces {
		if trace.NetID == netID {
			traces = append(traces, trace)
		}
	}
	for _, zone := range def.Zones {
		if zone.NetID == netID {
			zones = append(zones, zone)
		}
	}
	for _, via := range def.Vias {
		if via.NetID == netID {
			vias = append(vias, via)
		}
	}
	traceBase, zoneBase, viaBase := len(pads), len(pads)+len(traces), len(pads)+len(traces)+len(zones)
	u := newUnionFind(len(pads) + len(traces) + len(zones) + len(vias))
	for pi, pad := range pads {
		for ti, trace := range traces {
			if containsString(pad.Pad.Layers, trace.Layer) && pointToTraceDistance(pad.Center, trace) <= padRadius(pad.Pad)+float64(trace.WidthNM)/2 {
				u.join(pi, traceBase+ti)
			}
		}
		for zi, zone := range zones {
			if containsString(pad.Pad.Layers, zone.Layer) && pointInPolygon(pad.Center, zone.Polygon) {
				u.join(pi, zoneBase+zi)
			}
		}
		for vi, via := range vias {
			if shareLayer(pad.Pad.Layers, []string{via.FromLayer, via.ToLayer}) && pointDistance(pad.Center, Point{XNM: via.XNM, YNM: via.YNM}) <= padRadius(pad.Pad)+float64(via.DiameterNM)/2 {
				u.join(pi, viaBase+vi)
			}
		}
	}
	for i := range traces {
		for j := i + 1; j < len(traces); j++ {
			if traces[i].Layer == traces[j].Layer && traceDistance(traces[i], traces[j]) <= float64(traces[i].WidthNM+traces[j].WidthNM)/2 {
				u.join(traceBase+i, traceBase+j)
			}
		}
		for zi, zone := range zones {
			if traces[i].Layer == zone.Layer && traceIntersectsPolygon(traces[i], zone.Polygon) {
				u.join(traceBase+i, zoneBase+zi)
			}
		}
		for vi, via := range vias {
			if layerBetween(def, traces[i].Layer, via.FromLayer, via.ToLayer) && pointToTraceDistance(Point{XNM: via.XNM, YNM: via.YNM}, traces[i]) <= float64(via.DiameterNM+traces[i].WidthNM)/2 {
				u.join(traceBase+i, viaBase+vi)
			}
		}
	}
	for i := range zones {
		for j := i + 1; j < len(zones); j++ {
			if zones[i].Layer == zones[j].Layer && polygonsIntersect(zones[i].Polygon, zones[j].Polygon) {
				u.join(zoneBase+i, zoneBase+j)
			}
		}
		for vi, via := range vias {
			if layerBetween(def, zones[i].Layer, via.FromLayer, via.ToLayer) && pointInPolygon(Point{XNM: via.XNM, YNM: via.YNM}, zones[i].Polygon) {
				u.join(zoneBase+i, viaBase+vi)
			}
		}
	}
	if len(pads) < 2 {
		return true
	}
	root := u.find(0)
	for i := 1; i < len(pads); i++ {
		if u.find(i) != root {
			return false
		}
	}
	return true
}

func validateKeepout(def *Definition, keepout Keepout, pads []*placedPad, checks *[]Check) {
	add := func(code, severity, message string, ids ...string) {
		*checks = append(*checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	if !idValid(keepout.ID) || len(keepout.Polygon) < 3 {
		add("KEEPOUT_GEOMETRY", "error", "Keepout needs a stable ID and at least three polygon points", keepout.ID)
		return
	}
	kind := strings.ToLower(strings.TrimSpace(keepout.Kind))
	if kind != "antenna" && kind != "copper" && kind != "component" && kind != "all" {
		add("KEEPOUT_KIND_UNKNOWN", "error", "Keepout kind must be antenna, copper, component, or all", keepout.ID)
		return
	}
	checkCopper := kind == "antenna" || kind == "copper" || kind == "all"
	checkComponents := kind == "antenna" || kind == "component" || kind == "all"
	if checkCopper {
		for _, trace := range def.Traces {
			if keepout.Layer != "" && keepout.Layer != trace.Layer {
				continue
			}
			if traceIntersectsPolygon(trace, keepout.Polygon) {
				add("KEEPOUT_TRACE", "error", "Copper trace enters a keepout", keepout.ID, trace.ID)
			}
		}
		for _, via := range def.Vias {
			if pointInPolygon(Point{XNM: via.XNM, YNM: via.YNM}, keepout.Polygon) {
				add("KEEPOUT_VIA", "error", "Via enters a keepout", keepout.ID, via.ID)
			}
		}
		for _, zone := range def.Zones {
			if keepout.Layer != "" && keepout.Layer != zone.Layer {
				continue
			}
			if polygonsIntersect(zone.Polygon, keepout.Polygon) {
				add("KEEPOUT_ZONE", "error", "Copper zone enters a keepout", keepout.ID, zone.ID)
			}
		}
		for _, pad := range pads {
			if keepout.Layer != "" && !containsString(pad.Pad.Layers, keepout.Layer) {
				continue
			}
			if pointInPolygon(pad.Center, keepout.Polygon) {
				add("KEEPOUT_PAD", "error", "Copper pad enters a keepout", keepout.ID, pad.ComponentID+":"+pad.Pad.ID)
			}
		}
	}
	if checkComponents {
		for _, component := range def.Components {
			if component.ID == keepout.OwnerID {
				continue
			}
			body := component.Body
			if body == nil || body.WidthNM <= 0 || body.HeightNM <= 0 {
				fallback := inferredBody(component)
				body = &fallback
			}
			if polygonsIntersect(bodyPolygon(component, *body), keepout.Polygon) {
				add("KEEPOUT_COMPONENT", "error", "Component body enters a keepout", keepout.ID, component.ID)
			}
		}
	}
}

func validateDifferentialPair(def *Definition, pair DifferentialPair, nets map[string]Net, checks *[]Check) {
	add := func(code, severity, message string, ids ...string) {
		*checks = append(*checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	if !idValid(pair.ID) || pair.PositiveNetID == pair.NegativeNetID {
		add("DIFF_PAIR_INVALID", "error", "Differential pair needs a stable ID and two different nets", pair.ID)
		return
	}
	if _, ok := nets[pair.PositiveNetID]; !ok {
		add("DIFF_PAIR_NET_MISSING", "error", "Differential pair positive net is missing", pair.ID, pair.PositiveNetID)
	}
	if _, ok := nets[pair.NegativeNetID]; !ok {
		add("DIFF_PAIR_NET_MISSING", "error", "Differential pair negative net is missing", pair.ID, pair.NegativeNetID)
	}
	positive, negative := tracesForNet(def, pair.PositiveNetID), tracesForNet(def, pair.NegativeNetID)
	if len(positive) == 0 || len(negative) == 0 {
		add("DIFF_PAIR_UNROUTED", "error", "Both differential-pair nets need routed copper", pair.ID)
		return
	}
	posLength, negLength := tracesLength(positive), tracesLength(negative)
	if pair.MaxSkewNM <= 0 {
		add("DIFF_PAIR_SKEW_RULE", "error", "Differential pair max_skew_nm must be positive", pair.ID)
	} else if math.Abs(posLength-negLength) > float64(pair.MaxSkewNM) {
		add("DIFF_PAIR_SKEW", "error", fmt.Sprintf("Differential pair length skew %.3f mm exceeds %.3f mm", math.Abs(posLength-negLength)/1e6, float64(pair.MaxSkewNM)/1e6), pair.ID)
	}
	if pair.GapNM <= 0 {
		add("DIFF_PAIR_GAP_RULE", "error", "Differential pair gap_nm must be positive", pair.ID)
		return
	}
	minimum := math.MaxFloat64
	for _, a := range positive {
		for _, b := range negative {
			if a.Layer != b.Layer {
				continue
			}
			center := traceDistance(a, b)
			edge := center - float64(a.WidthNM+b.WidthNM)/2
			if edge < minimum {
				minimum = edge
			}
		}
	}
	if minimum == math.MaxFloat64 {
		add("DIFF_PAIR_LAYER", "error", "Differential-pair traces do not share a copper layer", pair.ID)
		return
	}
	tolerance := pair.GapToleranceNM
	if tolerance <= 0 {
		tolerance = max64(50_000, pair.GapNM/2)
	}
	if math.Abs(minimum-float64(pair.GapNM)) > float64(tolerance) {
		add("DIFF_PAIR_GAP", "error", fmt.Sprintf("Differential pair edge gap %.3f mm is outside %.3f ± %.3f mm", minimum/1e6, float64(pair.GapNM)/1e6, float64(tolerance)/1e6), pair.ID)
	}
}

func tracesForNet(def *Definition, netID string) []Trace {
	out := []Trace{}
	for _, trace := range def.Traces {
		if trace.NetID == netID {
			out = append(out, trace)
		}
	}
	return out
}

func tracesLength(traces []Trace) float64 {
	total := 0.0
	for _, trace := range traces {
		for i := 1; i < len(trace.Points); i++ {
			total += pointDistance(trace.Points[i-1], trace.Points[i])
		}
	}
	return total
}

func traceDistance(a, b Trace) float64 {
	minimum := math.MaxFloat64
	for i := 1; i < len(a.Points); i++ {
		for j := 1; j < len(b.Points); j++ {
			minimum = math.Min(minimum, segmentDistance(a.Points[i-1], a.Points[i], b.Points[j-1], b.Points[j]))
		}
	}
	return minimum
}

func pointToTraceDistance(point Point, trace Trace) float64 {
	minimum := math.MaxFloat64
	for i := 1; i < len(trace.Points); i++ {
		minimum = math.Min(minimum, pointSegmentDistance(point, trace.Points[i-1], trace.Points[i]))
	}
	return minimum
}

func traceIntersectsPolygon(trace Trace, polygon []Point) bool {
	for _, point := range trace.Points {
		if pointInPolygon(point, polygon) {
			return true
		}
	}
	for i := 1; i < len(trace.Points); i++ {
		for j := range polygon {
			if segmentsIntersect(trace.Points[i-1], trace.Points[i], polygon[j], polygon[(j+1)%len(polygon)]) {
				return true
			}
		}
	}
	return false
}

func polygonsIntersect(a, b []Point) bool {
	if len(a) < 3 || len(b) < 3 {
		return false
	}
	if pointInPolygon(a[0], b) || pointInPolygon(b[0], a) {
		return true
	}
	for i := range a {
		for j := range b {
			if segmentsIntersect(a[i], a[(i+1)%len(a)], b[j], b[(j+1)%len(b)]) {
				return true
			}
		}
	}
	return false
}

func pointInPolygon(point Point, polygon []Point) bool {
	inside := false
	for i, j := 0, len(polygon)-1; i < len(polygon); j, i = i, i+1 {
		a, b := polygon[i], polygon[j]
		if (a.YNM > point.YNM) != (b.YNM > point.YNM) && float64(point.XNM) < float64(b.XNM-a.XNM)*float64(point.YNM-a.YNM)/float64(b.YNM-a.YNM)+float64(a.XNM) {
			inside = !inside
		}
	}
	return inside
}

func pointDistance(a, b Point) float64 {
	return math.Hypot(float64(a.XNM-b.XNM), float64(a.YNM-b.YNM))
}

func padRadius(pad Pad) float64 { return math.Hypot(float64(pad.WidthNM), float64(pad.HeightNM)) / 2 }
func shareLayer(a, b []string) bool {
	for _, layer := range a {
		if containsString(b, layer) {
			return true
		}
	}
	return false
}
func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

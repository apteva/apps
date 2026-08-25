package main

import (
	"fmt"
	"math"
	"strings"
)

func validateDefinition(def *Definition) ValidationReport {
	report := ValidationReport{
		Schema: "apteva-pcb-validation/v1",
		Engine: engineVersion,
		Status: "passed",
		Checks: []Check{},
		Metrics: Metrics{
			Components: len(def.Components), Nets: len(def.Nets),
			Traces: len(def.Traces), Vias: len(def.Vias),
			Zones: len(def.Zones), Keepouts: len(def.Keepouts), DifferentialPairs: len(def.DifferentialPairs),
			BoardAreaNM2: saturatingArea(def.Board.WidthNM, def.Board.HeightNM),
		},
	}
	if def.Wiring != nil {
		report.Metrics.WiringParts = len(def.Wiring.Parts)
		report.Metrics.WiringWires = len(def.Wiring.Wires)
	}
	add := func(code, severity, message string, ids ...string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
		if severity == "error" {
			report.Errors++
		} else if severity == "warning" {
			report.Warnings++
		}
	}
	if def.Rules.MinClearanceNM <= 0 {
		add("RULE_CLEARANCE_INVALID", "error", "Minimum copper clearance must be positive")
	}
	if def.Rules.MinTraceWidthNM <= 0 {
		add("RULE_TRACE_WIDTH_INVALID", "error", "Minimum trace width must be positive")
	}
	if def.Rules.MinEdgeClearanceNM < 0 {
		add("RULE_EDGE_CLEARANCE_INVALID", "error", "Board-edge clearance cannot be negative")
	}
	if def.Rules.MinDrillNM <= 0 {
		add("RULE_DRILL_INVALID", "error", "Minimum plated drill must be positive")
	}
	if def.Rules.MinAnnularRingNM <= 0 {
		add("RULE_ANNULAR_RING_INVALID", "error", "Minimum annular ring must be positive")
	}

	layers := map[string]Layer{}
	orders := map[int]string{}
	for _, layer := range def.Board.Layers {
		if !idValid(layer.ID) {
			add("LAYER_ID_INVALID", "error", "Layer has an invalid stable ID", layer.ID)
		}
		if _, ok := layers[layer.ID]; ok {
			add("LAYER_ID_DUPLICATE", "error", "Layer ID is duplicated", layer.ID)
		}
		if other, ok := orders[layer.Order]; ok {
			add("LAYER_ORDER_DUPLICATE", "error", fmt.Sprintf("Layers %s and %s have the same order", other, layer.ID), other, layer.ID)
		}
		layers[layer.ID], orders[layer.Order] = layer, layer.ID
		if layer.Kind != "copper" && layer.Kind != "dielectric" && layer.Kind != "silkscreen" && layer.Kind != "mask" && layer.Kind != "mechanical" {
			add("LAYER_KIND_UNKNOWN", "warning", "Layer kind is not one of the native v1 kinds", layer.ID)
		}
	}
	if len(def.Board.Layers) < 2 {
		add("STACKUP_TOO_SMALL", "error", "A board needs at least two layers")
	}

	components := map[string]Component{}
	designators := map[string]string{}
	pins := map[string]map[string]Pin{}
	for _, component := range def.Components {
		if !idValid(component.ID) {
			add("COMPONENT_ID_INVALID", "error", "Component has an invalid stable ID", component.ID)
		}
		if _, ok := components[component.ID]; ok {
			add("COMPONENT_ID_DUPLICATE", "error", "Component ID is duplicated", component.ID)
		}
		components[component.ID] = component
		d := strings.ToUpper(strings.TrimSpace(component.Designator))
		if d == "" {
			add("DESIGNATOR_MISSING", "error", "Component designator is required", component.ID)
		} else if other, ok := designators[d]; ok {
			add("DESIGNATOR_DUPLICATE", "error", fmt.Sprintf("Designator %s is used by %s and %s", d, other, component.ID), other, component.ID)
		} else {
			designators[d] = component.ID
		}
		if strings.TrimSpace(component.Footprint) == "" {
			add("FOOTPRINT_MISSING", "warning", "Component has no footprint mapping", component.ID)
		}
		if component.Position.Side != "front" && component.Position.Side != "back" {
			add("SIDE_INVALID", "error", "Component side must be front or back", component.ID)
		}
		if !inside(def.Board, component.Position.XNM, component.Position.YNM, 0) {
			add("COMPONENT_OUTSIDE_BOARD", "error", "Component origin lies outside the board", component.ID)
		}
		pins[component.ID] = map[string]Pin{}
		for _, pin := range component.Pins {
			report.Metrics.Pins++
			if !idValid(pin.ID) {
				add("PIN_ID_INVALID", "error", "Pin has an invalid stable ID", component.ID, pin.ID)
			}
			if _, ok := pins[component.ID][pin.ID]; ok {
				add("PIN_ID_DUPLICATE", "error", "Pin ID is duplicated within its component", component.ID, pin.ID)
			}
			pins[component.ID][pin.ID] = pin
			if pin.Pad == "" {
				add("PIN_PAD_MISSING", "warning", "Pin has no footprint pad mapping", component.ID, pin.ID)
			}
		}
		report.Metrics.Pads += len(component.Pads)
	}

	nets := map[string]Net{}
	netNames := map[string]string{}
	assignedPins := map[string]string{}
	for _, net := range def.Nets {
		if !idValid(net.ID) {
			add("NET_ID_INVALID", "error", "Net has an invalid stable ID", net.ID)
		}
		if _, ok := nets[net.ID]; ok {
			add("NET_ID_DUPLICATE", "error", "Net ID is duplicated", net.ID)
		}
		nets[net.ID] = net
		name := strings.ToLower(strings.TrimSpace(net.Name))
		if name == "" {
			add("NET_NAME_MISSING", "warning", "Net has no human-readable name", net.ID)
		} else if other, ok := netNames[name]; ok {
			add("NET_NAME_DUPLICATE", "warning", "Net name is duplicated", other, net.ID)
		} else {
			netNames[name] = net.ID
		}
		if len(net.Nodes) < 2 {
			add("NET_SINGLETON", "warning", "Net connects fewer than two pins", net.ID)
		}
		netNodes := map[string]bool{}
		for _, node := range net.Nodes {
			nodeKey := node.ComponentID + ":" + node.PinID
			if netNodes[nodeKey] {
				add("NET_NODE_DUPLICATE", "error", "A pin is repeated within the same net", net.ID, nodeKey)
			}
			netNodes[nodeKey] = true
			if _, ok := components[node.ComponentID]; !ok {
				add("NET_COMPONENT_MISSING", "error", "Net node references a missing component", net.ID, node.ComponentID)
				continue
			}
			if _, ok := pins[node.ComponentID][node.PinID]; !ok {
				add("NET_PIN_MISSING", "error", "Net node references a missing pin", net.ID, node.ComponentID, node.PinID)
				continue
			}
			key := node.ComponentID + ":" + node.PinID
			if other, ok := assignedPins[key]; ok && other != net.ID {
				add("PIN_MULTIPLE_NETS", "error", "Pin belongs to more than one net", key, other, net.ID)
			} else {
				assignedPins[key] = net.ID
			}
		}
	}
	for componentID, pinMap := range pins {
		for pinID, pin := range pinMap {
			key := componentID + ":" + pinID
			if _, ok := assignedPins[key]; !ok && pin.ElectricalType != "nc" && pin.ElectricalType != "passive" {
				add("PIN_UNCONNECTED", "warning", "Non-passive pin is not assigned to a net", componentID, pinID)
			}
		}
	}

	netClassIDs, classifiedNets := map[string]bool{}, map[string]string{}
	for _, class := range def.NetClasses {
		if !idValid(class.ID) {
			add("NET_CLASS_ID_INVALID", "error", "Net class has an invalid stable ID", class.ID)
		}
		if netClassIDs[class.ID] {
			add("NET_CLASS_ID_DUPLICATE", "error", "Net class ID is duplicated", class.ID)
		}
		netClassIDs[class.ID] = true
		if class.TraceWidthNM > 0 && class.TraceWidthNM < def.Rules.MinTraceWidthNM {
			add("NET_CLASS_TRACE_WIDTH", "error", "Net class trace width is below the board minimum", class.ID)
		}
		if class.ClearanceNM > 0 && class.ClearanceNM < def.Rules.MinClearanceNM {
			add("NET_CLASS_CLEARANCE", "error", "Net class clearance is below the board minimum", class.ID)
		}
		if class.ViaDrillNM > 0 && class.ViaDrillNM < def.Rules.MinDrillNM {
			add("NET_CLASS_VIA_DRILL", "error", "Net class via drill is below the board minimum", class.ID)
		}
		if class.ViaDiameterNM > 0 && class.ViaDrillNM > 0 && (class.ViaDiameterNM-class.ViaDrillNM)/2 < def.Rules.MinAnnularRingNM {
			add("NET_CLASS_ANNULAR_RING", "error", "Net class via annular ring is below the board minimum", class.ID)
		}
		for _, netID := range class.NetIDs {
			if _, ok := nets[netID]; !ok {
				add("NET_CLASS_NET_MISSING", "error", "Net class references a missing net", class.ID, netID)
				continue
			}
			if other, ok := classifiedNets[netID]; ok {
				add("NET_MULTIPLE_CLASSES", "error", "Net belongs to multiple net classes", netID, other, class.ID)
			} else {
				classifiedNets[netID] = class.ID
			}
		}
	}
	if def.Simulation != nil {
		if def.Simulation.DurationUS < 0 || def.Simulation.StepUS < 0 {
			add("SIMULATION_TIME_INVALID", "error", "Simulation duration and step cannot be negative")
		}
		if def.Simulation.DurationUS > 0 && def.Simulation.StepUS > 0 && def.Simulation.DurationUS/def.Simulation.StepUS > 2_000 {
			add("SIMULATION_SAMPLE_LIMIT", "error", "Simulation configuration exceeds 2000 samples")
		}
		sourceIDs := map[string]bool{}
		for _, source := range def.Simulation.Sources {
			if !idValid(source.ID) || sourceIDs[source.ID] {
				add("SIMULATION_SOURCE_ID", "error", "Simulation source needs a unique stable ID", source.ID)
			}
			sourceIDs[source.ID] = true
			if _, ok := nets[source.NetID]; !ok {
				add("SIMULATION_SOURCE_NET", "error", "Simulation source references a missing net", source.ID, source.NetID)
			}
			if source.Kind != "dc" && source.Kind != "digital" && source.Kind != "clock" {
				add("SIMULATION_SOURCE_KIND", "error", "Simulation source kind must be dc, digital, or clock", source.ID)
			}
		}
		probeIDs := map[string]bool{}
		for _, probe := range def.Simulation.Probes {
			if !idValid(probe.ID) || probeIDs[probe.ID] {
				add("SIMULATION_PROBE_ID", "error", "Simulation probe needs a unique stable ID", probe.ID)
			}
			probeIDs[probe.ID] = true
			if _, ok := nets[probe.NetID]; !ok {
				add("SIMULATION_PROBE_NET", "error", "Simulation probe references a missing net", probe.ID, probe.NetID)
			}
		}
	}

	traceIDs := map[string]bool{}
	for _, trace := range def.Traces {
		if !idValid(trace.ID) {
			add("TRACE_ID_INVALID", "error", "Trace has an invalid stable ID", trace.ID)
		}
		if traceIDs[trace.ID] {
			add("TRACE_ID_DUPLICATE", "error", "Trace ID is duplicated", trace.ID)
		}
		traceIDs[trace.ID] = true
		if _, ok := nets[trace.NetID]; !ok {
			add("TRACE_NET_MISSING", "error", "Trace references a missing net", trace.ID, trace.NetID)
		}
		if layer, ok := layers[trace.Layer]; !ok {
			add("TRACE_LAYER_MISSING", "error", "Trace references a missing layer", trace.ID, trace.Layer)
		} else if layer.Kind != "copper" {
			add("TRACE_LAYER_NOT_COPPER", "error", "Trace is placed on a non-copper layer", trace.ID, trace.Layer)
		}
		if trace.WidthNM < def.Rules.MinTraceWidthNM {
			add("TRACE_WIDTH", "error", "Trace is narrower than the minimum rule", trace.ID)
		}
		if len(trace.Points) < 2 {
			add("TRACE_GEOMETRY", "error", "Trace must contain at least two points", trace.ID)
		}
		for _, p := range trace.Points {
			if !inside(def.Board, p.XNM, p.YNM, def.Rules.MinEdgeClearanceNM) {
				add("TRACE_EDGE_CLEARANCE", "error", "Trace violates board-edge clearance", trace.ID)
				break
			}
		}
	}

	viaIDs := map[string]bool{}
	for _, via := range def.Vias {
		if !idValid(via.ID) {
			add("VIA_ID_INVALID", "error", "Via has an invalid stable ID", via.ID)
		}
		if viaIDs[via.ID] {
			add("VIA_ID_DUPLICATE", "error", "Via ID is duplicated", via.ID)
		}
		viaIDs[via.ID] = true
		if _, ok := nets[via.NetID]; !ok {
			add("VIA_NET_MISSING", "error", "Via references a missing net", via.ID, via.NetID)
		}
		if layer, ok := layers[via.FromLayer]; !ok {
			add("VIA_LAYER_MISSING", "error", "Via start layer is missing", via.ID, via.FromLayer)
		} else if layer.Kind != "copper" {
			add("VIA_LAYER_NOT_COPPER", "error", "Via start layer is not copper", via.ID, via.FromLayer)
		}
		if layer, ok := layers[via.ToLayer]; !ok {
			add("VIA_LAYER_MISSING", "error", "Via end layer is missing", via.ID, via.ToLayer)
		} else if layer.Kind != "copper" {
			add("VIA_LAYER_NOT_COPPER", "error", "Via end layer is not copper", via.ID, via.ToLayer)
		}
		if via.DrillNM <= 0 || via.DiameterNM <= via.DrillNM {
			add("VIA_DIMENSIONS", "error", "Via diameter must exceed a positive drill diameter", via.ID)
		}
		if !inside(def.Board, via.XNM, via.YNM, via.DiameterNM/2+def.Rules.MinEdgeClearanceNM) {
			add("VIA_EDGE_CLEARANCE", "error", "Via violates board-edge clearance", via.ID)
		}
	}

	for i := 0; i < len(def.Traces); i++ {
		for j := i + 1; j < len(def.Traces); j++ {
			a, b := def.Traces[i], def.Traces[j]
			if a.Layer != b.Layer || a.NetID == b.NetID {
				continue
			}
			minimum := float64(a.WidthNM+b.WidthNM)/2 + float64(def.Rules.MinClearanceNM)
			if tracesCloserThan(a, b, minimum) {
				add("TRACE_CLEARANCE", "error", "Different nets violate copper clearance", a.ID, b.ID)
			}
		}
	}

	for _, check := range validateManufacturing(def) {
		add(check.Code, check.Severity, check.Message, check.ObjectIDs...)
	}
	for _, check := range wiringChecks(def.Wiring) {
		add(check.Code, check.Severity, check.Message, check.ObjectIDs...)
	}

	if report.Errors > 0 {
		report.Status = "failed"
	} else if report.Warnings > 0 {
		report.Status = "warning"
	}
	return report
}

func saturatingArea(a, b int64) int64 {
	if a <= 0 || b <= 0 {
		return 0
	}
	if a > math.MaxInt64/b {
		return math.MaxInt64
	}
	return a * b
}

func inside(board Board, x, y, margin int64) bool {
	return x >= margin && y >= margin && x <= board.WidthNM-margin && y <= board.HeightNM-margin
}

func tracesCloserThan(a, b Trace, minimum float64) bool {
	for i := 1; i < len(a.Points); i++ {
		for j := 1; j < len(b.Points); j++ {
			if segmentDistance(a.Points[i-1], a.Points[i], b.Points[j-1], b.Points[j]) < minimum {
				return true
			}
		}
	}
	return false
}

func segmentDistance(a, b, c, d Point) float64 {
	if segmentsIntersect(a, b, c, d) {
		return 0
	}
	return math.Min(math.Min(pointSegmentDistance(a, c, d), pointSegmentDistance(b, c, d)), math.Min(pointSegmentDistance(c, a, b), pointSegmentDistance(d, a, b)))
}

func pointSegmentDistance(p, a, b Point) float64 {
	dx, dy := float64(b.XNM-a.XNM), float64(b.YNM-a.YNM)
	if dx == 0 && dy == 0 {
		return math.Hypot(float64(p.XNM-a.XNM), float64(p.YNM-a.YNM))
	}
	t := (float64(p.XNM-a.XNM)*dx + float64(p.YNM-a.YNM)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(float64(p.XNM-a.XNM)-t*dx, float64(p.YNM-a.YNM)-t*dy)
}

func segmentsIntersect(a, b, c, d Point) bool {
	orient := func(p, q, r Point) float64 {
		return float64(q.XNM-p.XNM)*float64(r.YNM-p.YNM) - float64(q.YNM-p.YNM)*float64(r.XNM-p.XNM)
	}
	o1, o2, o3, o4 := orient(a, b, c), orient(a, b, d), orient(c, d, a), orient(c, d, b)
	return ((o1 > 0 && o2 < 0) || (o1 < 0 && o2 > 0)) && ((o3 > 0 && o4 < 0) || (o3 < 0 && o4 > 0))
}

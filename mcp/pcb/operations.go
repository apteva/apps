package main

import (
	"encoding/json"
	"errors"
	"fmt"
)

func applyOperations(base *Definition, operations []Operation) ([]byte, *Definition, string, error) {
	if len(operations) == 0 {
		return nil, nil, "", errors.New("operations must not be empty")
	}
	if len(operations) > 256 {
		return nil, nil, "", errors.New("operation batch exceeds 256 operations")
	}
	raw, _ := json.Marshal(base)
	var next Definition
	_ = json.Unmarshal(raw, &next)
	for i, op := range operations {
		if err := applyOperation(&next, op); err != nil {
			return nil, nil, "", fmt.Errorf("operation %d (%s): %w", i, op.Type, err)
		}
	}
	raw, _ = json.Marshal(next)
	return normalizeDefinition(raw, next.Name)
}

func applyOperation(def *Definition, op Operation) error {
	switch op.Type {
	case "board.set":
		if op.Board == nil {
			return errors.New("board is required")
		}
		def.Board = *op.Board
	case "rules.set":
		if op.Rules == nil {
			return errors.New("rules are required")
		}
		def.Rules = *op.Rules
	case "component.add":
		if op.Component == nil {
			return errors.New("component is required")
		}
		if findComponent(def.Components, op.Component.ID) >= 0 {
			return fmt.Errorf("component %q already exists", op.Component.ID)
		}
		def.Components = append(def.Components, *op.Component)
	case "component.replace":
		if op.Component == nil {
			return errors.New("component is required")
		}
		idx := findComponent(def.Components, op.Component.ID)
		if idx < 0 {
			return fmt.Errorf("component %q not found", op.Component.ID)
		}
		def.Components[idx] = *op.Component
	case "component.remove":
		idx := findComponent(def.Components, op.ComponentID)
		if idx < 0 {
			return fmt.Errorf("component %q not found", op.ComponentID)
		}
		def.Components = append(def.Components[:idx], def.Components[idx+1:]...)
		for i := range def.Nets {
			nodes := def.Nets[i].Nodes[:0]
			for _, node := range def.Nets[i].Nodes {
				if node.ComponentID != op.ComponentID {
					nodes = append(nodes, node)
				}
			}
			def.Nets[i].Nodes = nodes
		}
	case "placement.set":
		if op.Position == nil {
			return errors.New("position is required")
		}
		idx := findComponent(def.Components, op.ComponentID)
		if idx < 0 {
			return fmt.Errorf("component %q not found", op.ComponentID)
		}
		def.Components[idx].Position = *op.Position
	case "net.add":
		if op.Net == nil {
			return errors.New("net is required")
		}
		if findNet(def.Nets, op.Net.ID) >= 0 {
			return fmt.Errorf("net %q already exists", op.Net.ID)
		}
		def.Nets = append(def.Nets, *op.Net)
	case "net.replace":
		if op.Net == nil {
			return errors.New("net is required")
		}
		idx := findNet(def.Nets, op.Net.ID)
		if idx < 0 {
			return fmt.Errorf("net %q not found", op.Net.ID)
		}
		def.Nets[idx] = *op.Net
	case "net.remove":
		idx := findNet(def.Nets, op.NetID)
		if idx < 0 {
			return fmt.Errorf("net %q not found", op.NetID)
		}
		def.Nets = append(def.Nets[:idx], def.Nets[idx+1:]...)
		def.Traces = filterTraces(def.Traces, func(v Trace) bool { return v.NetID != op.NetID })
		def.Vias = filterVias(def.Vias, func(v Via) bool { return v.NetID != op.NetID })
		def.Zones = filterZones(def.Zones, func(v Zone) bool { return v.NetID != op.NetID })
		def.DifferentialPairs = filterDifferentialPairs(def.DifferentialPairs, func(v DifferentialPair) bool { return v.PositiveNetID != op.NetID && v.NegativeNetID != op.NetID })
	case "net.connect":
		if op.Node == nil {
			return errors.New("node is required")
		}
		idx := findNet(def.Nets, op.NetID)
		if idx < 0 {
			return fmt.Errorf("net %q not found", op.NetID)
		}
		for _, n := range def.Nets[idx].Nodes {
			if n == *op.Node {
				return errors.New("node is already connected")
			}
		}
		def.Nets[idx].Nodes = append(def.Nets[idx].Nodes, *op.Node)
	case "net.disconnect":
		if op.Node == nil {
			return errors.New("node is required")
		}
		idx := findNet(def.Nets, op.NetID)
		if idx < 0 {
			return fmt.Errorf("net %q not found", op.NetID)
		}
		removed := false
		nodes := def.Nets[idx].Nodes[:0]
		for _, n := range def.Nets[idx].Nodes {
			if n == *op.Node {
				removed = true
			} else {
				nodes = append(nodes, n)
			}
		}
		if !removed {
			return errors.New("node is not connected")
		}
		def.Nets[idx].Nodes = nodes
	case "trace.add":
		if op.Trace == nil {
			return errors.New("trace is required")
		}
		if findTrace(def.Traces, op.Trace.ID) >= 0 {
			return fmt.Errorf("trace %q already exists", op.Trace.ID)
		}
		def.Traces = append(def.Traces, *op.Trace)
	case "trace.remove":
		idx := findTrace(def.Traces, op.TraceID)
		if idx < 0 {
			return fmt.Errorf("trace %q not found", op.TraceID)
		}
		def.Traces = append(def.Traces[:idx], def.Traces[idx+1:]...)
	case "via.add":
		if op.Via == nil {
			return errors.New("via is required")
		}
		if findVia(def.Vias, op.Via.ID) >= 0 {
			return fmt.Errorf("via %q already exists", op.Via.ID)
		}
		def.Vias = append(def.Vias, *op.Via)
	case "via.remove":
		idx := findVia(def.Vias, op.ViaID)
		if idx < 0 {
			return fmt.Errorf("via %q not found", op.ViaID)
		}
		def.Vias = append(def.Vias[:idx], def.Vias[idx+1:]...)
	case "zone.add":
		if op.Zone == nil {
			return errors.New("zone is required")
		}
		if findZone(def.Zones, op.Zone.ID) >= 0 {
			return fmt.Errorf("zone %q already exists", op.Zone.ID)
		}
		def.Zones = append(def.Zones, *op.Zone)
	case "zone.remove":
		idx := findZone(def.Zones, op.ZoneID)
		if idx < 0 {
			return fmt.Errorf("zone %q not found", op.ZoneID)
		}
		def.Zones = append(def.Zones[:idx], def.Zones[idx+1:]...)
	case "keepout.add":
		if op.Keepout == nil {
			return errors.New("keepout is required")
		}
		if findKeepout(def.Keepouts, op.Keepout.ID) >= 0 {
			return fmt.Errorf("keepout %q already exists", op.Keepout.ID)
		}
		def.Keepouts = append(def.Keepouts, *op.Keepout)
	case "keepout.remove":
		idx := findKeepout(def.Keepouts, op.KeepoutID)
		if idx < 0 {
			return fmt.Errorf("keepout %q not found", op.KeepoutID)
		}
		def.Keepouts = append(def.Keepouts[:idx], def.Keepouts[idx+1:]...)
	case "differential_pair.add":
		if op.DifferentialPair == nil {
			return errors.New("differential_pair is required")
		}
		if findDifferentialPair(def.DifferentialPairs, op.DifferentialPair.ID) >= 0 {
			return fmt.Errorf("differential pair %q already exists", op.DifferentialPair.ID)
		}
		def.DifferentialPairs = append(def.DifferentialPairs, *op.DifferentialPair)
	case "differential_pair.remove":
		idx := findDifferentialPair(def.DifferentialPairs, op.DifferentialPairID)
		if idx < 0 {
			return fmt.Errorf("differential pair %q not found", op.DifferentialPairID)
		}
		def.DifferentialPairs = append(def.DifferentialPairs[:idx], def.DifferentialPairs[idx+1:]...)
	case "net_class.add":
		if op.NetClass == nil {
			return errors.New("net_class is required")
		}
		if findNetClass(def.NetClasses, op.NetClass.ID) >= 0 {
			return fmt.Errorf("net class %q already exists", op.NetClass.ID)
		}
		def.NetClasses = append(def.NetClasses, *op.NetClass)
	case "net_class.replace":
		if op.NetClass == nil {
			return errors.New("net_class is required")
		}
		idx := findNetClass(def.NetClasses, op.NetClass.ID)
		if idx < 0 {
			return fmt.Errorf("net class %q not found", op.NetClass.ID)
		}
		def.NetClasses[idx] = *op.NetClass
	case "net_class.remove":
		idx := findNetClass(def.NetClasses, op.NetClassID)
		if idx < 0 {
			return fmt.Errorf("net class %q not found", op.NetClassID)
		}
		def.NetClasses = append(def.NetClasses[:idx], def.NetClasses[idx+1:]...)
	case "simulation.set":
		def.Simulation = op.Simulation
	case "wiring.set":
		def.Wiring = op.Wiring
	case "wiring.part.add":
		if op.WiringPart == nil {
			return errors.New("wiring_part is required")
		}
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		if findWiringPart(def.Wiring.Parts, op.WiringPart.ID) >= 0 {
			return fmt.Errorf("wiring part %q already exists", op.WiringPart.ID)
		}
		def.Wiring.Parts = append(def.Wiring.Parts, *op.WiringPart)
	case "wiring.part.replace":
		if op.WiringPart == nil {
			return errors.New("wiring_part is required")
		}
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		idx := findWiringPart(def.Wiring.Parts, op.WiringPart.ID)
		if idx < 0 {
			return fmt.Errorf("wiring part %q not found", op.WiringPart.ID)
		}
		def.Wiring.Parts[idx] = *op.WiringPart
	case "wiring.part.remove":
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		idx := findWiringPart(def.Wiring.Parts, op.WiringPartID)
		if idx < 0 {
			return fmt.Errorf("wiring part %q not found", op.WiringPartID)
		}
		def.Wiring.Parts = append(def.Wiring.Parts[:idx], def.Wiring.Parts[idx+1:]...)
		wires := def.Wiring.Wires[:0]
		for _, w := range def.Wiring.Wires {
			if w.From.PartID != op.WiringPartID && w.To.PartID != op.WiringPartID {
				wires = append(wires, w)
			}
		}
		def.Wiring.Wires = wires
	case "wiring.wire.add":
		if op.WiringWire == nil {
			return errors.New("wiring_wire is required")
		}
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		if findWiringWire(def.Wiring.Wires, op.WiringWire.ID) >= 0 {
			return fmt.Errorf("wiring wire %q already exists", op.WiringWire.ID)
		}
		def.Wiring.Wires = append(def.Wiring.Wires, *op.WiringWire)
	case "wiring.wire.replace":
		if op.WiringWire == nil {
			return errors.New("wiring_wire is required")
		}
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		idx := findWiringWire(def.Wiring.Wires, op.WiringWire.ID)
		if idx < 0 {
			return fmt.Errorf("wiring wire %q not found", op.WiringWire.ID)
		}
		def.Wiring.Wires[idx] = *op.WiringWire
	case "wiring.wire.remove":
		if def.Wiring == nil {
			return errors.New("wiring workspace is not initialized")
		}
		idx := findWiringWire(def.Wiring.Wires, op.WiringWireID)
		if idx < 0 {
			return fmt.Errorf("wiring wire %q not found", op.WiringWireID)
		}
		def.Wiring.Wires = append(def.Wiring.Wires[:idx], def.Wiring.Wires[idx+1:]...)
	default:
		return fmt.Errorf("unsupported operation type %q", op.Type)
	}
	return nil
}

func findWiringPart(v []WiringPart, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findWiringWire(v []WiringWire, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}

func findNetClass(v []NetClass, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}

func findComponent(v []Component, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findNet(v []Net, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findTrace(v []Trace, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findVia(v []Via, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findZone(v []Zone, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findKeepout(v []Keepout, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func findDifferentialPair(v []DifferentialPair, id string) int {
	for i := range v {
		if v[i].ID == id {
			return i
		}
	}
	return -1
}
func filterTraces(v []Trace, keep func(Trace) bool) []Trace {
	out := v[:0]
	for _, x := range v {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}
func filterVias(v []Via, keep func(Via) bool) []Via {
	out := v[:0]
	for _, x := range v {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}
func filterZones(v []Zone, keep func(Zone) bool) []Zone {
	out := v[:0]
	for _, x := range v {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}
func filterDifferentialPairs(v []DifferentialPair, keep func(DifferentialPair) bool) []DifferentialPair {
	out := v[:0]
	for _, x := range v {
		if keep(x) {
			out = append(out, x)
		}
	}
	return out
}

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
	default:
		return fmt.Errorf("unsupported operation type %q", op.Type)
	}
	return nil
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

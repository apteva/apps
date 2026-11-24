package main

import (
	"fmt"
	"sort"
	"strings"
)

const schematicERCSchema = "apteva-pcb-schematic-erc/v1"

// schPin is one resolved pin anchor of a placed symbol unit.
type schPin struct {
	Symbol         int
	ComponentID    string
	Number         string
	PinID          string // empty when the symbol pin has no component pin
	ElectricalType string
	SheetID        string
	XNM, YNM       int64
}

type schGroup struct {
	Name   string
	Pins   []int
	Labels []int
}

type schDangling struct {
	WireID   string
	XNM, YNM int64
}

type schConnectivity struct {
	Pins            []schPin
	PinGroup        []int
	Groups          []schGroup
	LabelGroup      []int
	DanglingWires   []schDangling
	DanglingLabels  []int
	Checks          []Check
	PlacedSymbols   map[string]bool // component IDs with at least one valid symbol
	SymbolDefs      []SchematicSymbolDef
	ValidSymbolMask []bool
}

type schUnionFind struct{ parent []int }

func (u *schUnionFind) add() int {
	u.parent = append(u.parent, len(u.parent))
	return len(u.parent) - 1
}
func (u *schUnionFind) find(x int) int {
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]]
		x = u.parent[x]
	}
	return x
}
func (u *schUnionFind) union(a, b int) {
	ra, rb := u.find(a), u.find(b)
	if ra == rb {
		return
	}
	if ra < rb {
		u.parent[rb] = ra
	} else {
		u.parent[ra] = rb
	}
}

type schPointKey struct {
	Sheet string
	X, Y  int64
}

type schSegment struct {
	Wire int
	A, B Point
}

func onSegment(p, a, b Point) bool {
	if p.XNM < min64(a.XNM, b.XNM) || p.XNM > max64(a.XNM, b.XNM) || p.YNM < min64(a.YNM, b.YNM) || p.YNM > max64(a.YNM, b.YNM) {
		return false
	}
	// Coordinates are bounded to ±1e9 nm, so the cross product fits in int64.
	return (b.XNM-a.XNM)*(p.YNM-a.YNM)-(b.YNM-a.YNM)*(p.XNM-a.XNM) == 0
}

// buildSchematicConnectivity resolves symbols, wires, labels, and junctions
// into electrical groups. Two items connect when they share an exact point, a
// point lies on a wire (T-connection), or labels share a name in scope (local
// labels per sheet; global and power labels across sheets).
func buildSchematicConnectivity(def *Definition) *schConnectivity {
	spec := def.Schematic
	conn := &schConnectivity{PlacedSymbols: map[string]bool{}}
	add := func(code, severity, message string, ids ...string) {
		conn.Checks = append(conn.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	sheets := map[string]bool{}
	if len(spec.Sheets) == 0 {
		add("SCH_SHEET_INVALID", "error", "Schematic has no sheets")
	}
	for _, sheet := range spec.Sheets {
		if !idValid(sheet.ID) {
			add("SCH_SHEET_INVALID", "error", "Schematic sheet has an invalid stable ID", sheet.ID)
		} else if sheets[sheet.ID] {
			add("SCH_SHEET_INVALID", "error", "Schematic sheet ID is duplicated", sheet.ID)
		}
		sheets[sheet.ID] = true
	}
	components := map[string]*Component{}
	for i := range def.Components {
		components[def.Components[i].ID] = &def.Components[i]
	}
	uf := &schUnionFind{}
	points := map[schPointKey]int{}
	point := func(sheet string, x, y int64) int {
		key := schPointKey{sheet, x, y}
		if id, ok := points[key]; ok {
			return id
		}
		id := uf.add()
		points[key] = id
		return id
	}
	pointPins := map[schPointKey]int{}
	pointLabels := map[schPointKey]int{}
	pointJunctions := map[schPointKey]int{}

	// Symbols and their pin anchors.
	seenUnits := map[string]bool{}
	conn.SymbolDefs = make([]SchematicSymbolDef, len(spec.Symbols))
	conn.ValidSymbolMask = make([]bool, len(spec.Symbols))
	pinNodes := []int{}
	for si, sym := range spec.Symbols {
		component := components[sym.ComponentID]
		unitKey := fmt.Sprintf("%s\x00%d", sym.ComponentID, sym.Unit)
		switch {
		case component == nil:
			add("SCH_SYMBOL_COMPONENT_UNKNOWN", "error", fmt.Sprintf("Schematic symbol references unknown component %q", sym.ComponentID), sym.ComponentID)
			continue
		case !sheets[sym.SheetID]:
			add("SCH_SHEET_UNKNOWN", "error", fmt.Sprintf("Symbol %s is on unknown sheet %q", component.Designator, sym.SheetID), sym.ComponentID)
			continue
		case seenUnits[unitKey]:
			add("SCH_SYMBOL_DUPLICATE", "error", fmt.Sprintf("Symbol %s unit %d is placed more than once", component.Designator, sym.Unit), sym.ComponentID)
			continue
		}
		seenUnits[unitKey] = true
		if err := checkSymbolOrientation(sym); err != nil {
			add("SCH_SYMBOL_ORIENTATION_INVALID", "error", fmt.Sprintf("Symbol %s: %v", component.Designator, err), sym.ComponentID)
			continue
		}
		symDef, pinIDs, err := resolveSchematicSymbol(spec, sym.SymbolID, component)
		if err != nil {
			add("SCH_SYMBOL_UNRESOLVED", "error", fmt.Sprintf("Symbol %s: %v", component.Designator, err), sym.ComponentID)
			continue
		}
		conn.SymbolDefs[si], conn.ValidSymbolMask[si] = symDef, true
		conn.PlacedSymbols[sym.ComponentID] = true
		types := map[string]string{}
		for _, pin := range component.Pins {
			types[pin.ID] = pin.ElectricalType
		}
		for _, pin := range symDef.Pins {
			if !symbolPinActive(pin, sym.Unit) {
				continue
			}
			x, y := schematicTransform(sym, pin.XNM, pin.YNM)
			pinID := pinIDs[pin.Number]
			electrical := pin.ElectricalType
			if pinID != "" && types[pinID] != "" {
				electrical = types[pinID]
			}
			conn.Pins = append(conn.Pins, schPin{Symbol: si, ComponentID: sym.ComponentID, Number: pin.Number, PinID: pinID, ElectricalType: electrical, SheetID: sym.SheetID, XNM: x, YNM: y})
			node := uf.add()
			pinNodes = append(pinNodes, node)
			uf.union(node, point(sym.SheetID, x, y))
			pointPins[schPointKey{sym.SheetID, x, y}]++
		}
	}

	// Wires and their segment index.
	wireNodes := make([]int, len(spec.Wires))
	horizontal := map[string][]schSegment{}
	vertical := map[string][]schSegment{}
	diagonal := map[string][]schSegment{}
	wireIDs := map[string]bool{}
	validWire := make([]bool, len(spec.Wires))
	for wi, wire := range spec.Wires {
		wireNodes[wi] = -1
		if !idValid(wire.ID) || wireIDs[wire.ID] {
			add("SCH_WIRE_INVALID", "error", "Schematic wire has an invalid or duplicated ID", wire.ID)
			continue
		}
		wireIDs[wire.ID] = true
		if len(wire.Points) < 2 {
			add("SCH_WIRE_INVALID", "error", "Schematic wire needs at least two points", wire.ID)
			continue
		}
		if !sheets[wire.SheetID] {
			add("SCH_SHEET_UNKNOWN", "error", fmt.Sprintf("Wire %s is on unknown sheet %q", wire.ID, wire.SheetID), wire.ID)
			continue
		}
		validWire[wi] = true
		node := uf.add()
		wireNodes[wi] = node
		for i, p := range wire.Points {
			uf.union(node, point(wire.SheetID, p.XNM, p.YNM))
			if i == 0 {
				continue
			}
			seg := schSegment{Wire: wi, A: wire.Points[i-1], B: p}
			key := wire.SheetID
			switch {
			case seg.A.YNM == seg.B.YNM:
				k := fmt.Sprintf("%s\x00%d", key, seg.A.YNM)
				horizontal[k] = append(horizontal[k], seg)
			case seg.A.XNM == seg.B.XNM:
				k := fmt.Sprintf("%s\x00%d", key, seg.A.XNM)
				vertical[k] = append(vertical[k], seg)
			default:
				diagonal[key] = append(diagonal[key], seg)
			}
		}
	}
	for _, j := range spec.Junctions {
		if !sheets[j.SheetID] {
			add("SCH_SHEET_UNKNOWN", "error", "Junction is on an unknown sheet", j.ID)
			continue
		}
		point(j.SheetID, j.XNM, j.YNM)
		pointJunctions[schPointKey{j.SheetID, j.XNM, j.YNM}]++
	}

	// Labels.
	netIDs := map[string]bool{}
	for _, net := range def.Nets {
		netIDs[net.ID] = true
	}
	names := map[string]int{}
	labelIDs := map[string]bool{}
	labelNodes := make([]int, len(spec.Labels))
	for li, label := range spec.Labels {
		labelNodes[li] = -1
		if !idValid(label.ID) || labelIDs[label.ID] {
			add("SCH_LABEL_INVALID", "error", "Schematic label has an invalid or duplicated ID", label.ID)
			continue
		}
		labelIDs[label.ID] = true
		if strings.TrimSpace(label.Text) == "" || (label.Kind != "local" && label.Kind != "global" && label.Kind != "power") {
			add("SCH_LABEL_INVALID", "error", "Schematic label needs text and a local, global, or power kind", label.ID)
			continue
		}
		if !sheets[label.SheetID] {
			add("SCH_SHEET_UNKNOWN", "error", fmt.Sprintf("Label %s is on unknown sheet %q", label.ID, label.SheetID), label.ID)
			continue
		}
		if label.NetID != "" && !netIDs[label.NetID] {
			add("SCH_LABEL_NET_UNKNOWN", "warning", fmt.Sprintf("Label %q references unknown net %q", label.Text, label.NetID), label.ID)
		}
		scope := "g\x00" + label.Text
		if label.Kind == "local" {
			scope = "l\x00" + label.SheetID + "\x00" + label.Text
		}
		nameNode, ok := names[scope]
		if !ok {
			nameNode = uf.add()
			names[scope] = nameNode
		}
		node := uf.add()
		labelNodes[li] = node
		uf.union(node, nameNode)
		uf.union(node, point(label.SheetID, label.XNM, label.YNM))
		pointLabels[schPointKey{label.SheetID, label.XNM, label.YNM}]++
	}

	// T-connections: any connectable point lying on a wire joins that wire.
	touching := map[schPointKey][]int{}
	keys := make([]schPointKey, 0, len(points))
	for key := range points {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].Sheet != keys[j].Sheet {
			return keys[i].Sheet < keys[j].Sheet
		}
		if keys[i].X != keys[j].X {
			return keys[i].X < keys[j].X
		}
		return keys[i].Y < keys[j].Y
	})
	for _, key := range keys {
		p := Point{XNM: key.X, YNM: key.Y}
		seen := map[int]bool{}
		check := func(segs []schSegment) {
			for _, seg := range segs {
				if !seen[seg.Wire] && onSegment(p, seg.A, seg.B) {
					seen[seg.Wire] = true
					uf.union(points[key], wireNodes[seg.Wire])
					touching[key] = append(touching[key], seg.Wire)
				}
			}
		}
		check(horizontal[fmt.Sprintf("%s\x00%d", key.Sheet, key.Y)])
		check(vertical[fmt.Sprintf("%s\x00%d", key.Sheet, key.X)])
		check(diagonal[key.Sheet])
	}

	// Dangling wire ends and labels.
	for wi, wire := range spec.Wires {
		if !validWire[wi] {
			continue
		}
		for _, end := range []Point{wire.Points[0], wire.Points[len(wire.Points)-1]} {
			key := schPointKey{wire.SheetID, end.XNM, end.YNM}
			others := 0
			for _, other := range touching[key] {
				if other != wi {
					others++
				}
			}
			if others == 0 && pointPins[key] == 0 && pointLabels[key] == 0 && pointJunctions[key] == 0 {
				conn.DanglingWires = append(conn.DanglingWires, schDangling{WireID: wire.ID, XNM: end.XNM, YNM: end.YNM})
			}
		}
	}
	for li, label := range spec.Labels {
		if labelNodes[li] < 0 {
			continue
		}
		key := schPointKey{label.SheetID, label.XNM, label.YNM}
		if len(touching[key]) == 0 && pointPins[key] == 0 {
			conn.DanglingLabels = append(conn.DanglingLabels, li)
		}
	}

	// Groups, in deterministic first-seen order (pins, then labels).
	groupOf := map[int]int{}
	group := func(node int) int {
		root := uf.find(node)
		if g, ok := groupOf[root]; ok {
			return g
		}
		groupOf[root] = len(conn.Groups)
		conn.Groups = append(conn.Groups, schGroup{})
		return groupOf[root]
	}
	conn.PinGroup = make([]int, len(conn.Pins))
	for i, node := range pinNodes {
		g := group(node)
		conn.PinGroup[i] = g
		conn.Groups[g].Pins = append(conn.Groups[g].Pins, i)
	}
	conn.LabelGroup = make([]int, len(spec.Labels))
	for li, node := range labelNodes {
		conn.LabelGroup[li] = -1
		if node < 0 {
			continue
		}
		g := group(node)
		conn.LabelGroup[li] = g
		conn.Groups[g].Labels = append(conn.Groups[g].Labels, li)
	}
	rank := map[string]int{"power": 0, "global": 1, "local": 2}
	for gi := range conn.Groups {
		labels := conn.Groups[gi].Labels
		best := -1
		for _, li := range labels {
			l := spec.Labels[li]
			if best < 0 || rank[l.Kind] < rank[spec.Labels[best].Kind] || rank[l.Kind] == rank[spec.Labels[best].Kind] && l.Text < spec.Labels[best].Text {
				best = li
			}
		}
		if best >= 0 {
			conn.Groups[gi].Name = spec.Labels[best].Text
		}
	}
	return conn
}

func nodeKey(componentID, pinID string) string { return componentID + "\x00" + pinID }

// groupNodes returns the sorted component pin nodes in a group.
func (c *schConnectivity) groupNodes(g int) []Node {
	seen := map[string]bool{}
	out := []Node{}
	for _, pi := range c.Groups[g].Pins {
		pin := c.Pins[pi]
		if pin.PinID == "" || seen[nodeKey(pin.ComponentID, pin.PinID)] {
			continue
		}
		seen[nodeKey(pin.ComponentID, pin.PinID)] = true
		out = append(out, Node{ComponentID: pin.ComponentID, PinID: pin.PinID})
	}
	sortNodes(out)
	return out
}

func sortNodes(nodes []Node) {
	sort.Slice(nodes, func(i, j int) bool {
		if nodes[i].ComponentID != nodes[j].ComponentID {
			return nodes[i].ComponentID < nodes[j].ComponentID
		}
		return nodes[i].PinID < nodes[j].PinID
	})
}

type SchematicNetCheck struct {
	NetID         string `json:"net_id"`
	Name          string `json:"name"`
	Status        string `json:"status"` // ok | missing_connections | extra_connections | mismatch
	SchematicName string `json:"schematic_name,omitempty"`
	MissingNodes  []Node `json:"missing_nodes,omitempty"`
	ExtraNodes    []Node `json:"extra_nodes,omitempty"`
}

type SchematicExtraGroup struct {
	SchematicName string `json:"schematic_name,omitempty"`
	Nodes         []Node `json:"nodes"`
}

type SchematicERCMetrics struct {
	Sheets        int `json:"sheets"`
	Symbols       int `json:"symbols"`
	Wires         int `json:"wires"`
	Labels        int `json:"labels"`
	Junctions     int `json:"junctions"`
	SchematicNets int `json:"schematic_nets"`
	PlacedPins    int `json:"placed_pins"`
}

type SchematicERC struct {
	Schema      string                `json:"schema"`
	Engine      string                `json:"engine"`
	Present     bool                  `json:"present"`
	Status      string                `json:"status"` // passed | warning | failed | absent
	Errors      int                   `json:"errors"`
	Warnings    int                   `json:"warnings"`
	Checks      []Check               `json:"checks"`
	Nets        []SchematicNetCheck   `json:"nets"`
	ExtraGroups []SchematicExtraGroup `json:"extra_groups"`
	Metrics     SchematicERCMetrics   `json:"metrics"`
}

// schematicChecks is the validateDefinition hook. It is a no-op for designs
// without a schematic so pre-schematic revisions keep their reports.
func schematicChecks(def *Definition) []Check {
	if def.Schematic == nil {
		return nil
	}
	return runSchematicERC(def).Checks
}

func runSchematicERC(def *Definition) SchematicERC {
	report := SchematicERC{Schema: schematicERCSchema, Engine: engineVersion, Status: "absent", Checks: []Check{}, Nets: []SchematicNetCheck{}, ExtraGroups: []SchematicExtraGroup{}}
	if def.Schematic == nil {
		return report
	}
	report.Present = true
	spec := def.Schematic
	conn := buildSchematicConnectivity(def)
	report.Checks = append(report.Checks, conn.Checks...)
	add := func(code, severity, message string, ids ...string) {
		report.Checks = append(report.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
	}
	designators := map[string]string{}
	for _, c := range def.Components {
		designators[c.ID] = c.Designator
		if !conn.PlacedSymbols[c.ID] {
			add("SCH_SYMBOL_MISSING", "warning", fmt.Sprintf("Component %s has no schematic symbol", firstNonEmpty(c.Designator, c.ID)), c.ID)
		}
	}
	nodeLabel := func(n Node) string { return firstNonEmpty(designators[n.ComponentID], n.ComponentID) + "." + n.PinID }
	nodeList := func(nodes []Node) string {
		parts := make([]string, len(nodes))
		for i, n := range nodes {
			parts[i] = nodeLabel(n)
		}
		return strings.Join(parts, ", ")
	}

	nodeGroups := map[string][]int{}
	for pi, pin := range conn.Pins {
		if pin.PinID == "" {
			continue
		}
		key := nodeKey(pin.ComponentID, pin.PinID)
		nodeGroups[key] = appendUniqueInt(nodeGroups[key], conn.PinGroup[pi])
	}
	nodeNet := map[string]string{}
	for _, net := range def.Nets {
		for _, n := range net.Nodes {
			if _, ok := nodeNet[nodeKey(n.ComponentID, n.PinID)]; !ok {
				nodeNet[nodeKey(n.ComponentID, n.PinID)] = net.ID
			}
		}
	}
	groupHasNet := map[int]bool{}
	for _, net := range def.Nets {
		check := SchematicNetCheck{NetID: net.ID, Name: net.Name, Status: "ok"}
		counts := map[int]int{}
		for _, n := range net.Nodes {
			for _, g := range nodeGroups[nodeKey(n.ComponentID, n.PinID)] {
				counts[g]++
			}
		}
		// The net's main group holds most of its nodes; ties go to the group
		// with fewer foreign pins, then the earliest group.
		foreign := func(g int) int {
			n := 0
			for _, node := range conn.groupNodes(g) {
				if nodeNet[nodeKey(node.ComponentID, node.PinID)] != net.ID {
					n++
				}
			}
			return n
		}
		main := -1
		for g, count := range counts {
			if main < 0 || count > counts[main] || count == counts[main] && (foreign(g) < foreign(main) || foreign(g) == foreign(main) && g < main) {
				main = g
			}
		}
		if main >= 0 {
			groupHasNet[main] = true
			check.SchematicName = conn.Groups[main].Name
			if len(net.Nodes) >= 2 {
				for _, n := range net.Nodes {
					if !containsInt(nodeGroups[nodeKey(n.ComponentID, n.PinID)], main) {
						check.MissingNodes = append(check.MissingNodes, n)
					}
				}
			}
			for _, n := range conn.groupNodes(main) {
				if nodeNet[nodeKey(n.ComponentID, n.PinID)] != net.ID {
					check.ExtraNodes = append(check.ExtraNodes, n)
				}
			}
		} else if len(net.Nodes) >= 2 {
			check.MissingNodes = append(check.MissingNodes, net.Nodes...)
		}
		sortNodes(check.MissingNodes)
		sortNodes(check.ExtraNodes)
		switch {
		case len(check.MissingNodes) > 0 && len(check.ExtraNodes) > 0:
			check.Status = "mismatch"
		case len(check.MissingNodes) > 0:
			check.Status = "missing_connections"
		case len(check.ExtraNodes) > 0:
			check.Status = "extra_connections"
		}
		if len(check.MissingNodes) > 0 {
			add("SCH_NET_MISSING_CONNECTION", "error", fmt.Sprintf("Net %s is not connected in the schematic to: %s", firstNonEmpty(net.Name, net.ID), nodeList(check.MissingNodes)), net.ID)
		}
		if len(check.ExtraNodes) > 0 {
			add("SCH_NET_EXTRA_CONNECTION", "error", fmt.Sprintf("Schematic connects net %s to pins outside it: %s", firstNonEmpty(net.Name, net.ID), nodeList(check.ExtraNodes)), net.ID)
		}
		report.Nets = append(report.Nets, check)
	}
	for g := range conn.Groups {
		if groupHasNet[g] {
			continue
		}
		nodes := conn.groupNodes(g)
		if len(nodes) < 2 {
			continue
		}
		hasNet := false
		for _, n := range nodes {
			if nodeNet[nodeKey(n.ComponentID, n.PinID)] != "" {
				hasNet = true
			}
		}
		if hasNet {
			continue // already reported as an extra connection of that net
		}
		report.ExtraGroups = append(report.ExtraGroups, SchematicExtraGroup{SchematicName: conn.Groups[g].Name, Nodes: nodes})
		ids := []string{}
		for _, n := range nodes {
			ids = append(ids, n.ComponentID)
		}
		add("SCH_NET_EXTRA_CONNECTION", "error", fmt.Sprintf("Schematic connects pins that share no net: %s", nodeList(nodes)), ids...)
	}
	for _, d := range conn.DanglingWires {
		add("SCH_WIRE_DANGLING", "warning", fmt.Sprintf("Wire %s has an unconnected end at (%s, %s) mm", d.WireID, trimFloat(nmMM(d.XNM)), trimFloat(nmMM(d.YNM))), d.WireID)
	}
	for _, li := range conn.DanglingLabels {
		add("SCH_LABEL_DANGLING", "warning", fmt.Sprintf("Label %q is not attached to a wire or pin", spec.Labels[li].Text), spec.Labels[li].ID)
	}
	for pi, pin := range conn.Pins {
		t := strings.ToLower(pin.ElectricalType)
		if pin.PinID == "" || (t != "input" && t != "power_in") {
			continue
		}
		g := conn.Groups[conn.PinGroup[pi]]
		if len(g.Pins) <= 1 && len(g.Labels) == 0 {
			add("SCH_PIN_INPUT_UNCONNECTED", "warning", fmt.Sprintf("Input pin %s is not connected in the schematic", nodeLabel(Node{ComponentID: pin.ComponentID, PinID: pin.PinID})), pin.ComponentID)
		}
	}
	report.Metrics = SchematicERCMetrics{Sheets: len(spec.Sheets), Symbols: len(spec.Symbols), Wires: len(spec.Wires), Labels: len(spec.Labels), Junctions: len(spec.Junctions), PlacedPins: len(conn.Pins)}
	for g := range conn.Groups {
		if len(conn.groupNodes(g)) >= 2 || len(conn.Groups[g].Labels) > 0 {
			report.Metrics.SchematicNets++
		}
	}
	for _, c := range report.Checks {
		if c.Severity == "error" {
			report.Errors++
		} else if c.Severity == "warning" {
			report.Warnings++
		}
	}
	switch {
	case report.Errors > 0:
		report.Status = "failed"
	case report.Warnings > 0:
		report.Status = "warning"
	default:
		report.Status = "passed"
	}
	return report
}

func appendUniqueInt(v []int, x int) []int {
	if containsInt(v, x) {
		return v
	}
	return append(v, x)
}

func containsInt(v []int, x int) bool {
	for _, y := range v {
		if y == x {
			return true
		}
	}
	return false
}

// SchematicNetComparison reconciles schematic connectivity with the board's
// net list by exact node-set equality.
type SchematicNetComparison struct {
	Status          string              `json:"status"` // match | mismatch
	Matching        []SchematicNetMatch `json:"matching"`
	OnlyInSchematic []SchematicNetRef   `json:"only_in_schematic"`
	OnlyOnBoard     []SchematicNetRef   `json:"only_on_board"`
}

type SchematicNetMatch struct {
	NetID         string `json:"net_id"`
	Name          string `json:"name"`
	SchematicName string `json:"schematic_name,omitempty"`
	Nodes         []Node `json:"nodes"`
}

type SchematicNetRef struct {
	NetID string `json:"net_id,omitempty"`
	Name  string `json:"name,omitempty"`
	Nodes []Node `json:"nodes"`
}

func nodeSetKey(nodes []Node) string {
	sorted := append([]Node(nil), nodes...)
	sortNodes(sorted)
	parts := make([]string, len(sorted))
	for i, n := range sorted {
		parts[i] = nodeKey(n.ComponentID, n.PinID)
	}
	return strings.Join(parts, "\x01")
}

func compareSchematicNets(def *Definition, conn *schConnectivity) SchematicNetComparison {
	out := SchematicNetComparison{Status: "match", Matching: []SchematicNetMatch{}, OnlyInSchematic: []SchematicNetRef{}, OnlyOnBoard: []SchematicNetRef{}}
	byKey := map[string]int{}
	for g := range conn.Groups {
		nodes := conn.groupNodes(g)
		if len(nodes) == 0 {
			continue
		}
		key := nodeSetKey(nodes)
		if _, ok := byKey[key]; !ok {
			byKey[key] = g
		}
	}
	used := map[int]bool{}
	for _, net := range def.Nets {
		if len(net.Nodes) == 0 {
			continue
		}
		nodes := append([]Node(nil), net.Nodes...)
		sortNodes(nodes)
		if g, ok := byKey[nodeSetKey(nodes)]; ok && !used[g] {
			used[g] = true
			out.Matching = append(out.Matching, SchematicNetMatch{NetID: net.ID, Name: net.Name, SchematicName: conn.Groups[g].Name, Nodes: nodes})
			continue
		}
		out.OnlyOnBoard = append(out.OnlyOnBoard, SchematicNetRef{NetID: net.ID, Name: net.Name, Nodes: nodes})
	}
	for g := range conn.Groups {
		nodes := conn.groupNodes(g)
		if used[g] || len(nodes) == 0 || len(nodes) < 2 && len(conn.Groups[g].Labels) == 0 {
			continue
		}
		out.OnlyInSchematic = append(out.OnlyInSchematic, SchematicNetRef{Name: conn.Groups[g].Name, Nodes: nodes})
	}
	if len(out.OnlyInSchematic) > 0 || len(out.OnlyOnBoard) > 0 {
		out.Status = "mismatch"
	}
	return out
}

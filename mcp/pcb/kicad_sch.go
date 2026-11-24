package main

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

const kicadSchematicImportSchema = "apteva-pcb-kicad-schematic-import/v1"

// KiCadSchematicImportReport describes how a .kicad_sch maps onto a design.
type KiCadSchematicImportReport struct {
	Schema           string                 `json:"schema"`
	Engine           string                 `json:"engine"`
	Status           string                 `json:"status"` // match | mismatch
	NetsCreated      bool                   `json:"nets_created"`
	Comparison       SchematicNetComparison `json:"comparison"`
	MatchedSymbols   []string               `json:"matched_symbols"`
	UnmatchedSymbols []string               `json:"unmatched_symbols"`
	UnmatchedPins    []string               `json:"unmatched_pins"`
	Counts           map[string]int         `json:"counts"`
	Warnings         []string               `json:"warnings"`
}

type kicadSchPlaced struct {
	LibID     string
	Reference string
	Value     string
	Sym       SchematicSymbol
}

type kicadSchDoc struct {
	Spec      SchematicSpec
	Library   map[string]SchematicSymbolDef
	Placed    []kicadSchPlaced
	NoConnect int
	Buses     int
}

// parseKiCadSchematic reads a single-sheet KiCad 6/7/8 .kicad_sch file. Power
// symbols become power labels named by their Value; every other symbol keeps
// its embedded library definition.
func parseKiCadSchematic(body []byte) (*kicadSchDoc, error) {
	root, err := parseSExpressions(string(body))
	if err != nil {
		return nil, err
	}
	if root.atom() != "kicad_sch" {
		return nil, errors.New("KiCad schematic needs a kicad_sch root")
	}
	if len(root.children("sheet")) > 0 {
		return nil, errors.New("hierarchical KiCad schematics (sheet blocks) are not supported; flatten the design to a single sheet first")
	}
	sheet := defaultSchematicSheet()
	if paper := root.child("paper"); paper != nil {
		size := paper.value(1)
		if w, h, ok := paperSizeNM(size); ok {
			sheet.Size, sheet.WidthNM, sheet.HeightNM = strings.ToUpper(size), w, h
			if paper.value(2) == "portrait" {
				sheet.WidthNM, sheet.HeightNM = h, w
			}
		} else if strings.EqualFold(size, "User") {
			sheet.Size, sheet.WidthNM, sheet.HeightNM = "custom", mmToNM(paper.number(2)), mmToNM(paper.number(3))
		}
	}
	if tb := root.child("title_block"); tb != nil {
		if title := tb.child("title"); title != nil && strings.TrimSpace(title.value(1)) != "" {
			sheet.Title = title.value(1)
		}
	}
	doc := &kicadSchDoc{Library: map[string]SchematicSymbolDef{}}
	doc.Spec = SchematicSpec{Schema: schematicSchema, GridNM: schematicGridNM, Source: "kicad_sch", Sheets: []SchematicSheet{sheet}, Symbols: []SchematicSymbol{}, Wires: []SchematicWire{}, Labels: []SchematicLabel{}, Junctions: []SchematicJunction{}}
	if lib := root.child("lib_symbols"); lib != nil {
		for _, node := range lib.children("symbol") {
			def := kicadLibSymbol(node)
			doc.Library[node.value(1)] = def
		}
	}
	at := func(n *sexpr) (int64, int64, int) {
		a := n.child("at")
		return mmToNM(a.number(1)), mmToNM(a.number(2)), int(mathRound(a.number(3)))
	}
	for i, node := range root.children("wire") {
		pts := node.child("pts")
		wire := SchematicWire{ID: fmt.Sprintf("w-%d", i+1), SheetID: sheet.ID}
		for _, xy := range pts.children("xy") {
			wire.Points = append(wire.Points, Point{XNM: mmToNM(xy.number(1)), YNM: mmToNM(xy.number(2))})
		}
		if len(wire.Points) >= 2 {
			doc.Spec.Wires = append(doc.Spec.Wires, wire)
		}
	}
	doc.Buses = len(root.children("bus"))
	doc.NoConnect = len(root.children("no_connect"))
	for i, node := range root.children("junction") {
		x, y, _ := at(node)
		doc.Spec.Junctions = append(doc.Spec.Junctions, SchematicJunction{ID: fmt.Sprintf("j-%d", i+1), SheetID: sheet.ID, XNM: x, YNM: y})
	}
	labelIndex := 0
	addLabel := func(node *sexpr, kind string) {
		x, y, rot := at(node)
		labelIndex++
		doc.Spec.Labels = append(doc.Spec.Labels, SchematicLabel{ID: fmt.Sprintf("l-%d", labelIndex), SheetID: sheet.ID, Text: node.value(1), Kind: kind, XNM: x, YNM: y, Rotation: rot})
	}
	for _, node := range root.children("label") {
		addLabel(node, "local")
	}
	for _, node := range root.children("global_label") {
		addLabel(node, "global")
	}
	// A hierarchical label on a single (root) sheet behaves like a local label.
	for _, node := range root.children("hierarchical_label") {
		addLabel(node, "local")
	}
	for _, node := range root.children("symbol") {
		libID := node.child("lib_id").value(1)
		if libID == "" {
			continue
		}
		x, y, rot := at(node)
		sym := SchematicSymbol{SymbolID: libID, SheetID: sheet.ID, XNM: x, YNM: y, Rotation: ((rot % 360) + 360) % 360, Unit: 1}
		if unit := node.child("unit"); unit != nil {
			sym.Unit = max(int(unit.number(1)), 1)
		}
		if mirror := node.child("mirror"); mirror != nil {
			sym.Mirror = mirror.value(1)
		}
		reference, value := "", ""
		for _, prop := range node.children("property") {
			switch prop.value(1) {
			case "Reference":
				reference = prop.value(2)
			case "Value":
				value = prop.value(2)
			}
		}
		if reference == "" {
			reference = kicadInstanceReference(node)
		}
		def, ok := doc.Library[libID]
		if ok && def.Power {
			// Power ports connect globally by name at their pin.
			hasPowerIn := false
			for _, pin := range def.Pins {
				if pin.ElectricalType == "power_in" {
					hasPowerIn = true
				}
			}
			if !hasPowerIn || strings.TrimSpace(value) == "" {
				continue // PWR_FLAG and similar markers carry no net name
			}
			for _, pin := range def.Pins {
				if !symbolPinActive(pin, sym.Unit) {
					continue
				}
				px, py := schematicTransform(sym, pin.XNM, pin.YNM)
				labelIndex++
				doc.Spec.Labels = append(doc.Spec.Labels, SchematicLabel{ID: fmt.Sprintf("l-%d", labelIndex), SheetID: sheet.ID, Text: value, Kind: "power", XNM: px, YNM: py, Rotation: (sym.Rotation + 90) % 360})
				break
			}
			continue
		}
		doc.Placed = append(doc.Placed, kicadSchPlaced{LibID: libID, Reference: reference, Value: value, Sym: sym})
	}
	return doc, nil
}

func kicadInstanceReference(node *sexpr) string {
	instances := node.child("instances")
	if instances == nil {
		return ""
	}
	for _, project := range instances.children("project") {
		for _, path := range project.children("path") {
			if ref := path.child("reference"); ref != nil {
				return ref.value(1)
			}
		}
	}
	return ""
}

// kicadLibSymbol converts a lib_symbols entry to native symbol-local screen
// coordinates (KiCad library space is +Y up).
func kicadLibSymbol(node *sexpr) SchematicSymbolDef {
	def := SchematicSymbolDef{ID: node.value(1), Kind: "kicad", Pins: []SchematicSymbolPin{}}
	def.Power = node.child("power") != nil
	def.PinNamesHidden = kicadHidden(node.child("pin_names"))
	def.PinNumbersHidden = kicadHidden(node.child("pin_numbers"))
	var minX, minY, maxX, maxY int64
	have := false
	extend := func(x, y int64) {
		if !have {
			minX, minY, maxX, maxY, have = x, y, x, y, true
			return
		}
		minX, minY, maxX, maxY = min64(minX, x), min64(minY, y), max64(maxX, x), max64(maxY, y)
	}
	units := 1
	for _, sub := range node.children("symbol") {
		unit, style := kicadUnitSuffix(sub.value(1))
		if style > 1 {
			continue // alternate De Morgan body style
		}
		units = max(units, unit)
		for _, shape := range sub.List {
			switch shape.atom() {
			case "rectangle":
				s, e := shape.child("start"), shape.child("end")
				extend(mmToNM(s.number(1)), -mmToNM(s.number(2)))
				extend(mmToNM(e.number(1)), -mmToNM(e.number(2)))
			case "polyline":
				if pts := shape.child("pts"); pts != nil {
					for _, xy := range pts.children("xy") {
						extend(mmToNM(xy.number(1)), -mmToNM(xy.number(2)))
					}
				}
			case "circle":
				c := shape.child("center")
				r := mmToNM(shape.child("radius").number(1))
				cx, cy := mmToNM(c.number(1)), -mmToNM(c.number(2))
				extend(cx-r, cy-r)
				extend(cx+r, cy+r)
			case "pin":
				a := shape.child("at")
				pin := SchematicSymbolPin{ElectricalType: shape.value(1), XNM: mmToNM(a.number(1)), YNM: -mmToNM(a.number(2)), AngleDeg: int(mathRound(a.number(3))), LengthNM: mmToNM(shape.child("length").number(1)), Unit: unit}
				if name := shape.child("name"); name != nil {
					pin.Name = name.value(1)
					if pin.Name == "~" {
						pin.Name = ""
					}
				}
				if number := shape.child("number"); number != nil {
					pin.Number = number.value(1)
				}
				def.Pins = append(def.Pins, pin)
				dx, dy := pinDirection(pin.AngleDeg)
				extend(pin.XNM+dx*pin.LengthNM, pin.YNM+dy*pin.LengthNM)
			}
		}
	}
	if !have {
		minX, minY, maxX, maxY = -schematicGridNM, -schematicGridNM, schematicGridNM, schematicGridNM
	}
	def.Body = SchematicRect{X1NM: minX, Y1NM: minY, X2NM: maxX, Y2NM: maxY}
	if units > 1 {
		def.Units = units
	}
	sort.SliceStable(def.Pins, func(i, j int) bool {
		if def.Pins[i].Unit != def.Pins[j].Unit {
			return def.Pins[i].Unit < def.Pins[j].Unit
		}
		return def.Pins[i].Number < def.Pins[j].Number
	})
	return def
}

// kicadHidden reports a bare "hide" token (KiCad 6/7) or "(hide yes)" (KiCad 8).
func kicadHidden(node *sexpr) bool {
	if node == nil {
		return false
	}
	for _, item := range node.List[1:] {
		if item.atom() == "hide" {
			return len(item.List) == 0 || item.value(1) != "no"
		}
	}
	return false
}

// kicadUnitSuffix parses the "_<unit>_<style>" suffix of a sub-symbol name.
func kicadUnitSuffix(name string) (int, int) {
	parts := strings.Split(name, "_")
	if len(parts) < 3 {
		return 0, 1
	}
	unit, err1 := strconv.Atoi(parts[len(parts)-2])
	style, err2 := strconv.Atoi(parts[len(parts)-1])
	if err1 != nil || err2 != nil {
		return 0, 1
	}
	return unit, style
}

// importKiCadSchematic attaches a parsed .kicad_sch to a copy of def and
// returns the operations that create the next revision plus the comparison
// report. When replaceNets is set the net list is rebuilt from schematic
// connectivity; nets whose node sets match keep their board IDs.
func importKiCadSchematic(def *Definition, body []byte, replaceNets bool) ([]Operation, *KiCadSchematicImportReport, error) {
	doc, err := parseKiCadSchematic(body)
	if err != nil {
		return nil, nil, err
	}
	report := &KiCadSchematicImportReport{Schema: kicadSchematicImportSchema, Engine: engineVersion, MatchedSymbols: []string{}, UnmatchedSymbols: []string{}, UnmatchedPins: []string{}, Warnings: []string{}, Counts: map[string]int{}}
	byDesignator := map[string]*Component{}
	for i := range def.Components {
		byDesignator[def.Components[i].Designator] = &def.Components[i]
	}
	spec := doc.Spec
	usedLib := map[string]bool{}
	for _, placed := range doc.Placed {
		component := byDesignator[placed.Reference]
		if component == nil {
			if !strings.HasPrefix(placed.Reference, "#") {
				report.UnmatchedSymbols = append(report.UnmatchedSymbols, firstNonEmpty(placed.Reference, placed.LibID))
			}
			continue
		}
		libDef, ok := doc.Library[placed.LibID]
		if !ok {
			report.Warnings = append(report.Warnings, fmt.Sprintf("symbol %s references lib_id %q that is missing from lib_symbols", placed.Reference, placed.LibID))
			report.UnmatchedSymbols = append(report.UnmatchedSymbols, placed.Reference)
			continue
		}
		if !idValid(libDef.ID) {
			return nil, nil, fmt.Errorf("KiCad lib_id %q is not a valid native identifier", libDef.ID)
		}
		sym := placed.Sym
		sym.ComponentID = component.ID
		_, pinIDs, _ := resolveSchematicSymbol(&SchematicSpec{LibrarySymbols: []SchematicSymbolDef{libDef}}, libDef.ID, component)
		for _, pin := range libDef.Pins {
			if symbolPinActive(pin, sym.Unit) && pinIDs[pin.Number] == "" {
				report.UnmatchedPins = append(report.UnmatchedPins, placed.Reference+":"+pin.Number)
			}
		}
		sym.Pins = symbolAnchors(sym, libDef, pinIDs)
		spec.Symbols = append(spec.Symbols, sym)
		report.MatchedSymbols = append(report.MatchedSymbols, placed.Reference)
		usedLib[libDef.ID] = true
	}
	ids := make([]string, 0, len(usedLib))
	for id := range usedLib {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		spec.LibrarySymbols = append(spec.LibrarySymbols, doc.Library[id])
	}
	sort.Strings(report.MatchedSymbols)
	sort.Strings(report.UnmatchedSymbols)
	sort.Strings(report.UnmatchedPins)
	if doc.Buses > 0 {
		report.Warnings = append(report.Warnings, fmt.Sprintf("%d bus segments ignored; buses are not supported", doc.Buses))
	}
	report.Counts = map[string]int{"symbols": len(doc.Placed), "matched_symbols": len(report.MatchedSymbols), "wires": len(spec.Wires), "labels": len(spec.Labels), "junctions": len(spec.Junctions), "no_connects": doc.NoConnect, "library_symbols": len(spec.LibrarySymbols)}

	candidate := *def
	candidate.Schematic = &spec
	normalizeSchematic(candidate.Schematic)
	conn := buildSchematicConnectivity(&candidate)
	ops := []Operation{{Type: "schematic.set", Schematic: candidate.Schematic}}
	if replaceNets {
		netOps, err := schematicNetOperations(def, conn)
		if err != nil {
			return nil, nil, err
		}
		ops = append(ops, netOps...)
		report.NetsCreated = len(netOps) > 0
		next := candidate
		next.Nets = schematicNetsAfter(def, netOps)
		report.Comparison = compareSchematicNets(&next, conn)
	} else {
		report.Comparison = compareSchematicNets(&candidate, conn)
	}
	report.Status = report.Comparison.Status
	return ops, report, nil
}

// schematicNetOperations rebuilds the net list from connectivity. Existing
// nets with an identical node set are kept; removed nets must not carry copper.
func schematicNetOperations(def *Definition, conn *schConnectivity) ([]Operation, error) {
	existing := map[string]Net{}
	for _, net := range def.Nets {
		existing[nodeSetKey(net.Nodes)] = net
	}
	keep := map[string]bool{}
	ids := map[string]bool{}
	for _, net := range def.Nets {
		ids[net.ID] = true
	}
	designators := map[string]string{}
	for _, c := range def.Components {
		designators[c.ID] = c.Designator
	}
	type groupNet struct {
		name  string
		nodes []Node
	}
	groups := []groupNet{}
	for g := range conn.Groups {
		nodes := conn.groupNodes(g)
		if len(nodes) == 0 || len(nodes) < 2 && len(conn.Groups[g].Labels) == 0 {
			continue
		}
		groups = append(groups, groupNet{name: conn.Groups[g].Name, nodes: nodes})
	}
	sort.SliceStable(groups, func(i, j int) bool { return nodeSetKey(groups[i].nodes) < nodeSetKey(groups[j].nodes) })
	added := []Operation{}
	usedNames := map[string]bool{}
	for _, gn := range groups {
		if net, ok := existing[nodeSetKey(gn.nodes)]; ok && !keep[net.ID] {
			keep[net.ID] = true
			continue
		}
		name := gn.name
		if name == "" || usedNames[name] {
			first := gn.nodes[0]
			name = fmt.Sprintf("Net-(%s-%s)", firstNonEmpty(designators[first.ComponentID], first.ComponentID), first.PinID)
		}
		usedNames[name] = true
		base := kicadSafeNativeID(strings.ToLower(name), "net")
		if len(base) > 100 {
			base = base[:100]
		}
		id := base
		for n := 2; ids[id]; n++ {
			id = fmt.Sprintf("%s-%d", base, n)
		}
		ids[id] = true
		added = append(added, Operation{Type: "net.add", Net: &Net{ID: id, Name: name, Nodes: gn.nodes}})
	}
	removed := []Operation{}
	for _, net := range def.Nets {
		if keep[net.ID] {
			continue
		}
		if netCarriesCopper(def, net.ID) {
			return nil, fmt.Errorf("net %q carries routed copper or constraints but does not exist in the schematic; remove its routing first or import without replace_nets", firstNonEmpty(net.Name, net.ID))
		}
		removed = append(removed, Operation{Type: "net.remove", NetID: net.ID})
	}
	return append(removed, added...), nil
}

func netCarriesCopper(def *Definition, netID string) bool {
	for _, t := range def.Traces {
		if t.NetID == netID {
			return true
		}
	}
	for _, v := range def.Vias {
		if v.NetID == netID {
			return true
		}
	}
	for _, z := range def.Zones {
		if z.NetID == netID {
			return true
		}
	}
	for _, p := range def.DifferentialPairs {
		if p.PositiveNetID == netID || p.NegativeNetID == netID {
			return true
		}
	}
	for _, c := range def.NetClasses {
		if containsString(c.NetIDs, netID) {
			return true
		}
	}
	return false
}

func schematicNetsAfter(def *Definition, ops []Operation) []Net {
	removed := map[string]bool{}
	out := []Net{}
	for _, op := range ops {
		if op.Type == "net.remove" {
			removed[op.NetID] = true
		}
	}
	for _, net := range def.Nets {
		if !removed[net.ID] {
			out = append(out, net)
		}
	}
	for _, op := range ops {
		if op.Type == "net.add" && op.Net != nil {
			out = append(out, *op.Net)
		}
	}
	return out
}

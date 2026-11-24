package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

var (
	powerNetPrefix  = regexp.MustCompile(`^(GND|AGND|DGND|PGND|SGND|GNDA|GNDD|GNDPWR|VSS|VSSA|VCC|VCCA|VDD|VDDA|VDDIO|VEE|VBUS|VBAT|VSYS|VIN|VMOT)[A-Z0-9]*$`)
	powerNetVoltage = regexp.MustCompile(`^[0-9]+(V[0-9]*|\.[0-9]+V|P[0-9]+V)$`)
)

// isPowerNetName recognises rail names such as GND, VCC, +3V3, 5V, USB_5V,
// 3.3V and VDD_IO.
func isPowerNetName(name string) bool {
	upper := strings.ToUpper(strings.TrimSpace(name))
	upper = strings.TrimLeft(upper, "+-")
	if upper == "" {
		return false
	}
	tokens := strings.FieldsFunc(upper, func(r rune) bool { return r == '_' || r == '-' || r == ' ' || r == '/' })
	if len(tokens) == 0 {
		return false
	}
	if powerNetPrefix.MatchString(tokens[0]) {
		return true
	}
	for _, token := range tokens {
		if powerNetVoltage.MatchString(strings.TrimLeft(token, "+-")) {
			return true
		}
	}
	return false
}

// schematicNetLabelText picks a stable, unique label text for each net: the
// net name, or the net ID when the name is empty or shared with another net.
func schematicNetLabelText(nets []Net) map[string]string {
	counts := map[string]int{}
	for _, net := range nets {
		counts[strings.TrimSpace(net.Name)]++
	}
	ids := map[string]bool{}
	for _, net := range nets {
		ids[net.ID] = true
	}
	out := map[string]string{}
	for _, net := range nets {
		name := strings.TrimSpace(net.Name)
		if name == "" || counts[name] > 1 || (ids[name] && name != net.ID) {
			name = net.ID
		}
		out[net.ID] = name
	}
	return out
}

type genItem struct {
	Index    int
	Symbol   string
	Def      SchematicSymbolDef
	PinIDs   map[string]string
	Left     int64
	Right    int64
	Top      int64
	Bottom   int64
	Attached []int
}

func labelExtentNM(text string) int64 {
	return int64(len([]rune(text))+3) * schematicGridNM
}

// generateSchematic places one symbol per component on a tidy grid and ties
// every net with labels (power flags for rails), so its ERC matches the net
// list by construction. It never routes wires.
func generateSchematic(def *Definition) *SchematicSpec {
	g := schematicGridNM
	spec := &SchematicSpec{Schema: schematicSchema, GridNM: g, Source: "generated", Sheets: []SchematicSheet{}, Symbols: []SchematicSymbol{}, Wires: []SchematicWire{}, Labels: []SchematicLabel{}, Junctions: []SchematicJunction{}}
	labelText := schematicNetLabelText(def.Nets)
	power := map[string]bool{}
	nodeNet := map[string]string{}
	for _, net := range def.Nets {
		power[net.ID] = isPowerNetName(net.Name) || isPowerNetName(net.ID) && strings.TrimSpace(net.Name) == ""
		for _, n := range net.Nodes {
			if _, ok := nodeNet[nodeKey(n.ComponentID, n.PinID)]; !ok {
				nodeNet[nodeKey(n.ComponentID, n.PinID)] = net.ID
			}
		}
	}

	items := make([]*genItem, len(def.Components))
	for i := range def.Components {
		c := &def.Components[i]
		id := defaultSymbolFor(*c)
		symDef, pinIDs, err := resolveSchematicSymbol(nil, id, c)
		if err != nil {
			id = symIC
			symDef, pinIDs, _ = resolveSchematicSymbol(nil, id, c)
		}
		item := &genItem{Index: i, Symbol: id, Def: symDef, PinIDs: pinIDs}
		item.Left, item.Right = symDef.Body.X1NM, symDef.Body.X2NM
		item.Top, item.Bottom = symDef.Body.Y1NM-3*g, symDef.Body.Y2NM+3*g
		for _, pin := range symDef.Pins {
			ext := int64(0)
			if netID := nodeNet[nodeKey(c.ID, pinIDs[pin.Number])]; netID != "" && pinIDs[pin.Number] != "" {
				ext = labelExtentNM(labelText[netID])
				if power[netID] {
					ext = max64(ext, 4*g)
				}
			}
			dx, dy := pinDirection(pin.AngleDeg)
			// Labels extend away from the body.
			x, y := pin.XNM-dx*ext, pin.YNM-dy*ext
			item.Left, item.Right = min64(item.Left, min64(pin.XNM, x)), max64(item.Right, max64(pin.XNM, x))
			item.Top, item.Bottom = min64(item.Top, min64(pin.YNM, y)-g), max64(item.Bottom, max64(pin.YNM, y)+g)
		}
		items[i] = item
	}

	kind := func(it *genItem) string {
		switch it.Symbol {
		case symConnector:
			return "connector"
		case symIC:
			return "ic"
		}
		return "passive"
	}
	signalNets := func(it *genItem) map[string]bool {
		out := map[string]bool{}
		c := def.Components[it.Index]
		for _, pin := range c.Pins {
			if netID := nodeNet[nodeKey(c.ID, pin.ID)]; netID != "" && !power[netID] {
				out[netID] = true
			}
		}
		return out
	}
	anchors := []*genItem{}
	passives := []*genItem{}
	for _, it := range items {
		if kind(it) == "passive" {
			passives = append(passives, it)
		} else {
			anchors = append(anchors, it)
		}
	}
	// Attach each passive to the IC or connector sharing the most signal nets,
	// then let chains (R -> LED) follow the passive they connect to.
	owner := map[int]*genItem{}
	shared := func(a, b map[string]bool) int {
		n := 0
		for k := range a {
			if b[k] {
				n++
			}
		}
		return n
	}
	for _, p := range passives {
		nets := signalNets(p)
		var best *genItem
		bestScore := 0
		for _, a := range anchors {
			score := shared(nets, signalNets(a)) * 2
			if kind(a) == "ic" {
				score++ // prefer ICs on ties
			}
			if shared(nets, signalNets(a)) > 0 && score > bestScore {
				best, bestScore = a, score
			}
		}
		if best != nil {
			owner[p.Index] = best
		}
	}
	for changed := true; changed; {
		changed = false
		for _, p := range passives {
			if owner[p.Index] != nil {
				continue
			}
			nets := signalNets(p)
			for _, q := range passives {
				if o := owner[q.Index]; o != nil && shared(nets, signalNets(q)) > 0 {
					owner[p.Index] = o
					changed = true
					break
				}
			}
		}
	}
	free := []*genItem{}
	for _, p := range passives {
		if o := owner[p.Index]; o != nil {
			o.Attached = append(o.Attached, p.Index)
		} else {
			free = append(free, p)
		}
	}

	// Bands, left to right: connectors | their passives | ICs | their passives | free passives.
	byDesignator := func(list []*genItem) {
		sort.SliceStable(list, func(i, j int) bool {
			return designatorLess(def.Components[list[i].Index].Designator, def.Components[list[j].Index].Designator)
		})
	}
	connectors, ics := []*genItem{}, []*genItem{}
	for _, a := range anchors {
		if kind(a) == "connector" {
			connectors = append(connectors, a)
		} else {
			ics = append(ics, a)
		}
	}
	byDesignator(connectors)
	byDesignator(ics)
	byDesignator(free)
	for _, a := range anchors {
		sort.SliceStable(a.Attached, func(i, j int) bool {
			return designatorLess(def.Components[a.Attached[i]].Designator, def.Components[a.Attached[j]].Designator)
		})
	}
	margin := 20 * g
	gap := 6 * g
	snap := func(v int64) int64 { return (v + g - 1) / g * g }
	bandWidth := func(list []*genItem) (int64, int64) {
		left, right := int64(0), int64(0)
		for i, it := range list {
			if i == 0 || it.Left < left {
				left = it.Left
			}
			if i == 0 || it.Right > right {
				right = it.Right
			}
		}
		return left, right
	}
	attachedOf := func(list []*genItem) []*genItem {
		out := []*genItem{}
		for _, a := range list {
			for _, idx := range a.Attached {
				out = append(out, items[idx])
			}
		}
		return out
	}
	positions := map[int]Point{}
	cursor := margin
	maxBottom := margin
	placeColumn := func(anchorsList []*genItem) {
		aLeft, aRight := bandWidth(anchorsList)
		pLeft, pRight := bandWidth(attachedOf(anchorsList))
		anchorX := snap(cursor - aLeft)
		passiveX := anchorX
		if len(anchorsList) > 0 {
			passiveX = snap(anchorX + aRight + gap - pLeft)
		}
		y := margin
		for _, a := range anchorsList {
			ay := snap(y - a.Top)
			positions[a.Index] = Point{XNM: anchorX, YNM: ay}
			bottom := ay + a.Bottom
			py := y
			for _, idx := range a.Attached {
				p := items[idx]
				pp := snap(py - p.Top)
				positions[idx] = Point{XNM: passiveX, YNM: pp}
				py = pp + p.Bottom + g
				bottom = max64(bottom, pp+p.Bottom)
			}
			y = bottom + gap
			maxBottom = max64(maxBottom, bottom)
		}
		if len(anchorsList) == 0 {
			return
		}
		right := anchorX + aRight
		if len(attachedOf(anchorsList)) > 0 {
			right = passiveX + pRight
		}
		cursor = right + 2*gap
	}
	placeColumn(connectors)
	placeColumn(ics)
	if len(free) > 0 {
		fLeft, fRight := bandWidth(free)
		x := snap(cursor - fLeft)
		y := margin
		for _, p := range free {
			py := snap(y - p.Top)
			positions[p.Index] = Point{XNM: x, YNM: py}
			y = py + p.Bottom + g
			maxBottom = max64(maxBottom, py+p.Bottom)
		}
		cursor = x + fRight + gap
	}
	width, height := cursor+margin, maxBottom+margin
	sheet := SchematicSheet{ID: "main", Title: firstNonEmpty(def.Name, "Schematic"), Size: "custom", WidthNM: snap(width), HeightNM: snap(height)}
	for _, size := range []string{"A4", "A3", "A2", "A1", "A0"} {
		w, h, _ := paperSizeNM(size)
		if width <= w && height <= h {
			sheet.Size, sheet.WidthNM, sheet.HeightNM = size, w, h
			break
		}
	}
	spec.Sheets = append(spec.Sheets, sheet)

	labelIndex := 0
	for _, it := range items {
		c := def.Components[it.Index]
		pos := positions[it.Index]
		sym := SchematicSymbol{ComponentID: c.ID, SymbolID: it.Symbol, SheetID: sheet.ID, XNM: pos.XNM, YNM: pos.YNM, Unit: 1}
		sym.Pins = symbolAnchors(sym, it.Def, it.PinIDs)
		spec.Symbols = append(spec.Symbols, sym)
		for _, pin := range it.Def.Pins {
			pinID := it.PinIDs[pin.Number]
			netID := nodeNet[nodeKey(c.ID, pinID)]
			if pinID == "" || netID == "" {
				continue
			}
			x, y := schematicTransform(sym, pin.XNM, pin.YNM)
			labelKind := "local"
			if power[netID] {
				labelKind = "power"
			}
			labelIndex++
			spec.Labels = append(spec.Labels, SchematicLabel{ID: fmt.Sprintf("lbl-%d", labelIndex), SheetID: sheet.ID, NetID: netID, Text: labelText[netID], Kind: labelKind, XNM: x, YNM: y, Rotation: (pin.AngleDeg + 180) % 360})
		}
	}
	return spec
}

// designatorLess orders R2 before R10.
func designatorLess(a, b string) bool {
	pa, pb := designatorPrefix(a), designatorPrefix(b)
	if pa != pb {
		return pa < pb
	}
	na, nb := designatorNumber(a[len(pa):]), designatorNumber(b[len(pb):])
	if na != nb {
		return na < nb
	}
	return a < b
}

func designatorNumber(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			break
		}
	}
	return n
}

package main

import (
	"fmt"
	"html"
	"sort"
	"strings"
)

// renderSchematicSVG draws one sheet deterministically in the native preview
// style. An empty sheetID selects the first sheet.
func renderSchematicSVG(def *Definition, sheetID string) ([]byte, error) {
	spec := def.Schematic
	if spec == nil {
		return nil, fmt.Errorf("revision has no schematic")
	}
	if sheetID == "" && len(spec.Sheets) > 0 {
		sheetID = spec.Sheets[0].ID
	}
	si := findSchematicSheet(spec, sheetID)
	if si < 0 {
		return nil, fmt.Errorf("schematic sheet %q not found", sheetID)
	}
	sheet := spec.Sheets[si]
	conn := buildSchematicConnectivity(def)
	mm := func(v int64) string { return trimFloat(float64(v) / 1e6) }
	w, h := sheet.WidthNM, sheet.HeightNM
	if w <= 0 || h <= 0 {
		w, h, _ = paperSizeNM("A4")
	}
	components := map[string]Component{}
	for _, c := range def.Components {
		components[c.ID] = c
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-6 -10 %s %s" role="img" aria-label="Apteva PCB schematic">`, mm(w+12_000_000), mm(h+16_000_000))
	b.WriteString(`
<defs>
  <radialGradient id="sch-canvas" cx="50%" cy="42%" r="72%"><stop stop-color="#15110e"/><stop offset="1" stop-color="#090806"/></radialGradient>
  <pattern id="sch-grid" width="2.54" height="2.54" patternUnits="userSpaceOnUse"><path d="M2.54 0H0V2.54" fill="none" stroke="#d0ccc8" stroke-opacity=".05" stroke-width=".05"/></pattern>
</defs>
`)
	fmt.Fprintf(&b, `<rect x="-6" y="-10" width="%s" height="%s" rx="2.2" fill="url(#sch-canvas)"/>`, mm(w+12_000_000), mm(h+16_000_000))
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%s" height="%s" fill="url(#sch-grid)" stroke="#3a332d" stroke-width=".25"/>`, mm(w), mm(h))
	b.WriteByte('\n')
	title := firstNonEmpty(sheet.Title, def.Name, "Schematic")
	fmt.Fprintf(&b, `<g font-family="ui-sans-serif,system-ui,sans-serif"><text x="0" y="-5.2" font-size="3" font-weight="700" fill="#f5f2ef">%s</text><text x="0" y="-1.6" font-size="1.6" letter-spacing=".3" fill="#9f9993">APTEVA PCB · SCHEMATIC · SHEET %s (%s)</text><text x="%s" y="-1.6" text-anchor="end" font-size="1.6" fill="#d0ccc8">%d SYMBOLS · %d WIRES · %d LABELS</text></g>`, html.EscapeString(shortLabel(title, 64)), html.EscapeString(sheet.ID), html.EscapeString(sheet.Size), mm(w), countOnSheet(spec, sheetID, "symbols"), countOnSheet(spec, sheetID, "wires"), countOnSheet(spec, sheetID, "labels"))
	b.WriteByte('\n')

	// Wires below symbols.
	wires := []SchematicWire{}
	for _, wire := range spec.Wires {
		if wire.SheetID == sheetID && len(wire.Points) >= 2 {
			wires = append(wires, wire)
		}
	}
	sort.SliceStable(wires, func(i, j int) bool { return wires[i].ID < wires[j].ID })
	b.WriteString(`<g fill="none" stroke="#f97316" stroke-width=".3" stroke-linecap="round" stroke-linejoin="round">` + "\n")
	for _, wire := range wires {
		fmt.Fprintf(&b, `<polyline id="%s" points="%s"/>`, html.EscapeString(wire.ID), svgPoints(wire.Points))
		b.WriteByte('\n')
	}
	b.WriteString("</g>\n")

	// Symbols.
	order := make([]int, 0, len(spec.Symbols))
	for i, sym := range spec.Symbols {
		if sym.SheetID == sheetID && conn.ValidSymbolMask[i] {
			order = append(order, i)
		}
	}
	sort.SliceStable(order, func(i, j int) bool {
		a, c := spec.Symbols[order[i]], spec.Symbols[order[j]]
		if a.ComponentID != c.ComponentID {
			return a.ComponentID < c.ComponentID
		}
		return a.Unit < c.Unit
	})
	for _, si := range order {
		sym := spec.Symbols[si]
		symDef := conn.SymbolDefs[si]
		component := components[sym.ComponentID]
		renderSchematicSymbol(&b, sym, symDef, component, mm)
	}

	// Explicit junctions are drawn as dots.
	junctions := map[[2]int64]bool{}
	for _, j := range spec.Junctions {
		if j.SheetID == sheetID {
			junctions[[2]int64{j.XNM, j.YNM}] = true
		}
	}
	keys := make([][2]int64, 0, len(junctions))
	for k := range junctions {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i][0] != keys[j][0] {
			return keys[i][0] < keys[j][0]
		}
		return keys[i][1] < keys[j][1]
	})
	for _, k := range keys {
		fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r=".55" fill="#f97316"/>`, mm(k[0]), mm(k[1]))
	}
	b.WriteByte('\n')

	// Labels.
	labels := []SchematicLabel{}
	for _, l := range spec.Labels {
		if l.SheetID == sheetID {
			labels = append(labels, l)
		}
	}
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].ID < labels[j].ID })
	for _, l := range labels {
		renderSchematicLabel(&b, l, mm)
	}
	b.WriteString("</svg>\n")
	return []byte(b.String()), nil
}

func countOnSheet(spec *SchematicSpec, sheetID, kind string) int {
	n := 0
	switch kind {
	case "symbols":
		for _, s := range spec.Symbols {
			if s.SheetID == sheetID {
				n++
			}
		}
	case "wires":
		for _, s := range spec.Wires {
			if s.SheetID == sheetID {
				n++
			}
		}
	case "labels":
		for _, s := range spec.Labels {
			if s.SheetID == sheetID {
				n++
			}
		}
	}
	return n
}

func renderSchematicSymbol(b *strings.Builder, sym SchematicSymbol, def SchematicSymbolDef, component Component, mm func(int64) string) {
	sx, sy := "1", "1"
	switch sym.Mirror {
	case "x":
		sy = "-1"
	case "y":
		sx = "-1"
	}
	fmt.Fprintf(b, `<g id="sch-%s-%d"><title>%s · %s · %s</title>`, html.EscapeString(sym.ComponentID), sym.Unit, html.EscapeString(component.Designator), html.EscapeString(firstNonEmpty(component.Value, component.Name)), html.EscapeString(def.ID))
	fmt.Fprintf(b, `<g transform="translate(%s %s) scale(%s %s) rotate(%d)" fill="none" stroke="#d0ccc8" stroke-width=".25" stroke-linejoin="round">`, mm(sym.XNM), mm(sym.YNM), sx, sy, -sym.Rotation)
	body := def.Body
	x1, y1, x2, y2 := mm(body.X1NM), mm(body.Y1NM), mm(body.X2NM), mm(body.Y2NM)
	switch def.Kind {
	case "resistor":
		fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="%s" fill="#171411"/>`, x1, y1, mm(body.X2NM-body.X1NM), mm(body.Y2NM-body.Y1NM))
	case "capacitor":
		b.WriteString(`<path d="M-2.54 0H-.51M.51 0H2.54M-.51 -2.03V2.03M.51 -2.03V2.03"/>`)
	case "led", "diode":
		b.WriteString(`<path d="M-2.54 0H2.54M-1.27 -1.52V1.52L1.27 0Z" fill="#171411"/><path d="M1.27 -1.52V1.52"/>`)
		if def.Kind == "led" {
			b.WriteString(`<path d="M-.25 -2.1L.9 -3.25M.4 -3.25H.9V-2.75M.75 -1.7L1.9 -2.85M1.4 -2.85H1.9V-2.35" stroke="#fbbf24" stroke-width=".18"/>`)
		}
	default:
		fmt.Fprintf(b, `<rect x="%s" y="%s" width="%s" height="%s" rx=".3" fill="#171411"/>`, x1, y1, mm(body.X2NM-body.X1NM), mm(body.Y2NM-body.Y1NM))
	}
	_ = x2
	_ = y2
	b.WriteString("</g>")
	// Pins and names in absolute coordinates so text stays upright.
	showNames := (def.Kind == "ic" || def.Kind == "connector" || def.Kind == "kicad") && !def.PinNamesHidden
	showNumbers := (def.Kind == "ic" || def.Kind == "connector" || def.Kind == "kicad") && !def.PinNumbersHidden
	pins := append([]SchematicSymbolPin(nil), def.Pins...)
	sort.SliceStable(pins, func(i, j int) bool { return pins[i].Number < pins[j].Number })
	for _, pin := range pins {
		if !symbolPinActive(pin, sym.Unit) {
			continue
		}
		dx, dy := pinDirection(pin.AngleDeg)
		ax, ay := schematicTransform(sym, pin.XNM, pin.YNM)
		ex, ey := schematicTransform(sym, pin.XNM+dx*pin.LengthNM, pin.YNM+dy*pin.LengthNM)
		fmt.Fprintf(b, `<path d="M%s %sL%s %s" stroke="#fb923c" stroke-width=".25"/><circle cx="%s" cy="%s" r=".22" fill="#fb923c"/>`, mm(ax), mm(ay), mm(ex), mm(ey), mm(ax), mm(ay))
		vx, vy := sign64(ex-ax), sign64(ey-ay)
		name := strings.TrimSpace(pin.Name)
		if name == "~" {
			name = ""
		}
		if showNames && name != "" && def.Kind != "connector" {
			anchor := "start"
			if vx < 0 {
				anchor = "end"
			}
			nx, ny := ex+vx*600_000, ey+vy*600_000+400_000
			if vx == 0 {
				anchor = "middle"
				if vy < 0 {
					ny = ey - 600_000
				} else {
					ny = ey + 1_500_000
				}
			}
			fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="%s" font-family="ui-monospace,monospace" font-size="1.1" fill="#d0ccc8">%s</text>`, mm(nx), mm(ny), anchor, html.EscapeString(shortLabel(name, 24)))
		}
		// Pin numbers sit just above the pin line.
		mx, my := (ax+ex)/2, (ay+ey)/2-350_000
		if showNumbers {
			fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="middle" font-family="ui-monospace,monospace" font-size=".85" fill="#9f9993">%s</text>`, mm(mx), mm(my), html.EscapeString(shortLabel(pin.Number, 8)))
		}
		if showNames && def.Kind == "connector" && name != "" {
			fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="end" font-family="ui-monospace,monospace" font-size="1.0" fill="#9f9993">%s</text>`, mm(ex-1_800_000*abs64(vx)), mm(ey+350_000), html.EscapeString(shortLabel(name, 16)))
		}
	}
	// Designator above and value below the transformed body bounds.
	minX, minY, maxX, maxY := transformedBounds(sym, def.Body)
	cx := (minX + maxX) / 2
	fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="middle" font-family="ui-monospace,monospace" font-size="1.5" font-weight="700" fill="#f5f2ef">%s</text>`, mm(cx), mm(minY-700_000), html.EscapeString(component.Designator))
	value := firstNonEmpty(component.Value, component.Name)
	fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="middle" font-family="ui-sans-serif,sans-serif" font-size="1.2" fill="#9f9993">%s</text>`, mm(cx), mm(maxY+1_700_000), html.EscapeString(shortLabel(value, 28)))
	b.WriteString("</g>\n")
}

func transformedBounds(sym SchematicSymbol, r SchematicRect) (int64, int64, int64, int64) {
	xs, ys := []int64{}, []int64{}
	for _, p := range [][2]int64{{r.X1NM, r.Y1NM}, {r.X2NM, r.Y1NM}, {r.X1NM, r.Y2NM}, {r.X2NM, r.Y2NM}} {
		x, y := schematicTransform(sym, p[0], p[1])
		xs, ys = append(xs, x), append(ys, y)
	}
	sort.Slice(xs, func(i, j int) bool { return xs[i] < xs[j] })
	sort.Slice(ys, func(i, j int) bool { return ys[i] < ys[j] })
	return xs[0], ys[0], xs[3], ys[3]
}

func renderSchematicLabel(b *strings.Builder, l SchematicLabel, mm func(int64) string) {
	dx, dy := pinDirection(l.Rotation)
	text := html.EscapeString(shortLabel(l.Text, 40))
	x, y := l.XNM, l.YNM
	switch l.Kind {
	case "power":
		ground := strings.HasPrefix(strings.ToUpper(strings.TrimLeft(l.Text, "+-")), "GND") || strings.HasPrefix(strings.ToUpper(l.Text), "VSS")
		// Power flags point away from the pin; ground symbols point down.
		ex, ey := x+dx*1_270_000, y+dy*1_270_000
		if dx == 0 && dy == 0 {
			ex, ey = x, y+1_270_000
		}
		fmt.Fprintf(b, `<g id="%s" stroke="#fbbf24" stroke-width=".25" fill="none"><title>%s · power</title><path d="M%s %sL%s %s"/>`, html.EscapeString(l.ID), text, mm(x), mm(y), mm(ex), mm(ey))
		if ground {
			px, py := -dy, dx // perpendicular
			for i, half := range []int64{1_270_000, 800_000, 330_000} {
				ox, oy := ex+dx*int64(i)*450_000, ey+dy*int64(i)*450_000
				fmt.Fprintf(b, `<path d="M%s %sL%s %s"/>`, mm(ox-px*half), mm(oy-py*half), mm(ox+px*half), mm(oy+py*half))
			}
		} else {
			px, py := -dy, dx
			fmt.Fprintf(b, `<path d="M%s %sL%s %s"/>`, mm(ex-px*1_016_000), mm(ey-py*1_016_000), mm(ex+px*1_016_000), mm(ey+py*1_016_000))
		}
		tx, ty := ex+dx*1_900_000, ey+dy*1_900_000+400_000
		anchor := "middle"
		if dx > 0 {
			anchor, tx, ty = "start", ex+500_000, ey+1_600_000
		} else if dx < 0 {
			anchor, tx, ty = "end", ex-500_000, ey+1_600_000
		} else if dy < 0 {
			ty = ey - 900_000
		} else {
			ty = ey + 2_600_000
		}
		fmt.Fprintf(b, `<text x="%s" y="%s" text-anchor="%s" stroke="none" fill="#fbbf24" font-family="ui-monospace,monospace" font-size="1.2" font-weight="700">%s</text></g>`, mm(tx), mm(ty), anchor, text)
	default:
		anchor := "start"
		if dx < 0 {
			anchor = "end"
		}
		tx, ty := x+dx*600_000, y+dy*600_000-300_000
		if dx == 0 {
			anchor = "middle"
			if dy < 0 {
				ty = y - 900_000
			} else {
				ty = y + 1_800_000
			}
		}
		fill, decoration := "#e8bd66", ""
		if l.Kind == "global" {
			fill = "#93c5fd"
			width := int64(len([]rune(l.Text))+2) * 800_000
			ox := x
			if dx < 0 {
				ox = x - width
			}
			if dx != 0 {
				decoration = fmt.Sprintf(`<rect x="%s" y="%s" width="%s" height="2" rx="1" fill="none" stroke="#93c5fd" stroke-width=".2"/>`, mm(ox), mm(y-1_000_000), mm(width))
				tx = x + dx*800_000
				ty = y + 450_000
			}
		}
		fmt.Fprintf(b, `<g id="%s"><title>%s · %s label</title>%s<circle cx="%s" cy="%s" r=".3" fill="%s"/><text x="%s" y="%s" text-anchor="%s" fill="%s" font-family="ui-monospace,monospace" font-size="1.2">%s</text></g>`, html.EscapeString(l.ID), text, html.EscapeString(l.Kind), decoration, mm(x), mm(y), fill, mm(tx), mm(ty), anchor, fill, text)
	}
	b.WriteByte('\n')
}

func sign64(v int64) int64 {
	switch {
	case v > 0:
		return 1
	case v < 0:
		return -1
	}
	return 0
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}

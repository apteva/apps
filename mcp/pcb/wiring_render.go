package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"math"
	"strings"
)

func renderWiringSVG(def *Definition, states map[string]map[string]any) []byte {
	if def.Wiring == nil {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="800" height="450"><text x="40" y="60">No wiring workspace</text></svg>`)
	}
	w := def.Wiring
	width, height := w.Canvas.Width, w.Canvas.Height
	var b strings.Builder
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s wiring illustration"><defs><filter id="shadow" x="-20%%" y="-20%%" width="140%%" height="150%%"><feDropShadow dx="0" dy="7" stdDeviation="7" flood-color="#1c1917" flood-opacity=".18"/></filter><filter id="wireShadow" x="-20%%" y="-20%%" width="140%%" height="140%%"><feDropShadow dx="0" dy="3" stdDeviation="2" flood-color="#1c1917" flood-opacity=".25"/></filter><linearGradient id="uno" x2="0" y2="1"><stop stop-color="#149ca0"/><stop offset="1" stop-color="#08777d"/></linearGradient><radialGradient id="led"><stop stop-color="#ffb7a8"/><stop offset=".35" stop-color="#f04438"/><stop offset="1" stop-color="#9e1e1a"/></radialGradient><pattern id="paper" width="24" height="24" patternUnits="userSpaceOnUse"><path d="M24 0H0V24" fill="none" stroke="#ddd8cf" stroke-width=".7" opacity=".55"/></pattern></defs>`, width, height, width, height, html.EscapeString(def.Name))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#f2efe9"/><rect width="%d" height="%d" fill="url(#paper)"/><rect x="24" y="22" width="%d" height="%d" rx="22" fill="none" stroke="#d4cec3"/><text x="52" y="62" font-family="Inter,Arial,sans-serif" font-size="24" font-weight="700" fill="#22201e">%s</text><text x="52" y="88" font-family="Inter,Arial,sans-serif" font-size="12" letter-spacing="2" fill="#817a70">PCB STUDIO · WIRING VIEW</text>`, width, height, width, height, width-48, height-44, html.EscapeString(def.Name))
	catalog := wiringCatalog()
	parts := map[string]WiringPart{}
	// Parts are drawn before jumpers so wires convincingly sit on top.
	for _, p := range w.Parts {
		parts[p.ID] = p
		lib := catalog[p.LibraryID]
		switch lib.Kind {
		case "microcontroller":
			drawArduinoSVG(&b, p, lib)
		case "breadboard":
			drawBreadboardSVG(&b, p, lib)
		case "resistor":
			drawResistorSVG(&b, p, lib)
		case "led":
			active, _ := states[p.ID]["active"].(bool)
			drawLEDSVG(&b, p, lib, active)
		}
	}
	for _, wire := range w.Wires {
		start, ok1 := wiringPinPoint(parts, catalog, wire.From)
		end, ok2 := wiringPinPoint(parts, catalog, wire.To)
		if !ok1 || !ok2 {
			continue
		}
		pts := append([]WiringPoint{{X: start.X, Y: start.Y}}, wire.Points...)
		pts = append(pts, WiringPoint{X: end.X, Y: end.Y})
		d := smoothWirePath(pts)
		colorValue := wire.Color
		if colorValue == "" {
			colorValue = "#e16b32"
		}
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#faf8f2" stroke-width="13" stroke-linecap="round" stroke-linejoin="round" opacity=".9"/><path d="%s" fill="none" stroke="%s" stroke-width="8" stroke-linecap="round" stroke-linejoin="round" filter="url(#wireShadow)"/><circle cx="%.1f" cy="%.1f" r="6" fill="%s" stroke="#faf8f2" stroke-width="2"/><circle cx="%.1f" cy="%.1f" r="6" fill="%s" stroke="#faf8f2" stroke-width="2"/>`, d, d, html.EscapeString(colorValue), start.X, start.Y, colorValue, end.X, end.Y, colorValue)
	}
	for _, a := range w.Annotations {
		fmt.Fprintf(&b, `<g><rect x="%.1f" y="%.1f" width="%d" height="27" rx="13.5" fill="#242220" opacity=".94"/><text x="%.1f" y="%.1f" font-family="Inter,Arial,sans-serif" font-size="12" fill="#fffaf2">%s</text></g>`, a.X, a.Y, len([]rune(a.Text))*7+24, a.X+12, a.Y+18, html.EscapeString(a.Text))
	}
	fmt.Fprintf(&b, `<g transform="translate(%d,%d)"><rect width="330" height="54" rx="15" fill="#fffdf9" stroke="#d8d1c5"/><circle cx="25" cy="27" r="7" fill="#e16b32"/><text x="43" y="23" font-family="Inter,Arial,sans-serif" font-weight="700" font-size="12" fill="#282522">Pin-connected native model</text><text x="43" y="40" font-family="Inter,Arial,sans-serif" font-size="10" fill="#817a70">Every jumper terminates at a named electrical pin</text></g>`, width-370, height-82)
	b.WriteString(`</svg>`)
	return []byte(b.String())
}

func wiringPinPoint(parts map[string]WiringPart, catalog map[string]WiringLibraryPart, ep WiringEndpoint) (WiringPoint, bool) {
	p, ok := parts[ep.PartID]
	if !ok {
		return WiringPoint{}, false
	}
	for _, pin := range catalog[p.LibraryID].Pins {
		if pin.ID == ep.PinID {
			return WiringPoint{X: p.X + pin.X, Y: p.Y + pin.Y}, true
		}
	}
	return WiringPoint{}, false
}

func smoothWirePath(points []WiringPoint) string {
	if len(points) < 2 {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "M %.1f %.1f", points[0].X, points[0].Y)
	for i := 1; i < len(points); i++ {
		a := points[i-1]
		p := points[i]
		if i == len(points)-1 {
			fmt.Fprintf(&b, " Q %.1f %.1f %.1f %.1f", (a.X+p.X)/2, a.Y, p.X, p.Y)
		} else {
			fmt.Fprintf(&b, " Q %.1f %.1f %.1f %.1f", a.X, a.Y, (a.X+p.X)/2, (a.Y+p.Y)/2)
		}
	}
	return b.String()
}

func drawArduinoSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)"><path d="M14 0H300L330 29V382L302 410H12L0 395V16Z" fill="url(#uno)" stroke="#075d62" stroke-width="3"/><circle cx="27" cy="25" r="8" fill="#efece3" stroke="#315b5c" stroke-width="3"/><circle cx="300" cy="374" r="8" fill="#efece3" stroke="#315b5c" stroke-width="3"/><rect x="-7" y="71" width="72" height="55" rx="5" fill="#cbd0cf" stroke="#737978" stroke-width="2"/><rect x="5" y="82" width="49" height="32" fill="#555b5b"/><rect x="-8" y="290" width="70" height="64" rx="13" fill="#202426" stroke="#6d7172" stroke-width="3"/><circle cx="22" cy="322" r="12" fill="#090a0a"/><rect x="132" y="151" width="111" height="44" rx="5" fill="#26282a"/><circle cx="143" cy="173" r="3" fill="#c8c0ae"/><g fill="#d8d0b8" opacity=".8"><circle cx="91" cy="119" r="9"/><circle cx="278" cy="100" r="12"/><rect x="80" y="221" width="48" height="28" rx="3"/><rect x="254" y="231" width="33" height="57" rx="3"/></g><text x="91" y="293" fill="#eef9f6" font-family="Arial,sans-serif" font-weight="700" font-size="27">ARDUINO</text><text x="129" y="316" fill="#d9f2ee" font-family="Arial,sans-serif" font-size="13">UNO R3</text>`, x, y)
	// headers and pin labels
	b.WriteString(`<rect x="70" y="10" width="226" height="29" rx="4" fill="#202425"/><rect x="77" y="371" width="220" height="29" rx="4" fill="#202425"/>`)
	for _, pin := range lib.Pins {
		fmt.Fprintf(b, `<circle cx="%.1f" cy="%.1f" r="5.1" fill="#171918" stroke="#d5b15e" stroke-width="2"/>`, pin.X, pin.Y)
		if pin.Y < 100 {
			fmt.Fprintf(b, `<text x="%.1f" y="54" text-anchor="middle" fill="#e8f4f1" font-family="Arial,sans-serif" font-size="7">%s</text>`, pin.X, html.EscapeString(pin.Label))
		} else {
			fmt.Fprintf(b, `<text x="%.1f" y="367" text-anchor="middle" fill="#e8f4f1" font-family="Arial,sans-serif" font-size="7">%s</text>`, pin.X, html.EscapeString(pin.Label))
		}
	}
	fmt.Fprintf(b, `<text x="18" y="394" fill="#d4eeea" font-family="Inter,Arial" font-size="9">%s</text></g>`, html.EscapeString(p.Label))
}

func drawBreadboardSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)"><rect width="610" height="390" rx="24" fill="#fffefa" stroke="#cbc5ba" stroke-width="3"/><rect x="17" y="17" width="576" height="356" rx="15" fill="#f8f5ee" stroke="#dfd8cc"/><path d="M18 69H592M18 321H592" stroke="#d6cec1" stroke-width="1"/><path d="M40 44H570M40 346H570" stroke="#c9433d" stroke-width="3"/><path d="M40 58H570M40 332H570" stroke="#31343a" stroke-width="3"/><rect x="25" y="177" width="560" height="36" rx="7" fill="#e8e3da"/><text x="20" y="48" fill="#c9433d" font-family="Arial" font-size="16" font-weight="700">+</text><text x="20" y="63" fill="#34373b" font-family="Arial" font-size="17" font-weight="700">−</text><text x="20" y="337" fill="#34373b" font-family="Arial" font-size="17" font-weight="700">−</text><text x="20" y="352" fill="#c9433d" font-family="Arial" font-size="16" font-weight="700">+</text>`, x, y)
	for row := 0; row < 10; row++ {
		yy := 95 + row*23
		if row >= 5 {
			yy += 36
		}
		for col := 0; col < 30; col++ {
			xx := 45 + col*18
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="3.3" fill="#47433e"/><circle cx="%d" cy="%d" r="1.2" fill="#171614"/>`, xx, yy, xx, yy)
		}
	}
	for col := 0; col < 30; col++ {
		xx := 45 + col*18
		for _, yy := range []int{45, 58, 332, 345} {
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="3" fill="#5c5750"/>`, xx, yy)
		}
	}
	fmt.Fprintf(b, `<text x="305" y="381" text-anchor="middle" fill="#8b8479" font-family="Inter,Arial" font-size="10">%s</text></g>`, html.EscapeString(p.Label))
}

func drawResistorSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)"><path d="M0 16H31M99 16H130" stroke="#77736c" stroke-width="4"/><rect x="29" y="3" width="72" height="26" rx="12" fill="#e7c98f" stroke="#8b7049" stroke-width="2"/><path d="M45 4V28M57 4V28M74 4V28M88 4V28" stroke-width="7" stroke="#c54532"/><path d="M57 4V28" stroke="#3c2820" stroke-width="7"/><path d="M74 4V28" stroke="#604023" stroke-width="7"/><path d="M88 4V28" stroke="#d6a328" stroke-width="7"/><text x="65" y="48" text-anchor="middle" fill="#544d44" font-family="Inter,Arial" font-size="11" font-weight="700">%s</text></g>`, x, y, html.EscapeString(p.Label))
}

func drawLEDSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart, active bool) {
	x, y := p.X, p.Y
	glow := ""
	if active {
		glow = `<circle cx="27" cy="27" r="38" fill="#ff4b3e" opacity=".22"/>`
	}
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)">%s<path d="M17 48V88M37 48V88" stroke="#92918d" stroke-width="4"/><path d="M7 28A20 20 0 0 1 47 28V49H7Z" fill="url(#led)" stroke="#9f2d27" stroke-width="2"/><ellipse cx="20" cy="19" rx="7" ry="10" fill="#fff" opacity=".5"/><path d="M34 48h13" stroke="#eee" stroke-width="3"/><text x="27" y="108" text-anchor="middle" fill="#544d44" font-family="Inter,Arial" font-size="11" font-weight="700">%s</text></g>`, x, y, glow, html.EscapeString(p.Label))
}

func renderWiringPNG(def *Definition) []byte {
	if def.Wiring == nil {
		return nil
	}
	scale := 2
	img := image.NewRGBA(image.Rect(0, 0, def.Wiring.Canvas.Width*scale, def.Wiring.Canvas.Height*scale))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{242, 239, 233, 255}}, image.Point{}, draw.Src)
	catalog := wiringCatalog()
	parts := map[string]WiringPart{}
	for _, p := range def.Wiring.Parts {
		parts[p.ID] = p
		lib := catalog[p.LibraryID]
		c := color.RGBA{235, 231, 223, 255}
		switch lib.Kind {
		case "microcontroller":
			c = color.RGBA{15, 139, 145, 255}
		case "breadboard":
			c = color.RGBA{255, 254, 250, 255}
		case "resistor":
			c = color.RGBA{231, 201, 143, 255}
		case "led":
			c = color.RGBA{220, 54, 45, 255}
		}
		rect := image.Rect(int(p.X)*scale, int(p.Y)*scale, int(p.X+lib.Width)*scale, int(p.Y+lib.Height)*scale)
		draw.Draw(img, rect, &image.Uniform{c}, image.Point{}, draw.Src)
	}
	for _, wire := range def.Wiring.Wires {
		a, ok1 := wiringPinPoint(parts, catalog, wire.From)
		z, ok2 := wiringPinPoint(parts, catalog, wire.To)
		if !ok1 || !ok2 {
			continue
		}
		pts := append([]WiringPoint{a}, wire.Points...)
		pts = append(pts, z)
		c := parseHexColor(wire.Color)
		for i := 1; i < len(pts); i++ {
			drawThickLine(img, int(pts[i-1].X)*scale, int(pts[i-1].Y)*scale, int(pts[i].X)*scale, int(pts[i].Y)*scale, 7*scale, c)
		}
	}
	var out bytes.Buffer
	_ = png.Encode(&out, img)
	return out.Bytes()
}

func parseHexColor(s string) color.RGBA {
	var r, g, b uint8
	if _, err := fmt.Sscanf(strings.TrimPrefix(s, "#"), "%02x%02x%02x", &r, &g, &b); err != nil {
		return color.RGBA{225, 107, 50, 255}
	}
	return color.RGBA{r, g, b, 255}
}
func drawThickLine(img *image.RGBA, x0, y0, x1, y1, width int, c color.RGBA) {
	dx, dy := x1-x0, y1-y0
	steps := int(math.Max(math.Abs(float64(dx)), math.Abs(float64(dy))))
	if steps == 0 {
		steps = 1
	}
	rad := width / 2
	for i := 0; i <= steps; i++ {
		x := x0 + dx*i/steps
		y := y0 + dy*i/steps
		draw.Draw(img, image.Rect(x-rad, y-rad, x+rad+1, y+rad+1), &image.Uniform{c}, image.Point{}, draw.Src)
	}
}

func wiringTutorialJSON(def *Definition) []byte {
	body, _ := json.MarshalIndent(map[string]any{"schema": "apteva-pcb-tutorial/v1", "engine": engineVersion, "title": def.Name, "wiring": def.Wiring, "validation": validateWiring(def.Wiring)}, "", "  ")
	return append(body, '\n')
}

func wiringTutorialFiles(def *Definition) map[string][]byte {
	source, _ := json.MarshalIndent(def, "", "  ")
	return map[string][]byte{"illustration.svg": renderWiringSVG(def, nil), "illustration.png": renderWiringPNG(def), "tutorial.json": wiringTutorialJSON(def), "source/pcb.json": append(source, '\n')}
}

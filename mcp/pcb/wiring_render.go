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
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="%d" height="%d" viewBox="0 0 %d %d" role="img" aria-label="%s wiring illustration"><defs><filter id="shadow" x="-25%%" y="-25%%" width="150%%" height="165%%"><feDropShadow dx="0" dy="10" stdDeviation="9" flood-color="#2d2924" flood-opacity=".22"/></filter><filter id="smallShadow" x="-30%%" y="-50%%" width="160%%" height="200%%"><feDropShadow dx="0" dy="4" stdDeviation="3" flood-color="#2d2924" flood-opacity=".3"/></filter><filter id="wireShadow" x="-20%%" y="-20%%" width="140%%" height="140%%"><feDropShadow dx="0" dy="4" stdDeviation="2.4" flood-color="#171513" flood-opacity=".34"/></filter><linearGradient id="uno" x2="0" y2="1"><stop stop-color="#20a5aa"/><stop offset=".48" stop-color="#138c93"/><stop offset="1" stop-color="#087178"/></linearGradient><linearGradient id="metal" x2="0" y2="1"><stop stop-color="#f1f3f2"/><stop offset=".32" stop-color="#aeb5b5"/><stop offset=".65" stop-color="#e4e7e6"/><stop offset="1" stop-color="#8a9191"/></linearGradient><linearGradient id="plastic" x2="0" y2="1"><stop stop-color="#fff"/><stop offset=".55" stop-color="#f7f4ed"/><stop offset="1" stop-color="#e7e1d6"/></linearGradient><radialGradient id="hole"><stop stop-color="#191817"/><stop offset=".45" stop-color="#514d47"/><stop offset=".72" stop-color="#aaa399"/><stop offset="1" stop-color="#ebe6dc"/></radialGradient><radialGradient id="led"><stop stop-color="#ffe0d8"/><stop offset=".2" stop-color="#ff7668"/><stop offset=".55" stop-color="#e9362d"/><stop offset="1" stop-color="#941b18"/></radialGradient><pattern id="paper" width="24" height="24" patternUnits="userSpaceOnUse"><path d="M24 0H0V24" fill="none" stroke="#d9d3c8" stroke-width=".65" opacity=".48"/></pattern></defs>`, width, height, width, height, html.EscapeString(def.Name))
	fmt.Fprintf(&b, `<rect width="%d" height="%d" fill="#f5f2ec"/><rect width="%d" height="%d" fill="url(#paper)"/><rect x="24" y="22" width="%d" height="%d" rx="24" fill="none" stroke="#d8d1c5"/><text x="54" y="63" font-family="Inter,Arial,sans-serif" font-size="25" font-weight="750" fill="#24211e">%s</text><text x="54" y="89" font-family="Inter,Arial,sans-serif" font-size="11" letter-spacing="2.3" fill="#81796f">PCB STUDIO · BREADBOARD VIEW</text><g transform="translate(%d,43)"><rect width="138" height="31" rx="15.5" fill="#fffdf9" stroke="#d7d0c5"/><circle cx="18" cy="15.5" r="5" fill="#e9652c"/><text x="31" y="20" font-family="Inter,Arial,sans-serif" font-size="11" font-weight="650" fill="#504940">PIN CONNECTED</text></g>`, width, height, width, height, width-48, height-44, html.EscapeString(def.Name), width-186)
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
		fmt.Fprintf(&b, `<path d="%s" fill="none" stroke="#24211e" stroke-width="12" stroke-linecap="round" stroke-linejoin="round" opacity=".22" filter="url(#wireShadow)"/><path d="%s" fill="none" stroke="%s" stroke-width="8.5" stroke-linecap="round" stroke-linejoin="round"/><path d="%s" fill="none" stroke="#fff" stroke-width="1.7" stroke-linecap="round" stroke-linejoin="round" opacity=".28"/><circle cx="%.1f" cy="%.1f" r="7" fill="#272522" opacity=".22"/><circle cx="%.1f" cy="%.1f" r="5.2" fill="%s" stroke="#f9f6ef" stroke-width="1.5"/><circle cx="%.1f" cy="%.1f" r="7" fill="#272522" opacity=".22"/><circle cx="%.1f" cy="%.1f" r="5.2" fill="%s" stroke="#f9f6ef" stroke-width="1.5"/>`, d, d, html.EscapeString(colorValue), d, start.X+1.5, start.Y+2, start.X, start.Y, colorValue, end.X+1.5, end.Y+2, end.X, end.Y, colorValue)
	}
	for _, a := range w.Annotations {
		fmt.Fprintf(&b, `<g><rect x="%.1f" y="%.1f" width="%d" height="27" rx="13.5" fill="#242220" opacity=".94"/><text x="%.1f" y="%.1f" font-family="Inter,Arial,sans-serif" font-size="12" fill="#fffaf2">%s</text></g>`, a.X, a.Y, len([]rune(a.Text))*7+24, a.X+12, a.Y+18, html.EscapeString(a.Text))
	}
	fmt.Fprintf(&b, `<g transform="translate(%d,%d)"><rect width="355" height="54" rx="15" fill="#fffdf9" stroke="#d8d1c5"/><circle cx="25" cy="27" r="7" fill="#e9652c"/><text x="43" y="23" font-family="Inter,Arial,sans-serif" font-weight="700" font-size="12" fill="#282522">Native physical connection model</text><text x="43" y="40" font-family="Inter,Arial,sans-serif" font-size="10" fill="#817a70">Named pins drive validation, simulation, and export</text></g>`, width-400, height-82)
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
	// Catmull-Rom converted to cubic Béziers gives jumper-wire curves that
	// pass through every authored routing point without mechanical corners.
	for i := 0; i < len(points)-1; i++ {
		p0 := points[maxInt(0, i-1)]
		p1 := points[i]
		p2 := points[i+1]
		p3 := points[minInt(len(points)-1, i+2)]
		c1 := WiringPoint{X: p1.X + (p2.X-p0.X)/6, Y: p1.Y + (p2.Y-p0.Y)/6}
		c2 := WiringPoint{X: p2.X - (p3.X-p1.X)/6, Y: p2.Y - (p3.Y-p1.Y)/6}
		fmt.Fprintf(&b, " C %.1f %.1f %.1f %.1f %.1f %.1f", c1.X, c1.Y, c2.X, c2.Y, p2.X, p2.Y)
	}
	return b.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func drawArduinoSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)"><path d="M14 0H380L410 28V290L382 319H12L0 304V16Z" fill="url(#uno)" stroke="#08676d" stroke-width="3"/><path d="M18 8H377L401 32V284L377 310H18L8 299V20Z" fill="none" stroke="#6bc1c3" stroke-width="1" opacity=".48"/><g fill="#eef0e9" stroke="#316b6d" stroke-width="3"><circle cx="27" cy="25" r="8"/><circle cx="382" cy="48" r="8"/><circle cx="381" cy="286" r="8"/><circle cx="28" cy="286" r="8"/></g><g fill="none" stroke="#e7f5f2" stroke-width="1" opacity=".55"><path d="M74 78H383V116H330V202H389"/><path d="M72 278H385"/><path d="M111 101V196H73"/><path d="M168 76v54M297 180v76"/></g><g filter="url(#smallShadow)"><path d="M-8 34h69l12 10v39L61 92H-8Z" fill="url(#metal)" stroke="#737a79" stroke-width="2"/><rect x="4" y="45" width="50" height="36" rx="2" fill="#4a4f4f"/><rect x="10" y="51" width="38" height="24" rx="1" fill="#222626"/><path d="M0 40H56M0 86H57" stroke="#f5f7f6" opacity=".65"/></g><g filter="url(#smallShadow)"><rect x="-9" y="214" width="72" height="64" rx="14" fill="#232728" stroke="#767b7b" stroke-width="3"/><circle cx="23" cy="246" r="15" fill="#090a0a"/><circle cx="23" cy="246" r="8" fill="#1d2021"/><rect x="45" y="228" width="18" height="37" rx="4" fill="#111414"/></g><g filter="url(#smallShadow)"><rect x="190" y="133" width="146" height="50" rx="5" fill="#222526" stroke="#101213"/><path d="M190 146q11 7 0 14" fill="#121414"/><circle cx="324" cy="146" r="3.2" fill="#bcb7a9"/>`, x, y)
	for i := 0; i < 16; i++ {
		xx := 196 + i*9
		fmt.Fprintf(b, `<path d="M%d 129v-7M%d 187v7" stroke="#c8c5ba" stroke-width="3"/>`, xx, xx)
	}
	b.WriteString(`</g><g filter="url(#smallShadow)"><rect x="92" y="108" width="45" height="35" rx="3" fill="#25292a" stroke="#111313"/><circle cx="101" cy="118" r="2.5" fill="#bcb7a9"/><path d="M96 104v-7M104 104v-7M112 104v-7M120 104v-7M128 104v-7M96 147v7M104 147v7M112 147v7M120 147v7M128 147v7" stroke="#c8c5ba" stroke-width="2.5"/></g><g filter="url(#smallShadow)"><rect x="158" y="205" width="48" height="26" rx="12" fill="url(#metal)" stroke="#8c918f"/><path d="M165 211h34M165 217h34M165 223h34" stroke="#fff" opacity=".5"/></g><g filter="url(#smallShadow)"><rect x="91" y="218" width="43" height="27" rx="3" fill="#b8c0b2" stroke="#6d746c"/><rect x="97" y="223" width="31" height="17" fill="#70816f"/></g><g><circle cx="84" cy="57" r="10" fill="#d9dfd6" stroke="#8b948b"/><circle cx="84" cy="57" r="4" fill="#747b74"/><rect x="349" y="93" width="30" height="25" rx="4" fill="#d9dfd6" stroke="#8b948b"/><circle cx="364" cy="105.5" r="6" fill="#777e77"/></g><g fill="#e4c879"><rect x="78" y="164" width="20" height="11" rx="2"/><rect x="105" y="164" width="16" height="9" rx="2"/><rect x="345" y="137" width="24" height="12" rx="2"/></g><g fill="#f2f5ed" font-family="Arial,sans-serif"><text x="211" y="235" font-weight="700" font-size="25">ARDUINO</text><text x="245" y="256" font-size="13">UNO R3</text><text x="183" y="65" font-size="8" letter-spacing="1.2">DIGITAL (PWM ~)</text><text x="102" y="271" font-size="8" letter-spacing="1.1">POWER</text><text x="300" y="271" font-size="8" letter-spacing="1.1">ANALOG IN</text><text x="18" y="111" font-size="7">USB</text><text x="14" y="202" font-size="7">POWER</text></g>`)
	for _, pin := range lib.Pins {
		fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="14" height="17" rx="2" fill="#202324" stroke="#111313"/><rect x="%.1f" y="%.1f" width="6.4" height="6.4" rx="1" fill="#1a1c1c" stroke="#d7b55e" stroke-width="1.8"/>`, pin.X-7, pin.Y-8.5, pin.X-3.2, pin.Y-3.2)
		if pin.Y < 100 {
			fmt.Fprintf(b, `<text x="%.1f" y="51" text-anchor="middle" fill="#eff8f5" font-family="Arial,sans-serif" font-size="7">%s</text>`, pin.X, html.EscapeString(pin.Label))
		} else {
			fmt.Fprintf(b, `<text x="%.1f" y="282" text-anchor="middle" fill="#eff8f5" font-family="Arial,sans-serif" font-size="7">%s</text>`, pin.X, html.EscapeString(pin.Label))
		}
	}
	fmt.Fprintf(b, `<rect x="12" y="296" width="112" height="16" rx="8" fill="#09666c" opacity=".9"/><text x="68" y="307" text-anchor="middle" fill="#e5f4f1" font-family="Inter,Arial" font-size="8.5">%s</text></g>`, html.EscapeString(p.Label))
}

func drawBreadboardSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#shadow)"><rect x="-4" y="5" width="618" height="394" rx="26" fill="#d2cbc0" opacity=".45"/><rect width="610" height="390" rx="24" fill="url(#plastic)" stroke="#c7c0b5" stroke-width="3"/><rect x="13" y="13" width="584" height="364" rx="17" fill="#fbf9f4" stroke="#e0dacf"/><path d="M18 72H592M18 318H592" stroke="#d7d1c8"/><path d="M42 43H570M42 347H570" stroke="#d94b45" stroke-width="3.2"/><path d="M42 59H570M42 331H570" stroke="#3e4247" stroke-width="3.2"/><rect x="24" y="174" width="562" height="42" rx="8" fill="#e9e4db" stroke="#dad3c8"/><path d="M27 191H583M27 199H583" stroke="#d3ccc1" opacity=".7"/><g font-family="Arial,sans-serif" font-weight="700"><text x="18" y="48" fill="#d94b45" font-size="16">+</text><text x="18" y="64" fill="#34383d" font-size="17">−</text><text x="18" y="336" fill="#34383d" font-size="17">−</text><text x="18" y="353" fill="#d94b45" font-size="16">+</text></g><g fill="#9c958b" font-family="Arial,sans-serif" font-size="7" text-anchor="middle"><text x="45" y="84">a</text><text x="45" y="169">e</text><text x="45" y="230">f</text><text x="45" y="315">j</text></g><g fill="#9c958b" font-family="Arial,sans-serif" font-size="6.5" text-anchor="middle">`, x, y)
	for col := 0; col < 30; col += 5 {
		xx := 45 + col*18
		fmt.Fprintf(b, `<text x="%d" y="89">%d</text><text x="%d" y="309">%d</text>`, xx, col+1, xx, col+1)
	}
	b.WriteString(`</g><g><circle cx="25" cy="45" r="7" fill="#d94b45" opacity=".16"/><circle cx="25" cy="45" r="4.3" fill="url(#hole)"/><circle cx="25" cy="345" r="7" fill="#34383d" opacity=".13"/><circle cx="25" cy="345" r="4.3" fill="url(#hole)"/></g>`)
	for row := 0; row < 10; row++ {
		yy := 95 + row*23
		if row >= 5 {
			yy += 36
		}
		for col := 0; col < 30; col++ {
			xx := 45 + col*18
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="4.1" fill="#e4ded4"/><circle cx="%d" cy="%d" r="3.25" fill="url(#hole)"/>`, xx, yy, xx, yy)
		}
	}
	for col := 0; col < 30; col++ {
		xx := 45 + col*18
		for _, yy := range []int{45, 58, 332, 345} {
			fmt.Fprintf(b, `<circle cx="%d" cy="%d" r="3.5" fill="url(#hole)"/>`, xx, yy)
		}
	}
	fmt.Fprintf(b, `<rect x="246" y="369" width="118" height="16" rx="8" fill="#eee9e0"/><text x="305" y="380.5" text-anchor="middle" fill="#817a70" font-family="Inter,Arial" font-size="8.5">%s</text></g>`, html.EscapeString(p.Label))
}

func drawResistorSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart) {
	x, y := p.X, p.Y
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#smallShadow)"><ellipse cx="65" cy="22" rx="43" ry="10" fill="#2c2925" opacity=".15"/><path d="M0 16H31M99 16H130" stroke="#8e918d" stroke-width="3.2"/><path d="M0 16v5M130 16v5" stroke="#666a67" stroke-width="3"/><circle cx="0" cy="21" r="3.5" fill="#302e2a"/><circle cx="130" cy="21" r="3.5" fill="#302e2a"/><rect x="28" y="2" width="74" height="27" rx="13.5" fill="#e8ca91" stroke="#9d7b4e" stroke-width="1.6"/><path d="M34 8c17-6 46-6 62 0" fill="none" stroke="#fff3ce" stroke-width="3" opacity=".45"/><path d="M45 3V28" stroke="#bd382d" stroke-width="7"/><path d="M58 3V28" stroke="#3b2921" stroke-width="7"/><path d="M75 3V28" stroke="#704729" stroke-width="7"/><path d="M89 3V28" stroke="#d3a52d" stroke-width="6"/><rect x="24" y="36" width="82" height="18" rx="9" fill="#fffdf8" stroke="#d7d0c5"/><text x="65" y="48.5" text-anchor="middle" fill="#544d44" font-family="Inter,Arial" font-size="9.5" font-weight="700">%s</text></g>`, x, y, html.EscapeString(p.Label))
}

func drawLEDSVG(b *strings.Builder, p WiringPart, lib WiringLibraryPart, active bool) {
	x, y := p.X, p.Y
	glow := ""
	if active {
		glow = `<circle cx="27" cy="27" r="38" fill="#ff4b3e" opacity=".22"/>`
	}
	fmt.Fprintf(b, `<g transform="translate(%.1f %.1f)" filter="url(#smallShadow)">%s<ellipse cx="27" cy="119" rx="19" ry="6" fill="#272421" opacity=".17"/><path d="M17 47V120M37 47V120" stroke="#999d99" stroke-width="3.4"/><path d="M17 76l9-20M37 76l-7-19" stroke="#d9ddd8" stroke-width="2"/><circle cx="17" cy="120" r="3.5" fill="#302e2a"/><circle cx="37" cy="120" r="3.5" fill="#302e2a"/><path d="M7 28A20 20 0 0 1 47 28V49H7Z" fill="url(#led)" stroke="#9f2d27" stroke-width="2"/><path d="M7 45H47" stroke="#741816" opacity=".5"/><ellipse cx="19" cy="18" rx="6" ry="10" fill="#fff" opacity=".56"/><path d="M37 32v14h10" fill="none" stroke="#ffd5cd" stroke-width="2.6" opacity=".8"/><rect x="-2" y="130" width="58" height="18" rx="9" fill="#fffdf8" stroke="#d7d0c5"/><text x="27" y="142.5" text-anchor="middle" fill="#544d44" font-family="Inter,Arial" font-size="9.5" font-weight="700">%s</text></g>`, x, y, glow, html.EscapeString(p.Label))
}

func renderWiringPNG(def *Definition) []byte {
	if def.Wiring == nil {
		return nil
	}
	scale := 2
	img := image.NewRGBA(image.Rect(0, 0, def.Wiring.Canvas.Width*scale, def.Wiring.Canvas.Height*scale))
	draw.Draw(img, img.Bounds(), &image.Uniform{color.RGBA{242, 239, 233, 255}}, image.Point{}, draw.Src)
	for x := 0; x < img.Bounds().Dx(); x += 24 * scale {
		draw.Draw(img, image.Rect(x, 0, x+1, img.Bounds().Dy()), &image.Uniform{color.RGBA{222, 216, 205, 255}}, image.Point{}, draw.Src)
	}
	for y := 0; y < img.Bounds().Dy(); y += 24 * scale {
		draw.Draw(img, image.Rect(0, y, img.Bounds().Dx(), y+1), &image.Uniform{color.RGBA{222, 216, 205, 255}}, image.Point{}, draw.Src)
	}
	catalog := wiringCatalog()
	parts := map[string]WiringPart{}
	for _, p := range def.Wiring.Parts {
		parts[p.ID] = p
		lib := catalog[p.LibraryID]
		drawWiringPNGPart(img, p, lib, scale)
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

func drawWiringPNGPart(img *image.RGBA, p WiringPart, lib WiringLibraryPart, scale int) {
	x, y := int(p.X), int(p.Y)
	box := func(x1, y1, x2, y2 int, c color.RGBA) {
		draw.Draw(img, image.Rect((x+x1)*scale, (y+y1)*scale, (x+x2)*scale, (y+y2)*scale), &image.Uniform{c}, image.Point{}, draw.Over)
	}
	switch lib.Kind {
	case "microcontroller":
		box(4, 8, 414, 323, color.RGBA{102, 92, 80, 40})
		box(0, 0, 410, 319, color.RGBA{18, 139, 146, 255})
		box(0, 34, 63, 92, color.RGBA{184, 190, 189, 255})
		box(5, 45, 54, 81, color.RGBA{57, 63, 63, 255})
		box(0, 214, 64, 278, color.RGBA{31, 35, 36, 255})
		box(190, 133, 336, 183, color.RGBA{32, 35, 36, 255})
		box(92, 108, 137, 143, color.RGBA{37, 41, 42, 255})
		box(158, 205, 206, 231, color.RGBA{192, 196, 190, 255})
		box(91, 218, 134, 245, color.RGBA{174, 184, 171, 255})
		for _, pin := range lib.Pins {
			box(int(pin.X)-6, int(pin.Y)-7, int(pin.X)+6, int(pin.Y)+7, color.RGBA{28, 31, 32, 255})
			box(int(pin.X)-2, int(pin.Y)-2, int(pin.X)+3, int(pin.Y)+3, color.RGBA{215, 181, 94, 255})
		}
	case "breadboard":
		box(5, 8, 615, 398, color.RGBA{113, 103, 92, 35})
		box(0, 0, 610, 390, color.RGBA{255, 254, 250, 255})
		box(14, 14, 596, 376, color.RGBA{248, 246, 240, 255})
		box(25, 174, 585, 216, color.RGBA{232, 227, 218, 255})
		box(42, 41, 570, 45, color.RGBA{217, 75, 69, 255})
		box(42, 57, 570, 61, color.RGBA{57, 61, 66, 255})
		box(42, 329, 570, 333, color.RGBA{57, 61, 66, 255})
		box(42, 345, 570, 349, color.RGBA{217, 75, 69, 255})
		for row := 0; row < 10; row++ {
			yy := 95 + row*23
			if row >= 5 {
				yy += 36
			}
			for col := 0; col < 30; col++ {
				drawCirclePNG(img, (x+45+col*18)*scale, (y+yy)*scale, 3*scale, color.RGBA{64, 61, 57, 255})
			}
		}
		for col := 0; col < 30; col++ {
			for _, yy := range []int{45, 58, 332, 345} {
				drawCirclePNG(img, (x+45+col*18)*scale, (y+yy)*scale, 3*scale, color.RGBA{72, 68, 63, 255})
			}
		}
	case "resistor":
		box(0, 14, 130, 19, color.RGBA{126, 127, 122, 255})
		box(28, 2, 102, 30, color.RGBA{232, 202, 145, 255})
		box(43, 3, 50, 29, color.RGBA{188, 56, 45, 255})
		box(56, 3, 63, 29, color.RGBA{58, 41, 33, 255})
		box(72, 3, 79, 29, color.RGBA{112, 71, 41, 255})
		box(87, 3, 93, 29, color.RGBA{211, 165, 45, 255})
	case "led":
		box(15, 40, 19, 121, color.RGBA{153, 157, 153, 255})
		box(35, 40, 39, 121, color.RGBA{153, 157, 153, 255})
		drawCirclePNG(img, (x+27)*scale, (y+28)*scale, 21*scale, color.RGBA{226, 54, 45, 255})
		drawCirclePNG(img, (x+20)*scale, (y+19)*scale, 6*scale, color.RGBA{255, 184, 173, 210})
	}
}

func drawCirclePNG(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	rr := r * r
	for yy := -r; yy <= r; yy++ {
		span := int(math.Sqrt(float64(rr - yy*yy)))
		draw.Draw(img, image.Rect(cx-span, cy+yy, cx+span+1, cy+yy+1), &image.Uniform{c}, image.Point{}, draw.Src)
	}
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

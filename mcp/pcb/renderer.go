package main

import (
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
)

func renderSVG(def *Definition) []byte {
	mm := func(v int64) string { return trimFloat(float64(v) / 1e6) }
	width, height := mm(def.Board.WidthNM), mm(def.Board.HeightNM)
	canvasWidth, canvasHeight := mm(def.Board.WidthNM+8_000_000), mm(def.Board.HeightNM+8_000_000)
	netNames := map[string]string{}
	for _, net := range def.Nets {
		netNames[net.ID] = net.Name
	}
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" viewBox="-4 -4 %s %s" role="img" aria-label="Apteva PCB board preview">`, canvasWidth, canvasHeight)
	b.WriteString(`
<defs>
  <radialGradient id="canvas-fill" cx="50%" cy="42%" r="72%"><stop stop-color="#15110e"/><stop offset="1" stop-color="#090806"/></radialGradient>
  <linearGradient id="board-fill" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#21170f"/><stop offset="1" stop-color="#130e0a"/></linearGradient>
  <pattern id="canvas-grid" width="2.4" height="2.4" patternUnits="userSpaceOnUse"><path d="M2.4 0H0V2.4" fill="none" stroke="#9f9993" stroke-opacity=".045" stroke-width=".04"/></pattern>
  <pattern id="grid" width="2" height="2" patternUnits="userSpaceOnUse"><path d="M2 0H0V2" fill="none" stroke="#d0ccc8" stroke-opacity=".05" stroke-width=".04"/></pattern>
  <pattern id="keepout-pattern" width="1.4" height="1.4" patternUnits="userSpaceOnUse" patternTransform="rotate(45)"><line x1="0" y1="0" x2="0" y2="1.4" stroke="#fbbf24" stroke-opacity=".62" stroke-width=".18"/></pattern>
  <filter id="board-shadow" x="-30%" y="-30%" width="160%" height="170%"><feDropShadow dx="0" dy="1.2" stdDeviation="1.15" flood-color="#000" flood-opacity=".72"/></filter>
  <filter id="copper-glow" x="-30%" y="-30%" width="160%" height="160%"><feDropShadow dx="0" dy="0" stdDeviation=".11" flood-color="#f97316" flood-opacity=".42"/></filter>
</defs>
`)
	fmt.Fprintf(&b, `<rect x="-4" y="-4" width="%s" height="%s" rx="2.2" fill="url(#canvas-fill)"/>`, canvasWidth, canvasHeight)
	fmt.Fprintf(&b, `<rect x="-4" y="-4" width="%s" height="%s" rx="2.2" fill="url(#canvas-grid)" stroke="#252a31" stroke-width=".08"/>`, canvasWidth, canvasHeight)
	b.WriteByte('\n')
	label := strings.TrimSpace(def.Name)
	if label == "" {
		label = "Native PCB layout"
	}
	fmt.Fprintf(&b, `<g font-family="ui-sans-serif,system-ui,sans-serif"><text x="0" y="-2.25" font-size=".72" font-weight="700" fill="#f5f2ef">%s</text><text x="0" y="-1.25" font-size=".38" letter-spacing=".08" fill="#9f9993">APTEVA PCB · NATIVE LAYOUT</text><circle cx="%s" cy="-2.18" r=".18" fill="#f97316"/><text x="%s" y="-2.02" text-anchor="end" font-size=".38" fill="#d0ccc8">%d COMPONENTS · %d NETS</text></g>`, html.EscapeString(shortLabel(label, 48)), mm(def.Board.WidthNM-100_000), mm(def.Board.WidthNM-500_000), len(def.Components), len(def.Nets))
	b.WriteByte('\n')
	b.WriteString(`<g filter="url(#board-shadow)">` + "\n")
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%s" height="%s" rx="1.2" fill="url(#board-fill)" stroke="#f97316" stroke-width=".20"/>`, width, height)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%s" height="%s" rx="1.2" fill="url(#grid)"/>`, width, height)
	b.WriteByte('\n')

	// Filled copper is drawn below routed traces and footprints.
	for _, zone := range def.Zones {
		color := layerColor(zone.Layer)
		fmt.Fprintf(&b, `<polygon id="%s" points="%s" fill="%s" fill-opacity=".16" stroke="%s" stroke-opacity=".48" stroke-width=".10"><title>%s zone · %s</title></polygon>`, html.EscapeString(zone.ID), svgPoints(zone.Polygon), color, color, html.EscapeString(netNames[zone.NetID]), html.EscapeString(zone.Layer))
		b.WriteByte('\n')
	}
	for _, keepout := range def.Keepouts {
		fmt.Fprintf(&b, `<polygon id="%s" points="%s" fill="url(#keepout-pattern)" stroke="#fbbf24" stroke-width=".14" stroke-dasharray=".5 .3"><title>%s keepout</title></polygon>`, html.EscapeString(keepout.ID), svgPoints(keepout.Polygon), html.EscapeString(keepout.Kind))
		b.WriteByte('\n')
	}

	for _, trace := range def.Traces {
		color := layerColor(trace.Layer)
		dash := ""
		if trace.Layer == "B.Cu" {
			dash = ` stroke-dasharray=".55 .22"`
		}
		fmt.Fprintf(&b, `<polyline id="%s" points="%s" fill="none" stroke="#1a0b04" stroke-opacity=".7" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round"/>`, html.EscapeString(trace.ID)+"-mask", svgPoints(trace.Points), mm(trace.WidthNM+160_000))
		b.WriteByte('\n')
		fmt.Fprintf(&b, `<polyline id="%s" points="%s" fill="none" stroke="%s" stroke-width="%s" stroke-linecap="round" stroke-linejoin="round"%s filter="url(#copper-glow)"><title>%s · %s · %s mm</title></polyline>`, html.EscapeString(trace.ID), svgPoints(trace.Points), color, mm(trace.WidthNM), dash, html.EscapeString(netNames[trace.NetID]), html.EscapeString(trace.Layer), mm(trace.WidthNM))
		b.WriteByte('\n')
	}
	for _, via := range def.Vias {
		fmt.Fprintf(&b, `<g id="%s"><circle cx="%s" cy="%s" r="%s" fill="#d6a84f" stroke="#523a11" stroke-width=".10"/><circle cx="%s" cy="%s" r="%s" fill="#090806"/><title>Via · %s · drill %s mm</title></g>`, html.EscapeString(via.ID), mm(via.XNM), mm(via.YNM), mm(via.DiameterNM/2), mm(via.XNM), mm(via.YNM), mm(via.DrillNM/2), html.EscapeString(netNames[via.NetID]), mm(via.DrillNM))
		b.WriteByte('\n')
	}

	components := append([]Component(nil), def.Components...)
	sort.SliceStable(components, func(i, j int) bool { return components[i].Designator < components[j].Designator })
	for _, component := range components {
		body := component.Body
		if body == nil || body.WidthNM <= 0 || body.HeightNM <= 0 {
			fallback := inferredBody(component)
			body = &fallback
		}
		bodyFill := "#171411"
		bodyStroke := "#d0ccc8"
		if component.Position.Side == "back" {
			bodyFill, bodyStroke = "#1b1510", "#fdba74"
		}
		fmt.Fprintf(&b, `<g id="%s" transform="translate(%s %s) rotate(%s)">`, html.EscapeString(component.ID), mm(component.Position.XNM), mm(component.Position.YNM), trimFloat(float64(component.Position.RotationUdeg)/1e6))
		fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" rx=".28" fill="%s" fill-opacity=".92" stroke="%s" stroke-width=".13"/>`, mm(-body.WidthNM/2), mm(-body.HeightNM/2), mm(body.WidthNM), mm(body.HeightNM), bodyFill, bodyStroke)
		for _, pad := range component.Pads {
			padColor := "#fb923c"
			if containsString(pad.Layers, "B.Cu") && !containsString(pad.Layers, "F.Cu") {
				padColor = "#d97706"
			} else if len(pad.Layers) > 1 {
				padColor = "#d6a84f"
			}
			shape := strings.ToLower(pad.Shape)
			if shape == "circle" {
				fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="%s" fill="%s" stroke="#341d15" stroke-width=".07"><title>%s pad %s · pin %s</title></circle>`, mm(pad.XNM), mm(pad.YNM), mm(min64(pad.WidthNM, pad.HeightNM)/2), padColor, html.EscapeString(component.Designator), html.EscapeString(pad.ID), html.EscapeString(pad.PinID))
			} else {
				rx := "0"
				if shape == "roundrect" || shape == "oval" {
					rx = mm(min64(pad.WidthNM, pad.HeightNM) / 4)
				}
				fmt.Fprintf(&b, `<rect x="%s" y="%s" width="%s" height="%s" rx="%s" fill="%s" stroke="#341d15" stroke-width=".07"><title>%s pad %s · pin %s</title></rect>`, mm(pad.XNM-pad.WidthNM/2), mm(pad.YNM-pad.HeightNM/2), mm(pad.WidthNM), mm(pad.HeightNM), rx, padColor, html.EscapeString(component.Designator), html.EscapeString(pad.ID), html.EscapeString(pad.PinID))
			}
			if pad.DrillNM > 0 {
				fmt.Fprintf(&b, `<circle cx="%s" cy="%s" r="%s" fill="#071612" stroke="#f2dfae" stroke-width=".04"/>`, mm(pad.XNM), mm(pad.YNM), mm(pad.DrillNM/2))
			}
		}
		fmt.Fprintf(&b, `<text x="0" y="%s" text-anchor="middle" font-family="ui-monospace,monospace" font-size=".86" font-weight="700" fill="#f5f2ef" stroke="#090806" stroke-width=".08" paint-order="stroke">%s</text>`, mm(-body.HeightNM/2-500_000), html.EscapeString(component.Designator))
		value := component.Value
		if value == "" {
			value = component.Name
		}
		fmt.Fprintf(&b, `<text x="0" y="%s" text-anchor="middle" font-family="ui-sans-serif,sans-serif" font-size=".48" fill="#9f9993">%s</text><title>%s · %s · %s</title></g>`, mm(body.HeightNM/2+650_000), html.EscapeString(shortLabel(value, 24)), html.EscapeString(component.Designator), html.EscapeString(component.Name), html.EscapeString(component.Footprint))
		b.WriteByte('\n')
	}
	b.WriteString("</g>\n")
	// Native dimension marks make the artifact useful without a separate viewer.
	fmt.Fprintf(&b, `<g fill="#9f9993" stroke="#756d66" stroke-width=".06" font-family="ui-monospace,monospace" font-size=".58"><path d="M0 %sV%sM%s %sV%sM0 %sH%s"/><text x="%s" y="%s" text-anchor="middle">%s mm</text><text x="%s" y="%s" text-anchor="middle" transform="rotate(-90 %s %s)">%s mm</text></g>`, mm(def.Board.HeightNM+1_000_000), mm(def.Board.HeightNM+2_100_000), width, mm(def.Board.HeightNM+1_000_000), mm(def.Board.HeightNM+2_100_000), mm(def.Board.HeightNM+1_600_000), width, mm(def.Board.WidthNM/2), mm(def.Board.HeightNM+2_500_000), width, mm(-2_400_000), mm(def.Board.HeightNM/2), mm(-2_400_000), mm(def.Board.HeightNM/2), height)
	b.WriteString("</svg>\n")
	return []byte(b.String())
}

func inferredBody(component Component) Body {
	width, height := int64(3_000_000), int64(2_000_000)
	for _, pad := range component.Pads {
		width = max64(width, int64(math.Abs(float64(pad.XNM)))*2+pad.WidthNM+500_000)
		height = max64(height, int64(math.Abs(float64(pad.YNM)))*2+pad.HeightNM+500_000)
	}
	return Body{WidthNM: width, HeightNM: height}
}

func svgPoints(points []Point) string {
	out := make([]string, len(points))
	for i, point := range points {
		out[i] = trimFloat(float64(point.XNM)/1e6) + "," + trimFloat(float64(point.YNM)/1e6)
	}
	return strings.Join(out, " ")
}

func layerColor(layer string) string {
	if layer == "F.Cu" {
		return "#f97316"
	}
	if layer == "B.Cu" {
		return "#b45309"
	}
	return "#e8bd66"
}

func shortLabel(value string, limit int) string {
	runes := []rune(strings.TrimSpace(value))
	if len(runes) <= limit {
		return string(runes)
	}
	return string(runes[:limit-1]) + "…"
}

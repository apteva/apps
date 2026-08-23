package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

// manufacturingFiles writes a deterministic, dependency-free Gerber X2-like
// fabrication set plus Excellon drills. It intentionally supports the native
// v1 geometry we own: traces, footprint pads, vias, filled zones, component
// body silkscreen, and the rectangular board outline.
func manufacturingFiles(def *Definition) map[string][]byte {
	files := map[string][]byte{}
	for _, layer := range def.Board.Layers {
		switch {
		case layer.Kind == "copper":
			files["gerbers/"+gerberFilename(layer.ID)+".gbr"] = renderCopperGerber(def, layer.ID)
		case layer.Kind == "silkscreen":
			files["gerbers/"+gerberFilename(layer.ID)+".gbr"] = renderSilkscreenGerber(def, layer.ID)
		}
	}
	files["gerbers/Edge_Cuts.gbr"] = renderEdgeGerber(def)
	files["drill/board.drl"] = renderExcellon(def)
	job := map[string]any{
		"Header":          map[string]any{"GenerationSoftware": map[string]any{"Vendor": "Apteva", "Application": "PCB Studio", "Version": engineVersion}, "CreationDate": "1980-01-01T00:00:00Z"},
		"GeneralSpecs":    map[string]any{"ProjectId": def.Name, "Size": map[string]any{"X": float64(def.Board.WidthNM) / 1e6, "Y": float64(def.Board.HeightNM) / 1e6}, "LayerNumber": copperLayerCount(def)},
		"FilesAttributes": gerberJobFiles(files),
	}
	body, _ := json.MarshalIndent(job, "", "  ")
	files["board.gbrjob"] = append(body, '\n')
	return files
}

func renderCopperGerber(def *Definition, layerID string) []byte {
	widths := map[int64]bool{}
	for _, trace := range def.Traces {
		if trace.Layer == layerID && trace.WidthNM > 0 {
			widths[trace.WidthNM] = true
		}
	}
	ordered := make([]int64, 0, len(widths))
	for width := range widths {
		ordered = append(ordered, width)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	aperture := map[int64]int{}
	var b strings.Builder
	b.WriteString("G04 Apteva PCB Studio native copper*\n%FSLAX46Y46*%\n%MOMM*%\n%LPD*%\n%ADD10C,0.100000*%\nD10*\n")
	for i, width := range ordered {
		code := 11 + i
		aperture[width] = code
		fmt.Fprintf(&b, "%%ADD%dC,%s*%%\n", code, gerberMM(width))
	}
	for _, zone := range def.Zones {
		if zone.Layer == layerID && len(zone.Polygon) >= 3 {
			writeGerberRegion(&b, zone.Polygon)
		}
	}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if !containsString(pad.Layers, layerID) {
				continue
			}
			placed := placePad(component, pad)
			writeGerberRegion(&b, padPolygon(placed))
		}
	}
	for _, via := range def.Vias {
		if layerBetween(def, layerID, via.FromLayer, via.ToLayer) {
			writeGerberRegion(&b, circlePolygon(Point{XNM: via.XNM, YNM: via.YNM}, via.DiameterNM/2, 24))
		}
	}
	for _, trace := range def.Traces {
		if trace.Layer != layerID || len(trace.Points) < 2 {
			continue
		}
		fmt.Fprintf(&b, "D%d*\n", aperture[trace.WidthNM])
		fmt.Fprintf(&b, "%sD02*\n", gerberPoint(trace.Points[0]))
		for _, point := range trace.Points[1:] {
			fmt.Fprintf(&b, "%sD01*\n", gerberPoint(point))
		}
	}
	b.WriteString("M02*\n")
	return []byte(b.String())
}

func renderSilkscreenGerber(def *Definition, layerID string) []byte {
	front := strings.HasPrefix(strings.ToUpper(layerID), "F.")
	var b strings.Builder
	b.WriteString("G04 Apteva PCB Studio native silkscreen*\n%FSLAX46Y46*%\n%MOMM*%\n%LPD*%\n%ADD10C,0.150000*%\nD10*\n")
	for _, component := range def.Components {
		if (component.Position.Side == "front") != front {
			continue
		}
		body := component.Body
		if body == nil || body.WidthNM <= 0 || body.HeightNM <= 0 {
			body = &Body{WidthNM: 3_000_000, HeightNM: 2_000_000}
		}
		outline := bodyPolygon(component, *body)
		if len(outline) == 0 {
			continue
		}
		fmt.Fprintf(&b, "%sD02*\n", gerberPoint(outline[0]))
		for _, point := range append(outline[1:], outline[0]) {
			fmt.Fprintf(&b, "%sD01*\n", gerberPoint(point))
		}
	}
	b.WriteString("M02*\n")
	return []byte(b.String())
}

func renderEdgeGerber(def *Definition) []byte {
	points := []Point{{XNM: 0, YNM: 0}, {XNM: def.Board.WidthNM, YNM: 0}, {XNM: def.Board.WidthNM, YNM: def.Board.HeightNM}, {XNM: 0, YNM: def.Board.HeightNM}, {XNM: 0, YNM: 0}}
	var b strings.Builder
	b.WriteString("G04 Apteva PCB Studio board outline*\n%FSLAX46Y46*%\n%MOMM*%\n%LPD*%\n%ADD10C,0.100000*%\nD10*\n")
	fmt.Fprintf(&b, "%sD02*\n", gerberPoint(points[0]))
	for _, point := range points[1:] {
		fmt.Fprintf(&b, "%sD01*\n", gerberPoint(point))
	}
	b.WriteString("M02*\n")
	return []byte(b.String())
}

func renderExcellon(def *Definition) []byte {
	type hole struct {
		Point Point
		Drill int64
	}
	holes := []hole{}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if pad.DrillNM > 0 {
				holes = append(holes, hole{Point: placePad(component, pad).Center, Drill: pad.DrillNM})
			}
		}
	}
	for _, via := range def.Vias {
		if via.DrillNM > 0 {
			holes = append(holes, hole{Point: Point{XNM: via.XNM, YNM: via.YNM}, Drill: via.DrillNM})
		}
	}
	drills := map[int64]bool{}
	for _, hole := range holes {
		drills[hole.Drill] = true
	}
	ordered := make([]int64, 0, len(drills))
	for drill := range drills {
		ordered = append(ordered, drill)
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i] < ordered[j] })
	tools := map[int64]int{}
	var b strings.Builder
	b.WriteString("M48\n; Apteva PCB Studio native Excellon\nMETRIC,TZ\n")
	for i, drill := range ordered {
		tool := i + 1
		tools[drill] = tool
		fmt.Fprintf(&b, "T%02dC%s\n", tool, gerberMM(drill))
	}
	b.WriteString("%\nG90\n")
	sort.Slice(holes, func(i, j int) bool {
		if holes[i].Drill != holes[j].Drill {
			return holes[i].Drill < holes[j].Drill
		}
		if holes[i].Point.XNM != holes[j].Point.XNM {
			return holes[i].Point.XNM < holes[j].Point.XNM
		}
		return holes[i].Point.YNM < holes[j].Point.YNM
	})
	current := 0
	for _, hole := range holes {
		tool := tools[hole.Drill]
		if tool != current {
			fmt.Fprintf(&b, "T%02d\n", tool)
			current = tool
		}
		fmt.Fprintf(&b, "X%.6fY%.6f\n", float64(hole.Point.XNM)/1e6, float64(hole.Point.YNM)/1e6)
	}
	b.WriteString("M30\n")
	return []byte(b.String())
}

func writeGerberRegion(b *strings.Builder, polygon []Point) {
	if len(polygon) < 3 {
		return
	}
	b.WriteString("G36*\n")
	fmt.Fprintf(b, "%sD02*\n", gerberPoint(polygon[0]))
	for _, point := range append(polygon[1:], polygon[0]) {
		fmt.Fprintf(b, "%sD01*\n", gerberPoint(point))
	}
	b.WriteString("G37*\n")
}

func padPolygon(pad *placedPad) []Point {
	shape := strings.ToLower(pad.Pad.Shape)
	if shape == "circle" {
		return circlePolygon(pad.Center, min64(pad.Pad.WidthNM, pad.Pad.HeightNM)/2, 24)
	}
	// Rectangular regions preserve exact rotation and outer copper dimensions.
	// roundrect/oval corner radii remain a visualization hint in v0.2; the
	// manufacturing writer deliberately emits the conservative outer envelope.
	return pad.Corners
}

func circlePolygon(center Point, radius int64, segments int) []Point {
	out := make([]Point, segments)
	for i := range out {
		angle := 2 * math.Pi * float64(i) / float64(segments)
		out[i] = Point{XNM: center.XNM + int64(math.Round(math.Cos(angle)*float64(radius))), YNM: center.YNM + int64(math.Round(math.Sin(angle)*float64(radius)))}
	}
	return out
}

func bodyPolygon(component Component, body Body) []Point {
	pad := Pad{XNM: 0, YNM: 0, WidthNM: body.WidthNM, HeightNM: body.HeightNM}
	return placePad(component, pad).Corners
}

func gerberPoint(point Point) string { return fmt.Sprintf("X%dY%d", point.XNM, point.YNM) }
func gerberMM(nm int64) string       { return fmt.Sprintf("%.6f", float64(nm)/1e6) }
func gerberFilename(layer string) string {
	replacer := strings.NewReplacer(".", "_", "/", "_", " ", "_")
	return replacer.Replace(layer)
}
func copperLayerCount(def *Definition) int {
	count := 0
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" {
			count++
		}
	}
	return count
}
func layerBetween(def *Definition, target, from, to string) bool {
	orders := map[string]int{}
	for _, layer := range def.Board.Layers {
		orders[layer.ID] = layer.Order
	}
	t, tok := orders[target]
	f, fok := orders[from]
	end, eok := orders[to]
	if !tok || !fok || !eok {
		return false
	}
	if f > end {
		f, end = end, f
	}
	return t >= f && t <= end
}
func gerberJobFiles(files map[string][]byte) []map[string]any {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, name := range names {
		if name == "board.gbrjob" {
			continue
		}
		out = append(out, map[string]any{"Path": name, "FileFunction": gerberFileFunction(name)})
	}
	return out
}
func gerberFileFunction(name string) string {
	switch {
	case strings.Contains(name, "F_Cu"):
		return "Copper,L1,Top"
	case strings.Contains(name, "B_Cu"):
		return "Copper,L2,Bot"
	case strings.Contains(name, "Silk"):
		return "Legend,Top"
	case strings.Contains(name, "Edge_Cuts"):
		return "Profile,NP"
	case strings.HasSuffix(name, ".drl"):
		return "Plated,1,2,PTH"
	default:
		return "Other"
	}
}

// zipManufacturingSet is separate from deterministicZip to make tests and
// callers explicit about which files are manufacturing outputs.
func zipManufacturingSet(def *Definition) ([]byte, error) {
	return deterministicZip(manufacturingFiles(def))
}

func manufacturingFileSummary(def *Definition) []string {
	files := manufacturingFiles(def)
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

const (
	defaultSolderMaskExpansion = int64(50_000)
	pthDrillPath               = "drill/board-PTH.drl"
	npthDrillPath              = "drill/board-NPTH.drl"
)

// fabExtras lets derived outputs (panels) reuse the native writer: they
// replace the Edge_Cuts profile and add extra drawing layers (V-score) that
// must also be listed in the Gerber job.
type fabExtras struct {
	EdgeContours []contour
	Layers       []fabExtraLayer
}

type fabExtraLayer struct {
	Path, Function, Polarity string
	Body                     []byte
}

// manufacturingFiles writes a deterministic, dependency-free Gerber X2
// fabrication set plus Excellon drills: every copper layer (numbered in stack
// order), solder mask and paste per side, silkscreen, an arc-exact Edge_Cuts
// profile, separate PTH and NPTH drill programs (with G85 slots) and a Gerber
// job file describing them all.
func manufacturingFiles(def *Definition) map[string][]byte {
	return manufacturingFilesWith(def, nil)
}

func manufacturingFilesWith(def *Definition, extras *fabExtras) map[string][]byte {
	files := map[string][]byte{}
	functions := map[string][2]string{}
	put := func(path, function, polarity string, body []byte) {
		files[path] = body
		functions[path] = [2]string{function, polarity}
	}
	copper := copperLayers(def)
	for index, layer := range copper {
		function := copperFileFunction(index, len(copper))
		put("gerbers/"+gerberFilename(layer.ID)+".gbr", function, "Positive", renderCopperGerber(def, layer.ID, function))
	}
	for _, layer := range def.Board.Layers {
		if layer.Kind == "silkscreen" {
			function := "Legend,Top"
			if strings.HasPrefix(strings.ToUpper(layer.ID), "B.") {
				function = "Legend,Bot"
			}
			put("gerbers/"+gerberFilename(layer.ID)+".gbr", function, "Positive", renderSilkscreenGerber(def, layer.ID, function))
		}
	}
	for _, side := range outerSides(def) {
		put("gerbers/"+side.Prefix+"_Mask.gbr", "Soldermask,"+side.Name, "Negative", renderMaskGerber(def, side))
		if len(pastePads(def, side)) > 0 {
			put("gerbers/"+side.Prefix+"_Paste.gbr", "Paste,"+side.Name, "Positive", renderPasteGerber(def, side))
		}
	}
	edges := boardEdgeContours(def.Board)
	if extras != nil && extras.EdgeContours != nil {
		edges = extras.EdgeContours
	}
	put("gerbers/Edge_Cuts.gbr", "Profile,NP", "Positive", renderEdgeGerber(edges))
	if extras != nil {
		for _, layer := range extras.Layers {
			put(layer.Path, layer.Function, layer.Polarity, layer.Body)
		}
	}
	pth, npth := drillFeatures(def)
	n := len(copper)
	if n == 0 {
		n = 1
	}
	pthFunction, npthFunction := fmt.Sprintf("Plated,1,%d,PTH", n), fmt.Sprintf("NonPlated,1,%d,NPTH", n)
	put(pthDrillPath, pthFunction, "", renderExcellonProgram(pth, pthFunction))
	put(npthDrillPath, npthFunction, "", renderExcellonProgram(npth, npthFunction))
	job := map[string]any{
		"Header":          map[string]any{"GenerationSoftware": map[string]any{"Vendor": "Apteva", "Application": "PCB Studio", "Version": engineVersion}, "CreationDate": "1980-01-01T00:00:00Z"},
		"GeneralSpecs":    map[string]any{"ProjectId": map[string]any{"Name": def.Name}, "Size": map[string]any{"X": float64(def.Board.WidthNM) / 1e6, "Y": float64(def.Board.HeightNM) / 1e6}, "LayerNumber": copperLayerCount(def), "BoardThickness": float64(def.Board.ThicknessNM) / 1e6},
		"FilesAttributes": gerberJobFiles(functions),
	}
	body, _ := json.MarshalIndent(job, "", "  ")
	files["board.gbrjob"] = append(body, '\n')
	return files
}

// ---------------------------------------------------------------- Gerber writer

// gerberWriter collects apertures and body commands so apertures can be
// declared in deterministic first-use order ahead of the image.
type gerberWriter struct {
	apertures map[string]int
	order     []string
	body      strings.Builder
	current   int
	mode      string
}

func newGerberWriter() *gerberWriter {
	w := &gerberWriter{apertures: map[string]int{}}
	w.aperture("C," + gerberMM(100_000))
	return w
}

func (g *gerberWriter) aperture(spec string) int {
	if code, ok := g.apertures[spec]; ok {
		return code
	}
	code := 10 + len(g.order)
	g.apertures[spec] = code
	g.order = append(g.order, spec)
	return code
}

func (g *gerberWriter) use(code int) {
	if code != g.current {
		fmt.Fprintf(&g.body, "D%d*\n", code)
		g.current = code
	}
}

func (g *gerberWriter) setMode(mode string) {
	if g.mode != mode {
		g.body.WriteString(mode + "*\n")
		g.mode = mode
	}
}

func (g *gerberWriter) path(c contour) {
	fmt.Fprintf(&g.body, "%sD02*\n", gerberPoint(c.Start))
	current := c.Start
	for _, seg := range c.Segs {
		if seg.Arc {
			if seg.CW {
				g.setMode("G02")
			} else {
				g.setMode("G03")
			}
			fmt.Fprintf(&g.body, "%sI%dJ%dD01*\n", gerberPoint(seg.End), seg.Center.XNM-current.XNM, seg.Center.YNM-current.YNM)
		} else {
			g.setMode("G01")
			fmt.Fprintf(&g.body, "%sD01*\n", gerberPoint(seg.End))
		}
		current = seg.End
	}
}

func (g *gerberWriter) region(c contour) {
	if len(c.Segs) == 0 {
		return
	}
	g.body.WriteString("G36*\n")
	g.path(c)
	g.body.WriteString("G37*\n")
}

func (g *gerberWriter) stroke(width int64, c contour) {
	if len(c.Segs) == 0 {
		return
	}
	g.use(g.aperture("C," + gerberMM(width)))
	g.path(c)
}

func (g *gerberWriter) polyline(width int64, points []Point) {
	if len(points) < 2 {
		return
	}
	g.use(g.aperture("C," + gerberMM(width)))
	fmt.Fprintf(&g.body, "%sD02*\n", gerberPoint(points[0]))
	g.setMode("G01")
	for _, point := range points[1:] {
		fmt.Fprintf(&g.body, "%sD01*\n", gerberPoint(point))
	}
}

func (g *gerberWriter) flash(spec string, at Point) {
	g.use(g.aperture(spec))
	fmt.Fprintf(&g.body, "%sD03*\n", gerberPoint(at))
}

func (g *gerberWriter) render(comment, function, polarity string) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "G04 Apteva PCB Studio %s*\n", comment)
	fmt.Fprintf(&b, "%%TF.GenerationSoftware,Apteva,PCB Studio,%s*%%\n", engineVersion)
	fmt.Fprintf(&b, "%%TF.FileFunction,%s*%%\n", function)
	if polarity != "" {
		fmt.Fprintf(&b, "%%TF.FilePolarity,%s*%%\n", polarity)
	}
	b.WriteString("%FSLAX46Y46*%\n%MOMM*%\n%LPD*%\n")
	for _, spec := range g.order {
		fmt.Fprintf(&b, "%%ADD%d%s*%%\n", g.apertures[spec], spec)
	}
	b.WriteString("G75*\n")
	b.WriteString(g.body.String())
	b.WriteString("M02*\n")
	return []byte(b.String())
}

// writePad emits one pad as its true shape: circles as C flashes, rect and
// oval pads at multiples of 90 degrees as R/O flashes, and everything else
// (rotated pads, roundrect) as an exact region with G02/G03 corner arcs.
func writePad(g *gerberWriter, component Component, pad Pad, expansion int64) {
	shape := padShapeName(pad)
	center := placePad(component, pad).Center
	if shape == "circle" {
		if d := min64(pad.WidthNM, pad.HeightNM) + 2*expansion; d > 0 {
			g.flash("C,"+gerberMM(d), center)
		}
		return
	}
	w, h := pad.WidthNM+2*expansion, pad.HeightNM+2*expansion
	if w <= 0 || h <= 0 {
		return
	}
	rotation := ((component.Position.RotationUdeg % 360_000_000) + 360_000_000) % 360_000_000
	if (shape == "rect" || shape == "oval") && rotation%90_000_000 == 0 {
		if (rotation/90_000_000)%2 == 1 {
			w, h = h, w
		}
		switch {
		case shape == "oval" && w == h:
			g.flash("C,"+gerberMM(w), center)
		case shape == "oval":
			g.flash("O,"+gerberMM(w)+"X"+gerberMM(h), center)
		default:
			g.flash("R,"+gerberMM(w)+"X"+gerberMM(h), center)
		}
		return
	}
	g.region(placedPadContour(component, pad, expansion))
}

// ---------------------------------------------------------------- layers

type boardSide struct {
	Name, Prefix, CopperID string
}

func copperLayers(def *Definition) []Layer {
	out := []Layer{}
	for _, layer := range def.Board.Layers {
		if layer.Kind == "copper" {
			out = append(out, layer)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Order < out[j].Order })
	return out
}

// copperFileFunction numbers copper in stack order: L1 Top, Ln Inr, LN Bot.
func copperFileFunction(index, count int) string {
	position := "Inr"
	switch {
	case index == 0:
		position = "Top"
	case index == count-1:
		position = "Bot"
	}
	return fmt.Sprintf("Copper,L%d,%s", index+1, position)
}

func outerSides(def *Definition) []boardSide {
	copper := copperLayers(def)
	if len(copper) == 0 {
		return nil
	}
	sides := []boardSide{{Name: "Top", Prefix: "F", CopperID: copper[0].ID}}
	if len(copper) > 1 {
		sides = append(sides, boardSide{Name: "Bot", Prefix: "B", CopperID: copper[len(copper)-1].ID})
	}
	return sides
}

func renderCopperGerber(def *Definition, layerID, function string) []byte {
	g := newGerberWriter()
	for _, zone := range def.Zones {
		if zone.Layer == layerID && len(zone.Polygon) >= 3 {
			g.region(lineContour(zone.Polygon))
		}
	}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if pad.NonPlated && min64(pad.WidthNM, pad.HeightNM) <= max64(pad.DrillNM, min64(pad.DrillWidthNM, pad.DrillHeightNM)) {
				continue // NPTH with no copper beyond the hole, as KiCad emits it
			}
			if containsString(pad.Layers, layerID) {
				writePad(g, component, pad, 0)
			}
		}
	}
	for _, via := range def.Vias {
		if via.DiameterNM > 0 && layerBetween(def, layerID, via.FromLayer, via.ToLayer) {
			g.flash("C,"+gerberMM(via.DiameterNM), Point{XNM: via.XNM, YNM: via.YNM})
		}
	}
	for _, trace := range def.Traces {
		if trace.Layer == layerID && len(trace.Points) >= 2 && trace.WidthNM > 0 {
			g.polyline(trace.WidthNM, trace.Points)
		}
	}
	return g.render("native copper", function, "Positive")
}

func padMaskExpansion(def *Definition, pad Pad) int64 {
	if pad.SolderMaskExpansionNM != 0 {
		return pad.SolderMaskExpansionNM
	}
	return boardMaskExpansion(def)
}

func boardMaskExpansion(def *Definition) int64 {
	if def.Rules.SolderMaskExpansionNM > 0 {
		return def.Rules.SolderMaskExpansionNM
	}
	return defaultSolderMaskExpansion
}

// maskOpeningCount is the number of openings renderMaskGerber draws: every
// pad on the side, mounting holes, and vias only when untented.
func maskOpeningCount(def *Definition, side boardSide) int {
	count := 0
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if containsString(pad.Layers, side.CopperID) {
				count++
			}
		}
	}
	if def.Rules.UntentedVias {
		for _, via := range def.Vias {
			if layerBetween(def, side.CopperID, via.FromLayer, via.ToLayer) {
				count++
			}
		}
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			if hole.DiameterNM > 0 {
				count++
			}
		}
	}
	return count
}

// renderMaskGerber draws solder-mask openings (negative polarity): pads grown
// by their mask expansion, mounting holes, and vias only when untented.
func renderMaskGerber(def *Definition, side boardSide) []byte {
	g := newGerberWriter()
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if containsString(pad.Layers, side.CopperID) {
				writePad(g, component, pad, padMaskExpansion(def, pad))
			}
		}
	}
	expansion := boardMaskExpansion(def)
	if def.Rules.UntentedVias {
		for _, via := range def.Vias {
			if layerBetween(def, side.CopperID, via.FromLayer, via.ToLayer) {
				g.flash("C,"+gerberMM(via.DiameterNM+2*expansion), Point{XNM: via.XNM, YNM: via.YNM})
			}
		}
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			if hole.DiameterNM > 0 {
				g.flash("C,"+gerberMM(hole.DiameterNM+2*expansion), Point{XNM: hole.XNM, YNM: hole.YNM})
			}
		}
	}
	return g.render("solder mask openings", "Soldermask,"+side.Name, "Negative")
}

type pasteAperture struct {
	Component Component
	Pad       Pad
}

// pastePad returns the stencil aperture for an SMD pad: the pad size plus
// paste_expansion_nm and paste_ratio × size per axis. Through-hole pads and
// no_paste pads never get paste.
func pastePad(pad Pad) (Pad, bool) {
	if pad.DrillNM > 0 || pad.DrillWidthNM > 0 || pad.DrillHeightNM > 0 || pad.NoPaste {
		return pad, false
	}
	dx := pad.PasteExpansionNM + int64(math.Round(pad.PasteRatio*float64(pad.WidthNM)))
	dy := pad.PasteExpansionNM + int64(math.Round(pad.PasteRatio*float64(pad.HeightNM)))
	out := pad
	if padShapeName(pad) == "circle" {
		d := min64(dx, dy)
		out.WidthNM, out.HeightNM = min64(pad.WidthNM, pad.HeightNM)+2*d, min64(pad.WidthNM, pad.HeightNM)+2*d
	} else {
		out.WidthNM, out.HeightNM = pad.WidthNM+2*dx, pad.HeightNM+2*dy
		if padShapeName(pad) == "roundrect" && min64(out.WidthNM, out.HeightNM) > 0 {
			// Keep the absolute corner radius shrinking with the aperture.
			radius := max64(0, padCornerRadius(pad)+min64(dx, dy))
			out.RoundrectRatio = float64(radius) / float64(min64(out.WidthNM, out.HeightNM))
			if out.RoundrectRatio == 0 {
				out.Shape = "rect"
			}
		}
	}
	return out, out.WidthNM > 0 && out.HeightNM > 0
}

func pastePads(def *Definition, side boardSide) []pasteAperture {
	out := []pasteAperture{}
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if !containsString(pad.Layers, side.CopperID) {
				continue
			}
			if paste, ok := pastePad(pad); ok {
				out = append(out, pasteAperture{Component: component, Pad: paste})
			}
		}
	}
	return out
}

func renderPasteGerber(def *Definition, side boardSide) []byte {
	g := newGerberWriter()
	for _, item := range pastePads(def, side) {
		writePad(g, item.Component, item.Pad, 0)
	}
	return g.render("solder paste", "Paste,"+side.Name, "Positive")
}

func renderSilkscreenGerber(def *Definition, layerID, function string) []byte {
	front := strings.HasPrefix(strings.ToUpper(layerID), "F.")
	g := newGerberWriter()
	g.use(g.aperture("C," + gerberMM(150_000)))
	for _, component := range def.Components {
		if (component.Position.Side == "front") != front || strings.HasPrefix(component.Footprint, "Fiducial") {
			continue
		}
		outline := bodyPolygon(component, componentBody(component))
		if len(outline) == 0 {
			continue
		}
		g.polyline(150_000, append(outline, outline[0]))
	}
	return g.render("native silkscreen", function, "Positive")
}

func renderEdgeGerber(contours []contour) []byte {
	g := newGerberWriter()
	for _, c := range contours {
		g.stroke(100_000, c)
	}
	return g.render("board outline", "Profile,NP", "Positive")
}

// ---------------------------------------------------------------- drills

// drillFeature is one Excellon hit: a round hole (Start only) or a G85 routed
// slot from Start to End with the tool diameter as slot width.
type drillFeature struct {
	Start, End Point
	Diameter   int64
	Slot       bool
}

func slotFeature(center Point, length, width int64, angle float64) drillFeature {
	if length <= width {
		return drillFeature{Start: center, End: center, Diameter: width}
	}
	half := float64(length-width) / 2
	dx, dy := int64(math.Round(half*math.Cos(angle))), int64(math.Round(half*math.Sin(angle)))
	start, end := Point{XNM: center.XNM - dx, YNM: center.YNM - dy}, Point{XNM: center.XNM + dx, YNM: center.YNM + dy}
	if end.XNM < start.XNM || end.XNM == start.XNM && end.YNM < start.YNM {
		start, end = end, start
	}
	return drillFeature{Start: start, End: end, Diameter: width, Slot: true}
}

// padDrillFeature converts a pad's round or oval drill into board space. Oval
// drills follow the pad's long drill axis, rotated with the component.
func padDrillFeature(component Component, pad Pad) (drillFeature, bool) {
	center := placePad(component, pad).Center
	if pad.DrillWidthNM > 0 && pad.DrillHeightNM > 0 {
		angle := componentAngle(component)
		length, width := pad.DrillWidthNM, pad.DrillHeightNM
		if pad.DrillHeightNM > pad.DrillWidthNM {
			length, width = pad.DrillHeightNM, pad.DrillWidthNM
			angle += math.Pi / 2
		}
		return slotFeature(center, length, width, angle), true
	}
	if pad.DrillNM > 0 {
		return drillFeature{Start: center, End: center, Diameter: pad.DrillNM}, true
	}
	return drillFeature{}, false
}

// drillFeatures splits every hole into plated (pads, vias, plated mounting
// holes and cutouts) and non-plated programs.
func drillFeatures(def *Definition) (pth, npth []drillFeature) {
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			feature, ok := padDrillFeature(component, pad)
			if !ok {
				continue
			}
			if pad.NonPlated {
				npth = append(npth, feature)
			} else {
				pth = append(pth, feature)
			}
		}
	}
	for _, via := range def.Vias {
		if via.DrillNM > 0 {
			at := Point{XNM: via.XNM, YNM: via.YNM}
			pth = append(pth, drillFeature{Start: at, End: at, Diameter: via.DrillNM})
		}
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			if hole.DiameterNM <= 0 {
				continue
			}
			at := Point{XNM: hole.XNM, YNM: hole.YNM}
			feature := drillFeature{Start: at, End: at, Diameter: hole.DiameterNM}
			if hole.Plated {
				pth = append(pth, feature)
			} else {
				npth = append(npth, feature)
			}
		}
	}
	for _, cutout := range def.Board.Cutouts {
		if !cutout.Plated {
			continue
		}
		center := Point{XNM: cutout.XNM, YNM: cutout.YNM}
		switch cutout.Kind {
		case "circle":
			if cutout.DiameterNM > 0 {
				pth = append(pth, drillFeature{Start: center, End: center, Diameter: cutout.DiameterNM})
			}
		case "slot":
			angle := float64(cutout.RotationUdeg) / 1e6 * math.Pi / 180
			length, width := cutout.WidthNM, cutout.HeightNM
			if cutout.HeightNM > cutout.WidthNM {
				length, width = cutout.HeightNM, cutout.WidthNM
				angle += math.Pi / 2
			}
			if width > 0 {
				pth = append(pth, slotFeature(center, length, width, angle))
			}
		}
	}
	return pth, npth
}

func excellonMM(nm int64) string { return fmt.Sprintf("%.6f", float64(nm)/1e6) }

func excellonPoint(p Point) string { return "X" + excellonMM(p.XNM) + "Y" + excellonMM(p.YNM) }

func renderExcellonProgram(features []drillFeature, function string) []byte {
	features = append([]drillFeature(nil), features...)
	sort.Slice(features, func(i, j int) bool {
		a, b := features[i], features[j]
		if a.Diameter != b.Diameter {
			return a.Diameter < b.Diameter
		}
		if a.Slot != b.Slot {
			return !a.Slot
		}
		if a.Start != b.Start {
			if a.Start.XNM != b.Start.XNM {
				return a.Start.XNM < b.Start.XNM
			}
			return a.Start.YNM < b.Start.YNM
		}
		if a.End.XNM != b.End.XNM {
			return a.End.XNM < b.End.XNM
		}
		return a.End.YNM < b.End.YNM
	})
	tools := map[int64]int{}
	ordered := []int64{}
	for _, feature := range features {
		if _, ok := tools[feature.Diameter]; !ok {
			tools[feature.Diameter] = len(ordered) + 1
			ordered = append(ordered, feature.Diameter)
		}
	}
	var b strings.Builder
	b.WriteString("M48\n; Apteva PCB Studio native Excellon\n")
	fmt.Fprintf(&b, "; #@! TF.GenerationSoftware,Apteva,PCB Studio,%s\n", engineVersion)
	fmt.Fprintf(&b, "; #@! TF.FileFunction,%s\n", function)
	b.WriteString("FMAT,2\nMETRIC,TZ\n")
	for i, diameter := range ordered {
		fmt.Fprintf(&b, "T%02dC%s\n", i+1, excellonMM(diameter))
	}
	b.WriteString("%\nG90\nG05\n")
	current := 0
	for _, feature := range features {
		tool := tools[feature.Diameter]
		if tool != current {
			fmt.Fprintf(&b, "T%02d\n", tool)
			current = tool
		}
		if feature.Slot {
			fmt.Fprintf(&b, "%sG85%s\n", excellonPoint(feature.Start), excellonPoint(feature.End))
		} else {
			fmt.Fprintf(&b, "%s\n", excellonPoint(feature.Start))
		}
	}
	b.WriteString("T0\nM30\n")
	return []byte(b.String())
}

// ---------------------------------------------------------------- helpers

func circlePolygon(center Point, radius int64, segments int) []Point {
	out := make([]Point, segments)
	for i := range out {
		angle := 2 * math.Pi * float64(i) / float64(segments)
		out[i] = Point{XNM: center.XNM + int64(math.Round(math.Cos(angle)*float64(radius))), YNM: center.YNM + int64(math.Round(math.Sin(angle)*float64(radius)))}
	}
	return out
}

// boardCutoutContour is the flattened outline of a cutout, used by placement.
func boardCutoutContour(cutout BoardCutout) []Point {
	return cutoutEdgeContour(cutout).polygon()
}

func bodyPolygon(component Component, body Body) []Point {
	pad := Pad{XNM: body.OffsetXNM, YNM: body.OffsetYNM, WidthNM: body.WidthNM, HeightNM: body.HeightNM}
	return placePad(component, pad).Corners
}

func gerberPoint(point Point) string { return fmt.Sprintf("X%dY%d", point.XNM, point.YNM) }
func gerberMM(nm int64) string       { return fmt.Sprintf("%.6f", float64(nm)/1e6) }
func gerberFilename(layer string) string {
	replacer := strings.NewReplacer(".", "_", "/", "_", " ", "_")
	return replacer.Replace(layer)
}
func copperLayerCount(def *Definition) int {
	return len(copperLayers(def))
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

func gerberJobFiles(functions map[string][2]string) []map[string]any {
	names := make([]string, 0, len(functions))
	for name := range functions {
		names = append(names, name)
	}
	sort.Strings(names)
	out := []map[string]any{}
	for _, name := range names {
		entry := map[string]any{"Path": name, "FileFunction": functions[name][0]}
		if polarity := functions[name][1]; polarity != "" {
			entry["FilePolarity"] = polarity
		}
		out = append(out, entry)
	}
	return out
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

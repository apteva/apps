package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

const (
	panelSchema               = "apteva-pcb-panel/v1"
	defaultPanelRailWidth     = int64(5_000_000)
	defaultPanelTabSpacing    = int64(2_000_000)
	defaultPanelTabWidth      = int64(3_000_000)
	panelToolingHoleDiameter  = int64(2_000_000)
	panelToolingHoleInset     = int64(5_000_000)
	panelFiducialDiameter     = int64(1_000_000)
	panelFiducialMaskOpening  = int64(2_000_000)
	panelFiducialInset        = int64(10_000_000)
	panelMouseBiteDiameter    = int64(500_000)
	panelMouseBitePitch       = int64(800_000)
	panelVScoreLinePath       = "gerbers/V_Score.gbr"
	panelVScoreFileFunction   = "Other,V-Score"
	panelMinimumRailLengthNM  = int64(25_000_000)
	panelMaximumSpacingNM     = int64(20_000_000)
	panelMinimumTabSpacingNM  = int64(1_000_000)
	panelMinimumRailWidthNM   = int64(3_000_000)
	panelMaximumRailWidthNM   = int64(30_000_000)
	panelMinimumTabWidthNM    = int64(1_000_000)
	panelMaximumTabWidthNM    = int64(20_000_000)
	panelMaximumGridDimension = 20
)

// PanelOptions describes an N×M production panel. Panelization is an output
// derived from one revision; it never edits the design.
type PanelOptions struct {
	Rows        int    `json:"rows"`
	Columns     int    `json:"columns"`
	SpacingNM   int64  `json:"spacing_nm"`
	RailWidthNM int64  `json:"rail_width_nm"`
	RailSides   string `json:"rail_sides,omitempty"` // top_bottom (default) | left_right | all
	Separation  string `json:"separation"`           // vscore (default) | tabs
	TabWidthNM  int64  `json:"tab_width_nm,omitempty"`
}

// normalize applies defaults and validates the options in place.
func (o *PanelOptions) normalize() error {
	if o.Rows < 1 || o.Rows > panelMaximumGridDimension || o.Columns < 1 || o.Columns > panelMaximumGridDimension {
		return fmt.Errorf("rows and columns must each be between 1 and %d", panelMaximumGridDimension)
	}
	if o.Separation == "" {
		o.Separation = "vscore"
	}
	if o.Separation != "vscore" && o.Separation != "tabs" {
		return errors.New(`separation must be "vscore" or "tabs"`)
	}
	if o.RailSides == "" {
		o.RailSides = "top_bottom"
	}
	if o.RailSides != "top_bottom" && o.RailSides != "left_right" && o.RailSides != "all" {
		return errors.New(`rail_sides must be "top_bottom", "left_right" or "all"`)
	}
	if o.RailWidthNM == 0 {
		o.RailWidthNM = defaultPanelRailWidth
	}
	if o.RailWidthNM < panelMinimumRailWidthNM || o.RailWidthNM > panelMaximumRailWidthNM {
		return fmt.Errorf("rail_width_nm must be between %d and %d", panelMinimumRailWidthNM, panelMaximumRailWidthNM)
	}
	if o.SpacingNM < 0 || o.SpacingNM > panelMaximumSpacingNM {
		return fmt.Errorf("spacing_nm must be between 0 and %d", panelMaximumSpacingNM)
	}
	if o.Separation == "tabs" {
		if o.SpacingNM == 0 {
			o.SpacingNM = defaultPanelTabSpacing
		}
		if o.SpacingNM < panelMinimumTabSpacingNM {
			return fmt.Errorf("tab-routed panels need spacing_nm >= %d for the router", panelMinimumTabSpacingNM)
		}
		if o.TabWidthNM == 0 {
			o.TabWidthNM = defaultPanelTabWidth
		}
		if o.TabWidthNM < panelMinimumTabWidthNM || o.TabWidthNM > panelMaximumTabWidthNM {
			return fmt.Errorf("tab_width_nm must be between %d and %d", panelMinimumTabWidthNM, panelMaximumTabWidthNM)
		}
	} else if o.TabWidthNM != 0 {
		return errors.New("tab_width_nm only applies to separation=tabs")
	}
	return nil
}

type PanelBoard struct {
	Row      int   `json:"row"`
	Column   int   `json:"column"`
	OffsetX  int64 `json:"offset_x_nm"`
	OffsetY  int64 `json:"offset_y_nm"`
	OriginX  int64 `json:"origin_x_nm"`
	OriginY  int64 `json:"origin_y_nm"`
	WidthNM  int64 `json:"width_nm"`
	HeightNM int64 `json:"height_nm"`
}

type PanelLine struct {
	Axis       string `json:"axis"` // x: vertical line at x; y: horizontal line at y
	PositionNM int64  `json:"position_nm"`
}

type PanelRect struct {
	XNM      int64 `json:"x_nm"`
	YNM      int64 `json:"y_nm"`
	WidthNM  int64 `json:"width_nm"`
	HeightNM int64 `json:"height_nm"`
}

type PanelReport struct {
	Schema        string                   `json:"schema"`
	Engine        string                   `json:"engine"`
	Options       PanelOptions             `json:"options"`
	WidthNM       int64                    `json:"width_nm"`
	HeightNM      int64                    `json:"height_nm"`
	BoardWidthNM  int64                    `json:"board_width_nm"`
	BoardHeightNM int64                    `json:"board_height_nm"`
	Boards        []PanelBoard             `json:"boards"`
	Rails         []PanelRect              `json:"rails"`
	Fiducials     []Point                  `json:"fiducials"`
	ToolingHoles  []Point                  `json:"tooling_holes"`
	VScores       []PanelLine              `json:"v_scores,omitempty"`
	Tabs          []PanelRect              `json:"tabs,omitempty"`
	MouseBites    int                      `json:"mouse_bite_holes,omitempty"`
	Warnings      []string                 `json:"warnings"`
	Files         []string                 `json:"files"`
	Verification  *FabricationVerification `json:"verification,omitempty"`
}

type panelRect struct{ x0, y0, x1, y1 int64 }

func (r panelRect) contains(x, y float64) bool {
	return x > float64(r.x0) && x < float64(r.x1) && y > float64(r.y0) && y < float64(r.y1)
}

func (r panelRect) report() PanelRect {
	return PanelRect{XNM: r.x0, YNM: r.y0, WidthNM: r.x1 - r.x0, HeightNM: r.y1 - r.y0}
}

// panelizeFiles builds the panel definition, its full manufacturing set
// (copper, mask, paste, silkscreen, Edge_Cuts, PTH/NPTH, V-score, gbrjob) and
// a panel.json description, verified by the independent fabrication parser.
func panelizeFiles(def *Definition, options PanelOptions) (map[string][]byte, *PanelReport, error) {
	if err := options.normalize(); err != nil {
		return nil, nil, err
	}
	panel, extras, report, err := buildPanel(def, options)
	if err != nil {
		return nil, nil, err
	}
	files := manufacturingFilesWith(panel, extras)
	edges := boardEdgeContours(panel.Board)
	if extras.EdgeContours != nil {
		edges = extras.EdgeContours
	}
	report.Verification = verifyManufacturingFilesWith(panel, files, fabVerifyExpect{EdgeDraws: contourSegmentCount(edges)})
	for name := range files {
		report.Files = append(report.Files, name)
	}
	report.Files = append(report.Files, "panel.json")
	sort.Strings(report.Files)
	body, _ := json.MarshalIndent(report, "", "  ")
	files["panel.json"] = append(body, '\n')
	return files, report, nil
}

func buildPanel(def *Definition, o PanelOptions) (*Definition, *fabExtras, *PanelReport, error) {
	outline := boardOutline(def.Board)
	if len(outline) < 3 {
		return nil, nil, nil, errors.New("board has no outline to panelize")
	}
	minX, minY, maxX, maxY := outline[0].XNM, outline[0].YNM, outline[0].XNM, outline[0].YNM
	for _, p := range outline {
		minX, minY, maxX, maxY = min64(minX, p.XNM), min64(minY, p.YNM), max64(maxX, p.XNM), max64(maxY, p.YNM)
	}
	w, h := maxX-minX, maxY-minY
	if w <= 0 || h <= 0 {
		return nil, nil, nil, errors.New("board outline is degenerate")
	}
	tabs := o.Separation == "tabs"
	topBottom := o.RailSides == "top_bottom" || o.RailSides == "all"
	leftRight := o.RailSides == "left_right" || o.RailSides == "all"
	gap := int64(0)
	if tabs {
		gap = o.SpacingNM
	}
	x0, y0 := int64(0), int64(0)
	if leftRight {
		x0 = o.RailWidthNM + gap
	}
	if topBottom {
		y0 = o.RailWidthNM + gap
	}
	panelW := 2*x0 + int64(o.Columns)*w + int64(o.Columns-1)*o.SpacingNM
	panelH := 2*y0 + int64(o.Rows)*h + int64(o.Rows-1)*o.SpacingNM
	if topBottom && panelW < panelMinimumRailLengthNM || leftRight && !topBottom && panelH < panelMinimumRailLengthNM {
		return nil, nil, nil, fmt.Errorf("panel rails are shorter than %.0f mm, too short for fiducials and tooling holes; add columns/rows or change rail_sides", float64(panelMinimumRailLengthNM)/1e6)
	}
	report := &PanelReport{Schema: panelSchema, Engine: engineVersion, Options: o, WidthNM: panelW, HeightNM: panelH, BoardWidthNM: w, BoardHeightNM: h, Warnings: []string{}}

	rails := []panelRect{}
	if topBottom {
		rails = append(rails, panelRect{0, 0, panelW, o.RailWidthNM}, panelRect{0, panelH - o.RailWidthNM, panelW, panelH})
	}
	if leftRight {
		rails = append(rails, panelRect{0, 0, o.RailWidthNM, panelH}, panelRect{panelW - o.RailWidthNM, 0, panelW, panelH})
	}
	for _, rail := range rails {
		report.Rails = append(report.Rails, rail.report())
	}
	cells := make([]panelRect, 0, o.Rows*o.Columns)
	panel := &Definition{Schema: def.Schema, Name: def.Name + " panel", Rules: def.Rules, Board: Board{WidthNM: panelW, HeightNM: panelH, ThicknessNM: def.Board.ThicknessNM, Layers: append([]Layer(nil), def.Board.Layers...)}, Mechanical: &MechanicalSpec{Schema: mechanicalSchema, Datum: defaultMechanicalDatum()}}
	hasBack := false
	for row := 0; row < o.Rows; row++ {
		for column := 0; column < o.Columns; column++ {
			cx := x0 + int64(column)*(w+o.SpacingNM)
			cy := y0 + int64(row)*(h+o.SpacingNM)
			cells = append(cells, panelRect{cx, cy, cx + w, cy + h})
			dx, dy := cx-minX, cy-minY
			report.Boards = append(report.Boards, PanelBoard{Row: row + 1, Column: column + 1, OffsetX: dx, OffsetY: dy, OriginX: cx, OriginY: cy, WidthNM: w, HeightNM: h})
			if copyBoardIntoPanel(panel, def, fmt.Sprintf("-r%dc%d", row+1, column+1), dx, dy) {
				hasBack = true
			}
		}
	}

	// Tooling holes and fiducials sit on the primary rails (top/bottom unless
	// only left/right rails exist). Fiducials are asymmetric so the machine
	// can detect a rotated panel.
	var tooling, fiducials []Point
	mid := o.RailWidthNM / 2
	if topBottom {
		tooling = []Point{{XNM: panelToolingHoleInset, YNM: mid}, {XNM: panelW - panelToolingHoleInset, YNM: mid}, {XNM: panelToolingHoleInset, YNM: panelH - mid}, {XNM: panelW - panelToolingHoleInset, YNM: panelH - mid}}
		fiducials = []Point{{XNM: panelFiducialInset, YNM: mid}, {XNM: panelW - panelFiducialInset, YNM: mid}, {XNM: panelFiducialInset, YNM: panelH - mid}}
	} else {
		tooling = []Point{{XNM: mid, YNM: panelToolingHoleInset}, {XNM: mid, YNM: panelH - panelToolingHoleInset}, {XNM: panelW - mid, YNM: panelToolingHoleInset}, {XNM: panelW - mid, YNM: panelH - panelToolingHoleInset}}
		fiducials = []Point{{XNM: mid, YNM: panelFiducialInset}, {XNM: mid, YNM: panelH - panelFiducialInset}, {XNM: panelW - mid, YNM: panelFiducialInset}}
	}
	report.ToolingHoles, report.Fiducials = tooling, fiducials
	for i, p := range tooling {
		panel.Mechanical.MountingHoles = append(panel.Mechanical.MountingHoles, MechanicalHole{ID: fmt.Sprintf("panel-tooling-%d", i+1), XNM: p.XNM, YNM: p.YNM, DiameterNM: panelToolingHoleDiameter})
	}
	sides := outerSides(def)
	for i, p := range fiducials {
		for _, side := range sides {
			if side.Name == "Bot" && !hasBack {
				continue
			}
			position := Position{XNM: p.XNM, YNM: p.YNM, Side: "front"}
			if side.Name == "Bot" {
				position.Side = "back"
			}
			id := fmt.Sprintf("panel-fid-%s%d", side.Prefix, i+1)
			panel.Components = append(panel.Components, Component{ID: id, Designator: fmt.Sprintf("FID%s%d", side.Prefix, i+1), Name: "Panel fiducial", Footprint: "Fiducial_1mm_Mask2mm", Position: position, Pins: []Pin{{ID: "1", Number: "1"}}, Pads: []Pad{{ID: "1", PinID: "1", Shape: "circle", WidthNM: panelFiducialDiameter, HeightNM: panelFiducialDiameter, Layers: []string{side.CopperID}, SolderMaskExpansionNM: (panelFiducialMaskOpening - panelFiducialDiameter) / 2, NoPaste: true}}})
		}
	}

	extras := &fabExtras{}
	if !tabs {
		if len(def.Board.Outline) >= 3 || def.Board.CornerRadiusNM > 0 {
			report.Warnings = append(report.Warnings, "V-score breaks boards along straight lines: rounded or shaped board corners are not reproduced; use separation=tabs to keep the board profile")
		}
		lines := map[PanelLine]bool{}
		for _, cell := range cells {
			for _, x := range []int64{cell.x0, cell.x1} {
				if x > 0 && x < panelW {
					lines[PanelLine{Axis: "x", PositionNM: x}] = true
				}
			}
			for _, y := range []int64{cell.y0, cell.y1} {
				if y > 0 && y < panelH {
					lines[PanelLine{Axis: "y", PositionNM: y}] = true
				}
			}
		}
		for line := range lines {
			report.VScores = append(report.VScores, line)
		}
		sort.Slice(report.VScores, func(i, j int) bool {
			if report.VScores[i].Axis != report.VScores[j].Axis {
				return report.VScores[i].Axis < report.VScores[j].Axis
			}
			return report.VScores[i].PositionNM < report.VScores[j].PositionNM
		})
		g := newGerberWriter()
		for _, line := range report.VScores {
			if line.Axis == "x" {
				g.polyline(100_000, []Point{{XNM: line.PositionNM, YNM: 0}, {XNM: line.PositionNM, YNM: panelH}})
			} else {
				g.polyline(100_000, []Point{{XNM: 0, YNM: line.PositionNM}, {XNM: panelW, YNM: line.PositionNM}})
			}
		}
		extras.Layers = append(extras.Layers, fabExtraLayer{Path: panelVScoreLinePath, Function: panelVScoreFileFunction, Polarity: "Positive", Body: g.render("V-score lines (full-length, 1/3 depth each side)", panelVScoreFileFunction, "Positive")})
		return panel, extras, report, nil
	}

	// Tab-routed panel: everything that is neither rail, board nor tab is
	// milled away. Tabs bridge every board edge that faces another board or a
	// rail, with a mouse-bite NPTH perforation along each board edge.
	tabRects := []panelRect{}
	bites := []Point{}
	perforate := func(horizontalEdge bool, edge, from, to int64) {
		span := to - from
		count := int((span-panelMouseBiteDiameter)/panelMouseBitePitch) + 1
		if count < 1 {
			return
		}
		start := from + (span-int64(count-1)*panelMouseBitePitch)/2
		for i := 0; i < count; i++ {
			along := start + int64(i)*panelMouseBitePitch
			if horizontalEdge {
				bites = append(bites, Point{XNM: along, YNM: edge})
			} else {
				bites = append(bites, Point{XNM: edge, YNM: along})
			}
		}
	}
	tabSpans := func(from, to int64) [][2]int64 {
		length := to - from
		n := int64(1)
		if length >= 80_000_000 {
			n = 3
		} else if length >= 30_000_000 {
			n = 2
		}
		width := min64(o.TabWidthNM, length/(2*n))
		out := [][2]int64{}
		for k := int64(0); k < n; k++ {
			center := from + (2*k+1)*length/(2*n)
			out = append(out, [2]int64{center - width/2, center + width/2})
		}
		return out
	}
	for index, cell := range cells {
		row, column := index/o.Columns, index%o.Columns
		// Vertical channels: right neighbour, or left/right rail.
		type channel struct {
			a, b             int64 // channel extent across (x for vertical channels)
			perforateA, perB bool
		}
		vertical := []channel{}
		if column < o.Columns-1 {
			vertical = append(vertical, channel{cell.x1, cell.x1 + o.SpacingNM, true, true})
		}
		if leftRight && column == 0 {
			vertical = append(vertical, channel{cell.x0 - gap, cell.x0, false, true})
		}
		if leftRight && column == o.Columns-1 {
			vertical = append(vertical, channel{cell.x1, cell.x1 + gap, true, false})
		}
		for _, ch := range vertical {
			for _, span := range tabSpans(cell.y0, cell.y1) {
				tabRects = append(tabRects, panelRect{ch.a, span[0], ch.b, span[1]})
				if ch.perforateA {
					perforate(false, ch.a, span[0], span[1])
				}
				if ch.perB {
					perforate(false, ch.b, span[0], span[1])
				}
			}
		}
		horizontal := []channel{}
		if row < o.Rows-1 {
			horizontal = append(horizontal, channel{cell.y1, cell.y1 + o.SpacingNM, true, true})
		}
		if topBottom && row == 0 {
			horizontal = append(horizontal, channel{cell.y0 - gap, cell.y0, false, true})
		}
		if topBottom && row == o.Rows-1 {
			horizontal = append(horizontal, channel{cell.y1, cell.y1 + gap, true, false})
		}
		for _, ch := range horizontal {
			for _, span := range tabSpans(cell.x0, cell.x1) {
				tabRects = append(tabRects, panelRect{span[0], ch.a, span[1], ch.b})
				if ch.perforateA {
					perforate(true, ch.a, span[0], span[1])
				}
				if ch.perB {
					perforate(true, ch.b, span[0], span[1])
				}
			}
		}
	}
	for _, tab := range tabRects {
		report.Tabs = append(report.Tabs, tab.report())
	}
	for i, p := range bites {
		panel.Mechanical.MountingHoles = append(panel.Mechanical.MountingHoles, MechanicalHole{ID: fmt.Sprintf("panel-bite-%d", i+1), XNM: p.XNM, YNM: p.YNM, DiameterNM: panelMouseBiteDiameter})
	}
	report.MouseBites = len(bites)

	material := append(append(append([]panelRect(nil), rails...), cells...), tabRects...)
	loops := traceMaterialLoops(panelW, panelH, material)
	corner := newPanelCornerProfile(def.Board, minX, minY)
	edges := []contour{}
	squared := false
	for _, loop := range loops {
		c, ok := carveBoardCorners(loop, cells, corner)
		if !ok {
			squared = true
		}
		edges = append(edges, c)
	}
	if squared {
		report.Warnings = append(report.Warnings, "Board corner profile could not be matched to its bounding box; some corners are routed square")
	}
	for _, cutout := range panel.Board.Cutouts {
		if cutout.Plated && (cutout.Kind == "slot" || cutout.Kind == "circle") {
			continue
		}
		edges = append(edges, cutoutEdgeContour(cutout))
	}
	extras.EdgeContours = edges
	return panel, extras, report, nil
}

// copyBoardIntoPanel appends one translated copy of the board's copper,
// components, holes and cutouts. It reports whether any part sits on the back.
func copyBoardIntoPanel(panel, def *Definition, suffix string, dx, dy int64) bool {
	move := func(p Point) Point { return Point{XNM: p.XNM + dx, YNM: p.YNM + dy} }
	movePoints := func(points []Point) []Point {
		out := make([]Point, len(points))
		for i, p := range points {
			out[i] = move(p)
		}
		return out
	}
	back := false
	for _, component := range def.Components {
		copy := component
		copy.ID += suffix
		copy.Position.XNM += dx
		copy.Position.YNM += dy
		if copy.Position.Side == "back" {
			back = true
		}
		panel.Components = append(panel.Components, copy)
	}
	for _, trace := range def.Traces {
		copy := trace
		copy.ID += suffix
		copy.Points = movePoints(trace.Points)
		panel.Traces = append(panel.Traces, copy)
	}
	for _, via := range def.Vias {
		copy := via
		copy.ID += suffix
		copy.XNM += dx
		copy.YNM += dy
		panel.Vias = append(panel.Vias, copy)
	}
	for _, zone := range def.Zones {
		copy := zone
		copy.ID += suffix
		copy.Polygon = movePoints(zone.Polygon)
		panel.Zones = append(panel.Zones, copy)
	}
	for _, cutout := range def.Board.Cutouts {
		copy := cutout
		copy.ID += suffix
		copy.XNM += dx
		copy.YNM += dy
		copy.Polygon = movePoints(cutout.Polygon)
		panel.Board.Cutouts = append(panel.Board.Cutouts, copy)
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			copy := hole
			copy.ID += suffix
			copy.XNM += dx
			copy.YNM += dy
			panel.Mechanical.MountingHoles = append(panel.Mechanical.MountingHoles, copy)
		}
	}
	return back
}

// traceMaterialLoops rasterises the panel on the grid of all rectangle edges
// and returns the closed boundary loops of the material, each traversed with
// material on the left (outer boundary CCW, milled windows CW).
func traceMaterialLoops(width, height int64, material []panelRect) [][]Point {
	xsSet := map[int64]bool{0: true, width: true}
	ysSet := map[int64]bool{0: true, height: true}
	for _, r := range material {
		xsSet[r.x0], xsSet[r.x1], ysSet[r.y0], ysSet[r.y1] = true, true, true, true
	}
	sortedKeys := func(set map[int64]bool) []int64 {
		out := make([]int64, 0, len(set))
		for v := range set {
			out = append(out, v)
		}
		sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
		return out
	}
	xs, ys := sortedKeys(xsSet), sortedKeys(ysSet)
	nx, ny := len(xs)-1, len(ys)-1
	filled := make([]bool, nx*ny)
	for i := 0; i < nx; i++ {
		for j := 0; j < ny; j++ {
			cx, cy := (float64(xs[i])+float64(xs[i+1]))/2, (float64(ys[j])+float64(ys[j+1]))/2
			for _, r := range material {
				if r.contains(cx, cy) {
					filled[i*ny+j] = true
					break
				}
			}
		}
	}
	at := func(i, j int) bool { return i >= 0 && j >= 0 && i < nx && j < ny && filled[i*ny+j] }
	type edge struct{ a, b Point }
	outgoing := map[Point][]edge{}
	edges := []edge{}
	push := func(a, b Point) {
		e := edge{a, b}
		outgoing[a] = append(outgoing[a], e)
		edges = append(edges, e)
	}
	for i := 0; i < nx; i++ {
		for j := 0; j < ny; j++ {
			if !at(i, j) {
				continue
			}
			p00, p10, p11, p01 := Point{XNM: xs[i], YNM: ys[j]}, Point{XNM: xs[i+1], YNM: ys[j]}, Point{XNM: xs[i+1], YNM: ys[j+1]}, Point{XNM: xs[i], YNM: ys[j+1]}
			if !at(i, j-1) {
				push(p00, p10)
			}
			if !at(i+1, j) {
				push(p10, p11)
			}
			if !at(i, j+1) {
				push(p11, p01)
			}
			if !at(i-1, j) {
				push(p01, p00)
			}
		}
	}
	used := map[edge]bool{}
	loops := [][]Point{}
	for _, first := range edges {
		if used[first] {
			continue
		}
		loop := []Point{first.a}
		current := first
		used[current] = true
		for current.b != first.a {
			dx, dy := current.b.XNM-current.a.XNM, current.b.YNM-current.a.YNM
			var next edge
			best := int64(-2)
			for _, candidate := range outgoing[current.b] {
				if used[candidate] {
					continue
				}
				cx, cy := candidate.b.XNM-candidate.a.XNM, candidate.b.YNM-candidate.a.YNM
				cross := sign64(dx*cy - dy*cx)
				if dx*cx+dy*cy < 0 && cross == 0 {
					cross = -2 // reversal, never preferred
				}
				if cross > best {
					best, next = cross, candidate
				}
			}
			if best == -2 {
				break
			}
			loop = append(loop, current.b)
			used[next] = true
			current = next
		}
		loops = append(loops, simplifyLoop(loop))
	}
	return loops
}

// simplifyLoop removes collinear intermediate vertices.
func simplifyLoop(points []Point) []Point {
	out := []Point{}
	n := len(points)
	for i := 0; i < n; i++ {
		prev, cur, next := points[(i+n-1)%n], points[i], points[(i+1)%n]
		if (cur.XNM-prev.XNM)*(next.YNM-cur.YNM)-(cur.YNM-prev.YNM)*(next.XNM-cur.XNM) != 0 {
			out = append(out, cur)
		}
	}
	return out
}

// panelCornerProfile describes how a board's bounding-box corners are cut:
// a true radius (implicit rounded boards) or the outline's corner chains.
type panelCornerProfile struct {
	radius  int64
	outline []Point // local CCW outline relative to the bounding-box origin
}

func newPanelCornerProfile(board Board, minX, minY int64) panelCornerProfile {
	if len(board.Outline) < 3 {
		return panelCornerProfile{radius: board.CornerRadiusNM}
	}
	local := make([]Point, len(board.Outline))
	for i, p := range board.Outline {
		local[i] = Point{XNM: p.XNM - minX, YNM: p.YNM - minY}
	}
	if polygonArea(local) < 0 {
		for i, j := 0, len(local)-1; i < j; i, j = i+1, j-1 {
			local[i], local[j] = local[j], local[i]
		}
	}
	return panelCornerProfile{outline: local}
}

// carveBoardCorners turns a material loop into a contour, replacing each
// convex board bounding-box corner with the board's real corner profile.
// It returns false when a corner had to stay square.
func carveBoardCorners(loop []Point, cells []panelRect, profile panelCornerProfile) (contour, bool) {
	corners := map[Point]panelRect{}
	if profile.radius > 0 || len(profile.outline) > 0 {
		for _, cell := range cells {
			for _, p := range []Point{{XNM: cell.x0, YNM: cell.y0}, {XNM: cell.x1, YNM: cell.y0}, {XNM: cell.x1, YNM: cell.y1}, {XNM: cell.x0, YNM: cell.y1}} {
				corners[p] = cell
			}
		}
	}
	ok := true
	n := len(loop)
	type piece struct {
		point Point
		arc   bool
		cntr  Point
	}
	pieces := []piece{}
	for i := 0; i < n; i++ {
		prev, v, next := loop[(i+n-1)%n], loop[i], loop[(i+1)%n]
		cell, isCorner := corners[v]
		d1x, d1y := sign64(v.XNM-prev.XNM), sign64(v.YNM-prev.YNM)
		d2x, d2y := sign64(next.XNM-v.XNM), sign64(next.YNM-v.YNM)
		left := d1x*d2y-d1y*d2x > 0
		if !isCorner || !left {
			pieces = append(pieces, piece{point: v})
			continue
		}
		inLen := abs64(v.XNM-prev.XNM) + abs64(v.YNM-prev.YNM)
		outLen := abs64(next.XNM-v.XNM) + abs64(next.YNM-v.YNM)
		if profile.radius > 0 {
			r := profile.radius
			if r > inLen || r > outLen {
				ok = false
				pieces = append(pieces, piece{point: v})
				continue
			}
			p1 := Point{XNM: v.XNM - int64(d1x)*r, YNM: v.YNM - int64(d1y)*r}
			p2 := Point{XNM: v.XNM + int64(d2x)*r, YNM: v.YNM + int64(d2y)*r}
			center := Point{XNM: p1.XNM + int64(d2x)*r, YNM: p1.YNM + int64(d2y)*r}
			pieces = append(pieces, piece{point: p1}, piece{point: p2, arc: true, cntr: center})
			continue
		}
		chain := outlineCornerChain(profile.outline, Point{XNM: v.XNM - cell.x0, YNM: v.YNM - cell.y0}, d1x, d1y, d2x, d2y)
		if len(chain) == 0 {
			ok = false
			pieces = append(pieces, piece{point: v})
			continue
		}
		first, last := chain[0], chain[len(chain)-1]
		local := Point{XNM: v.XNM - cell.x0, YNM: v.YNM - cell.y0}
		if abs64(local.XNM-first.XNM)+abs64(local.YNM-first.YNM) > inLen || abs64(local.XNM-last.XNM)+abs64(local.YNM-last.YNM) > outLen {
			ok = false
			pieces = append(pieces, piece{point: v})
			continue
		}
		for _, p := range chain {
			pieces = append(pieces, piece{point: Point{XNM: p.XNM + cell.x0, YNM: p.YNM + cell.y0}})
		}
	}
	if len(pieces) == 0 {
		return contour{}, ok
	}
	// Rotate so the contour starts on a plain vertex (never mid-arc).
	start := 0
	for start < len(pieces) && pieces[start].arc {
		start++
	}
	out := contour{Start: pieces[start].point}
	for k := 1; k <= len(pieces); k++ {
		p := pieces[(start+k)%len(pieces)]
		last := out.Start
		if len(out.Segs) > 0 {
			last = out.Segs[len(out.Segs)-1].End
		}
		if !p.arc && p.point == last {
			continue
		}
		out.Segs = append(out.Segs, contourSeg{End: p.point, Arc: p.arc, Center: p.cntr})
	}
	return out, ok
}

// outlineCornerChain finds the outline vertices that replace bounding-box
// corner v: from the last vertex on the incoming edge line to the first on
// the outgoing edge line, walking the CCW outline.
func outlineCornerChain(outline []Point, v Point, d1x, d1y, d2x, d2y int64) []Point {
	n := len(outline)
	onLine := func(p Point, dx, dy int64) bool {
		return (p.XNM-v.XNM)*dy-(p.YNM-v.YNM)*dx == 0
	}
	for i := 0; i < n; i++ {
		if !onLine(outline[i], d1x, d1y) || onLine(outline[(i+1)%n], d1x, d1y) {
			continue
		}
		if outline[i] == v {
			return []Point{v}
		}
		chain := []Point{outline[i]}
		for step := 1; step < n/2+1; step++ {
			p := outline[(i+step)%n]
			chain = append(chain, p)
			if onLine(p, d2x, d2y) {
				// The chain must stay inside the corner's quadrant.
				for _, q := range chain {
					along1 := (q.XNM-v.XNM)*int64(d1x) + (q.YNM-v.YNM)*int64(d1y)
					along2 := (q.XNM-v.XNM)*int64(d2x) + (q.YNM-v.YNM)*int64(d2y)
					if along1 > 0 || along2 < 0 {
						return nil
					}
				}
				return chain
			}
		}
		return nil
	}
	return nil
}

// Panelize produces a verified panel manufacturing ZIP for one revision as an
// artifact. The design and its revisions are not modified.
func (s *Service) Panelize(designID, revisionID int64, options PanelOptions) (*Artifact, *PanelReport, error) {
	design, revision, def, err := s.revision(designID, revisionID)
	if err != nil {
		return nil, nil, err
	}
	if validation := validateDefinition(def); validation.Status == "failed" {
		return nil, nil, fmt.Errorf("revision failed validation with %d errors; panelization refused", validation.Errors)
	}
	files, report, err := panelizeFiles(def, options)
	if err != nil {
		return nil, nil, err
	}
	if report.Verification.Status == "failed" {
		return nil, report, fmt.Errorf("independent fabrication verification of the panel failed with %d errors", report.Verification.Errors)
	}
	body, err := deterministicZip(files)
	if err != nil {
		return nil, report, err
	}
	artifact, err := s.persistArtifact(design, revision, "panel", "zip", "application/zip", body, map[string]any{
		"engine": engineVersion, "schema": panelSchema, "rows": report.Options.Rows, "columns": report.Options.Columns,
		"separation": report.Options.Separation, "width_mm": math.Round(float64(report.WidthNM)/1e3) / 1e3, "height_mm": math.Round(float64(report.HeightNM)/1e3) / 1e3,
		"files": report.Files, "fabrication_verification_status": report.Verification.Status,
	})
	return artifact, report, err
}

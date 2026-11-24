package main

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"sort"
	"strings"
)

type MechanicalValidation struct {
	Schema   string         `json:"schema"`
	Engine   string         `json:"engine"`
	Status   string         `json:"status"`
	Errors   int            `json:"errors"`
	Warnings int            `json:"warnings"`
	Checks   []Check        `json:"checks"`
	Metrics  map[string]int `json:"metrics"`
}

type MechanicalEnvelopeArtifact struct {
	Schema               string                `json:"schema"`
	SourceApp            string                `json:"source_app"`
	SourceDesignID       int64                 `json:"source_design_id"`
	SourceRevisionID     int64                 `json:"source_revision_id"`
	SourceRevisionNumber int                   `json:"source_revision_number"`
	SourceSHA256         string                `json:"source_sha256"`
	Datum                MechanicalDatum       `json:"datum"`
	Tolerances           MechanicalTolerances  `json:"tolerances"`
	Board                MechanicalBoard       `json:"board"`
	MountingHoles        []MechanicalHole      `json:"mounting_holes"`
	Components           []ComponentEnvelope   `json:"component_envelopes"`
	PanelOpenings        []PanelOpening        `json:"panel_openings"`
	ClearanceZones       []MechanicalClearance `json:"clearance_zones"`
}

type MechanicalBoard struct {
	WidthNM        int64         `json:"width_nm"`
	HeightNM       int64         `json:"height_nm"`
	ThicknessNM    int64         `json:"thickness_nm"`
	CornerRadiusNM int64         `json:"corner_radius_nm,omitempty"`
	Outline        []Point       `json:"outline"`
	Cutouts        []BoardCutout `json:"cutouts"`
}

func defaultMechanicalDatum() MechanicalDatum {
	return MechanicalDatum{Origin: "board-outline-lower-left-bottom-face", Handedness: "right", XAxis: "board-right", YAxis: "board-up-in-top-view", ZAxis: "front-side"}
}

func boardOutline(board Board) []Point {
	if len(board.Outline) >= 3 {
		return append([]Point(nil), board.Outline...)
	}
	// Rounded corners are flattened at 11.25 degree steps for DRC, rendering
	// and solids; Edge_Cuts emits the same corners as true G02/G03 arcs.
	return boardEdgeContour(board).polygon()
}

func validateMechanical(def *Definition) MechanicalValidation {
	out := MechanicalValidation{Schema: "apteva-pcb-mechanical-validation/v1", Engine: engineVersion, Status: "passed", Checks: []Check{}, Metrics: map[string]int{}}
	add := func(code, severity, message string, ids ...string) {
		out.Checks = append(out.Checks, Check{Code: code, Severity: severity, Message: message, ObjectIDs: ids})
		if severity == "error" {
			out.Errors++
		}
		if severity == "warning" {
			out.Warnings++
		}
	}
	if def.Mechanical == nil {
		add("MECHANICAL_MISSING", "error", "Revision has no mechanical envelope")
		out.Status = "failed"
		return out
	}
	m := def.Mechanical
	if m.Schema != mechanicalSchema {
		add("MECHANICAL_SCHEMA_UNSUPPORTED", "error", "Unsupported mechanical schema")
	}
	if def.Board.ThicknessNM <= 0 {
		add("BOARD_THICKNESS_INVALID", "error", "Board thickness must be positive")
	}
	if def.Board.CornerRadiusNM < 0 || def.Board.CornerRadiusNM*2 > mechanicalMin64(def.Board.WidthNM, def.Board.HeightNM) {
		add("BOARD_CORNER_RADIUS_INVALID", "error", "Board corner radius must fit inside the board extents")
	}
	outline := boardOutline(def.Board)
	if len(outline) < 3 || math.Abs(polygonArea(outline)) < 1 {
		add("BOARD_OUTLINE_INVALID", "error", "Board outline must be a non-degenerate closed polygon")
	}
	ids := map[string]bool{}
	components := map[string]bool{}
	for _, component := range def.Components {
		components[component.ID] = true
	}
	claim := func(id, kind string) bool {
		if !idValid(id) {
			add("MECHANICAL_ID_INVALID", "error", kind+" has an invalid stable ID", id)
			return false
		}
		if ids[id] {
			add("MECHANICAL_ID_DUPLICATE", "error", "Mechanical object ID is duplicated", id)
			return false
		}
		ids[id] = true
		return true
	}
	for _, cutout := range def.Board.Cutouts {
		claim(cutout.ID, "Board cutout")
		switch cutout.Kind {
		case "circle":
			if cutout.DiameterNM <= 0 {
				add("BOARD_CUTOUT_SIZE_INVALID", "error", "Circular cutout must have a positive diameter", cutout.ID)
			}
		case "slot":
			if cutout.WidthNM <= 0 || cutout.HeightNM <= 0 {
				add("BOARD_CUTOUT_SIZE_INVALID", "error", "Slot cutout must have positive width and height", cutout.ID)
			}
		case "polygon":
			if len(cutout.Polygon) < 3 || math.Abs(polygonArea(cutout.Polygon)) < 1 {
				add("BOARD_CUTOUT_POLYGON_INVALID", "error", "Polygon cutout must be non-degenerate", cutout.ID)
			}
		default:
			add("BOARD_CUTOUT_KIND_INVALID", "error", "Board cutout kind must be circle, slot, or polygon", cutout.ID)
		}
		if cutout.Kind != "polygon" && !mechanicalPointInPolygon(Point{XNM: cutout.XNM, YNM: cutout.YNM}, outline) {
			add("BOARD_CUTOUT_OUTSIDE", "error", "Board cutout centre lies outside the board outline", cutout.ID)
		}
	}
	if m.Tolerances.XYNM < 0 || m.Tolerances.ZNM < 0 || m.Tolerances.PanelOpeningFitNM < 0 || m.Tolerances.FastenerFitNM < 0 {
		add("MECHANICAL_TOLERANCE_INVALID", "error", "Mechanical tolerances cannot be negative")
	}
	for _, hole := range m.MountingHoles {
		claim(hole.ID, "Mounting hole")
		if hole.DiameterNM <= 0 {
			add("MOUNTING_HOLE_DIAMETER_INVALID", "error", "Mounting-hole diameter must be positive", hole.ID)
		}
		if hole.PositionTolNM < 0 || hole.DiameterTolNM < 0 {
			add("MOUNTING_HOLE_TOLERANCE_INVALID", "error", "Mounting-hole tolerances cannot be negative", hole.ID)
		}
		if !mechanicalPointInPolygon(Point{XNM: hole.XNM, YNM: hole.YNM}, outline) {
			add("MOUNTING_HOLE_OUTSIDE_BOARD", "error", "Mounting-hole centre lies outside the board outline", hole.ID)
		}
	}
	for _, envelope := range m.ComponentEnvelopes {
		claim(envelope.ID, "Component envelope")
		if envelope.WidthNM <= 0 || envelope.DepthNM <= 0 || envelope.ZMaxNM <= envelope.ZMinNM {
			add("COMPONENT_ENVELOPE_INVALID", "error", "Component envelope must have positive XYZ dimensions", envelope.ID)
		}
		if envelope.ComponentID != "" && !components[envelope.ComponentID] {
			add("COMPONENT_ENVELOPE_ORPHAN", "error", "Component envelope references a missing component", envelope.ID, envelope.ComponentID)
		}
		if envelope.ToleranceNM < 0 {
			add("COMPONENT_TOLERANCE_INVALID", "error", "Component-envelope tolerance cannot be negative", envelope.ID)
		}
		if envelope.Shape != "" && envelope.Shape != "box" && envelope.Shape != "cylinder" && envelope.Shape != "header" && envelope.Shape != "connector" {
			add("COMPONENT_SHAPE_INVALID", "error", "Component shape is not supported by the native solid model", envelope.ID)
		}
		if envelope.ModelRef != "" && !strings.HasPrefix(envelope.ModelRef, "apteva://mechanical/") {
			add("COMPONENT_MODEL_REF_INVALID", "error", "Native model_ref must use the apteva://mechanical/ namespace", envelope.ID)
		}
	}
	for i := 0; i < len(m.ComponentEnvelopes); i++ {
		for j := i + 1; j < len(m.ComponentEnvelopes); j++ {
			a, b := m.ComponentEnvelopes[i], m.ComponentEnvelopes[j]
			if a.ZMinNM < b.ZMaxNM && b.ZMinNM < a.ZMaxNM && orientedRectanglesOverlap(a, b) {
				add("MECHANICAL_SOLID_COLLISION", "error", "Rotation-aware component solids overlap", a.ID, b.ID)
			}
		}
	}
	validFaces := map[string]bool{"north": true, "south": true, "east": true, "west": true, "top": true, "bottom": true}
	for _, opening := range m.PanelOpenings {
		claim(opening.ID, "Panel opening")
		if !validFaces[opening.Face] {
			add("PANEL_OPENING_FACE_INVALID", "error", "Panel opening needs a supported interface face", opening.ID)
		}
		if opening.WidthNM <= 0 || opening.HeightNM <= 0 {
			add("PANEL_OPENING_SIZE_INVALID", "error", "Panel opening must have positive width and height", opening.ID)
		}
		if opening.ToleranceNM < 0 {
			add("PANEL_OPENING_TOLERANCE_INVALID", "error", "Panel-opening tolerance cannot be negative", opening.ID)
		}
		if opening.ComponentID != "" && !components[opening.ComponentID] {
			add("PANEL_OPENING_ORPHAN", "error", "Panel opening references a missing component", opening.ID, opening.ComponentID)
		}
	}
	validKinds := map[string]bool{"cable_insertion": true, "service": true, "airflow": true, "thermal": true, "antenna": true}
	for _, zone := range m.ClearanceZones {
		claim(zone.ID, "Clearance zone")
		if !validKinds[zone.Kind] {
			add("CLEARANCE_KIND_INVALID", "error", "Clearance zone has an unsupported kind", zone.ID)
		}
		if zone.WidthNM <= 0 || zone.DepthNM <= 0 || zone.ZMaxNM <= zone.ZMinNM {
			add("CLEARANCE_VOLUME_INVALID", "error", "Clearance zone must have positive XYZ dimensions", zone.ID)
		}
	}
	if len(m.MountingHoles) == 0 {
		add("MOUNTING_HOLES_MISSING", "warning", "No dedicated mounting holes are defined")
	}
	if len(m.ComponentEnvelopes) == 0 {
		add("COMPONENT_ENVELOPES_MISSING", "warning", "No component 3D envelopes are defined")
	}
	if len(m.PanelOpenings) == 0 {
		add("PANEL_OPENINGS_MISSING", "warning", "No connector panel openings are defined")
	}
	if out.Errors > 0 {
		out.Status = "failed"
	} else if out.Warnings > 0 {
		out.Status = "warning"
	}
	if len(out.Checks) == 0 {
		add("MECHANICAL_READY", "info", "Mechanical envelope is complete and handoff-ready")
	}
	modeled := 0
	for _, envelope := range m.ComponentEnvelopes {
		if envelope.ModelRef != "" {
			modeled++
		}
	}
	out.Metrics = map[string]int{"outline_points": len(outline), "board_cutouts": len(def.Board.Cutouts), "mounting_holes": len(m.MountingHoles), "component_envelopes": len(m.ComponentEnvelopes), "modeled_components": modeled, "panel_openings": len(m.PanelOpenings), "clearance_zones": len(m.ClearanceZones)}
	return out
}

type mechanicalVec2 struct{ x, y float64 }

func orientedRectangleCorners(e ComponentEnvelope) [4]mechanicalVec2 {
	hx, hy := float64(e.WidthNM)/2, float64(e.DepthNM)/2
	angle := float64(e.RotationUdeg) / 1e6 * math.Pi / 180
	c, s := math.Cos(angle), math.Sin(angle)
	local := [4]mechanicalVec2{{-hx, -hy}, {hx, -hy}, {hx, hy}, {-hx, hy}}
	var out [4]mechanicalVec2
	for i, p := range local {
		out[i] = mechanicalVec2{float64(e.XNM) + p.x*c - p.y*s, float64(e.YNM) + p.x*s + p.y*c}
	}
	return out
}

func orientedRectanglesOverlap(a, b ComponentEnvelope) bool {
	ac, bc := orientedRectangleCorners(a), orientedRectangleCorners(b)
	axes := [4]mechanicalVec2{}
	for i, pair := range [][2]mechanicalVec2{{ac[0], ac[1]}, {ac[1], ac[2]}, {bc[0], bc[1]}, {bc[1], bc[2]}} {
		edge := mechanicalVec2{pair[1].x - pair[0].x, pair[1].y - pair[0].y}
		axes[i] = mechanicalVec2{-edge.y, edge.x}
	}
	project := func(points [4]mechanicalVec2, axis mechanicalVec2) (float64, float64) {
		lo, hi := points[0].x*axis.x+points[0].y*axis.y, points[0].x*axis.x+points[0].y*axis.y
		for _, p := range points[1:] {
			value := p.x*axis.x + p.y*axis.y
			lo, hi = math.Min(lo, value), math.Max(hi, value)
		}
		return lo, hi
	}
	for _, axis := range axes {
		alo, ahi := project(ac, axis)
		blo, bhi := project(bc, axis)
		if ahi <= blo || bhi <= alo {
			return false
		}
	}
	return true
}

func mechanicalMin64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func polygonArea(points []Point) float64 {
	var area float64
	for i := range points {
		j := (i + 1) % len(points)
		area += float64(points[i].XNM)*float64(points[j].YNM) - float64(points[j].XNM)*float64(points[i].YNM)
	}
	return area / 2
}

func mechanicalPointInPolygon(point Point, polygon []Point) bool {
	inside := false
	for i, j := 0, len(polygon)-1; i < len(polygon); j, i = i, i+1 {
		a, b := polygon[i], polygon[j]
		if (a.YNM > point.YNM) != (b.YNM > point.YNM) && float64(point.XNM) < float64(b.XNM-a.XNM)*float64(point.YNM-a.YNM)/float64(b.YNM-a.YNM)+float64(a.XNM) {
			inside = !inside
		}
	}
	return inside
}

func mechanicalEnvelope(design *Design, revision *Revision, def *Definition) MechanicalEnvelopeArtifact {
	m := def.Mechanical
	return MechanicalEnvelopeArtifact{Schema: mechanicalEnvelopeSchema, SourceApp: "pcb", SourceDesignID: design.ID, SourceRevisionID: revision.ID, SourceRevisionNumber: revision.Number, SourceSHA256: revision.SourceSHA256, Datum: m.Datum, Tolerances: m.Tolerances, Board: MechanicalBoard{WidthNM: def.Board.WidthNM, HeightNM: def.Board.HeightNM, ThicknessNM: def.Board.ThicknessNM, CornerRadiusNM: def.Board.CornerRadiusNM, Outline: boardOutline(def.Board), Cutouts: append([]BoardCutout(nil), def.Board.Cutouts...)}, MountingHoles: append([]MechanicalHole(nil), m.MountingHoles...), Components: append([]ComponentEnvelope(nil), m.ComponentEnvelopes...), PanelOpenings: append([]PanelOpening(nil), m.PanelOpenings...), ClearanceZones: append([]MechanicalClearance(nil), m.ClearanceZones...)}
}

func mechanicalJSON(design *Design, revision *Revision, def *Definition) []byte {
	body, _ := json.MarshalIndent(mechanicalEnvelope(design, revision, def), "", "  ")
	return append(body, '\n')
}

type isoPoint struct{ X, Y float64 }

func isoProject(x, y, z float64) isoPoint {
	yaw, pitch, scale := -34*math.Pi/180, 27*math.Pi/180, 7.2
	rx, ry := x*math.Cos(yaw)-y*math.Sin(yaw), x*math.Sin(yaw)+y*math.Cos(yaw)
	return isoPoint{X: 430 + rx*scale, Y: 405 + (ry*math.Sin(pitch)-z*math.Cos(pitch))*scale}
}
func isoPolygon(points []Point, z float64) string {
	out := make([]string, 0, len(points))
	for _, p := range points {
		q := isoProject(float64(p.XNM)/1e6, float64(p.YNM)/1e6, z)
		out = append(out, fmt.Sprintf("%.1f,%.1f", q.X, q.Y))
	}
	return strings.Join(out, " ")
}

func renderMechanicalSVG(def *Definition) []byte {
	if def.Mechanical == nil {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="1000" height="680"><text x="40" y="60">No mechanical envelope</text></svg>`)
	}
	var b strings.Builder
	b.WriteString(`<svg xmlns="http://www.w3.org/2000/svg" width="1000" height="680" viewBox="0 0 1000 680" role="img" aria-label="PCB mechanical 3D envelope"><defs><linearGradient id="bg" x2="0" y2="1"><stop stop-color="#171512"/><stop offset="1" stop-color="#090807"/></linearGradient><linearGradient id="board3d" x2="0" y2="1"><stop stop-color="#238d73"/><stop offset="1" stop-color="#0b5c4d"/></linearGradient><filter id="mechShadow"><feDropShadow dx="0" dy="14" stdDeviation="12" flood-opacity=".45"/></filter></defs><rect width="1000" height="680" fill="url(#bg)"/><g opacity=".18" stroke="#aaa096" stroke-width="1">`)
	for i := 0; i < 15; i++ {
		fmt.Fprintf(&b, `<path d="M0 %dH1000"/>`, 80+i*40)
	}
	for i := 0; i < 25; i++ {
		fmt.Fprintf(&b, `<path d="M%d 0V680"/>`, i*40)
	}
	b.WriteString(`</g>`)
	b.WriteString(`<text x="42" y="52" fill="#f4efe9" font-family="Inter,Arial" font-size="24" font-weight="700">Mechanical envelope · 3D</text><text x="42" y="78" fill="#9d958c" font-family="Inter,Arial" font-size="11" letter-spacing="2">NATIVE PCB GEOMETRY · NANOMETRE DATUM</text><g filter="url(#mechShadow)">`)
	outline := boardOutline(def.Board)
	t := float64(def.Board.ThicknessNM) / 1e6
	bottom := isoPolygon(outline, 0)
	top := isoPolygon(outline, t)
	fmt.Fprintf(&b, `<polygon points="%s" fill="#073e35" stroke="#5dc0a6" stroke-width="1.4"/><polygon points="%s" fill="url(#board3d)" stroke="#79d7bd" stroke-width="1.8"/>`, bottom, top)
	for _, hole := range def.Mechanical.MountingHoles {
		q := isoProject(float64(hole.XNM)/1e6, float64(hole.YNM)/1e6, t+.02)
		r := math.Max(3, float64(hole.DiameterNM)/1e6*3.2)
		fmt.Fprintf(&b, `<ellipse cx="%.1f" cy="%.1f" rx="%.1f" ry="%.1f" fill="#090807" stroke="#d4c798" stroke-width="2"/>`, q.X, q.Y, r, r*.48)
	}
	envelopes := append([]ComponentEnvelope(nil), def.Mechanical.ComponentEnvelopes...)
	sort.SliceStable(envelopes, func(i, j int) bool { return envelopes[i].YNM < envelopes[j].YNM })
	for _, e := range envelopes {
		drawIsoBox(&b, e.XNM, e.YNM, e.ZMinNM, e.ZMaxNM, e.WidthNM, e.DepthNM, "#2d3135", "#5e6870", e.Label)
	}
	for _, z := range def.Mechanical.ClearanceZones {
		drawIsoBox(&b, z.XNM, z.YNM, z.ZMinNM, z.ZMaxNM, z.WidthNM, z.DepthNM, "#e9652c22", "#e99263", strings.ReplaceAll(z.Kind, "_", " "))
	}
	b.WriteString(`</g><g transform="translate(42 600)"><rect width="916" height="48" rx="12" fill="#12100e" stroke="#39342e"/><circle cx="24" cy="24" r="6" fill="#4ade80"/><text x="42" y="21" fill="#f1ece6" font-family="Inter,Arial" font-size="12" font-weight="700">Validated mechanical producer model</text><text x="42" y="36" fill="#928a81" font-family="Inter,Arial" font-size="9">Board prism · mounting holes · component bodies · connector openings · clearance volumes</text></g></svg>`)
	return []byte(b.String())
}

func drawIsoBox(b *strings.Builder, x, y, z0, z1, w, d int64, fill, stroke, label string) {
	x0, x1 := float64(x-w/2)/1e6, float64(x+w/2)/1e6
	y0, y1 := float64(y-d/2)/1e6, float64(y+d/2)/1e6
	a0 := isoProject(x0, y0, float64(z0)/1e6)
	b0 := isoProject(x1, y0, float64(z0)/1e6)
	c0 := isoProject(x1, y1, float64(z0)/1e6)
	d0 := isoProject(x0, y1, float64(z0)/1e6)
	a := isoProject(x0, y0, float64(z1)/1e6)
	bb := isoProject(x1, y0, float64(z1)/1e6)
	c := isoProject(x1, y1, float64(z1)/1e6)
	d2 := isoProject(x0, y1, float64(z1)/1e6)
	pts := func(p ...isoPoint) string {
		v := []string{}
		for _, q := range p {
			v = append(v, fmt.Sprintf("%.1f,%.1f", q.X, q.Y))
		}
		return strings.Join(v, " ")
	}
	fmt.Fprintf(b, `<g><polygon points="%s" fill="%s" stroke="%s" stroke-width="1"/><polygon points="%s" fill="%s" opacity=".72" stroke="%s"/><polygon points="%s" fill="%s" opacity=".55" stroke="%s"/><polygon points="%s" fill="%s" stroke="%s" stroke-width="1.2"/>`, pts(a0, b0, bb, a), fill, stroke, pts(b0, c0, c, bb), fill, stroke, pts(c0, d0, d2, c), fill, stroke, pts(a, bb, c, d2), fill, stroke)
	if label != "" {
		q := isoProject(float64(x)/1e6, float64(y)/1e6, float64(z1)/1e6+.7)
		fmt.Fprintf(b, `<text x="%.1f" y="%.1f" text-anchor="middle" fill="#fff" font-family="Inter,Arial" font-size="8" paint-order="stroke" stroke="#111" stroke-width="2">%s</text>`, q.X, q.Y, html.EscapeString(label))
	}
	b.WriteString(`</g>`)
}

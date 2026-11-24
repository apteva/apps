package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	librarySchema         = "apteva-pcb-library/v1"
	libraryRevision       = "apteva-2026.11"
	maxKiCadFootprintSize = 2 << 20
)

// LibraryPin describes one electrical pin of a library part. Number matches
// the footprint pad number (KiCad convention); several pads may share it.
// Name is the short net-facing name (IO4, VBUS, GND...) and Functions lists
// alternate functions from the datasheet.
type LibraryPin struct {
	Number         string   `json:"number"`
	Name           string   `json:"name"`
	ElectricalType string   `json:"electrical_type,omitempty"`
	Functions      []string `json:"functions,omitempty"`
}

// LibraryFootprint is a complete native land pattern. Pad coordinates are in
// integer nanometres relative to the footprint origin (KiCad anchor), +Y down.
// Pad.PinID holds the pad number; it is mapped onto component pin IDs when
// the part is instantiated.
type LibraryFootprint struct {
	LibraryID  string  `json:"library_id"`
	Pads       []Pad   `json:"pads"`
	Courtyard  []Point `json:"courtyard"`
	BodyWidth  int64   `json:"body_width_nm"`
	BodyHeight int64   `json:"body_height_nm"`
	HeightNM   int64   `json:"height_nm,omitempty"`
	Model3DRef string  `json:"model_3d_ref,omitempty"`
	Mount      string  `json:"mount,omitempty"` // smd | tht | mixed
}

// LibraryProvenance records where a part's geometry came from.
type LibraryProvenance struct {
	Source       string `json:"source"`              // builtin | kicad_mod
	Reference    string `json:"reference,omitempty"` // upstream footprint / datasheet drawing
	SourceSHA256 string `json:"source_sha256"`       // upstream file hash (imports) or geometry hash (builtin)
	Attribution  string `json:"attribution,omitempty"`
	ImportedAt   string `json:"imported_at,omitempty"`
}

// LibraryPart is one reusable part: footprint, pins, sourcing identity and
// electrical model hints. Built-in parts live in code; imported parts are
// stored per project in pcb_library_parts.
type LibraryPart struct {
	Schema       string            `json:"schema"`
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Category     string            `json:"category"`
	Description  string            `json:"description,omitempty"`
	Keywords     []string          `json:"keywords,omitempty"`
	Value        string            `json:"value,omitempty"`
	Manufacturer string            `json:"manufacturer,omitempty"`
	MPN          string            `json:"mpn,omitempty"`
	AltMPNs      []string          `json:"alt_mpns,omitempty"`
	LCSC         string            `json:"lcsc,omitempty"`
	DatasheetURL string            `json:"datasheet_url,omitempty"`
	Designator   string            `json:"designator_prefix,omitempty"`
	Footprint    LibraryFootprint  `json:"footprint"`
	Pins         []LibraryPin      `json:"pins"`
	Model        *ElectricalModel  `json:"electrical_model,omitempty"`
	Notes        []string          `json:"notes,omitempty"`
	Provenance   LibraryProvenance `json:"provenance"`
}

// LibraryPartSummary is the list-view projection.
type LibraryPartSummary struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	Category     string `json:"category"`
	Description  string `json:"description,omitempty"`
	MPN          string `json:"mpn,omitempty"`
	Manufacturer string `json:"manufacturer,omitempty"`
	LCSC         string `json:"lcsc,omitempty"`
	Footprint    string `json:"footprint"`
	PadCount     int    `json:"pad_count"`
	PinCount     int    `json:"pin_count"`
	Source       string `json:"source"`
	DatasheetURL string `json:"datasheet_url,omitempty"`
}

func (p *LibraryPart) summary() LibraryPartSummary {
	return LibraryPartSummary{ID: p.ID, Name: p.Name, Category: p.Category, Description: p.Description, MPN: p.MPN, Manufacturer: p.Manufacturer, LCSC: p.LCSC, Footprint: p.Footprint.LibraryID, PadCount: len(p.Footprint.Pads), PinCount: len(p.Pins), Source: p.Provenance.Source, DatasheetURL: p.DatasheetURL}
}

// matches reports whether the part matches a free-text query and category.
func (p *LibraryPart) matches(query, category string) bool {
	if category = strings.TrimSpace(strings.ToLower(category)); category != "" && strings.ToLower(p.Category) != category {
		return false
	}
	query = strings.TrimSpace(strings.ToLower(query))
	if query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join(append([]string{p.ID, p.Name, p.Category, p.Description, p.Value, p.Manufacturer, p.MPN, p.LCSC, p.Footprint.LibraryID}, append(p.Keywords, p.AltMPNs...)...), " "))
	for _, term := range strings.Fields(query) {
		if !strings.Contains(haystack, term) {
			return false
		}
	}
	return true
}

// geometryHash is the deterministic identity of a part's land pattern.
func (p *LibraryPart) geometryHash() string {
	body, _ := json.Marshal(map[string]any{"library_id": p.Footprint.LibraryID, "pads": p.Footprint.Pads, "courtyard": p.Footprint.Courtyard})
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// validateLibraryPart checks the invariants every stored or built-in part
// must hold before it can be instantiated.
func validateLibraryPart(p *LibraryPart) error {
	if !idValid(p.ID) {
		return fmt.Errorf("library part id %q is invalid", p.ID)
	}
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("library part name is required")
	}
	if strings.TrimSpace(p.Footprint.LibraryID) == "" {
		return errors.New("library footprint id is required")
	}
	if len(p.Footprint.Pads) == 0 {
		return errors.New("library footprint has no copper pads")
	}
	if len(p.Footprint.Courtyard) < 4 {
		return errors.New("library footprint needs a courtyard")
	}
	pins := map[string]bool{}
	for _, pin := range p.Pins {
		if pins[pin.Number] {
			return fmt.Errorf("duplicate library pin %q", pin.Number)
		}
		pins[pin.Number] = true
	}
	for _, pad := range p.Footprint.Pads {
		if !pins[pad.PinID] {
			return fmt.Errorf("pad %s references undeclared pin %q", pad.ID, pad.PinID)
		}
		if pad.WidthNM <= 0 || pad.HeightNM <= 0 {
			return fmt.Errorf("pad %s has no size", pad.ID)
		}
		if pad.RoundrectRatio < 0 || pad.RoundrectRatio > 0.5 {
			return fmt.Errorf("pad %s roundrect_ratio must be within 0..0.5", pad.ID)
		}
	}
	if p.Model != nil {
		for role, pin := range p.Model.PinRoles {
			if !pins[pin] {
				return fmt.Errorf("electrical model role %s references unknown pin %q", role, pin)
			}
		}
	}
	return nil
}

// ---- .kicad_mod import ----

// KiCadFootprintImport is the result of parsing one KiCad footprint file.
type KiCadFootprintImport struct {
	Part     *LibraryPart `json:"part"`
	Warnings []string     `json:"warnings,omitempty"`
}

// importKiCadFootprint parses a KiCad 6-10 (and legacy `module`) footprint
// into a native library part. Copper pads become native pads; paste-only
// apertures are skipped; np_thru_hole pads keep their hole with plated=false.
// F.CrtYd gives the courtyard and F.Fab the body outline.
func importKiCadFootprint(content []byte) (*KiCadFootprintImport, error) {
	if len(content) == 0 {
		return nil, errors.New("KiCad footprint content is empty")
	}
	if len(content) > maxKiCadFootprintSize {
		return nil, fmt.Errorf("KiCad footprint exceeds %d bytes", maxKiCadFootprintSize)
	}
	root, err := parseSExpressions(string(content))
	if err != nil {
		return nil, err
	}
	if root.atom() != "footprint" && root.atom() != "module" {
		return nil, errors.New("KiCad footprint needs a footprint (or legacy module) root")
	}
	name := strings.TrimSpace(root.value(1))
	if name == "" {
		return nil, errors.New("KiCad footprint has no name")
	}
	warnings := []string{}
	sum := sha256.Sum256(content)
	part := &LibraryPart{Schema: librarySchema, Name: name, Category: "imported", Description: root.child("descr").value(1), Provenance: LibraryProvenance{Source: "kicad_mod", Reference: name, SourceSHA256: hex.EncodeToString(sum[:])}}
	if tags := strings.TrimSpace(root.child("tags").value(1)); tags != "" {
		part.Keywords = strings.Fields(tags)
	}
	for _, property := range root.children("property") {
		switch property.value(1) {
		case "Datasheet":
			if v := strings.TrimSpace(property.value(2)); v != "" && v != "~" {
				part.DatasheetURL = v
			}
		case "Description":
			if part.Description == "" {
				part.Description = property.value(2)
			}
		case "Value":
			part.Value = property.value(2)
		}
	}
	for _, text := range root.children("fp_text") {
		if text.value(1) == "value" && part.Value == "" {
			part.Value = text.value(2)
		}
	}
	if model := root.child("model"); model != nil {
		part.Footprint.Model3DRef = model.value(1)
	}
	pinOrder := []string{}
	pinSeen := map[string]bool{}
	padIDs := map[string]int{}
	hasSMD, hasTHT := false, false
	skippedPaste := 0
	for index, node := range root.children("pad") {
		number, kind, shape := strings.TrimSpace(node.value(1)), node.value(2), node.value(3)
		layers := kicadCopperLayers(node.child("layers"))
		if kind == "smd" || kind == "connect" {
			// SMD pads only keep their own outer copper layer.
			filtered := []string{}
			for _, layer := range layers {
				if layer != "B.Cu" || !containsString(layers, "F.Cu") {
					filtered = append(filtered, layer)
				}
			}
			layers = filtered
		}
		if len(layers) == 0 && kind != "np_thru_hole" {
			skippedPaste++
			continue
		}
		at, size := node.child("at"), node.child("size")
		if at == nil || size == nil {
			return nil, fmt.Errorf("pad %d (%q) is missing at/size", index+1, number)
		}
		width, height := mmNM(size.number(1)), mmNM(size.number(2))
		if height == 0 {
			height = width
		}
		pad := Pad{XNM: mmNM(at.number(1)), YNM: mmNM(at.number(2)), WidthNM: width, HeightNM: height, Layers: layers}
		switch shape {
		case "rect", "circle", "oval":
			pad.Shape = shape
		case "roundrect":
			pad.Shape = "roundrect"
			pad.RoundrectRatio = 0.25 // KiCad default
			if ratio := node.child("roundrect_rratio"); ratio != nil {
				pad.RoundrectRatio = math.Min(0.5, math.Max(0, ratio.number(1)))
			}
		case "trapezoid":
			pad.Shape = "rect"
			warnings = append(warnings, fmt.Sprintf("pad %q: trapezoid approximated by its bounding rectangle", number))
		case "custom":
			pad.Shape = "rect"
			if anchor := node.child("options").child("anchor"); anchor.value(1) == "circle" {
				pad.Shape = "circle"
			}
			if minX, minY, maxX, maxY, ok := kicadPrimitiveBounds(node.child("primitives")); ok {
				minX, minY = math.Min(minX, -size.number(1)/2), math.Min(minY, -size.number(2)/2)
				maxX, maxY = math.Max(maxX, size.number(1)/2), math.Max(maxY, size.number(2)/2)
				pad.Shape = "rect"
				pad.XNM += mmNM((minX + maxX) / 2)
				pad.YNM += mmNM((minY + maxY) / 2)
				pad.WidthNM, pad.HeightNM = mmNM(maxX-minX), mmNM(maxY-minY)
			}
			warnings = append(warnings, fmt.Sprintf("pad %q: custom shape approximated by its bounding rectangle", number))
		default:
			return nil, fmt.Errorf("pad %q has unsupported shape %q", number, shape)
		}
		if drill := node.child("drill"); drill != nil {
			if drill.value(1) == "oval" {
				dw, dh := mmNM(drill.number(2)), mmNM(drill.number(3))
				if dh == 0 {
					dh = dw
				}
				if dw == dh {
					pad.DrillNM = dw
				} else {
					pad.DrillWidthNM, pad.DrillHeightNM, pad.DrillNM = dw, dh, min64(dw, dh)
				}
			} else {
				pad.DrillNM = mmNM(drill.number(1))
			}
			if drill.child("offset") != nil {
				warnings = append(warnings, fmt.Sprintf("pad %q: drill offset ignored (hole centred on pad)", number))
			}
		}
		switch kind {
		case "thru_hole":
			hasTHT = true
			if len(layers) > 0 {
				pad.Layers = []string{"F.Cu", "B.Cu"}
			}
		case "np_thru_hole":
			hasTHT = true
			pad.NonPlated, pad.Plated = true, false
			// The validator requires a copper layer; an NPTH pad keeps the
			// hole outline on both sides with plated=false.
			pad.Layers = []string{"F.Cu", "B.Cu"}
			if pad.DrillNM == 0 {
				pad.DrillNM = min64(pad.WidthNM, pad.HeightNM)
			}
		default:
			hasSMD = true
		}
		if rotation := math.Mod(math.Mod(at.number(3), 360)+360, 360); rotation != 0 {
			switch {
			case rotation == 180:
			case rotation == 90 || rotation == 270:
				pad.WidthNM, pad.HeightNM = pad.HeightNM, pad.WidthNM
				pad.DrillWidthNM, pad.DrillHeightNM = pad.DrillHeightNM, pad.DrillWidthNM
			case pad.Shape == "circle":
			default:
				rad := rotation * math.Pi / 180
				c, s := math.Abs(math.Cos(rad)), math.Abs(math.Sin(rad))
				w, h := float64(pad.WidthNM), float64(pad.HeightNM)
				pad.WidthNM, pad.HeightNM = int64(math.Round(w*c+h*s)), int64(math.Round(w*s+h*c))
				pad.Shape = "rect"
				warnings = append(warnings, fmt.Sprintf("pad %q: %.1f degree rotation approximated by its bounding rectangle", number, rotation))
			}
		}
		pin := number
		if pin == "" {
			if kind == "np_thru_hole" {
				pin = "NPTH"
			} else {
				pin = "UNNUMBERED"
			}
		}
		pin = kicadSafeNativeID(pin, fmt.Sprintf("P%d", index+1))
		pad.PinID = pin
		padIDs[pin]++
		pad.ID = pin
		if padIDs[pin] > 1 {
			pad.ID = fmt.Sprintf("%s-%d", pin, padIDs[pin])
		}
		part.Footprint.Pads = append(part.Footprint.Pads, pad)
		if !pinSeen[pin] {
			pinSeen[pin] = true
			pinOrder = append(pinOrder, pin)
		}
	}
	if len(part.Footprint.Pads) == 0 {
		return nil, errors.New("KiCad footprint has no copper pads")
	}
	if skippedPaste > 0 {
		warnings = append(warnings, fmt.Sprintf("%d paste-only aperture(s) skipped; paste is derived from copper pads", skippedPaste))
	}
	for _, pin := range pinOrder {
		electrical := "passive"
		if pin == "NPTH" {
			electrical = "no_connect"
		}
		part.Pins = append(part.Pins, LibraryPin{Number: pin, Name: pin, ElectricalType: electrical})
	}
	part.Footprint.Mount = "smd"
	if hasTHT && hasSMD {
		part.Footprint.Mount = "mixed"
	} else if hasTHT {
		part.Footprint.Mount = "tht"
	}
	if attr := root.child("attr"); attr != nil && attr.value(1) == "through_hole" && part.Footprint.Mount == "smd" {
		part.Footprint.Mount = "tht"
	}
	padMinX, padMinY, padMaxX, padMaxY := padBounds(part.Footprint.Pads)
	if minX, minY, maxX, maxY, ok := kicadGraphicBounds(root, "F.CrtYd"); ok {
		part.Footprint.Courtyard = rectPoints(minX, minY, maxX, maxY)
	} else if minX, minY, maxX, maxY, ok := kicadGraphicBounds(root, "B.CrtYd"); ok {
		part.Footprint.Courtyard = rectPoints(minX, minY, maxX, maxY)
	} else {
		gap := int64(250_000)
		part.Footprint.Courtyard = rectPoints(padMinX-gap, padMinY-gap, padMaxX+gap, padMaxY+gap)
		warnings = append(warnings, "no F.CrtYd outline; courtyard derived from pad extents + 0.25 mm")
	}
	if minX, minY, maxX, maxY, ok := kicadGraphicBounds(root, "F.Fab"); ok {
		part.Footprint.BodyWidth, part.Footprint.BodyHeight = maxX-minX, maxY-minY
	} else {
		part.Footprint.BodyWidth, part.Footprint.BodyHeight = padMaxX-padMinX, padMaxY-padMinY
		warnings = append(warnings, "no F.Fab outline; body size derived from pad extents")
	}
	part.Footprint.LibraryID = name
	part.ID = libraryImportID(name)
	part.Designator = designatorPrefixFor(part)
	if err := validateLibraryPart(part); err != nil {
		return nil, err
	}
	return &KiCadFootprintImport{Part: part, Warnings: warnings}, nil
}

func libraryImportID(name string) string {
	id := kicadSafeNativeID("kicad:"+name, "")
	if id == "" || len(id) > 120 {
		sum := sha256.Sum256([]byte(name))
		id = "kicad:" + hex.EncodeToString(sum[:8])
	}
	return id
}

func kicadCopperLayers(node *sexpr) []string {
	out := []string{}
	if node == nil {
		return out
	}
	for _, value := range node.List[1:] {
		switch value.atom() {
		case "*.Cu", "F&B.Cu":
			return []string{"F.Cu", "B.Cu"}
		case "F.Cu", "B.Cu":
			if !containsString(out, value.atom()) {
				out = append(out, value.atom())
			}
		}
	}
	return out
}

// kicadGraphicBounds returns the bounding box (nm) of all graphic items on a
// layer: fp_line, fp_rect, fp_poly, fp_circle, fp_arc.
func kicadGraphicBounds(root *sexpr, layer string) (int64, int64, int64, int64, bool) {
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	add := func(x, y float64) {
		minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
	}
	for _, item := range root.List {
		switch item.atom() {
		case "fp_line", "fp_rect", "fp_poly", "fp_circle", "fp_arc":
		default:
			continue
		}
		if item.child("layer").value(1) != layer {
			continue
		}
		switch item.atom() {
		case "fp_circle":
			center, end := item.child("center"), item.child("end")
			r := math.Hypot(end.number(1)-center.number(1), end.number(2)-center.number(2))
			add(center.number(1)-r, center.number(2)-r)
			add(center.number(1)+r, center.number(2)+r)
		case "fp_poly":
			if pts := item.child("pts"); pts != nil {
				for _, xy := range pts.children("xy") {
					add(xy.number(1), xy.number(2))
				}
			}
		default:
			for _, key := range []string{"start", "mid", "end"} {
				if point := item.child(key); point != nil {
					add(point.number(1), point.number(2))
				}
			}
		}
	}
	if math.IsInf(minX, 1) {
		return 0, 0, 0, 0, false
	}
	return mmNM(minX), mmNM(minY), mmNM(maxX), mmNM(maxY), true
}

func kicadPrimitiveBounds(node *sexpr) (float64, float64, float64, float64, bool) {
	if node == nil {
		return 0, 0, 0, 0, false
	}
	minX, minY, maxX, maxY := math.Inf(1), math.Inf(1), math.Inf(-1), math.Inf(-1)
	var walk func(*sexpr)
	walk = func(n *sexpr) {
		for _, child := range n.List {
			switch child.atom() {
			case "xy", "start", "end", "mid", "center":
				x, y := child.number(1), child.number(2)
				minX, minY, maxX, maxY = math.Min(minX, x), math.Min(minY, y), math.Max(maxX, x), math.Max(maxY, y)
			}
			if len(child.List) > 0 {
				walk(child)
			}
		}
	}
	walk(node)
	if math.IsInf(minX, 1) {
		return 0, 0, 0, 0, false
	}
	return minX, minY, maxX, maxY, true
}

func padBounds(pads []Pad) (int64, int64, int64, int64) {
	var minX, minY, maxX, maxY int64 = math.MaxInt64, math.MaxInt64, math.MinInt64, math.MinInt64
	for _, pad := range pads {
		minX, minY = min64(minX, pad.XNM-pad.WidthNM/2), min64(minY, pad.YNM-pad.HeightNM/2)
		maxX, maxY = max64(maxX, pad.XNM+pad.WidthNM/2), max64(maxY, pad.YNM+pad.HeightNM/2)
	}
	return minX, minY, maxX, maxY
}

func rectPoints(minX, minY, maxX, maxY int64) []Point {
	return []Point{{XNM: minX, YNM: minY}, {XNM: maxX, YNM: minY}, {XNM: maxX, YNM: maxY}, {XNM: minX, YNM: maxY}}
}

func designatorPrefixFor(p *LibraryPart) string {
	if p.Designator != "" {
		return p.Designator
	}
	switch strings.ToLower(p.Category) {
	case "resistor":
		return "R"
	case "capacitor":
		return "C"
	case "led", "diode", "protection":
		return "D"
	case "connector":
		return "J"
	case "switch", "encoder":
		return "SW"
	case "relay":
		return "K"
	case "testpoint":
		return "TP"
	}
	name := strings.ToLower(p.Name)
	switch {
	case strings.HasPrefix(name, "r_"):
		return "R"
	case strings.HasPrefix(name, "c_"):
		return "C"
	case strings.HasPrefix(name, "led"), strings.HasPrefix(name, "d_"):
		return "D"
	case strings.Contains(name, "connector") || strings.Contains(name, "usb") || strings.Contains(name, "header") || strings.Contains(name, "jst"):
		return "J"
	case strings.Contains(name, "relay"):
		return "K"
	case strings.Contains(name, "encoder") || strings.Contains(name, "switch"):
		return "SW"
	case strings.Contains(name, "testpoint"):
		return "TP"
	}
	return "U"
}

// ---- instantiation ----

// LibraryPlacement is the caller-controlled part of instantiating a library
// part into a design.
type LibraryPlacement struct {
	ComponentID string
	Designator  string
	Name        string
	Value       string
	MPN         string
	Position    Position
}

// instantiateLibraryPart turns a library part into a native Component with
// pads, pins, a frozen footprint contract and sourcing identity. Back-side
// placement mirrors the land pattern in X and swaps outer copper layers.
func instantiateLibraryPart(def *Definition, part *LibraryPart, placement LibraryPlacement) (Component, error) {
	if part == nil {
		return Component{}, errors.New("library part is required")
	}
	if err := validateLibraryPart(part); err != nil {
		return Component{}, err
	}
	side := strings.TrimSpace(placement.Position.Side)
	if side == "" {
		side = "front"
	}
	if side != "front" && side != "back" {
		return Component{}, fmt.Errorf("side must be front or back, got %q", side)
	}
	designator := strings.TrimSpace(placement.Designator)
	if designator == "" {
		designator = nextDesignator(def, designatorPrefixFor(part))
	}
	for _, existing := range def.Components {
		if strings.EqualFold(existing.Designator, designator) {
			return Component{}, fmt.Errorf("designator %s is already used", designator)
		}
	}
	id := strings.TrimSpace(placement.ComponentID)
	if id == "" {
		id = kicadSafeNativeID(strings.ToLower(designator), "")
	}
	if !idValid(id) {
		return Component{}, fmt.Errorf("component id %q is invalid", id)
	}
	if findComponent(def.Components, id) >= 0 {
		return Component{}, fmt.Errorf("component %q already exists", id)
	}
	mirror := side == "back"
	pins := map[string]LibraryPin{}
	for _, pin := range part.Pins {
		pins[pin.Number] = pin
	}
	component := Component{
		ID: id, Designator: designator, Name: firstNonEmpty(placement.Name, part.Name), Value: firstNonEmpty(placement.Value, part.Value, part.MPN, part.Name),
		MPN: firstNonEmpty(placement.MPN, part.MPN), LCSC: part.LCSC, Footprint: part.Footprint.LibraryID,
		Position:     Position{XNM: placement.Position.XNM, YNM: placement.Position.YNM, RotationUdeg: placement.Position.RotationUdeg, Side: side},
		DatasheetRef: part.DatasheetURL,
	}
	if part.Footprint.BodyWidth > 0 && part.Footprint.BodyHeight > 0 {
		component.Body = &Body{WidthNM: part.Footprint.BodyWidth, HeightNM: part.Footprint.BodyHeight}
	}
	firstPad := map[string]string{}
	for _, source := range part.Footprint.Pads {
		pad := source
		pad.Layers = append([]string(nil), source.Layers...)
		applyPadDefaults(&pad)
		if mirror {
			pad.XNM = -pad.XNM
			for i, layer := range pad.Layers {
				switch layer {
				case "F.Cu":
					pad.Layers[i] = "B.Cu"
				case "B.Cu":
					pad.Layers[i] = "F.Cu"
				}
			}
			sort.Strings(pad.Layers)
			if len(pad.Layers) == 2 {
				pad.Layers = []string{"F.Cu", "B.Cu"}
			}
		}
		component.Pads = append(component.Pads, pad)
		if _, ok := firstPad[pad.PinID]; !ok {
			firstPad[pad.PinID] = pad.ID
		}
	}
	for _, pin := range part.Pins {
		component.Pins = append(component.Pins, Pin{ID: pin.Number, Number: pin.Number, Name: firstNonEmpty(pin.Name, pin.Number), ElectricalType: firstNonEmpty(pin.ElectricalType, "passive"), Pad: firstPad[pin.Number]})
	}
	if part.Model != nil {
		model := *part.Model
		model.PinRoles = map[string]string{}
		for role, pin := range part.Model.PinRoles {
			model.PinRoles[role] = pin
		}
		component.Model = &model
	}
	courtyard := make([]Point, len(part.Footprint.Courtyard))
	for i, point := range part.Footprint.Courtyard {
		if mirror {
			point.XNM = -point.XNM
		}
		courtyard[i] = point
	}
	contractBody, _ := json.Marshal(map[string]any{"library_id": part.Footprint.LibraryID, "revision": libraryRevision, "pads": component.Pads, "courtyard": courtyard})
	sum := sha256.Sum256(contractBody)
	compatible := []string{}
	for _, mpn := range append([]string{part.MPN}, part.AltMPNs...) {
		if mpn = strings.TrimSpace(mpn); mpn != "" && !containsString(compatible, mpn) {
			compatible = append(compatible, mpn)
		}
	}
	if component.MPN != "" && !containsString(compatible, component.MPN) {
		compatible = append(compatible, component.MPN)
	}
	standard := part.Provenance.Reference
	if standard == "" {
		standard = "IPC-7351B"
	}
	component.FootprintSpec = &FootprintSpec{
		LibraryID: part.Footprint.LibraryID, Revision: libraryRevision, Standard: standard,
		Source: "apteva://library/" + part.ID, SourceSHA256: hex.EncodeToString(sum[:]), Verified: true,
		PinCount: len(component.Pins), Courtyard: courtyard, AssemblyHeightNM: part.Footprint.HeightNM,
		CompatibleMPNs: compatible, LibraryPartID: part.ID, Model3DRef: part.Footprint.Model3DRef,
	}
	return component, nil
}

// applyPadDefaults mirrors the mask/paste defaults attachNativeContracts
// applies, so a library contract hash stays stable after normalization.
func applyPadDefaults(pad *Pad) {
	if pad.DrillNM > 0 || pad.DrillWidthNM > 0 || pad.DrillHeightNM > 0 {
		pad.Plated = !pad.NonPlated
		if pad.SolderMaskExpansionNM == 0 {
			pad.SolderMaskExpansionNM = 75_000
		}
		return
	}
	if pad.SolderMaskExpansionNM == 0 {
		pad.SolderMaskExpansionNM = 50_000
	}
	if pad.PasteExpansionNM == 0 {
		pad.PasteExpansionNM = -25_000
	}
}

func padIsPlated(pad Pad) bool { return !pad.NonPlated }

// libraryContractFrozen reports whether a component carries a footprint
// contract minted from the parts library; attachNativeContracts keeps those
// instead of re-deriving a courtyard from pad extents.
func libraryContractFrozen(component *Component) bool {
	spec := component.FootprintSpec
	return spec != nil && spec.LibraryPartID != "" && strings.HasPrefix(spec.Source, "apteva://library/") && len(spec.Courtyard) >= 4 && spec.LibraryID == component.Footprint
}

func attachLibraryModel3D(component *Component, models map[string]ComponentEnvelope) {
	envelope, ok := models[component.ID]
	if !ok {
		return
	}
	modelBody, _ := json.Marshal(envelope)
	modelSum := sha256.Sum256(modelBody)
	component.Model3D = &Model3DSpec{LibraryID: envelope.ModelRef, Revision: libraryRevision, SourceSHA256: hex.EncodeToString(modelSum[:]), WidthNM: envelope.WidthNM, DepthNM: envelope.DepthNM, HeightNM: envelope.ZMaxNM - envelope.ZMinNM, Verified: envelope.WidthNM > 0 && envelope.DepthNM > 0 && envelope.ZMaxNM > envelope.ZMinNM}
	component.FootprintSpec.AssemblyHeightNM = envelope.ZMaxNM - envelope.ZMinNM
}

// nextDesignator returns prefix+N with the lowest N not yet used.
func nextDesignator(def *Definition, prefix string) string {
	used := map[int]bool{}
	for _, component := range def.Components {
		d := strings.ToUpper(component.Designator)
		if !strings.HasPrefix(d, strings.ToUpper(prefix)) {
			continue
		}
		if n, err := strconv.Atoi(d[len(prefix):]); err == nil {
			used[n] = true
		}
	}
	for n := 1; ; n++ {
		if !used[n] {
			return fmt.Sprintf("%s%d", prefix, n)
		}
	}
}

// applyLibraryOperation implements component.add_from_library.
func applyLibraryOperation(def *Definition, op Operation) error {
	part := op.LibraryPart
	if part == nil {
		if op.LibraryPartID == "" {
			return errors.New("library_part_id or library_part is required")
		}
		builtin, ok := builtinLibraryPart(op.LibraryPartID)
		if !ok {
			return fmt.Errorf("library part %q is not built in; resolve project parts with pcb_component_place_from_library", op.LibraryPartID)
		}
		part = builtin
	} else if op.LibraryPartID != "" && op.LibraryPartID != part.ID {
		return fmt.Errorf("library_part_id %q does not match library_part.id %q", op.LibraryPartID, part.ID)
	}
	if op.Position == nil {
		return errors.New("position is required")
	}
	placement := LibraryPlacement{Position: *op.Position}
	if op.Component != nil {
		placement.ComponentID, placement.Designator, placement.Name, placement.Value, placement.MPN = op.Component.ID, op.Component.Designator, op.Component.Name, op.Component.Value, op.Component.MPN
	}
	component, err := instantiateLibraryPart(def, part, placement)
	if err != nil {
		return err
	}
	def.Components = append(def.Components, component)
	return nil
}

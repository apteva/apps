package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"
)

// Schematic capture model. Coordinates are integer nanometres on a sheet with
// +X to the right and +Y down (the KiCad schematic convention); the default
// grid is KiCad's 50 mil (1.27 mm). Rotations are visual counter-clockwise
// degrees in multiples of 90. Mirror "x" flips about the X axis (vertical
// flip) and "y" flips about the Y axis (horizontal flip); mirroring is applied
// after rotation.
const (
	schematicSchema     = "apteva-pcb-schematic/v1"
	schematicViewSchema = "apteva-pcb-schematic-view/v1"
	schematicGridNM     = int64(1_270_000)
	schematicCoordLimit = int64(1_000_000_000)
)

type SchematicSpec struct {
	Schema         string               `json:"schema,omitempty"`
	GridNM         int64                `json:"grid_nm,omitempty"`
	Sheets         []SchematicSheet     `json:"sheets"`
	Symbols        []SchematicSymbol    `json:"symbols"`
	Wires          []SchematicWire      `json:"wires"`
	Labels         []SchematicLabel     `json:"labels"`
	Junctions      []SchematicJunction  `json:"junctions"`
	LibrarySymbols []SchematicSymbolDef `json:"library_symbols,omitempty"`
	Source         string               `json:"source,omitempty"` // generated | kicad_sch | manual
}

type SchematicSheet struct {
	ID       string `json:"id"`
	Title    string `json:"title,omitempty"`
	Size     string `json:"size"` // A4 | A3 | A2 | A1 | A0 | custom
	WidthNM  int64  `json:"width_nm"`
	HeightNM int64  `json:"height_nm"`
}

// SchematicSymbol places one unit of a component's symbol on a sheet. Pins is
// a read-only cache of resolved absolute anchors written by the generator and
// importer; connectivity is always recomputed from the symbol definition.
type SchematicSymbol struct {
	ComponentID string               `json:"component_id"`
	SymbolID    string               `json:"symbol_id"`
	SheetID     string               `json:"sheet_id"`
	XNM         int64                `json:"x_nm"`
	YNM         int64                `json:"y_nm"`
	Rotation    int                  `json:"rotation_deg,omitempty"`
	Mirror      string               `json:"mirror,omitempty"`
	Unit        int                  `json:"unit,omitempty"`
	Pins        []SchematicPinAnchor `json:"pins,omitempty"`
}

type SchematicPinAnchor struct {
	Number string `json:"number"`
	PinID  string `json:"pin_id,omitempty"`
	XNM    int64  `json:"x_nm"`
	YNM    int64  `json:"y_nm"`
}

type SchematicWire struct {
	ID      string  `json:"id"`
	SheetID string  `json:"sheet_id"`
	Points  []Point `json:"points"`
}

type SchematicLabel struct {
	ID       string `json:"id"`
	SheetID  string `json:"sheet_id"`
	NetID    string `json:"net_id,omitempty"`
	Text     string `json:"text"`
	Kind     string `json:"kind"` // local | global | power
	XNM      int64  `json:"x_nm"`
	YNM      int64  `json:"y_nm"`
	Rotation int    `json:"rotation_deg,omitempty"`
}

type SchematicJunction struct {
	ID      string `json:"id,omitempty"`
	SheetID string `json:"sheet_id"`
	XNM     int64  `json:"x_nm"`
	YNM     int64  `json:"y_nm"`
}

// SchematicSymbolDef is a symbol in symbol-local screen coordinates (+Y down).
// A pin's anchor is its electrical connection point; AngleDeg is the visual
// direction from the anchor toward the body (0 right, 90 up, 180 left, 270
// down), the same convention as KiCad library pins.
type SchematicSymbolDef struct {
	ID    string               `json:"id"`
	Kind  string               `json:"kind"` // resistor | capacitor | led | diode | connector | ic | kicad
	Body  SchematicRect        `json:"body"`
	Pins  []SchematicSymbolPin `json:"pins"`
	Power bool                 `json:"power,omitempty"`
	Units int                  `json:"units,omitempty"`
	// Display hints carried over from KiCad libraries.
	PinNamesHidden   bool `json:"pin_names_hidden,omitempty"`
	PinNumbersHidden bool `json:"pin_numbers_hidden,omitempty"`
}

type SchematicRect struct {
	X1NM int64 `json:"x1_nm"`
	Y1NM int64 `json:"y1_nm"`
	X2NM int64 `json:"x2_nm"`
	Y2NM int64 `json:"y2_nm"`
}

type SchematicSymbolPin struct {
	Number         string `json:"number"`
	Name           string `json:"name,omitempty"`
	ElectricalType string `json:"electrical_type,omitempty"`
	XNM            int64  `json:"x_nm"`
	YNM            int64  `json:"y_nm"`
	AngleDeg       int    `json:"angle_deg"`
	LengthNM       int64  `json:"length_nm"`
	Unit           int    `json:"unit,omitempty"`
}

// Built-in library identifiers. Two-pin symbols bind to the component's pins
// numbered "1" and "2" when present, otherwise to its first two pins. The
// connector and IC symbols are generated from the component's pin list.
const (
	symResistor  = "apteva:R"
	symCapacitor = "apteva:C"
	symLED       = "apteva:LED"
	symDiode     = "apteva:D"
	symConnector = "apteva:CONN"
	symIC        = "apteva:IC"
)

func builtinSchematicSymbolIDs() []string {
	return []string{symResistor, symCapacitor, symLED, symDiode, symConnector, symIC}
}

func schematicLibraryResponse() map[string]any {
	return map[string]any{
		"schema":      schematicSchema,
		"units":       map[string]any{"geometry": "integer nanometres, +Y down", "grid_nm": schematicGridNM, "rotation": "visual counter-clockwise degrees (0/90/180/270)", "mirror": "x (vertical flip) | y (horizontal flip), applied after rotation"},
		"built_in":    builtinSchematicSymbolIDs(),
		"label_kinds": []string{"local", "global", "power"},
		"notes": []string{
			"apteva:R, apteva:C, apteva:LED and apteva:D are two-pin symbols; pin 1 is on the left (LED/diode anode) and pin 2 on the right (cathode). They bind to the component's pins numbered 1 and 2, otherwise to its first two pins.",
			"apteva:CONN is a vertical connector generated from the component's pins, anchors on the right at a 2.54 mm pitch.",
			"apteva:IC is a box generated from the component's pins: inputs and power inputs on the left, outputs and power outputs on the right, everything else balanced.",
			"Symbols imported from .kicad_sch are embedded in schematic.library_symbols under their KiCad lib_id.",
		},
		"operations": schematicOperationTypes(),
	}
}

func schematicOperationTypes() []string {
	return []string{"schematic.set", "schematic.symbol.place", "schematic.symbol.remove", "schematic.wire.add", "schematic.wire.remove", "schematic.label.add", "schematic.label.remove"}
}

func mmToNM(mm float64) int64 { return int64(mathRound(mm * 1e6)) }

// resolveSchematicSymbol returns the symbol definition for a placed symbol and
// the component pin IDs keyed by symbol pin number.
func resolveSchematicSymbol(spec *SchematicSpec, symbolID string, component *Component) (SchematicSymbolDef, map[string]string, error) {
	pinIDs := map[string]string{}
	if component != nil {
		for _, pin := range component.Pins {
			key := schematicPinKey(pin)
			if _, ok := pinIDs[key]; !ok {
				pinIDs[key] = pin.ID
			}
		}
	}
	if spec != nil {
		for _, def := range spec.LibrarySymbols {
			if def.ID == symbolID {
				out := map[string]string{}
				if component != nil {
					for _, pin := range def.Pins {
						if id := componentPinForNumber(component, pin.Number); id != "" {
							out[pin.Number] = id
						}
					}
				}
				return def, out, nil
			}
		}
	}
	switch symbolID {
	case symResistor, symCapacitor, symLED, symDiode:
		def := twoPinSymbol(symbolID)
		out := map[string]string{}
		if component != nil {
			first, second := componentPinForNumber(component, "1"), componentPinForNumber(component, "2")
			if (first == "" || second == "") && len(component.Pins) >= 2 {
				first, second = component.Pins[0].ID, component.Pins[1].ID
			} else if first == "" && second == "" && len(component.Pins) == 1 {
				first = component.Pins[0].ID
			}
			if first != "" {
				out["1"] = first
			}
			if second != "" {
				out["2"] = second
			}
			for i := range def.Pins {
				if id := out[def.Pins[i].Number]; id != "" {
					for _, pin := range component.Pins {
						if pin.ID == id {
							def.Pins[i].Name = pin.Name
							def.Pins[i].ElectricalType = pin.ElectricalType
						}
					}
				}
			}
		}
		return def, out, nil
	case symConnector:
		if component == nil {
			return SchematicSymbolDef{}, nil, errors.New("apteva:CONN needs a component")
		}
		return connectorSymbol(component), pinIDs, nil
	case symIC:
		if component == nil {
			return SchematicSymbolDef{}, nil, errors.New("apteva:IC needs a component")
		}
		return icSymbol(component), pinIDs, nil
	}
	return SchematicSymbolDef{}, nil, fmt.Errorf("unknown schematic symbol %q", symbolID)
}

func schematicPinKey(pin Pin) string {
	if strings.TrimSpace(pin.Number) != "" {
		return pin.Number
	}
	return pin.ID
}

func componentPinForNumber(component *Component, number string) string {
	for _, pin := range component.Pins {
		if pin.Number == number {
			return pin.ID
		}
	}
	for _, pin := range component.Pins {
		if pin.ID == number {
			return pin.ID
		}
	}
	return ""
}

func twoPinSymbol(id string) SchematicSymbolDef {
	g := schematicGridNM
	kind := map[string]string{symResistor: "resistor", symCapacitor: "capacitor", symLED: "led", symDiode: "diode"}[id]
	half := int64(1_016_000)
	if kind == "capacitor" || kind == "led" || kind == "diode" {
		half = 2 * g
	}
	return SchematicSymbolDef{
		ID: id, Kind: kind,
		Body: SchematicRect{X1NM: -2 * g, Y1NM: -half, X2NM: 2 * g, Y2NM: half},
		Pins: []SchematicSymbolPin{
			{Number: "1", ElectricalType: "passive", XNM: -3 * g, YNM: 0, AngleDeg: 0, LengthNM: g},
			{Number: "2", ElectricalType: "passive", XNM: 3 * g, YNM: 0, AngleDeg: 180, LengthNM: g},
		},
	}
}

func connectorSymbol(component *Component) SchematicSymbolDef {
	pitch := 2 * schematicGridNM
	n := int64(len(component.Pins))
	rows := max64(n, 1)
	def := SchematicSymbolDef{ID: symConnector, Kind: "connector", Body: SchematicRect{X1NM: -2 * pitch, Y1NM: -schematicGridNM, X2NM: schematicGridNM, Y2NM: (rows-1)*pitch + schematicGridNM}}
	for i, pin := range component.Pins {
		def.Pins = append(def.Pins, SchematicSymbolPin{Number: schematicPinKey(pin), Name: pin.Name, ElectricalType: pin.ElectricalType, XNM: 3 * schematicGridNM, YNM: int64(i) * pitch, AngleDeg: 180, LengthNM: 2 * schematicGridNM})
	}
	return def
}

func icPinSide(pin Pin) int {
	switch strings.ToLower(pin.ElectricalType) {
	case "input", "power_in":
		return -1
	case "output", "power_out", "open_collector", "open_emitter", "tri_state":
		return 1
	}
	return 0
}

func icSymbol(component *Component) SchematicSymbolDef {
	pitch := 2 * schematicGridNM
	left, right, either := []Pin{}, []Pin{}, []Pin{}
	for _, pin := range component.Pins {
		switch icPinSide(pin) {
		case -1:
			left = append(left, pin)
		case 1:
			right = append(right, pin)
		default:
			either = append(either, pin)
		}
	}
	for _, pin := range either {
		if len(left) <= len(right) {
			left = append(left, pin)
		} else {
			right = append(right, pin)
		}
	}
	rows := max64(int64(max(len(left), len(right))), 1)
	longest := func(pins []Pin) int {
		n := 0
		for _, pin := range pins {
			n = max(n, len([]rune(firstNonEmpty(pin.Name, pin.Number, pin.ID))))
		}
		return n
	}
	// ~1.27 mm per character at the renderer's pin-name size, plus a margin.
	halfWidth := int64(longest(left)+longest(right)+4) * schematicGridNM / 2
	halfWidth = max64(halfWidth, 2*pitch)
	halfWidth = (halfWidth + pitch - 1) / pitch * pitch
	def := SchematicSymbolDef{ID: symIC, Kind: "ic", Body: SchematicRect{X1NM: -halfWidth, Y1NM: -pitch, X2NM: halfWidth, Y2NM: rows * pitch}}
	for i, pin := range left {
		def.Pins = append(def.Pins, SchematicSymbolPin{Number: schematicPinKey(pin), Name: pin.Name, ElectricalType: pin.ElectricalType, XNM: -halfWidth - pitch, YNM: int64(i) * pitch, AngleDeg: 0, LengthNM: pitch})
	}
	for i, pin := range right {
		def.Pins = append(def.Pins, SchematicSymbolPin{Number: schematicPinKey(pin), Name: pin.Name, ElectricalType: pin.ElectricalType, XNM: halfWidth + pitch, YNM: int64(i) * pitch, AngleDeg: 180, LengthNM: pitch})
	}
	return def
}

// schematicTransform maps a symbol-local point to sheet coordinates.
func schematicTransform(sym SchematicSymbol, x, y int64) (int64, int64) {
	rx, ry := rotateVisual(x, y, sym.Rotation)
	switch sym.Mirror {
	case "x":
		ry = -ry
	case "y":
		rx = -rx
	}
	return sym.XNM + rx, sym.YNM + ry
}

// rotateVisual rotates counter-clockwise as seen on a +Y-down sheet.
func rotateVisual(x, y int64, deg int) (int64, int64) {
	switch ((deg % 360) + 360) % 360 {
	case 90:
		return y, -x
	case 180:
		return -x, -y
	case 270:
		return -y, x
	}
	return x, y
}

func pinDirection(angle int) (int64, int64) {
	switch ((angle % 360) + 360) % 360 {
	case 90:
		return 0, -1
	case 180:
		return -1, 0
	case 270:
		return 0, 1
	}
	return 1, 0
}

func symbolPinActive(pin SchematicSymbolPin, unit int) bool {
	return pin.Unit == 0 || unit == 0 || pin.Unit == unit
}

func normalizeSchematic(spec *SchematicSpec) {
	if spec == nil {
		return
	}
	if spec.Schema == "" {
		spec.Schema = schematicSchema
	}
	if spec.GridNM == 0 {
		spec.GridNM = schematicGridNM
	}
	if spec.Sheets == nil {
		spec.Sheets = []SchematicSheet{}
	}
	if len(spec.Sheets) == 0 {
		spec.Sheets = append(spec.Sheets, defaultSchematicSheet())
	}
	for i := range spec.Sheets {
		if spec.Sheets[i].Size == "" {
			spec.Sheets[i].Size = "A4"
		}
		if spec.Sheets[i].WidthNM == 0 || spec.Sheets[i].HeightNM == 0 {
			if w, h, ok := paperSizeNM(spec.Sheets[i].Size); ok {
				spec.Sheets[i].WidthNM, spec.Sheets[i].HeightNM = w, h
			}
		}
	}
	defaultSheet := spec.Sheets[0].ID
	if spec.Symbols == nil {
		spec.Symbols = []SchematicSymbol{}
	}
	for i := range spec.Symbols {
		if spec.Symbols[i].SheetID == "" {
			spec.Symbols[i].SheetID = defaultSheet
		}
		if spec.Symbols[i].Unit == 0 {
			spec.Symbols[i].Unit = 1
		}
	}
	if spec.Wires == nil {
		spec.Wires = []SchematicWire{}
	}
	for i := range spec.Wires {
		if spec.Wires[i].SheetID == "" {
			spec.Wires[i].SheetID = defaultSheet
		}
	}
	if spec.Labels == nil {
		spec.Labels = []SchematicLabel{}
	}
	for i := range spec.Labels {
		if spec.Labels[i].SheetID == "" {
			spec.Labels[i].SheetID = defaultSheet
		}
		if spec.Labels[i].Kind == "" {
			spec.Labels[i].Kind = "local"
		}
	}
	if spec.Junctions == nil {
		spec.Junctions = []SchematicJunction{}
	}
	for i := range spec.Junctions {
		if spec.Junctions[i].SheetID == "" {
			spec.Junctions[i].SheetID = defaultSheet
		}
	}
}

func defaultSchematicSheet() SchematicSheet {
	w, h, _ := paperSizeNM("A4")
	return SchematicSheet{ID: "main", Title: "Main", Size: "A4", WidthNM: w, HeightNM: h}
}

func paperSizeNM(size string) (int64, int64, bool) {
	sizes := map[string][2]int64{"A4": {297_000_000, 210_000_000}, "A3": {420_000_000, 297_000_000}, "A2": {594_000_000, 420_000_000}, "A1": {841_000_000, 594_000_000}, "A0": {1_189_000_000, 841_000_000}, "A": {279_400_000, 215_900_000}, "B": {431_800_000, 279_400_000}, "C": {558_800_000, 431_800_000}, "D": {863_600_000, 558_800_000}, "E": {1_117_600_000, 863_600_000}}
	v, ok := sizes[strings.ToUpper(strings.TrimSpace(size))]
	return v[0], v[1], ok
}

func schematicLimits(def *Definition) error {
	spec := def.Schematic
	if spec == nil {
		return nil
	}
	if len(spec.Sheets) > 64 || len(spec.Symbols) > 10_000 || len(spec.Wires) > 10_000 || len(spec.Labels) > 20_000 || len(spec.Junctions) > 20_000 || len(spec.LibrarySymbols) > 2_000 {
		return errors.New("schematic exceeds native v1 object limits")
	}
	inRange := func(v int64) bool { return v >= -schematicCoordLimit && v <= schematicCoordLimit }
	for _, sheet := range spec.Sheets {
		if sheet.WidthNM < 0 || sheet.HeightNM < 0 || sheet.WidthNM > 2*schematicCoordLimit || sheet.HeightNM > 2*schematicCoordLimit {
			return fmt.Errorf("schematic sheet %q size out of range", sheet.ID)
		}
	}
	for _, sym := range spec.Symbols {
		if !inRange(sym.XNM) || !inRange(sym.YNM) || len(sym.Pins) > 4_096 {
			return fmt.Errorf("schematic symbol for %q is out of range", sym.ComponentID)
		}
	}
	for _, wire := range spec.Wires {
		if len(wire.Points) > 1_024 {
			return fmt.Errorf("schematic wire %q exceeds point limit", wire.ID)
		}
		for _, p := range wire.Points {
			if !inRange(p.XNM) || !inRange(p.YNM) {
				return fmt.Errorf("schematic wire %q is out of range", wire.ID)
			}
		}
	}
	for _, label := range spec.Labels {
		if !inRange(label.XNM) || !inRange(label.YNM) || len(label.Text) > 256 {
			return fmt.Errorf("schematic label %q is out of range", label.ID)
		}
	}
	for _, j := range spec.Junctions {
		if !inRange(j.XNM) || !inRange(j.YNM) {
			return errors.New("schematic junction is out of range")
		}
	}
	for _, sym := range spec.LibrarySymbols {
		if len(sym.Pins) > 4_096 {
			return fmt.Errorf("schematic library symbol %q exceeds pin limit", sym.ID)
		}
		for _, pin := range sym.Pins {
			if !inRange(pin.XNM) || !inRange(pin.YNM) {
				return fmt.Errorf("schematic library symbol %q is out of range", sym.ID)
			}
		}
	}
	return nil
}

func ensureSchematic(def *Definition) *SchematicSpec {
	if def.Schematic == nil {
		def.Schematic = &SchematicSpec{Source: "manual"}
	}
	normalizeSchematic(def.Schematic)
	return def.Schematic
}

// applySchematicOperation implements the schematic.* operation family.
func applySchematicOperation(def *Definition, op Operation) error {
	switch op.Type {
	case "schematic.set":
		def.Schematic = op.Schematic
		normalizeSchematic(def.Schematic)
		return nil
	case "schematic.symbol.place":
		if op.SchematicSymbol == nil {
			return errors.New("schematic_symbol is required")
		}
		sym := *op.SchematicSymbol
		idx := findComponent(def.Components, sym.ComponentID)
		if idx < 0 {
			return fmt.Errorf("component %q not found", sym.ComponentID)
		}
		spec := ensureSchematic(def)
		if sym.SheetID == "" {
			sym.SheetID = spec.Sheets[0].ID
		}
		if sym.Unit == 0 {
			sym.Unit = 1
		}
		if sym.SymbolID == "" {
			// Moving an existing symbol may omit symbol_id.
			for _, existing := range spec.Symbols {
				if existing.ComponentID == sym.ComponentID && existing.Unit == sym.Unit {
					sym.SymbolID = existing.SymbolID
				}
			}
			if sym.SymbolID == "" {
				sym.SymbolID = defaultSymbolFor(def.Components[idx])
			}
		}
		if err := checkSymbolOrientation(sym); err != nil {
			return err
		}
		if findSchematicSheet(spec, sym.SheetID) < 0 {
			return fmt.Errorf("schematic sheet %q not found", sym.SheetID)
		}
		symDef, pinIDs, err := resolveSchematicSymbol(spec, sym.SymbolID, &def.Components[idx])
		if err != nil {
			return err
		}
		sym.Pins = symbolAnchors(sym, symDef, pinIDs)
		for i := range spec.Symbols {
			if spec.Symbols[i].ComponentID == sym.ComponentID && spec.Symbols[i].Unit == sym.Unit {
				spec.Symbols[i] = sym
				return nil
			}
		}
		spec.Symbols = append(spec.Symbols, sym)
	case "schematic.symbol.remove":
		if def.Schematic == nil {
			return errors.New("schematic is not initialized")
		}
		if !removeSchematicComponent(def, op.ComponentID) {
			return fmt.Errorf("no schematic symbol for component %q", op.ComponentID)
		}
	case "schematic.wire.add":
		if op.SchematicWire == nil {
			return errors.New("schematic_wire is required")
		}
		wire := *op.SchematicWire
		spec := ensureSchematic(def)
		if !idValid(wire.ID) {
			return fmt.Errorf("schematic wire id %q is invalid", wire.ID)
		}
		if len(wire.Points) < 2 {
			return errors.New("schematic wire needs at least two points")
		}
		if wire.SheetID == "" {
			wire.SheetID = spec.Sheets[0].ID
		}
		if findSchematicSheet(spec, wire.SheetID) < 0 {
			return fmt.Errorf("schematic sheet %q not found", wire.SheetID)
		}
		for _, w := range spec.Wires {
			if w.ID == wire.ID {
				return fmt.Errorf("schematic wire %q already exists", wire.ID)
			}
		}
		spec.Wires = append(spec.Wires, wire)
	case "schematic.wire.remove":
		if def.Schematic == nil {
			return errors.New("schematic is not initialized")
		}
		for i, w := range def.Schematic.Wires {
			if w.ID == op.SchematicWireID {
				def.Schematic.Wires = append(def.Schematic.Wires[:i], def.Schematic.Wires[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("schematic wire %q not found", op.SchematicWireID)
	case "schematic.label.add":
		if op.SchematicLabel == nil {
			return errors.New("schematic_label is required")
		}
		label := *op.SchematicLabel
		spec := ensureSchematic(def)
		if !idValid(label.ID) {
			return fmt.Errorf("schematic label id %q is invalid", label.ID)
		}
		if label.Kind == "" {
			label.Kind = "local"
		}
		if label.Kind != "local" && label.Kind != "global" && label.Kind != "power" {
			return fmt.Errorf("schematic label kind %q must be local, global, or power", label.Kind)
		}
		if strings.TrimSpace(label.Text) == "" {
			if label.NetID == "" {
				return errors.New("schematic label needs text or net_id")
			}
			n := findNet(def.Nets, label.NetID)
			if n < 0 {
				return fmt.Errorf("net %q not found", label.NetID)
			}
			label.Text = firstNonEmpty(def.Nets[n].Name, def.Nets[n].ID)
		}
		if label.SheetID == "" {
			label.SheetID = spec.Sheets[0].ID
		}
		if findSchematicSheet(spec, label.SheetID) < 0 {
			return fmt.Errorf("schematic sheet %q not found", label.SheetID)
		}
		for _, l := range spec.Labels {
			if l.ID == label.ID {
				return fmt.Errorf("schematic label %q already exists", label.ID)
			}
		}
		spec.Labels = append(spec.Labels, label)
	case "schematic.label.remove":
		if def.Schematic == nil {
			return errors.New("schematic is not initialized")
		}
		for i, l := range def.Schematic.Labels {
			if l.ID == op.SchematicLabelID {
				def.Schematic.Labels = append(def.Schematic.Labels[:i], def.Schematic.Labels[i+1:]...)
				return nil
			}
		}
		return fmt.Errorf("schematic label %q not found", op.SchematicLabelID)
	default:
		return fmt.Errorf("unsupported operation type %q", op.Type)
	}
	return nil
}

// removeSchematicComponent drops every placed unit of a component. It is also
// called when component.remove deletes the component itself.
func removeSchematicComponent(def *Definition, componentID string) bool {
	if def.Schematic == nil {
		return false
	}
	kept := def.Schematic.Symbols[:0]
	removed := false
	for _, sym := range def.Schematic.Symbols {
		if sym.ComponentID == componentID {
			removed = true
			continue
		}
		kept = append(kept, sym)
	}
	def.Schematic.Symbols = kept
	return removed
}

func checkSymbolOrientation(sym SchematicSymbol) error {
	if sym.Rotation%90 != 0 {
		return fmt.Errorf("schematic symbol rotation %d must be a multiple of 90", sym.Rotation)
	}
	if sym.Mirror != "" && sym.Mirror != "x" && sym.Mirror != "y" {
		return fmt.Errorf("schematic symbol mirror %q must be x or y", sym.Mirror)
	}
	return nil
}

func findSchematicSheet(spec *SchematicSpec, id string) int {
	for i := range spec.Sheets {
		if spec.Sheets[i].ID == id {
			return i
		}
	}
	return -1
}

func symbolAnchors(sym SchematicSymbol, def SchematicSymbolDef, pinIDs map[string]string) []SchematicPinAnchor {
	out := []SchematicPinAnchor{}
	for _, pin := range def.Pins {
		if !symbolPinActive(pin, sym.Unit) {
			continue
		}
		x, y := schematicTransform(sym, pin.XNM, pin.YNM)
		out = append(out, SchematicPinAnchor{Number: pin.Number, PinID: pinIDs[pin.Number], XNM: x, YNM: y})
	}
	return out
}

// defaultSymbolFor picks a built-in symbol from designator, model and pins.
func defaultSymbolFor(c Component) string {
	prefix := designatorPrefix(c.Designator)
	kind := ""
	if c.Model != nil {
		kind = c.Model.Kind
	}
	text := strings.ToLower(c.Name + " " + c.Value)
	if len(c.Pins) <= 2 {
		switch {
		case prefix == "R" || kind == "resistor":
			return symResistor
		case prefix == "C" || kind == "capacitor":
			return symCapacitor
		case kind == "led" || prefix == "LED" || (prefix == "D" && strings.Contains(text, "led")):
			return symLED
		case prefix == "D":
			return symDiode
		}
	}
	if isConnectorPrefix(prefix) {
		return symConnector
	}
	return symIC
}

func designatorPrefix(designator string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(designator)) {
		if r < 'A' || r > 'Z' {
			break
		}
		b.WriteRune(r)
	}
	return b.String()
}

func isConnectorPrefix(prefix string) bool {
	switch prefix {
	case "J", "P", "CN", "X", "TP", "CON", "USB", "H":
		return true
	}
	return false
}

func sortedSchematicSymbols(spec *SchematicSpec) []SchematicSymbol {
	out := append([]SchematicSymbol(nil), spec.Symbols...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ComponentID != out[j].ComponentID {
			return out[i].ComponentID < out[j].ComponentID
		}
		return out[i].Unit < out[j].Unit
	})
	return out
}

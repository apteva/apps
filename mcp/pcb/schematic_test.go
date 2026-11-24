package main

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func schematicLEDDefinition() Definition {
	def := emptyDefinition("LED indicator")
	two := func(id, designator, name, value string, pins ...Pin) Component {
		return Component{ID: id, Designator: designator, Name: name, Value: value, Position: Position{Side: "front"}, Pins: pins}
	}
	def.Components = []Component{
		two("j1", "J1", "Power input", "PWR_IN", Pin{ID: "1", Number: "1", Name: "VIN", ElectricalType: "passive"}, Pin{ID: "2", Number: "2", Name: "GND", ElectricalType: "passive"}),
		two("r1", "R1", "Resistor", "220", Pin{ID: "1", Number: "1", ElectricalType: "passive"}, Pin{ID: "2", Number: "2", ElectricalType: "passive"}),
		two("d1", "D1", "LED", "red", Pin{ID: "k", Number: "1", Name: "K", ElectricalType: "passive"}, Pin{ID: "a", Number: "2", Name: "A", ElectricalType: "passive"}),
	}
	def.Nets = []Net{
		{ID: "vin", Name: "VIN", Nodes: []Node{{ComponentID: "j1", PinID: "1"}, {ComponentID: "r1", PinID: "1"}}},
		{ID: "led_a", Name: "LED_A", Nodes: []Node{{ComponentID: "r1", PinID: "2"}, {ComponentID: "d1", PinID: "a"}}},
		{ID: "gnd", Name: "GND", Nodes: []Node{{ComponentID: "j1", PinID: "2"}, {ComponentID: "d1", PinID: "k"}}},
	}
	return def
}

func withGeneratedSchematic(t *testing.T, def Definition) *Definition {
	t.Helper()
	raw, _ := json.Marshal(def)
	_, base, _, err := normalizeDefinition(raw, def.Name)
	if err != nil {
		t.Fatal(err)
	}
	_, next, _, err := applyOperations(base, []Operation{{Type: "schematic.set", Schematic: generateSchematic(base)}})
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func ercErrors(report SchematicERC) []string {
	out := []string{}
	for _, c := range report.Checks {
		if c.Severity == "error" {
			out = append(out, c.Code+": "+c.Message)
		}
	}
	return out
}

func TestGeneratedSchematicPassesERCOnExamples(t *testing.T) {
	for _, example := range []Definition{dht22SensorNodeExample(), arduinoLEDExample(), raspberryPi4MechanicalExample(), schematicLEDDefinition()} {
		def := withGeneratedSchematic(t, example)
		report := runSchematicERC(def)
		if report.Errors != 0 || report.Status == "failed" {
			t.Fatalf("%s: generated schematic ERC errors: %v", example.Name, ercErrors(report))
		}
		for _, c := range report.Checks {
			if c.Code == "SCH_SYMBOL_MISSING" || c.Code == "SCH_WIRE_DANGLING" || c.Code == "SCH_LABEL_DANGLING" {
				t.Fatalf("%s: unexpected %s: %s", example.Name, c.Code, c.Message)
			}
		}
		if len(def.Schematic.Symbols) != len(def.Components) {
			t.Fatalf("%s: %d symbols for %d components", example.Name, len(def.Schematic.Symbols), len(def.Components))
		}
		for _, n := range report.Nets {
			if n.Status != "ok" {
				t.Fatalf("%s: net %s status %s", example.Name, n.NetID, n.Status)
			}
		}
		// The schematic hook must not change the board's validation verdict.
		before := validateDefinition(&example)
		after := validateDefinition(def)
		if before.Errors != after.Errors {
			t.Fatalf("%s: schematic changed validation errors %d -> %d", example.Name, before.Errors, after.Errors)
		}
		// No two symbols may overlap in the tidy grid.
		view := buildSchematicView(1, 1, def)
		syms := view.Sheets[0].Symbols
		for i := range syms {
			for j := i + 1; j < len(syms); j++ {
				a, b := syms[i].Bounds, syms[j].Bounds
				if a.X1NM < b.X2NM && b.X1NM < a.X2NM && a.Y1NM < b.Y2NM && b.Y1NM < a.Y2NM {
					t.Fatalf("%s: symbols %s and %s overlap", example.Name, syms[i].Designator, syms[j].Designator)
				}
			}
		}
	}
}

func TestGeneratedSchematicUsesPowerFlagsForRails(t *testing.T) {
	def := withGeneratedSchematic(t, dht22SensorNodeExample())
	kinds := map[string]string{}
	for _, l := range def.Schematic.Labels {
		kinds[l.Text] = l.Kind
	}
	for _, rail := range []string{"GND", "+3V3", "USB_5V"} {
		if kinds[rail] != "power" {
			t.Errorf("net %s label kind = %q, want power", rail, kinds[rail])
		}
	}
	if kinds["I2C_SDA"] != "local" {
		t.Errorf("signal label kind = %q", kinds["I2C_SDA"])
	}
	for _, sym := range def.Schematic.Symbols {
		if sym.ComponentID == "r1" && sym.SymbolID != symResistor || sym.ComponentID == "c1" && sym.SymbolID != symCapacitor || sym.ComponentID == "d2" && sym.SymbolID != symLED || sym.ComponentID == "j1" && sym.SymbolID != symConnector || sym.ComponentID == "u1" && sym.SymbolID != symIC {
			t.Errorf("component %s got symbol %s", sym.ComponentID, sym.SymbolID)
		}
	}
	if !isPowerNetName("VDD_IO") || !isPowerNetName("3.3V") || isPowerNetName("STATUS_LED_A") || isPowerNetName("USB_D+") {
		t.Fatal("power-net classifier is wrong")
	}
}

func TestSchematicERCCatchesBrokenConnection(t *testing.T) {
	def := withGeneratedSchematic(t, schematicLEDDefinition())
	// Re-label R1 pin 2 as GND: LED_A loses a node and GND gains one.
	for i, l := range def.Schematic.Labels {
		if l.Text == "LED_A" {
			for _, sym := range def.Schematic.Symbols {
				for _, p := range sym.Pins {
					if sym.ComponentID == "r1" && p.PinID == "2" && p.XNM == l.XNM && p.YNM == l.YNM {
						def.Schematic.Labels[i].Text, def.Schematic.Labels[i].Kind = "GND", "power"
					}
				}
			}
		}
	}
	report := runSchematicERC(def)
	codes := map[string]bool{}
	for _, c := range report.Checks {
		codes[c.Code] = true
	}
	if !codes["SCH_NET_MISSING_CONNECTION"] || !codes["SCH_NET_EXTRA_CONNECTION"] || report.Status != "failed" {
		t.Fatalf("broken connection not detected: %v", report.Checks)
	}
	statuses := map[string]string{}
	for _, n := range report.Nets {
		statuses[n.NetID] = n.Status
	}
	if statuses["led_a"] != "missing_connections" || statuses["gnd"] != "extra_connections" {
		t.Fatalf("net statuses = %v", statuses)
	}
	// It is surfaced through the main validator too.
	found := false
	for _, c := range validateDefinition(def).Checks {
		if c.Code == "SCH_NET_MISSING_CONNECTION" {
			found = true
		}
	}
	if !found {
		t.Fatal("validateDefinition does not include schematic ERC")
	}
}

func TestSchematicERCDanglingWireMissingSymbolAndInput(t *testing.T) {
	def := withGeneratedSchematic(t, schematicLEDDefinition())
	def.Components = append(def.Components, Component{ID: "u9", Designator: "U9", Name: "Buffer", Position: Position{Side: "front"}, Pins: []Pin{{ID: "in", Number: "1", Name: "IN", ElectricalType: "input"}}})
	def.Schematic.Wires = append(def.Schematic.Wires, SchematicWire{ID: "stub", SheetID: "main", Points: []Point{{XNM: 1_270_000, YNM: 1_270_000}, {XNM: 5_080_000, YNM: 1_270_000}}})
	report := runSchematicERC(def)
	codes := map[string]bool{}
	for _, c := range report.Checks {
		codes[c.Code] = true
	}
	if !codes["SCH_WIRE_DANGLING"] || !codes["SCH_SYMBOL_MISSING"] {
		t.Fatalf("expected dangling wire and missing symbol: %v", report.Checks)
	}
	def.Schematic.Symbols = append(def.Schematic.Symbols, SchematicSymbol{ComponentID: "u9", SymbolID: symIC, SheetID: "main", XNM: 254_000_000, YNM: 25_400_000, Unit: 1})
	report = runSchematicERC(def)
	codes = map[string]bool{}
	for _, c := range report.Checks {
		codes[c.Code] = true
	}
	if !codes["SCH_PIN_INPUT_UNCONNECTED"] || codes["SCH_SYMBOL_MISSING"] {
		t.Fatalf("expected unconnected input only: %v", report.Checks)
	}
}

func TestNoSchematicKeepsValidationUnchanged(t *testing.T) {
	def := dht22SensorNodeExample()
	for _, c := range validateDefinition(&def).Checks {
		if strings.HasPrefix(c.Code, "SCH_") {
			t.Fatalf("schematic check on design without schematic: %s", c.Code)
		}
	}
	if r := runSchematicERC(&def); r.Present || r.Status != "absent" {
		t.Fatalf("ERC on absent schematic = %#v", r)
	}
}

func TestSchematicOperationsRoundTrip(t *testing.T) {
	raw, _ := json.Marshal(schematicLEDDefinition())
	_, base, _, err := normalizeDefinition(raw, "")
	if err != nil {
		t.Fatal(err)
	}
	g := schematicGridNM
	ops := []Operation{
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "j1", SymbolID: symConnector, XNM: 20 * g, YNM: 20 * g}},
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "r1", SymbolID: symResistor, XNM: 40 * g, YNM: 20 * g}},
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "d1", SymbolID: symLED, XNM: 60 * g, YNM: 20 * g}},
		// j1 pin 1 is at (23g, 20g); r1 pin 1 at (37g, 20g).
		{Type: "schematic.wire.add", SchematicWire: &SchematicWire{ID: "w-vin", Points: []Point{{XNM: 23 * g, YNM: 20 * g}, {XNM: 37 * g, YNM: 20 * g}}}},
		// r1 pin 2 at (43g, 20g); LED is placed so its pin 1 (anode) is at (57g, 20g).
		{Type: "schematic.wire.add", SchematicWire: &SchematicWire{ID: "w-led", Points: []Point{{XNM: 43 * g, YNM: 20 * g}, {XNM: 57 * g, YNM: 20 * g}}}},
		{Type: "schematic.label.add", SchematicLabel: &SchematicLabel{ID: "gnd-j1", NetID: "gnd", Kind: "power", XNM: 23 * g, YNM: 22 * g}},
		{Type: "schematic.label.add", SchematicLabel: &SchematicLabel{ID: "gnd-d1", NetID: "gnd", Kind: "power", XNM: 63 * g, YNM: 20 * g}},
	}
	_, next, _, err := applyOperations(base, ops)
	if err != nil {
		t.Fatal(err)
	}
	report := runSchematicERC(next)
	// The built-in LED puts pin 1 on the left, but this component's pin 1 is
	// the cathode, so the hand-drawn circuit deliberately swaps LED_A and GND.
	if report.Errors == 0 {
		t.Fatal("expected the swapped LED orientation to fail ERC")
	}
	// Mirror the LED horizontally and the hand-drawn wiring now matches.
	fixed, next2, _, err := applyOperations(next, []Operation{{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "d1", XNM: 60 * g, YNM: 20 * g, Mirror: "y"}}})
	if err != nil {
		t.Fatal(err)
	}
	if r := runSchematicERC(next2); r.Errors != 0 {
		t.Fatalf("mirrored LED ERC errors: %v", ercErrors(r))
	}
	if next2.Schematic.Symbols[2].SymbolID != symLED {
		t.Fatal("moving a symbol without symbol_id lost its library symbol")
	}
	diff := semanticDiff(1, 2, next, next2, "a", "b")
	if !diff.SchematicChanged {
		t.Fatal("semantic diff missed a schematic change")
	}
	// Canonical JSON round-trips byte-for-byte.
	_, again, _, err := normalizeDefinition(fixed, "")
	if err != nil {
		t.Fatal(err)
	}
	b2, _ := json.Marshal(again)
	if !bytes.Equal(fixed, b2) {
		t.Fatal("schematic does not round-trip through normalization")
	}
	// Removal operations.
	_, next3, _, err := applyOperations(next2, []Operation{{Type: "schematic.wire.remove", SchematicWireID: "w-vin"}, {Type: "schematic.label.remove", SchematicLabelID: "gnd-d1"}, {Type: "schematic.symbol.remove", ComponentID: "r1"}})
	if err != nil {
		t.Fatal(err)
	}
	if len(next3.Schematic.Wires) != 1 || len(next3.Schematic.Labels) != 1 || len(next3.Schematic.Symbols) != 2 {
		t.Fatalf("removals not applied: %d wires %d labels %d symbols", len(next3.Schematic.Wires), len(next3.Schematic.Labels), len(next3.Schematic.Symbols))
	}
	// component.remove also drops its symbol.
	_, next4, _, err := applyOperations(next2, []Operation{{Type: "component.remove", ComponentID: "d1"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, sym := range next4.Schematic.Symbols {
		if sym.ComponentID == "d1" {
			t.Fatal("component.remove left a dangling schematic symbol")
		}
	}
	// Bad operations are rejected.
	for _, op := range []Operation{
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "nope", SymbolID: symResistor}},
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "r1", SymbolID: "apteva:UNKNOWN"}},
		{Type: "schematic.symbol.place", SchematicSymbol: &SchematicSymbol{ComponentID: "r1", SymbolID: symResistor, Rotation: 45}},
		{Type: "schematic.wire.add", SchematicWire: &SchematicWire{ID: "w-vin", Points: []Point{{}, {XNM: g}}}},
		{Type: "schematic.label.add", SchematicLabel: &SchematicLabel{ID: "x", Kind: "bogus", Text: "X"}},
	} {
		if _, _, _, err := applyOperations(next2, []Operation{op}); err == nil {
			t.Errorf("operation %s %+v was accepted", op.Type, op)
		}
	}
}

func TestSchematicRenderIsDeterministic(t *testing.T) {
	def := withGeneratedSchematic(t, dht22SensorNodeExample())
	a, err := renderSchematicSVG(def, "")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := renderSchematicSVG(def, "main")
	if !bytes.Equal(a, b) {
		t.Fatal("schematic SVG is not deterministic")
	}
	for _, want := range []string{"<svg", "U1", "ESP32-C3-MINI-1-N4", "I2C_SDA", "+3V3", `id="sch-u1-1"`} {
		if !bytes.Contains(a, []byte(want)) {
			t.Fatalf("schematic SVG lacks %q", want)
		}
	}
	imported := importFixture(t, schematicLEDDefinition(), false)
	c, err := renderSchematicSVG(imported, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"VIN", "LED_A", "GND", "R1", "<circle cx=\"88.9\" cy=\"50.8\""} {
		if !bytes.Contains(c, []byte(want)) {
			t.Fatalf("imported schematic SVG lacks %q", want)
		}
	}
	if _, err := renderSchematicSVG(def, "missing"); err == nil {
		t.Fatal("unknown sheet rendered")
	}
}

func importFixture(t *testing.T, def Definition, replace bool) *Definition {
	t.Helper()
	body, err := os.ReadFile("testdata/led_resistor.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(def)
	_, base, _, err := normalizeDefinition(raw, def.Name)
	if err != nil {
		t.Fatal(err)
	}
	ops, _, err := importKiCadSchematic(base, body, replace)
	if err != nil {
		t.Fatal(err)
	}
	_, next, _, err := applyOperations(base, ops)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

// Acceptance: the hand-written KiCad 8 fixture imports and its nets match the
// test definition's nets exactly.
func TestKiCadSchematicFixtureMatchesDefinitionNets(t *testing.T) {
	body, err := os.ReadFile("testdata/led_resistor.kicad_sch")
	if err != nil {
		t.Fatal(err)
	}
	def := schematicLEDDefinition()
	ops, report, err := importKiCadSchematic(&def, body, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "match" || len(report.Comparison.Matching) != 3 || len(report.Comparison.OnlyInSchematic) != 0 || len(report.Comparison.OnlyOnBoard) != 0 {
		raw, _ := json.MarshalIndent(report, "", "  ")
		t.Fatalf("fixture nets do not match:\n%s", raw)
	}
	names := map[string]string{}
	for _, m := range report.Comparison.Matching {
		names[m.NetID] = m.SchematicName
	}
	if names["vin"] != "VIN" || names["led_a"] != "LED_A" || names["gnd"] != "GND" {
		t.Fatalf("schematic net names = %v", names)
	}
	if strings.Join(report.MatchedSymbols, ",") != "D1,J1,R1" || len(report.UnmatchedSymbols) != 0 || len(report.UnmatchedPins) != 0 {
		t.Fatalf("symbol mapping = %+v", report)
	}
	if report.Counts["wires"] != 7 || report.Counts["junctions"] != 1 || report.Counts["labels"] != 4 {
		t.Fatalf("counts = %v", report.Counts)
	}
	_, next, _, err := applyOperations(&def, ops)
	if err != nil {
		t.Fatal(err)
	}
	erc := runSchematicERC(next)
	if erc.Errors != 0 || erc.Warnings != 0 {
		t.Fatalf("imported schematic ERC: %v", erc.Checks)
	}
	if len(next.Schematic.LibrarySymbols) != 3 {
		t.Fatalf("expected 3 embedded library symbols, got %d", len(next.Schematic.LibrarySymbols))
	}
	// Rotation and mirror are honoured: R1 (90 deg) is horizontal and J1
	// (mirrored) has its pins on the right.
	for _, sym := range next.Schematic.Symbols {
		for _, p := range sym.Pins {
			if sym.ComponentID == "r1" && p.Number == "1" && (p.XNM != 72_390_000 || p.YNM != 50_800_000) {
				t.Fatalf("R1 pin 1 anchor = %d,%d", p.XNM, p.YNM)
			}
			if sym.ComponentID == "j1" && p.Number == "2" && (p.XNM != 55_880_000 || p.YNM != 53_340_000) {
				t.Fatalf("J1 pin 2 anchor = %d,%d", p.XNM, p.YNM)
			}
		}
	}
}

func TestKiCadSchematicCreatesNetsAndReportsMismatch(t *testing.T) {
	bare := schematicLEDDefinition()
	bare.Nets = []Net{}
	next := importFixture(t, bare, true)
	want := map[string]string{}
	for _, net := range schematicLEDDefinition().Nets {
		want[nodeSetKey(net.Nodes)] = net.Name
	}
	if len(next.Nets) != 3 {
		t.Fatalf("created %d nets", len(next.Nets))
	}
	for _, net := range next.Nets {
		if want[nodeSetKey(net.Nodes)] != net.Name {
			t.Fatalf("created net %s %v does not match the reference definition", net.Name, net.Nodes)
		}
	}
	if erc := runSchematicERC(next); erc.Errors != 0 {
		t.Fatalf("ERC after net creation: %v", ercErrors(erc))
	}

	// A board whose LED net is wired differently is reported, not overwritten.
	wrong := schematicLEDDefinition()
	wrong.Nets[1].Nodes = []Node{{ComponentID: "r1", PinID: "2"}}
	wrong.Nets = append(wrong.Nets, Net{ID: "orphan", Name: "ORPHAN", Nodes: []Node{{ComponentID: "d1", PinID: "a"}}})
	body, _ := os.ReadFile("testdata/led_resistor.kicad_sch")
	_, report, err := importKiCadSchematic(&wrong, body, false)
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "mismatch" || len(report.Comparison.OnlyInSchematic) != 1 || len(report.Comparison.OnlyOnBoard) != 2 || len(report.Comparison.Matching) != 2 {
		raw, _ := json.MarshalIndent(report.Comparison, "", "  ")
		t.Fatalf("mismatch report:\n%s", raw)
	}
	// replace_nets keeps matching board net IDs and refuses to drop routed nets.
	wrong.Traces = []Trace{{ID: "t1", NetID: "orphan", Layer: "F.Cu", WidthNM: 200_000, Points: []Point{{}, {XNM: 1_000_000}}}}
	if _, _, err := importKiCadSchematic(&wrong, body, true); err == nil {
		t.Fatal("replace_nets dropped a net that carries copper")
	}
}

func TestKiCadSchematicRejectsHierarchyAndGarbage(t *testing.T) {
	def := schematicLEDDefinition()
	if _, _, err := importKiCadSchematic(&def, []byte(`(kicad_sch (version 20231120) (sheet (at 10 10) (size 20 20)))`), false); err == nil || !strings.Contains(err.Error(), "hierarchical") {
		t.Fatalf("hierarchical sheet not rejected: %v", err)
	}
	if _, _, err := importKiCadSchematic(&def, []byte(`(kicad_pcb (version 1))`), false); err == nil {
		t.Fatal("non-schematic accepted")
	}
	if _, _, err := importKiCadSchematic(&def, []byte(`(kicad_sch (version 1)`), false); err == nil {
		t.Fatal("malformed schematic accepted")
	}
}

func TestSchematicServiceFlow(t *testing.T) {
	store := testStore(t)
	s := &Service{store: store, project: "project-a", artifactRoot: t.TempDir()}
	raw, _ := json.Marshal(schematicLEDDefinition())
	canonical, _, hash, err := normalizeDefinition(raw, "LED")
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("project-a", "LED", canonical, nil, hash, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	revision, view, err := s.SchematicGenerate(design.ID, design.CurrentRevisionID, false, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if !view.Present || view.ERC.Status != "passed" || len(view.Sheets) != 1 || len(view.Sheets[0].Symbols) != 3 {
		t.Fatalf("generated view = %+v", view.ERC)
	}
	for _, sym := range view.Sheets[0].Symbols {
		for _, p := range sym.Pins {
			if p.PinID == "" || p.NetID == "" || p.SchematicNet == "" {
				t.Fatalf("pin view incomplete: %s %+v", sym.Designator, p)
			}
		}
	}
	if _, _, err := s.SchematicGenerate(design.ID, revision.ID, false, "", "test"); err == nil {
		t.Fatal("regenerated over an existing schematic without replace_existing")
	}
	if _, _, err := s.SchematicGenerate(design.ID, design.CurrentRevisionID, true, "", "test"); err == nil {
		t.Fatal("stale expected_parent_id accepted")
	}
	body, _ := os.ReadFile("testdata/led_resistor.kicad_sch")
	imported, report, erc, err := s.KiCadSchematicImport(design.ID, revision.ID, body, false, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != "match" || erc.Status != "passed" {
		t.Fatalf("import report %s erc %s", report.Status, erc.Status)
	}
	got, err := s.SchematicGet(design.ID, imported.ID)
	if err != nil || got.Schematic.Source != "kicad_sch" || got.SVGPath == "" {
		t.Fatalf("schematic get = %+v, %v", got, err)
	}
	artifact, err := s.SchematicRender(design.ID, 0, "")
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "schematic" || artifact.ContentType != "image/svg+xml" {
		t.Fatalf("artifact = %+v", artifact)
	}
}

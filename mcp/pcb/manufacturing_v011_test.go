package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func fabFunctions(t *testing.T, files map[string][]byte) map[string]string {
	t.Helper()
	var job struct {
		Files []struct{ Path, FileFunction, FilePolarity string } `json:"FilesAttributes"`
	}
	if err := json.Unmarshal(files["board.gbrjob"], &job); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range job.Files {
		out[f.Path] = f.FileFunction + "|" + f.FilePolarity
	}
	return out
}

func mustVerify(t *testing.T, def *Definition, files map[string][]byte) *FabricationVerification {
	t.Helper()
	report := verifyManufacturingFiles(def, files)
	if report.Status != "passed" {
		t.Fatalf("fabrication verification failed: %+v", report.Checks)
	}
	return report
}

// Item 1: solder mask and paste layers.
func TestMaskAndPasteLayersPerSide(t *testing.T) {
	def := dhtExample(t)
	files := manufacturingFiles(&def)
	mustVerify(t, &def, files)
	functions := fabFunctions(t, files)
	for path, want := range map[string]string{
		"gerbers/F_Mask.gbr":  "Soldermask,Top|Negative",
		"gerbers/B_Mask.gbr":  "Soldermask,Bot|Negative",
		"gerbers/F_Paste.gbr": "Paste,Top|Positive",
	} {
		if functions[path] != want {
			t.Errorf("%s job function = %q, want %q", path, functions[path], want)
		}
		if !bytes.Contains(files[path], []byte("%TF.FileFunction,"+strings.Split(want, "|")[0]+"*%")) {
			t.Errorf("%s lacks its TF.FileFunction header", path)
		}
	}
	// Bottom has only through-hole pads, so it gets mask but no stencil.
	if _, ok := files["gerbers/B_Paste.gbr"]; ok {
		t.Error("B_Paste emitted for a side without SMD pads")
	}
	// Mask openings: every pad on the side plus the four NPTH mounting
	// holes; vias are tented by default.
	front, back := boardSide{"Top", "F", "F.Cu"}, boardSide{"Bot", "B", "B.Cu"}
	if maskOpeningCount(&def, front) == 0 || maskOpeningCount(&def, back) != 8+4 {
		t.Fatalf("mask openings front=%d back=%d", maskOpeningCount(&def, front), maskOpeningCount(&def, back))
	}
	tented := maskOpeningCount(&def, back)
	def.Rules.UntentedVias = true
	if got := maskOpeningCount(&def, back); got != tented+len(def.Vias) || len(def.Vias) == 0 {
		t.Fatalf("untented vias not opened: %d vs %d + %d", got, tented, len(def.Vias))
	}
	mustVerify(t, &def, manufacturingFiles(&def))
	// Paste excludes through-hole pads entirely.
	smd := 0
	for _, component := range def.Components {
		for _, pad := range component.Pads {
			if pad.DrillNM == 0 && containsString(pad.Layers, "F.Cu") {
				smd++
			}
		}
	}
	if got := len(pastePads(&def, front)); got != smd {
		t.Fatalf("paste apertures=%d, SMD pads=%d", got, smd)
	}
}

func TestMaskExpansionAndPasteRatioShapes(t *testing.T) {
	component := Component{ID: "u1", Position: Position{XNM: 10_000_000, YNM: 10_000_000, Side: "front"}}
	def := &Definition{Board: Board{WidthNM: 20_000_000, HeightNM: 20_000_000, Layers: []Layer{{ID: "F.Cu", Kind: "copper"}}}}
	pad := Pad{ID: "1", Shape: "rect", WidthNM: 1_000_000, HeightNM: 600_000, Layers: []string{"F.Cu"}}
	component.Pads = []Pad{pad}
	def.Components = []Component{component}
	mask := string(renderMaskGerber(def, boardSide{"Top", "F", "F.Cu"}))
	if !strings.Contains(mask, "%ADD11R,1.100000X0.700000*%") {
		t.Fatalf("default 0.05 mm mask expansion missing:\n%s", mask)
	}
	def.Rules.SolderMaskExpansionNM = 100_000
	if mask := string(renderMaskGerber(def, boardSide{"Top", "F", "F.Cu"})); !strings.Contains(mask, "R,1.200000X0.800000") {
		t.Fatalf("rule mask expansion ignored:\n%s", mask)
	}
	paste, ok := pastePad(Pad{Shape: "rect", WidthNM: 1_000_000, HeightNM: 600_000, PasteRatio: -0.1, PasteExpansionNM: -10_000})
	if !ok || paste.WidthNM != 1_000_000-2*110_000 || paste.HeightNM != 600_000-2*70_000 {
		t.Fatalf("paste ratio/margin = %+v", paste)
	}
	if _, ok := pastePad(Pad{Shape: "circle", WidthNM: 1_000_000, HeightNM: 1_000_000, NoPaste: true}); ok {
		t.Fatal("no_paste pad produced a stencil aperture")
	}
	if _, ok := pastePad(Pad{Shape: "circle", WidthNM: 1_500_000, HeightNM: 1_500_000, DrillNM: 800_000}); ok {
		t.Fatal("through-hole pad produced a stencil aperture")
	}
}

// Item 2: rounded board corners use true arcs.
func TestBoardCornerRadiusUsesArcs(t *testing.T) {
	def := manufacturablePairDefinition()
	def.Board.CornerRadiusNM = 2_000_000
	files := manufacturingFiles(&def)
	report := mustVerify(t, &def, files)
	edge := string(files["gerbers/Edge_Cuts.gbr"])
	if strings.Count(edge, "G03*") != 4 || !strings.Contains(edge, "G75*") {
		t.Fatalf("Edge_Cuts lacks four G03 corner arcs:\n%s", edge)
	}
	if !strings.Contains(edge, "X30000000Y2000000I0J2000000D01*") {
		t.Fatalf("bottom-right corner arc missing:\n%s", edge)
	}
	if metrics := fileMetrics(report.Files, "gerbers/Edge_Cuts.gbr"); metrics.Arcs != 4 || metrics.Draws != 8 {
		t.Fatalf("edge metrics %+v", metrics)
	}
	polygon := boardOutline(def.Board)
	for _, p := range polygon {
		// Every flattened point stays within the rounded profile.
		cx, cy := math.Max(2e6, math.Min(28e6, float64(p.XNM))), math.Max(2e6, math.Min(18e6, float64(p.YNM)))
		if math.Hypot(float64(p.XNM)-cx, float64(p.YNM)-cy) > 2e6+2 {
			t.Fatalf("outline point %+v lies outside the 2 mm corner", p)
		}
	}
	if len(polygon) <= 4 || containsPoint(polygon, Point{XNM: 0, YNM: 0}) {
		t.Fatalf("DRC polygon ignores the corner radius: %v", polygon)
	}
}

func containsPoint(points []Point, want Point) bool {
	for _, p := range points {
		if p == want {
			return true
		}
	}
	return false
}

// Item 3: plated and non-plated drill programs.
func TestPlatedAndNonPlatedDrillPrograms(t *testing.T) {
	def := dhtExample(t)
	def.Mechanical.MountingHoles[0].Plated = true
	files := manufacturingFiles(&def)
	mustVerify(t, &def, files)
	pth, npth := string(files[pthDrillPath]), string(files[npthDrillPath])
	if !strings.Contains(pth, "; #@! TF.FileFunction,Plated,1,2,PTH") || !strings.Contains(npth, "; #@! TF.FileFunction,NonPlated,1,2,NPTH") {
		t.Fatalf("drill headers:\n%s\n%s", pth, npth)
	}
	if !strings.Contains(pth, "X3.500000Y3.500000") || strings.Contains(npth, "X3.500000Y3.500000") {
		t.Fatal("plated mounting hole not in the PTH program")
	}
	if strings.Count(npth, "\nX") != 3 || !strings.Contains(npth, "T01C2.700000") {
		t.Fatalf("non-plated mounting holes missing from NPTH:\n%s", npth)
	}
	functions := fabFunctions(t, files)
	if functions[pthDrillPath] != "Plated,1,2,PTH|" || functions[npthDrillPath] != "NonPlated,1,2,NPTH|" {
		t.Fatalf("gbrjob drill functions %v", functions)
	}
	if _, ok := files["drill/board.drl"]; ok {
		t.Fatal("legacy combined drill file still emitted")
	}
	// A non-plated pad (np_thru_hole) moves to NPTH and the verifier catches
	// a hole that lands in the wrong program.
	for i := range def.Components {
		if def.Components[i].ID == "u4" {
			def.Components[i].Pads[2].NonPlated = true
		}
	}
	files = manufacturingFiles(&def)
	mustVerify(t, &def, files)
	swapped := map[string][]byte{}
	for k, v := range files {
		swapped[k] = v
	}
	swapped[pthDrillPath], swapped[npthDrillPath] = files[npthDrillPath], files[pthDrillPath]
	report := verifyManufacturingFiles(&def, swapped)
	if report.Status != "failed" || codes(report.Checks)["FAB_DRILL_RECONCILIATION"] == 0 {
		t.Fatalf("swapped drill programs accepted: %+v", report.Checks)
	}
}

// Item 4: oval drills become G85 slots rotated with the component.
func TestOvalDrillSlotsAndPlatedSlotCutouts(t *testing.T) {
	def := manufacturablePairDefinition()
	shell := Component{ID: "j9", Designator: "J9", Footprint: "USB_C_Shell", Position: Position{XNM: 15_000_000, YNM: 10_000_000, RotationUdeg: 90_000_000, Side: "front"},
		Pins: []Pin{{ID: "s", Pad: "s"}},
		Pads: []Pad{{ID: "s", PinID: "s", Shape: "oval", XNM: 2_000_000, WidthNM: 1_000_000, HeightNM: 2_200_000, DrillWidthNM: 600_000, DrillHeightNM: 1_600_000, Layers: []string{"F.Cu", "B.Cu"}}}}
	def.Components = append(def.Components, shell)
	def.Board.Cutouts = []BoardCutout{
		{ID: "slot-p", Kind: "slot", XNM: 8_000_000, YNM: 16_000_000, WidthNM: 3_000_000, HeightNM: 1_000_000, Plated: true},
		{ID: "slot-np", Kind: "slot", XNM: 22_000_000, YNM: 16_000_000, WidthNM: 3_000_000, HeightNM: 1_000_000},
	}
	files := manufacturingFiles(&def)
	report := mustVerify(t, &def, files)
	pth := string(files[pthDrillPath])
	// Pad at local (+2, 0) rotated 90 degrees sits at (15, 12); its vertical
	// 1.6 mm drill becomes horizontal: 0.6 mm tool from x=14.5 to x=15.5.
	if !strings.Contains(pth, "X14.500000Y12.000000G85X15.500000Y12.000000") {
		t.Fatalf("rotated pad slot missing:\n%s", pth)
	}
	if !strings.Contains(pth, "X7.000000Y16.000000G85X9.000000Y16.000000") {
		t.Fatalf("plated slot cutout missing:\n%s", pth)
	}
	if report.Summary["slots"] != 2 {
		t.Fatalf("parsed slots=%d", report.Summary["slots"])
	}
	edge := string(files["gerbers/Edge_Cuts.gbr"])
	if strings.Contains(edge, "X7000000") || !strings.Contains(edge, "X23000000Y15500000D01*") {
		t.Fatalf("plated slot leaked into Edge_Cuts or non-plated slot missing:\n%s", edge)
	}
	// Corrupting a slot line is detected by the independent parser.
	broken := map[string][]byte{}
	for k, v := range files {
		broken[k] = v
	}
	broken[pthDrillPath] = bytes.Replace(files[pthDrillPath], []byte("G85X15.500000"), []byte("G85X95.500000"), 1)
	if verifyManufacturingFiles(&def, broken).Status != "failed" {
		t.Fatal("out-of-board slot accepted")
	}
}

// Item 5: true pad shapes in Gerber and DRC.
func TestPadShapesAreExact(t *testing.T) {
	def := &Definition{Board: Board{WidthNM: 20_000_000, HeightNM: 20_000_000, Layers: []Layer{{ID: "F.Cu", Kind: "copper"}}}}
	at := func(id string, rotation int64, pad Pad) Component {
		pad.ID, pad.PinID, pad.Layers = "1", "1", []string{"F.Cu"}
		return Component{ID: id, Position: Position{XNM: 10_000_000, YNM: 10_000_000, RotationUdeg: rotation, Side: "front"}, Pads: []Pad{pad}}
	}
	def.Components = []Component{
		at("c", 0, Pad{Shape: "circle", WidthNM: 1_000_000, HeightNM: 1_000_000}),
		at("o", 90_000_000, Pad{Shape: "oval", WidthNM: 2_000_000, HeightNM: 1_000_000}),
		at("r", 0, Pad{Shape: "roundrect", WidthNM: 2_000_000, HeightNM: 1_000_000}),
		at("t", 30_000_000, Pad{Shape: "oval", WidthNM: 2_000_000, HeightNM: 1_000_000}),
	}
	copper := string(renderCopperGerber(def, "F.Cu", "Copper,L1,Top"))
	for _, want := range []string{"C,1.000000*%", "O,1.000000X2.000000*%", "G36*", "G03*"} {
		if !strings.Contains(copper, want) {
			t.Fatalf("copper lacks %q:\n%s", want, copper)
		}
	}
	// Roundrect default ratio 0.25 of the 1 mm short side: 0.25 mm corners.
	if !strings.Contains(copper, "I0J250000D01*") {
		t.Fatalf("roundrect corner radius not 0.25 mm:\n%s", copper)
	}
	// Rotated oval region: both semicircle arcs, endpoints rotated exactly.
	tilted := placedPadContour(def.Components[3], def.Components[3].Pads[0], 0)
	arcs := 0
	for _, seg := range tilted.Segs {
		if seg.Arc {
			arcs++
			r := math.Hypot(float64(seg.End.XNM-seg.Center.XNM), float64(seg.End.YNM-seg.Center.YNM))
			if math.Abs(r-500_000) > 1 {
				t.Fatalf("obround arc radius %.1f", r)
			}
		}
	}
	if arcs != 4 {
		t.Fatalf("tilted obround has %d arcs", arcs)
	}
	files := map[string][]byte{"gerbers/F_Cu.gbr": []byte(copper)}
	if _, issues := parseGerberForVerification("gerbers/F_Cu.gbr", files["gerbers/F_Cu.gbr"], def.Board); len(issues) != 0 {
		t.Fatalf("pad copper does not parse cleanly: %+v", issues)
	}
	// DRC polygons follow the same true shapes.
	roundrect := padLocalPolygon(Pad{Shape: "roundrect", WidthNM: 2_000_000, HeightNM: 1_000_000})
	if containsPoint(roundrect, Point{XNM: 1_000_000, YNM: 500_000}) || !containsPoint(roundrect, Point{XNM: 750_000, YNM: 500_000}) {
		t.Fatalf("roundrect DRC polygon is not a rounded rectangle: %v", roundrect)
	}
	oval := padLocalPolygon(Pad{Shape: "oval", WidthNM: 2_000_000, HeightNM: 1_000_000})
	if !containsPoint(oval, Point{XNM: 500_000, YNM: 500_000}) || !containsPoint(oval, Point{XNM: 1_000_000, YNM: 0}) {
		t.Fatalf("oval DRC polygon is not an obround: %v", oval)
	}
}

func fourLayerDefinition() Definition {
	def := manufacturablePairDefinition()
	def.Board.Layers = []Layer{{ID: "F.Cu", Kind: "copper", Order: 0}, {ID: "In1.Cu", Kind: "copper", Order: 1}, {ID: "In2.Cu", Kind: "copper", Order: 2}, {ID: "B.Cu", Kind: "copper", Order: 3}, {ID: "F.SilkS", Kind: "silkscreen", Order: 4}, {ID: "Edge.Cuts", Kind: "mechanical", Order: 5}}
	def.Zones = append(def.Zones, Zone{ID: "gnd_plane", NetID: "gnd", Layer: "In1.Cu", Polygon: []Point{{XNM: 1_000_000, YNM: 1_000_000}, {XNM: 29_000_000, YNM: 1_000_000}, {XNM: 29_000_000, YNM: 19_000_000}, {XNM: 1_000_000, YNM: 19_000_000}}})
	def.Traces = append(def.Traces, Trace{ID: "inner", NetID: "gnd", Layer: "In2.Cu", WidthNM: 300_000, Points: []Point{{XNM: 2_000_000, YNM: 5_000_000}, {XNM: 28_000_000, YNM: 5_000_000}}})
	def.Vias = []Via{
		{ID: "through", NetID: "gnd", XNM: 3_000_000, YNM: 5_000_000, DiameterNM: 600_000, DrillNM: 300_000, FromLayer: "F.Cu", ToLayer: "B.Cu"},
		{ID: "blind", NetID: "gnd", XNM: 27_000_000, YNM: 5_000_000, DiameterNM: 600_000, DrillNM: 300_000, FromLayer: "F.Cu", ToLayer: "In1.Cu"},
	}
	return def
}

// Item 6: copper numbering and KiCad inner layers.
func TestFourLayerCopperNumberingAndKiCadRoundTrip(t *testing.T) {
	def := fourLayerDefinition()
	files := manufacturingFiles(&def)
	mustVerify(t, &def, files)
	functions := fabFunctions(t, files)
	for path, want := range map[string]string{"gerbers/F_Cu.gbr": "Copper,L1,Top", "gerbers/In1_Cu.gbr": "Copper,L2,Inr", "gerbers/In2_Cu.gbr": "Copper,L3,Inr", "gerbers/B_Cu.gbr": "Copper,L4,Bot"} {
		if functions[path] != want+"|Positive" || !bytes.Contains(files[path], []byte("%TF.FileFunction,"+want+"*%")) {
			t.Errorf("%s labelled %q", path, functions[path])
		}
	}
	if !strings.Contains(string(files[pthDrillPath]), "Plated,1,4,PTH") {
		t.Fatal("PTH span does not cover four layers")
	}
	// The blind via (F.Cu..In1.Cu) appears on L1/L2 only.
	if bytes.Contains(files["gerbers/In2_Cu.gbr"], []byte("X27000000Y5000000D03*")) || !bytes.Contains(files["gerbers/In1_Cu.gbr"], []byte("X27000000Y5000000D03*")) {
		t.Fatal("blind via span wrong on inner layers")
	}
	mislabelled := map[string][]byte{}
	for k, v := range files {
		mislabelled[k] = v
	}
	mislabelled["gerbers/In1_Cu.gbr"] = bytes.Replace(files["gerbers/In1_Cu.gbr"], []byte("Copper,L2,Inr"), []byte("Copper,L2,Bot"), 1)
	if report := verifyManufacturingFiles(&def, mislabelled); codes(report.Checks)["FAB_COPPER_NUMBERING"] == 0 {
		t.Fatalf("mislabelled inner layer accepted: %+v", report.Checks)
	}

	board := renderKiCadPCB(&def)
	text := string(board)
	// KiCad is +Y down: the via at y=5 mm sits 5 mm below the board's top edge.
	blindAt := fmt.Sprintf("(via blind (at 27.000000 %.6f)", float64(boardTopNM(&def)-5_000_000)/1e6)
	for _, want := range []string{`(0 "F.Cu" signal)`, `(1 "In1.Cu" signal)`, `(2 "In2.Cu" signal)`, `(31 "B.Cu" signal)`, `(layer "In2.Cu")`, blindAt, `(layers "F.Cu" "In1.Cu")`} {
		if !strings.Contains(text, want) {
			t.Fatalf("KiCad export lacks %s", want)
		}
	}
	if report := verifyKiCadPCB(&def, board); report.Status != "passed" || report.Counts["copper_layers"] != 4 {
		t.Fatalf("KiCad verification %+v", report)
	}
	imported, err := importKiCadPCB("Four layer", board)
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{}
	for _, layer := range copperLayers(imported) {
		ids = append(ids, layer.ID)
	}
	if strings.Join(ids, ",") != "F.Cu,In1.Cu,In2.Cu,B.Cu" {
		t.Fatalf("imported copper stack %v", ids)
	}
	inner := false
	for _, trace := range imported.Traces {
		inner = inner || trace.Layer == "In2.Cu"
	}
	if !inner || len(imported.Vias) != 2 || imported.Vias[1].ToLayer != "In1.Cu" {
		t.Fatalf("inner trace/blind via lost in round trip: %+v", imported.Vias)
	}
	if v := imported.Vias[1]; v.XNM != 27_000_000 || v.YNM != 5_000_000 {
		t.Fatalf("blind via moved in KiCad round trip: (%d, %d)", v.XNM, v.YNM)
	}
	again := renderKiCadPCB(imported)
	if report := verifyKiCadPCB(imported, again); report.Counts["copper_layers"] != 4 {
		t.Fatalf("re-export lost layers: %+v", report.Counts)
	}
	if out := manufacturingFiles(imported); len(out["gerbers/In2_Cu.gbr"]) == 0 {
		t.Fatal("imported four-layer board does not produce inner Gerbers")
	}
}

func TestKiCadPadDetailsRoundTrip(t *testing.T) {
	def := dhtExample(t)
	for i := range def.Components {
		if def.Components[i].ID == "u4" {
			def.Components[i].Pads[2].NonPlated = true
			def.Components[i].Pads[3].DrillNM = 0
			def.Components[i].Pads[3].DrillWidthNM, def.Components[i].Pads[3].DrillHeightNM = 600_000, 1_200_000
		}
	}
	imported, err := importKiCadPCB("Pads", renderKiCadPCB(&def))
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range imported.Components {
		if component.Designator != "U4" {
			continue
		}
		if !component.Pads[2].NonPlated || component.Pads[3].DrillWidthNM != 600_000 || component.Pads[3].DrillHeightNM != 1_200_000 {
			t.Fatalf("np_thru_hole/oval drill lost: %+v", component.Pads)
		}
		return
	}
	t.Fatal("U4 missing after import")
}

func TestManufacturingOutputsStayDeterministic(t *testing.T) {
	def := fourLayerDefinition()
	def.Board.CornerRadiusNM = 1_000_000
	a, err := zipManufacturingSet(&def)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := zipManufacturingSet(&def)
	if !bytes.Equal(a, b) {
		t.Fatal("manufacturing ZIP not deterministic")
	}
}

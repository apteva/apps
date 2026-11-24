package main

import (
	"errors"
	"math"
	"testing"
)

// syntheticKiCadFootprint is a hand-written KiCad 8 footprint exercising every
// pad form the importer supports.
const syntheticKiCadFootprint = `(footprint "Test_Mixed_Part"
	(version 20240108)
	(generator "pcbnew")
	(layer "F.Cu")
	(descr "Synthetic importer fixture")
	(fp_rect (start -3 -2) (end 3 2) (stroke (width 0.1) (type solid)) (fill none) (layer "F.Fab"))
	(fp_line (start -4 -3) (end 4 -3) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(fp_line (start 4 -3) (end 4 3.5) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(fp_line (start 4 3.5) (end -4 3.5) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(fp_line (start -4 3.5) (end -4 -3) (stroke (width 0.05) (type solid)) (layer "F.CrtYd"))
	(pad "1" smd roundrect (at -2 -1) (size 0.6 1.2) (layers "F.Cu" "F.Paste" "F.Mask") (roundrect_rratio 0.2))
	(pad "2" smd rect (at -1 -1 90) (size 0.6 1.2) (layers "F.Cu" "F.Paste" "F.Mask"))
	(pad "3" thru_hole oval (at 1 1) (size 1.2 2.4) (drill oval 0.6 1.7) (layers "*.Cu" "*.Mask"))
	(pad "4" thru_hole circle (at 2 1 90) (size 1.7 1.7) (drill oval 0.8 1.2) (layers "*.Cu" "*.Mask"))
	(pad "" np_thru_hole circle (at 0 2) (size 1.1 1.1) (drill 1.1) (layers "*.Cu" "*.Mask"))
	(pad "" smd rect (at 0 -2) (size 1 1) (layers "F.Paste"))
	(model "${KICAD8_3DMODEL_DIR}/Test.3dshapes/Test_Mixed_Part.step" (offset (xyz 0 0 0)) (scale (xyz 1 1 1)) (rotate (xyz 0 0 0)))
)`

func libraryPadByID(t *testing.T, pads []Pad, id string) Pad {
	t.Helper()
	for _, pad := range pads {
		if pad.ID == id {
			return pad
		}
	}
	t.Fatalf("pad %q missing", id)
	return Pad{}
}

func assertNM(t *testing.T, label string, got int64, wantMM float64) {
	t.Helper()
	if want := int64(math.Round(wantMM * 1e6)); got != want {
		t.Fatalf("%s = %d nm, want %d nm", label, got, want)
	}
}

func TestImportKiCadFootprintPadForms(t *testing.T) {
	result, err := importKiCadFootprint([]byte(syntheticKiCadFootprint))
	if err != nil {
		t.Fatal(err)
	}
	part := result.Part
	if part.ID != "kicad:Test_Mixed_Part" || part.Footprint.LibraryID != "Test_Mixed_Part" {
		t.Fatalf("identity id=%q library=%q", part.ID, part.Footprint.LibraryID)
	}
	if len(part.Footprint.Pads) != 5 {
		t.Fatalf("pads=%d, want 5 (paste-only aperture skipped): %#v", len(part.Footprint.Pads), part.Footprint.Pads)
	}
	p1 := libraryPadByID(t, part.Footprint.Pads, "1")
	if p1.Shape != "roundrect" || math.Abs(p1.RoundrectRatio-0.2) > 1e-9 || p1.DrillNM != 0 || len(p1.Layers) != 1 || p1.Layers[0] != "F.Cu" {
		t.Fatalf("pad 1 = %#v", p1)
	}
	assertNM(t, "pad1.x", p1.XNM, -2)
	assertNM(t, "pad1.y", p1.YNM, -1)
	p2 := libraryPadByID(t, part.Footprint.Pads, "2")
	assertNM(t, "pad2 rotated width", p2.WidthNM, 1.2)
	assertNM(t, "pad2 rotated height", p2.HeightNM, 0.6)
	p3 := libraryPadByID(t, part.Footprint.Pads, "3")
	if p3.Shape != "oval" || !padIsPlated(p3) || len(p3.Layers) != 2 {
		t.Fatalf("pad 3 = %#v", p3)
	}
	assertNM(t, "pad3 drill width", p3.DrillWidthNM, 0.6)
	assertNM(t, "pad3 drill height", p3.DrillHeightNM, 1.7)
	assertNM(t, "pad3 drill min", p3.DrillNM, 0.6)
	p4 := libraryPadByID(t, part.Footprint.Pads, "4")
	assertNM(t, "pad4 rotated drill width", p4.DrillWidthNM, 1.2)
	assertNM(t, "pad4 rotated drill height", p4.DrillHeightNM, 0.8)
	npth := libraryPadByID(t, part.Footprint.Pads, "NPTH")
	if !npth.NonPlated || npth.Plated || padIsPlated(npth) {
		t.Fatalf("NPTH must be explicitly non-plated: %#v", npth)
	}
	assertNM(t, "npth drill", npth.DrillNM, 1.1)
	minX, minY, maxX, maxY := int64(math.MaxInt64), int64(math.MaxInt64), int64(math.MinInt64), int64(math.MinInt64)
	for _, point := range part.Footprint.Courtyard {
		minX, minY, maxX, maxY = min64(minX, point.XNM), min64(minY, point.YNM), max64(maxX, point.XNM), max64(maxY, point.YNM)
	}
	assertNM(t, "courtyard minX", minX, -4)
	assertNM(t, "courtyard minY", minY, -3)
	assertNM(t, "courtyard maxX", maxX, 4)
	assertNM(t, "courtyard maxY", maxY, 3.5)
	assertNM(t, "fab body width", part.Footprint.BodyWidth, 6)
	assertNM(t, "fab body height", part.Footprint.BodyHeight, 4)
	if part.Footprint.Model3DRef != "${KICAD8_3DMODEL_DIR}/Test.3dshapes/Test_Mixed_Part.step" {
		t.Fatalf("model=%q", part.Footprint.Model3DRef)
	}
	if len(part.Provenance.SourceSHA256) != 64 || part.Footprint.Mount != "mixed" {
		t.Fatalf("provenance=%#v mount=%q", part.Provenance, part.Footprint.Mount)
	}
	if err := validateLibraryPart(part); err != nil {
		t.Fatal(err)
	}
}

func TestImportKiCadFootprintRejectsGarbage(t *testing.T) {
	for _, content := range []string{"", "(symbol \"x\")", "not an s-expression"} {
		if _, err := importKiCadFootprint([]byte(content)); err == nil {
			t.Fatalf("expected error for %q", content)
		}
	}
}

func TestBuiltinLibraryPartsAreValid(t *testing.T) {
	parts := builtinLibrary()
	if len(parts) < 20 {
		t.Fatalf("only %d built-in parts", len(parts))
	}
	seen := map[string]bool{}
	for _, part := range parts {
		if seen[part.ID] {
			t.Fatalf("duplicate part %s", part.ID)
		}
		seen[part.ID] = true
		if err := validateLibraryPart(part); err != nil {
			t.Fatalf("%s: %v", part.ID, err)
		}
		if part.Provenance.Reference == "" {
			t.Fatalf("%s: no source cited", part.ID)
		}
		def := emptyDefinition("probe")
		if _, err := instantiateLibraryPart(&def, part, LibraryPlacement{Position: Position{XNM: mm(20), YNM: mm(20)}}); err != nil {
			t.Fatalf("%s: instantiate: %v", part.ID, err)
		}
	}
	for _, id := range []string{"usb-c-gct-usb4105", "jst-sh-4-qwiic", "esp32-s3-wroom-1", "esp32-c3-mini-1", "dht22", "oled-ssd1306-0.96-i2c", "encoder-alps-ec11e-switch", "relay-srd-05vdc-sl-c", "pir-hc-sr501", "mcp73831-sot23-5", "ap2112k-3.3", "sot-23", "r-0402", "r-0603", "r-0805", "led-0603", "pogo-target-1x04-p2.54"} {
		if !seen[id] {
			t.Fatalf("built-in %s missing", id)
		}
	}
}

func TestBuiltinUSBCKeyDimensions(t *testing.T) {
	part, ok := builtinLibraryPart("usb-c-gct-usb4105")
	if !ok {
		t.Fatal("usb-c missing")
	}
	pads := part.Footprint.Pads
	a6, a7 := libraryPadByID(t, pads, "A6"), libraryPadByID(t, pads, "A7")
	assertNM(t, "A6-A7 pitch", a7.XNM-a6.XNM, 0.5)
	assertNM(t, "A6 width", a6.WidthNM, 0.3)
	assertNM(t, "A6 height", a6.HeightNM, 1.15)
	assertNM(t, "A1 width", libraryPadByID(t, pads, "A1").WidthNM, 0.6)
	npth, slots := 0, map[[2]int64]int{}
	for _, pad := range pads {
		if !padIsPlated(pad) {
			npth++
			assertNM(t, "NPTH drill", pad.DrillNM, 0.65)
			assertNM(t, "NPTH x offset", int64(math.Abs(float64(pad.XNM))), 2.89)
		}
		if pad.DrillWidthNM > 0 && padIsPlated(pad) {
			if pad.PinID != "SH" {
				t.Fatalf("slot %s not on shield pin", pad.ID)
			}
			slots[[2]int64{pad.DrillWidthNM, pad.DrillHeightNM}]++
		}
	}
	if npth != 2 {
		t.Fatalf("npth=%d", npth)
	}
	if slots[[2]int64{mm(0.6), mm(1.7)}] != 2 || slots[[2]int64{mm(0.6), mm(1.4)}] != 2 {
		t.Fatalf("shield slots=%v", slots)
	}
}

func TestBuiltinJSTSHKeyDimensions(t *testing.T) {
	part, _ := builtinLibraryPart("jst-sh-4-qwiic")
	pads := part.Footprint.Pads
	for i := 1; i <= 4; i++ {
		pad := libraryPadByID(t, pads, itoa(i))
		assertNM(t, "JST-SH pad width", pad.WidthNM, 0.6)
		assertNM(t, "JST-SH pad height", pad.HeightNM, 1.55)
		assertNM(t, "JST-SH pad x", pad.XNM, -1.5+float64(i-1))
	}
	if part.Pins[0].Name != "GND" || part.Pins[2].Name != "SDA" || part.Pins[3].Name != "SCL" {
		t.Fatalf("qwiic pin order=%#v", part.Pins)
	}
}

func TestBuiltinESP32S3KeyDimensions(t *testing.T) {
	part, _ := builtinLibraryPart("esp32-s3-wroom-1")
	pads := part.Footprint.Pads
	if len(pads) != 41 || len(part.Pins) != 41 {
		t.Fatalf("pads=%d pins=%d", len(pads), len(part.Pins))
	}
	p1, p2, p40 := libraryPadByID(t, pads, "1"), libraryPadByID(t, pads, "2"), libraryPadByID(t, pads, "40")
	assertNM(t, "pin 1 x", p1.XNM, -8.75)
	assertNM(t, "pin 40 x", p40.XNM, 8.75)
	assertNM(t, "side pitch", p2.YNM-p1.YNM, 1.27)
	assertNM(t, "side pad width", p1.WidthNM, 1.5)
	assertNM(t, "side pad height", p1.HeightNM, 0.9)
	ep := libraryPadByID(t, pads, "41")
	assertNM(t, "EP width", ep.WidthNM, 3.9)
	assertNM(t, "EP height", ep.HeightNM, 3.9)
}

func TestComponentAddFromLibraryOperation(t *testing.T) {
	def := emptyDefinition("library op")
	position := Position{XNM: mm(10), YNM: mm(10)}
	if err := applyOperation(&def, Operation{Type: "component.add_from_library", LibraryPartID: "ap2112k-3.3", Position: &position}); err != nil {
		t.Fatal(err)
	}
	back := Position{XNM: mm(20), YNM: mm(10), Side: "back"}
	if err := applyOperation(&def, Operation{Type: "component.add_from_library", LibraryPartID: "ap2112k-3.3", Position: &back}); err != nil {
		t.Fatal(err)
	}
	if len(def.Components) != 2 || def.Components[0].Designator != "U1" || def.Components[1].Designator != "U2" || def.Components[1].ID != "u2" {
		t.Fatalf("designators=%#v", def.Components)
	}
	front, rear := def.Components[0], def.Components[1]
	if front.LCSC != "C51118" || front.FootprintSpec == nil || front.FootprintSpec.LibraryPartID != "ap2112k-3.3" {
		t.Fatalf("front=%#v", front)
	}
	if rear.Pads[0].XNM != -front.Pads[0].XNM || rear.Pads[0].Layers[0] != "B.Cu" {
		t.Fatalf("back side not mirrored: %#v vs %#v", rear.Pads[0], front.Pads[0])
	}
	spec := *front.FootprintSpec
	attachNativeContracts(&def)
	if def.Components[0].FootprintSpec.SourceSHA256 != spec.SourceSHA256 || def.Components[0].FootprintSpec.Source != spec.Source {
		t.Fatalf("library contract overwritten: %#v", def.Components[0].FootprintSpec)
	}
	if err := applyOperation(&def, Operation{Type: "component.add_from_library", LibraryPartID: "does-not-exist", Position: &position}); err == nil {
		t.Fatal("unknown part accepted")
	}
}

func TestLibraryStoreProjectParts(t *testing.T) {
	store := testStore(t)
	result, err := importKiCadFootprint([]byte(syntheticKiCadFootprint))
	if err != nil {
		t.Fatal(err)
	}
	part := result.Part
	part.MPN = "TEST-MPN-1"
	if err := store.UpsertLibraryPart("p1", part, "tester"); err != nil {
		t.Fatal(err)
	}
	part.Description = "updated"
	if err := store.UpsertLibraryPart("p1", part, "tester"); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetLibraryPart("p1", part.ID)
	if err != nil || got.Description != "updated" || len(got.Footprint.Pads) != 5 {
		t.Fatalf("get=%#v err=%v", got, err)
	}
	if _, err := store.GetLibraryPart("p2", part.ID); !errors.Is(err, errLibraryPartNotFound) {
		t.Fatalf("cross-project read err=%v", err)
	}
	if builtin, err := store.GetLibraryPart("p2", "r-0402"); err != nil || builtin.ID != "r-0402" {
		t.Fatalf("builtin via store err=%v", err)
	}
	listed, err := store.ListLibraryParts("p1", "TEST-MPN", "", 0)
	if err != nil || len(listed) != 1 || listed[0].ID != part.ID {
		t.Fatalf("list=%#v err=%v", listed, err)
	}
	all, err := store.ListLibraryParts("p1", "", "", 0)
	if err != nil || len(all) != len(builtinLibrary())+1 {
		t.Fatalf("all=%d err=%v", len(all), err)
	}
	shadow, _ := builtinLibraryPart("r-0402")
	if err := store.UpsertLibraryPart("p1", shadow, "tester"); err == nil {
		t.Fatal("built-in id overwritten")
	}
	if none, err := (*Store)(nil).ListLibraryParts("", "esp32", "mcu", 0); err != nil || len(none) < 2 {
		t.Fatalf("nil-store list=%d err=%v", len(none), err)
	}
}

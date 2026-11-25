package main

import (
	"encoding/csv"
	"math"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func placeLibrary(t *testing.T, partID string, pos Position) *Definition {
	t.Helper()
	base := emptyDefinition("v0112")
	base.Board.WidthNM, base.Board.HeightNM = 60_000_000, 60_000_000
	_, def, _, err := applyOperations(&base, []Operation{{Type: "component.add_from_library", LibraryPartID: partID, Position: &pos}})
	if err != nil {
		t.Fatal(err)
	}
	return def
}

func bbox(points []Point) (minX, minY, maxX, maxY int64) {
	minX, minY, maxX, maxY = math.MaxInt64, math.MaxInt64, math.MinInt64, math.MinInt64
	for _, p := range points {
		minX, minY, maxX, maxY = min64(minX, p.XNM), min64(minY, p.YNM), max64(maxX, p.XNM), max64(maxY, p.YNM)
	}
	return
}

// A pin-1-anchored header must get its library body size, centred on the
// lead row, not a box centred on pin 1.
func TestLibraryBodyIsCentredOnThePart(t *testing.T) {
	def := placeLibrary(t, "pogo-header-1x04-p2.54", Position{XNM: 30_000_000, YNM: 30_000_000})
	c := def.Components[0]
	body := componentBody(c)
	if body.WidthNM != 2_540_000 || body.HeightNM != 10_160_000 {
		t.Fatalf("body %+v, want the library 2.54 x 10.16 mm", body)
	}
	// Board frame is +Y up: the KiCad row runs 0..7.62 mm down, so -7.62..0 here.
	if body.OffsetXNM != 0 || body.OffsetYNM != -3_810_000 {
		t.Fatalf("body centre (%d, %d), want (0, -3810000)", body.OffsetXNM, body.OffsetYNM)
	}
	minX, minY, maxX, maxY := bbox(bodyPolygon(c, body))
	for _, pad := range c.Pads {
		p := placePad(c, pad).Center
		if p.XNM < minX || p.XNM > maxX || p.YNM < minY || p.YNM > maxY {
			t.Fatalf("pad %s at %v outside body box [%d,%d]-[%d,%d]", pad.ID, p, minX, minY, maxX, maxY)
		}
	}
}

func TestInferredBodyFollowsPadExtent(t *testing.T) {
	c := Component{ID: "j1", Designator: "J1", Position: Position{XNM: 10_000_000, YNM: 10_000_000}}
	for i := 0; i < 4; i++ {
		c.Pads = append(c.Pads, Pad{ID: strconv.Itoa(i + 1), PinID: strconv.Itoa(i + 1), Shape: "circle", YNM: -int64(i) * 2_540_000, WidthNM: 1_700_000, HeightNM: 1_700_000, DrillNM: 1_000_000, Layers: []string{"F.Cu", "B.Cu"}})
	}
	body := inferredBody(c)
	// Pads span 7.62 + 1.7 = 9.32 mm; +0.5 mm margin.
	if body.HeightNM != 9_820_000 || body.WidthNM != 2_200_000 {
		t.Fatalf("inferred body %+v, want 2.2 x 9.82 mm (not doubled)", body)
	}
	if body.OffsetYNM != -3_810_000 || body.OffsetXNM != 0 {
		t.Fatalf("inferred centre (%d, %d), want (0, -3810000)", body.OffsetXNM, body.OffsetYNM)
	}
}

// Designs saved by 0.11.0/0.11.1 hold library bodies without an offset; the
// centre comes from the frozen courtyard and normalisation stamps it.
func TestLegacyLibraryBodyGetsCentreFromCourtyard(t *testing.T) {
	def := placeLibrary(t, "encoder-alps-ec11e-switch", Position{XNM: 30_000_000, YNM: 30_000_000})
	want := componentBody(def.Components[0])
	if want.OffsetXNM == 0 {
		t.Fatalf("EC11 body should sit off the origin: %+v", want)
	}
	legacy := cloneDefinition(def)
	legacy.Components[0].Body.OffsetXNM, legacy.Components[0].Body.OffsetYNM = 0, 0
	if got := componentBody(legacy.Components[0]); got != want {
		t.Fatalf("legacy body %+v, want %+v", got, want)
	}
	attachNativeContracts(legacy)
	if got := *legacy.Components[0].Body; got != want {
		t.Fatalf("normalised legacy body %+v, want %+v", got, want)
	}
}

func cplRow(t *testing.T, def *Definition, designator string) (x, y float64) {
	t.Helper()
	rows, err := csv.NewReader(strings.NewReader(string(renderJLCPlacementCSV(def)))).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows[1:] {
		if row[0] == designator {
			x, _ = strconv.ParseFloat(strings.TrimSuffix(row[1], "mm"), 64)
			y, _ = strconv.ParseFloat(strings.TrimSuffix(row[2], "mm"), 64)
			return
		}
	}
	t.Fatalf("%s not in CPL", designator)
	return
}

// JLC's CPL gives the part centre, not the footprint origin (pin 1).
func TestJLCPlacementUsesPartCentre(t *testing.T) {
	def := placeLibrary(t, "pogo-header-1x04-p2.54", Position{XNM: 30_000_000, YNM: 30_000_000})
	ref := def.Components[0].Designator
	if x, y := cplRow(t, def, ref); math.Abs(x-30) > 1e-6 || math.Abs(y-26.19) > 1e-6 {
		t.Fatalf("CPL %s at (%v, %v), want (30, 26.19)", ref, x, y)
	}
	rotated := placeLibrary(t, "pogo-header-1x04-p2.54", Position{XNM: 30_000_000, YNM: 30_000_000, RotationUdeg: 90_000_000})
	if x, y := cplRow(t, rotated, ref); math.Abs(x-33.81) > 1e-6 || math.Abs(y-30) > 1e-6 {
		t.Fatalf("rotated CPL %s at (%v, %v), want (33.81, 30)", ref, x, y)
	}
}

func TestSHTC3TapeAndReelIsAccepted(t *testing.T) {
	def := placeLibrary(t, "shtc3", Position{XNM: 30_000_000, YNM: 30_000_000})
	c := def.Components[0]
	if c.MPN != "SHTC3-TR-2.5KS" || c.LCSC != "C194656" {
		t.Fatalf("shtc3 placed as %s / %s", c.MPN, c.LCSC)
	}
	for _, mpn := range []string{"SHTC3-TR-10KS", "SHTC3-DIS-B2.5KS"} {
		def.Components[0].MPN = mpn
		if hasCheck(validateDefinition(def), "FOOTPRINT_MPN_MISMATCH") {
			t.Fatalf("%s rejected", mpn)
		}
	}
	// A 0.11.1 placement froze only the -DIS number; the JLC reel must pass.
	def.Components[0].MPN = "SHTC3-TR-2.5KS"
	def.Components[0].FootprintSpec.CompatibleMPNs = []string{"SHTC3-DIS-B2.5KS"}
	if hasCheck(validateDefinition(def), "FOOTPRINT_MPN_MISMATCH") {
		t.Fatal("legacy contract rejects SHTC3-TR-2.5KS")
	}
	def.Components[0].MPN = "SHT40-AD1B-R2"
	if !hasCheck(validateDefinition(def), "FOOTPRINT_MPN_MISMATCH") {
		t.Fatal("unrelated MPN accepted")
	}
}

func TestBuiltinLCSCNumbersAreWellFormed(t *testing.T) {
	for id, lcsc := range builtinLCSC {
		part, ok := builtinLibraryPart(id)
		if !ok {
			t.Fatalf("builtinLCSC names unknown part %s", id)
		}
		if part.LCSC != lcsc || !strings.HasPrefix(lcsc, "C") {
			t.Fatalf("%s LCSC %q", id, part.LCSC)
		}
	}
}

// When the exact MPN is not stocked, a footprint-compatible variant is matched.
func TestLCSCMatchTriesCompatibleMPNs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch strings.ToUpper(r.URL.Query().Get("search")) {
		case "SHTC3-TR-2.5KS":
			_, _ = w.Write([]byte(`{"components":[{"lcsc":194656,"mfr":"SHTC3-TR-2.5KS","package":"DFN-4-EP(2x2)","stock":8128,"price":"1-:0.9"}]}`))
		case "SHTC3-TR-10KS":
			_, _ = w.Write([]byte(`{"components":[{"lcsc":2880044,"mfr":"SHTC3-TR-10KS","package":"DFN-4-EP(2x2)","stock":11668,"price":"1-:0.9"}]}`))
		default:
			_, _ = w.Write([]byte(`{"components":[]}`))
		}
	}))
	defer srv.Close()
	catalog, _ := newLCSCCatalog(srv.URL)
	def := placeLibrary(t, "shtc3", Position{XNM: 30_000_000, YNM: 30_000_000})
	def.Components[0].MPN, def.Components[0].LCSC = "SHTC3-DIS-B2.5KS", ""
	matches := lcscMatchBOM(catalog, def, 5, false)
	if len(matches) != 1 || matches[0].Status != "matched" || matches[0].Basis != "compatible_mpn" || matches[0].Match.LCSC != "C2880044" {
		t.Fatalf("compatible match %+v", matches)
	}
}

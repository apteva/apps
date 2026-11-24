package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func smallPanelBoard() Definition {
	def := manufacturablePairDefinition() // 30 x 20 mm
	def.Board.CornerRadiusNM = 2_000_000
	def.Mechanical = &MechanicalSpec{Schema: mechanicalSchema, Datum: defaultMechanicalDatum(), MountingHoles: []MechanicalHole{{ID: "mh1", XNM: 3_000_000, YNM: 17_000_000, DiameterNM: 2_200_000}}}
	return def
}

func TestPanelOptionsValidation(t *testing.T) {
	for _, bad := range []PanelOptions{
		{Rows: 0, Columns: 2},
		{Rows: 21, Columns: 1},
		{Rows: 2, Columns: 2, Separation: "laser"},
		{Rows: 2, Columns: 2, RailSides: "diagonal"},
		{Rows: 2, Columns: 2, RailWidthNM: 1_000_000},
		{Rows: 2, Columns: 2, SpacingNM: -1},
		{Rows: 2, Columns: 2, Separation: "tabs", SpacingNM: 500_000},
		{Rows: 2, Columns: 2, TabWidthNM: 2_000_000},
	} {
		options := bad
		if err := options.normalize(); err == nil {
			t.Errorf("options %+v accepted", bad)
		}
	}
	options := PanelOptions{Rows: 2, Columns: 3, Separation: "tabs"}
	if err := options.normalize(); err != nil || options.RailWidthNM != 5_000_000 || options.SpacingNM != 2_000_000 || options.TabWidthNM != 3_000_000 || options.RailSides != "top_bottom" {
		t.Fatalf("defaults %+v %v", options, err)
	}
}

func TestPanelizeVScore(t *testing.T) {
	def := smallPanelBoard()
	files, report, err := panelizeFiles(&def, PanelOptions{Rows: 2, Columns: 3})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verification.Status != "passed" {
		t.Fatalf("panel verification: %+v", report.Verification.Checks)
	}
	// 3 x 30 mm wide, 2 x 20 mm + two 5 mm rails high.
	if report.WidthNM != 90_000_000 || report.HeightNM != 50_000_000 || len(report.Boards) != 6 {
		t.Fatalf("panel %d x %d with %d boards", report.WidthNM, report.HeightNM, len(report.Boards))
	}
	if len(report.Fiducials) != 3 || len(report.ToolingHoles) != 4 {
		t.Fatalf("fiducials=%d tooling=%d", len(report.Fiducials), len(report.ToolingHoles))
	}
	// V-scores: x = 30, 60 and y = 5, 25, 45.
	if len(report.VScores) != 5 {
		t.Fatalf("v-scores %+v", report.VScores)
	}
	vscore := string(files[panelVScoreLinePath])
	if !strings.Contains(vscore, "%TF.FileFunction,Other,V-Score*%") || !strings.Contains(vscore, "X30000000Y0D02*") || !strings.Contains(vscore, "X30000000Y50000000D01*") {
		t.Fatalf("V-score layer:\n%s", vscore)
	}
	if fabFunctions(t, files)[panelVScoreLinePath] != "Other,V-Score|Positive" {
		t.Fatal("V-score layer missing from gbrjob")
	}
	npth := string(files[npthDrillPath])
	// 4 tooling holes (2.0 mm) + 6 board mounting holes (2.2 mm).
	if strings.Count(npth, "T01C2.000000") != 1 || strings.Count(npth, "\nX") != 10 {
		t.Fatalf("NPTH:\n%s", npth)
	}
	mask := string(files["gerbers/F_Mask.gbr"])
	if !strings.Contains(mask, "C,2.000000*%") {
		t.Fatal("fiducial 2 mm mask opening missing")
	}
	if strings.Contains(string(files["gerbers/F_Paste.gbr"]), "X10000000Y2500000D03*") {
		t.Fatal("fiducial got solder paste")
	}
	if !strings.Contains(string(files["gerbers/F_Cu.gbr"]), "X10000000Y2500000D03*") {
		t.Fatal("fiducial copper missing")
	}
	if len(report.Warnings) == 0 {
		t.Fatal("rounded corners on a V-score panel should warn")
	}
	if _, ok := files["panel.json"]; !ok {
		t.Fatal("panel.json missing")
	}
	again, _, _ := panelizeFiles(&def, PanelOptions{Rows: 2, Columns: 3})
	a, _ := deterministicZip(files)
	b, _ := deterministicZip(again)
	if !bytes.Equal(a, b) {
		t.Fatal("panel output not deterministic")
	}
}

func TestPanelizeTabsWithMouseBites(t *testing.T) {
	def := smallPanelBoard()
	files, report, err := panelizeFiles(&def, PanelOptions{Rows: 2, Columns: 2, Separation: "tabs"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verification.Status != "passed" {
		t.Fatalf("panel verification: %+v", report.Verification.Checks)
	}
	// 2 x 30 + 2 mm gap wide; 2 x 20 + 2 + 2 x (5 + 2) high.
	if report.WidthNM != 62_000_000 || report.HeightNM != 56_000_000 {
		t.Fatalf("panel size %d x %d", report.WidthNM, report.HeightNM)
	}
	// Per board: one tab to its column neighbour (2 boards per row x 1 gap
	// = 2 tabs), each board edge to rail or row neighbour has 2 tabs (30 mm).
	if len(report.Tabs) != 2+4*2+2*2 || report.MouseBites == 0 {
		t.Fatalf("tabs=%d bites=%d", len(report.Tabs), report.MouseBites)
	}
	edge := string(files["gerbers/Edge_Cuts.gbr"])
	// Board rounded corners survive as G03 arcs: 4 per board.
	if got := strings.Count(edge, "I0J2000000D01*") + strings.Count(edge, "I0J-2000000D01*") + strings.Count(edge, "I2000000J0D01*") + strings.Count(edge, "I-2000000J0D01*"); got != 16 {
		t.Fatalf("board corner arcs=%d\n%s", got, edge)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("warnings %v", report.Warnings)
	}
	npth := string(files[npthDrillPath])
	if !strings.Contains(npth, "C0.500000") {
		t.Fatal("mouse-bite perforations missing from NPTH")
	}
	if metrics := fileMetrics(report.Verification.Files, npthDrillPath); metrics.Holes != 4+4+report.MouseBites {
		t.Fatalf("NPTH holes=%d", metrics.Holes)
	}
}

func TestPanelizeTabsKeepsExplicitOutlineCorners(t *testing.T) {
	def := dhtExample(t)
	files, report, err := panelizeFiles(&def, PanelOptions{Rows: 1, Columns: 2, Separation: "tabs", RailSides: "all"})
	if err != nil {
		t.Fatal(err)
	}
	if report.Verification.Status != "passed" || len(report.Warnings) != 0 {
		t.Fatalf("verification %+v warnings %v", report.Verification.Checks, report.Warnings)
	}
	// The first board sits at (7, 7): its chamfer vertex (0.88, 0.88) moves
	// to (7.88, 7.88).
	if !strings.Contains(string(files["gerbers/Edge_Cuts.gbr"]), "X7880000Y7880000D01*") {
		t.Fatal("explicit outline corner chain missing from tab routing")
	}
	if len(report.Fiducials) != 3 || len(report.ToolingHoles) != 4 {
		t.Fatal("rails must carry fiducials and tooling holes")
	}
}

func TestPanelizeServiceStoresArtifactWithoutNewRevision(t *testing.T) {
	store := testStore(t)
	def := dhtExample(t)
	canonical, _, hash, err := normalizeDefinition(mustJSON(def), def.Name)
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDesign("p", def.Name, canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	s := &Service{store: store, project: "p", artifactRoot: t.TempDir()}
	artifact, report, err := s.Panelize(d.ID, 0, PanelOptions{Rows: 2, Columns: 2, Separation: "vscore"})
	if err != nil {
		t.Fatal(err)
	}
	if artifact.Kind != "panel" || report.Verification.Status != "passed" {
		t.Fatalf("artifact %+v status %s", artifact, report.Verification.Status)
	}
	after, err := store.GetDesign("p", d.ID)
	if err != nil || after.CurrentRevisionID != d.CurrentRevisionID {
		t.Fatalf("panelization changed the revision: %v %+v", err, after)
	}
	body, _ := os.ReadFile(artifact.LocalPath)
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	for _, f := range zr.File {
		names[f.Name] = true
	}
	for _, want := range []string{"board.gbrjob", pthDrillPath, npthDrillPath, "gerbers/F_Cu.gbr", "gerbers/B_Cu.gbr", "gerbers/F_Mask.gbr", "gerbers/B_Mask.gbr", "gerbers/F_Paste.gbr", "gerbers/Edge_Cuts.gbr", panelVScoreLinePath, "panel.json"} {
		if !names[want] {
			t.Fatalf("panel zip lacks %s: %v", want, names)
		}
	}
	var meta map[string]any
	if err := json.Unmarshal(artifact.Metadata, &meta); err != nil || meta["fabrication_verification_status"] != "passed" {
		t.Fatalf("metadata %s", artifact.Metadata)
	}
	if _, _, err := s.Panelize(d.ID, 0, PanelOptions{Rows: 0, Columns: 2}); err == nil {
		t.Fatal("invalid options accepted")
	}
}

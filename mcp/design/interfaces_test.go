package main

import (
	"context"
	"strings"
	"testing"
)

// tileDefinition is a 20 mm cube with a magnet on its west and east faces.
func tileDefinition(west, east string, extraEast string) string {
	return `{"schema":"apteva-design/v1","units":"mm",
		"parameters":{"size":{"type":"length","default":20,"min":10,"max":40}},
		"operations":[{"id":"tile","type":"box","size":["size","size","size/2"]}],"output":"tile",
		"parts":[{"id":"tile","name":"Tile","output":"tile"}],
		"assembly":{"name":"tile","instances":[{"id":"tile_1","part_id":"tile","transform":{"translate":[100,0,0]}}],
		"interfaces":[
			{"id":"west","kind":"magnet","part_id":"tile","position":[0,"size/2","size/4"],"direction":[-1,0,0],"up":[0,0,1],"parameters":{"polarity":"` + west + `","diameter_mm":6}},
			{"id":"east","kind":"magnet","part_id":"tile","position":["size","size/2","size/4"],"direction":[1,0,0],"up":[0,0,1],"parameters":{"polarity":"` + east + `","diameter_mm":6}}` + extraEast + `]}}`
}

type linkFixture struct {
	t *testing.T
	s *Service
}

func (f linkFixture) source(design *Design, extra string) string {
	revision, err := f.s.store.GetRevision(f.s.project, design.CurrentRevisionID)
	if err != nil {
		f.t.Fatal(err)
	}
	return `{"design_id":` + itoa(int(design.ID)) + `,"revision_id":` + itoa(int(revision.ID)) + `,"source_sha256":"` + revision.SourceSHA256 + `"` + extra + `}`
}

func (f linkFixture) pair(name string, a, b *Design, aExtra, bExtra, mateA, mateB string) *BuildResult {
	f.t.Helper()
	definition := `{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"marker","type":"box","size":[1,1,1]}],"output":"marker",
		"parts":[{"id":"a","name":"A","source":` + f.source(a, aExtra) + `},{"id":"b","name":"B","source":` + f.source(b, bExtra) + `}],
		"assembly":{"name":"pair","instances":[{"id":"a_1","part_id":"a"},{"id":"b_1","part_id":"b","mate":{"interface":"` + mateB + `","to_instance":"a_1","to_interface":"` + mateA + `"}}]}}`
	design := createDesign(f.t, f.s, name, definition, `{}`)
	result, err := f.s.Build(context.Background(), design.ID, 0, nil)
	if err != nil {
		f.t.Fatalf("%s: %v", name, err)
	}
	return result
}

func optionalCheck(result *BuildResult, kind string) *CheckResult {
	for i := range result.Checks {
		if result.Checks[i].Type == kind {
			return &result.Checks[i]
		}
	}
	return nil
}

func TestInheritedInterfacesAndPolarity(t *testing.T) {
	s := inspectionService(t)
	f := linkFixture{t, s}
	ns := createDesign(t, s, "NS tile", tileDefinition("N", "S", ""), `{}`)
	nn := createDesign(t, s, "NN tile", tileDefinition("N", "N", ""), `{}`)

	// Part links keep the source part's frame: b's west face lands on a's east face.
	good := f.pair("attract", ns, ns, `,"part_id":"tile"`, `,"part_id":"tile"`, "a/east", "b/west")
	boundsNear(t, "b mated to a's east face", instanceBounds(t, good.Report, "b_1"), [3]float64{20, 0, 0}, [3]float64{40, 20, 10})
	if c := optionalCheck(good, "mate_compatibility"); c == nil || c.Status != "pass" || !strings.Contains(c.Message, "1 facing magnet pair") {
		t.Fatalf("N facing S should pass: %#v", c)
	}
	if good.Report.ComponentBuilds == nil || good.Report.ComponentBuilds.Built != 1 || good.Report.ComponentBuilds.Reused != 1 {
		t.Fatalf("the same source with the same parameters should build once: %#v", good.Report.ComponentBuilds)
	}
	// Different link parameters are a different build.
	sized := f.pair("sized", ns, ns, `,"part_id":"tile"`, `,"part_id":"tile","parameters":{"size":30}`, "a/east", "b/west")
	if sized.Report.ComponentBuilds.Built != 2 {
		t.Fatalf("different parameters need their own build: %#v", sized.Report.ComponentBuilds)
	}
	boundsNear(t, "inherited interface uses the link's parameters", instanceBounds(t, sized.Report, "b_1"), [3]float64{20, -5, -2.5}, [3]float64{50, 25, 12.5})

	bad := f.pair("repel", nn, ns, `,"part_id":"tile"`, `,"part_id":"tile"`, "a/east", "b/west")
	if c := optionalCheck(bad, "mate_compatibility"); c == nil || c.Status != "fail" || !strings.Contains(c.Message, "repel") {
		t.Fatalf("N facing N must fail: %#v", c)
	}
	// Whole-design link: the interface follows the source's placement (+100 x).
	whole := f.pair("whole", ns, ns, ``, ``, "a/east", "b/west")
	boundsNear(t, "whole-design link carries placed interfaces", instanceBounds(t, whole.Report, "b_1"), [3]float64{120, 0, 0}, [3]float64{140, 20, 10})

	// A second magnet on the east face with no partner is reported.
	extra := `,{"id":"east2","kind":"magnet","part_id":"tile","position":["size","size/2","size/4+3"],"direction":[1,0,0],"up":[0,0,1],"parameters":{"polarity":"S","diameter_mm":6}}`
	two := createDesign(t, s, "two magnets", tileDefinition("N", "S", extra), `{}`)
	lonely := f.pair("lonely", two, ns, `,"part_id":"tile"`, `,"part_id":"tile"`, "a/east", "b/west")
	if c := optionalCheck(lonely, "mate_compatibility"); c == nil || c.Status != "warning" || !strings.Contains(c.Message, "a_1/a/east2 is on a mated face") {
		t.Fatalf("an unpaired magnet on a mated face should warn: %#v", c)
	}

	// Inherited names only work on linked parts, and must name a source interface.
	if _, _, err := normalizeDefinition([]byte(`{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"x","type":"box","size":[1,1,1]}],"output":"x",
		"parts":[{"id":"a","name":"A","output":"x"},{"id":"b","name":"B","output":"x"}],
		"assembly":{"name":"p","instances":[{"id":"a_1","part_id":"a"},{"id":"b_1","part_id":"b","mate":{"interface":"b/west","to_instance":"a_1","to_interface":"a/east"}}]}}`), 256); err == nil || !strings.Contains(err.Error(), "is not an interface of part") {
		t.Fatalf("unlinked parts have no inherited interfaces: %v", err)
	}
	missing := createDesign(t, s, "missing", `{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"marker","type":"box","size":[1,1,1]}],"output":"marker",
		"parts":[{"id":"a","name":"A","source":`+f.source(ns, `,"part_id":"tile"`)+`},{"id":"b","name":"B","source":`+f.source(ns, `,"part_id":"tile"`)+`}],
		"assembly":{"name":"pair","instances":[{"id":"a_1","part_id":"a"},{"id":"b_1","part_id":"b","mate":{"interface":"b/north","to_instance":"a_1","to_interface":"a/east"}}]}}`, `{}`)
	if _, err := s.Build(context.Background(), missing.ID, 0, nil); err == nil || !strings.Contains(err.Error(), "unknown interface b/north") {
		t.Fatalf("a missing source interface should fail the build: %v", err)
	}
}

func TestMatesWithAndMeshWarning(t *testing.T) {
	s := inspectionService(t)
	design := createDesign(t, s, "mates_with", `{"schema":"apteva-design/v1","units":"mm",
		"operations":[{"id":"a","type":"box","size":[10,10,10]},{"id":"b","type":"box","size":[10,10,10]}],"output":"a",
		"parts":[{"id":"a","name":"A","output":"a"},{"id":"b","name":"B","output":"b"}],
		"assembly":{"name":"p","instances":[{"id":"a_1","part_id":"a"},{"id":"b_1","part_id":"b","mate":{"interface":"plug","to_instance":"a_1","to_interface":"socket"}}],
		"interfaces":[{"id":"socket","kind":"usb_c_receptacle","part_id":"a","position":[10,5,5],"direction":[1,0,0],"parameters":{"mates_with":["usb_c_plug"]}},
			{"id":"plug","kind":"pogo","part_id":"b","position":[0,5,5],"direction":[-1,0,0]}]}}`, `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := optionalCheck(result, "mate_compatibility"); c == nil || c.Status != "fail" || !strings.Contains(c.Message, "mates_with usb_c_plug") {
		t.Fatalf("mates_with should reject a pogo plug: %#v", c)
	}

	asset, err := s.importSTLAsset("cube.stl", binarySTLCube(10))
	if err != nil {
		t.Fatal(err)
	}
	cradle := createDesign(t, s, "cradle", `{"schema":"apteva-design/v1","units":"mm","operations":[
		{"id":"scan","type":"import_stl","asset_id":"`+asset.ID+`"},{"id":"moved","type":"translate","input":"scan","vector":[5,5,5]},
		{"id":"block","type":"box","size":[20,20,10]},{"id":"pocket","type":"cut","base":"block","tools":["moved"]}],"output":"pocket"}`, `{}`)
	result, err = s.Build(context.Background(), cradle.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := optionalCheck(result, "mesh_derived"); c == nil || c.Status != "warning" || !strings.Contains(c.Message, "pocket") || strings.Contains(c.Message, "moved") {
		t.Fatalf("cutting with a mesh should warn about the cut only: %#v", c)
	}
	near(t, "the mesh still cuts", result.Report.VolumeMM3, 20*20*10-10*10*5, 1e-3)
}

func TestMouldingReportsSampling(t *testing.T) {
	s := inspectionService(t)
	design := createDesign(t, s, "sampled", `{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"s","type":"sphere","radius":10}],"output":"s",
		"checks":[{"id":"mould","type":"moulding","min_draft_deg":0,"undercut_samples":16}]}`, `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c := checkID(t, result, "mould"); !strings.Contains(c.Message, "sampled on") {
		t.Fatalf("a sampled undercut search must say so: %#v", c)
	}
}

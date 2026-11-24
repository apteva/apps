package main

import (
	"context"
	"math"
	"strings"
	"testing"
)

func featureReport(t *testing.T, s *Service, name, operations, output, parameters string) GeometryReport {
	t.Helper()
	design := createDesign(t, s, name, `{"schema":"apteva-design/v1","units":"mm"`+parameters+`,"operations":[`+operations+`],"output":"`+output+`"}`, `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, nil)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return result.Report
}

func featureError(t *testing.T, s *Service, name, operations, output string) error {
	t.Helper()
	design := createDesign(t, s, name, `{"schema":"apteva-design/v1","units":"mm","operations":[`+operations+`],"output":"`+output+`"}`, `{}`)
	_, err := s.Build(context.Background(), design.ID, 0, nil)
	return err
}

func near(t *testing.T, label string, got, want, tolerance float64) {
	t.Helper()
	if math.Abs(got-want) > tolerance {
		t.Fatalf("%s: got %.3f want %.3f (±%.3f)", label, got, want, tolerance)
	}
}

const block = `{"id":"block","type":"box","size":[20,20,10]}`

func TestEdgeSelectors(t *testing.T) {
	s := inspectionService(t)
	all := featureReport(t, s, "all", block+`,{"id":"f","type":"fillet","input":"block","radius":2}`, "f", "")
	top := featureReport(t, s, "top", block+`,{"id":"f","type":"fillet","input":"block","radius":2,"edges":{"side":"top"}}`, "f", "")
	vertical := featureReport(t, s, "vertical", block+`,{"id":"f","type":"fillet","input":"block","radius":2,"edges":{"direction":"Z"}}`, "f", "")
	// Four vertical edges rounded: each removes (4-pi) r^2 per mm of height.
	near(t, "vertical fillet", vertical.VolumeMM3, 4000-4*(4-math.Pi)*10, 0.5)
	if !(all.VolumeMM3 < top.VolumeMM3 && top.VolumeMM3 < 4000) {
		t.Fatalf("all %v < top %v < 4000 expected", all.VolumeMM3, top.VolumeMM3)
	}
	// A list is a union; not excludes.
	union := featureReport(t, s, "union", block+`,{"id":"f","type":"chamfer","input":"block","distance":1,"edges":[{"side":"top"},{"side":"bottom"}]}`, "f", "")
	topBottom := featureReport(t, s, "topnot", block+`,{"id":"f","type":"chamfer","input":"block","distance":1,"edges":{"not":{"direction":"Z"}}}`, "f", "")
	near(t, "union equals not-vertical", union.VolumeMM3, topBottom.VolumeMM3, 1e-3)
	boxed := featureReport(t, s, "boxed", block+`,{"id":"f","type":"chamfer","input":"block","distance":1,"edges":{"direction":"Z","in_box":[[-1,-1,-1],[1,1,11]]}}`, "f", "")
	near(t, "one chamfered corner edge", boxed.VolumeMM3, 4000-0.5*10, 1e-3)
	if err := featureError(t, s, "none", block+`,{"id":"f","type":"fillet","input":"block","radius":1,"edges":{"curve":"circle"}}`, "f"); err == nil || !strings.Contains(err.Error(), "selected no edges") {
		t.Fatalf("empty selection must fail clearly: %v", err)
	}
	if err := featureError(t, s, "badkey", block+`,{"id":"f","type":"fillet","input":"block","radius":1,"edges":{"colour":"red"}}`, "f"); err == nil || !strings.Contains(err.Error(), "not a edge selector") {
		t.Fatalf("unknown selector key must fail: %v", err)
	}
}

func TestShellHolesProfilesLoft(t *testing.T) {
	s := inspectionService(t)
	shell := featureReport(t, s, "shell", block+`,{"id":"s","type":"shell","input":"block","thickness":2,"open_faces":{"side":"top"}}`, "s", "")
	near(t, "open-top shell", shell.VolumeMM3, 4000-16*16*8, 0.5)
	near(t, "shell keeps outside", shell.Bounds.Max[0]-shell.Bounds.Min[0], 20, 1e-3)

	through := featureReport(t, s, "through", block+`,{"id":"h","type":"hole","input":"block","position":[10,10,10],"diameter":4}`, "h", "")
	near(t, "through hole", through.VolumeMM3, 4000-math.Pi*4*10, 0.5)
	side := featureReport(t, s, "side", block+`,{"id":"h","type":"hole","input":"block","position":[0,10,5],"direction":[1,0,0],"diameter":4}`, "h", "")
	near(t, "side hole", side.VolumeMM3, 4000-math.Pi*4*20, 0.5)
	insert := featureReport(t, s, "insert", block+`,{"id":"h","type":"hole","input":"block","positions":[[5,5,10],[15,15,10]],"thread":{"size":"M3","mode":"insert"}}`, "h", "")
	near(t, "two M3 insert holes", insert.VolumeMM3, 4000-2*math.Pi*4*6.7, 0.5)
	cbore := featureReport(t, s, "cbore", block+`,{"id":"h","type":"hole","input":"block","position":[10,10,10],"thread":{"size":"M3"},"counterbore":{"diameter":6,"depth":3}}`, "h", "")
	near(t, "counterbore", cbore.VolumeMM3, 4000-math.Pi*9*3-math.Pi*1.7*1.7*7, 0.5)
	csink := featureReport(t, s, "csink", block+`,{"id":"h","type":"hole","input":"block","position":[10,10,10],"diameter":3,"countersink":{"diameter":6,"angle":90}}`, "h", "")
	// 90° sink: cone frustum from r=3 to r=1.5 over 1.5 mm, then the bore.
	cone := math.Pi * 1.5 / 3 * (9 + 3*1.5 + 1.5*1.5)
	near(t, "countersink", csink.VolumeMM3, 4000-cone-math.Pi*2.25*8.5, 0.5)
	if err := featureError(t, s, "badthread", block+`,{"id":"h","type":"hole","input":"block","position":[10,10,10],"thread":{"size":"M7"}}`, "h"); err == nil || !strings.Contains(err.Error(), "thread.size") {
		t.Fatalf("unknown thread must fail: %v", err)
	}

	rounded := featureReport(t, s, "rounded", `{"id":"p","type":"extrude_profile","profile":{"kind":"rounded_rectangle","width":20,"height":10,"radius":2},"height":5}`, "p", "")
	near(t, "rounded rectangle", rounded.VolumeMM3, (200-(4-math.Pi)*4)*5, 0.05)
	slot := featureReport(t, s, "slot", `{"id":"p","type":"extrude_profile","profile":{"kind":"slot","length":20,"width":6,"angle":30,"center":[5,5]},"height":2}`, "p", "")
	near(t, "slot", slot.VolumeMM3, (14*6+math.Pi*9)*2, 0.05)
	arc := featureReport(t, s, "arc", `{"id":"p","type":"extrude_profile","profile":{"kind":"path","start":[0,0],"segments":[{"line":[20,0]},{"arc":[0,0],"via":[10,10]}]},"height":1}`, "p", "")
	near(t, "half disc from a three-point arc", arc.VolumeMM3, math.Pi*100/2, 0.05)
	plate := featureReport(t, s, "plate", `{"id":"p","type":"extrude_profile","profile":{"kind":"rectangle","width":30,"height":20,"holes":[{"kind":"circle","diameter":4,"center":[-10,0]},{"kind":"slot","length":10,"width":4,"center":[5,0]}]},"height":2}`, "p", "")
	near(t, "plate with holes", plate.VolumeMM3, (600-math.Pi*4-(6*4+math.Pi*4))*2, 0.05)

	loft := featureReport(t, s, "loft", `{"id":"l","type":"loft","sections":[{"profile":{"kind":"rectangle","width":10,"height":10},"offset":0},{"profile":{"kind":"circle","radius":3},"offset":10}]}`, "l", "")
	near(t, "loft height", loft.Bounds.Max[2]-loft.Bounds.Min[2], 10, 1e-3)
	if !(loft.VolumeMM3 > math.Pi*9*10 && loft.VolumeMM3 < 1000) {
		t.Fatalf("loft volume %v out of range", loft.VolumeMM3)
	}
	ruled := featureReport(t, s, "ruled", `{"id":"l","type":"loft","ruled":true,"sections":[{"profile":{"kind":"rectangle","width":10,"height":10}},{"profile":{"kind":"rectangle","width":6,"height":6},"offset":6}]}`, "l", "")
	near(t, "ruled square frustum", ruled.VolumeMM3, 6.0/3*(100+36+60), 0.05)
}

// Acceptance: a Pocket-style handheld shell built only from Phase 2
// features: rounded body, softened top edges, open-bottom shell, a USB-C
// slot through the end wall, a countersunk lanyard hole, and heat-set
// insert holes drilled up into corner bosses.
func TestPocketStyleShell(t *testing.T) {
	s := inspectionService(t)
	params := `,"parameters":{"length":{"type":"length","default":70},"width":{"type":"length","default":40},"height":{"type":"length","default":14},"wall":{"type":"length","default":2,"min":1.2,"max":4},"corner":{"type":"length","default":8}}`
	operations := `
		{"id":"body","type":"extrude_profile","profile":{"kind":"rounded_rectangle","width":"length","height":"width","radius":"corner","center":["length/2","width/2"]},"height":"height"},
		{"id":"soft","type":"fillet","input":"body","radius":3,"edges":{"side":"top"}},
		{"id":"hollow","type":"shell","input":"soft","thickness":"wall","open_faces":{"side":"bottom"}},
		{"id":"bosses","type":"extrude_profile","profile":{"kind":"circle","diameter":7,"center":["corner","corner"]},"height":"height-wall"},
		{"id":"bosses_x","type":"linear_pattern","input":"bosses","count":2,"step":["length-2*corner",0,0]},
		{"id":"bosses_all","type":"linear_pattern","input":"bosses_x","count":2,"step":[0,"width-2*corner",0]},
		{"id":"with_bosses","type":"fuse","inputs":["hollow","bosses_all"]},
		{"id":"usb","type":"extrude_profile","plane":"YZ","origin":["length-wall-1",0,0],"profile":{"kind":"slot","length":9.5,"width":3.5,"center":["width/2","height/2"]},"height":"wall+2"},
		{"id":"ported","type":"cut","base":"with_bosses","tools":["usb"]},
		{"id":"lanyard","type":"hole","input":"ported","position":[6,"width/2","height"],"diameter":3,"countersink":{"diameter":5.5}},
		{"id":"inserts","type":"hole","input":"lanyard","direction":[0,0,1],"thread":{"size":"M2","mode":"insert"},"positions":[["corner","corner",0],["length-corner","corner",0],["corner","width-corner",0],["length-corner","width-corner",0]]}`
	report := featureReport(t, s, "Pocket shell", operations, "inserts", params)
	if !report.Valid || report.BodyCount != 1 {
		t.Fatalf("pocket shell must be one valid solid: valid=%v bodies=%d", report.Valid, report.BodyCount)
	}
	near(t, "length", report.Bounds.Max[0]-report.Bounds.Min[0], 70, 1e-3)
	near(t, "height", report.Bounds.Max[2]-report.Bounds.Min[2], 14, 1e-3)
	solid := 70*40*14 - (4-math.Pi)*64*14
	if !(report.VolumeMM3 > 0.15*solid && report.VolumeMM3 < 0.45*solid) {
		t.Fatalf("shell volume %v should be a thin-walled fraction of %v", report.VolumeMM3, solid)
	}
}

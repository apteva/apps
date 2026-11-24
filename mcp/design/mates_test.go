package main

import (
	"context"
	"math"
	"strings"
	"testing"
)

const mateParts = `"parts":[
	{"id":"base","name":"Base","output":"base_box"},
	{"id":"lid","name":"Lid","output":"lid_box"},
	{"id":"key","name":"Key","output":"key_box"},
	{"id":"wall","name":"Wall","output":"wall_box"}],
"operations":[
	{"id":"base_box","type":"box","size":[40,40,10]},
	{"id":"lid_box","type":"box","size":[40,40,2]},
	{"id":"key_box","type":"box","size":[6,4,3]},
	{"id":"wall_box","type":"box","size":[40,4,60],"origin":[0,50,0]}],
"output":"base_box"`

const mateInterfaces = `"interfaces":[
	{"id":"base_top","kind":"face","part_id":"base","position":[20,20,10],"direction":[0,0,1],"up":[1,0,0]},
	{"id":"lid_under","kind":"face","part_id":"lid","position":[20,20,0],"direction":[0,0,-1],"up":[1,0,0]},
	{"id":"lid_top","kind":"dovetail_out","part_id":"lid","position":[30,20,2],"direction":[0,0,1],"up":[1,0,0]},
	{"id":"key_bottom","kind":"dovetail_in","part_id":"key","position":[3,2,0],"direction":[0,0,-1],"up":[0,1,0]}]`

func mateDefinition(instances, joints, checks string) string {
	return `{"schema":"apteva-design/v1","units":"mm",` + mateParts + `,"assembly":{"name":"mates",` + mateInterfaces + `,"instances":[` + instances + `],"joints":[` + joints + `]},"checks":[` + checks + `]}`
}

func instanceBounds(t *testing.T, report GeometryReport, id string) Bounds {
	t.Helper()
	for _, part := range report.Parts {
		if part.InstanceID == id {
			return part.Bounds
		}
	}
	t.Fatalf("instance %s missing from report", id)
	return Bounds{}
}

func boundsNear(t *testing.T, label string, got Bounds, min, max [3]float64) {
	t.Helper()
	for k := 0; k < 3; k++ {
		if math.Abs(got.Min[k]-min[k]) > 1e-3 || math.Abs(got.Max[k]-max[k]) > 1e-3 {
			t.Fatalf("%s: bounds %v..%v want %v..%v", label, got.Min, got.Max, min, max)
		}
	}
}

func TestMatePlacement(t *testing.T) {
	s := inspectionService(t)
	build := func(name, instances string) *BuildResult {
		t.Helper()
		design := createDesign(t, s, name, mateDefinition(instances, "", ""), `{}`)
		result, err := s.Build(context.Background(), design.ID, 0, nil)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		return result
	}
	r := build("stacked", `{"id":"b","part_id":"base","transform":{"translate":[100,0,0]}},
		{"id":"l","part_id":"lid","mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top","gap":1}},
		{"id":"k","part_id":"key","mate":{"interface":"key_bottom","to_instance":"l","to_interface":"lid_top"}}`)
	boundsNear(t, "lid on base", instanceBounds(t, r.Report, "l"), [3]float64{100, 0, 11}, [3]float64{140, 40, 13})
	// The key's up (+Y) aligns with lid_top's up (+X): it is turned 90°.
	boundsNear(t, "key on lid", instanceBounds(t, r.Report, "k"), [3]float64{128, 17, 13}, [3]float64{132, 23, 16})

	turned := build("turned", `{"id":"b","part_id":"base"},
		{"id":"k","part_id":"key","mate":{"interface":"key_bottom","to_instance":"b","to_interface":"base_top","angle":90}}`)
	boundsNear(t, "key turned 90° more", instanceBounds(t, turned.Report, "k"), [3]float64{17, 18, 10}, [3]float64{23, 22, 13})

	for instances, want := range map[string]string{
		`{"id":"b","part_id":"base"},{"id":"k","part_id":"key","mate":{"interface":"nope","to_instance":"b","to_interface":"base_top"}}`:                                                                                     "not an interface of part",
		`{"id":"b","part_id":"base"},{"id":"k","part_id":"key","mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top"}}`:                                                                                "not an interface of part",
		`{"id":"b","part_id":"base"},{"id":"l","part_id":"lid","mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top"}},{"id":"l2","part_id":"lid","mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top"}}`: "both mate to",
		`{"id":"l","part_id":"lid","mate":{"interface":"lid_top","to_instance":"k","to_interface":"key_bottom"}},{"id":"k","part_id":"key","mate":{"interface":"key_bottom","to_instance":"l","to_interface":"lid_top"}}`:    "cycle",
		`{"id":"b","part_id":"base"},{"id":"l","part_id":"lid","transform":{"translate":[1,0,0]},"mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top"}}`:                                         "both a transform and a mate",
	} {
		if _, _, err := normalizeDefinition([]byte(mateDefinition(instances, "", "")), 256); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("want %q, got %v", want, err)
		}
	}
}

func TestJointStatesAndSweep(t *testing.T) {
	s := inspectionService(t)
	instances := `{"id":"b","part_id":"base"},{"id":"w","part_id":"wall"},
		{"id":"l","part_id":"lid","mate":{"interface":"lid_under","to_instance":"b","to_interface":"base_top"}}`
	hinge := func(state string) string {
		return `{"id":"hinge","type":"revolute","parent_instance":"b","child_instance":"l","origin":[0,40,10],"axis":[-1,0,0],"limits":[0,180],"state":` + state + `}`
	}
	checks := `{"id":"opens_to_80","type":"joint_sweep","joint":"hinge","from":0,"to":80,"steps":4},
		{"id":"opens_fully","type":"joint_sweep","joint":"hinge","steps":12}`
	design := createDesign(t, s, "hinged", strings.Replace(mateDefinition(instances, hinge(`"open"`), checks), `"units":"mm",`, `"units":"mm","parameters":{"open":{"type":"angle","default":90,"min":0,"max":180}},`, 1), `{}`)
	result, err := s.Build(context.Background(), design.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	// At 90° the lid stands up along the back edge: y 40..42, z 10..50.
	boundsNear(t, "lid at 90°", instanceBounds(t, result.Report, "l"), [3]float64{0, 40, 10}, [3]float64{40, 42, 50})
	if c := checkID(t, result, "opens_to_80"); c.Status != "pass" {
		t.Fatalf("0-80° sweep should be clear: %#v", c)
	}
	// Past ~104° the 40 mm lid reaches the wall at y=50.
	if c := checkID(t, result, "opens_fully"); c.Status != "fail" || !strings.Contains(c.Message, "collides at") {
		t.Fatalf("0-180° sweep must hit the wall: %#v", c)
	}
	if _, _, err := normalizeDefinition([]byte(mateDefinition(instances, hinge(`200`), "")), 256); err == nil || !strings.Contains(err.Error(), "outside its limits") {
		t.Fatalf("numeric state outside limits must fail: %v", err)
	}
	twoJoints := hinge(`0`) + `,{"id":"slide","type":"prismatic","parent_instance":"w","child_instance":"l","origin":[0,0,0],"axis":[1,0,0]}`
	if _, _, err := normalizeDefinition([]byte(mateDefinition(instances, twoJoints, "")), 256); err == nil || !strings.Contains(err.Error(), "child of two joints") {
		t.Fatalf("two joints on one child must fail: %v", err)
	}
	slide := `{"id":"slide","type":"prismatic","parent_instance":"b","child_instance":"l","origin":[0,0,0],"axis":[1,0,0],"limits":[0,30],"state":25}`
	slid := createDesign(t, s, "slid", mateDefinition(instances, slide, ""), `{}`)
	slidResult, err := s.Build(context.Background(), slid.ID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	boundsNear(t, "lid slid 25", instanceBounds(t, slidResult.Report, "l"), [3]float64{25, 0, 10}, [3]float64{65, 40, 12})
}

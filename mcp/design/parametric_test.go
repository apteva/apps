package main

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"strings"
	"testing"
)

// The expression cases in testdata/expressions.json are the contract for the
// runner's formula language; run them through bun so `go test` covers them.
func TestSharedExpressionCases(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	cmd := exec.Command(bun, "test", "src/expressions.test.mjs")
	cmd.Dir = "runner"
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("expression cases failed: %v\n%s", err, out)
	}
}

func TestParameterTypes(t *testing.T) {
	raw := `{"schema":"apteva-design/v1","units":"mm","parameters":{
		"vent":{"type":"boolean","default":true},
		"count":{"type":"integer","default":3,"min":1,"max":8},
		"size":{"type":"length","default":20,"options":[10,20,30]}},
		"operations":[{"id":"b","type":"box","size":["size","size","size"]}],"output":"b"}`
	_, definition, err := normalizeDefinition([]byte(raw), 256)
	if err != nil {
		t.Fatal(err)
	}
	if definition.Parameters["vent"].Default != 1 {
		t.Fatalf("boolean default true must be 1, got %v", definition.Parameters["vent"].Default)
	}
	for _, good := range []string{`{"vent":false}`, `{"vent":1}`, `{"count":8}`, `{"size":30}`} {
		if _, err := normalizeParameters([]byte(good), definition); err != nil {
			t.Fatalf("%s: %v", good, err)
		}
	}
	for good, want := range map[string]string{`{"vent":0.5}`: "true/false", `{"count":2.5}`: "integer", `{"count":9}`: "above max", `{"size":25}`: "one of", `{"vent":"yes"}`: "number or boolean"} {
		if _, err := normalizeParameters([]byte(good), definition); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", good, want, err)
		}
	}
	bad := map[string]string{
		`"vent":{"type":"boolean","default":true,"max":1}`:        "cannot have",
		`"size":{"type":"length","default":25,"options":[10,20]}`: "one of",
		`"count":{"type":"integer","default":2.5}`:                "integer",
		`"x":{"type":"string","default":1}`:                       "type must be",
	}
	for params, want := range bad {
		body := `{"schema":"apteva-design/v1","units":"mm","parameters":{` + params + `},"operations":[{"id":"b","type":"box","size":[1,1,1]}],"output":"b"}`
		if _, _, err := normalizeDefinition([]byte(body), 256); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", params, want, err)
		}
	}
	for op, want := range map[string]string{
		`{"id":"c","type":"component"}`:                                                                               "requires source",
		`{"id":"c","type":"component","source":{"design_id":1}}`:                                                      "requires design_id",
		`{"id":"c","type":"box","size":[1,1,1],"when":""}`:                                                            "must not be empty",
		`{"id":"c","type":"box","size":[1,1,1],"when":[1]}`:                                                           "boolean, number, or expression",
		`{"id":"c","type":"component","source":{"design_id":1,"revision_id":1,"source_sha256":"x"},"parameters":[1]}`: "must be an object",
	} {
		body := `{"schema":"apteva-design/v1","units":"mm","operations":[` + op + `],"output":"c"}`
		if _, _, err := normalizeDefinition([]byte(body), 256); err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%s: want %q, got %v", op, want, err)
		}
	}
}

func createDesign(t *testing.T, s *Service, name, definition, parameters string) *Design {
	t.Helper()
	canonical, parsed, err := normalizeDefinition([]byte(definition), 256)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	params, err := normalizeParameters([]byte(parameters), parsed)
	if err != nil {
		t.Fatalf("%s parameters: %v", name, err)
	}
	design, err := s.store.CreateDesign(s.project, CreateDesignInput{Name: name, Definition: canonical, Parameters: params})
	if err != nil {
		t.Fatal(err)
	}
	return design
}

func buildVolume(t *testing.T, s *Service, designID int64) float64 {
	t.Helper()
	result, err := s.Build(context.Background(), designID, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	return result.Report.VolumeMM3
}

func TestConditionalOperations(t *testing.T) {
	s := inspectionService(t)
	definition := `{"schema":"apteva-design/v1","units":"mm",
		"parameters":{"hole":{"type":"boolean","default":false},"slot":{"type":"boolean","default":false}},
		"operations":[
			{"id":"block","type":"box","size":[20,20,10]},
			{"id":"bore","type":"cylinder","radius":3,"height":10,"origin":[10,10,0],"when":"hole"},
			{"id":"slot_cut","type":"box","size":[4,20,10],"origin":[0,0,0],"when":"slot and not hole"},
			{"id":"part","type":"cut","base":"block","tools":["bore","slot_cut"]}],
		"output":"part"}`
	plain := buildVolume(t, s, createDesign(t, s, "plain", definition, `{}`).ID)
	if math.Abs(plain-4000) > 1e-3 {
		t.Fatalf("skipped tools must leave the base: %v", plain)
	}
	bored := buildVolume(t, s, createDesign(t, s, "bored", definition, `{"hole":true,"slot":true}`).ID)
	if want := 4000 - math.Pi*9*10; math.Abs(bored-want) > 1 {
		t.Fatalf("bore only (slot suppressed by condition): %v want %v", bored, want)
	}
	slotted := buildVolume(t, s, createDesign(t, s, "slotted", definition, `{"slot":true}`).ID)
	if math.Abs(slotted-3200) > 1e-3 {
		t.Fatalf("slot: %v", slotted)
	}
	// Referring to a skipped operation directly is an error, not a silent no-op.
	broken := strings.Replace(definition, `"output":"part"`, `"output":"bore"`, 1)
	if _, err := s.Build(context.Background(), createDesign(t, s, "broken", broken, `{}`).ID, 0, nil); err == nil || !strings.Contains(err.Error(), "skipped") {
		t.Fatalf("skipped output must fail and name the skip: %v", err)
	}
}

// Acceptance: one base enclosure block with three variants built from it
// through component operations (button hole, OLED window, PIR opening).
// Changing the block and running one bulk refresh moves every variant.
func TestComponentVariantsBulkRefresh(t *testing.T) {
	s := inspectionService(t)
	baseDefinition := func(height int) string {
		return `{"schema":"apteva-design/v1","units":"mm",
			"parameters":{"width":{"type":"length","default":40,"min":20,"max":80},"depth":{"type":"length","default":30},"height":{"type":"length","default":` + itoa(height) + `}},
			"operations":[{"id":"block","type":"box","size":["width","depth","height"]}],"output":"block"}`
	}
	base := createDesign(t, s, "Base block", baseDefinition(10), `{}`)
	baseRevision, _ := s.store.GetRevision(s.project, base.CurrentRevisionID)
	source := func(extra string) string {
		return `{"design_id":` + itoa(int(base.ID)) + `,"revision_id":` + itoa(int(baseRevision.ID)) + `,"source_sha256":"` + baseRevision.SourceSHA256 + `"` + extra + `}`
	}
	variant := func(name, params, cutter string) *Design {
		return createDesign(t, s, name, `{"schema":"apteva-design/v1","units":"mm",
			"parameters":{"size":{"type":"length","default":50}`+params+`},
			"operations":[
				{"id":"body","type":"component","source":`+source("")+`,"parameters":{"width":"size","depth":30}},
				`+cutter+`,
				{"id":"part","type":"cut","base":"body","tools":["opening"]}],
			"output":"part"}`, `{}`)
	}
	button := variant("Button", "", `{"id":"opening","type":"cylinder","radius":3,"height":40,"origin":[10,15,-1]}`)
	oled := variant("OLED", "", `{"id":"opening","type":"box","size":[20,10,40],"origin":[5,10,-1]}`)
	pir := variant("PIR", `,"pir":{"type":"boolean","default":true}`, `{"id":"opening","type":"cylinder","radius":5,"height":40,"origin":[25,15,-1],"when":"pir"}`)

	before := map[int64]float64{}
	for _, design := range []*Design{button, oled, pir} {
		before[design.ID] = buildVolume(t, s, design.ID)
	}
	// The link override "width":"size" makes each variant 50 mm wide.
	if want := 50*30*10 - math.Pi*9*10; math.Abs(before[button.ID]-want) > 1 {
		t.Fatalf("button variant volume %v want %v", before[button.ID], want)
	}

	// An override outside the source range is rejected at build time.
	tooWide := createDesign(t, s, "Too wide", `{"schema":"apteva-design/v1","units":"mm",
		"operations":[{"id":"body","type":"component","source":`+source("")+`,"parameters":{"width":120}}],"output":"body"}`, `{}`)
	if _, err := s.Build(context.Background(), tooWide.ID, 0, nil); err == nil || !strings.Contains(err.Error(), "above max") {
		t.Fatalf("override above source max must fail: %v", err)
	}

	dependents, err := s.ComponentDependents(base.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(dependents) != 4 {
		t.Fatalf("want 4 dependents, got %#v", dependents)
	}
	for _, dependent := range dependents {
		if dependent.Stale {
			t.Fatalf("fresh dependent reported stale: %#v", dependent)
		}
	}

	// Make the block taller, then refresh everything from one call.
	canonical, _, err := normalizeDefinition([]byte(baseDefinition(14)), 256)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.store.CreateRevision(s.project, CreateRevisionInput{DesignID: base.ID, ExpectedParent: base.CurrentRevisionID, Definition: canonical, Parameters: []byte(`{}`), Note: "taller"}); err != nil {
		t.Fatal(err)
	}
	if dependents, _ = s.ComponentDependents(base.ID); !dependents[0].Stale {
		t.Fatalf("dependents must be stale after the source changes: %#v", dependents[0])
	}
	results, err := s.RefreshAllComponentDependents(base.ID, true, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, result := range results {
		counts[result.Status]++
		if result.Status == "failed" && (result.DesignName != "Too wide" || !strings.Contains(result.Error, "above max")) {
			t.Fatalf("unexpected failure: %#v", result)
		}
	}
	// The out-of-range link fails on its own; the three variants still move.
	if counts["refreshed"] != 3 || counts["failed"] != 1 {
		t.Fatalf("want 3 refreshed + 1 failed, got %#v", results)
	}
	for _, design := range []*Design{button, oled, pir} {
		after := buildVolume(t, s, design.ID)
		if ratio := after / before[design.ID]; math.Abs(ratio-1.4) > 0.01 {
			t.Fatalf("%s: volume %v -> %v (ratio %v), want the 14/10 height change", design.Name, before[design.ID], after, ratio)
		}
	}
	again, err := s.RefreshAllComponentDependents(base.ID, true, "", "test")
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range again {
		if result.Status == "refreshed" {
			t.Fatalf("second refresh must not create revisions: %#v", result)
		}
	}
}

func itoa(value int) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

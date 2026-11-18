package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

// selectionFake is a Mobile session whose Computer offers the Android
// selection actions added in Computer 0.15.0.
func selectionFake() *fakePlatform {
	p := nativeFake()
	respond := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		out := respond(tool, in)
		if tool == "computer.browser_open" {
			caps := out["capabilities"].(map[string]any)
			caps["actions"] = append(caps["actions"].([]string), "native_select", "native_scroll_to")
		}
		if tool == "computer.computer_use" && in["action"] == "native_select" {
			return map[string]any{"success": true, "native_value": "changed"}
		}
		return out
	}
	return p
}

func TestNativeSelectionForwardsGuards(t *testing.T) {
	p := selectionFake()
	ctx, a := newTestCtx(t, p)
	markers := []string{"Choose a plan", "Continue"}
	row := "Plan B >> android.widget.RadioButton"
	actor := saveFixtureActor(t, ctx, a, privateNativeFixture(
		map[string]any{"action": "native_scroll_to", "native_app": "ExampleApp", "native_required": markers, "native_target": "Plan B", "native_container": "example:id/plans", "direction": "down", "max_swipes": 5},
		map[string]any{"action": "native_select", "native_app": "ExampleApp", "native_required": markers, "native_target": row, "native_state": true},
		map[string]any{"action": "native_tap", "native_app": "ExampleApp", "native_required": markers, "native_target": "Continue", "native_checked": map[string]bool{row: true}},
	))
	if _, err := a.toolActorRun(ctx, map[string]any{"actor_id": actor.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, a)
	if run["status"] != "completed" {
		t.Fatalf("status=%v error=%v", run["status"], run["error"])
	}
	var got []map[string]any
	for _, call := range p.callsSnapshot() {
		if call.tool == "computer_use" {
			got = append(got, call.args)
		}
	}
	if len(got) != 3 {
		t.Fatalf("calls=%v", got)
	}
	scroll, sel, tap := got[0], got[1], got[2]
	if scroll["action"] != "native_scroll_to" || scroll["native_container"] != "example:id/plans" || scroll["direction"] != "down" || intFromAny(scroll["max_swipes"]) != 5 || scroll["native_target"] != "Plan B" {
		t.Fatalf("scroll args=%v", scroll)
	}
	if sel["action"] != "native_select" || sel["native_state"] != true || sel["native_target"] != row || sel["native_container"] != nil {
		t.Fatalf("select args=%v", sel)
	}
	if tap["action"] != "native_tap" || !reflect.DeepEqual(tap["native_checked"], map[string]bool{row: true}) || tap["native_state"] != nil {
		t.Fatalf("tap args=%v", tap)
	}
}

func TestNativeCheckedSelectorsRenderInputs(t *testing.T) {
	var def actorDefinition
	json.Unmarshal(mustJSON(privateNativeFixture(
		map[string]any{"action": "native_select", "native_app": "ExampleApp", "native_required": []string{"Ready"}, "native_target": "{{plan}} >> android.widget.RadioButton"},
		map[string]any{"action": "native_tap", "native_app": "ExampleApp", "native_required": []string{"Ready"}, "native_target": "Continue",
			"native_checked": map[string]bool{"{{plan}} >> android.widget.RadioButton": true},
			"native_values":  map[string]string{"{{field}}": "{{plan}}"}},
	)), &def)
	rendered, err := renderActorDefinition(def, map[string]any{"plan": "Plan B", "field": "example:id/summary"})
	if err != nil {
		t.Fatal(err)
	}
	tap := rendered.Steps[2]
	if rendered.Steps[1].NativeTarget != "Plan B >> android.widget.RadioButton" ||
		!reflect.DeepEqual(tap.NativeChecked, map[string]bool{"Plan B >> android.widget.RadioButton": true}) ||
		!reflect.DeepEqual(tap.NativeValues, map[string]string{"example:id/summary": "Plan B"}) {
		t.Fatalf("rendered=%+v", rendered.Steps[1:])
	}
	if _, err := renderActorDefinition(def, map[string]any{"field": "x"}); err == nil {
		t.Fatal("undefined selector variable accepted")
	}
}

func TestNativeSelectionValidation(t *testing.T) {
	m := []string{"Ready"}
	step := func(extra map[string]any) map[string]any {
		s := map[string]any{"native_app": "ExampleApp", "native_required": m, "native_target": "Row"}
		for k, v := range extra {
			s[k] = v
		}
		return s
	}
	cases := map[string]map[string]any{
		"scroll without container": step(map[string]any{"action": "native_scroll_to"}),
		"scroll sideways":          step(map[string]any{"action": "native_scroll_to", "native_container": "list", "direction": "left"}),
		"too many swipes":          step(map[string]any{"action": "native_scroll_to", "native_container": "list", "max_swipes": 21}),
		"scroll with text":         step(map[string]any{"action": "native_scroll_to", "native_container": "list", "text": "x"}),
		"select with text":         step(map[string]any{"action": "native_select", "text": "x"}),
		"select without target":    {"action": "native_select", "native_app": "ExampleApp", "native_required": m},
		"state on tap":             step(map[string]any{"action": "native_tap", "native_state": true}),
		"container on tap":         step(map[string]any{"action": "native_tap", "native_container": "list"}),
		"direction on tap":         step(map[string]any{"action": "native_tap", "direction": "down"}),
		"empty checked selector":   step(map[string]any{"action": "native_tap", "native_checked": map[string]bool{" ": true}}),
		"checked on coordinate":    {"action": "tap", "x": 1, "y": 1, "native_checked": map[string]bool{"Row": true}},
	}
	for name, s := range cases {
		var def actorDefinition
		json.Unmarshal(mustJSON(privateNativeFixture(s)), &def)
		if err := validateActorDefinition(def); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	for _, s := range []map[string]any{
		step(map[string]any{"action": "native_scroll_to", "native_container": "list", "direction": "up", "max_swipes": 20}),
		step(map[string]any{"action": "native_select", "native_state": false, "native_checked": map[string]bool{"Other": false}}),
	} {
		var def actorDefinition
		json.Unmarshal(mustJSON(privateNativeFixture(s)), &def)
		if err := validateActorDefinition(def); err != nil {
			t.Errorf("%v rejected: %v", s["action"], err)
		}
	}
}

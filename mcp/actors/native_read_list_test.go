package main

import (
	"encoding/json"
	"strings"
	"testing"
)

// listFake is a Mobile session whose Computer offers native_read_list
// (Computer 0.17.0) and answers it with respond.
func listFake(respond func(in map[string]any) map[string]any) *fakePlatform {
	p := nativeFake()
	base := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		out := base(tool, in)
		if tool == "computer.browser_open" {
			caps := out["capabilities"].(map[string]any)
			caps["actions"] = append(caps["actions"].([]string), "native_read_list")
		}
		if tool == "computer.computer_use" && in["action"] == "native_read_list" {
			return respond(in)
		}
		return out
	}
	return p
}

func listStep(extra map[string]any) map[string]any {
	s := map[string]any{"action": "native_read_list", "native_app": "com.example", "native_required": []string{"Results"}, "native_container": "example:id/list", "verified_field": "row"}
	for k, v := range extra {
		s[k] = v
	}
	return s
}

func runListActor(t *testing.T, p *fakePlatform, step map[string]any) (map[string]any, []string) {
	t.Helper()
	ctx, app := newTestCtx(t, p)
	def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, step)
	def["read_only"] = true
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	rows, err := ctx.AppDB().Query(`SELECT item_json FROM actors_dataset_items ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var values []string
	for rows.Next() {
		var raw string
		rows.Scan(&raw)
		var item map[string]any
		json.Unmarshal([]byte(raw), &item)
		values = append(values, stringFromAny(item["row"]))
	}
	return run, values
}

func listRows(rows ...[]string) []any {
	out := []any{}
	for _, texts := range rows {
		t := []any{}
		for _, s := range texts {
			t = append(t, s)
		}
		out = append(out, map[string]any{"texts": t})
	}
	return out
}

func TestNativeReadListStoresOneItemPerRow(t *testing.T) {
	var got map[string]any
	p := listFake(func(in map[string]any) map[string]any {
		got = in
		return map[string]any{"success": true, "stopped": "end_of_list", "swipes": 2, "screens": 3, "rows": listRows(
			[]string{"First person", "Role one", " "},
			[]string{"Second person", "Role two"},
			[]string{},
		)}
	})
	run, values := runListActor(t, p, listStep(map[string]any{"native_rows": "clickable", "max_items": 10, "max_swipes": 3, "direction": "down", "native_values": map[string]string{"header": "account"}}))
	if run["status"] != "completed" {
		t.Fatalf("status=%v error=%v", run["status"], run["error"])
	}
	if got["native_container"] != "example:id/list" || got["native_rows"] != "clickable" || intFromAny(got["max_items"]) != 10 || intFromAny(got["max_swipes"]) != 3 || got["direction"] != "down" || mapFromAny(got["native_values"])["header"] != "account" || got["native_target"] != nil {
		t.Fatalf("Computer args=%v", got)
	}
	if strings.Join(values, "|") != "First person\nRole one|Second person\nRole two" {
		t.Fatalf("dataset=%q", values)
	}
}

func TestNativeReadListTextModeStoresOneItem(t *testing.T) {
	var got map[string]any
	p := listFake(func(in map[string]any) map[string]any {
		got = in
		return map[string]any{"success": true, "stopped": "max_swipes", "swipes": 4, "screens": 5, "text": []any{"Name", "Headline", "About"}}
	})
	run, values := runListActor(t, p, listStep(map[string]any{"native_rows": "text", "max_swipes": 4}))
	if run["status"] != "completed" {
		t.Fatalf("status=%v error=%v", run["status"], run["error"])
	}
	if got["max_items"] != nil || got["native_rows"] != "text" {
		t.Fatalf("Computer args=%v", got)
	}
	if len(values) != 1 || values[0] != "Name\nHeadline\nAbout" {
		t.Fatalf("dataset=%q", values)
	}
}

func TestNativeReadListFailsClosed(t *testing.T) {
	two := listRows([]string{"A"}, []string{"B"})
	cases := map[string]struct {
		step map[string]any
		out  map[string]any
		want string
	}{
		"screen changed":   {listStep(nil), map[string]any{"success": true, "stopped": "screen_changed", "detail": "native screen marker is not visible", "rows": two}, "screen changed"},
		"below min_items":  {listStep(map[string]any{"min_items": 3}), map[string]any{"success": true, "stopped": "end_of_list", "rows": two}, "min_items is 3"},
		"no receipt":       {listStep(nil), map[string]any{"stopped": "end_of_list", "rows": two}, "did not confirm"},
		"too many rows":    {listStep(map[string]any{"max_items": 1}), map[string]any{"success": true, "stopped": "max_items", "rows": two}, "bounded list rows"},
		"rows not strings": {listStep(nil), map[string]any{"success": true, "stopped": "end_of_list", "rows": []any{map[string]any{"texts": []any{1}}}}, "invalid list row"},
		"text not strings": {listStep(map[string]any{"native_rows": "text"}), map[string]any{"success": true, "stopped": "end_of_list", "text": "A"}, "bounded list text"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			out := c.out
			p := listFake(func(map[string]any) map[string]any { return out })
			run, values := runListActor(t, p, c.step)
			if run["status"] == "completed" || !strings.Contains(stringFromAny(run["error"]), c.want) {
				t.Fatalf("status=%v error=%v", run["status"], run["error"])
			}
			if len(values) != 0 {
				t.Fatalf("saved rows from a failed read: %q", values)
			}
		})
	}
}

func TestNativeReadListNeedsComputerSupport(t *testing.T) {
	p := nativeFake() // no native_read_list capability
	ctx, app := newTestCtx(t, p)
	def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, listStep(nil))
	def["read_only"] = true
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, app)
	if run["status"] == "completed" || !strings.Contains(stringFromAny(run["error"]), "native_read_list") {
		t.Fatalf("status=%v error=%v", run["status"], run["error"])
	}
	for _, call := range p.callsSnapshot() {
		if call.tool == "computer_use" && call.args["action"] == "native_read_list" {
			t.Fatal("list read sent to a Computer without support")
		}
	}
}

func TestNativeReadListValidation(t *testing.T) {
	bad := map[string]map[string]any{
		"no container":         listStep(map[string]any{"native_container": ""}),
		"no verified_field":    listStep(map[string]any{"verified_field": ""}),
		"target":               listStep(map[string]any{"native_target": "Row"}),
		"input text":           listStep(map[string]any{"text": "x"}),
		"no markers":           listStep(map[string]any{"native_required": []string{}}),
		"too many items":       listStep(map[string]any{"max_items": 51}),
		"too many swipes":      listStep(map[string]any{"max_swipes": 11}),
		"sideways":             listStep(map[string]any{"direction": "left"}),
		"unknown grouping":     listStep(map[string]any{"native_rows": "cards"}),
		"min above max":        listStep(map[string]any{"max_items": 2, "min_items": 3}),
		"text with max_items":  listStep(map[string]any{"native_rows": "text", "max_items": 5}),
		"rows on another step": {"action": "native_read", "native_app": "com.example", "native_required": []string{"Results"}, "native_target": "x", "verified_field": "row", "native_rows": "items"},
		"replace over 4096":    {"action": "native_replace", "native_app": "com.example", "native_required": []string{"Results"}, "native_target": "x", "text": strings.Repeat("é", 2049)},
		"replace with newline": {"action": "native_replace", "native_app": "com.example", "native_required": []string{"Results"}, "native_target": "x", "text": "a\nb"},
		"type over 256 bytes":  {"action": "native_type", "native_app": "com.example", "native_required": []string{"Results"}, "native_target": "x", "text": strings.Repeat("a", 257)},
		"list without a phone": nil,
	}
	for name, s := range bad {
		def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, s)
		if s == nil {
			def = map[string]any{"schema_version": 1, "browser": map[string]any{"backend": "local"}, "allowed_hosts": []string{"example.com"}, "steps": []map[string]any{listStep(nil)}}
		}
		var parsed actorDefinition
		json.Unmarshal(mustJSON(def), &parsed)
		if err := validateActorDefinition(parsed); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	good := []map[string]any{
		listStep(nil),
		listStep(map[string]any{"native_rows": "items", "max_items": 50, "max_swipes": 10, "direction": "up", "min_items": 1}),
		listStep(map[string]any{"native_rows": "text", "max_swipes": 6, "min_items": 1}),
		{"action": "native_replace", "native_app": "com.example", "native_required": []string{"Results"}, "native_target": "x", "text": strings.Repeat("é", 2048)},
	}
	for _, s := range good {
		def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, s)
		if s["action"] == "native_read_list" {
			def["read_only"] = true
		}
		var parsed actorDefinition
		json.Unmarshal(mustJSON(def), &parsed)
		if err := validateActorDefinition(parsed); err != nil {
			t.Errorf("%v rejected: %v", s, err)
		}
	}
}

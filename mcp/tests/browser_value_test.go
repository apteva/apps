package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestBrowserSelectValueValidation(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		valid        bool
	}{
		{"empty literal", `,"value":""`, true},
		{"nonempty literal", `,"value":"bell"`, true},
		{"reference", `,"value_ref":"ringtone.default"`, true},
		{"missing", "", false},
		{"null", `,"value":null`, false},
		{"null with reference", `,"value":null,"value_ref":"ringtone.default"`, false},
		{"number", `,"value":0`, false},
		{"boolean", `,"value":false`, false},
		{"empty literal and reference", `,"value":"","value_ref":"ringtone.default"`, false},
		{"nonempty literal and reference", `,"value":"bell","value_ref":"ringtone.default"`, false},
		{"empty reference", `,"value_ref":""`, false},
		{"null reference", `,"value_ref":null`, false},
		{"literal and empty reference", `,"value":"","value_ref":""`, false},
		{"case-insensitive null", `,"Value":null,"value_ref":"ringtone.default"`, false},
		{"invalid reference", `,"value_ref":"bad ref"`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var action BrowserAction
			err := json.Unmarshal([]byte(`{"action":"select","locator":{"test_id":"ringtone"}`+tc.fields+`}`), &action)
			if err == nil {
				step := Step{ID: "select", Kind: "browser", Browser: &action}
				err = step.validateBrowser()
			}
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v, err=%v", tc.valid, err)
			}
		})
	}
}

func TestBrowserSelectSaveReloadExecutePreservesResolvedValues(t *testing.T) {
	for _, tc := range []struct {
		name, fields string
		refs         map[string]string
		captures     map[string]any
		want         string
	}{
		{name: "empty literal", fields: `,"value":""`, want: ""},
		{name: "literal", fields: `,"value":"bell"`, want: "bell"},
		{name: "expanded empty", fields: `,"value":"{{selected}}"`, captures: map[string]any{"selected": ""}, want: ""},
		{name: "expanded whitespace", fields: `,"value":"{{selected}}"`, captures: map[string]any{"selected": " bell "}, want: " bell "},
		{name: "empty reference", fields: `,"value_ref":"ringtone.default"`, refs: map[string]string{"ringtone.default": ""}, want: ""},
		{name: "reference", fields: `,"value_ref":"ringtone.default"`, refs: map[string]string{"ringtone.default": "bell"}, want: "bell"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			commands := 0
			platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
				if app != "computer" || tool != "browser_test_step" {
					t.Fatalf("unexpected call %s.%s", app, tool)
				}
				// Exercise the JSON wire shape, not just the Go pointer representation.
				var wire struct {
					Command map[string]json.RawMessage `json:"command"`
				}
				if err := json.Unmarshal([]byte(encode(args)), &wire); err != nil {
					t.Fatal(err)
				}
				var action string
				json.Unmarshal(wire.Command["action"], &action)
				if action == "observe" {
					return json.Unmarshal([]byte(`{"observation":{"count":1,"visible":true,"enabled":true}}`), out)
				}
				commands++
				raw, ok := wire.Command["value"]
				if !ok {
					t.Fatal("value omitted from Computer command")
				}
				if string(raw) == "null" {
					t.Fatal("value became null")
				}
				var actual string
				if err := json.Unmarshal(raw, &actual); err != nil || actual != tc.want {
					t.Fatalf("wire value %s; want %q; err %v", raw, tc.want, err)
				}
				return json.Unmarshal([]byte(`{"action_dispatched":true}`), out)
			}}
			a, app, s := fixture(t, tk.WithPlatform(platform))
			config := browserReleaseConfig()
			if tc.refs != nil {
				config.Browser.ValueRefs = map[string]ToolProbe{"ringtone.default": {App: "api", Tool: "default", Path: "/value"}}
			}
			suite, err := s.saveSuite(Suite{Name: "Ring selection", Release: config})
			if err != nil {
				t.Fatal(err)
			}
			var check Check
			if err = json.Unmarshal([]byte(`{"name":"inherit","kind":"browser","enabled":true,"definition":{"steps":[{"id":"inherit","browser":{"action":"select","locator":{"test_id":"ringtone"}`+tc.fields+`}}]}}`), &check); err != nil {
				t.Fatal(err)
			}
			check.SuiteID = suite.ID
			saved, err := s.saveCheck(check)
			if err != nil {
				t.Fatal(err)
			}
			var stored string
			if err = s.db.QueryRow(`SELECT definition FROM test_checks WHERE id=?`, saved.ID).Scan(&stored); err != nil {
				t.Fatal(err)
			}
			if tc.name == "empty literal" && !strings.Contains(stored, `"value":""`) {
				t.Fatalf("empty literal lost in storage: %s", stored)
			}
			loaded, err := s.checks(suite.ID)
			if err != nil || len(loaded) != 1 {
				t.Fatal(loaded, err)
			}
			before := encode(loaded[0])
			services := &appRunContext{ctx: app, browser: &browserCheckSession{id: "bt_test"}, release: &releaseSession{config: config, browserValues: tc.refs}}
			row := StepResult{}
			if _, err = a.executeBrowserStep(context.Background(), loaded[0].Definition.Steps[0], tc.captures, services, &row); err != nil {
				t.Fatal(err)
			}
			if commands != 1 || !row.ActionDispatched {
				t.Fatal("select was not dispatched exactly once", commands, row)
			}
			if encode(loaded[0]) != before {
				t.Fatal("execution mutated saved literal/template")
			}
		})
	}
}

func TestBrowserValueProbeAllowsEmptyWithoutWeakeningIdentityProbes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		output any
		allow  bool
	}{
		{"empty", map[string]any{"value": ""}, true},
		{"whitespace", map[string]any{"value": " bell "}, true},
		{"missing", map[string]any{}, false},
		{"null", map[string]any{"value": nil}, false},
		{"non-string", map[string]any{"value": 1}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			platform := &scenarioPlatform{call: func(_ context.Context, app, tool string, args map[string]any, out any) error {
				return json.Unmarshal([]byte(encode(tc.output)), out)
			}}
			session := &releaseSession{id: "run_test", env: environmentClient{platform: platform}}
			probe := ToolProbe{App: "fixture", Tool: "default", Path: "/value"}
			got, err := session.probeString(context.Background(), probe, true)
			if (err == nil) != tc.allow {
				t.Fatal(got, err)
			}
			if tc.name == "empty" {
				if _, err = session.probe(context.Background(), probe); err == nil {
					t.Fatal("empty identity/candidate probe accepted")
				}
			}
			if tc.name == "whitespace" && got != " bell " {
				t.Fatal("browser value was normalized", got)
			}
		})
	}
}

func TestBrowserOmittedFillValueRemainsValid(t *testing.T) {
	var action BrowserAction
	if err := json.Unmarshal([]byte(`{"action":"fill","locator":{"test_id":"name"}}`), &action); err != nil {
		t.Fatal(err)
	}
	step := Step{ID: "clear", Kind: "browser", Browser: &action}
	if err := step.validateBrowser(); err != nil {
		t.Fatal(err)
	}
	if action.Value != nil || strings.Contains(encode(action), `"value"`) {
		t.Fatal("omitted fill value became explicit", encode(action))
	}
}

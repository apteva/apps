package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func clickOpensActor(opens map[string]any, extra map[string]any) map[string]any {
	click := map[string]any{"action": "click", "expected_effect": "open_configuration", "locator": map[string]any{"selector": "button.add"}}
	if opens != nil {
		click["opens"] = opens
	}
	for k, v := range extra {
		click[k] = v
	}
	return map[string]any{
		"schema_version": 1, "allowed_hosts": []any{"example.com"},
		"limits": map[string]any{"step_retries": 0},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://example.com/edit"},
			click,
			map[string]any{"action": "click", "locator": map[string]any{"selector": "button.add-selected"}},
		}, "output_schema": map[string]any{},
	}
}

var pickerOpens = map[string]any{"conditions": []any{map[string]any{"type": "selector_present", "selector": ".picker .add-selected"}}, "timeout_ms": 1000}

func TestClickOpensConfirmsAndRetriesOnce(t *testing.T) {
	for _, tc := range []struct {
		name       string
		openAfter  int  // the picker shows once this many add clicks happened
		lateOnWait int  // the picker shows from this wait_for call on, regardless of clicks
		moveAway   bool // the first wait reports a different page
		wantClicks int
		wantDone   bool
		wantErr    string
	}{
		{name: "opens_first_click", openAfter: 1, wantClicks: 1, wantDone: true},
		{name: "opens_on_second_click", openAfter: 2, wantClicks: 2, wantDone: true},
		{name: "opens_late_no_second_click", openAfter: 99, lateOnWait: 2, wantClicks: 1, wantDone: true},
		{name: "never_opens", openAfter: 99, wantClicks: 2, wantErr: "configuration did not open after 2 clicks"},
		{name: "page_changed", openAfter: 99, moveAway: true, wantClicks: 1, wantErr: "page changed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plat := newFakePlatform()
			addClicks, waits := 0, 0
			var waitTimeouts []any
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool != "computer.computer_use" {
					return nil
				}
				switch in["action"] {
				case "click":
					if in["selector"] == "button.add" {
						addClicks++
						if in["expected_effect"] != "open_configuration" {
							t.Errorf("click lost expected_effect: %v", in)
						}
					}
					return map[string]any{"current_url": "https://example.com/edit"}
				case "wait_for":
					waits++
					waitTimeouts = append(waitTimeouts, in["timeout_ms"])
					url := "https://example.com/edit"
					if tc.moveAway {
						url = "https://example.com/elsewhere"
					}
					open := addClicks >= tc.openAfter || (tc.lateOnWait > 0 && waits >= tc.lateOnWait)
					return map[string]any{"matched": open, "timed_out": !open, "current_url": url}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, clickOpensActor(pickerOpens, nil))
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			queued, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_ = app.executeActorRun(context.Background(), ctx, queued)
			run, err := getActorRun(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (run["status"] == "completed") != tc.wantDone || !strings.Contains(stringFromAny(run["error"]), tc.wantErr) {
				t.Fatalf("status=%v error=%v", run["status"], run["error"])
			}
			if addClicks != tc.wantClicks {
				t.Fatalf("add clicks = %d, want %d (waits %d)", addClicks, tc.wantClicks, waits)
			}
			nextStep := false
			for _, call := range plat.callsSnapshot() {
				if call.args["selector"] == "button.add-selected" {
					nextStep = true
				}
			}
			if nextStep != tc.wantDone {
				t.Fatalf("next step dispatched=%t", nextStep)
			}
			if tc.wantClicks == 2 && (len(waitTimeouts) < 3 || intFromAny(waitTimeouts[1]) != 500) {
				t.Fatalf("expected a short recheck before clicking again, wait timeouts %v", waitTimeouts)
			}
		})
	}
}

func TestClickOpensValidation(t *testing.T) {
	bad := map[string]map[string]any{
		"no expected_effect": clickOpensActor(pickerOpens, map[string]any{"expected_effect": ""}),
		"committing effect":  clickOpensActor(pickerOpens, map[string]any{"expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit"}),
		"once_key":           clickOpensActor(pickerOpens, map[string]any{"once_key": "k1"}),
		"effect_phase":       clickOpensActor(pickerOpens, map[string]any{"effect_phase": "prepare"}),
		"no conditions":      clickOpensActor(map[string]any{"timeout_ms": 1000}, nil),
		"bad condition":      clickOpensActor(map[string]any{"conditions": []any{map[string]any{"type": "nonsense"}}}, nil),
	}
	onWait := clickOpensActor(nil, nil)
	onWait["steps"] = append(onWait["steps"].([]any), map[string]any{"action": "wait_for", "conditions": []any{map[string]any{"type": "selector_present", "selector": "x"}}, "opens": pickerOpens})
	bad["opens on wait_for"] = onWait
	for name, raw := range bad {
		var def actorDefinition
		if err := json.Unmarshal(mustJSON(raw), &def); err != nil {
			t.Fatal(err)
		}
		if err := validateActorDefinition(def); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	var def actorDefinition
	json.Unmarshal(mustJSON(clickOpensActor(pickerOpens, nil)), &def)
	if err := validateActorDefinition(def); err != nil {
		t.Fatalf("valid opens rejected: %v", err)
	}
	if actorStepMayWrite(def.Steps[1]) {
		t.Fatal("a configuration click with opens must stay a non-writing step")
	}
}

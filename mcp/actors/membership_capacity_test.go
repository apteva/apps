package main

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestLargeActorRetrievesAndExecutesOnlySelectedOperation(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	operations := map[string]any{}
	for i := 0; i < maxActorOperations-1; i++ {
		operations[fmt.Sprintf("unused_%03d", i)] = map[string]any{"steps": []any{map[string]any{"action": "goto", "url": "https://example.com/unused"}}}
	}
	selected := map[string]any{"steps": []any{map[string]any{"action": "goto", "url": "https://example.com/selected"}}}
	operations["selected"] = selected
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "operations": operations})
	got, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "revision": rec.Revision, "operation": "selected"})
	if err != nil {
		t.Fatal(err)
	}
	encoded := stringMustJSON(got)
	if strings.Contains(encoded, "unused_") || len(encoded) > maxActorOperationResponseBytes {
		t.Fatal("single-operation retrieval included unrelated operations")
	}
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "selected"}); err != nil {
		t.Fatal(err)
	}
	run, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = app.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	for _, call := range plat.callsSnapshot() {
		if strings.Contains(stringMustJSON(call.args), "/unused") {
			t.Fatal("an unselected operation was executed")
		}
	}
}

func TestSelectOptionForwardsObservedTextAndModes(t *testing.T) {
	for _, tc := range []struct {
		name, key, mode string
		value           any
	}{
		{"value", "value", "", "paid"},
		{"values", "values", "add", []string{"paid", "exclusive"}},
		{"text", "text", "", "General support"},
		{"texts", "texts", "remove", []string{"General support", "Digital downloads"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plat := newFakePlatform()
			plat.formSOM = true
			ctx, app := newTestCtx(t, plat)
			step := map[string]any{"action": "select_option", "locator": map[string]any{"text": "Tier", "exact": true, "som_only": true}, tc.key: tc.value}
			if tc.mode != "" {
				step["mode"] = tc.mode
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, step}})
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			found := false
			for _, call := range plat.callsSnapshot() {
				if call.args["action"] != "select_option" {
					continue
				}
				found = true
				if stringMustJSON(call.args[tc.key]) != stringMustJSON(tc.value) || call.args["coordinate"] != nil || call.args["target_id"] == nil || call.args["som_revision"] != "form-1" {
					t.Fatalf("selection or semantic guards changed: %#v", call.args)
				}
				if tc.mode != "" && call.args["mode"] != tc.mode {
					t.Fatalf("selection mode was dropped: %#v", call.args)
				}
				for _, key := range []string{"value", "values", "text", "texts"} {
					if key != tc.key && call.args[key] != nil {
						t.Fatalf("conflicting selection input: %#v", call.args)
					}
				}
			}
			if !found {
				t.Fatal("selection was not dispatched")
			}
		})
	}
}

func TestSelectOptionRejectsAmbiguousOrMissingChoices(t *testing.T) {
	for _, step := range []actorStep{
		{Action: "select_option"},
		{Action: "select_option", Value: "paid", Text: "General support"},
		{Action: "select_option", Values: []string{"paid"}, Texts: []string{"General support"}},
		{Action: "select_option", Text: "General support", Mode: "append"},
	} {
		step.Locator = actorLocator{Selector: "select"}
		d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{step}}
		if err := validateActorDefinition(d); err == nil {
			t.Fatalf("invalid selection accepted: %#v", step)
		}
	}
	// The expansion must not loosen the step limit or mixing rules.
	d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Operations: map[string]actorOperation{"one": {Steps: make([]actorStep, maxActorSteps+1)}}}
	if validateActorDefinition(d) == nil {
		t.Fatal("operation step limit changed")
	}
	d.Operations["one"] = actorOperation{Steps: []actorStep{{Action: "wait", Duration: 1}}}
	d.Steps = []actorStep{{Action: "wait", Duration: 1}}
	if validateActorDefinition(d) == nil {
		t.Fatal("mixed steps and operations accepted")
	}
}

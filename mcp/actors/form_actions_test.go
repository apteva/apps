package main

import (
	"context"
	"testing"
)

func TestSemanticFormActionsAndRawExtraction(t *testing.T) {
	plat := newFakePlatform()
	plat.formSOM = true
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, map[string]any{
		"schema_version": 1, "allowed_hosts": []any{"example.com"},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://example.com"},
			map[string]any{"action": "set_checked", "locator": map[string]any{"text": "Notifications", "exact": true, "som_only": true}, "checked": false},
			map[string]any{"action": "select_option", "locator": map[string]any{"text": "Tier", "exact": true, "som_only": true}, "value": "paid"},
			map[string]any{"action": "set_temporal", "locator": map[string]any{"text": "Schedule date", "exact": true, "som_only": true}, "value": "2027-01-01"},
			map[string]any{"action": "extract", "items": "h1", "fields": map[string]any{"title": map[string]any{"type": "text"}}, "readability": false},
		}, "output_schema": map[string]any{},
	})
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
	seen := map[string]bool{}
	for _, call := range plat.callsSnapshot() {
		switch call.args["action"] {
		case "set_checked", "select_option", "set_temporal":
			action := call.args["action"].(string)
			seen[action] = true
			if call.args["target_id"] == nil || call.args["som_revision"] != "form-1" || call.args["coordinate"] != nil {
				t.Fatalf("unguarded action: %#v", call.args)
			}
			if action == "set_checked" && call.args["checked"] != false {
				t.Fatalf("false lost: %#v", call.args)
			}
		}
		if call.tool == "browser_extract" && call.args["readability"] == false {
			seen["raw_extract"] = true
		}
	}
	if len(seen) != 4 {
		t.Fatalf("actions=%#v", seen)
	}
}

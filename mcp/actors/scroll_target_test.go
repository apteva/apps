package main

import "testing"

func TestScrollForwardsObservedRegionAndIdentityGuards(t *testing.T) {
	for _, targeted := range []bool{false, true} {
		name := "legacy"
		if targeted {
			name = "targeted"
		}
		t.Run(name, func(t *testing.T) {
			plat := newFakePlatform()
			observed := false
			plat.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool != "computer.computer_use" {
					return nil
				}
				if args["action"] == "screenshot" && args["include_som"] == true {
					observed = true
					return map[string]any{"current_url": "https://example.com", "som_revision": "scroll-1", "scroll_regions": []map[string]any{{"id": "scroll_document", "name": "Document", "role": "document"}, {"id": "scroll_editor", "name": "Composer", "role": "textbox"}}}
				}
				if args["action"] == "scroll" && targeted && (!observed || args["som_revision"] != "scroll-1") {
					return map[string]any{"success": false}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			step := map[string]any{"action": "scroll", "direction": "down", "amount": 450}
			if targeted {
				step["target_id"] = "{{region}}"
				step["expected_name"] = "Document"
				step["expected_role"] = "document"
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, step}})
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"region": "scroll_document"}}); err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, app)
			if run["status"] != "completed" {
				t.Fatalf("run failed: %v", run)
			}
			found := false
			for _, call := range plat.callsSnapshot() {
				if call.args["action"] != "scroll" {
					continue
				}
				found = true
				if call.args["direction"] != "down" || call.args["amount"] != 450 {
					t.Fatalf("scroll changed: %#v", call.args)
				}
				if targeted {
					if call.args["target_id"] != "scroll_document" || call.args["expected_name"] != "Document" || call.args["expected_role"] != "document" || call.args["som_revision"] != "scroll-1" {
						t.Fatalf("region guards were dropped: %#v", call.args)
					}
				} else if call.args["target_id"] != nil || call.args["expected_name"] != nil || call.args["expected_role"] != nil {
					t.Fatalf("legacy scroll gained an inferred target: %#v", call.args)
				}
			}
			if !found {
				t.Fatal("scroll was not dispatched")
			}
		})
	}
}

func TestScrollRejectsMissingOrChangedFreshRegionBeforeDispatch(t *testing.T) {
	for _, region := range []map[string]any{
		{"id": "other_region", "name": "Document", "role": "document"},
		{"id": "scroll_document", "name": "Unexpected", "role": "document"},
		{"id": "scroll_document", "name": "Document", "role": "textbox"},
	} {
		plat := newFakePlatform()
		plat.readViewsResponse = func(tool string, args map[string]any) map[string]any {
			if tool == "computer.computer_use" && args["action"] == "screenshot" {
				return map[string]any{"current_url": "https://example.com", "som_revision": "fresh", "scroll_regions": []map[string]any{region}}
			}
			return nil
		}
		ctx, app := newTestCtx(t, plat)
		rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "scroll", "direction": "down", "target_id": "scroll_document", "expected_name": "Document", "expected_role": "document"}}})
		if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
			t.Fatal(err)
		}
		if run := executeNext(t, ctx, app); run["status"] != "failed" {
			t.Fatalf("changed or absent region accepted: %v", run)
		}
		for _, call := range plat.callsSnapshot() {
			if call.args["action"] == "scroll" {
				t.Fatal("scroll dispatched before validating its fresh region")
			}
		}
	}
}

func TestScrollRejectsUnboundIdentityGuards(t *testing.T) {
	for _, step := range []map[string]any{
		{"action": "scroll", "direction": "down", "expected_name": "Document"},
		{"action": "scroll", "direction": "down", "expected_role": "document"},
		{"action": "key", "key": "End", "target_id": "scroll_document"},
	} {
		ctx, app := newTestCtx(t, newFakePlatform())
		_, err := app.toolActorSave(ctx, map[string]any{"name": "invalid", "definition": map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{step}}})
		if err == nil {
			t.Fatalf("accepted unbound or ignored scroll fields: %#v", step)
		}
	}
}

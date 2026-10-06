package main

import (
	"context"
	"testing"
)

func TestMediaWaitGuardsPublication(t *testing.T) {
	for _, scenario := range []struct {
		name      string
		out       map[string]any
		completed bool
	}{
		{"ready", map[string]any{"matched": true, "timed_out": false, "media_embed_status": "loaded", "media_provider": "example.com", "media_iframe_src": "https://example.com/player"}, true},
		{"timeout", map[string]any{"matched": false, "timed_out": true}, false},
		{"rejected", map[string]any{"matched": false, "timed_out": true, "media_embed_status": "error"}, false},
		{"missing_result", map[string]any{}, false},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			plat := newFakePlatform()
			plat.waitOutput = scenario.out
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, map[string]any{
				"schema_version": 1, "allowed_hosts": []any{"example.com"},
				"limits": map[string]any{"step_retries": 0},
				"steps": []any{
					map[string]any{"action": "goto", "url": "https://example.com/new"},
					map[string]any{"action": "wait_for", "conditions": []any{map[string]any{"type": "media_present"}}, "timeout_ms": 500},
					map[string]any{"action": "click", "locator": map[string]any{"selector": "button.publish"}},
				}, "output_schema": map[string]any{},
			})
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
			if (run["status"] == "completed") != scenario.completed {
				t.Fatalf("run=%#v", run)
			}
			published := false
			for _, call := range plat.callsSnapshot() {
				if call.args["selector"] == "button.publish" {
					published = true
				}
			}
			if published != scenario.completed {
				t.Fatalf("publish dispatched=%t", published)
			}
			if scenario.completed {
				out := run["output"].(map[string]any)
				media := out["media"].([]any)
				if len(media) != 1 || media[0].(map[string]any)["provider"] != "example.com" {
					t.Fatalf("media=%#v", media)
				}
			}
		})
	}
}

func TestWaitConditionValidation(t *testing.T) {
	for _, step := range []actorStep{
		{Action: "wait_for"},
		{Conditions: []actorWaitCondition{{Type: "unknown"}}},
		{Conditions: []actorWaitCondition{{Type: "text_present"}}},
		{Conditions: []actorWaitCondition{{Type: "selector_present"}}},
		{Conditions: []actorWaitCondition{{Type: "target_state", TargetID: "live-target"}}},
		{Conditions: []actorWaitCondition{{Type: "media_present"}}, Match: "invalid"},
	} {
		if err := validateWaitStep(step); err == nil {
			t.Fatalf("invalid step accepted: %+v", step)
		}
	}
}

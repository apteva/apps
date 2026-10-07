package main

import (
	"context"
	"strings"
	"testing"
)

func TestOnceKeyPreventsCompletedAndUncertainReplay(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "completed", true: "uncertain"}[fail], func(t *testing.T) {
			plat := newFakePlatform()
			if fail {
				plat.failAction = "click"
			}
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, map[string]any{
				"schema_version": 1, "allowed_hosts": []any{"example.com"},
				"limits": map[string]any{"step_retries": 0},
				"steps": []any{
					map[string]any{"action": "goto", "url": "https://example.com"},
					map[string]any{"action": "click", "locator": map[string]any{"selector": "button.send"}, "once_key": "request-1", "expected_effect": "message_send", "confirm_consequence": "message_send"},
				}, "output_schema": map[string]any{},
			})
			for i := 0; i < 2; i++ {
				if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
					t.Fatal(err)
				}
				run, err := claimActorRun(ctx)
				if err != nil {
					t.Fatal(err)
				}
				_ = app.executeActorRun(context.Background(), ctx, run)
				result, err := getActorRun(ctx, run.ID)
				if err != nil {
					t.Fatal(err)
				}
				if i == 1 && (!strings.Contains(result["error"].(string), "once_key already reserved") || result["status"] != "failed") {
					t.Fatalf("replay=%#v", result)
				}
			}
			clicks := 0
			for _, call := range plat.callsSnapshot() {
				if call.args["action"] == "click" {
					clicks++
				}
			}
			if clicks != 1 {
				t.Fatalf("consequential dispatches=%d", clicks)
			}
		})
	}
}

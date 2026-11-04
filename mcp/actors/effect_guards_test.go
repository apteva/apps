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
				if i == 1 && mapFromAny(mapFromAny(result["output"])["execution_progress"])["commit_attempted"] != false {
					t.Fatal("blocked duplicate reported a new commit", result)
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

func TestOnceScopeSharesGuardAcrossOperations(t *testing.T) {
	plat := newFakePlatform()
	plat.failAction = "click"
	ctx, app := newTestCtx(t, plat)
	step := map[string]any{"action": "click", "locator": map[string]any{"selector": "button.send"}, "once_key": "request-1", "once_scope": "reply", "expected_effect": "message_send", "confirm_consequence": "message_send"}
	steps := []any{map[string]any{"action": "goto", "url": "https://example.com"}, step}
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 0}, "operations": map[string]any{"initial_send": map[string]any{"steps": steps}, "reply": map[string]any{"steps": steps}}})
	for _, operation := range []string{"initial_send", "reply"} {
		if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": operation}); err != nil {
			t.Fatal(err)
		}
		run, err := claimActorRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		app.executeActorRun(context.Background(), ctx, run)
		result, err := getActorRun(ctx, run.ID)
		if err != nil {
			t.Fatal(err)
		}
		if operation == "reply" && !strings.Contains(result["error"].(string), "once_key already reserved") {
			t.Fatal(result["error"])
		}
	}
	clicks := 0
	for _, call := range plat.callsSnapshot() {
		if call.args["action"] == "click" {
			clicks++
		}
	}
	if clicks != 1 {
		t.Fatalf("dispatched %d sends", clicks)
	}
}

func TestOnceNavigationHasNoIrreversibleAcknowledgement(t *testing.T) {
	plat := newFakePlatform()
	ctx, app := newTestCtx(t, plat)
	step := map[string]any{"action": "click", "locator": map[string]any{"selector": "button.upload"}, "once_key": "upload-1", "expected_effect": "navigation_only"}
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, step}})
	for i := 0; i < 2; i++ {
		if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
			t.Fatal(err)
		}
		run, err := claimActorRun(ctx)
		if err != nil {
			t.Fatal(err)
		}
		app.executeActorRun(context.Background(), ctx, run)
		result, _ := getActorRun(ctx, run.ID)
		if i == 0 && result["status"] != "completed" {
			t.Fatal(result["error"])
		}
		if i == 1 && !strings.Contains(result["error"].(string), "once_key already reserved") {
			t.Fatal(result)
		}
	}
	clicks := 0
	for _, call := range plat.callsSnapshot() {
		if call.args["action"] == "click" {
			clicks++
			if call.args["confirm_consequence"] != "" && call.args["confirm_consequence"] != nil {
				t.Fatal(call.args)
			}
		}
	}
	if clicks != 1 {
		t.Fatal(clicks)
	}
	for _, effect := range []string{"navigation_only", "message_send"} {
		d := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "click", Locator: actorLocator{Selector: "button"}, OnceKey: "key", ExpectedEffect: effect, ConfirmConsequence: "navigation_only"}}}
		if validateActorDefinition(d) == nil {
			t.Fatal("accepted invalid acknowledgement", effect)
		}
	}
}

package main

import (
	"testing"
)

func TestNativeReadPersistsOnlyConfirmedField(t *testing.T) {
	for _, mode := range []string{"good", "missing_receipt", "failed", "missing_value"} {
		t.Run(mode, func(t *testing.T) {
			p := nativeFake()
			respond := p.readViewsResponse
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				out := respond(tool, in)
				if tool == "computer.browser_open" {
					caps := out["capabilities"].(map[string]any)
					caps["actions"] = append(caps["actions"].([]string), "native_read")
				}
				if tool == "computer.computer_use" && in["action"] == "native_read" {
					if mapFromAny(in["native_values"])["header"] != "test-account" {
						t.Fatal("field guards not forwarded", in)
					}
					r := map[string]any{"success": true, "native_value": "Test bio"}
					if mode == "missing_receipt" {
						delete(r, "success")
					}
					if mode == "failed" {
						r["success"] = false
					}
					if mode == "missing_value" {
						delete(r, "native_value")
					}
					return r
				}
				return out
			}
			ctx, app := newTestCtx(t, p)
			def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, map[string]any{"action": "native_read", "native_app": "com.example", "native_required": []string{"Bio"}, "native_values": map[string]any{"header": "test-account"}, "native_target": "bio", "verified_field": "bio"})
			def["read_only"] = true
			rec := saveFixtureActor(t, ctx, app, def)
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, app)
			if (run["status"] == "completed") != (mode == "good") {
				t.Fatal(run)
			}
			var count int
			ctx.AppDB().QueryRow(`SELECT count(*) FROM actors_dataset_items`).Scan(&count)
			if (count == 1) != (mode == "good") {
				t.Fatal("unconfirmed native value persisted", count)
			}
		})
	}
}

func TestNativeClearRequiresGuardedTargetAndNoText(t *testing.T) {
	good := actorStep{Action: "native_clear", NativeApp: "com.example", NativeRequired: []string{"Bio"}, NativeTarget: "bio"}
	if err := validateScreenStep(good, false); err != nil {
		t.Fatal(err)
	}
	bad := good
	bad.Text = "unexpected"
	if err := validateScreenStep(bad, false); err == nil {
		t.Fatal("clear accepted input text")
	}
	bad = good
	bad.NativeTarget = ""
	if err := validateScreenStep(bad, false); err == nil {
		t.Fatal("clear accepted missing target")
	}
	bad = good
	bad.NativeRequired = nil
	if err := validateScreenStep(bad, false); err == nil {
		t.Fatal("clear accepted missing screen guard")
	}
}

func TestNativeClearAcceptedAsGuardedMobileOperation(t *testing.T) {
	def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, map[string]any{"action": "native_clear", "native_app": "com.example", "native_required": []string{"Bio"}, "native_target": "bio"})
	if _, _, err := decodeActorDefinition(def); err != nil {
		t.Fatal(err)
	}
}

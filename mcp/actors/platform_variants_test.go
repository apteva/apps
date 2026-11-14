package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

func variantFixture() map[string]any {
	return map[string]any{
		"schema_version": 1,
		"limits":         map[string]any{"step_retries": 0},
		"browser":        map[string]any{"backend": "mobile", "context_id": "ctx-ios"},
		"presets":        map[string]any{"iphone": map[string]any{"platform": "ios"}, "samsung": map[string]any{"platform": "android"}},
		"operations": map[string]any{"open_app": map[string]any{
			"variants": map[string]any{
				"ios": map[string]any{"steps": []any{map[string]any{"action": "open"}, map[string]any{"action": "launch_app", "app_id": "{{ios_app}}"}}},
				"android": map[string]any{
					"browser": map[string]any{"backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "android:samsung", "platform": "android", "mode": "screen"}},
					"steps":   []any{map[string]any{"action": "open"}, map[string]any{"action": "launch_app", "app_id": "com.instagram.android"}},
				},
			},
		}},
	}
}

func variantFake() *fakePlatform {
	p := newFakePlatform()
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		switch tool {
		case "computer.computer_context_get":
			return map[string]any{"found": true, "context": map[string]any{"backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "ios:iphone", "platform": "ios", "mode": "screen"}}}
		case "computer.browser_open":
			return map[string]any{"session_id": "mobile-test", "backend": "mobile", "target": in["target"], "capabilities": map[string]any{"target_kind": "mobile", "coordinate_space": "screen_pixels", "observations": []string{"screenshot"}, "actions": []string{"screenshot", "wait", "launch_app"}, "keys": []string{"home"}}}
		}
		return nil
	}
	return p
}

func TestPlatformVariantSelectionAndPinnedSnapshot(t *testing.T) {
	for _, mode := range []string{"input", "preset", "context", "target"} {
		t.Run(mode, func(t *testing.T) {
			p := variantFake()
			ctx, app := newTestCtx(t, p)
			def := variantFixture()
			args := map[string]any{"operation": "open_app"}
			want := "android"
			switch mode {
			case "input":
				args["input"] = map[string]any{"platform": "android"}
			case "preset":
				args["preset"] = "samsung"
			case "context":
				want = "ios"
				args["input"] = map[string]any{"ios_app": "com.burbn.instagram"}
			case "target":
				def["browser"] = map[string]any{"backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "android:samsung", "mode": "screen"}}
				def["operations"].(map[string]any)["open_app"].(map[string]any)["variants"].(map[string]any)["ios"].(map[string]any)["browser"] = map[string]any{"backend": "mobile", "context_id": "ctx-ios"}
			}
			rec := saveFixtureActor(t, ctx, app, def)
			args["actor_id"] = rec.ID
			queued, err := app.toolActorRun(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil || run == nil {
				t.Fatal(err)
			}
			var snapshot actorDefinition
			if err := json.Unmarshal([]byte(run.DefinitionSnapshot), &snapshot); err != nil {
				t.Fatal(err)
			}
			if len(snapshot.Variants) != 0 || snapshot.SelectedPlatform != want || snapshot.Browser.Target.Platform != want {
				t.Fatalf("variant/device not pinned: %v", run.DefinitionSnapshot)
			}
			if got := actorQueueKey(run); got != "mobile-device:"+snapshot.Browser.Target.DeviceID {
				t.Fatal(got)
			}
			// A definition edit after enqueue cannot change the accepted implementation.
			def["operations"].(map[string]any)["open_app"] = map[string]any{"steps": []any{map[string]any{"action": "open"}}}
			if _, err := app.toolActorSave(ctx, map[string]any{"id": rec.ID, "name": rec.Name, "expected_revision": rec.Revision, "definition": def}); err != nil {
				t.Fatal(err)
			}
			if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			result, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if result["status"] != "completed" || mapFromAny(result["output"])["platform"] != want {
				t.Fatal(result)
			}
			for _, call := range p.callsSnapshot() {
				if call.args["action"] == "launch_app" {
					appID := "com.instagram.android"
					if want == "ios" {
						appID = "com.burbn.instagram"
					}
					if call.args["app_id"] != appID {
						t.Fatal(call.args)
					}
				}
			}
		})
	}
}

func TestPlatformVariantRejectsUnsupportedWithoutOpeningDevice(t *testing.T) {
	for _, platform := range []any{"web", "windows", "", true, nil} {
		p := variantFake()
		ctx, app := newTestCtx(t, p)
		rec := saveFixtureActor(t, ctx, app, variantFixture())
		if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "open_app", "input": map[string]any{"platform": platform}}); err == nil {
			t.Fatalf("accepted %v", platform)
		}
		if countCalls(p, "computer", "browser_open") != 0 {
			t.Fatal("device opened before platform validation")
		}
		var queued int
		ctx.AppDB().QueryRow(`SELECT count(*) FROM actors_runs`).Scan(&queued)
		if queued != 0 {
			t.Fatal("unsupported platform queued")
		}
	}
}

func TestPlatformVariantValidationAndPolicies(t *testing.T) {
	for _, mode := range []string{"unknown", "mixed_steps", "wrong_device", "read_only_write", "recovery", "missing_steps"} {
		t.Run(mode, func(t *testing.T) {
			def := variantFixture()
			op := def["operations"].(map[string]any)["open_app"].(map[string]any)
			variants := op["variants"].(map[string]any)
			switch mode {
			case "unknown":
				variants["windows"] = variants["android"]
			case "mixed_steps":
				op["steps"] = []any{map[string]any{"action": "wait"}}
			case "wrong_device":
				variants["android"].(map[string]any)["browser"] = map[string]any{"backend": "mobile", "device_id": "ios:wrong"}
			case "read_only_write":
				op["read_only"] = true
			case "recovery":
				op["recovery_only"] = true
			case "missing_steps":
				variants["android"].(map[string]any)["steps"] = []any{}
			}
			if _, _, err := decodeActorDefinition(def); err == nil {
				t.Fatal("invalid variant definition accepted")
			}
		})
	}
}

func TestPlatformVariantDiscoveryAndTask(t *testing.T) {
	ctx, app := newTestCtx(t, variantFake())
	rec := saveFixtureActor(t, ctx, app, variantFixture())
	got, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "open_app"})
	if err != nil {
		t.Fatal(err)
	}
	m := got.(map[string]any)
	if len(m["platforms"].([]string)) != 2 || m["input_requirements_by_platform"] == nil {
		t.Fatal(got)
	}
	got, err = app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "open_app", "platform": "android"})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(got)
	if strings.Contains(string(b), "ios_app") {
		t.Fatalf("unselected implementation leaked into contract: %s", b)
	}
	export, err := app.toolActorDefinitionExport(ctx, map[string]any{"id": rec.ID, "revision": rec.Revision, "operation": "open_app", "platform": "android"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(export.(map[string]any)["content_base64"].(string))
	if strings.Contains(string(raw), "ios_app") {
		t.Fatal("export lost platform selector")
	}
	task, err := app.toolTaskSave(ctx, map[string]any{"name": "Android launch", "actor_id": rec.ID, "operation": "open_app", "preset": "samsung"})
	if err != nil {
		t.Fatal(err)
	}
	taskID := task.(map[string]any)["task"].(actorTask).ID
	if _, err = app.toolTaskRun(ctx, map[string]any{"id": taskID}); err != nil {
		t.Fatal(err)
	}
	run, _ := claimActorRun(ctx)
	if !strings.Contains(run.DefinitionSnapshot, `"selected_platform":"android"`) {
		t.Fatal(run.DefinitionSnapshot)
	}
}

func TestPlatformVariantDeviceChangeAndIdempotency(t *testing.T) {
	p := variantFake()
	ctx, app := newTestCtx(t, p)
	rec := saveFixtureActor(t, ctx, app, variantFixture())
	args := map[string]any{"actor_id": rec.ID, "operation": "open_app", "input": map[string]any{"platform": "ios", "ios_app": "com.burbn.instagram"}, "idempotency_key": "same-request"}
	first, err := app.toolActorRun(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolActorRun(ctx, args)
	if err != nil || second.(map[string]any)["duplicate"] != true || first.(map[string]any)["run_id"] != second.(map[string]any)["run_id"] {
		t.Fatalf("idempotency lost: %v %v", second, err)
	}
	args["input"] = map[string]any{"platform": "android"}
	if _, err = app.toolActorRun(ctx, args); err == nil || !strings.Contains(err.Error(), "idempotency conflict") {
		t.Fatal("platform change reused idempotency key", err)
	}
	oldResponse := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.computer_context_get" {
			return map[string]any{"found": true, "context": map[string]any{"backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "android:other", "platform": "android", "mode": "screen"}}}
		}
		return oldResponse(tool, in)
	}
	run, _ := claimActorRun(ctx)
	_ = app.executeActorRun(context.Background(), ctx, run)
	result, _ := getActorRun(ctx, run.ID)
	if result["status"] != "failed" || countCalls(p, "computer", "browser_open") != 0 {
		t.Fatalf("retargeted context was opened: %v", result)
	}
}

func TestPlatformVariantWebAndImplicitOperation(t *testing.T) {
	p := newFakePlatform()
	ctx, app := newTestCtx(t, p)
	def := map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "browser": map[string]any{"backend": "local"}, "variants": map[string]any{
		"web": map[string]any{"read_only": true, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}}},
	}}
	rec := saveFixtureActor(t, ctx, app, def)
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
		t.Fatal(err)
	}
	run, _ := claimActorRun(ctx)
	if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	result, _ := getActorRun(ctx, run.ID)
	if result["status"] != "completed" || mapFromAny(result["output"])["platform"] != "web" {
		t.Fatal(result)
	}
	got, err := app.toolActorGet(ctx, map[string]any{"id": rec.ID, "operation": "run", "platform": "web"})
	if err != nil || got.(map[string]any)["step_count"] != 1 {
		t.Fatal(got, err)
	}
}

func TestPlatformVariantCannotWeakenSharedPolicy(t *testing.T) {
	d := actorDefinition{ReadOnly: true, PrivateScreen: true, Variants: map[string]actorPlatformVariant{"android": {}}}
	selected, err := selectActorVariant(d, "android")
	if err != nil || !selected.ReadOnly || !selected.PrivateScreen {
		t.Fatal("variant weakened shared safety policy")
	}
}

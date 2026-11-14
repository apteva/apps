package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func mobileFixture(contextID, device string, steps ...map[string]any) map[string]any {
	b := map[string]any{"backend": "mobile"}
	if contextID != "" {
		b["context_id"] = contextID
	} else {
		b["target"] = map[string]any{"kind": "mobile", "device_id": device, "mode": "screen"}
	}
	return map[string]any{"schema_version": 1, "browser": b, "allowed_hosts": []string{"example.com"}, "limits": map[string]any{"max_duration_seconds": 30, "step_retries": 2}, "steps": steps}
}
func mobileFake() *fakePlatform {
	p := newFakePlatform()
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		switch tool {
		case "computer.computer_context_get":
			return map[string]any{"found": true, "context": map[string]any{"backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "phone", "platform": "ios", "mode": "screen"}}}
		case "computer.browser_open":
			return map[string]any{"session_id": "phone-session", "backend": "mobile", "target": map[string]any{"kind": "mobile", "device_id": "phone"}, "capabilities": map[string]any{"target_kind": "mobile", "coordinate_space": "screen_pixels", "observations": []string{"screenshot"}, "actions": []string{"screenshot", "navigate", "click", "swipe", "type", "key", "scroll", "wait", "home", "launch_app", "stop_app"}, "keys": []string{"home", "enter"}}}
		}
		return nil
	}
	return p
}
func TestMobileScreenWorkflow(t *testing.T) {
	p := mobileFake()
	ctx, a := newTestCtx(t, p)
	def := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, map[string]any{"action": "launch_app", "app_id": "com.apple.mobilesafari"}, map[string]any{"action": "goto", "url": "https://example.com/"}, map[string]any{"action": "tap", "x": 0, "y": 200}, map[string]any{"action": "swipe", "x": 100, "y": 500, "to_x": 100, "to_y": 200}, map[string]any{"action": "type", "text": "test"}, map[string]any{"action": "key", "key": "enter"}, map[string]any{"action": "scroll", "direction": "down", "amount": 100}, map[string]any{"action": "screenshot"}, map[string]any{"action": "home"}, map[string]any{"action": "stop_app", "app_id": "com.apple.mobilesafari"})
	rec := saveFixtureActor(t, ctx, a, def)
	out, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["run_id"].(int64)
	run, err := claimActorRun(ctx)
	if err != nil || run == nil {
		t.Fatalf("claim %v %v", run, err)
	}
	if err = a.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	result := queueTestWait(t, ctx, id, "completed")
	_ = result
	var actions []string
	for _, call := range p.calls {
		if call.tool == "browser_open" {
			if call.args["url"] != "" || call.args["persist"] != true || intFromAny(call.args["timeout"]) < 30 {
				t.Fatalf("unsafe mobile open: %v", call.args)
			}
		}
		if call.tool == "computer_use" {
			actions = append(actions, stringFromAny(call.args["action"]))
			for _, key := range []string{"selector", "target_id", "label", "expected_effect", "confirm_consequence"} {
				if _, ok := call.args[key]; ok {
					t.Fatalf("semantic argument %s on Mobile", key)
				}
			}
			if call.args["action"] == "click" && call.args["coordinate"] != "0,200" {
				t.Fatalf("coordinate: %v", call.args)
			}
		}
	}
	if strings.Join(actions, ",") != "launch_app,navigate,click,swipe,type,key,scroll,home,stop_app" {
		t.Fatalf("actions %v", actions)
	}
	var locks int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM actors_context_locks`).Scan(&locks)
	if locks != 0 {
		t.Fatalf("leaked %d locks", locks)
	}
}
func TestMobileQueueAliasesShareDevice(t *testing.T) {
	p := mobileFake()
	ctx, a := newTestCtx(t, p)
	enqueue := func(c, d string) int64 {
		t.Helper()
		rec := saveFixtureActor(t, ctx, a, mobileFixture(c, d, map[string]any{"action": "open"}, map[string]any{"action": "screenshot"}))
		out, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)["run_id"].(int64)
	}
	first := enqueue("ctx-one", "")
	second := enqueue("ctx-two", "")
	third := enqueue("", "another-phone")
	r, err := claimActorRun(ctx)
	if err != nil || r == nil || r.ID != first {
		t.Fatalf("first claim %v %v", r, err)
	}
	r, err = claimActorRun(ctx)
	if err != nil || r == nil || r.ID != third {
		t.Fatalf("independent claim %v %v", r, err)
	}
	pending, _ := getActorRun(ctx, second)
	if pending["wait_reason"] != "session_lock" {
		t.Fatalf("waiting %v", pending)
	}
	r, err = claimActorRun(ctx)
	if err != nil || r != nil {
		t.Fatalf("same phone concurrently claimed %v %v", r, err)
	}
	ctx.AppDB().Exec(`UPDATE actors_runs SET status='completed' WHERE id IN (?,?)`, first, third)
	ctx.AppDB().Exec(`INSERT INTO actors_context_locks(project_id,context_id,run_id,expires_at,cleanup_pending) VALUES(?,?,?,?,1)`, projectID(ctx), "mobile-device:phone", first, time.Now().Add(time.Minute).Unix())
	r, err = claimActorRun(ctx)
	if err != nil || r != nil {
		t.Fatalf("quarantined device claimed %v %v", r, err)
	}
	ctx.AppDB().Exec(`DELETE FROM actors_context_locks`)
	r, err = claimActorRun(ctx)
	if err != nil || r == nil || r.ID != second {
		t.Fatalf("released device %v %v", r, err)
	}
}
func TestMobileRejectsUnsupportedBeforeInput(t *testing.T) {
	for _, action := range []string{"extract", "upload_file", "click", "wait_for"} {
		t.Run(action, func(t *testing.T) {
			var def actorDefinition
			b, _ := json.Marshal(mobileFixture("", "phone", map[string]any{"action": "goto", "url": "https://example.com/"}, map[string]any{"action": action}))
			json.Unmarshal(b, &def)
			if err := validateActorDefinition(def); err == nil || !strings.Contains(err.Error(), "Mobile does not support") {
				t.Fatalf("validation %v", err)
			}
		})
	}
	p := mobileFake()
	original := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		r := original(tool, in)
		if tool == "computer.browser_open" {
			r["capabilities"].(map[string]any)["keys"] = []string{"home"}
		}
		return r
	}
	ctx, a := newTestCtx(t, p)
	rec := saveFixtureActor(t, ctx, a, mobileFixture("", "phone", map[string]any{"action": "goto", "url": "https://example.com/"}, map[string]any{"action": "key", "key": "enter"}))
	_, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := claimActorRun(ctx)
	if err = a.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	result := queueTestWait(t, ctx, run.ID, "failed")
	if !strings.Contains(stringFromAny(result["error"]), "key") {
		t.Fatalf("failure %v", result)
	}
	for _, call := range p.calls {
		if call.tool == "computer_use" {
			t.Fatalf("input before capability preflight: %v", call)
		}
	}
}
func TestMobileCoordinatesValidate(t *testing.T) {
	for _, v := range []any{nil, -1, 1.5, "oops", 10001} {
		if _, err := screenCoordinate(v, false); err == nil {
			t.Fatalf("accepted %v", v)
		}
	}
	if n, err := screenCoordinate(float64(0), false); err != nil || n != 0 {
		t.Fatal(n, err)
	}
	if _, err := screenCoordinate("{{x}}", true); err != nil {
		t.Fatal(err)
	}
}

func TestMobileNativeInputAndFailureReconciliation(t *testing.T) {
	for name, failure := range map[string]error{"timeout": context.DeadlineExceeded, "backend": errors.New("backend transport failed; input may have been dispatched")} {
		t.Run(name, func(t *testing.T) {
			p := mobileFake()
			ctx, a := newTestCtx(t, p)
			def := mobileFixture("", "phone", map[string]any{"action": "open"}, map[string]any{"action": "type", "text": "local test"}, map[string]any{"action": "tap", "x": 100, "y": 200})
			def["allowed_hosts"] = []string{}
			p.actionErrors = map[string][]error{"click": {failure}}
			rec := saveFixtureActor(t, ctx, a, def)
			_, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			if err = a.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			result := queueTestWait(t, ctx, run.ID, "failed")
			if mapFromAny(result["output"])["reconciliation_required"] != true {
				t.Fatalf("lost uncertainty: %v", result)
			}
			count := 0
			for _, c := range p.callsSnapshot() {
				if c.tool == "computer_use" && c.args["action"] == "click" {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("tap dispatched %d times", count)
			}
			if _, err = a.toolActorRunRetry(ctx, map[string]any{"run_id": run.ID, "id": run.ID}); err == nil {
				t.Fatal("uncertain mobile input retried without reconciliation")
			}
		})
	}
}
func TestMobileRejectsChangedQueuedTarget(t *testing.T) {
	p := mobileFake()
	ctx, a := newTestCtx(t, p)
	rec := saveFixtureActor(t, ctx, a, mobileFixture("ctx-mobile", "", map[string]any{"action": "open"}, map[string]any{"action": "screenshot"}))
	_, err := a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	original := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		out := original(tool, in)
		if tool == "computer.computer_context_get" {
			out["context"].(map[string]any)["target"].(map[string]any)["device_id"] = "different-phone"
		}
		return out
	}
	run, _ := claimActorRun(ctx)
	if err = a.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	result := queueTestWait(t, ctx, run.ID, "failed")
	if !strings.Contains(stringFromAny(result["error"]), "target changed") {
		t.Fatalf("failure: %v", result)
	}
	if countCalls(p, "computer", "browser_open") != 0 {
		t.Fatal("changed device opened")
	}
}

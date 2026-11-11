package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// nativeFake is a Mobile session whose Computer supports native screen input
// and reports the saved context it opened.
func nativeFake() *fakePlatform {
	p := mobileFake()
	respond := p.readViewsResponse
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		out := respond(tool, in)
		if tool == "computer.browser_open" {
			caps := out["capabilities"].(map[string]any)
			caps["actions"] = append(caps["actions"].([]string), "native_tap", "native_tap_badged", "native_tap_if_present", "native_assert", "native_wait", "native_type", "native_type_secure", "native_replace")
			out["app_context_id"] = in["context_id"]
		}
		return out
	}
	return p
}

func privateNativeFixture(steps ...map[string]any) map[string]any {
	def := mobileFixture("ctx-phone", "", append([]map[string]any{{"action": "open"}}, steps...)...)
	def["allowed_hosts"] = []string{}
	def["private_screen"] = true
	return def
}

func nativeSecretConfig(ref, value, app string) tk.Option {
	entry := actorBrowserSecret{Value: value, ContextID: "ctx-phone", NativeApp: app, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	return tk.WithConfig(map[string]string{"browser_secrets": string(mustJSON(map[string]map[string]actorBrowserSecret{"p": {ref: entry}}))})
}

func TestNativeSecretInputUsesOnlyBoundSecret(t *testing.T) {
	const password = "fixture-secret-value"
	markers := []string{"Sign in", "Password"}
	for _, boundApp := range []string{"ExampleApp", "OtherApp"} {
		t.Run(boundApp, func(t *testing.T) {
			p := nativeFake()
			ctx, a := newTestCtx(t, p, nativeSecretConfig("ref", password, boundApp))
			ctx = ctx.WithProject("p")
			actor := saveFixtureActor(t, ctx, a, privateNativeFixture(
				map[string]any{"action": "native_type_secret", "native_app": "ExampleApp", "native_required": markers, "native_target": "Password", "secret_ref": "ref"},
				map[string]any{"action": "native_tap", "native_app": "ExampleApp", "native_required": markers, "native_target": "Continue"},
			))
			if _, err := a.toolActorRun(ctx, map[string]any{"actor_id": actor.ID}); err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, a)
			var actions []string
			for _, call := range p.callsSnapshot() {
				if call.tool != "computer_use" {
					continue
				}
				action := stringFromAny(call.args["action"])
				actions = append(actions, action)
				if action == "native_type_secure" && (call.args["text"] != password || call.args["native_target"] != "Password" || call.args["observation"] != "none" || call.args["native_app"] != "ExampleApp") {
					t.Fatalf("secret sent outside its bound field: %v", call.args)
				}
			}
			if boundApp == "ExampleApp" {
				if run["status"] != "completed" || !reflect.DeepEqual(actions, []string{"native_type_secure", "native_tap"}) {
					t.Fatalf("status=%v actions=%v error=%v", run["status"], actions, run["error"])
				}
			} else if run["status"] != "failed" || len(actions) != 0 {
				t.Fatalf("secret bound to another app dispatched input: %v", actions)
			}
			if strings.Contains(string(mustJSON(run)), password) || countCalls(p, "computer", "browser_screenshot") != 0 || p.lastCall("computer", "browser_open")["extraction_only"] != true {
				t.Fatal("private run retained secret-bearing output")
			}
		})
	}
}

func TestNativePrivateKeypadInput(t *testing.T) {
	const code = "287364"
	markers := []string{"Enter the code", "Code entry"}
	p := nativeFake()
	ctx, a := newTestCtx(t, p, nativeSecretConfig("code", code, "ExampleApp"))
	ctx = ctx.WithProject("p")
	actor := saveFixtureActor(t, ctx, a, privateNativeFixture(
		map[string]any{"action": "native_type_private", "mode": "keypad", "native_app": "ExampleApp", "native_required": markers, "secret_ref": "code"},
	))
	if _, err := a.toolActorRun(ctx, map[string]any{"actor_id": actor.ID}); err != nil {
		t.Fatal(err)
	}
	if run := executeNext(t, ctx, a); run["status"] != "completed" || strings.Contains(string(mustJSON(run)), code) {
		t.Fatalf("keypad run: %v %v", run["status"], run["error"])
	}
	var digits strings.Builder
	for _, call := range p.callsSnapshot() {
		if call.tool != "computer_use" {
			continue
		}
		if call.args["action"] != "native_tap" || call.args["observation"] != "none" || !reflect.DeepEqual(call.args["native_required"], markers) {
			t.Fatalf("unguarded keypad tap: %v", call.args)
		}
		digits.WriteString(stringFromAny(call.args["native_target"]))
	}
	if digits.String() != code || countCalls(p, "computer", "browser_screenshot") != 0 {
		t.Fatalf("keypad received %q", digits.String())
	}
}

func TestNativeScreenValidation(t *testing.T) {
	markers := []string{"Ready"}
	cases := map[string]map[string]any{
		"missing markers":          privateNativeFixture(map[string]any{"action": "native_tap", "native_app": "ExampleApp", "native_target": "Next"}),
		"missing target":           privateNativeFixture(map[string]any{"action": "native_tap", "native_app": "ExampleApp", "native_required": markers}),
		"multi-line text":          privateNativeFixture(map[string]any{"action": "native_type", "native_app": "ExampleApp", "native_required": markers, "native_target": "Name", "text": "a\nb"}),
		"literal secret":           privateNativeFixture(map[string]any{"action": "native_type_secret", "native_app": "ExampleApp", "native_required": markers, "native_target": "Password", "secret_ref": "ref", "text": "plain"}),
		"screenshot while private": privateNativeFixture(map[string]any{"action": "screenshot"}),
	}
	public := mobileFixture("ctx-phone", "", map[string]any{"action": "open"}, map[string]any{"action": "native_type_secret", "native_app": "ExampleApp", "native_required": markers, "native_target": "Password", "secret_ref": "ref"})
	public["allowed_hosts"] = []string{}
	cases["secret without private_screen"] = public
	for name, raw := range cases {
		var def actorDefinition
		if err := json.Unmarshal(mustJSON(raw), &def); err != nil {
			t.Fatal(err)
		}
		if err := validateActorDefinition(def); err == nil {
			t.Errorf("%s accepted", name)
		}
	}
	var valid actorDefinition
	json.Unmarshal(mustJSON(privateNativeFixture(map[string]any{"action": "native_assert", "native_app": "ExampleApp", "native_required": markers})), &valid)
	if err := validateActorDefinition(valid); err != nil {
		t.Fatal(err)
	}
	// A secret bound to a native app is never usable for browser input.
	entry := actorBrowserSecret{Value: "v", ContextID: "ctx", Host: "example.com", NativeApp: "ExampleApp", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	raw := string(mustJSON(map[string]map[string]actorBrowserSecret{"p": {"ref": entry}}))
	if _, err := resolveActorBrowserSecret(raw, "p", "ref", "ctx", "https://example.com/login", time.Now()); err == nil {
		t.Fatal("native-bound secret resolved for a browser host")
	}
}

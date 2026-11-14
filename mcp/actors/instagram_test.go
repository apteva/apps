package main

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func instagramFixture(t *testing.T) (map[string]any, *fakePlatform) {
	t.Helper()
	raw, err := os.ReadFile("examples/instagram-iphone.json")
	if err != nil {
		t.Fatal(err)
	}
	var definition map[string]any
	if err := json.Unmarshal(raw, &definition); err != nil {
		t.Fatal(err)
	}
	definition["browser"] = map[string]any{"backend": "mobile", "context_id": "ctx-phone"}
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
	return definition, p
}

func TestInstagramDisableProfileSuggestions(t *testing.T) {
	const editURL = "https://www.instagram.com/accounts/edit/"
	for _, tc := range []struct {
		name, target, signedIn          string
		alreadyOff, ambiguous, loseSave bool
		wantStatus                      string
		wantWrites                      int
	}{
		{name: "save and verify", target: "brand.test", signedIn: "brand.test", wantStatus: "completed", wantWrites: 2},
		{name: "already off", target: "brand.test", signedIn: "brand.test", alreadyOff: true, wantStatus: "completed", wantWrites: 1},
		{name: "save did not persist", target: "brand.test", signedIn: "brand.test", loseSave: true, wantStatus: "failed", wantWrites: 2},
		{name: "wrong login", target: "brand.test", signedIn: "marcoschwartz", wantStatus: "failed"},
		{name: "protected profile", target: "marcoschwartz", signedIn: "marcoschwartz", wantStatus: "failed"},
		{name: "ambiguous control", target: "brand.test", signedIn: "brand.test", ambiguous: true, wantStatus: "failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			definition, _ := instagramFixture(t)
			p := newFakePlatform()
			savedOff, formOff := tc.alreadyOff, tc.alreadyOff
			reads, writes := 0, 0
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					if in["backend"] != "local" || in["context_id"] != "ctx-brand-web" || in["extraction_only"] != true {
						t.Fatalf("unexpected browser binding: %v", in)
					}
				case "computer.browser_extract":
					control := fmt.Sprintf(`<button role="switch" aria-checked="%t"></button>`, !formOff)
					if tc.ambiguous {
						control += `<input type="checkbox">`
					}
					return map[string]any{"html": `<main><span>Show account suggestions on profiles</span>` + control + `</main>`, "current_url": editURL, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "wait_for":
						conditions := string(mustJSON(in["conditions"]))
						if strings.Contains(conditions, "Profile saved") {
							return map[string]any{"current_url": editURL, "matched": savedOff, "timed_out": !savedOff}
						}
						reads++
						if !strings.Contains(conditions, "/"+tc.target+"/") || in["match"] != "all" {
							t.Fatalf("missing exact account check: %v", in)
						}
						matched := tc.target == tc.signedIn
						if reads == 2 {
							if !strings.Contains(conditions, "selector_absent") || !strings.Contains(conditions, ":not(:checked)") {
								t.Fatalf("missing unchecked-state verification: %v", in)
							}
							matched = matched && formOff
						}
						return map[string]any{"current_url": editURL, "matched": matched, "timed_out": !matched}
					case "set_checked":
						writes++
						if in["checked"] != false || !strings.Contains(stringFromAny(in["selector"]), "/"+tc.target+"/") {
							t.Fatalf("unexpected or unscoped switch action: %v", in)
						}
						formOff = true
					case "screenshot":
						return map[string]any{"current_url": editURL, "som_revision": "edit-1", "som": []map[string]any{
							{"id": "submit", "text": "Submit", "role": "button", "disabled": tc.alreadyOff},
						}}
					case "click":
						writes++
						if in["target_id"] != "submit" || in["expected_text"] != "Submit" || !formOff {
							t.Fatalf("unexpected settings submission: %v", in)
						}
						if !tc.loseSave {
							savedOff = formOff
						}
					case "navigate":
						formOff = savedOff
					}
				}
				return nil
			}
			ctx, a := newTestCtx(t, p)
			actor := saveFixtureActor(t, ctx, a, definition)
			_, err := a.toolActorRun(ctx, map[string]any{
				"actor_id": actor.ID, "operation": "disable_profile_suggestions",
				"input": map[string]any{"target_handle": tc.target, "web_context_id": "ctx-brand-web"},
			})
			if err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, a)
			if run["status"] != tc.wantStatus || writes != tc.wantWrites {
				t.Fatalf("status=%v, writes=%d, error=%v", run["status"], writes, run["error"])
			}
			if tc.name == "protected profile" && countCalls(p, "computer", "browser_open") != 0 {
				t.Fatal("opened a browser for the protected account")
			}
		})
	}
}

func TestInstagramPhotoPreparationStopsAtReview(t *testing.T) {
	definition, p := instagramFixture(t)
	ctx, a := newTestCtx(t, p)
	actor := saveFixtureActor(t, ctx, a, definition)
	_, err := a.toolActorRun(ctx, map[string]any{
		"actor_id": actor.ID, "operation": "prepare_photo_post",
	})
	if err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, a)
	if run["status"] != "completed" {
		t.Fatalf("photo preparation: %v", run["error"])
	}
	var actions []string
	for _, call := range p.callsSnapshot() {
		if call.tool != "computer_use" {
			continue
		}
		action := stringFromAny(call.args["action"])
		actions = append(actions, action)
		switch action {
		case "native_tap":
			if call.args["native_target"] != "next-button" || !reflect.DeepEqual(call.args["native_required"], []string{"filter-button", "edit-button", "aspect-ratio-button"}) {
				t.Fatalf("unexpected editor input: %v", call.args)
			}
		case "native_assert":
			if !reflect.DeepEqual(call.args["native_required"], []string{"New post", "caption-cell-text-view", "share-button"}) {
				t.Fatalf("review was not verified: %v", call.args)
			}
		}
	}
	if !reflect.DeepEqual(actions, []string{"native_tap", "wait", "native_assert"}) {
		t.Fatalf("unexpected input sequence %v", actions)
	}
}

func TestInstagramSignupHandsOffAtTheFirstForm(t *testing.T) {
	definition, p := instagramFixture(t)
	ctx, a := newTestCtx(t, p)
	actor := saveFixtureActor(t, ctx, a, definition)
	input := map[string]any{
		"signup_entry_marker": "Log in",
		"signup_button":       "Create new account",
		"signup_form_marker":  "What’s your mobile number?",
	}
	_, err := a.toolActorRun(ctx, map[string]any{
		"actor_id": actor.ID, "operation": "begin_signup", "input": input,
	})
	if err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, a)
	if run["status"] != "completed" {
		t.Fatalf("signup handoff: %v", run["error"])
	}
	if p.lastCall("computer", "browser_open")["extraction_only"] != true ||
		countCalls(p, "computer", "browser_screenshot") != 0 {
		t.Fatal("private signup captured a screenshot")
	}

	var actions []string
	for _, call := range p.callsSnapshot() {
		if call.tool != "computer_use" {
			continue
		}
		action := stringFromAny(call.args["action"])
		actions = append(actions, action)
		if action == "native_tap" && (call.args["native_target"] != "Create new account" ||
			!reflect.DeepEqual(call.args["native_required"], []string{"Log in"})) {
			t.Fatalf("signup opened from an unverified screen: %v", call.args)
		}
	}
	if !reflect.DeepEqual(actions, []string{"native_tap", "wait", "native_assert"}) {
		t.Fatalf("unexpected signup input sequence %v", actions)
	}
}

func TestInstagramSignInUsesOnlyBoundSecret(t *testing.T) {
	const password = "fixture-secret-value"
	for _, appLabel := range []string{"Instagram", "Other"} {
		t.Run(appLabel, func(t *testing.T) {
			definition, p := instagramFixture(t)
			entry := actorBrowserSecret{Value: password, ContextID: "ctx-phone", NativeApp: appLabel, ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
			config := mustJSON(map[string]map[string]actorBrowserSecret{"p": {"ref": entry}})
			ctx, a := newTestCtx(t, p, tk.WithConfig(map[string]string{"browser_secrets": string(config)}))
			ctx = ctx.WithProject("p")
			actor := saveFixtureActor(t, ctx, a, definition)
			_, err := a.toolActorRun(ctx, map[string]any{
				"actor_id": actor.ID, "operation": "sign_in", "input": map[string]any{"username": "fixture", "password_ref": "ref"},
			})
			if err != nil {
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
				if action == "native_type_secure" && (call.args["text"] != password || call.args["native_target"] != "Password" || call.args["observation"] != "none") {
					t.Fatal("secret was sent outside the intended field or with observation")
				}
				if action == "native_tap" && call.args["native_target"] != "Log in" {
					t.Fatal("unexpected sign-in tap")
				}
			}
			if appLabel == "Instagram" {
				if run["status"] != "completed" || !reflect.DeepEqual(actions, []string{"native_type_secure", "native_tap"}) {
					t.Fatalf("expected guarded login dispatch, got %v, %v", run["status"], actions)
				}
			} else if run["status"] != "failed" || len(actions) != 0 {
				t.Fatalf("cross-app reference dispatched input: %v", actions)
			}
			if strings.Contains(string(mustJSON(run)), password) || countCalls(p, "computer", "browser_screenshot") != 0 ||
				p.lastCall("computer", "browser_open")["extraction_only"] != true {
				t.Fatal("sign-in retained secret-bearing output")
			}
		})
	}
}

func TestInstagramLinkedSignupAndAccountSwitch(t *testing.T) {
	for _, tc := range []struct {
		operation, action, target string
		input                     map[string]any
	}{
		{"begin_linked_signup", "native_tap", "Create New Account", nil},
		{"set_linked_signup_username", "native_replace", "Username", map[string]any{"username": "apteva.test"}},
		{"choose_linked_signup_method", "native_tap", "Use mobile number or email address", map[string]any{"signup_method": "Use mobile number or email address"}},
		{"create_linked_account", "native_tap", "I agree", map[string]any{"confirm_creation": "create_instagram_account"}},
		{"decline_signup_optional_cookies", "native_tap", "Decline optional cookies", nil},
		{"open_current_profile", "native_tap", "profile-tab", map[string]any{"handle": "apteva.test"}},
		{"switch_account", "native_tap_badged", "INSTAGRAM profile, apteva.test", map[string]any{"target_handle": "apteva.test"}},
	} {
		t.Run(tc.operation, func(t *testing.T) {
			definition, p := instagramFixture(t)
			ctx, a := newTestCtx(t, p)
			actor := saveFixtureActor(t, ctx, a, definition)
			_, err := a.toolActorRun(ctx, map[string]any{"actor_id": actor.ID, "operation": tc.operation, "input": tc.input})
			if err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, a)
			if run["status"] != "completed" {
				t.Fatalf("operation failed: %v", run["error"])
			}
			inputCount := 0
			for _, call := range p.callsSnapshot() {
				if call.tool != "computer_use" || !screenInputAction(stringFromAny(call.args["action"])) {
					continue
				}
				inputCount++
				if call.args["action"] != tc.action || call.args["native_target"] != tc.target {
					t.Fatalf("unexpected action or target: %v", call.args)
				}
			}
			if inputCount != 1 || countCalls(p, "computer", "browser_screenshot") != 0 ||
				p.lastCall("computer", "browser_open")["extraction_only"] != true {
				t.Fatal("expected one guarded input and a private session")
			}
			if tc.operation == "switch_account" {
				check := p.lastCall("computer", "computer_use")
				if check["action"] != "native_wait" || check["duration"] != 15000 ||
					!reflect.DeepEqual(check["native_required"], []string{"apteva.test", "Edit profile", "user-switch-title-button"}) {
					t.Fatalf("expected a bounded wait for the target profile: %v", check)
				}
			}
		})
	}
}

func TestInstagramPrivateEmailCodeInput(t *testing.T) {
	const code = "287364"
	definition, p := instagramFixture(t)
	definition["operations"].(map[string]any)["enter_fixture_code"] = map[string]any{
		"private_screen": true,
		"steps": []map[string]any{
			{"action": "open"},
			{
				"action": "native_type_private", "mode": "keypad", "native_app": "Instagram",
				"native_required": []string{"Enter the confirmation code", "Code input entry field"}, "secret_ref": "signup-code",
			},
		},
	}
	entry := actorBrowserSecret{Value: code, ContextID: "ctx-phone", NativeApp: "Instagram", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
	config := mustJSON(map[string]map[string]actorBrowserSecret{"p": {"signup-code": entry}})
	ctx, a := newTestCtx(t, p, tk.WithConfig(map[string]string{"browser_secrets": string(config)}))
	ctx = ctx.WithProject("p")
	actor := saveFixtureActor(t, ctx, a, definition)
	if _, err := a.toolActorRun(ctx, map[string]any{"actor_id": actor.ID, "operation": "enter_fixture_code"}); err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, a)
	if run["status"] != "completed" {
		t.Fatal("private code entry failed")
	}
	var digits strings.Builder
	for _, call := range p.callsSnapshot() {
		if call.tool == "computer_use" {
			if call.args["action"] != "native_tap" || call.args["observation"] != "none" ||
				!reflect.DeepEqual(call.args["native_required"], []string{"Enter the confirmation code", "Code input entry field"}) {
				t.Fatal("a private keypad tap lacked the screen guard")
			}
			digits.WriteString(stringFromAny(call.args["native_target"]))
		}
	}
	if digits.String() != code {
		t.Fatal("the keypad did not receive exactly the expected digits")
	}
	if strings.Contains(string(mustJSON(run)), code) || countCalls(p, "computer", "browser_screenshot") != 0 {
		t.Fatal("the code was retained in run output")
	}
}

func TestInstagramCreateAndSwitchBack(t *testing.T) {
	definition, p := instagramFixture(t)
	ctx, a := newTestCtx(t, p)
	actor := saveFixtureActor(t, ctx, a, definition)
	_, err := a.toolActorRun(ctx, map[string]any{
		"actor_id": actor.ID, "operation": "create_and_switch_back",
		"input": map[string]any{
			"source_handle": "original", "username": "apteva.test", "confirm_creation": "create_in_accounts_centre",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := executeNext(t, ctx, a)
	if run["status"] != "completed" {
		t.Fatalf("create and switch back: %v", run["error"])
	}
	var controls []string
	for _, call := range p.callsSnapshot() {
		if call.tool != "computer_use" {
			continue
		}
		if target := stringFromAny(call.args["native_target"]); target != "" {
			controls = append(controls, target)
		}
		if call.args["action"] == "scroll" && call.args["observation"] != "none" {
			t.Fatal("account switcher scroll requested a screenshot")
		}
	}
	want := []string{"user-switch-title-button", "Add Instagram Account", "Create New Account", "Username", "Next", "Allow and continue", "I agree", "Decline optional cookies", "profile-tab", "user-switch-title-button", "INSTAGRAM profile, original"}
	if !reflect.DeepEqual(controls, want) || countCalls(p, "computer", "browser_open") != 1 || countCalls(p, "computer", "browser_screenshot") != 0 {
		t.Fatalf("unexpected create and switch sequence: %v", controls)
	}
}

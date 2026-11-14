package main

import (
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"strings"
	"testing"
	"time"
)

func TestInstagramAndroidLoginRequiresPrivateBoundConfirmedInput(t *testing.T) {
	const password = "fake-android-login-secret"
	for _, mode := range []string{"valid", "wrong_context", "wrong_app", "expired", "missing_receipt", "rejected_receipt"} {
		t.Run(mode, func(t *testing.T) {
			raw, err := os.ReadFile("examples/instagram.json")
			if err != nil {
				t.Fatal(err)
			}
			var definition map[string]any
			if json.Unmarshal(raw, &definition) != nil {
				t.Fatal("invalid example")
			}
			p := nativeFake()
			respond := p.readViewsResponse
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				out := respond(tool, in)
				if tool == "computer.computer_context_get" {
					out["context"].(map[string]any)["target"].(map[string]any)["platform"] = "android"
				}
				if tool == "computer.computer_use" && in["action"] == "native_type_secure" {
					if in["password"] != password || in["text"] != nil || in["observation"] != "none" || mapFromAny(in["native_values"])["Username, email or mobile number,"] != "fixture.test" {
						t.Fatal("unsafe password dispatch")
					}
					if mode == "missing_receipt" {
						return map[string]any{}
					}
					if mode == "rejected_receipt" {
						return map[string]any{"success": false}
					}
				}
				return out
			}
			entry := actorBrowserSecret{Value: password, ContextID: "ctx-phone", NativeApp: "com.instagram.android", ExpiresAt: time.Now().Add(time.Hour).Format(time.RFC3339)}
			if mode == "wrong_context" {
				entry.ContextID = "ctx-other"
			}
			if mode == "wrong_app" {
				entry.NativeApp = "Instagram"
			}
			if mode == "expired" {
				entry.ExpiresAt = time.Now().Add(-time.Hour).Format(time.RFC3339)
			}
			ctx, a := newTestCtx(t, p, tk.WithConfig(map[string]string{"browser_secrets": string(mustJSON(map[string]map[string]actorBrowserSecret{"p": {"ref": entry}}))}))
			ctx = ctx.WithProject("p")
			rec := saveFixtureActor(t, ctx, a, definition)
			if _, err = a.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "sign_in", "preset": "android", "input": map[string]any{"username": "fixture.test", "password_ref": "ref", "android_context_id": "ctx-phone"}}); err != nil {
				t.Fatal(err)
			}
			run := executeNext(t, ctx, a)
			if (run["status"] == "completed") != (mode == "valid") {
				t.Fatalf("unexpected outcome %v", run["status"])
			}
			submits := 0
			secure := 0
			for _, call := range p.callsSnapshot() {
				if call.tool == "computer_use" {
					if call.args["action"] == "native_tap" {
						submits++
					}
					if call.args["action"] == "native_type_secure" {
						secure++
					}
				}
			}
			if mode != "valid" && submits != 0 {
				t.Fatal("failed secret input submitted login")
			}
			if (mode == "wrong_context" || mode == "wrong_app" || mode == "expired") && secure != 0 {
				t.Fatal("unbound secret sent")
			}
			if strings.Contains(string(mustJSON(run)), password) || countCalls(p, "computer", "browser_screenshot") != 0 || p.lastCall("computer", "browser_open")["extraction_only"] != true {
				t.Fatal("private login leaked evidence")
			}
		})
	}
}

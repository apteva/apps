package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Exercise the exact reviewed production definition against rendered page
// fixtures. No real service or credentials are used.
func TestAdultfolioServiceInterruptionGuard(t *testing.T) {
	path := os.Getenv("ADULTFOLIO_OUTAGE_DEFINITION")
	if path == "" {
		t.Skip("operator definition fixture not supplied")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"outage_banner", "outage_heading", "healthy_quoted_outage", "healthy_reply", "wrong_account", "missing_thread", "reply_outage", "precommit_outage"} {
		t.Run(mode, func(t *testing.T) {
			var def map[string]any
			if err := json.Unmarshal(raw, &def); err != nil {
				t.Fatal(err)
			}
			def["browser"].(map[string]any)["context_id"] = ""
			current := ""
			composer := ""
			commits := 0
			reads := 0
			plat := newFakePlatform()
			plat.waitOutput = map[string]any{"matched": true, "timed_out": false}
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "set_text":
						composer = "Thanks for clarifying."
						return map[string]any{"current_url": current}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "send", "tag": "button", "accessible_name": "Send!", "effect": "message_send"}}}
					case "click":
						commits++
						return map[string]any{"current_url": current}
					}
				case "computer.browser_extract":
					reads++
					outage := mode == "outage_banner" || mode == "outage_heading" || mode == "reply_outage" || (mode == "precommit_outage" && composer != "")
					if outage {
						h := `<body><div>TEMPORARY SERVICE INTERRUPTION</div><h1>We'll be back shortly.</h1><p>We're experiencing a temporary technical issue.</p><button>Try again</button></body>`
						if mode == "outage_heading" {
							h = `<body><h1>We'll be back shortly.</h1></body>`
						}
						return map[string]any{"html": h, "current_url": current, "rendered": true}
					}
					owner := "owner"
					if mode == "wrong_account" {
						owner = "other"
					}
					thread := `id="MessageResult" class="conversation-42"`
					if mode == "missing_thread" {
						thread = `id="MissingResult"`
					}
					h := `<body><div class="navbar"><a href="/` + owner + `">Your Profile</a></div><div class="MainTitle"><a class="a4" href="/model">Model</a></div><div class="note-editable panel-body" contenteditable="true">` + composer + `</div><div ` + thread + `><div id="10" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/model"></a><div class="message-modern-meta"><span class="timeago" title="2026-10-31T08:00:00Z"></span></div><div class="message-modern-body">I saw TEMPORARY SERVICE INTERRUPTION and we'll be back shortly yesterday.</div></div><span id="end"></span><hr></div></body>`
					if commits > 0 {
						h = strings.Replace(h, `<span id="end">`, `<div id="11" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/owner"></a><div class="message-modern-meta"><span class="timeago" title="2026-10-31T08:01:00Z"></span></div><div class="message-modern-body">Thanks for clarifying.</div></div><span id="end">`, 1)
					}
					h = strings.Replace(h, `</body>`, `<button class="message_button_class" type="button" onclick="sendMessageReply()">Send!</button></body>`, 1)
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, def)
			operation := "get_thread"
			if mode == "healthy_reply" || mode == "reply_outage" || mode == "precommit_outage" {
				operation = "reply"
			}
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": operation, "input": map[string]any{"account_url": "https://www.adultfolio.com/owner", "account_profile_path": "/owner", "model_url": "https://www.adultfolio.com/model", "model_id": "42", "message": "Thanks for clarifying.", "request_id": "outage-test-" + mode, "max_duration_seconds": 60, "max_pages": 8, "max_items": 100, "read_pages": 2}})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			result, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			failure := stringFromAny(result["error"])
			if mode == "healthy_quoted_outage" || mode == "healthy_reply" {
				if result["status"] != "completed" {
					t.Fatalf("healthy conversation blocked: %s", failure)
				}
			} else if strings.Contains(mode, "outage") {
				if !strings.Contains(failure, "platform_service_interruption_markers") {
					t.Fatalf("outage misclassified: %s", failure)
				}
			} else if result["status"] != "failed" {
				t.Fatalf("identity/thread guard lost: %v", result)
			}
			wantCommits := 0
			if mode == "healthy_reply" {
				wantCommits = 1
			}
			if commits != wantCommits {
				t.Fatalf("dispatched %d messages, want %d", commits, wantCommits)
			}
			if reads == 0 {
				t.Fatal("did not inspect rendered DOM")
			}
		})
	}
}

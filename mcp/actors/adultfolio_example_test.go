package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

func TestEditableReplyExamplePrecommitAndPersistence(t *testing.T) {
	for _, mode := range []string{"success", "wrong_account", "wrong_recipient", "wrong_composer", "unchanged_id", "wrong_sender", "missing_saved_body"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			current := ""
			composer := ""
			commits := 0
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
						composer = "<p>Hello</p><p>World</p>"
						if mode == "wrong_composer" {
							composer = "<p>hello</p><p>World</p>"
						}
						return map[string]any{"current_url": current}
					case "screenshot":
						return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "send", "tag": "button", "accessible_name": "Send!", "effect": "message_send"}}}
					case "click":
						if in["expected_effect"] != "message_send" || in["confirm_consequence"] != "message_send" || in["target_id"] != "send" || in["coordinate"] != nil {
							t.Fatal("unguarded send")
						}
						commits++
						return map[string]any{"current_url": current}
					}
				case "computer.browser_extract":
					account := "owner"
					recipient := "model"
					if mode == "wrong_account" {
						account = "other"
					}
					if mode == "wrong_recipient" {
						recipient = "other"
					}
					h := `<body><div class="navbar"><a href="/` + account + `">Your Profile</a></div><div class="MainTitle"><a class="a4" href="/` + recipient + `">Model</a></div><div class="note-editable panel-body" contenteditable="true">` + composer + `</div><div id="MessageResult" class="conversation-42"><div id="10" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/model"></a><div class="message-modern-meta"><span class="timeago" title="2026-10-10T00:00:00Z"></span></div><div class="message-modern-body">Previous</div></div>`
					if commits > 0 {
						id := "11"
						sender := "owner"
						body := "<p>Hello</p><p>World</p>"
						if mode == "unchanged_id" {
							id = "10"
						}
						if mode == "wrong_sender" {
							sender = "model"
						}
						if mode == "missing_saved_body" {
							body = "Previous"
						}
						h += `<div id="` + id + `" class="messageContainer message-modern-wrap"><a class="thumbnailPic" href="/` + sender + `"></a><div class="message-modern-meta"><span class="timeago" title="2026-10-10T00:01:00Z"></span></div><div class="message-modern-body">` + body + `</div></div>`
					}
					return map[string]any{"html": h + `</div></body>`, "current_url": current, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			raw, err := os.ReadFile("examples/adultfolio-control.json")
			if err != nil {
				t.Fatal(err)
			}
			var def map[string]any
			json.Unmarshal(raw, &def)
			def["browser"].(map[string]any)["context_id"] = ""
			rec := saveFixtureActor(t, ctx, app, def)
			input := map[string]any{"account_url": "https://www.adultfolio.com/owner", "model_url": "https://www.adultfolio.com/model", "model_id": "42", "message": "Hello\nWorld", "request_id": "reply-test", "max_duration_seconds": 60}
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "reply", "input": input})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if (r["status"] == "completed") != (mode == "success") {
				t.Fatalf("%s: %v", mode, r["error"])
			}
			want := 1
			if mode == "wrong_account" || mode == "wrong_recipient" || mode == "wrong_composer" {
				want = 0
			}
			if commits != want {
				t.Fatalf("commits=%d want=%d", commits, want)
			}
			if mode == "success" {
				queued, err = app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "operation": "reply", "input": input})
				if err != nil {
					t.Fatal(err)
				}
				run, _ = claimActorRun(ctx)
				app.executeActorRun(context.Background(), ctx, run)
				r, _ = getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
				if r["status"] != "failed" || commits != 1 {
					t.Fatal("duplicate send allowed")
				}
			}
		})
	}
}

package main

import (
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestExplicitEmailFormattingAndSenderRecovery(t *testing.T) {
	for _, scenario := range []string{"legacy", "edited body", "plain text source", "existing sender", "malformed metadata", "null metadata", "SQL null metadata", "invalid sender", "default flags", "concurrent metadata edit", "sender only"} {
		t.Run(scenario, func(t *testing.T) {
			raw := `<p>First paragraph.</p>` + strings.Repeat("<div>", 60) + `<p>Second paragraph.</p>` + strings.Repeat("</div>", 60)
			platform := &bodyRecoveryPlatform{idempotentMessagingPlatform: newIdempotentMessagingPlatform(), message: map[string]any{
				"id": int64(31414), "project_id": "test-proj", "channel": "email", "direction": "in", "from": "Sam <sam@example.test>",
				"subject": "Example", "body_html": raw, "message_id_header": "<original@example.test>",
			}}
			ctx := newTestCtx(t, tk.WithPlatform(platform))
			first, err := ingestInbound(ctx, "test-proj", inboundPayload{MatchedRecipient: "inbox@example.test", MessageID: 31414, Channel: channelEmail, From: "sam@example.test", Subject: "Example", MessageIDHeader: "<original@example.test>"})
			if err != nil {
				t.Fatal(err)
			}
			stored := "Example\n\n" + legacyPlainTextFromHTML(raw)
			detail := `{"to":["inbox@example.test"],"private":{"audit":"keep"}}`
			switch scenario {
			case "edited body":
				stored = "Operator edited content\n\n\nkeep spacing"
			case "plain text source":
				platform.message["body_text"] = "Original plain text" // Never replace a complete body from a different source representation.
			case "existing sender":
				detail = `{"from":"existing@example.test","private":{"audit":"keep"}}`
			case "malformed metadata":
				detail = `{not-json`
			case "null metadata":
				detail = `null`
			case "invalid sender":
				platform.message["from"] = "not-an-email"
			case "sender only":
				stored = "Already complete body"
			}
			if _, err = ctx.AppDB().Exec(`UPDATE contact_activities SET body=?,source_detail=?,messaging_install_id=0 WHERE id=?`, stored, detail, first["activity_id"]); err != nil {
				t.Fatal(err)
			}
			if scenario == "SQL null metadata" {
				ctx.AppDB().Exec(`UPDATE contact_activities SET source_detail=NULL WHERE id=?`, first["activity_id"])
				detail = ""
			}
			if _, err = ctx.AppDB().Exec(`UPDATE contact_conversations SET status='pending',priority='high' WHERE id=?`, first["conversation_id"]); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"id": first["contact_id"], "activity_id": first["activity_id"], "normalize_formatting": true, "recover_sender": true}
			if scenario == "default flags" {
				delete(args, "normalize_formatting")
				delete(args, "recover_sender")
			}
			app := &App{}
			preview, err := app.toolRefreshMessageBody(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			var after, beforeDetail string
			ctx.AppDB().QueryRow(`SELECT body,COALESCE(source_detail,'') FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&after, &beforeDetail)
			if after != stored || beforeDetail != detail || preview.(map[string]any)["dry_run"] != true {
				t.Fatal("preview modified data")
			}
			if scenario == "concurrent metadata edit" {
				platform.onRead = func() {
					ctx.AppDB().Exec(`UPDATE contact_activities SET source_detail='{"private":"concurrent"}' WHERE id=?`, first["activity_id"])
				}
			}
			var eventsBefore int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox`).Scan(&eventsBefore)
			args["dry_run"] = false
			result, err := app.toolRefreshMessageBody(ctx, args)
			if scenario == "concurrent metadata edit" {
				if err == nil {
					t.Fatal("concurrent metadata overwritten")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			out := result.(map[string]any)
			var afterDetail, status, priority string
			ctx.AppDB().QueryRow(`SELECT body,COALESCE(source_detail,'') FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&after, &afterDetail)
			formatExpected := scenario != "edited body" && scenario != "plain text source" && scenario != "default flags" && scenario != "sender only"
			if out["formatting_repaired"] != formatExpected {
				t.Fatalf("formatting result=%#v", out)
			}
			if formatExpected {
				if after != inboundActivityBody(inboundPayload{Channel: channelEmail, Subject: "Example", BodyHTML: raw}) || strings.Contains(after, "\n\n\n") {
					t.Fatalf("bad recovered formatting %q", after)
				}
			} else if after != stored {
				t.Fatal("complete/edited content was changed")
			}
			senderExpected := scenario != "existing sender" && scenario != "malformed metadata" && scenario != "null metadata" && scenario != "invalid sender" && scenario != "default flags"
			if out["sender_repaired"] != senderExpected {
				t.Fatalf("sender result=%#v", out)
			}
			if senderExpected {
				var original, current map[string]any
				json.Unmarshal([]byte(detail), &original)
				json.Unmarshal([]byte(afterDetail), &current)
				if current["from"] != "sam@example.test" {
					t.Fatal("sender not recovered from original")
				}
				delete(current, "from")
				want, _ := json.Marshal(original)
				got, _ := json.Marshal(current)
				if scenario != "SQL null metadata" && string(want) != string(got) {
					t.Fatal("private source metadata was lost")
				}
			} else if afterDetail != detail {
				t.Fatal("existing or malformed metadata was overwritten")
			}
			ctx.AppDB().QueryRow(`SELECT status,priority FROM contact_conversations WHERE id=?`, first["conversation_id"]).Scan(&status, &priority)
			var activities, conversations, eventsAfter int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE messaging_id=31414`).Scan(&activities)
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_conversations`).Scan(&conversations)
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox`).Scan(&eventsAfter)
			if activities != 1 || conversations != 1 || eventsAfter != eventsBefore || status != "pending" || priority != "high" {
				t.Fatal("recovery changed workflow/records")
			}
			repeat, err := app.toolRefreshMessageBody(ctx, args)
			if err != nil || repeat.(map[string]any)["recoverable"] != false {
				t.Fatalf("recovery not idempotent: %#v %v", repeat, err)
			}
		})
	}
}

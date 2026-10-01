package main

import (
	"encoding/json"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const testInboundHTML = `<!DOCTYPE html><html><head><title>Hidden title</title><style>hidden-css</style></head><body><p>Hello &amp; team<br>Full email content.</p><script>hidden-script</script></body></html>`

func TestInboundHTMLBodyRetryRepairsOnlyMissingContent(t *testing.T) {
	ctx := newTestCtx(t)
	body := inboundPayload{MessageID: 31898, Channel: channelEmail, From: "sender@example.test",
		Subject: "RE: Example", BodyText: " \r\n\t", BodyHTML: testInboundHTML, MessageIDHeader: "<original@example.test>"}
	first, err := ingestInbound(ctx, "test-proj", body)
	if err != nil {
		t.Fatal(err)
	}
	activityID := first["activity_id"]
	var fullBody string
	if err := ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, activityID).Scan(&fullBody); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(fullBody, "Hello & team") || !strings.Contains(fullBody, "Full email content.") || strings.Contains(fullBody, "hidden-") || strings.Contains(fullBody, "Hidden title") {
		t.Fatalf("HTML-only content was lost or unsafe content retained: %q", fullBody)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET body=? WHERE id=?`, body.Subject+"\n\n", activityID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_conversations SET status='pending',priority='high' WHERE id=?`, first["conversation_id"]); err != nil {
		t.Fatal(err)
	}
	var eventsBefore int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox`).Scan(&eventsBefore)
	second, err := ingestInbound(ctx, "test-proj", body)
	if err != nil || second["deduped"] != true || second["body_repaired"] != true || second["activity_id"] != activityID || second["conversation_id"] != first["conversation_id"] {
		t.Fatalf("retry did not repair the existing activity: %#v err=%v", second, err)
	}
	var repaired, status, priority string
	ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, activityID).Scan(&repaired)
	ctx.AppDB().QueryRow(`SELECT status,priority FROM contact_conversations WHERE id=?`, first["conversation_id"]).Scan(&status, &priority)
	if repaired != fullBody || status != "pending" || priority != "high" {
		t.Fatalf("retry changed content or workflow state: body=%q status=%s priority=%s", repaired, status, priority)
	}
	// A different retry body cannot replace complete content.
	body.BodyText = "Changed provider text"
	third, err := ingestInbound(ctx, "test-proj", body)
	if err != nil || third["body_repaired"] != false {
		t.Fatalf("complete body was replaced: %#v err=%v", third, err)
	}
	var activities, conversations, eventsAfter int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE messaging_id=?`, body.MessageID).Scan(&activities)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_conversations`).Scan(&conversations)
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox`).Scan(&eventsAfter)
	ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, activityID).Scan(&repaired)
	if activities != 1 || conversations != 1 || eventsAfter != eventsBefore || repaired != fullBody {
		t.Fatalf("retry duplicated or changed existing data: activities=%d conversations=%d events=%d/%d body=%q", activities, conversations, eventsBefore, eventsAfter, repaired)
	}
}

type bodyRecoveryPlatform struct {
	*idempotentMessagingPlatform
	message map[string]any
	onRead  func()
}

func (p *bodyRecoveryPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if tool != "message_get" {
		return p.idempotentMessagingPlatform.CallAppResult(app, tool, input, out)
	}
	if p.onRead != nil {
		p.onRead()
	}
	raw, err := json.Marshal(map[string]any{"message": p.message})
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func TestRefreshMessageBodyValidatesOriginalAndPreservesExistingData(t *testing.T) {
	for _, scenario := range []string{"legacy recovery", "complete body", "different project", "different header", "different source", "concurrent edit"} {
		t.Run(scenario, func(t *testing.T) {
			platform := &bodyRecoveryPlatform{idempotentMessagingPlatform: newIdempotentMessagingPlatform(), message: map[string]any{
				"id": int64(31414), "project_id": "test-proj", "channel": "email", "direction": "in",
				"subject": "RE: Example", "body_html": testInboundHTML, "message_id_header": "<original@example.test>",
			}}
			ctx := newTestCtx(t, tk.WithPlatform(platform))
			first, err := ingestInbound(ctx, "test-proj", inboundPayload{MessageID: 31414, Channel: channelEmail,
				From: "sender@example.test", Subject: "RE: Example", MessageIDHeader: "<original@example.test>"})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"id": first["contact_id"], "activity_id": first["activity_id"]}
			var before string
			ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&before)
			if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET messaging_install_id=0 WHERE id=?`, first["activity_id"]); err != nil {
				t.Fatal(err)
			}
			switch scenario {
			case "complete body":
				before = "Existing complete content"
				ctx.AppDB().Exec(`UPDATE contact_activities SET body=? WHERE id=?`, before, first["activity_id"])
			case "different project":
				platform.message["project_id"] = "other-project"
			case "different header":
				platform.message["message_id_header"] = "<other@example.test>"
			case "different source":
				ctx.AppDB().Exec(`UPDATE contact_activities SET messaging_install_id=999 WHERE id=?`, first["activity_id"])
			}
			app := &App{}
			preview, err := app.toolRefreshMessageBody(ctx, args)
			if strings.HasPrefix(scenario, "different ") {
				if err == nil {
					t.Fatalf("recovery accepted mismatched original: %#v", preview)
				}
				return
			}
			if err != nil || preview.(map[string]any)["dry_run"] != true {
				t.Fatalf("default must preview without writing: %#v err=%v", preview, err)
			}
			var after string
			ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&after)
			if after != before {
				t.Fatal("dry run changed stored content")
			}
			if scenario == "concurrent edit" {
				platform.onRead = func() {
					if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET body='User edited content' WHERE id=?`, first["activity_id"]); err != nil {
						t.Fatal(err)
					}
				}
			}
			args["dry_run"] = false
			result, err := app.toolRefreshMessageBody(ctx, args)
			ctx.AppDB().QueryRow(`SELECT body FROM contact_activities WHERE id=?`, first["activity_id"]).Scan(&after)
			if scenario == "concurrent edit" {
				if err == nil || after != "User edited content" {
					t.Fatalf("concurrent edit was overwritten: body=%q err=%v", after, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if scenario == "complete body" {
				if result.(map[string]any)["body_repaired"] != false || after != before {
					t.Fatal("recovery overwrote a complete body")
				}
			} else if result.(map[string]any)["body_repaired"] != true || !strings.Contains(after, "Full email content.") {
				t.Fatalf("legacy recovery failed: %#v body=%q", result, after)
			}
			var count int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE messaging_id=31414`).Scan(&count)
			if count != 1 {
				t.Fatalf("recovery created %d activities", count)
			}
		})
	}
}

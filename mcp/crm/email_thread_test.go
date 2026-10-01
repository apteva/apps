package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestEmailReplyMatchesOutboundRFCMessageID(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	platform.sendResponse = map[string]any{
		"id":                  int64(1363),
		"status":              "sent",
		"provider_message_id": "ses-maclear",
		"message_id_header":   "<ses-maclear@eu-west-1.amazonses.com>",
	}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	contact := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "maclear@example.test", "is_primary": true}}})
	sent, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"id": contact.ID, "subject": "Partnership", "body": "Hello", "from": "partner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := sent.(map[string]any)["conversation_id"]
	activity := sent.(map[string]any)["activity"].(*Activity)
	if activity.MessageIDHeader != "<ses-maclear@eu-west-1.amazonses.com>" {
		t.Fatalf("outbound activity Message-ID=%q", activity.MessageIDHeader)
	}
	deduped, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"id": contact.ID, "subject": "Partnership", "body": "Hello", "from": "partner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if deduped.(map[string]any)["provider_message_id"] != "ses-maclear" || deduped.(map[string]any)["message_id_header"] != activity.MessageIDHeader {
		t.Fatalf("deduped send conflated provider and RFC IDs: %#v", deduped)
	}
	reply, err := ingestInbound(ctx, "test-proj", inboundPayload{
		Channel: "email", From: "maclear@example.test", MessageID: 1369,
		MessageIDHeader: "<maclear-reply@example.test>", InReplyTo: "<ses-maclear@eu-west-1.amazonses.com>", BodyText: "Yes",
	})
	if err != nil {
		t.Fatal(err)
	}
	if reply["conversation_id"] != conversationID {
		t.Fatalf("reply split from original conversation: sent=%v reply=%v", conversationID, reply)
	}
	regionalVariant, err := ingestInbound(ctx, "test-proj", inboundPayload{
		Channel: "email", From: "maclear@example.test", MessageID: 1370,
		MessageIDHeader: "<maclear-reply-2@example.test>", InReplyTo: "<ses-maclear@email.amazonses.com>", BodyText: "Following up",
	})
	if err != nil || regionalVariant["conversation_id"] != conversationID {
		t.Fatalf("alternate SES domain split conversation: reply=%v err=%v", regionalVariant, err)
	}
	var root string
	if err := ctx.AppDB().QueryRow(`SELECT root_message_id FROM contact_conversations WHERE id=?`, conversationID).Scan(&root); err != nil || root != "<ses-maclear@email.amazonses.com>" {
		t.Fatalf("delivered SES Message-ID did not correct root: %q err=%v", root, err)
	}
}

func TestLegacySESProviderIDMatchesOnlySameContactAndSESReference(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	platform.sendResponse = map[string]any{"id": int64(1364), "status": "sent", "provider_message_id": "ses-ventus"}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	contact := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "ventus@example.test", "is_primary": true}}})
	mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "other@example.test", "is_primary": true}}})
	sent, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"id": contact.ID, "subject": "Partnership", "body": "Hello", "from": "partner@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	conversationID := sent.(map[string]any)["conversation_id"].(int64)
	if _, err := ctx.AppDB().Exec(`UPDATE contact_conversations SET root_message_id=? WHERE id=?`, "ses-ventus", conversationID); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET message_id_header=? WHERE id=?`, "ses-ventus", sent.(map[string]any)["activity"].(*Activity).ID); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name    string
		from    string
		id      int64
		ref     string
		matches bool
	}{
		{"other domain", "ventus@example.test", 1390, "<ses-ventus@example.test>", false},
		{"other contact", "other@example.test", 1391, "<ses-ventus@eu-west-1.amazonses.com>", false},
		{"SES reference", "ventus@example.test", 1392, "<ses-ventus@eu-west-1.amazonses.com>", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			reply, err := ingestInbound(ctx, "test-proj", inboundPayload{
				Channel: "email", From: test.from, MessageID: test.id,
				MessageIDHeader: "<reply-" + test.name + "@example.test>", InReplyTo: test.ref, BodyText: "Reply",
			})
			if err != nil {
				t.Fatal(err)
			}
			if (reply["conversation_id"] == conversationID) != test.matches {
				t.Fatalf("conversation=%v, original=%d", reply["conversation_id"], conversationID)
			}
		})
	}
	var root string
	if err := ctx.AppDB().QueryRow(`SELECT root_message_id FROM contact_conversations WHERE id=?`, conversationID).Scan(&root); err != nil {
		t.Fatal(err)
	}
	if root != "<ses-ventus@eu-west-1.amazonses.com>" {
		t.Fatalf("legacy root was not repaired: %q", root)
	}
}

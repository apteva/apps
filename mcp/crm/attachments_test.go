package main

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAttachmentOnlySendForwardsAndPersistsNormalizedMetadata(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	platform.sendResponse = map[string]any{
		"id":                  int64(801),
		"status":              "sent",
		"provider_message_id": "provider-801",
		"attachments": []any{map[string]any{
			"id":                int64(901),
			"message_id":        int64(801),
			"storage_id":        int64(44),
			"filename":          "proposal.pdf",
			"content_type":      "application/pdf",
			"size_bytes":        int64(1234),
			"source":            "storage",
			"processing_status": "ready",
		}},
	}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{map[string]any{
			"kind": "phone", "value": "+12025550123", "is_primary": true,
		}},
	})

	out, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"id":      contact.ID,
		"channel": channelSMS,
		"from":    "+12025550100",
		"attachments": []any{map[string]any{
			"content_base64": "aGVsbG8=",
			"filename":       "proposal.pdf",
			"content_type":   "application/pdf",
			"size_bytes":     5,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, calls := platform.snapshot()
	var sent map[string]any
	for _, call := range calls {
		if call.Tool == "send_message" {
			sent = call.Input
			break
		}
	}
	if sent == nil || sent["attachments"] == nil {
		t.Fatalf("Messaging input did not receive attachments: %#v", sent)
	}
	activity := out.(map[string]any)["activity"].(*Activity)
	if activity.Body != "" || len(activity.Attachments) != 1 {
		t.Fatalf("activity=%#v, want attachment-only activity with one attachment", activity)
	}
	attachment := activity.Attachments[0]
	if attachment.MessagingAttachmentID != 901 || attachment.StorageID != 44 || attachment.Filename != "proposal.pdf" {
		t.Fatalf("attachment=%#v", attachment)
	}
	wantURL := "/api/apps/storage/files/44/content?project_id=test-proj"
	if attachment.DownloadURL != wantURL {
		t.Fatalf("download_url=%q, want %q", attachment.DownloadURL, wantURL)
	}

	activities, err := dbActivities(ctx.AppDB(), "test-proj", contact.ID, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 1 || len(activities[0].Attachments) != 1 {
		t.Fatalf("reloaded activities=%#v", activities)
	}
}

func TestInboundAttachmentRetryReconcilesMetadataWithoutDuplicateActivity(t *testing.T) {
	ctx := newTestCtx(t)
	payload := inboundPayload{
		MessageID:       12345,
		Channel:         channelEmail,
		From:            "sender@example.test",
		To:              []string{"inbox@example.test"},
		Subject:         "Documents",
		BodyText:        "Attached",
		MessageIDHeader: "<inbound-12345@example.test>",
		Attachments: []messagingAttachment{{
			ID: 77, StorageID: 88, Filename: "old-name.pdf", ContentType: "application/pdf", ProcessingStatus: "ready",
		}},
	}
	first, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	payload.Attachments[0].Filename = "final-name.pdf"
	second, err := ingestInbound(ctx, "test-proj", payload)
	if err != nil {
		t.Fatal(err)
	}
	if second["deduped"] != true {
		t.Fatalf("second=%#v, want deduped", second)
	}
	activityID := int64FromAny(first["activity_id"])
	activity, err := dbActivityByMessagingID(ctx.AppDB(), "test-proj", payload.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if activity == nil || activity.ID != activityID || len(activity.Attachments) != 1 || activity.Attachments[0].Filename != "final-name.pdf" {
		t.Fatalf("activity after retry=%#v", activity)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE project_id=? AND messaging_id=?`, "test-proj", payload.MessageID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("activity count=%d, want 1", count)
	}
}

func TestReplyForwardsStorageAttachmentIDs(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	platform.sendResponse = map[string]any{"id": int64(802), "status": "sent", "provider_message_id": "provider-802", "attachments": []any{}}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Bob",
		"channels": []any{map[string]any{
			"kind": "phone", "value": "+12025550124", "is_primary": true,
		}},
	})
	if _, err := ingestInbound(ctx, "test-proj", inboundPayload{
		MessageID: 700, Channel: channelSMS, From: "+12025550124", To: []string{"+12025550100"}, BodyText: "hello",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolReply(ctx, map[string]any{
		"id": contact.ID, "body": "", "from": "+12025550100", "attachment_storage_ids": []any{int64(55)},
	}); err != nil {
		t.Fatal(err)
	}
	_, calls := platform.snapshot()
	for _, call := range calls {
		if call.Tool != "send_message" {
			continue
		}
		ids, ok := call.Input["attachment_storage_ids"].([]any)
		if !ok || len(ids) != 1 || int64FromAny(ids[0]) != 55 {
			t.Fatalf("reply attachment_storage_ids=%#v", call.Input["attachment_storage_ids"])
		}
		return
	}
	t.Fatal("reply did not call Messaging send_message")
}

func TestAttachmentContentChangesAutomaticIdempotencyFingerprint(t *testing.T) {
	base := map[string]any{
		"channel": "email", "to": "alice@example.test", "from": "sender@example.test", "body": "same",
		"attachments": []any{map[string]any{"filename": "note.txt", "content_base64": "b25l"}},
	}
	other := map[string]any{}
	for key, value := range base {
		other[key] = value
	}
	other["attachments"] = []any{map[string]any{"filename": "note.txt", "content_base64": "dHdv"}}
	if outboundContentFingerprint("test-proj", 1, false, base) == outboundContentFingerprint("test-proj", 1, false, other) {
		t.Fatal("different attachment bytes produced the same outbound fingerprint")
	}
}

func TestMessageJSONLimitAllowsBase64PayloadButDefaultLimitDoesNot(t *testing.T) {
	body := []byte(`{"body":"` + strings.Repeat("a", int(maxJSONBodyBytes)+1024) + `"}`)
	request := httptest.NewRequest("POST", "/", bytes.NewReader(body))
	if _, err := mustReadJSONArgsLimit(httptest.NewRecorder(), request, maxMessageJSONBodyBytes); err != nil {
		t.Fatalf("message decoder rejected payload above generic limit: %v", err)
	}
	request = httptest.NewRequest("POST", "/", bytes.NewReader(body))
	if _, err := mustReadJSONArgs(httptest.NewRecorder(), request); err == nil {
		t.Fatal("generic decoder unexpectedly accepted payload above 1 MiB")
	}
}

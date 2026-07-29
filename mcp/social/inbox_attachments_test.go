package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func insertInstagramInboxAccount(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'instagram', 43, 'ig_1', 'Instagram', 'active', '{"access_token":"page_tok"}')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

func configureAudioStorage(pf *recordingPlatform) {
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"content_type":"audio/mpeg","name":"voice.mp3","size_bytes":1024}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"url":"https://files.example.test/voice.mp3?sig=abc"}`,
	)
}

func TestInboxReply_InstagramAudioFromStorage(t *testing.T) {
	pf := newRecordingPlatform()
	configureAudioStorage(pf)
	pf.executeResponses["send_message"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"message_id":"ig_reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	accountID := insertInstagramInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  accountID,
		Platform:         "instagram",
		Kind:             inboxKindDM,
		ExternalID:       "ig_message_1",
		ExternalPostID:   "ig_conversation_1",
		AuthorExternalID: "igsid_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (&App{}).toolInboxReply(ctx, map[string]any{
		"id":                itemID,
		"media_storage_ids": []any{int64(55)},
		"media_project_id":  "test-proj",
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(inboxOutcome)
	if out.Status != "ok" || len(out.Deliveries) != 1 || out.Deliveries[0].Kind != inboxAttachmentAudio {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "send_message" {
		t.Fatalf("unexpected integration calls: %+v", pf.executeCalls)
	}
	message := pf.executeCalls[0].Input["message"].(map[string]any)
	attachment := message["attachment"].(map[string]any)
	payload := attachment["payload"].(map[string]any)
	if attachment["type"] != "audio" || payload["url"] != "https://files.example.test/voice.mp3?sig=abc" {
		t.Fatalf("unexpected attachment payload: %+v", message)
	}
	var mediaJSON string
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(media_json,'') FROM inbox_items WHERE external_id='ig_reply_1'`,
	).Scan(&mediaJSON); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(mediaJSON, `"kind":"audio"`) || !strings.Contains(mediaJSON, `"storage_id":55`) {
		t.Fatalf("outgoing media_json = %s", mediaJSON)
	}
}

func TestInboxReply_FacebookTextAndAudioReportsPartialDelivery(t *testing.T) {
	pf := newRecordingPlatform()
	configureAudioStorage(pf)
	pf.executeQueues["facebook_send_message"] = []*sdk.ExecuteResult{
		{Success: true, Status: 200, Data: json.RawMessage(`{"message_id":"fb_text_1"}`)},
		{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"message":"unsupported audio codec"}}`)},
	}
	ctx := newSocialCtx(t, pf)
	accountID := insertFacebookInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  accountID,
		Platform:         "facebook",
		Kind:             inboxKindDM,
		ExternalID:       "fb_message_1",
		ExternalPostID:   "fb_conversation_1",
		AuthorExternalID: "psid_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	result, err := (&App{}).toolInboxReply(ctx, map[string]any{
		"id":                itemID,
		"body":              "hello back",
		"media_storage_ids": []any{int64(55)},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(inboxOutcome)
	if out.Status != "partial" || len(out.Deliveries) != 2 {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if out.Deliveries[0].Kind != "text" || out.Deliveries[0].Status != "ok" ||
		out.Deliveries[1].Kind != "audio" || out.Deliveries[1].Status != "failed" {
		t.Fatalf("unexpected deliveries: %+v", out.Deliveries)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("integration calls = %d, want 2", len(pf.executeCalls))
	}
}

func TestInboxReply_ZernioUsesCanonicalAttachmentPayload(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["send_inbox_message"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"messageId":"provider_reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	result, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status,
		    provider_slug, provider_account_id, capabilities)
		 VALUES ('test-proj', 'linkedin', 99, 'za_1', 'Company', 'active',
		         'zernio', 'za_1', '{"inbox_attachment_types":["audio"],"inbox_max_attachments":1}')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := result.LastInsertId()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  accountID,
		Platform:         "linkedin",
		Kind:             inboxKindDM,
		ExternalID:       "provider_message_1",
		ExternalPostID:   "provider_conversation_1",
		AuthorExternalID: "provider_user_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	reply, err := (&App{}).toolInboxReply(ctx, map[string]any{
		"id": itemID,
		"attachments": []any{map[string]any{
			"source": "url",
			"type":   "audio",
			"url":    "https://cdn.example.test/voice.ogg",
			"mime":   "audio/ogg",
			"name":   "voice.ogg",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := reply.(inboxOutcome)
	if out.Status != "ok" || out.ExternalID != "provider_reply_1" {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	input := pf.executeCalls[0].Input
	if input["accountId"] != "za_1" ||
		input["attachmentType"] != "audio" ||
		input["attachmentUrl"] != "https://cdn.example.test/voice.ogg" ||
		input["attachmentName"] != "voice.ogg" {
		t.Fatalf("provider attachment input = %+v", input)
	}
	var stored int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM inbox_items WHERE external_id='provider_reply_1' AND media_json LIKE '%"kind":"audio"%'`,
	).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored != 1 {
		t.Fatalf("stored provider reply count = %d", stored)
	}
}

func TestInboxReply_RejectsAttachmentForCommentsBeforeProviderCall(t *testing.T) {
	pf := newRecordingPlatform()
	configureAudioStorage(pf)
	ctx := newSocialCtx(t, pf)
	accountID := insertFacebookInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  accountID,
		Platform:         "facebook",
		Kind:             inboxKindComment,
		ExternalID:       "comment_1",
		AuthorExternalID: "psid_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := (&App{}).toolInboxReply(ctx, map[string]any{
		"id":                itemID,
		"media_storage_ids": []any{int64(55)},
	})
	if err != nil {
		t.Fatal(err)
	}
	out := result.(inboxOutcome)
	if out.Status != "unsupported" || !strings.Contains(out.Reason, "does not support attachments") {
		t.Fatalf("unexpected outcome: %+v", out)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("provider was called: %+v", pf.executeCalls)
	}
}

func TestResolveInboxMessage_EnforcesProjectAndPublicURL(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}
	if _, err := app.resolveInboxMessage(ctx, map[string]any{
		"body":             "hello",
		"media_project_id": "other-project",
	}, "test-proj"); err == nil || !strings.Contains(err.Error(), "current project") {
		t.Fatalf("project mismatch err = %v", err)
	}
	if _, err := app.resolveInboxMessage(ctx, map[string]any{
		"attachments": []any{map[string]any{
			"source": "url",
			"type":   "audio",
			"url":    "https://127.0.0.1/voice.mp3",
		}},
	}, "test-proj"); err == nil || !strings.Contains(err.Error(), "private address") {
		t.Fatalf("private URL err = %v", err)
	}
}

func TestNormalizeInboxMediaJSON_ProducesCanonicalAttachment(t *testing.T) {
	got := normalizeInboxMediaJSON(json.RawMessage(
		`{"attachments":{"data":[{"type":"audio","payload":{"url":"https://cdn.example.test/voice.ogg"},"mime_type":"audio/ogg"}]}}`,
	))
	var attachments []inboxAttachment
	if err := json.Unmarshal([]byte(got), &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Kind != "audio" || attachments[0].URL == "" {
		t.Fatalf("normalized attachments: %+v", attachments)
	}
	got = normalizeInboxMediaJSON(json.RawMessage(
		`["https://cdn.example.test/photo.jpg?sig=abc"]`,
	))
	if err := json.Unmarshal([]byte(got), &attachments); err != nil {
		t.Fatal(err)
	}
	if len(attachments) != 1 || attachments[0].Kind != "image" {
		t.Fatalf("normalized URL attachments: %+v", attachments)
	}
}

func TestInboxCapabilitiesPayloadIsExplicit(t *testing.T) {
	payload := inboxCapabilitiesPayload(platforms["instagram"].Inbox)
	types := payload["dm_attachment_types"].([]string)
	if payload["dm_max_attachments"] != 1 || len(types) != 4 {
		t.Fatalf("capability payload = %+v", payload)
	}
	unsupported := inboxCapabilitiesPayload(platforms["twitter"].Inbox)
	if unsupported["dm_max_attachments"] != 0 || len(unsupported["dm_attachment_types"].([]string)) != 0 {
		t.Fatalf("twitter capability payload = %+v", unsupported)
	}
}

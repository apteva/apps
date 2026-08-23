package main

import (
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type idempotentMessagingPlatform struct {
	tk.BasePlatformClient

	mu              sync.Mutex
	calls           []crmCallAppCall
	messagesByKey   map[string]map[string]any
	createdMessages int
	nextMessageID   int64
	sendResponse    map[string]any
}

func newIdempotentMessagingPlatform() *idempotentMessagingPlatform {
	return &idempotentMessagingPlatform{
		messagesByKey: map[string]map[string]any{},
		nextMessageID: 7000,
	}
}

func (p *idempotentMessagingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{
		AppName:   "crm",
		InstallID: 99,
		ProjectID: "test-proj",
		Bindings:  map[string]any{"messaging": float64(42)},
	}, nil
}

func (p *idempotentMessagingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return &sdk.PlatformInstance{
		ID:        id,
		Name:      "messaging",
		Status:    "running",
		ProjectID: "test-proj",
	}, nil
}

func (p *idempotentMessagingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.calls = append(p.calls, crmCallAppCall{AppName: appName, Tool: tool, Input: input})
	payload := map[string]any{"ok": true}
	switch tool {
	case "suppression_check":
		payload = map[string]any{"suppressed": false}
	case "send_message":
		if p.sendResponse != nil {
			payload = p.sendResponse
			break
		}
		key := strings.TrimSpace(strArg(input, "idempotency_key"))
		if existing := p.messagesByKey[key]; existing != nil {
			payload = existing
			break
		}
		p.nextMessageID++
		p.createdMessages++
		payload = map[string]any{
			"id":                  p.nextMessageID,
			"status":              "sent",
			"provider_message_id": "provider-" + key,
		}
		p.messagesByKey[key] = payload
	}
	if out != nil {
		body, _ := json.Marshal(payload)
		_ = json.Unmarshal(body, out)
	}
	return nil
}

func TestFailedMessagingResponseLogsSendFailure(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	platform.sendResponse = map[string]any{
		"id":            int64(18506),
		"status":        "failed",
		"status_reason": "Twilio error 21408: SMS sending is not enabled for the Czech Republic (+420)",
	}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "phone", "value": "+420123456789", "is_primary": true},
		},
	})

	_, err := app.toolSendMessage(ctx, map[string]any{
		"id":      contact.ID,
		"channel": channelSMS,
		"body":    "Hello",
		"from":    "+447380368854",
	})
	if err == nil || !strings.Contains(err.Error(), "message 18506 status failed") || !strings.Contains(err.Error(), "21408") {
		t.Fatalf("error=%v, want failed Messaging status and provider reason", err)
	}

	var sentCount, failedCount int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_activities
		 WHERE project_id = 'test-proj' AND contact_id = ? AND kind = 'sms_sent'`,
		contact.ID,
	).Scan(&sentCount); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_activities
		 WHERE project_id = 'test-proj' AND contact_id = ? AND kind = 'sms_send_failed'`,
		contact.ID,
	).Scan(&failedCount); err != nil {
		t.Fatal(err)
	}
	if sentCount != 0 || failedCount != 1 {
		t.Fatalf("sms_sent=%d sms_send_failed=%d, want 0/1", sentCount, failedCount)
	}

	var sourceDetail string
	var messagingID, conversationID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(source_detail, ''), COALESCE(messaging_id, 0), COALESCE(conversation_id, 0)
		 FROM contact_activities
		 WHERE project_id = 'test-proj' AND contact_id = ? AND kind = 'sms_send_failed'`,
		contact.ID,
	).Scan(&sourceDetail, &messagingID, &conversationID); err != nil {
		t.Fatal(err)
	}
	if messagingID != 18506 || conversationID <= 0 || !strings.Contains(sourceDetail, `"status":"failed"`) || !strings.Contains(sourceDetail, "21408") {
		t.Fatalf("source_detail=%s messaging_id=%d conversation_id=%d", sourceDetail, messagingID, conversationID)
	}
}

func (p *idempotentMessagingPlatform) snapshot() (created int, calls []crmCallAppCall) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.createdMessages, append([]crmCallAppCall(nil), p.calls...)
}

func TestSendMessageRejectsSubjectlessStandaloneEmailBeforePlatformCalls(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true},
		},
	})

	_, err := app.toolSendMessage(ctx, map[string]any{
		"id":   contact.ID,
		"body": "x",
		"from": "sender@example.test",
	})
	if err == nil || !strings.Contains(err.Error(), "subject required for a new email conversation") {
		t.Fatalf("error=%v, want standalone email subject validation", err)
	}
	if platform.whoAmICalls != 0 || platform.getInstanceCalls != 0 || len(platform.calls) != 0 {
		t.Fatalf(
			"platform calls before validation: whoami=%d get_instance=%d call_app=%d",
			platform.whoAmICalls, platform.getInstanceCalls, len(platform.calls),
		)
	}
}

func TestFreeFormSendNormalizesOptionalPlaceholders(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "phone", "value": "+12025550123", "is_primary": true},
		},
	})

	_, err := app.toolSendMessage(ctx, map[string]any{
		"id":            contact.ID,
		"channel":       channelSMS,
		"body":          "A real free-form message",
		"from":          "+12025550100",
		"template_id":   0,
		"list_id":       0,
		"subject":       "",
		"body_html":     "",
		"template_vars": map[string]any{"unused": "placeholder"},
	})
	if err != nil {
		t.Fatal(err)
	}

	created, calls := platform.snapshot()
	if created != 1 {
		t.Fatalf("messages created=%d, want 1", created)
	}
	var sent map[string]any
	for _, call := range calls {
		if call.Tool == "send_message" {
			sent = call.Input
			break
		}
	}
	if sent == nil {
		t.Fatal("Messaging send_message was not called")
	}
	for _, absent := range []string{"template_id", "list_id", "subject", "body_html", "vars", "template_vars"} {
		if value, exists := sent[absent]; exists {
			t.Errorf("Messaging input retained placeholder %s=%#v", absent, value)
		}
	}
}

func TestZeroTemplateIDWithoutMessageContentIsRejectedBeforePlatformCalls(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}

	_, err := app.toolSendMessage(ctx, map[string]any{"id": 1, "template_id": 0})
	if err == nil || !strings.Contains(err.Error(), "body, body_html, template_id, content_sid, or attachments required") {
		t.Fatalf("error=%v, want missing content", err)
	}
	if platform.whoAmICalls != 0 || platform.getInstanceCalls != 0 || len(platform.calls) != 0 {
		t.Fatalf(
			"platform calls before validation: whoami=%d get_instance=%d call_app=%d",
			platform.whoAmICalls, platform.getInstanceCalls, len(platform.calls),
		)
	}
}

func TestIntentionalTemplateSendForwardsTemplateFields(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "phone", "value": "+12025550123", "is_primary": true},
		},
	})

	_, err := app.toolSendMessage(ctx, map[string]any{
		"id":            contact.ID,
		"channel":       channelWhatsApp,
		"from":          "+12025550100",
		"template_id":   42,
		"template_vars": map[string]any{"first_name": "Alice"},
	})
	if err != nil {
		t.Fatal(err)
	}

	_, calls := platform.snapshot()
	for _, call := range calls {
		if call.Tool != "send_message" {
			continue
		}
		if int64FromAny(call.Input["template_id"]) != 42 {
			t.Fatalf("template_id=%#v, want 42", call.Input["template_id"])
		}
		vars, ok := call.Input["vars"].(map[string]any)
		if !ok || vars["first_name"] != "Alice" {
			t.Fatalf("vars=%#v, want intentional template variables", call.Input["vars"])
		}
		return
	}
	t.Fatal("Messaging send_message was not called")
}

func TestReplyRejectsUnknownConversationBeforePlatformCalls(t *testing.T) {
	platform := &crmRecordingPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true},
		},
	})

	_, err := app.toolReply(ctx, map[string]any{
		"id":              contact.ID,
		"conversation_id": 999,
		"body":            "Reply",
		"from":            "sender@example.test",
	})
	if err == nil || !strings.Contains(err.Error(), "conversation_id does not belong to this contact") {
		t.Fatalf("error=%v, want conversation ownership validation", err)
	}
	if platform.whoAmICalls != 0 || platform.getInstanceCalls != 0 || len(platform.calls) != 0 {
		t.Fatalf(
			"platform calls before validation: whoami=%d get_instance=%d call_app=%d",
			platform.whoAmICalls, platform.getInstanceCalls, len(platform.calls),
		)
	}
}

func TestAutomaticOutboundIdempotencyKey(t *testing.T) {
	now := time.Date(2026, 7, 18, 8, 18, 10, 0, time.UTC)
	base := map[string]any{
		"channel": "email",
		"from":    "sender@example.test",
		"to":      "alice@example.test",
		"subject": "Hello",
		"body":    "A real message",
	}
	fingerprint := outboundContentFingerprint("test-proj", 1, false, base)
	first := automaticOutboundIdempotencyKey(fingerprint, now)
	second := automaticOutboundIdempotencyKey(fingerprint, now.Add(30*time.Second))
	if first != second || !strings.HasPrefix(first, "crm-auto-v1:") {
		t.Fatalf("same-window keys differ: first=%q second=%q", first, second)
	}

	changed := map[string]any{}
	for key, value := range base {
		changed[key] = value
	}
	changed["body"] = "An intentionally different message"
	changedFingerprint := outboundContentFingerprint("test-proj", 1, false, changed)
	if got := automaticOutboundIdempotencyKey(changedFingerprint, now); got == first {
		t.Fatal("different message content produced the same key")
	}
	if got := automaticOutboundIdempotencyKey(fingerprint, now.Add(outboundIdempotencyWindow)); got == first {
		t.Fatal("a later send window produced the same key")
	}
}

func TestConcurrentIdenticalSendsCreateOneMessageActivityAndConversation(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true},
		},
	})

	start := make(chan struct{})
	results := make(chan map[string]any, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			out, err := app.toolSendMessage(ctx, map[string]any{
				"id":      contact.ID,
				"subject": "Partnership",
				"body":    "Would you like to discuss a partnership?",
				"from":    "sender@example.test",
			})
			if err != nil {
				errs <- err
				return
			}
			results <- out.(map[string]any)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	close(results)
	for err := range errs {
		t.Fatalf("concurrent send: %v", err)
	}

	var got []map[string]any
	for result := range results {
		got = append(got, result)
	}
	if len(got) != 2 {
		t.Fatalf("results=%d, want 2", len(got))
	}
	if got[0]["messaging_id"] != got[1]["messaging_id"] ||
		got[0]["conversation_id"] != got[1]["conversation_id"] {
		t.Fatalf("deduplicated results differ: %#v %#v", got[0], got[1])
	}
	firstActivity := got[0]["activity"].(*Activity)
	secondActivity := got[1]["activity"].(*Activity)
	if firstActivity.ID != secondActivity.ID {
		t.Fatalf("activity ids differ: %d != %d", firstActivity.ID, secondActivity.ID)
	}
	dedupedCount := 0
	for _, result := range got {
		if result["deduped"] == true {
			dedupedCount++
		}
		if key, _ := result["idempotency_key"].(string); !strings.HasPrefix(key, "crm-auto-v1:") {
			t.Fatalf("automatic idempotency key missing: %#v", result)
		}
	}
	if dedupedCount != 1 {
		t.Fatalf("deduped results=%d, want exactly 1", dedupedCount)
	}

	created, calls := platform.snapshot()
	if created != 1 {
		t.Fatalf("messaging rows created=%d, want 1", created)
	}
	sendCalls := 0
	for _, call := range calls {
		if call.AppName == "messaging" && call.Tool == "send_message" {
			sendCalls++
		}
	}
	if sendCalls != 1 {
		t.Fatalf("messaging send calls=%d, want 1", sendCalls)
	}

	var activities, conversations int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_activities
		 WHERE project_id = 'test-proj' AND contact_id = ? AND kind = 'email_sent'`,
		contact.ID,
	).Scan(&activities); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_conversations
		 WHERE project_id = 'test-proj' AND contact_id = ?`,
		contact.ID,
	).Scan(&conversations); err != nil {
		t.Fatal(err)
	}
	if activities != 1 || conversations != 1 {
		t.Fatalf("activities=%d conversations=%d, want 1/1", activities, conversations)
	}
}

func TestIntentionallyDifferentMessagesAreNotDeduplicated(t *testing.T) {
	platform := newIdempotentMessagingPlatform()
	ctx := newTestCtx(t, tk.WithPlatform(platform))
	app := &App{}
	contact := mustCreate(t, ctx, map[string]any{
		"display_name": "Alice",
		"channels": []any{
			map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true},
		},
	})

	for _, body := range []string{"First message", "Second message"} {
		if _, err := app.toolSendMessage(ctx, map[string]any{
			"id":      contact.ID,
			"subject": "Partnership",
			"body":    body,
			"from":    "sender@example.test",
		}); err != nil {
			t.Fatal(err)
		}
	}
	created, _ := platform.snapshot()
	if created != 2 {
		t.Fatalf("messaging rows created=%d, want 2", created)
	}
	var activities, conversations int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_activities
		 WHERE project_id = 'test-proj' AND contact_id = ? AND kind = 'email_sent'`,
		contact.ID,
	).Scan(&activities)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM contact_conversations
		 WHERE project_id = 'test-proj' AND contact_id = ?`,
		contact.ID,
	).Scan(&conversations)
	if activities != 2 || conversations != 2 {
		t.Fatalf("activities=%d conversations=%d, want 2/2", activities, conversations)
	}
}

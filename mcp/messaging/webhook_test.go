package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Bounce webhook ───────────────────────────────────────────────

func TestBounceWebhook_HardBounceSuppresses(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	// Pre-seed a sent message that we'll bounce.
	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, channel, direction, from_addr, to_addrs,
			 status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com',
		         '["bouncy@example.com"]', 'sent', 'ses-msg-bounce-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()

	// SES bounce notification wrapped in SNS.
	innerSES := map[string]any{
		"notificationType": "Bounce",
		"mail":             map[string]any{"messageId": "ses-msg-bounce-1"},
		"bounce": map[string]any{
			"bounceType": "Permanent",
			"bouncedRecipients": []map[string]any{
				{"emailAddress": "bouncy@example.com", "diagnosticCode": "550 user unknown"},
			},
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleBounceWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// Message should now be 'bounced'.
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m == nil || m.Status != "bounced" {
		t.Fatalf("status=%q want bounced", func() string {
			if m == nil {
				return "<nil>"
			}
			return m.Status
		}())
	}
	// And the recipient should be on the suppression list.
	supp, _ := dbSuppressionList(ctx.AppDB(), "test-proj", "email", 100)
	if len(supp) != 1 {
		t.Fatalf("expected 1 suppression, got %d", len(supp))
	}
	if supp[0].Address != "bouncy@example.com" {
		t.Errorf("suppressed addr=%q", supp[0].Address)
	}
	if supp[0].Reason != "hard-bounce" {
		t.Errorf("reason=%q", supp[0].Reason)
	}
}

func TestBounceWebhook_ComplaintSuppresses(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	_, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["angry@example.com"]', 'sent', 'ses-c-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}

	innerSES := map[string]any{
		"notificationType": "Complaint",
		"mail":             map[string]any{"messageId": "ses-c-1"},
		"complaint": map[string]any{
			"complainedRecipients":  []map[string]any{{"emailAddress": "angry@example.com"}},
			"complaintFeedbackType": "abuse",
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleBounceWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	supp, _ := dbSuppressionList(ctx.AppDB(), "test-proj", "email", 100)
	if len(supp) != 1 || supp[0].Reason != "complaint" {
		t.Errorf("suppressions=%v", supp)
	}
}

func TestSESBounceEventPublishesTypedClassificationAndSuppressionChange(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, nil, tk.WithEmitter(recorder))
	app := &App{}
	_, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["missing@example.com"]', 'sent', 'ses-typed-bounce-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}

	innerSES := map[string]any{
		"eventType": "Bounce",
		"mail": map[string]any{
			"messageId":   "ses-typed-bounce-1",
			"timestamp":   "2026-08-26T10:00:00Z",
			"destination": []string{"missing@example.com"},
		},
		"bounce": map[string]any{
			"timestamp":     "2026-08-26T10:00:01Z",
			"bounceType":    "Permanent",
			"bounceSubType": "NoEmail",
			"bouncedRecipients": []map[string]any{{
				"emailAddress": "missing@example.com", "diagnosticCode": "smtp; 550 5.1.1 user unknown",
			}},
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type": "Notification", "MessageId": "sns-typed-bounce", "TopicArn": testSNSTopicARN,
		"Message": string(innerJSON), "SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleBounceWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	messageEvents := recorder.EventsByTopic("message.event")
	if len(messageEvents) != 1 {
		t.Fatalf("message.event count=%d", len(messageEvents))
	}
	payload := messageEvents[0].Data.(map[string]any)
	if payload["permanent"] != true || payload["bounce_type"] != "Permanent" || payload["bounce_subtype"] != "NoEmail" || payload["reason"] != "smtp; 550 5.1.1 user unknown" {
		t.Fatalf("typed bounce payload=%#v", payload)
	}
	suppressionEvents := recorder.EventsByTopic("suppression.changed")
	if len(suppressionEvents) != 1 {
		t.Fatalf("suppression events=%#v", suppressionEvents)
	}
	suppressionPayload := suppressionEvents[0].Data.(map[string]any)
	if suppressionPayload["operation"] != "add" || suppressionPayload["address"] != "missing@example.com" {
		t.Fatalf("suppression payload=%#v", suppressionPayload)
	}
}

func TestSESTransientBounceClassifiesWithoutSuppressing(t *testing.T) {
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, nil, tk.WithEmitter(recorder))
	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["full@example.com"]', 'sent', 'ses-transient-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := res.LastInsertId()
	message, _ := dbMessageGet(ctx.AppDB(), "test-proj", messageID)
	events, err := parseSESProviderEvents(`{
		"eventType":"Bounce",
		"mail":{"messageId":"ses-transient-1","destination":["full@example.com"]},
		"bounce":{"bounceType":"Transient","bounceSubType":"MailboxFull","bouncedRecipients":[{"emailAddress":"full@example.com","diagnosticCode":"mailbox full"}]}
	}`)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	events[0].ProviderEventID = "sns:transient:0"
	if persisted, err := persistAndEmitProviderEvent(ctx, message, events[0]); err != nil || !persisted {
		t.Fatalf("persisted=%v err=%v", persisted, err)
	}
	suppressions, _ := dbSuppressionList(ctx.AppDB(), "test-proj", channelEmail, 100)
	if len(suppressions) != 0 {
		t.Fatalf("transient bounce suppressions=%#v", suppressions)
	}
	payload := recorder.EventsByTopic("message.event")[0].Data.(map[string]any)
	if payload["permanent"] != false || payload["bounce_type"] != "Transient" || payload["bounce_subtype"] != "MailboxFull" || payload["reason"] != "mailbox full" {
		t.Fatalf("transient payload=%#v", payload)
	}
}

func TestLegacySESNotificationPreservesBounceClassification(t *testing.T) {
	events, err := parseSESProviderEvents(`{
		"notificationType":"Bounce",
		"mail":{"messageId":"legacy-bounce-1"},
		"bounce":{"bounceType":"Permanent","bounceSubType":"Suppressed","bouncedRecipients":[{"emailAddress":"legacy@example.com","diagnosticCode":"address suppressed"}]}
	}`)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%#v err=%v", events, err)
	}
	event := events[0]
	if !event.Permanent || event.BounceType != "Permanent" || event.BounceSubType != "Suppressed" || event.Reason != "address suppressed" {
		t.Fatalf("legacy event=%#v", event)
	}
}

func TestSESSubscriptionOnlyGloballySuppressesUnsubscribeAll(t *testing.T) {
	for _, tc := range []struct {
		name           string
		unsubscribeAll bool
		wantSuppressed bool
	}{
		{name: "unsubscribe all", unsubscribeAll: true, wantSuppressed: true},
		{name: "topic only", unsubscribeAll: false, wantSuppressed: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := tk.NewEmitRecorder()
			ctx := newTestCtx(t, nil, tk.WithEmitter(recorder))
			res, err := ctx.AppDB().Exec(
				`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
				 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["subscriber@example.com"]', 'sent', 'ses-subscription-1')`,
			)
			if err != nil {
				t.Fatal(err)
			}
			messageID, _ := res.LastInsertId()
			message, _ := dbMessageGet(ctx.AppDB(), "test-proj", messageID)
			raw := fmt.Sprintf(`{
				"eventType":"Subscription",
				"mail":{"messageId":"ses-subscription-1","destination":["subscriber@example.com"]},
				"subscription":{"contactList":"customers","newTopicPreferences":{"unsubscribeAll":%t,"topicSubscriptionStatus":{"topicName":"news","subscriptionStatus":"OptOut"}}}
			}`, tc.unsubscribeAll)
			events, err := parseSESProviderEvents(raw)
			if err != nil || len(events) != 1 {
				t.Fatalf("events=%#v err=%v", events, err)
			}
			events[0].ProviderEventID = "sns:subscription:0"
			if persisted, err := persistAndEmitProviderEvent(ctx, message, events[0]); err != nil || !persisted {
				t.Fatalf("persisted=%v err=%v", persisted, err)
			}
			suppressions, _ := dbSuppressionList(ctx.AppDB(), "test-proj", channelEmail, 100)
			if got := len(suppressions) == 1; got != tc.wantSuppressed {
				t.Fatalf("suppressions=%#v wantSuppressed=%v", suppressions, tc.wantSuppressed)
			}
			changes := recorder.EventsByTopic("suppression.changed")
			if got := len(changes) == 1; got != tc.wantSuppressed {
				t.Fatalf("suppression.changed=%#v want=%v", changes, tc.wantSuppressed)
			}
			if tc.wantSuppressed && suppressions[0].Reason != "unsubscribe-all" {
				t.Fatalf("suppression=%#v", suppressions[0])
			}
		})
	}
}

func TestProviderEventRollsBackWhenAutomaticSuppressionFails(t *testing.T) {
	ctx := newTestCtx(t, nil)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["broken@example.com"]', 'sent', 'ses-atomic-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	messageID, _ := res.LastInsertId()
	message, _ := dbMessageGet(ctx.AppDB(), "test-proj", messageID)
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_suppression BEFORE INSERT ON suppressions BEGIN SELECT RAISE(ABORT, 'suppression unavailable'); END`); err != nil {
		t.Fatal(err)
	}

	persisted, err := persistAndEmitProviderEvent(ctx, message, providerEvent{
		ProviderEventID: "sns:atomic:0", Provider: "aws-ses", ProviderMessageID: "ses-atomic-1",
		Kind: "bounced", Recipient: "broken@example.com", Reason: "user unknown",
		Permanent: true, BounceType: "Permanent", BounceSubType: "NoEmail", Raw: json.RawMessage(`{"eventType":"Bounce"}`),
	})
	if err == nil || persisted {
		t.Fatalf("persisted=%v err=%v, want atomic failure", persisted, err)
	}
	stored, _ := dbDeliveryEvents(ctx.AppDB(), messageID)
	if len(stored) != 0 {
		t.Fatalf("delivery event survived rollback: %#v", stored)
	}
}

func TestSESEventPublishing_ClickPersistsAndEmitsSpecificTopic(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["alice@example.com"]', 'sent', 'ses-click-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()

	innerSES := map[string]any{
		"eventType": "Click",
		"mail": map[string]any{
			"messageId":   "ses-click-1",
			"timestamp":   "2026-05-23T10:30:00Z",
			"destination": []string{"alice@example.com"},
		},
		"click": map[string]any{
			"timestamp": "2026-05-23T10:31:00Z",
			"link":      "https://example.com/pricing",
			"ipAddress": "203.0.113.10",
			"userAgent": "Mozilla/5.0",
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleBounceWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	events, _ := dbDeliveryEvents(ctx.AppDB(), msgID)
	if len(events) != 1 {
		t.Fatalf("expected 1 event, got %d", len(events))
	}
	if events[0].Kind != "clicked" {
		t.Fatalf("kind=%q want clicked", events[0].Kind)
	}
	if !strings.Contains(string(events[0].Raw), "https://example.com/pricing") {
		t.Fatalf("raw event did not include click metadata: %s", string(events[0].Raw))
	}
	counts := dbDeliveryEventCounts(ctx.AppDB(), msgID)
	if counts["clicked"] != 1 {
		t.Fatalf("clicked count=%d", counts["clicked"])
	}
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m == nil || m.Status != "clicked" {
		t.Fatalf("status=%q want clicked", func() string {
			if m == nil {
				return "<nil>"
			}
			return m.Status
		}())
	}
}

func TestProviderEvent_GlobalInstallEmitsForMessageProject(t *testing.T) {
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEmitter(rec))
	globalCtx = ctx

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'whatsapp', 'out', '+15551112222', '["+15553334444"]', 'sent', 'SMglobal1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()
	msg, err := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := persistAndEmitProviderEvent(ctx, msg, providerEvent{
		Provider:          "twilio",
		ProviderMessageID: "SMglobal1",
		Kind:              "opened",
		Recipient:         "+15553334444",
		Raw:               json.RawMessage(`{"MessageStatus":"read"}`),
	}); err != nil {
		t.Fatal(err)
	}

	for _, topic := range []string{"message.opened", "message.event"} {
		events := rec.EventsByTopic(topic)
		if len(events) != 1 {
			t.Fatalf("%s emits=%d, want 1", topic, len(events))
		}
		if events[0].ProjectID != "test-proj" {
			t.Fatalf("%s project=%q, want test-proj", topic, events[0].ProjectID)
		}
	}
}

func TestSNSRejectsForgedAWSLookingCertificateURL(t *testing.T) {
	ctx := newTestCtx(t, nil, tk.WithConfig(map[string]string{"webhook_signing_secret": ""}))
	body := []byte(`{"Type":"Notification","Message":"{}","MessageId":"m1","TopicArn":"arn:aws:sns:eu-west-1:1:test","Timestamp":"2026-01-01T00:00:00Z","Signature":"ZmFrZQ==","SignatureVersion":"1","SigningCertURL":"https://evil.example/amazonaws.com/cert.pem"}`)
	r := httptest.NewRequest(http.MethodPost, "/webhooks/ses-bounces", strings.NewReader(string(body)))
	if verifySNS(r, body, ctx, "ses_bounce_topic_arn") {
		t.Fatal("forged SNS envelope was accepted")
	}
}

func TestProviderEventIsIdempotent(t *testing.T) {
	ctx := newTestCtx(t, nil)
	res, err := ctx.AppDB().Exec(`INSERT INTO messages
		(project_id, channel, direction, from_addr, to_addrs, status)
		VALUES ('test-proj', 'email', 'out', 'a@example.com', '["b@example.com"]', 'sent')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	message, _ := dbMessageGet(ctx.AppDB(), "test-proj", id)
	event := providerEvent{
		ProviderEventID: "sns:event-1:0", Provider: "ses", ProviderMessageID: "ses-1",
		Kind: "delivered", Recipient: "b@example.com", Raw: json.RawMessage(`{"event":"delivery"}`),
	}
	persisted, err := persistAndEmitProviderEvent(ctx, message, event)
	if err != nil {
		t.Fatal(err)
	}
	if !persisted {
		t.Fatal("first event was not persisted")
	}
	persisted, err = persistAndEmitProviderEvent(ctx, message, event)
	if err != nil {
		t.Fatal(err)
	}
	if persisted {
		t.Fatal("duplicate event was persisted")
	}
	if events, _ := dbDeliveryEvents(ctx.AppDB(), id); len(events) != 1 {
		t.Fatalf("events=%d", len(events))
	}
}

func TestSESEventPublishing_OpenPromotesStatus(t *testing.T) {
	ctx := newTestCtx(t, nil)
	app := &App{}

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["alice@example.com"]', 'delivered', 'ses-open-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()

	innerSES := map[string]any{
		"eventType": "Open",
		"mail": map[string]any{
			"messageId":   "ses-open-1",
			"timestamp":   "2026-05-23T10:30:00Z",
			"destination": []string{"alice@example.com"},
		},
		"open": map[string]any{
			"timestamp": "2026-05-23T10:31:00Z",
			"ipAddress": "203.0.113.10",
			"userAgent": "Mozilla/5.0",
		},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleBounceWebhook(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m == nil || m.Status != "opened" {
		t.Fatalf("status=%q want opened", func() string {
			if m == nil {
				return "<nil>"
			}
			return m.Status
		}())
	}
}

func TestMessageGet_DerivesEffectiveStatusFromEvents(t *testing.T) {
	ctx := newTestCtx(t, nil)

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["alice@example.com"]', 'delivered', 'ses-historical-open-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO delivery_events (message_id, kind, recipient, raw)
		 VALUES (?, 'opened', 'alice@example.com', '{}')`,
		msgID,
	); err != nil {
		t.Fatal(err)
	}

	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", msgID)
	if m == nil || m.Status != "opened" {
		t.Fatalf("status=%q want opened", func() string {
			if m == nil {
				return "<nil>"
			}
			return m.Status
		}())
	}
}

func TestMessageList_DerivesEffectiveStatusFromStoredEvents(t *testing.T) {
	ctx := newTestCtx(t, nil)

	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages (project_id, channel, direction, from_addr, to_addrs, status, provider_message_id)
		 VALUES ('test-proj', 'email', 'out', 'noreply@acme.com', '["alice@example.com"]', 'delivered', 'ses-historical-open-list-1')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	msgID, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO delivery_events (message_id, kind, recipient, raw)
		 VALUES (?, 'opened', 'alice@example.com', '{}')`,
		msgID,
	); err != nil {
		t.Fatal(err)
	}

	rows, err := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "out", Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("message count=%d want 1", len(rows))
	}
	if rows[0].Status != "opened" {
		t.Fatalf("status=%q want opened", rows[0].Status)
	}
	if rows[0].EventCounts["opened"] != 1 {
		t.Fatalf("opened count=%d want 1", rows[0].EventCounts["opened"])
	}
}

// ─── Inbound webhook ──────────────────────────────────────────────

const sampleEml = "From: customer@example.com\r\n" +
	"To: support+T-1234@acme.com\r\n" +
	"Subject: Re: Order #1234\r\n" +
	"Message-ID: <abc123@example.com>\r\n" +
	"In-Reply-To: <orig@acme.com>\r\n" +
	"Content-Type: text/plain\r\n" +
	"\r\n" +
	"Still no package.\r\n"

func TestInboundWebhook_PersistsAndDispatches(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	// Register an inbound route so dispatch has a target.
	if _, err := app.toolInboundRouteSet(ctx, map[string]any{
		"pattern":      "support+*@acme.com",
		"target_app":   "support",
		"target_route": "/inbound",
	}); err != nil {
		t.Fatal(err)
	}

	innerSES := map[string]any{
		"notificationType": "Received",
		"content":          sampleEml,
		"mail":             map[string]any{"messageId": "ses-inbound-1"},
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}

	// One inbound message persisted.
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected 1 inbound message, got %d", len(rows))
	}
	m := rows[0]
	if m.Subject != "Re: Order #1234" {
		t.Errorf("subject=%q", m.Subject)
	}
	if m.From != "customer@example.com" {
		t.Errorf("from=%q", m.From)
	}
	if m.MessageIDHeader != "<abc123@example.com>" {
		t.Errorf("msg-id=%q", m.MessageIDHeader)
	}
	if m.RouteStatus != "ok" {
		t.Errorf("route_status=%q want ok", m.RouteStatus)
	}
	if m.RouteTargetApp != "support" || m.RouteTargetRoute != "/inbound" {
		t.Errorf("route target=%s%s", m.RouteTargetApp, m.RouteTargetRoute)
	}
	// v0.1 lowercases canonical addresses (and thus subaddresses).
	if m.ToSubaddress != "t-1234" {
		t.Errorf("subaddress=%q", m.ToSubaddress)
	}

	// Dispatch should have hit the support app.
	if len(plat.callAppCalls) != 1 {
		t.Fatalf("expected 1 CallApp, got %d", len(plat.callAppCalls))
	}
	call := plat.callAppCalls[0]
	if call.App != "support" || call.Tool != "/inbound" {
		t.Errorf("call=%+v", call)
	}
	if call.Input["matched_recipient"] != "support+t-1234@acme.com" {
		t.Errorf("matched=%v", call.Input["matched_recipient"])
	}
	if call.Input["to_subaddress"] != "t-1234" {
		t.Errorf("subaddress=%v", call.Input["to_subaddress"])
	}
}

func TestInboundWebhook_NoMatchSetsNoMatch(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}

	innerSES := map[string]any{
		"notificationType": "Received",
		"content":          sampleEml,
	}
	innerJSON, _ := json.Marshal(innerSES)
	envelope := map[string]any{
		"Type":           "Notification",
		"MessageId":      "test-sns-message",
		"TopicArn":       testSNSTopicARN,
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", strings.NewReader(string(body)))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleInboundWebhook(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status=%d", w.Code)
	}
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 1 {
		t.Fatalf("expected 1 row")
	}
	if rows[0].RouteStatus != "no_match" {
		t.Errorf("route_status=%q want no_match", rows[0].RouteStatus)
	}
	if len(plat.callAppCalls) != 0 {
		t.Errorf("no CallApp expected, got %d", len(plat.callAppCalls))
	}
}

package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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
		 VALUES ('test-proj', 'email', 'out', 'mailto:noreply@acme.com',
		         '["mailto:bouncy@example.com"]', 'sent', 'ses-msg-bounce-1')`,
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
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Sns-Message-Type", "Notification")
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
	if supp[0].Address != "mailto:bouncy@example.com" {
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
		 VALUES ('test-proj', 'email', 'out', 'mailto:noreply@acme.com', '["mailto:angry@example.com"]', 'sent', 'ses-c-1')`,
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
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-bounces?project_id=test-proj", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Sns-Message-Type", "Notification")
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
		"pattern":      "mailto:support+*@acme.com",
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
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Sns-Message-Type", "Notification")
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
	if m.From != "mailto:customer@example.com" {
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
	if call.Input["matched_recipient"] != "mailto:support+t-1234@acme.com" {
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
		"Message":        string(innerJSON),
		"SigningCertURL": "https://sns.us-east-1.amazonaws.com/cert.pem",
	}
	body, _ := json.Marshal(envelope)

	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", strings.NewReader(string(body)))
	r.Header.Set("X-Amz-Sns-Message-Type", "Notification")
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

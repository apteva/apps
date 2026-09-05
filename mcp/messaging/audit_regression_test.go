package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"testing"
)

// Audit regressions: assertions describe intended behavior; failures reproduce defects.
func TestAuditMissingMessage(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("missing message panics: %v", r)
		}
	}()
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", 999999)
	if err != nil || m != nil {
		t.Fatalf("got %v %v", m, err)
	}
}
func TestAuditAttachmentOnlyEmail(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "attachments": []any{map[string]any{"filename": "file.txt", "content_type": "text/plain", "content_base64": base64.StdEncoding.EncodeToString([]byte("document"))}}})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "sent" {
		t.Fatalf("attachment-only email status=%v reason=%v", out.(map[string]any)["status"], out.(map[string]any)["status_reason"])
	}
}
func TestAuditWhatsAppRefresh(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool["list_whatsapp_senders"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"senders":[{"sid":"XE-test","sender_id":"whatsapp:+15551234567","status":"ONLINE"}]}`)}
	plat.replyByTool["list_phone_numbers"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"incoming_phone_numbers":[]}`)}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "whatsapp", Address: "+15551234567", Kind: "phone", Provider: "twilio", ProviderIdentityID: "XE-test", Verified: true, SendingEnabled: true})
	if err := (&App{}).refreshTwilioNumbers(ctx, "test-proj", 2); err != nil {
		t.Fatal(err)
	}
	rows, err := dbListSenders(ctx.AppDB(), "test-proj", "whatsapp", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("valid WhatsApp sender removed by phone-number refresh; remaining=%d", len(rows))
	}
}
func TestAuditRefreshPreservesInboundWiring(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool["list_phone_numbers"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"incoming_phone_numbers":[{"sid":"PN-test","phone_number":"+15551234567","sms_url":"https://example.com/inbound"}]}`)}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "sms", Address: "+15551234567", Kind: "phone", Provider: "twilio", Verified: true, InboundBootstrapped: true})
	if err := (&App{}).refreshTwilioNumbers(ctx, "test-proj", 2); err != nil {
		t.Fatal(err)
	}
	row, err := dbFindSender(ctx.AppDB(), "test-proj", "sms", "+15551234567")
	if err != nil {
		t.Fatal(err)
	}
	if !row.InboundBootstrapped {
		t.Fatal("refresh erased inbound_bootstrapped despite upstream sms_url")
	}
}
func TestAuditMalformedInventoryIsNonDestructive(t *testing.T) {
	plat := newPhoneStub(nil)
	plat.replyByTool["list_phone_numbers"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{broken`)}
	ctx := newTestCtx(t, plat)
	preseedSender(t, ctx, senderUpsert{Channel: "sms", Address: "+15551234567", Kind: "phone", Provider: "twilio", Verified: true})
	err := (&App{}).refreshTwilioNumbers(ctx, "test-proj", 2)
	rows, _ := dbListSenders(ctx.AppDB(), "test-proj", "sms", false)
	if err == nil || len(rows) != 1 {
		t.Fatalf("malformed response: error=%v, remaining senders=%d", err, len(rows))
	}
}
func TestAuditGenericRouteTool(t *testing.T) {
	if got := inboundRouteTargetTool("tickets", "receive"); got != "receive" {
		t.Fatalf("MCP tool rewritten to %q", got)
	}
}
func TestAuditDeleteRequiresLocalOwnership(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	_, _ = (&App{}).toolSendersDelete(ctx, map[string]any{"address": "unregistered.example.com"})
	for _, call := range plat.executeCalls {
		if call.Tool == "delete_identity" {
			t.Fatal("upstream delete attempted with no local sender/identity ownership")
		}
	}
}
func TestAuditSingleReceiptRule(t *testing.T) {
	got := extractRecipientsForRule(json.RawMessage(`{"member":{"Name":"inbound","Recipients":{"member":"existing.example.com"}}}`), "inbound")
	if len(got) != 1 || got[0] != "existing.example.com" {
		t.Fatalf("existing recipients lost: %v", got)
	}
}
func TestAuditFriendlyFromFilter(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: fromAcme, Kind: "email_mailbox", Provider: "aws-ses", Verified: true, SendingEnabled: true, DisplayName: "Acme"})
	_, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "body": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	_, total, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Address: fromAcme, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Fatalf("canonical From filter returned %d instead of 1", total)
	}
}
func TestAuditHeadersSurviveSimpleEmail(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	_, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "body": "hello", "headers": map[string]any{"List-Unsubscribe": "<https://example.com/unsubscribe>"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, call := range plat.executeCalls {
		if call.Tool == "send_email" {
			encoded, _ := json.Marshal(call.Input)
			if !strings.Contains(string(encoded), "List-Unsubscribe") {
				t.Fatal("custom List-Unsubscribe header discarded")
			}
		}
	}
}
func TestAuditStaleEventCannotRegressStatus(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "body": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(int64)
	a, _ := dbMessageGet(ctx.AppDB(), "test-proj", id)
	b, _ := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if _, err := persistAndEmitProviderEvent(ctx, a, providerEvent{Provider: "aws-ses", ProviderEventID: "open", Kind: "opened", OccurredAt: "2026-09-05T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	if _, err := persistAndEmitProviderEvent(ctx, b, providerEvent{Provider: "aws-ses", ProviderEventID: "delivery", Kind: "delivered", OccurredAt: "2026-09-05T09:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	var status string
	ctx.AppDB().QueryRow("SELECT status FROM messages WHERE id=?", id).Scan(&status)
	if status != "opened" {
		t.Fatalf("stale callback regressed persisted status to %q (status filters use this value)", status)
	}
}

func auditInbound(t *testing.T, app *App, providerID, raw string, recipients []string) {
	t.Helper()
	inner, _ := json.Marshal(map[string]any{"notificationType": "Received", "content": raw, "mail": map[string]any{"messageId": providerID}, "receipt": map[string]any{"recipients": recipients}})
	body, _ := json.Marshal(map[string]any{"Type": "Notification", "MessageId": "sns-" + providerID, "TopicArn": testSNSTopicARN, "Message": string(inner)})
	r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", bytes.NewReader(body))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	app.handleInboundAndProcessForTest(w, r)
	if w.Code != 200 {
		t.Fatalf("webhook HTTP %d %s", w.Code, w.Body.String())
	}
}
func TestAuditInboundRetryResumesDispatch(t *testing.T) {
	plat := &stubPlatform{callAppResultErr: errors.New("temporary downstream outage")}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := app.toolInboundRouteSet(ctx, map[string]any{"pattern": "support@acme.com", "target_app": "crm", "target_route": "/inbound"})
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: customer@example.com\r\nTo: support@acme.com\r\nMessage-ID: <retry@example.com>\r\n\r\nHello"
	auditInbound(t, app, "retry", raw, []string{"support@acme.com"})
	plat.callAppResultErr = nil
	auditInbound(t, app, "retry", raw, []string{"support@acme.com"})
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 1 || rows[0].RouteStatus != "ok" {
		t.Fatalf("provider retry leaves route status %q after recovery", rows[0].RouteStatus)
	}
}
func TestAuditInboundUsesEnvelopeRecipient(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	_, err := app.toolInboundRouteSet(ctx, map[string]any{"pattern": "support@acme.com", "target_app": "crm", "target_route": "/inbound"})
	if err != nil {
		t.Fatal(err)
	}
	raw := "From: customer@example.com\r\nTo: visible@example.com\r\nMessage-ID: <bcc@example.com>\r\n\r\nHello BCC support"
	auditInbound(t, app, "bcc", raw, []string{"support@acme.com"})
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if rows[0].RouteStatus != "ok" {
		t.Fatalf("BCC envelope recipient not routed: %q", rows[0].RouteStatus)
	}
}
func TestAuditDistinctSESDeliveriesWithRepeatedRFCID(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	app := &App{}
	raw := "From: customer@example.com\r\nTo: support@acme.com\r\nMessage-ID: <reused@example.com>\r\n\r\nHello"
	auditInbound(t, app, "delivery-1", raw, []string{"support@acme.com"})
	auditInbound(t, app, "delivery-2", raw+" again", []string{"support@acme.com"})
	rows, _ := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if len(rows) != 2 {
		t.Fatalf("distinct provider deliveries collapsed to %d row", len(rows))
	}
}
func TestAuditDeletedSenderBlockedInProjectInstall(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: fromAcme, Kind: "email_mailbox", Provider: "aws-ses", Verified: true, SendingEnabled: true})
	if err := dbSoftDeleteSender(ctx.AppDB(), "test-proj", "email", fromAcme); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "body": "hello"})
	if err == nil {
		t.Fatalf("deleted sender still accepted: status=%v", out.(map[string]any)["status"])
	}
}

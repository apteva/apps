package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func gmailTestReply(v any) *sdk.ExecuteResult {
	b, _ := json.Marshal(v)
	return &sdk.ExecuteResult{Success: true, Data: b}
}

func gmailTestPlatform() *stubPlatform {
	return &stubPlatform{
		bindingsOverride: map[string]any{"email_provider": map[string]any{"ids": []any{float64(1), float64(3)}, "default_id": float64(3)}},
		replyByTool: map[string]*sdk.ExecuteResult{
			"list_send_as":    gmailTestReply(map[string]any{"sendAs": []map[string]any{{"sendAsEmail": "support@example.com", "verificationStatus": "accepted"}}}),
			"send_raw_email":  gmailTestReply(map[string]any{"id": "gmail-sent-1", "threadId": "gmail-thread-1"}),
			"get_profile":     gmailTestReply(map[string]any{"emailAddress": "support@example.com", "historyId": "10"}),
			"list_history":    gmailTestReply(map[string]any{"historyId": "11", "history": []map[string]any{{"messagesAdded": []map[string]any{{"message": map[string]any{"id": "gmail-in-1", "threadId": "gmail-thread-in"}}}}}}),
			"get_raw_message": gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: customer@example.org\r\nTo: support@example.com\r\nMessage-ID: <customer-1@example.org>\r\nSubject: Hello\r\n\r\nHi")), "threadId": "gmail-thread-in", "labelIds": []string{"INBOX"}}),
		},
	}
}

func TestGmailSenderSendAndMailboxSync(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	app := &App{}
	_, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)})
	if err != nil {
		t.Fatal(err)
	}
	sender, err := dbFindSender(ctx.AppDB(), "test-proj", "email", "support@example.com")
	if err != nil || sender == nil || sender.Provider != "gmail" || sender.ProviderConnectionID != 3 {
		t.Fatalf("sender: %+v, %v", sender, err)
	}
	sent, err := app.toolSendMessage(ctx, map[string]any{"channel": "email", "from": "support@example.com", "to": "customer@example.org", "bcc": "audit@example.com", "body": "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if got := sent.(map[string]any)["message_id_header"]; got != "<apteva-message-1@apteva.local>" {
		t.Fatalf("Gmail RFC Message-ID=%v", got)
	}
	var sentRaw string
	for _, call := range platform.executeCalls {
		if call.Tool == "send_raw_email" {
			if call.ConnID != 3 {
				t.Fatalf("sent on connection %d", call.ConnID)
			}
			sentRaw, _ = call.Input["raw"].(string)
		}
	}
	raw, err := base64.RawURLEncoding.DecodeString(sentRaw)
	if err != nil || !strings.Contains(string(raw), "Bcc: audit@example.com") {
		t.Fatalf("raw Gmail MIME missing Bcc: %v", err)
	}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	} // establish current cursor
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	} // duplicate is idempotent
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE project_id='test-proj' AND provider_slug='gmail' AND provider_message_id='gmail-in-1'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("inbound count=%d", count)
	}
	var cursor string
	if err := ctx.AppDB().QueryRow(`SELECT history_id FROM gmail_sync_state WHERE project_id='test-proj' AND connection_id=3`).Scan(&cursor); err != nil || cursor != "11" {
		t.Fatalf("cursor=%s err=%v", cursor, err)
	}
}

func TestGmailSenderConnectionCannotBeStolen(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolSendMessage(ctx, map[string]any{"channel": "email", "from": "support@example.com", "connection_id": int64(1), "to": "person@example.org", "body": "Hello"}); err == nil {
		t.Fatal("expected connection mismatch")
	}
	if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: "test-proj", Channel: "email", Address: "support@example.com", Kind: "email_mailbox", Provider: "aws-ses", ProviderConnectionID: 1}); err == nil {
		t.Fatal("expected provider reassignment rejection")
	}
}

func TestGmailInboundRequiresProjectRecipientAndSkipsSent(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	if err := app.ingestGmailMessage(ctx, "other-project", 3, gmailMessageRef{ID: "gmail-in-1"}); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE project_id='other-project'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("cross-project messages=%d err=%v", count, err)
	}
	platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: support@example.com\r\nTo: support@example.com\r\n\r\nSelf copy")), "labelIds": []string{"SENT", "INBOX"}})
	if err := app.ingestGmailMessage(ctx, "test-proj", 3, gmailMessageRef{ID: "gmail-sent-copy"}); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE provider_message_id='gmail-sent-copy'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("SENT copy imported=%d err=%v", count, err)
	}
}

func TestGmailExpiredHistoryResyncsBeforeAdvancingCursor(t *testing.T) {
	platform := gmailTestPlatform()
	platform.replyByTool["list_history"] = &sdk.ExecuteResult{Success: false, Data: json.RawMessage(`{"error":{"code":404,"message":"History not found"}}`)}
	platform.replyByTool["list_messages"] = gmailTestReply(map[string]any{"messages": []map[string]any{{"id": "gmail-in-1", "threadId": "gmail-thread-in"}}})
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE provider_slug='gmail' AND provider_message_id='gmail-in-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("resync count=%d err=%v", count, err)
	}
}

func TestGmailTransportErrorKeepsOutcomePending(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	platform.executeErr = errors.New("connection reset")
	result, err := app.toolSendMessage(ctx, map[string]any{"channel": "email", "from": "support@example.com", "to": "person@example.org", "body": "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["status"] != "pending" {
		t.Fatalf("ambiguous send result: %v", result)
	}
}

func TestGmailForwardedMailUsesAuthenticatedMailboxNotVisibleRecipients(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: customer@example.net\r\nTo: original@elsewhere.example\r\nDelivered-To: forged@elsewhere.example\r\n\r\nForwarded")), "labelIds": []string{"INBOX"}})
	if err := app.ingestGmailMessage(ctx, "test-proj", 3, gmailMessageRef{ID: "gmail-forwarded"}); err != nil {
		t.Fatal(err)
	}
	var identity, envelope string
	if err := ctx.AppDB().QueryRow(`SELECT receiving_identity,envelope_recipients FROM messages WHERE provider_message_id='gmail-forwarded'`).Scan(&identity, &envelope); err != nil || identity != "support@example.com" || envelope != `["support@example.com"]` {
		t.Fatalf("Gmail delivery=%s %s %v", identity, envelope, err)
	}
	if len(platform.callAppCalls) != 1 || platform.callAppCalls[0].Input["receiving_identity"] != identity {
		t.Fatalf("Gmail consumer=%+v", platform.callAppCalls)
	}
}

func TestLegacyGmailJobCannotUseVisibleAliasToClaimAnotherProject(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	t.Setenv("APTEVA_PROJECT_ID", "")
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: "support@example.com", Kind: "email_mailbox", Provider: "gmail", ProviderConnectionID: 3, Verified: true})
	if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: "other-project", Channel: "email", Address: "alias@other.example", Kind: "email_mailbox", Provider: "gmail", ProviderConnectionID: 3, Verified: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "other-project", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	res, err := persistInbound(ctx, "other-project", "email", nil, `INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,envelope_recipients,provider_slug,provider_connection_id,status,route_status) VALUES('other-project','email','in','customer@example.net','["alias@other.example"]','["alias@other.example"]','gmail',3,'received','pending')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := processInboundJob(ctx, "other-project", id, false); err != nil {
		t.Fatal(err)
	}
	m, err := dbMessageGet(ctx.AppDB(), "other-project", id)
	if err != nil || m.RouteStatus != "quarantined" || len(platform.callAppCalls) != 0 {
		t.Fatalf("legacy Gmail escaped quarantine: %+v %v", m, err)
	}
}

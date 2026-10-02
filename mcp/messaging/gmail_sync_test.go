package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
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
			"list_messages":   gmailTestReply(map[string]any{"messages": []any{}}),
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

func TestGmailInboundRequiresProjectRecipientAndImportsSentAsOutbound(t *testing.T) {
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
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE provider_message_id='gmail-sent-copy' AND direction='out' AND status='sent' AND route_status='not_applicable'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("SENT copy imported=%d err=%v", count, err)
	}
	if len(platform.callAppCalls) != 0 {
		t.Fatal("sent copy was routed into CRM as inbound")
	}
}

func TestGmailExpiredHistoryRestartsResumableCatchup(t *testing.T) {
	platform := gmailTestPlatform()
	platform.replyByTool["list_history"] = gmailTestReply(map[string]any{"historyId": "11"})
	ctx := newTestCtx(t, platform)
	app := &App{}
	if _, err := app.toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	platform.replyByTool["get_profile"] = gmailTestReply(map[string]any{"emailAddress": "support@example.com", "historyId": "20"})
	platform.replyByTool["list_history"] = &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{"error":{"code":404,"message":"History not found"}}`)}
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var complete int
	var cursor string
	if err := ctx.AppDB().QueryRow(`SELECT history_id,backfill_complete FROM gmail_sync_state`).Scan(&cursor, &complete); err != nil || cursor != "20" || complete != 0 {
		t.Fatalf("expired cursor did not schedule recovery: %s %d %v", cursor, complete, err)
	}
	platform.replyByTool["list_history"] = gmailTestReply(map[string]any{"historyId": "21"})
	platform.replyByTool["list_messages"] = gmailTestReply(map[string]any{"messages": []gmailMessageRef{{ID: "gmail-in-1", ThreadID: "gmail-thread-in"}}})
	if err := (&App{}).syncGmailMailboxes(ctx); err != nil {
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

func createGmailTestSender(t *testing.T, ctx *sdk.AppCtx) {
	t.Helper()
	if _, err := (&App{}).toolSendersCreate(ctx, map[string]any{"address": "support@example.com", "connection_id": int64(3)}); err != nil {
		t.Fatal(err)
	}
}

func TestGmailSDKWorkerBackfillsExistingMailboxAndPreservesDates(t *testing.T) {
	platform := gmailTestPlatform()
	platform.replyByTool["list_history"] = gmailTestReply(map[string]any{"historyId": "11"})
	platform.replyByTool["list_messages"] = gmailTestReply(map[string]any{"messages": []gmailMessageRef{{ID: "old-in"}, {ID: "old-sent"}, {ID: "apteva-sent"}}})
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
	createGmailTestSender(t, ctx)
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	// An existing connection upgrading from .56 gets a catch-up too.
	if _, err := ctx.AppDB().Exec(`INSERT INTO gmail_sync_state(project_id,connection_id,mailbox,history_id) VALUES('test-proj',3,'support@example.com','10')`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_slug,provider_connection_id,provider_message_id) VALUES('test-proj','email','out','support@example.com','sent','gmail',3,'apteva-sent')`); err != nil {
		t.Fatal(err)
	}
	platform.executeOverride = func(tool string, prior int) *sdk.ExecuteResult {
		if tool != "get_raw_message" {
			return nil
		}
		if prior == 0 {
			return gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: customer@example.org\r\nTo: support@example.com\r\nDate: Thu, 1 Oct 2026 16:00:00 +0000\r\nMessage-ID: <old-in@example.org>\r\n\r\nYesterday")), "internalDate": "1790870400000", "labelIds": []string{"INBOX"}})
		}
		return gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: support@example.com\r\nTo: customer@example.org\r\nCc: cc@example.org\r\nBcc: audit@example.org\r\nDate: Thu, 1 Oct 2026 16:01:00 +0000\r\nMessage-ID: <old-sent@example.org>\r\n\r\nSent yesterday")), "internalDate": "1790870460000", "threadId": "sent-thread", "labelIds": []string{"SENT"}})
	}
	app := &App{}
	var worker sdk.Worker
	for _, w := range app.Workers() {
		if w.Name == "gmail-mailbox-sync" {
			worker = w
		}
	}
	if worker.Run == nil || worker.Schedule != "@every 2m" {
		t.Fatal("Gmail sync is not an SDK periodic worker")
	}
	for tick := 0; tick < 2; tick++ {
		if err := worker.Run(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	var complete, count int
	if err := ctx.AppDB().QueryRow(`SELECT backfill_complete FROM gmail_sync_state`).Scan(&complete); err != nil || complete != 1 {
		t.Fatalf("backfill complete=%d %v", complete, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("deduplicated mailbox count=%d %v", count, err)
	}
	var incomingAt, sentAt, direction, bcc, thread string
	if err := ctx.AppDB().QueryRow(`SELECT received_at FROM messages WHERE provider_message_id='old-in'`).Scan(&incomingAt); err != nil || incomingAt != "2026-10-01T16:00:00Z" {
		t.Fatalf("inbound date=%s %v", incomingAt, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT sent_at,direction,bcc_addrs,provider_thread_id FROM messages WHERE provider_message_id='old-sent'`).Scan(&sentAt, &direction, &bcc, &thread); err != nil || sentAt != "2026-10-01T16:01:00Z" || direction != "out" || bcc != `["audit@example.org"]` || thread != "sent-thread" {
		t.Fatalf("sent record=%s %s %s %s %v", sentAt, direction, bcc, thread, err)
	}
	if len(platform.callAppCalls) != 1 || platform.callAppCalls[0].Input["from"] != "customer@example.org" {
		t.Fatalf("only incoming mail should reach CRM: %+v", platform.callAppCalls)
	}
	for _, call := range platform.executeCalls {
		if call.Tool == "send_raw_email" || call.Tool == "send_email" {
			t.Fatal("sync resent mail")
		}
		if call.Tool == "list_messages" && (strings.Contains(call.Input["q"].(string), "-in:sent") || call.Input["maxResults"] != 100) {
			t.Fatalf("unbounded or inbound-only backfill: %+v", call.Input)
		}
	}
	if len(recorder.EventsByTopic("message.sent")) != 0 || len(recorder.EventsByTopic("message.received")) != 1 || len(recorder.EventsByTopic("message.event")) != 1 {
		t.Fatalf("historical send emitted business delivery events: %+v", recorder.Events())
	}
}

func TestGmailBackfillResumesAcrossTicksWithoutBlockingFreshHistory(t *testing.T) {
	platform := gmailTestPlatform()
	platform.executeOverride = func(tool string, prior int) *sdk.ExecuteResult {
		if tool != "list_messages" {
			return nil
		}
		if prior == 0 {
			return gmailTestReply(map[string]any{"messages": []gmailMessageRef{{ID: "old-1"}}, "nextPageToken": "page-2"})
		}
		if prior == 1 {
			return &sdk.ExecuteResult{Success: false, Status: 503, Data: json.RawMessage(`{"error":"temporary"}`)}
		}
		return gmailTestReply(map[string]any{"messages": []gmailMessageRef{{ID: "old-1"}, {ID: "old-2"}}})
	}
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	if err := (&App{}).syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var token, cursor string
	var until int64
	if err := ctx.AppDB().QueryRow(`SELECT backfill_page_token,history_id,backfill_until FROM gmail_sync_state`).Scan(&token, &cursor, &until); err != nil || token != "page-2" || cursor != "11" {
		t.Fatalf("first page/history: %s %s %v", token, cursor, err)
	}
	if err := (&App{}).syncGmailMailboxes(ctx); err == nil {
		t.Fatal("expected backfill error")
	}
	if err := (&App{}).syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var complete, count int
	if err := ctx.AppDB().QueryRow(`SELECT backfill_complete,backfill_page_token FROM gmail_sync_state`).Scan(&complete, &token); err != nil || complete != 1 || token != "" {
		t.Fatalf("finished backfill=%d %s %v", complete, token, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 3 {
		t.Fatalf("partial page duplicated mail or missed live history: %d %v", count, err)
	}
	var queries []string
	for _, call := range platform.executeCalls {
		if call.Tool == "list_messages" {
			queries = append(queries, call.Input["q"].(string))
			if len(queries) > 1 && call.Input["pageToken"] != "page-2" {
				t.Fatalf("lost durable page token: %+v", call.Input)
			}
		}
	}
	if len(queries) != 3 || queries[0] != queries[1] || queries[1] != queries[2] || until == 0 {
		t.Fatalf("backfill window moved: %v", queries)
	}
}

func TestGmailInitialBackfillFailureKeepsCatchupPendingAndPollsFreshMail(t *testing.T) {
	platform := gmailTestPlatform()
	platform.replyByTool["list_messages"] = &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":"missing read scope"}`)}
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	if err := (&App{}).syncGmailMailboxes(ctx); err == nil {
		t.Fatal("expected missing read scope")
	}
	var cursor, lastError string
	var complete int
	if err := ctx.AppDB().QueryRow(`SELECT history_id,backfill_complete,last_error FROM gmail_sync_state`).Scan(&cursor, &complete, &lastError); err != nil || cursor != "11" || complete != 0 || !strings.Contains(lastError, "missing read scope") {
		t.Fatalf("lost cursor/error: %s %d %s %v", cursor, complete, lastError, err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE provider_message_id='gmail-in-1'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("historical failure blocked fresh mail: %d %v", count, err)
	}
	platform.replyByTool["list_messages"] = gmailTestReply(map[string]any{})
	if err := (&App{}).syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT history_id,last_error FROM gmail_sync_state`).Scan(&cursor, &lastError); err != nil || cursor != "11" || lastError != "" {
		t.Fatalf("recovered cursor/error: %s %s %v", cursor, lastError, err)
	}
}

func TestGmailSentLabelTransitionAndDraftSpamTrashSkipping(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	app := &App{}
	for _, label := range []string{"DRAFT", "SPAM", "TRASH"} {
		platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": "not MIME", "labelIds": []string{label}})
		if err := app.ingestGmailMessage(ctx, "test-proj", 3, gmailMessageRef{ID: "draft-1"}); err != nil {
			t.Fatal(err)
		}
	}
	platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: support@example.com\r\nTo: customer@example.org\r\n\r\nAlready sent")), "labelIds": []string{"SENT"}})
	platform.replyByTool["list_history"] = gmailTestReply(map[string]any{"historyId": "12", "history": []any{map[string]any{"labelsAdded": []any{map[string]any{"message": gmailMessageRef{ID: "draft-1"}, "labelIds": []string{"SENT"}}}}}})
	if err := app.syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages WHERE direction='out'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("draft transition not synced: %d %v", count, err)
	}
	if len(platform.callAppCalls) != 0 {
		t.Fatal("sent history entered inbound routing")
	}
}

func TestGmailSentJobRevalidatesOwnershipAndCannotEnterInboundRouting(t *testing.T) {
	platform := gmailTestPlatform()
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
	createGmailTestSender(t, ctx)
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	res, err := persistInbound(ctx, "test-proj", "gmail-sent", nil, `INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,provider_slug,provider_connection_id) VALUES('test-proj','email','out','support@example.com','["customer@example.org"]','sent','gmail',3)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := ctx.AppDB().Exec(`UPDATE senders SET deleted_at=CURRENT_TIMESTAMP`); err != nil {
		t.Fatal(err)
	}
	if err := processInboundJob(ctx, "test-proj", id, false); err != nil {
		t.Fatal(err)
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.RouteStatus != "quarantined" {
		t.Fatalf("revoked sent import=%+v %v", m, err)
	}
	if err := dispatchInbound(ctx, "test-proj", m); err == nil {
		t.Fatal("outbound import entered inbound dispatch")
	}
	if len(platform.callAppCalls) != 0 || len(recorder.Events()) != 0 {
		t.Fatal("quarantined sent message escaped into consumers/events")
	}
}

func TestGmailMessageTime(t *testing.T) {
	for _, tt := range []struct{ millis, date, want string }{
		{"1790870400000", "Fri, 2 Oct 2026 12:00:00 +0000", "2026-10-01T16:00:00Z"},
		{"", "Thu, 1 Oct 2026 18:00:00 +0200", "2026-10-01T16:00:00Z"},
		{"invalid", "invalid", "2026-10-02T12:00:00Z"},
	} {
		if got := gmailMessageTime(tt.millis, map[string]string{"Date": tt.date}, "2026-10-02T12:00:00Z"); got != tt.want {
			t.Fatalf("time=%s want=%s", got, tt.want)
		}
	}
	if gmailBackfillWindow != 7*24*time.Hour {
		t.Fatal("unexpected automatic catch-up window")
	}
}

func TestGmailDownstreamFailureDoesNotStallMailboxAndRecoveryRetries(t *testing.T) {
	platform := gmailTestPlatform()
	platform.callAppResultErr = errors.New("CRM offline")
	platform.replyByTool["list_messages"] = gmailTestReply(map[string]any{"messages": []gmailMessageRef{{ID: "gmail-in-1"}}})
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).syncGmailMailboxes(ctx); err != nil {
		t.Fatal(err)
	}
	var job, cursor string
	var complete int
	if err := ctx.AppDB().QueryRow(`SELECT status FROM inbound_jobs`).Scan(&job); err != nil || job != "pending" {
		t.Fatalf("lost durable retry: %s %v", job, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT history_id,backfill_complete FROM gmail_sync_state`).Scan(&cursor, &complete); err != nil || cursor != "11" || complete != 1 {
		t.Fatalf("downstream failure stalled mailbox: %s %d %v", cursor, complete, err)
	}
	platform.callAppResultErr = nil
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT status FROM inbound_jobs`).Scan(&job); err != nil || job != "done" {
		t.Fatalf("recovery did not deliver: %s %v", job, err)
	}
}

func TestGmailSentAttachmentJobRecoversWithoutSendingOrInboundDispatch(t *testing.T) {
	platform := gmailTestPlatform()
	platform.bindingsOverride["storage"] = float64(2)
	platform.callAppResultErr = errors.New("storage offline")
	platform.callAppReply = json.RawMessage(`{"id":901,"name":"file.txt","content_type":"text/plain","size_bytes":5}`)
	platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: support@example.com\r\nTo: customer@example.org\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=b\r\n\r\n--b\r\nContent-Type: text/plain\r\n\r\nSee file\r\n--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=file.txt\r\nContent-Transfer-Encoding: base64\r\n\r\naGVsbG8=\r\n--b--\r\n")), "labelIds": []string{"SENT"}})
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	if err := (&App{}).ingestGmailMessage(ctx, "test-proj", 3, gmailMessageRef{ID: "sent-file"}); err != nil {
		t.Fatal(err)
	}
	var id int64
	var job, source string
	if err := ctx.AppDB().QueryRow(`SELECT message_id,status,source FROM inbound_jobs`).Scan(&id, &job, &source); err != nil || job != "pending" || !strings.Contains(source, "aGVsbG8=") {
		t.Fatalf("sent attachment source lost: %s %s %v", job, source, err)
	}
	platform.callAppResultErr = nil
	if _, err := ctx.AppDB().Exec(`UPDATE inbound_jobs SET next_attempt=0`); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.Direction != "out" || m.Status != "sent" || m.RouteStatus != "not_applicable" || len(m.Attachments) != 1 || m.Attachments[0].StorageID != 901 {
		t.Fatalf("sent attachment not recovered: %+v %v", m, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT status,source FROM inbound_jobs`).Scan(&job, &source); err != nil || job != "done" || source != "{}" {
		t.Fatalf("completed import retained raw bytes: %s %s %v", job, source, err)
	}
	for _, call := range platform.callAppCalls {
		if call.App != "storage" || call.Tool != "files_upload" {
			t.Fatalf("sent import reached a consumer: %+v", call)
		}
	}
}

func TestGmailWorkerRespectsSDKProjectDispatch(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	t.Setenv("APTEVA_PROJECT_ID", "")
	if err := (&App{}).syncGmailMailboxes(ctx.WithProject("different-project")); err != nil {
		t.Fatal(err)
	}
	for _, call := range platform.executeCalls {
		if call.Tool == "get_profile" || call.Tool == "list_messages" || call.Tool == "list_history" {
			t.Fatal("SDK project dispatch synced another project's mailbox")
		}
	}
}

func TestGmailSyncReconcilesAnAmbiguousAptevaSendWithoutDuplicateOrResend(t *testing.T) {
	platform := gmailTestPlatform()
	ctx := newTestCtx(t, platform)
	createGmailTestSender(t, ctx)
	platform.replyByTool["get_raw_message"] = gmailTestReply(map[string]any{"raw": base64.RawURLEncoding.EncodeToString([]byte("From: support@example.com\r\nTo: customer@example.org\r\nMessage-ID: <apteva-pending@apteva.local>\r\n\r\nAlready delivered")), "threadId": "actual-thread", "labelIds": []string{"SENT"}})
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,body_text,status,status_reason,provider_slug,provider_connection_id,message_id_header) VALUES('test-proj','email','out','support@example.com','["customer@example.org"]','Original body','pending','provider outcome unknown','gmail',3,'<apteva-pending@apteva.local>')`); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := (&App{}).ingestGmailMessage(ctx, "test-proj", 3, gmailMessageRef{ID: "actual-provider-id"}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	var body, providerID, thread, status string
	if err := ctx.AppDB().QueryRow(`SELECT count(*),body_text,provider_message_id,provider_thread_id,status FROM messages`).Scan(&count, &body, &providerID, &thread, &status); err != nil || count != 1 || body != "Original body" || providerID != "actual-provider-id" || thread != "actual-thread" || status != "sent" {
		t.Fatalf("ambiguous send was lost/duplicated: %d %s %s %s %s %v", count, body, providerID, thread, status, err)
	}
	for _, call := range platform.executeCalls {
		if strings.HasPrefix(call.Tool, "send_") {
			t.Fatal("ambiguous send was resent")
		}
	}
}

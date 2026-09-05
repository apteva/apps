package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"testing"
)

func insertJob(t *testing.T, ctx *sdk.AppCtx, source any) int64 {
	t.Helper()
	res, err := persistInbound(ctx, "test-proj", "email", source, `INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,route_status) VALUES('test-proj','email','in','alice@example.com','["support@example.com"]','received','pending')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}
func TestInboundCommitRollsBackWhenJobCannotBeSaved(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_jobs BEFORE INSERT ON inbound_jobs BEGIN SELECT RAISE(ABORT,'disk unavailable'); END`)
	if err != nil {
		t.Fatal(err)
	}
	_, err = persistInbound(ctx, "test-proj", "email", nil, `INSERT INTO messages(project_id,channel,direction,from_addr,status) VALUES('test-proj','email','in','a@example.com','received')`)
	if err == nil {
		t.Fatal("expected job failure")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count)
	if count != 0 {
		t.Fatal("message committed without durable job")
	}
}
func TestWebhookAcknowledgesDurableWorkBeforeDispatch(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	app := &App{}
	_, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "support@example.com", "tickets", "receive", 10)
	if err != nil {
		t.Fatal(err)
	}
	inner := mustJSON(map[string]any{"notificationType": "Received", "content": "From: alice@example.com\r\nTo: support@example.com\r\nSubject: Hello\r\n\r\nMessage", "mail": map[string]any{"messageId": "durable-1"}, "receipt": map[string]any{"recipients": []string{"support@example.com"}}})
	body := mustJSON(map[string]any{"Type": "Notification", "TopicArn": testSNSTopicARN, "MessageId": "sns-durable", "Message": string(inner)})
	post := func() {
		r := httptest.NewRequest("POST", "/webhooks/ses-inbound?project_id=test-proj", bytes.NewReader(body))
		signTestSNSRequest(r, body)
		w := httptest.NewRecorder()
		app.handleInboundWebhook(w, r)
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	post()
	if len(plat.callAppCalls) != 0 {
		t.Fatal("webhook performed downstream work before ACK")
	}
	var id int64
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT message_id,status FROM inbound_jobs`).Scan(&id, &status); err != nil || status != "pending" {
		t.Fatalf("job %s %v", status, err)
	}
	// A fresh App instance models restart: no in-memory handoff is necessary.
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(plat.callAppCalls) != 1 || plat.callAppCalls[0].Tool != "receive" {
		t.Fatalf("dispatches %+v", plat.callAppCalls)
	}
	key := plat.callAppCalls[0].Input["idempotency_key"]
	if key == "" {
		t.Fatal("missing stable target key")
	}
	post()
	if err := app.retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(plat.callAppCalls) != 1 {
		t.Fatal("duplicate webhook dispatched a completed job")
	}
	var source string
	ctx.AppDB().QueryRow(`SELECT source,status FROM inbound_jobs WHERE message_id=?`, id).Scan(&source, &status)
	if source != "{}" || status != "done" {
		t.Fatalf("completed job retained source: %s %s", source, status)
	}
}
func TestInboundWorkerRetriesWithBackoffAndStops(t *testing.T) {
	plat := &stubPlatform{callAppResultErr: errors.New("target offline")}
	ctx := newTestCtx(t, plat)
	dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "support@example.com", "tickets", "receive", 0)
	id := insertJob(t, ctx, []providerAttachment{})
	for attempt := 1; attempt <= 8; attempt++ {
		if err := processInboundJob(ctx, "test-proj", id, false); err == nil {
			t.Fatal("expected target error")
		}
		var attempts, next int
		var status string
		ctx.AppDB().QueryRow(`SELECT attempts,next_attempt,status FROM inbound_jobs WHERE message_id=?`, id).Scan(&attempts, &next, &status)
		if attempts != attempt || next == 0 {
			t.Fatalf("attempts=%d next=%d", attempts, next)
		}
		if attempt == 8 && status != "failed" {
			t.Fatalf("status=%s", status)
		}
		if err := processInboundJob(ctx, "test-proj", id, false); err != nil {
			t.Fatal(err)
		}
		if len(plat.callAppCalls) != attempt {
			t.Fatal("backoff ignored")
		}
		ctx.AppDB().Exec(`UPDATE inbound_jobs SET next_attempt=0 WHERE message_id=?`, id)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(plat.callAppCalls) != 8 {
		t.Fatal("exhausted job retried automatically")
	}
	plat.callAppResultErr = nil
	if err := processInboundJob(ctx, "test-proj", id, true); err != nil {
		t.Fatal(err)
	}
	if len(plat.callAppCalls) != 9 {
		t.Fatal("manual retry did not resume")
	}
	if plat.callAppCalls[0].Input["idempotency_key"] != plat.callAppCalls[8].Input["idempotency_key"] {
		t.Fatal("retry changed consumer identity")
	}
}
func TestActiveJobLeasePreventsSecondClaim(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	id := insertJob(t, ctx, []providerAttachment{})
	ctx.AppDB().Exec(`UPDATE inbound_jobs SET status='running',lease_until=9999999999,attempts=1 WHERE message_id=?`, id)
	if err := processInboundJob(ctx, "test-proj", id, true); err != nil {
		t.Fatal(err)
	}
	var attempts int
	ctx.AppDB().QueryRow(`SELECT attempts FROM inbound_jobs WHERE message_id=?`, id).Scan(&attempts)
	if attempts != 1 {
		t.Fatal("active job reclaimed")
	}
}
func TestVirusVerdictQuarantinesBeforeTarget(t *testing.T) {
	plat := &stubPlatform{}
	ctx := newTestCtx(t, plat)
	id := insertJob(t, ctx, []providerAttachment{})
	dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "support@example.com", "tickets", "receive", 0)
	ctx.AppDB().Exec(`UPDATE messages SET verdicts='{"virus":"FAIL"}' WHERE id=?`, id)
	if err := processInboundJob(ctx, "test-proj", id, false); err != nil {
		t.Fatal(err)
	}
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if m.RouteStatus != "quarantined" || len(plat.callAppCalls) != 0 {
		t.Fatalf("quarantine=%+v", m)
	}
}
func TestEarlyCallbacksReplayWithoutUnknownIDStarvation(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	events := []providerEvent{}
	for i := 0; i < 205; i++ {
		events = append(events, providerEvent{ProviderMessageID: fmt.Sprintf("unknown-%d", i), ProviderEventID: fmt.Sprintf("event-%d", i), Kind: "delivered"})
	}
	events = append(events, providerEvent{ProviderMessageID: "ses-msg-123", ProviderEventID: "early-delivery", Kind: "delivered", Recipient: "alice@example.com", OccurredAt: "2026-09-05T10:00:00Z"})
	if err := queueProviderEvents(ctx, "test-proj", "", events); err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": "alice@example.com", "body": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(int64)
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.Status != "delivered" {
		t.Fatalf("replay: %+v %v", m, err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM unmatched_provider_events`).Scan(&count)
	if count != 205 {
		t.Fatalf("pending=%d", count)
	}
}
func TestPerRecipientOutcomeDoesNotMarkEveryoneBounced(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "email", "from": fromAcme, "to": []string{"alice@example.com", "bob@example.com"}, "body": "hello"})
	if err != nil {
		t.Fatal(err)
	}
	id := out.(map[string]any)["id"].(int64)
	m, _ := dbMessageGet(ctx.AppDB(), "test-proj", id)
	for i, ev := range []providerEvent{{Kind: "bounced", Recipient: "alice@example.com"}, {Kind: "delivered", Recipient: "bob@example.com"}} {
		ev.ProviderEventID = fmt.Sprint(i)
		if _, err := persistAndEmitProviderEvent(ctx, m, ev); err != nil {
			t.Fatal(err)
		}
	}
	m, _ = dbMessageGet(ctx.AppDB(), "test-proj", id)
	if m.RecipientStatuses["alice@example.com"] != "bounced" || m.RecipientStatuses["bob@example.com"] != "delivered" {
		t.Fatalf("outcomes=%v", m.RecipientStatuses)
	}
	_, total, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Status: "bounced", Limit: 10})
	if err != nil || total != 1 {
		t.Fatalf("status filter %d %v", total, err)
	}
}
func TestIndexedSearchAndSummaryPreserveLiteralSemantics(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	body := strings.Repeat("padding ", 10000) + `Needle_100% "quoted"` + strings.Repeat(" end", 10000)
	_, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,body_text,body_html) VALUES('test-proj','email','out','"Support" <support@example.com>','["alice@example.com"]','sent',?,?)`, body, body)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{"Needle_100%", `"quoted"`, "alice@example.com", "support@example.com"} {
		rows, total, err := dbMessageListPage(ctx.AppDB(), "test-proj", messageListOpts{Q: q, Summary: true, Limit: 10})
		if err != nil || total != 1 {
			t.Fatalf("query=%q count=%d err=%v", q, total, err)
		}
		if len(rows[0].BodyText) > 300 || rows[0].BodyHTML != "" {
			t.Fatal("summary returned full body")
		}
		full, _ := json.Marshal(rows[0])
		if len(full) > 3000 {
			t.Fatalf("summary %d bytes", len(full))
		}
	}
}

func TestAttachmentRetryUpdatesMetadataWithoutChangingIdentity(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	id := insertJob(t, ctx, []providerAttachment{})
	input := providerAttachment{MessageAttachment: MessageAttachment{ProviderRef: "part-1", Filename: "document.txt", ContentType: "text/plain", ProcessingStatus: "failed", ProcessingError: "storage offline"}}
	if err := dbInsertMessageAttachments(ctx.AppDB(), "test-proj", id, []providerAttachment{input}); err != nil {
		t.Fatal(err)
	}
	before := dbMessageAttachments(ctx.AppDB(), "test-proj", id)
	if len(before) != 1 {
		t.Fatal("missing failed metadata")
	}
	input.StorageID = 123
	input.ProcessingStatus = "ready"
	input.ProcessingError = ""
	input.SizeBytes = 8
	if err := dbInsertMessageAttachments(ctx.AppDB(), "test-proj", id, []providerAttachment{input}); err != nil {
		t.Fatal(err)
	}
	after := dbMessageAttachments(ctx.AppDB(), "test-proj", id)
	if len(after) != 1 || after[0].ID != before[0].ID || after[0].StorageID != 123 || after[0].ProcessingStatus != "ready" || after[0].ProcessingError != "" {
		t.Fatalf("retry metadata=%+v", after)
	}
}
func TestAttachmentDownloadRequiresMessageAndProjectOwnership(t *testing.T) {
	plat := &stubPlatform{callAppReplyByTool: map[string]json.RawMessage{"files_get": json.RawMessage(`{"found":true,"file":{"name":"doc.txt","content_type":"text/plain","size_bytes":8}}`), "files_get_url": json.RawMessage(`{"url":"https://files.example.com/doc.txt"}`)}}
	ctx := newTestCtx(t, plat)
	t.Setenv("APTEVA_PROJECT_ID", "")
	id := insertJob(t, ctx, []providerAttachment{})
	if err := dbInsertMessageAttachments(ctx.AppDB(), "test-proj", id, []providerAttachment{{MessageAttachment: MessageAttachment{StorageID: 123, ProviderRef: "part-1", Filename: "doc.txt", ProcessingStatus: "ready"}}}); err != nil {
		t.Fatal(err)
	}
	att := dbMessageAttachments(ctx.AppDB(), "test-proj", id)[0]
	for _, tc := range []struct {
		pid                 string
		message, attachment int64
		want                int
	}{{"test-proj", id, att.ID, 200}, {"other-project", id, att.ID, 404}, {"test-proj", id, att.ID + 1, 404}, {"test-proj", id + 1, att.ID, 404}} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("GET", fmt.Sprintf("/messages/%d/attachments/%d?project_id=%s", tc.message, tc.attachment, tc.pid), nil)
		(&App{}).handleMessageItem(w, r)
		if w.Code != tc.want {
			t.Fatalf("%s => %d: %s", r.URL, w.Code, w.Body.String())
		}
	}
	if len(plat.callAppCalls) != 2 {
		t.Fatalf("unauthorized request reached storage: %+v", plat.callAppCalls)
	}
}

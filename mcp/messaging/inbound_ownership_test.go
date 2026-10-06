package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"reflect"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func seedSESRecipient(t *testing.T, ctx *sdk.AppCtx, address string) {
	t.Helper()
	preseedSender(t, ctx, senderUpsert{Channel: "email", Address: address, Kind: "email_mailbox", Provider: "aws-ses", Verified: true})
}

func postOwnershipSES(t *testing.T, inner map[string]any, snsID, hint string) *httptest.ResponseRecorder {
	t.Helper()
	body := mustJSON(map[string]any{"Type": "Notification", "TopicArn": testSNSTopicARN, "MessageId": snsID, "Message": string(mustJSON(inner))})
	r := httptest.NewRequest("POST", "/webhooks/ses-inbound"+hint, bytes.NewReader(body))
	signTestSNSRequest(r, body)
	w := httptest.NewRecorder()
	(&App{}).handleInboundWebhook(w, r)
	return w
}

func ownershipEmail(providerID, raw string, recipients []string) map[string]any {
	return map[string]any{"notificationType": "Received", "mail": map[string]any{"messageId": providerID}, "content": raw, "receipt": map[string]any{"recipients": recipients}}
}

const forwardedEmail = "Received: from mx.example.net (mx.example.net [192.0.2.1])\r\n\tby inbound-smtp.eu-west-1.amazonaws.com with SMTP id abc\r\n\tfor <Contact@alpha.example>; Fri, 2 Oct 2026 12:00:00 +0000\r\n" +
	"Received: from original.example by old.example for <original@beta.example>; Fri, 2 Oct 2026 11:00:00 +0000\r\n" +
	"From: customer@example.net\r\nTo: original@beta.example\r\nCc: other@beta.example\r\nSubject: Forwarded\r\n\r\nHello"

func TestSMTPRecipientEvidence(t *testing.T) {
	for _, tt := range []struct {
		name, header string
		want         []string
	}{
		{"SES angle", "from mx by inbound-smtp.eu-west-1.amazonaws.com with SMTP for <USER@alpha.example>; date", []string{"user@alpha.example"}},
		{"SES bare", "from mx by inbound-smtp.us-east-1.amazonaws.com for user@alpha.example; date", []string{"user@alpha.example"}},
		{"untrusted host", "from mx by inbound-smtp.eu-west-1.amazonaws.com.evil.example for user@alpha.example; date", nil},
		{"comment forgery", "from mx (by inbound-smtp.eu-west-1.amazonaws.com) by evil.example for user@alpha.example; date", nil},
		{"later by forgery", "from mx by evil.example by inbound-smtp.eu-west-1.amazonaws.com for user@alpha.example; date", nil},
		{"no recipient", "from mx by inbound-smtp.eu-west-1.amazonaws.com with SMTP; date", nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := receivedSMTPRecipients(tt.header); !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
	parsed, err := parseRawEml([]byte(forwardedEmail), "")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(parsed.EnvelopeRecipients, []string{"contact@alpha.example"}) {
		t.Fatalf("folded outermost hop: %v", parsed.EnvelopeRecipients)
	}
	// Never fall through an untrusted outer hop to a forged lower SES hop.
	parsed, err = parseRawEml([]byte("Received: from attacker by proxy.example; date\r\n"+forwardedEmail), "")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := sesSMTPRecipients(parsed, &sesInboundEnvelope{}); len(got) != 0 {
		t.Fatalf("trusted lower hop: %v", got)
	}
	env := &sesInboundEnvelope{}
	env.Mail.Destination = []string{"destination@alpha.example"}
	env.Receipt.Recipients = []string{"receipt@alpha.example"}
	got, err := sesSMTPRecipients(parsed, env)
	if err != nil || !reflect.DeepEqual(got, []string{"receipt@alpha.example"}) {
		t.Fatalf("receipt precedence: %v %v", got, err)
	}
	env.Receipt.Recipients = nil
	got, err = sesSMTPRecipients(parsed, env)
	if err != nil || !reflect.DeepEqual(got, env.Mail.Destination) {
		t.Fatalf("destination precedence: %v %v", got, err)
	}
}

func TestSESForwardedEmailOwnershipAndProjectFanout(t *testing.T) {
	platform := &stubPlatform{}
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
	t.Setenv("APTEVA_PROJECT_ID", "")
	seedSESRecipient(t, ctx, "contact@alpha.example")
	if _, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{ProjectID: "other-project", Kind: "email_domain", Address: "beta.example", Provider: "aws-ses"}); err != nil {
		t.Fatal(err)
	}
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	inner := ownershipEmail("ses-forwarded", forwardedEmail, nil)
	for _, hint := range []string{"?project_id=other-project", "?project_id=third-project"} {
		w := postOwnershipSES(t, inner, "sns-forwarded", hint)
		if w.Code != 403 {
			t.Fatalf("fanout accepted: %d %s", w.Code, w.Body.String())
		}
	}
	for _, snsID := range []string{"sns-forwarded", "sns-forwarded", "sns-other-subscription"} {
		w := postOwnershipSES(t, inner, snsID, "")
		if w.Code != 200 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
	}
	if len(recorder.EventsByTopic("message.received")) != 0 {
		t.Fatal("receive event published before safety checks")
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("global message count=%d %v", count, err)
	}
	rows, err := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 20})
	if err != nil || len(rows) != 1 {
		t.Fatalf("messages=%d %v", len(rows), err)
	}
	m := rows[0]
	if m.ProjectID != "test-proj" || m.ReceivingIdentity != "contact@alpha.example" || m.MatchedRecipient != m.ReceivingIdentity || m.RouteStatus != "ok" || !reflect.DeepEqual(m.EnvelopeRecipients, []string{m.ReceivingIdentity}) {
		t.Fatalf("incorrect ownership: %+v", m)
	}
	if len(platform.callAppCalls) != 1 {
		t.Fatalf("consumer calls=%d", len(platform.callAppCalls))
	}
	payload := platform.callAppCalls[0].Input
	if !reflect.DeepEqual(payload["to"], []string{"contact@alpha.example"}) || !reflect.DeepEqual(payload["header_to"], []string{"original@beta.example"}) || len(payload["cc"].([]string)) != 0 {
		t.Fatalf("canonical/header split: %+v", payload)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	for _, topic := range []string{"message.received", "message.processed"} {
		events := recorder.EventsByTopic(topic)
		if len(events) != 1 || events[0].ProjectID != "test-proj" {
			t.Fatalf("%s: %+v", topic, events)
		}
	}
}

func TestSESRejectsUnownedAmbiguousAndProjectScopedDelivery(t *testing.T) {
	for _, scenario := range []string{"visible-to-only", "unknown-envelope", "cross-project", "conflicting-domain", "scoped-mismatch", "invalid-envelope"} {
		t.Run(scenario, func(t *testing.T) {
			platform := &stubPlatform{}
			recorder := tk.NewEmitRecorder()
			ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
			seedSESRecipient(t, ctx, "contact@alpha.example")
			raw := "From: customer@example.net\r\nTo: contact@alpha.example\r\n\r\nhello"
			recipients := []string{"contact@alpha.example"}
			status := 422
			switch scenario {
			case "visible-to-only":
				recipients = nil
			case "unknown-envelope":
				recipients = []string{"unknown@other.example"}
			case "invalid-envelope":
				recipients = []string{"not an address"}
			case "cross-project", "conflicting-domain":
				domain := "beta.example"
				if scenario == "conflicting-domain" {
					domain = "alpha.example"
				} else {
					recipients = append(recipients, "second@beta.example")
				}
				if _, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{ProjectID: "other-project", Kind: "email_domain", Address: domain, Provider: "aws-ses"}); err != nil {
					t.Fatal(err)
				}
			case "scoped-mismatch":
				t.Setenv("APTEVA_PROJECT_ID", "other-project")
				status = 403
			}
			w := postOwnershipSES(t, ownershipEmail("rejected", raw, recipients), "sns-rejected", "?project_id=test-proj")
			if w.Code != status {
				t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
			}
			var count int
			if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 0 {
				t.Fatalf("rejected message persisted: %d %v", count, err)
			}
			if len(platform.callAppCalls) != 0 || len(recorder.EventsByTopic("message.received")) != 0 {
				t.Fatal("rejected message reached consumer")
			}
		})
	}
}

func TestUnsafeInboundJobsNeverMaterializeInboxOnRetry(t *testing.T) {
	for _, scenario := range []string{"suppressed", "no_match", "wrong-owner", "legacy-to-only", "virus", "legacy-received"} {
		t.Run(scenario, func(t *testing.T) {
			platform := &stubPlatform{}
			recorder := tk.NewEmitRecorder()
			ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
			seedSESRecipient(t, ctx, "contact@alpha.example")
			if scenario != "no_match" {
				if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
					t.Fatal(err)
				}
			}
			envelope, headers, verdicts := `["contact@alpha.example"]`, `{}`, `{}`
			want := "quarantined"
			switch scenario {
			case "suppressed":
				want = "suppressed"
				if err := dbSuppressionUpsertKind(ctx.AppDB(), "test-proj", "email", "address", "customer@example.net", "spam", "manual"); err != nil {
					t.Fatal(err)
				}
			case "no_match":
				want = "no_match"
			case "wrong-owner":
				envelope = `["contact@beta.example"]`
				if _, err := dbUpsertIdentity(ctx.AppDB(), &identityUpsert{ProjectID: "other-project", Kind: "email_domain", Address: "beta.example", Provider: "aws-ses"}); err != nil {
					t.Fatal(err)
				}
			case "legacy-to-only":
				envelope = `[]`
			case "legacy-received":
				want, envelope = "ok", `[]`
				parsed, err := parseRawEml([]byte(forwardedEmail), "")
				if err != nil {
					t.Fatal(err)
				}
				headers = string(mustJSON(parsed.Headers))
			case "virus":
				verdicts = `{"virus":"FAIL"}`
			}
			res, err := persistInbound(ctx, "test-proj", "email", nil, `INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,envelope_recipients,headers,verdicts,status,route_status,matched_recipient,route_target_app,route_target_route) VALUES('test-proj','email','in','customer@example.net','["contact@alpha.example"]',?,?,?,'received','ok','stale@old.example','crm','/inbound')`, envelope, headers, verdicts)
			if err != nil {
				t.Fatal(err)
			}
			id, _ := res.LastInsertId()
			for attempt := 0; attempt < 2; attempt++ {
				if err := processInboundJob(ctx, "test-proj", id, attempt > 0); err != nil {
					t.Fatal(err)
				}
			}
			m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
			if err != nil || m.RouteStatus != want {
				t.Fatalf("message=%+v %v", m, err)
			}
			if want == "ok" {
				if m.ReceivingIdentity != "contact@alpha.example" || !reflect.DeepEqual(m.EnvelopeRecipients, []string{m.ReceivingIdentity}) {
					t.Fatalf("legacy envelope not recovered: %+v", m)
				}
				return
			}
			if m.RouteTargetApp != "" || m.RouteTargetRoute != "" {
				t.Fatalf("stale consumer target retained: %+v", m)
			}
			if scenario == "suppressed" && m.ReceivingIdentity != "contact@alpha.example" {
				t.Fatal("suppression lost canonical receiving identity")
			}
			if len(platform.callAppCalls) != 0 {
				t.Fatal("unsafe message called consumer")
			}
			for _, topic := range []string{"message.received", "message.processed"} {
				if len(recorder.EventsByTopic(topic)) != 0 {
					t.Fatalf("unsafe %s event", topic)
				}
			}
		})
	}
}

func TestS3ForwardedDeliveryRecoversRecipientAndDeduplicatesObject(t *testing.T) {
	platform := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{"get_object": {Success: true, Data: json.RawMessage(mustJSON(map[string]string{"body": forwardedEmail}))}}}
	ctx := newTestCtx(t, platform)
	t.Setenv("APTEVA_PROJECT_ID", "")
	seedSESRecipient(t, ctx, "contact@alpha.example")
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	for _, providerID := range []string{"s3-first", "s3-second"} {
		inner := map[string]any{"notificationType": "Received", "mail": map[string]any{"messageId": providerID}, "receipt": map[string]any{"action": map[string]string{"type": "S3", "bucketName": "inbound-bucket", "objectKey": "raw-object"}}}
		w := postOwnershipSES(t, inner, "sns-"+providerID, "")
		if w.Code != 200 {
			t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
		}
		if err := (&App{}).retryMessagingWork(ctx); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := dbMessageList(ctx.AppDB(), "test-proj", messageListOpts{Direction: "in", Limit: 10})
	if err != nil || len(rows) != 1 || rows[0].S3Key != "inbound-bucket/raw-object" || rows[0].ReceivingIdentity != "contact@alpha.example" || rows[0].RouteStatus != "ok" {
		t.Fatalf("S3 messages=%+v %v", rows, err)
	}
	if len(platform.callAppCalls) != 1 {
		t.Fatalf("S3 consumer calls=%d", len(platform.callAppCalls))
	}
}

func TestSMTPRecipientsRejectsUnprovenSESMetadataReceived(t *testing.T) {
	env := &sesInboundEnvelope{}
	env.Mail.Headers = []struct{ Name, Value string }{
		{"Received", "from mx by inbound-smtp.eu-west-1.amazonaws.com for <contact@alpha.example>; date"},
		{"Received", "from mx by inbound-smtp.eu-west-1.amazonaws.com for <forged@beta.example>; date"},
	}
	got, err := sesSMTPRecipients(&parsedInbound{To: []string{"visible@beta.example"}}, env)
	if err != nil || len(got) != 0 {
		t.Fatalf("original header metadata established delivery: %v %v", got, err)
	}
	env.Mail.Headers[0].Value = "from mx by proxy.example; date"
	got, err = sesSMTPRecipients(nil, env)
	if err != nil || len(got) != 0 {
		t.Fatalf("lower metadata hop trusted: %v %v", got, err)
	}
}

func TestOwnershipDatabaseFailureIsRetryable(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	id := insertJob(t, ctx, nil)
	if _, err := ctx.AppDB().Exec(`ALTER TABLE senders RENAME TO unavailable_senders`); err != nil {
		t.Fatal(err)
	}
	if err := processInboundJob(ctx, "test-proj", id, false); err == nil {
		t.Fatal("expected retryable database error")
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM inbound_jobs WHERE message_id=?`, id).Scan(&status); err != nil || status != "pending" {
		t.Fatalf("job=%s %v", status, err)
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.RouteStatus == "quarantined" {
		t.Fatalf("temporary failure permanently quarantined: %+v %v", m, err)
	}
}

type hydratingInboundPlatform struct {
	stubPlatform
	ctx *sdk.AppCtx
	t   *testing.T
}

func (p *hydratingInboundPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	id := input["message_id"].(int64)
	m, err := dbMessageGet(p.ctx.AppDB(), "test-proj", id)
	if err != nil || m == nil || m.MatchedRecipient != "contact@alpha.example" || m.ReceivingIdentity != m.MatchedRecipient || m.RouteStatus != "routing" {
		p.t.Fatalf("consumer hydrated incomplete recipient metadata: %+v %v", m, err)
	}
	return p.stubPlatform.CallAppResult(app, tool, input, out)
}

func TestCanonicalRecipientIsStoredBeforeConsumerHydration(t *testing.T) {
	platform := &hydratingInboundPlatform{t: t}
	ctx := newTestCtx(t, nil, tk.WithPlatform(platform))
	platform.ctx = ctx
	seedSESRecipient(t, ctx, "contact@alpha.example")
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "email", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	w := postOwnershipSES(t, ownershipEmail("hydrate", forwardedEmail, nil), "sns-hydrate", "")
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(platform.callAppCalls) != 1 {
		t.Fatalf("calls=%d", len(platform.callAppCalls))
	}
}

func persistLedgerTest(t *testing.T, ctx *sdk.AppCtx, pid, s3, provider string, keys []string) (int64, bool, error) {
	t.Helper()
	return persistSESInbound(ctx, pid, s3, provider, keys, []string{"contact@alpha.example"}, "contact@alpha.example", nil,
		`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_slug,provider_message_id,s3_key) VALUES(?,'email','in','customer@example.net','received','aws-ses',NULLIF(?,''),NULLIF(?,''))`, pid, provider, s3)
}

func TestSESDeliveryLedgerGlobalClaimsAndRollback(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	id, duplicate, err := persistLedgerTest(t, ctx, "test-proj", "bucket/raw-1", "ses-1", []string{"s3:bucket/raw-1", "message:ses-1", "sns:topic:sns-1"})
	if err != nil || duplicate {
		t.Fatalf("insert: %d %v %v", id, duplicate, err)
	}
	for _, key := range []string{"s3:bucket/raw-1", "message:ses-1", "sns:topic:sns-1"} {
		got, dup, err := persistLedgerTest(t, ctx, "test-proj", "", "", []string{key})
		if err != nil || !dup || got != id {
			t.Fatalf("dedupe %s: %d %v %v", key, got, dup, err)
		}
		if _, _, err := persistLedgerTest(t, ctx, "other-project", "", "", []string{key}); !errors.Is(err, errInboundDeliveryConflict) {
			t.Fatalf("cross-project %s: %v", key, err)
		}
	}
	if _, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_ledger_job BEFORE INSERT ON inbound_jobs BEGIN SELECT RAISE(ABORT,'job failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := persistLedgerTest(t, ctx, "test-proj", "bucket/raw-2", "ses-2", []string{"message:ses-2"}); err == nil {
		t.Fatal("expected transaction rollback")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("rollback messages=%d %v", count, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM inbound_delivery_keys WHERE delivery_key='message:ses-2'`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("rollback claims=%d %v", count, err)
	}
}

func TestSESDeliveryLedgerRejectsLegacyCrossProjectCopyAndBackfillsEnvelope(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	res, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_message_id) VALUES('test-proj','email','in','customer@example.net','received','legacy-1')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	got, dup, err := persistLedgerTest(t, ctx, "test-proj", "", "legacy-1", []string{"message:legacy-1"})
	if err != nil || !dup || got != id {
		t.Fatalf("legacy duplicate: %d %v %v", got, dup, err)
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.ReceivingIdentity != "contact@alpha.example" || len(m.EnvelopeRecipients) != 1 {
		t.Fatalf("missing envelope backfill: %+v %v", m, err)
	}
	if _, _, err := persistLedgerTest(t, ctx, "other-project", "", "legacy-1", []string{"message:legacy-1"}); !errors.Is(err, errInboundDeliveryConflict) {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,status,provider_message_id) VALUES('other-project','email','in','customer@example.net','received','legacy-2')`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := persistLedgerTest(t, ctx, "test-proj", "", "legacy-2", []string{"message:legacy-2"}); !errors.Is(err, errInboundDeliveryConflict) {
		t.Fatalf("pre-ledger project copy accepted: %v", err)
	}
}

func TestConcurrentSESDeliveryCreatesOneDurableMessage(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	var wg sync.WaitGroup
	errs := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := persistLedgerTest(t, ctx, "test-proj", "bucket/concurrent", "concurrent", []string{"message:concurrent"})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, table := range []string{"messages", "inbound_jobs", "inbound_delivery_keys"} {
		var count int
		if err := ctx.AppDB().QueryRow(`SELECT count(*) FROM ` + table).Scan(&count); err != nil || count != 1 {
			t.Fatalf("%s=%d %v", table, count, err)
		}
	}
}

// Ensure SNS JSON test fixtures use the same provider response shape as AWS.
func subscriptionReply(subscriptions []snsSubscription) *sdk.ExecuteResult {
	members := []map[string]string{}
	for _, sub := range subscriptions {
		members = append(members, map[string]string{"Endpoint": sub.Endpoint, "SubscriptionArn": sub.SubscriptionARN})
	}
	return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(mustJSON(map[string]any{"ListSubscriptionsByTopicResponse": map[string]any{"ListSubscriptionsByTopicResult": map[string]any{"Subscriptions": map[string]any{"member": members}}}}))}
}

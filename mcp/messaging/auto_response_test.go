package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type responseReplay struct {
	Message Message `json:"message"`
	Events  []struct {
		Raw json.RawMessage `json:"raw"`
		ID  string          `json:"provider_event_id"`
	} `json:"events"`
	AutoReply struct {
		ID        int64             `json:"id"`
		Project   string            `json:"project_id"`
		From      string            `json:"from"`
		MessageID string            `json:"message_id_header"`
		InReplyTo string            `json:"in_reply_to"`
		Headers   map[string]any    `json:"headers"`
		Verdicts  map[string]string `json:"verdicts"`
		Envelope  []string          `json:"envelope_recipients"`
		Identity  string            `json:"receiving_identity"`
	} `json:"auto_reply"`
}

func insertReplayReply(t *testing.T, ctx *sdk.AppCtx, c responseReplay) {
	t.Helper()
	r := c.AutoReply
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,provider_slug,message_id_header,in_reply_to,headers,verdicts,envelope_recipients,receiving_identity) VALUES(?,'test-proj','email','in',?,'[]','received','aws-ses',?,?,?,?,?,?)`, r.ID, r.From, r.MessageID, r.InReplyTo, string(mustJSON(r.Headers)), string(mustJSON(r.Verdicts)), string(mustJSON(r.Envelope)), r.Identity); err != nil {
		t.Fatal(err)
	}
}

func replayCase(t *testing.T, c responseReplay, replyFirst bool, seed ...func(*sdk.AppCtx)) {
	t.Helper()
	rec := tk.NewEmitRecorder()
	ctx := newTestCtx(t, &stubPlatform{}, tk.WithEmitter(rec))
	for _, s := range seed {
		s(ctx)
	}
	m := c.Message
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,provider_message_id,message_id_header) VALUES(?,'test-proj','email','out',?,?,'sent',?,?)`, m.ID, m.From, string(mustJSON(m.To)), m.ProviderMessageID, m.MessageIDHeader); err != nil {
		t.Fatal(err)
	}
	if replyFirst {
		insertReplayReply(t, ctx, c)
	}
	msg, err := dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range c.Events {
		raw := e.Raw
		var wrapper struct {
			Raw json.RawMessage `json:"raw"`
		}
		_ = json.Unmarshal(raw, &wrapper)
		if len(wrapper.Raw) > 0 {
			raw = wrapper.Raw
		}
		events, err := parseSESProviderEvents(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		for _, ev := range events {
			ev.ProviderEventID = e.ID
			if _, err = persistAndEmitProviderEvent(ctx, msg, ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	if !replyFirst {
		before, _ := dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
		if before.Status != "bounced" {
			t.Fatalf("uncorrelated bounce must remain bounced: %+v", before)
		}
		insertReplayReply(t, ctx, c)
	}
	for range 2 {
		if err := reconcilePendingAutoResponses(ctx); err != nil {
			t.Fatal(err)
		}
	}
	msg, err = dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	bare := canonicalAddrForChannel(channelEmail, m.To[0])
	if msg.Status != "opened" || len(msg.RecipientStatuses) != 1 || msg.RecipientStatuses[bare] != "opened" || msg.EventCounts["bounced"] != 0 || msg.EventCounts["auto_replied"] != 1 {
		t.Fatalf("incorrect derived state: %+v", msg)
	}
	stored, err := dbDeliveryEvents(ctx.AppDB(), m.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range stored {
		if e.Kind == "auto_replied" {
			found = true
			if e.ProviderKind != "bounced" {
				t.Fatalf("lost original provider kind: %+v", e)
			}
			var raw map[string]any
			_ = json.Unmarshal(e.Raw, &raw)
			if wrapped, ok := raw["raw"].(map[string]any); ok {
				raw = wrapped
			}
			if raw["bounce"] == nil {
				t.Fatal("lost raw bounce evidence")
			}
		}
	}
	if !found {
		t.Fatal("no typed automatic response")
	}
	correctionCount := 0
	for _, e := range rec.EventsByTopic("message.event") {
		p := e.Data.(map[string]any)
		if p["reclassified_event_id"] != nil {
			correctionCount++
			if p["kind"] != "auto_replied" || p["recipient"] != bare {
				t.Fatalf("wrong correction: %+v", p)
			}
		}
	}
	if correctionCount != 1 {
		t.Fatalf("corrections=%d", correctionCount)
	}
	supp, err := dbSuppressionList(ctx.AppDB(), "test-proj", "email", 100)
	if err != nil || len(supp) != 0 {
		t.Fatalf("unexpected suppressions: %+v %v", supp, err)
	}
}

// Synthetic fixtures retain the three production event sequences and IDs while
// omitting customer names, content, addresses, account IDs and credentials.
func TestAutomaticResponseHistoryReplay(t *testing.T) {
	data, err := os.ReadFile("testdata/auto_response_histories.json")
	if err != nil {
		t.Fatal(err)
	}
	replayHistories(t, data)
}

// Optional local-only forensic replay; customer material is never committed.
func TestPrivateProductionAutoResponseReplay(t *testing.T) {
	path := os.Getenv("MESSAGING_INVESTIGATION_CASES")
	if path == "" {
		t.Skip("set MESSAGING_INVESTIGATION_CASES to a private capture")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	replayHistories(t, data)
}
func replayHistories(t *testing.T, data []byte) {
	var cases []responseReplay
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		for _, first := range []bool{true, false} {
			t.Run(fmt.Sprintf("%d/reply_first=%t", c.Message.ID, first), func(t *testing.T) { replayCase(t, c, first) })
		}
	}
}

func TestAutoResponseRequiresExactEvidence(t *testing.T) {
	data, err := os.ReadFile("testdata/auto_response_histories.json")
	if err != nil {
		t.Fatal(err)
	}
	changes := map[string]func(*responseReplay){
		"human reply":            func(c *responseReplay) { c.AutoReply.Headers["Auto-Submitted"] = "no" },
		"missing authentication": func(c *responseReplay) { c.AutoReply.Verdicts = map[string]string{} },
		"failed DKIM":            func(c *responseReplay) { c.AutoReply.Verdicts["dkim"] = "FAIL" },
		"wrong feedback":         func(c *responseReplay) { c.AutoReply.MessageID = "<unrelated@eu-west-1.amazonses.com>" },
		"wrong thread":           func(c *responseReplay) { c.AutoReply.InReplyTo = "<unrelated@eu-west-1.amazonses.com>" },
		"wrong identity":         func(c *responseReplay) { c.AutoReply.Identity = "other@example.test" },
		"untrusted sender":       func(c *responseReplay) { c.AutoReply.From = "mailer-daemon@evil.example" },
		"DSN report": func(c *responseReplay) {
			c.AutoReply.Headers["Content-Type"] = "multipart/report; report-type=delivery-status"
		},
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			var cases []responseReplay
			_ = json.Unmarshal(data, &cases)
			c := cases[0]
			change(&c)
			ctx := newTestCtx(t, nil)
			m := c.Message
			_, err := ctx.AppDB().Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,message_id_header,provider_message_id) VALUES(?,'test-proj','email','out',?,?,'sent',?,?)`, m.ID, m.From, string(mustJSON(m.To)), m.MessageIDHeader, m.ProviderMessageID)
			if err != nil {
				t.Fatal(err)
			}
			insertReplayReply(t, ctx, c)
			msg, _ := dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
			for _, e := range c.Events {
				var w struct {
					Raw json.RawMessage `json:"raw"`
				}
				_ = json.Unmarshal(e.Raw, &w)
				raw := e.Raw
				if len(w.Raw) > 0 {
					raw = w.Raw
				}
				events, _ := parseSESProviderEvents(string(raw))
				for _, ev := range events {
					ev.ProviderEventID = e.ID
					if _, err := persistAndEmitProviderEvent(ctx, msg, ev); err != nil {
						t.Fatal(err)
					}
				}
			}
			msg, _ = dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
			if msg.Status != "bounced" || msg.EventCounts["auto_replied"] != 0 || len(msg.RecipientStatuses) != 1 {
				t.Fatalf("unsafe reinterpretation: %+v", msg)
			}
		})
	}
}

func TestConfirmedAutoResponseDoesNotHideOtherRecipientFailure(t *testing.T) {
	data, err := os.ReadFile("testdata/auto_response_histories.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []responseReplay
	_ = json.Unmarshal(data, &cases)
	c := cases[0]
	ctx := newTestCtx(t, nil)
	m := c.Message
	_, err = ctx.AppDB().Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,message_id_header,provider_message_id) VALUES(?,'test-proj','email','out',?,?,'sent',?,?)`, m.ID, m.From, string(mustJSON(m.To)), m.MessageIDHeader, m.ProviderMessageID)
	if err != nil {
		t.Fatal(err)
	}
	insertReplayReply(t, ctx, c)
	msg, _ := dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
	for _, e := range c.Events {
		events, _ := parseSESProviderEvents(string(e.Raw))
		for _, ev := range events {
			ev.ProviderEventID = e.ID
			if _, err := persistAndEmitProviderEvent(ctx, msg, ev); err != nil {
				t.Fatal(err)
			}
		}
	}
	_, err = persistAndEmitProviderEvent(ctx, msg, providerEvent{Provider: "aws-ses", ProviderEventID: "genuine-failure", Kind: "bounced", Recipient: "Other Person <other@example.test>", Permanent: true, BounceType: "Permanent", Raw: json.RawMessage(`{"bounce":{"bounceType":"Permanent"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	msg, err = dbMessageGet(ctx.AppDB(), "test-proj", m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if msg.Status != "bounced" || msg.RecipientStatuses[m.To[0]] != "opened" || msg.RecipientStatuses["other@example.test"] != "bounced" {
		t.Fatalf("other failure lost: %+v", msg)
	}
}

type correctionFailureEmitter struct {
	*tk.EmitRecorder
	fail bool
}

func (e *correctionFailureEmitter) EmitWithProjectAck(ctx context.Context, topic, pid string, payload any) error {
	if e.fail {
		return fmt.Errorf("temporary gateway failure")
	}
	return e.EmitRecorder.EmitWithProjectAck(ctx, topic, pid, payload)
}
func TestAutoResponseCorrectionOutboxRetriesFailedGatewayAcknowledgment(t *testing.T) {
	emitter := &correctionFailureEmitter{EmitRecorder: tk.NewEmitRecorder(), fail: true}
	ctx := newTestCtx(t, nil)
	ctx.SetEmitter(emitter)
	_, err := ctx.AppDB().Exec(`INSERT INTO delivery_correction_outbox(event_id,project_id,payload) VALUES('correction','test-proj','{"event_id":"correction","kind":"auto_replied"}')`)
	if err != nil {
		t.Fatal(err)
	}
	if err = flushDeliveryCorrections(ctx); err == nil {
		t.Fatal("expected gateway failure")
	}
	var pending int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM delivery_correction_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil || pending != 1 {
		t.Fatalf("pending=%d err=%v", pending, err)
	}
	emitter.fail = false
	for range 2 {
		if err = flushDeliveryCorrections(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(emitter.EventsByTopic("message.event")) != 1 {
		t.Fatalf("events=%+v", emitter.Events())
	}
}

// Production regression (0.13.61): recovery rescanned ~11k historical bounces
// with an unindexable LIKE every 30s and starved normal requests. Recovery
// must converge in bounded passes and then stop reconsidering old bounces.
func seedHistoricalBounces(t *testing.T, ctx *sdk.AppCtx, n int) {
	t.Helper()
	// Ids stay below the replay fixtures so later rows arrive in id order,
	// as SQLite rowids do in production.
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := range n {
		id := int64(10000 + i)
		if _, err = tx.Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,provider_message_id,message_id_header) VALUES(?,'test-proj','email','out','sales@example.com','["lead@example.net"]','bounced',?,?)`, id, fmt.Sprintf("hist-%d", i), fmt.Sprintf("<hist-%d@example.com>", i)); err != nil {
			t.Fatal(err)
		}
		raw := fmt.Sprintf(`{"raw":{"notificationType":"Bounce","bounce":{"bounceType":"Transient","bounceSubType":"General","feedbackId":"hist-feedback-%d","bouncedRecipients":[{"emailAddress":"lead@example.net"}]}}}`, i)
		if _, err = tx.Exec(`INSERT INTO delivery_events(message_id,kind,recipient,raw) VALUES(?,'bounced','lead@example.net',?)`, id, raw); err != nil {
			t.Fatal(err)
		}
		if i%2 == 0 {
			if _, err = tx.Exec(`INSERT INTO messages(id,project_id,channel,direction,from_addr,to_addrs,status,provider_slug,message_id_header) VALUES(?,'test-proj','email','in','someone@example.org','[]','received','aws-ses',?)`, 30000+i/2, fmt.Sprintf("<unrelated-%d@example.org>", i)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
}

func TestAutoResponseRecoveryIsBoundedOverHistoricalBounces(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	seedHistoricalBounces(t, ctx, 11069)
	cursorsDone := func() bool {
		var pending int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM auto_response_scan_state s WHERE last_id < CASE s.name WHEN 'bounces' THEN (SELECT MAX(id) FROM delivery_events) ELSE (SELECT MAX(id) FROM messages) END`).Scan(&pending); err != nil {
			t.Fatal(err)
		}
		return pending == 0
	}
	passes := 0
	for !cursorsDone() {
		start := time.Now()
		if err := reconcilePendingAutoResponses(ctx); err != nil {
			t.Fatal(err)
		}
		if d := time.Since(start); d > autoResponsePassBudget+autoResponseQueryTimeout {
			t.Fatalf("pass %d took %s", passes, d)
		}
		if passes++; passes > 50 {
			t.Fatal("recovery did not converge")
		}
	}
	var candidates, ready int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*),COALESCE(SUM(ready),0) FROM auto_response_candidates`).Scan(&candidates, &ready); err != nil {
		t.Fatal(err)
	}
	if candidates != 11069 || ready != 0 {
		t.Fatalf("candidates=%d ready=%d", candidates, ready)
	}
	// Steady state: nothing new means no historical bounce is reconsidered.
	start := time.Now()
	for range 3 {
		if err := reconcilePendingAutoResponses(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Fatalf("idle recovery passes took %s", d)
	}
}

func TestAutomaticResponseHistoryReplayAfterBackfill(t *testing.T) {
	data, err := os.ReadFile("testdata/auto_response_histories.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []responseReplay
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	backfill := func(ctx *sdk.AppCtx) {
		seedHistoricalBounces(t, ctx, 3000)
		for range 10 {
			if err := reconcilePendingAutoResponses(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	for _, first := range []bool{true, false} {
		t.Run(fmt.Sprintf("reply_first=%t", first), func(t *testing.T) { replayCase(t, cases[0], first, backfill) })
	}
}

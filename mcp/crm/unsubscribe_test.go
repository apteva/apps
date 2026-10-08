package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type unsubscribePlatform struct {
	crmRecordingPlatform
	blocked, inbound, old, failAdd, failReadback, noEffect bool
	added                                                  bool
	checks                                                 int
}

func (p *unsubscribePlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	if tool != "suppression_check" && tool != "suppression_add" {
		return p.crmRecordingPlatform.CallAppResult(app, tool, args, out)
	}
	p.calls = append(p.calls, crmCallAppCall{AppName: app, Tool: tool, Input: args})
	var payload map[string]any
	if tool == "suppression_add" {
		if p.failAdd {
			return errors.New("simulated Messaging failure")
		}
		p.added = true
		if !p.noEffect {
			p.blocked = true
		}
		direction := "outbound"
		if p.inbound {
			direction = "both"
		}
		payload = map[string]any{"suppression": map[string]any{"direction": direction}}
	} else {
		p.checks++
		if p.added && p.failReadback {
			return errors.New("readback unavailable")
		}
		direction := strArg(args, "direction")
		blocked := p.blocked
		if direction == "inbound" {
			blocked = p.inbound
		}
		payload = map[string]any{"suppressed": blocked, "kind": "address", "matched": strArg(args, "address"), "reason": "unsubscribe", "source": "crm", "suppressed_at": "2026-10-08T08:00:00Z", "direction": "outbound"}
		if p.inbound {
			payload["reason"] = "crm-spam"
			payload["direction"] = "both"
		}
		if !p.old {
			payload["check_direction"] = direction
		}
	}
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func unsubscribeFixture(t *testing.T, p *unsubscribePlatform) (*sdk.AppCtx, int64, int64) {
	t.Helper()
	ctx := newTestCtx(t, tk.WithPlatform(p))
	in, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "edbis@free.fr", MatchedRecipient: "support@example.test", To: []string{"misleading@example.net"}, BodyText: "UNSUBSCRIBE!!!!", MessageID: 7890, MessageIDHeader: "<unsub-request@free.fr>", ReceivedAt: "2026-10-08T07:00:00Z"})
	if err != nil {
		t.Fatal(err)
	}
	p.calls = nil
	return ctx, in["contact_id"].(int64), in["conversation_id"].(int64)
}

func TestEmailUnsubscribePinsSenderAuditsOnceAndKeepsInbound(t *testing.T) {
	p := &unsubscribePlatform{}
	ctx, cid, convoID := unsubscribeFixture(t, p)
	app := &App{}
	before, err := dbGetByID(ctx.AppDB(), "test-proj", cid)
	if err != nil {
		t.Fatal(err)
	}
	preview, err := app.toolUnsubscribeEmail(ctx, map[string]any{"conversation_id": convoID, "id": cid})
	if err != nil {
		t.Fatal(err)
	}
	if got := preview.(*emailUnsubscribeState); got.Address != "edbis@free.fr" || got.Confirmed || got.OutboundBlocked {
		t.Fatalf("wrong preview: %+v", got)
	}
	if len(crmCallsTo(&p.crmRecordingPlatform, "suppression_add")) != 0 {
		t.Fatal("preview wrote a suppression")
	}
	args := map[string]any{"conversation_id": convoID, "id": cid, "dry_run": false, "expected_address": "edbis@free.fr", "source": "human"}
	for i := 0; i < 2; i++ {
		out, err := app.toolUnsubscribeEmail(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		state := out.(*emailUnsubscribeState)
		if !state.Confirmed || !state.Unsubscribed || !state.OutboundBlocked || state.InboundBlocked {
			t.Fatalf("unconfirmed unsubscribe: %+v", state)
		}
	}
	adds := crmCallsTo(&p.crmRecordingPlatform, "suppression_add")
	for _, call := range adds {
		if call.Input["_project_id"] != "test-proj" || call.Input["address"] != "edbis@free.fr" || call.Input["kind"] != "address" || call.Input["direction"] != "outbound" || call.Input["channel"] != "email" {
			t.Fatalf("wrong scope: %v", call)
		}
	}
	c := readAttributeContact(t, ctx, cid)
	if c.Status != "active" || containsString(c.Tags, "spam") || len(c.Attributes) != 0 {
		t.Fatalf("contact was marked spam/DNC: %+v", c)
	}
	if len(c.Channels) != 1 || !c.Channels[0].Deliverability[0].Suppressed || c.Channels[0].Deliverability[0].Messageable {
		t.Fatalf("local email eligibility not updated: %+v", c.Channels)
	}
	if c.FirstContactAt != before.FirstContactAt || c.LastContactAt != before.LastContactAt {
		t.Fatal("operator unsubscribe audit changed contact interaction timestamps")
	}
	convo, err := dbConversationGet(ctx.AppDB(), "test-proj", convoID)
	if err != nil || convo.Status != "open" {
		t.Fatalf("conversation was closed/spammed: %+v %v", convo, err)
	}
	var count int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE project_id='test-proj' AND kind='system' AND idempotency_key LIKE 'email-unsubscribe:%'`).Scan(&count); err != nil || count != 1 {
		t.Fatalf("audit count=%d err=%v", count, err)
	}
	inbox, total, err := dbInboxConversations(ctx.AppDB(), "test-proj", "open", 50, 0, nil)
	if err != nil || total != 1 || len(inbox) != 1 || inbox[0].Snippet != "UNSUBSCRIBE!!!!" || inbox[0].LastMessageAddresses == nil || inbox[0].LastMessageAddresses.From != "edbis@free.fr" || len(inbox[0].LastMessageAddresses.To) != 1 || inbox[0].LastMessageAddresses.To[0] != "support@example.test" {
		t.Fatalf("audit replaced latest-message preview: %+v %v", inbox, err)
	}
	received, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "email", From: "edbis@free.fr", MatchedRecipient: "support@example.test", BodyText: "A later incoming reply", MessageID: 7891, InReplyTo: "<unsub-request@free.fr>", MessageIDHeader: "<next@free.fr>", ReceivedAt: "2026-10-08T09:00:00Z"})
	if err != nil || received["contact_id"] != cid || received["conversation_id"] != convoID {
		t.Fatalf("incoming reply lost: %v %v", received, err)
	}
}

func TestEmailUnsubscribeFailuresDoNotRecordFalseSuccess(t *testing.T) {
	for _, scenario := range []string{"old-messaging", "write-failed", "readback-failed", "no-effect", "wrong-confirmation"} {
		t.Run(scenario, func(t *testing.T) {
			p := &unsubscribePlatform{}
			ctx, cid, convo := unsubscribeFixture(t, p)
			switch scenario {
			case "old-messaging":
				p.old = true
			case "write-failed":
				p.failAdd = true
			case "readback-failed":
				p.failReadback = true
			case "no-effect":
				p.noEffect = true
			}
			address := "edbis@free.fr"
			if scenario == "wrong-confirmation" {
				address = "support@example.test"
			}
			_, err := (&App{}).toolUnsubscribeEmail(ctx, map[string]any{"conversation_id": convo, "id": cid, "dry_run": false, "expected_address": address})
			if err == nil {
				t.Fatal("failed operation reported success")
			}
			if scenario == "old-messaging" && !strings.Contains(err.Error(), "v0.13.59") {
				t.Fatalf("unclear upgrade error: %v", err)
			}
			if (scenario == "old-messaging" || scenario == "wrong-confirmation") && p.added {
				t.Fatal("unsafe suppression write")
			}
			var count int
			ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM contact_activities WHERE idempotency_key LIKE 'email-unsubscribe:%'`).Scan(&count)
			if count != 0 {
				t.Fatal("failed operation wrote a success audit")
			}
			states, err := loadChannelDeliverability(ctx.AppDB(), "test-proj", cid)
			if err != nil {
				t.Fatal(err)
			}
			for _, s := range states {
				if s.Suppressed {
					t.Fatal("failed confirmation changed local eligibility")
				}
			}
		})
	}
}

func TestEmailUnsubscribePreservesStrongBlocksAndRejectsUnsafeTargets(t *testing.T) {
	p := &unsubscribePlatform{blocked: true, inbound: true}
	ctx, cid, convo := unsubscribeFixture(t, p)
	out, err := (&App{}).toolUnsubscribeEmail(ctx, map[string]any{"conversation_id": convo, "dry_run": false, "expected_address": "edbis@free.fr"})
	if err != nil || !out.(*emailUnsubscribeState).InboundBlocked || out.(*emailUnsubscribeState).Direction != "both" {
		t.Fatalf("existing block weakened: %v %v", out, err)
	}
	t.Setenv("APTEVA_PROJECT_ID", "") // global installs resolve trusted per-call scope
	for _, args := range []map[string]any{
		{"conversation_id": convo, "id": cid + 1, "_project_id": "test-proj"},
		{"conversation_id": convo, "_project_id": "foreign"},
	} {
		before := len(p.calls)
		if _, err = (&App{}).toolUnsubscribeEmail(ctx, args); err == nil {
			t.Fatal("foreign/wrong contact accepted")
		}
		if len(p.calls) != before {
			t.Fatal("unsafe target called Messaging")
		}
	}
	if _, err = ctx.AppDB().Exec(`UPDATE contact_activities SET source_detail='{}' WHERE kind='email_received'`); err != nil {
		t.Fatal(err)
	}
	if _, err = (&App{}).toolUnsubscribeEmail(ctx, map[string]any{"conversation_id": convo, "_project_id": "test-proj"}); err == nil {
		t.Fatal("missing sender guessed from contact")
	}
}

func TestEmailUnsubscribeHTTPRoundtripAndMethodSafety(t *testing.T) {
	p := &unsubscribePlatform{}
	ctx, cid, convo := unsubscribeFixture(t, p)
	if err := (&App{}).OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	path := "/contacts/" + anyString(cid) + "/conversations/" + anyString(convo) + "/unsubscribe"
	app := &App{}
	for _, method := range []string{"GET", "POST", "DELETE"} {
		body := bytes.NewBufferString(`{"expected_address":"edbis@free.fr"}`)
		w := httptest.NewRecorder()
		app.handleHTTPContactItem(w, httptest.NewRequest(method, path, body))
		if method == "DELETE" {
			if w.Code != 405 {
				t.Fatalf("method safety=%d", w.Code)
			}
			continue
		}
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", method, w.Code, w.Body)
		}
		var state emailUnsubscribeState
		if err := json.Unmarshal(w.Body.Bytes(), &state); err != nil {
			t.Fatal(err)
		}
		if state.Confirmed != (method == "POST") {
			t.Fatalf("wrong confirmation: %+v", state)
		}
	}
}

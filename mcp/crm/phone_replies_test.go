package main

import (
	"encoding/json"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type phoneReplyPlatform struct {
	crmRecordingPlatform
	wrongSender, suppressed, failSend bool
}

func (p *phoneReplyPlatform) CallAppResult(app, tool string, args map[string]any, out any) error {
	var payload map[string]any
	switch tool {
	case "senders_list":
		channel := strArg(args, "channel")
		if p.wrongSender {
			channel = "whatsapp"
		}
		payload = map[string]any{"senders": []map[string]any{{"channel": channel, "address": "+15550002222"}}}
	case "suppression_check":
		payload = map[string]any{"suppressed": p.suppressed, "reason": "unsubscribed"}
	case "send_message":
		if p.failSend {
			payload = map[string]any{"id": 9801, "status": "failed", "status_reason": "provider rejected"}
		}
	}
	if payload == nil {
		return p.crmRecordingPlatform.CallAppResult(app, tool, args, out)
	}
	p.calls = append(p.calls, crmCallAppCall{AppName: app, Tool: tool, Input: args})
	raw, _ := json.Marshal(payload)
	return json.Unmarshal(raw, out)
}

func TestWhatsAppSMSReplyStaysInThreadAndReturnsToExactRoute(t *testing.T) {
	pf := &phoneReplyPlatform{}
	ctx := newTestCtx(t, tk.WithPlatform(pf))
	app := &App{}
	// The actual WhatsApp remote number is NOT the primary phone.
	c := mustCreate(t, ctx, map[string]any{"channels": []any{
		map[string]any{"kind": "phone", "value": "+15551110000", "is_primary": true},
		map[string]any{"kind": "phone", "value": "+15551234567"},
	}})
	oldSMS := mkConversation(t, ctx, "test-proj", c.ID, "sms")
	oldActivity, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: oldSMS, Kind: "sms_received", Body: "Keep original SMS history", OccurredAt: time.Now().UTC().Add(-48 * time.Hour).Format(time.RFC3339)})
	if err != nil {
		t.Fatal(err)
	}
	in, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "whatsapp", From: "+15551234567", MatchedRecipient: "+15550001111", BodyText: "WhatsApp question", MessageID: 9100})
	if err != nil {
		t.Fatal(err)
	}
	convo := in["conversation_id"].(int64)
	args := map[string]any{"id": c.ID, "conversation_id": convo, "channel": "sms", "from": "+1 (555) 000-2222", "body": "Reply by SMS", "reply_to_activity_id": in["activity_id"]}
	if _, err := app.toolReply(ctx, args); err != nil {
		t.Fatal(err)
	}
	sends := crmCallsTo(&pf.crmRecordingPlatform, "send_message")
	if len(sends) != 1 || sends[0].Input["channel"] != "sms" || sends[0].Input["to"] != "+15551234567" || sends[0].Input["from"] != "+15550002222" || sends[0].Input["conversation_id"] != convo {
		t.Fatalf("wrong send: %v", sends)
	}
	if _, err := app.toolReply(ctx, args); err != nil {
		t.Fatal(err)
	}
	if len(crmCallsTo(&pf.crmRecordingPlatform, "send_message")) != 1 {
		t.Fatal("repeat reply was not deduplicated")
	}
	returned, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "sms", From: "+15551234567", MatchedRecipient: "sms:+1 (555) 000-2222", BodyText: "SMS reply", MessageID: 9101, ReceivedAt: time.Now().UTC().Add(time.Second).Format(time.RFC3339Nano)})
	if err != nil || returned["conversation_id"] != convo {
		t.Fatalf("SMS did not return to WhatsApp thread: %v %v", returned, err)
	}
	if _, err := app.toolReply(ctx, map[string]any{"id": c.ID, "conversation_id": convo, "body": "Continue by SMS"}); err != nil {
		t.Fatal(err)
	}
	sends = crmCallsTo(&pf.crmRecordingPlatform, "send_message")
	if sends[len(sends)-1].Input["channel"] != "sms" {
		t.Fatalf("mixed-thread default reply reverted to WhatsApp: %v", sends)
	}
	// The exact sender pair is required; neither a different receiving number
	// nor an old delivery should be pulled into the newly switched thread.
	for i, tc := range []struct{ local, at string }{
		{"+15550003333", time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)},
		{"+15550002222", time.Now().UTC().Add(-time.Hour).Format(time.RFC3339Nano)},
	} {
		out, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "sms", From: "+15551234567", MatchedRecipient: tc.local, BodyText: "Other SMS", MessageID: int64(9200 + i), ReceivedAt: tc.at})
		if err != nil || out["conversation_id"] != oldSMS {
			t.Fatalf("unrelated/historical SMS rethreaded: %v %v", out, err)
		}
	}
	rows, total, err := dbInboxConversations(ctx.AppDB(), "test-proj", "all", 50, 0, []inboxFilter{{Field: "channel", Op: "is", Value: "sms"}})
	if err != nil || total != 2 {
		t.Fatalf("mixed inbox filter: %v %v", rows, err)
	}
	found := false
	for _, row := range rows {
		if row.ID == convo {
			found = len(row.Channels) == 2 && row.Channel == "whatsapp"
		}
	}
	if !found {
		t.Fatalf("mixed channel labels missing: %v", rows)
	}
	_, total, err = dbInboxConversations(ctx.AppDB(), "test-proj", "all", 50, 0, []inboxFilter{{Field: "channel", Op: "is_not", Value: "sms"}})
	if err != nil || total != 0 {
		t.Fatalf("negative mixed filter total=%d err=%v", total, err)
	}
	// Neither original conversation nor its channel has been changed.
	original, err := dbConversationGet(ctx.AppDB(), "test-proj", oldSMS)
	if err != nil || original == nil || original.Channel != "sms" {
		t.Fatalf("lost original SMS conversation: %v %v", original, err)
	}
	var originalBody string
	var originalConversation int64
	if err := ctx.AppDB().QueryRow(`SELECT body,conversation_id FROM contact_activities WHERE id=?`, oldActivity.ID).Scan(&originalBody, &originalConversation); err != nil || originalBody != "Keep original SMS history" || originalConversation != oldSMS {
		t.Fatalf("historical message changed: %q %d %v", originalBody, originalConversation, err)
	}
}

func TestPhoneReturnRouteIsScopedToProjectSourceAndNumberPair(t *testing.T) {
	ctx := newTestCtx(t)
	c := mustCreate(t, ctx, map[string]any{"display_name": "Scoped"})
	convo := mkConversation(t, ctx, "test-proj", c.ID, "whatsapp")
	at := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if _, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: convo, Kind: "sms_sent", MessagingInstallID: 42, Body: "Switched", OccurredAt: at, SourceDetail: map[string]any{"reply_channel_switch": true, "from": "+15550002222", "to": "+15551234567"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		pid           string
		source        int64
		remote, local string
		want          int64
	}{
		{"test-proj", 42, "+15551234567", "+15550002222", convo},
		{"other-proj", 42, "+15551234567", "+15550002222", 0},
		{"test-proj", 43, "+15551234567", "+15550002222", 0},
		{"test-proj", 42, "+15551110000", "+15550002222", 0},
		{"test-proj", 42, "+15551234567", "", 0},
	} {
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			t.Fatal(err)
		}
		id, err := switchedPhoneConversationTx(tx, tc.pid, c.ID, tc.source, "sms", tc.remote, tc.local, time.Now().UTC().Format(time.RFC3339))
		tx.Rollback()
		if err != nil || id != tc.want {
			t.Fatalf("route %+v: id=%d err=%v", tc, id, err)
		}
	}
}

func TestPhoneChannelSwitchRejectsWrongSenderSuppressionAndFailure(t *testing.T) {
	for _, name := range []string{"wrong_sender", "suppressed", "failed_send"} {
		t.Run(name, func(t *testing.T) {
			pf := &phoneReplyPlatform{wrongSender: name == "wrong_sender", suppressed: name == "suppressed", failSend: name == "failed_send"}
			ctx := newTestCtx(t, tk.WithPlatform(pf))
			in, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "whatsapp", From: "+15551234567", MatchedRecipient: "+15550001111", BodyText: "Question", MessageID: 9300})
			if err != nil {
				t.Fatal(err)
			}
			_, err = (&App{}).toolReply(ctx, map[string]any{"id": in["contact_id"], "conversation_id": in["conversation_id"], "channel": "sms", "from": "+15550002222", "body": "SMS"})
			if err == nil {
				t.Fatal("unsafe/failed send succeeded")
			}
			if name != "failed_send" && len(crmCallsTo(&pf.crmRecordingPlatform, "send_message")) != 0 {
				t.Fatal("invalid send reached Messaging")
			}
			out, err := ingestInbound(ctx, "test-proj", inboundPayload{Channel: "sms", From: "+15551234567", MatchedRecipient: "+15550002222", BodyText: "Unrelated", MessageID: 9301})
			if err != nil || out["conversation_id"] == in["conversation_id"] {
				t.Fatalf("failed send established return route: %v %v", out, err)
			}
		})
	}
}

func TestReplyTransportCannotMixEmailOrForeignContacts(t *testing.T) {
	ctx := newTestCtx(t, tk.WithPlatform(&phoneReplyPlatform{}))
	c := mustCreate(t, ctx, map[string]any{"primary_phone": "+15551234567"})
	convo := mkConversation(t, ctx, "test-proj", c.ID, "email")
	for _, channel := range []string{"sms", "whatsapp", "unknown"} {
		if _, err := (&App{}).toolReply(ctx, map[string]any{"id": c.ID, "conversation_id": convo, "channel": channel, "from": "+15550002222", "body": "No"}); err == nil {
			t.Fatalf("accepted email switch to %s", channel)
		}
	}
	other := mustCreate(t, ctx, map[string]any{"display_name": "Other"})
	if _, err := (&App{}).toolReply(ctx, map[string]any{"id": other.ID, "conversation_id": convo, "channel": "sms", "body": "No"}); err == nil {
		t.Fatal("accepted foreign contact")
	}
}

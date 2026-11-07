package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type anchoredReplyPlatform struct {
	*idempotentMessagingPlatform
	messages map[int64]*crmMessagingReplyMessage
	getError bool
}

func (p *anchoredReplyPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if tool == "senders_list" {
		raw, _ := json.Marshal(map[string]any{"senders": []map[string]any{{"channel": "email", "address": "sender@example.test"}}})
		return json.Unmarshal(raw, out)
	}
	if tool == "message_get" {
		p.mu.Lock()
		p.calls = append(p.calls, crmCallAppCall{AppName: app, Tool: tool, Input: input})
		p.mu.Unlock()
		if p.getError {
			return errors.New("Messaging unavailable")
		}
		raw, _ := json.Marshal(map[string]any{"message": p.messages[int64Arg(input, "id")]})
		return json.Unmarshal(raw, out)
	}
	if tool == "send_message" && int64Arg(input, "reply_to_message_id") > 0 {
		// Mirror Messaging 0.15's strict reply-mode transport contract. A
		// leaked CRM conversation ID, sender, subject or header must fail.
		for key := range input {
			switch key {
			case "reply_to_message_id", "body", "body_html", "attachments", "attachment_storage_ids", "template_id", "vars", "content_sid", "idempotency_key", "_project_id":
			default:
				return fmt.Errorf("unexpected reply-mode argument %s", key)
			}
		}
	}
	return p.idempotentMessagingPlatform.CallAppResult(app, tool, input, out)
}

func anchoredReplyFixture(t *testing.T) (*sdk.AppCtx, *anchoredReplyPlatform, *Contact, int64, []*Activity) {
	t.Helper()
	p := &anchoredReplyPlatform{idempotentMessagingPlatform: newIdempotentMessagingPlatform(), messages: map[int64]*crmMessagingReplyMessage{}}
	ctx := newTestCtx(t, tk.WithPlatform(p), tk.WithConfig(map[string]string{"email_verification_mode": "off"}))
	c := mustCreate(t, ctx, map[string]any{"channels": []any{map[string]any{"kind": "email", "value": "alice@example.test", "is_primary": true}}})
	convo := mkConversation(t, ctx, "test-proj", c.ID, channelEmail)
	if _, err := ctx.AppDB().Exec(`UPDATE contact_conversations SET subject='Original subject',root_message_id='<root@example.test>' WHERE id=?`, convo); err != nil {
		t.Fatal(err)
	}
	var activities []*Activity
	for i := 1; i <= 2; i++ {
		id := int64(900 + i)
		header := fmt.Sprintf("<inbound-%d@example.test>", i)
		p.messages[id] = &crmMessagingReplyMessage{ID: id, Channel: channelEmail, Direction: "in", Subject: "Current subject", MessageIDHeader: header, References: []string{"<root@example.test>"}}
		act, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: convo, Kind: "email_received", Body: "Incoming", OccurredAt: "2026-11-07T10:00:00Z", Source: "messaging", SourceDetail: map[string]any{"from": "alice@example.test", "reply_to": "alice@example.test", "receiving_identity": "sender@example.test", "to": []string{"sender@example.test"}}, MessageIDHeader: header, MessagingID: id, MessagingInstallID: 42})
		if err != nil {
			t.Fatal(err)
		}
		activities = append(activities, act)
	}
	// A newer outbound message must not become the automatic reply anchor.
	if _, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: convo, Kind: "email_sent", Body: "Outgoing", OccurredAt: time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	return ctx, p, c, convo, activities
}

func TestEmailReplyAnchorsSelectedOrLatestInbound(t *testing.T) {
	app := &App{}
	for _, test := range []struct {
		name                   string
		call                   sdk.ToolHandler
		selected, conversation bool
		want                   int
	}{
		{"reply latest", app.toolReply, false, true, 1},
		{"reply selected", app.toolReply, true, true, 0},
		{"latest conversation", app.toolReply, false, false, 1},
		{"selected without conversation", app.toolReply, true, false, 0},
		{"send latest", app.toolSendMessage, false, true, 1},
		{"send selected", app.toolSendMessage, true, true, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, p, c, convo, activities := anchoredReplyFixture(t)
			args := map[string]any{"id": c.ID, "body": "Thanks", "body_html": "<p>Thanks</p>", "attachment_storage_ids": []any{float64(12)}}
			if test.conversation {
				args["conversation_id"] = convo
			}
			if test.selected {
				args["reply_to_activity_id"] = activities[0].ID
			}
			out, err := test.call(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			sends := emailSubjectSends(p.idempotentMessagingPlatform)
			if len(sends) != 1 {
				t.Fatalf("sends=%v", sends)
			}
			sent := sends[0].Input
			if int64Arg(sent, "reply_to_message_id") != activities[test.want].MessagingID {
				t.Fatalf("wrong reply anchor: %v", sent)
			}
			if sent["body"] != "Thanks" || sent["body_html"] != "<p>Thanks</p>" || !reflect.DeepEqual(sent["attachment_storage_ids"], args["attachment_storage_ids"]) {
				t.Fatalf("content lost: %v", sent)
			}
			act := out.(map[string]any)["activity"].(*Activity)
			if act.ConversationID != convo || !strings.HasPrefix(act.Body, "Re: Current subject\n\n") {
				t.Fatalf("wrong saved activity: %+v", act)
			}
			var detail map[string]any
			_ = json.Unmarshal([]byte(act.SourceDetail), &detail)
			if int64Arg(detail, "reply_to_activity_id") != activities[test.want].ID {
				t.Fatalf("anchor missing from audit: %v", detail)
			}
		})
	}
}

func TestEmailReplyDeduplicationIncludesInboundAnchor(t *testing.T) {
	ctx, p, c, convo, activities := anchoredReplyFixture(t)
	app := &App{}
	for _, index := range []int{0, 0, 1, 1} {
		out, err := app.toolReply(ctx, map[string]any{"id": c.ID, "conversation_id": convo, "reply_to_activity_id": activities[index].ID, "body": "Same answer"})
		if err != nil {
			t.Fatal(err)
		}
		if int64Arg(out.(map[string]any), "conversation_id") != convo {
			t.Fatal("conversation lost")
		}
	}
	created, _ := p.snapshot()
	if created != 2 || len(emailSubjectSends(p.idempotentMessagingPlatform)) != 2 {
		t.Fatalf("different anchors must send once each, created=%d", created)
	}
	args := map[string]any{"id": c.ID, "conversation_id": convo, "reply_to_activity_id": activities[0].ID, "body": "Explicit retry", "idempotency_key": "once"}
	if _, err := app.toolReply(ctx, args); err != nil {
		t.Fatal(err)
	}
	args["reply_to_activity_id"] = activities[1].ID
	if _, err := app.toolReply(ctx, args); err == nil || !strings.Contains(err.Error(), "different reply message") {
		t.Fatalf("reused key accepted: %v", err)
	}
}

func TestEmailReplyAnchorValidation(t *testing.T) {
	for _, mode := range []string{"foreign contact", "foreign project", "outbound", "different install", "unknown provenance", "missing message", "lookup failure", "wrong RFC ID"} {
		t.Run(mode, func(t *testing.T) {
			ctx, p, c, convo, activities := anchoredReplyFixture(t)
			id := activities[0].ID
			switch mode {
			case "foreign contact":
				other := mustCreate(t, ctx, map[string]any{"display_name": "Other contact"})
				_, err := ctx.AppDB().Exec(`UPDATE contact_activities SET contact_id=? WHERE id=?`, other.ID, id)
				if err != nil {
					t.Fatal(err)
				}
			case "foreign project":
				_, err := ctx.AppDB().Exec(`UPDATE contact_activities SET project_id='other-project' WHERE id=?`, id)
				if err != nil {
					t.Fatal(err)
				}
			case "outbound":
				_, err := ctx.AppDB().Exec(`UPDATE contact_activities SET kind='email_sent' WHERE id=?`, id)
				if err != nil {
					t.Fatal(err)
				}
			case "different install":
				_, err := ctx.AppDB().Exec(`UPDATE contact_activities SET messaging_install_id=43 WHERE id=?`, id)
				if err != nil {
					t.Fatal(err)
				}
			case "unknown provenance":
				_, err := ctx.AppDB().Exec(`UPDATE contact_activities SET messaging_install_id=0 WHERE id=?`, id)
				if err != nil {
					t.Fatal(err)
				}
			case "missing message":
				delete(p.messages, activities[0].MessagingID)
			case "lookup failure":
				p.getError = true
			case "wrong RFC ID":
				p.messages[activities[0].MessagingID].MessageIDHeader = "<other@example.test>"
			}
			_, err := (&App{}).toolReply(ctx, map[string]any{"id": c.ID, "conversation_id": convo, "reply_to_activity_id": id, "body": "Answer"})
			if mode == "unknown provenance" {
				if err != nil {
					t.Fatal(err)
				}
				sent := emailSubjectSends(p.idempotentMessagingPlatform)[0].Input
				if _, ok := sent["reply_to_message_id"]; ok {
					t.Fatal("unknown provenance used a remote anchor")
				}
				if sent["in_reply_to"] != activities[0].MessageIDHeader {
					t.Fatalf("wrong legacy parent: %v", sent)
				}
				return
			}
			if err == nil {
				t.Fatal("invalid reply accepted")
			}
			if len(emailSubjectSends(p.idempotentMessagingPlatform)) != 0 {
				t.Fatal("invalid anchor dispatched")
			}
		})
	}
}

func TestEmailReplySelectedActivityDeterminesConversation(t *testing.T) {
	ctx, p, c, convo, activities := anchoredReplyFixture(t)
	other := mkConversation(t, ctx, "test-proj", c.ID, channelEmail)
	_, err := logMessageActivity(ctx.AppDB(), logMessageActivityInput{ProjectID: "test-proj", ContactID: c.ID, ConversationID: other, Kind: "email_received", Body: "Other thread", OccurredAt: time.Now().UTC().Format(time.RFC3339Nano)})
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolReply(ctx, map[string]any{"id": c.ID, "reply_to_activity_id": activities[0].ID, "body": "Selected thread"})
	if err != nil {
		t.Fatal(err)
	}
	if int64Arg(out.(map[string]any), "conversation_id") != convo || int64Arg(emailSubjectSends(p.idempotentMessagingPlatform)[0].Input, "reply_to_message_id") != activities[0].MessagingID {
		t.Fatalf("selected activity lost its conversation: %v", out)
	}
}

func TestEmailReplyLatestUsesOccurrenceTimeAndAdvancesAfterInbound(t *testing.T) {
	ctx, p, c, convo, activities := anchoredReplyFixture(t)
	if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET occurred_at='2026-11-07T11:00:00Z' WHERE id=?`, activities[0].ID); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"id": c.ID, "conversation_id": convo, "body": "Same answer"}
	app := &App{}
	if _, err := app.toolReply(ctx, args); err != nil {
		t.Fatal(err)
	}
	if int64Arg(emailSubjectSends(p.idempotentMessagingPlatform)[0].Input, "reply_to_message_id") != activities[0].MessagingID {
		t.Fatal("latest selected by insertion ID instead of occurrence time")
	}
	if _, err := ctx.AppDB().Exec(`UPDATE contact_activities SET occurred_at='2026-11-07T12:00:00Z' WHERE id=?`, activities[1].ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolReply(ctx, args); err != nil {
		t.Fatal(err)
	}
	sends := emailSubjectSends(p.idempotentMessagingPlatform)
	if len(sends) != 2 || int64Arg(sends[1].Input, "reply_to_message_id") != activities[1].MessagingID {
		t.Fatalf("new inbound deduplicated against old reply: %v", sends)
	}
}

func TestEmailReplyHTTPUsesMessagingAnchor(t *testing.T) {
	for _, route := range []string{"messages", "reply"} {
		t.Run(route, func(t *testing.T) {
			ctx, p, c, convo, activities := anchoredReplyFixture(t)
			old := globalCtx
			globalCtx = ctx
			t.Cleanup(func() { globalCtx = old })
			body, _ := json.Marshal(map[string]any{"conversation_id": convo, "reply_to_activity_id": activities[0].ID, "body": "HTTP answer"})
			r := httptest.NewRequest(http.MethodPost, "/contacts/"+anyString(c.ID)+"/"+route+"?project_id=test-proj", bytes.NewReader(body))
			w := httptest.NewRecorder()
			(&App{}).handleHTTPContactItem(w, r)
			if w.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
			}
			sends := emailSubjectSends(p.idempotentMessagingPlatform)
			if len(sends) != 1 || int64Arg(sends[0].Input, "reply_to_message_id") != activities[0].MessagingID {
				t.Fatalf("wrong HTTP reply: %v", sends)
			}
		})
	}
}

func TestEmailReplyOverridesUseSelectedRFCThread(t *testing.T) {
	for _, override := range []string{"subject", "from"} {
		t.Run(override, func(t *testing.T) {
			ctx, p, c, convo, activities := anchoredReplyFixture(t)
			args := map[string]any{"id": c.ID, "conversation_id": convo, "reply_to_activity_id": activities[0].ID, "body": "Answer"}
			want := "Custom subject"
			if override == "from" {
				want = "alternate@example.test"
			}
			args[override] = want
			if _, err := (&App{}).toolReply(ctx, args); err != nil {
				t.Fatal(err)
			}
			sent := emailSubjectSends(p.idempotentMessagingPlatform)[0].Input
			if sent[override] != want || sent["in_reply_to"] != activities[0].MessageIDHeader {
				t.Fatalf("override/thread lost: %v", sent)
			}
			if !reflect.DeepEqual(sent["references"], []string{"<root@example.test>", activities[0].MessageIDHeader}) {
				t.Fatalf("references lost: %v", sent)
			}
			if _, ok := sent["headers"]; ok {
				t.Fatal("transport headers passed as custom headers")
			}
		})
	}
}

func TestEmailReplySavedDraftKeepsSelectedAnchor(t *testing.T) {
	ctx, p, _, convo, activities := anchoredReplyFixture(t)
	app := &App{}
	out, err := app.toolDraftCreate(ctx, map[string]any{"conversation_id": convo, "reply_to_activity_id": activities[0].ID, "body": "Draft answer", "subject": "Re: Current subject"})
	if err != nil {
		t.Fatal(err)
	}
	draft := out.(map[string]any)["draft"].(*conversationDraft)
	result, err := app.toolDraftSend(ctx, map[string]any{"id": draft.ID, "expected_revision": draft.Revision})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["sent"] != true {
		t.Fatalf("draft send failed: %v", result)
	}
	sends := emailSubjectSends(p.idempotentMessagingPlatform)
	if len(sends) != 1 || int64Arg(sends[0].Input, "reply_to_message_id") != activities[0].MessagingID {
		t.Fatalf("draft anchor changed: %v", sends)
	}
}

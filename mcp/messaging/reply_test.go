package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/mail"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func seedReplyAnchor(t *testing.T, ctx *sdk.AppCtx, m Message) int64 {
	t.Helper()
	if m.ProjectID == "" {
		m.ProjectID = "test-proj"
	}
	if m.Channel == "" {
		m.Channel = channelEmail
	}
	if m.Direction == "" {
		m.Direction = "in"
	}
	if m.From == "" {
		m.From = "customer@example.net"
	}
	if m.To == nil {
		m.To = []string{fromAcme}
	}
	if m.Headers == nil {
		m.Headers = json.RawMessage(`{}`)
	}
	var rfcID, providerID any
	if m.MessageIDHeader != "" {
		rfcID = m.MessageIDHeader
	}
	if m.ProviderMessageID != "" {
		providerID = m.ProviderMessageID
	}
	result, err := ctx.AppDB().Exec(`INSERT INTO messages
		(project_id,channel,direction,from_addr,to_addrs,cc_addrs,bcc_addrs,subject,headers,
		message_id_header,in_reply_to,references_json,provider_message_id,provider_slug,provider_connection_id,
		envelope_recipients,receiving_identity,matched_recipient,attachment_storage_ids,status,received_at)
		VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'received',?)`,
		m.ProjectID, m.Channel, m.Direction, m.From, string(mustJSON(m.To)), string(mustJSON(m.CC)), string(mustJSON(m.BCC)), m.Subject, string(m.Headers),
		rfcID, m.InReplyTo, string(mustJSON(m.References)), providerID, m.ProviderSlug, m.ProviderConnectionID,
		string(mustJSON(m.EnvelopeRecipients)), m.ReceivingIdentity, m.MatchedRecipient, string(mustJSON(m.AttachmentStorageIDs)), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func seedReplySender(t *testing.T, ctx *sdk.AppCtx, channel, address string) {
	t.Helper()
	provider, kind := "aws-ses", "email_mailbox"
	if channel != channelEmail {
		provider, kind = "twilio", "phone"
	}
	preseedSender(t, ctx, senderUpsert{Channel: channel, Address: address, Kind: kind, Provider: provider, Verified: true, SendingEnabled: true})
}

func sentReply(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *Message {
	t.Helper()
	out, err := (&App{}).toolSendMessage(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["status"] != "sent" {
		t.Fatalf("reply result: %+v", r)
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", r["id"].(int64))
	if err != nil || m == nil {
		t.Fatalf("stored reply: %+v %v", m, err)
	}
	return m
}

func replyWireHeaders(t *testing.T, platform *stubPlatform) mail.Header {
	t.Helper()
	for i := len(platform.executeCalls) - 1; i >= 0; i-- {
		call := platform.executeCalls[i]
		if call.Tool != "send_raw_email" {
			continue
		}
		var raw []byte
		var err error
		if value, ok := call.Input["raw"].(string); ok {
			raw, err = base64.RawURLEncoding.DecodeString(value)
		} else {
			content := call.Input["Content"].(map[string]any)["Raw"].(map[string]any)
			raw, err = base64.StdEncoding.DecodeString(content["Data"].(string))
		}
		if err != nil {
			t.Fatal(err)
		}
		m, err := mail.ReadMessage(bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		return m.Header
	}
	t.Fatal("no raw email provider call")
	return nil
}

func TestSendMessageReplyInboundPreservesThreadAndTicketIdentifiers(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	id := seedReplyAnchor(t, ctx, Message{
		From: `"Customer, Jane" <customer@example.net>`,
		To:   []string{fromAcme, "colleague@example.net"}, CC: []string{"cc@example.net"}, BCC: []string{"secret@example.net"},
		Subject: "[T-1234] Help", MessageIDHeader: "<original@example.net>", ProviderMessageID: "opaque-provider-id",
		References: []string{"<root@example.net>", "<root@example.net>"}, AttachmentStorageIDs: []int64{999},
		Headers: json.RawMessage(`{"rEpLy-To":"\"Support, Jane\" <case+T-1234@example.net>","x-ticket-id":"T-1234","X-Ticket-Number":"1234","X-Secret":{"unrelated":"metadata"},"Authentication-Results":"untrusted","Bcc":"secret@example.net"}`),
	})
	m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Looking into it."})
	if m.From != fromAcme || !reflect.DeepEqual(m.To, []string{"case+t-1234@example.net"}) || len(m.CC) != 0 || len(m.BCC) != 0 || len(m.Attachments) != 0 || len(m.AttachmentStorageIDs) != 0 {
		t.Fatalf("unexpected reply addressing/attachments: %+v", m)
	}
	if m.Subject != "Re: [T-1234] Help" || m.InReplyTo != "<original@example.net>" || !reflect.DeepEqual(m.References, []string{"<root@example.net>", "<original@example.net>"}) {
		t.Fatalf("thread lost: %+v", m)
	}
	h := replyWireHeaders(t, platform)
	for key, want := range map[string]string{"To": "case+t-1234@example.net", "Subject": "Re: [T-1234] Help", "In-Reply-To": "<original@example.net>", "References": "<root@example.net> <original@example.net>", "X-Ticket-ID": "T-1234", "X-Ticket-Number": "1234"} {
		if h.Get(key) != want {
			t.Errorf("%s=%q, want %q", key, h.Get(key), want)
		}
	}
	for _, key := range []string{"Bcc", "Cc", "X-Secret", "Authentication-Results"} {
		if h.Get(key) != "" {
			t.Errorf("copied forbidden header %s", key)
		}
	}
	original, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || original.Subject != "[T-1234] Help" || len(original.BCC) != 1 || original.AttachmentStorageIDs[0] != 999 {
		t.Fatalf("original changed: %+v %v", original, err)
	}
}

func TestSendMessageReplyReplyAllDeduplicatesAndExcludesOwnMailboxes(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	seedReplySender(t, ctx, channelEmail, "team@acme.com")
	id := seedReplyAnchor(t, ctx, Message{
		To: []string{fromAcme, "team+case@acme.com", "other@example.net", "customer@example.net"},
		CC: []string{"other@example.net", "cc@example.net", "team@acme.com"}, BCC: []string{"hidden@example.net"},
		MessageIDHeader: "<original@example.net>",
	})
	m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Reply all", "reply_all": true})
	if !reflect.DeepEqual(m.To, []string{"customer@example.net"}) || !reflect.DeepEqual(m.CC, []string{"other@example.net", "cc@example.net"}) || len(m.BCC) != 0 {
		t.Fatalf("reply-all recipients: To=%v Cc=%v Bcc=%v", m.To, m.CC, m.BCC)
	}
	if replyWireHeaders(t, platform).Get("Cc") != "other@example.net, cc@example.net" {
		t.Fatal("Cc missing on the wire")
	}
}

func TestSendMessageReplyOutboundKeepsSenderAndTo(t *testing.T) {
	for _, all := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "reply-all"}[all], func(t *testing.T) {
			ctx := newTestCtx(t, &stubPlatform{})
			seedReplySender(t, ctx, channelEmail, fromAcme)
			id := seedReplyAnchor(t, ctx, Message{
				Direction: "out", From: `"Acme Team" <` + fromAcme + `>`, To: []string{"first@example.net", "second@example.net"},
				CC: []string{"cc@example.net"}, BCC: []string{"hidden@example.net"}, Subject: "rE: [T-5] Help",
				MessageIDHeader: "<sent@acme.com>", Headers: json.RawMessage(`{"Reply-To":"support+T-5@acme.com"}`),
			})
			m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Following up", "reply_all": all})
			if m.From != fromAcme || !reflect.DeepEqual(m.To, []string{"first@example.net", "second@example.net"}) || len(m.BCC) != 0 || m.Subject != "rE: [T-5] Help" || (len(m.CC) == 1) != all {
				t.Fatalf("outbound follow-up: %+v", m)
			}
		})
	}
}

func TestSendMessageReplyTaggedDeliveryAndFollowupKeepReplyTo(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	id := seedReplyAnchor(t, ctx, Message{
		To: []string{"unrelated@elsewhere.net"}, MatchedRecipient: "notifications+T-99@acme.com",
		MessageIDHeader: "<original@example.net>",
	})
	first := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "First"})
	second := sentReply(t, ctx, map[string]any{"reply_to_message_id": first.ID, "body": "Follow-up"})
	if first.From != fromAcme || second.From != fromAcme || second.InReplyTo != first.MessageIDHeader {
		t.Fatalf("tagged reply or follow-up sender/thread changed: %+v %+v", first, second)
	}
	h := replyWireHeaders(t, platform)
	if h.Get("Reply-To") != "notifications+t-99@acme.com" || len(h["Reply-To"]) != 1 {
		t.Fatalf("tagged Reply-To lost or duplicated: %v", h)
	}
}

func TestSendMessageReplyGmailPreservesAnchorThreadAndConnection(t *testing.T) {
	for _, messageID := range []string{"<gmail-original@example.net>", ""} {
		t.Run("RFC-ID="+messageID, func(t *testing.T) {
			platform := gmailTestPlatform()
			ctx := newTestCtx(t, platform)
			preseedSender(t, ctx, senderUpsert{Channel: channelEmail, Address: "support@example.com", Kind: "email_mailbox", Provider: "gmail", ProviderConnectionID: 3, Verified: true, SendingEnabled: true})
			id := seedReplyAnchor(t, ctx, Message{To: []string{"support@example.com"}, MessageIDHeader: messageID, ProviderMessageID: "opaque-gmail-id", ProviderSlug: "gmail", ProviderConnectionID: 3})
			if _, err := ctx.AppDB().Exec(`UPDATE messages SET provider_thread_id='original-thread' WHERE id=?`, id); err != nil {
				t.Fatal(err)
			}
			m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Hello"})
			if m.InReplyTo != messageID {
				t.Fatalf("RFC header confused with provider ID: %q", m.InReplyTo)
			}
			call := platform.executeCalls[len(platform.executeCalls)-1]
			if call.ConnID != 3 || call.Input["threadId"] != "original-thread" {
				t.Fatalf("Gmail thread/account changed: %+v", call)
			}
			if replyWireHeaders(t, platform).Get("In-Reply-To") != messageID {
				t.Fatal("Gmail wire threading differs")
			}
		})
	}
}

func TestSendMessageReplyDoesNotInventRFCIDAndSupportsAttachmentsAndTemplates(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	seedReplySender(t, ctx, channelEmail, fromAcme)
	id := seedReplyAnchor(t, ctx, Message{ProviderMessageID: "opaque-id", InReplyTo: "<parent@example.net>"})
	m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "attachments": []any{map[string]any{"filename": "answer.txt", "content_type": "text/plain", "content_base64": "aGk="}}})
	if m.InReplyTo != "" || !reflect.DeepEqual(m.References, []string{"<parent@example.net>"}) || len(m.Attachments) != 1 || m.Attachments[0].Filename != "answer.txt" {
		t.Fatalf("attachment-only reply: %+v", m)
	}
	tpl, err := (&App{}).toolTemplateCreate(ctx, map[string]any{"name": "reply", "body_text": "Hi {{name}}", "subject": "A different subject"})
	if err != nil {
		t.Fatal(err)
	}
	m = sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "template_id": tpl.(map[string]any)["template"].(*Template).ID, "vars": map[string]any{"name": "Jane"}})
	if m.BodyText != "Hi Jane" || m.Subject != "Re: (no subject)" || m.TemplateSnapshot == nil {
		t.Fatalf("template reply lost content or original subject: %+v", m)
	}
}

func TestSendMessageReplyPhoneChannelsUseOriginalAddressPair(t *testing.T) {
	for _, channel := range []string{channelSMS, channelWhatsApp} {
		t.Run(channel, func(t *testing.T) {
			platform := &stubPlatform{bindingsOverride: map[string]any{"phone_provider": float64(2)}, replyByTool: map[string]*sdk.ExecuteResult{
				"send_sms": gmailTestReply(map[string]any{"sid": "SMreply"}), "send_whatsapp": gmailTestReply(map[string]any{"sid": "SMreply"}),
			}}
			ctx := newTestCtx(t, platform)
			seedReplySender(t, ctx, channel, "+15551112222")
			id := seedReplyAnchor(t, ctx, Message{Channel: channel, From: "+15553334444", To: []string{"+15551112222"}, Headers: json.RawMessage(`{"Reply-To":"attacker@example.net","X-Ticket-ID":"T-5"}`)})
			m := sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Hello"})
			if m.Channel != channel || m.From != "+15551112222" || !reflect.DeepEqual(m.To, []string{"+15553334444"}) || m.InReplyTo != "" || m.Subject != "" {
				t.Fatalf("phone reply: %+v", m)
			}
			call := platform.executeCalls[len(platform.executeCalls)-1]
			prefix := ""
			if channel == channelWhatsApp {
				prefix = "whatsapp:"
			}
			if call.Input["From"] != prefix+"+15551112222" || call.Input["To"] != prefix+"+15553334444" {
				t.Fatalf("phone wire addresses: %+v", call)
			}
		})
	}
}

func TestSendMessageReplyValidationDoesNotSend(t *testing.T) {
	for _, scenario := range []string{"missing-id", "fractional-id", "unknown-id", "other-project", "no-sender", "disabled-sender", "unverified-sender", "deleted-sender", "invalid-reply-to", "conflicting-reply-to", "invalid-message-id", "invalid-references", "empty-content", "phone-reply-all", "reply-all-type", "changed-connection"} {
		t.Run(scenario, func(t *testing.T) {
			platform := &stubPlatform{}
			ctx := newTestCtx(t, platform)
			seedReplySender(t, ctx, channelEmail, fromAcme)
			anchor := Message{MessageIDHeader: "<original@example.net>"}
			switch scenario {
			case "other-project":
				anchor.ProjectID = "other-project"
			case "invalid-reply-to":
				anchor.Headers = json.RawMessage(`{"Reply-To":"bad\r\nBcc: evil@example.net"}`)
			case "conflicting-reply-to":
				anchor.Headers = json.RawMessage(`{"Reply-To":"a@example.net","reply-to":"b@example.net"}`)
			case "invalid-message-id":
				anchor.MessageIDHeader = "opaque-provider-id"
			case "invalid-references":
				anchor.References = []string{"<good@example.net>\r\nX-Evil: yes"}
			case "phone-reply-all":
				anchor.Channel = channelSMS
			case "changed-connection":
				anchor.Direction, anchor.From = "out", fromAcme
				anchor.To, anchor.ProviderConnectionID = []string{"customer@example.net"}, 3
				preseedSender(t, ctx, senderUpsert{Channel: channelEmail, Address: fromAcme, Kind: "email_mailbox", Provider: "aws-ses", ProviderConnectionID: 1, Verified: true, SendingEnabled: true})
			}
			id := seedReplyAnchor(t, ctx, anchor)
			args := map[string]any{"reply_to_message_id": id, "body": "Hello"}
			switch scenario {
			case "missing-id":
				delete(args, "reply_to_message_id")
			case "fractional-id":
				args["reply_to_message_id"] = float64(id) + 0.5
			case "unknown-id":
				args["reply_to_message_id"] = id + 1000
			case "no-sender", "disabled-sender", "unverified-sender", "deleted-sender":
				query := map[string]string{"no-sender": `DELETE FROM senders`, "disabled-sender": `UPDATE senders SET sending_enabled=0`, "unverified-sender": `UPDATE senders SET verified=0`, "deleted-sender": `UPDATE senders SET deleted_at=CURRENT_TIMESTAMP`}[scenario]
				if _, err := ctx.AppDB().Exec(query); err != nil {
					t.Fatal(err)
				}
			case "empty-content":
				delete(args, "body")
			case "phone-reply-all":
				args["reply_all"] = true
			case "reply-all-type":
				args["reply_all"] = "true"
			}
			if _, err := (&App{}).toolSendMessage(ctx, args); err == nil {
				t.Fatal("expected validation error")
			}
			if len(platform.executeCalls) != 0 {
				t.Fatalf("validation failure contacted provider: %+v", platform.executeCalls)
			}
		})
	}
}

func TestSendMessageReplyRejectsAddressingAndThreadingOverrides(t *testing.T) {
	ctx := newTestCtx(t, &stubPlatform{})
	id := seedReplyAnchor(t, ctx, Message{})
	for _, field := range []string{"from", "from_name", "to", "cc", "bcc", "reply_to", "channel", "connection_id", "subject", "headers", "in_reply_to", "references", "message_id_header"} {
		t.Run(field, func(t *testing.T) {
			_, err := (&App{}).toolSendMessage(ctx, map[string]any{"reply_to_message_id": id, "body": "Hello", field: "override"})
			if err == nil || !strings.Contains(err.Error(), "unsupported argument") {
				t.Fatalf("override accepted: %v", err)
			}
		})
	}
}

func TestSendMessageReplySuppressionAndWhatsAppWindowStillApply(t *testing.T) {
	t.Run("suppression", func(t *testing.T) {
		platform := &stubPlatform{}
		ctx := newTestCtx(t, platform)
		seedReplySender(t, ctx, channelEmail, fromAcme)
		id := seedReplyAnchor(t, ctx, Message{})
		if _, err := (&App{}).toolSuppressionAdd(ctx, map[string]any{"address": "customer@example.net", "channel": channelEmail}); err != nil {
			t.Fatal(err)
		}
		_, err := (&App{}).toolSendMessage(ctx, map[string]any{"reply_to_message_id": id, "body": "Hello"})
		var blocked *recipientSuppressedError
		if !errors.As(err, &blocked) || len(platform.executeCalls) != 0 {
			t.Fatalf("suppression bypassed: %v calls=%+v", err, platform.executeCalls)
		}
	})
	t.Run("expired-whatsapp", func(t *testing.T) {
		platform := &stubPlatform{bindingsOverride: map[string]any{"phone_provider": float64(2)}}
		ctx := newTestCtx(t, platform)
		seedReplySender(t, ctx, channelWhatsApp, "+15551112222")
		id := seedReplyAnchor(t, ctx, Message{Channel: channelWhatsApp, From: "+15553334444", To: []string{"+15551112222"}})
		if _, err := ctx.AppDB().Exec(`UPDATE messages SET created_at=?,received_at=? WHERE id=?`, time.Now().Add(-48*time.Hour).Format(time.RFC3339), time.Now().Add(-48*time.Hour).Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
		_, err := (&App{}).toolSendMessage(ctx, map[string]any{"reply_to_message_id": id, "body": "Hello"})
		if err == nil || !strings.Contains(err.Error(), "24 hours") || len(platform.executeCalls) != 0 {
			t.Fatalf("WhatsApp window bypassed: %v calls=%+v", err, platform.executeCalls)
		}
	})
}

func TestSendMessageReplyRetriesAreScopedToAnchorAndDoNotResend(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	app := &App{}
	// A plain send using the same caller key is independent of reply retries.
	if _, err := app.toolSendMessage(ctx, map[string]any{"channel": channelEmail, "from": fromAcme, "to": "customer@example.net", "body": "New message", "idempotency_key": "request-1"}); err != nil {
		t.Fatal(err)
	}
	first := seedReplyAnchor(t, ctx, Message{})
	second := seedReplyAnchor(t, ctx, Message{})
	a := sentReply(t, ctx, map[string]any{"reply_to_message_id": first, "body": "First reply", "idempotency_key": "request-1"})
	b := sentReply(t, ctx, map[string]any{"reply_to_message_id": second, "body": "Other reply", "idempotency_key": "request-1"})
	if a.ID == b.ID || a.BodyText != "First reply" || b.BodyText != "Other reply" {
		t.Fatal("idempotency keys collided across anchors")
	}
	// Sender changes after a completed send must not prevent its safe replay.
	if _, err := ctx.AppDB().Exec(`UPDATE senders SET sending_enabled=0`); err != nil {
		t.Fatal(err)
	}
	replayed := sentReply(t, ctx, map[string]any{"reply_to_message_id": first, "body": "Changed retry body", "idempotency_key": "request-1"})
	if replayed.ID != a.ID || replayed.BodyText != a.BodyText || len(platform.executeCalls) != 3 {
		t.Fatalf("retry resent: %+v calls=%d", replayed, len(platform.executeCalls))
	}
}

func TestSendMessageReplyUnsafeTicketHeadersAreNotCopied(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	id := seedReplyAnchor(t, ctx, Message{MessageIDHeader: "<original@example.net>", Headers: json.RawMessage(`{"X-Ticket-ID":"T-1\r\nBcc: evil@example.net","X-Ticket-Number":"ticket with spaces","X-Case-ID":"unsupported"}`)})
	sentReply(t, ctx, map[string]any{"reply_to_message_id": id, "body": "Hello"})
	h := replyWireHeaders(t, platform)
	for _, name := range []string{"X-Ticket-ID", "X-Ticket-Number", "X-Case-ID", "Bcc"} {
		if h.Get(name) != "" {
			t.Fatalf("unsafe/unsupported header copied: %s=%s", name, h.Get(name))
		}
	}
}

func TestSendMessageReplyFailedAndUncertainSendsAreNotRetried(t *testing.T) {
	for _, uncertain := range []bool{false, true} {
		t.Run(map[bool]string{false: "rejected", true: "unknown-outcome"}[uncertain], func(t *testing.T) {
			platform := gmailTestPlatform()
			ctx := newTestCtx(t, platform)
			preseedSender(t, ctx, senderUpsert{Channel: channelEmail, Address: "support@example.com", Kind: "email_mailbox", Provider: "gmail", ProviderConnectionID: 3, Verified: true, SendingEnabled: true})
			id := seedReplyAnchor(t, ctx, Message{To: []string{"support@example.com"}, ProviderSlug: "gmail", ProviderConnectionID: 3})
			wantStatus := "failed"
			if uncertain {
				platform.executeErr = errors.New("connection lost after request")
				wantStatus = "pending"
			} else {
				platform.replyByTool["send_raw_email"] = &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":"rejected"}`)}
			}
			args := map[string]any{"reply_to_message_id": id, "body": "Hello", "idempotency_key": "once"}
			var firstID any
			for i := 0; i < 2; i++ {
				out, err := (&App{}).toolSendMessage(ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				r := out.(map[string]any)
				if r["status"] != wantStatus {
					t.Fatalf("status=%v, want %s", r["status"], wantStatus)
				}
				if i == 0 {
					firstID = r["id"]
				} else if r["id"] != firstID {
					t.Fatalf("retry created another message: %v != %v", r["id"], firstID)
				}
			}
			if len(platform.executeCalls) != 1 {
				t.Fatalf("provider called %d times", len(platform.executeCalls))
			}
		})
	}
}

func TestSendMessageReplyHTTPDispatcherAndGlobalProjectIsolation(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	seedReplySender(t, ctx, channelEmail, fromAcme)
	id := seedReplyAnchor(t, ctx, Message{})
	t.Setenv("APTEVA_PROJECT_ID", "")
	app := &App{}
	call := func(project string) *httptest.ResponseRecorder {
		body := mustJSON(map[string]any{"tool": "send_message", "args": map[string]any{"reply_to_message_id": id, "body": "Hello"}})
		r := httptest.NewRequest(http.MethodPost, "/tools/call?project_id="+project, bytes.NewReader(body))
		w := httptest.NewRecorder()
		app.handleToolsCall(w, r)
		return w
	}
	if w := call("other-project"); w.Code == http.StatusOK || len(platform.executeCalls) != 0 {
		t.Fatalf("cross-project reply accepted: %d %s", w.Code, w.Body.String())
	}
	if w := call("test-proj"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"status":"sent"`) {
		t.Fatalf("tool dispatch failed: %d %s", w.Code, w.Body.String())
	}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/tools/call?project_id=test-proj", strings.NewReader(`{"tool":"reply_message","args":{"message_id":1,"body":"Hello"}}`))
	app.handleToolsCall(w, r)
	if w.Code != http.StatusNotFound || len(platform.executeCalls) != 1 {
		t.Fatalf("removed reply_message tool is still callable: %d %s", w.Code, w.Body.String())
	}
}

func TestSendMessageReplyModeNeverFallsBackToNewSend(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	for _, anchor := range []any{nil, 0, -1, 1.5, "invalid"} {
		_, err := (&App{}).toolSendMessage(ctx, map[string]any{
			"reply_to_message_id": anchor, "channel": channelEmail,
			"from": fromAcme, "to": "customer@example.net", "body": "Hello",
		})
		if err == nil {
			t.Fatalf("invalid anchor %v fell back to a new send", anchor)
		}
	}
	if len(platform.executeCalls) != 0 {
		t.Fatal("invalid reply called a provider")
	}
}

func TestSendMessageNewSendKeepsReplyToAddressAndRequiresChannelAndTo(t *testing.T) {
	platform := &stubPlatform{}
	ctx := newTestCtx(t, platform)
	for _, args := range []map[string]any{
		{"to": "customer@example.net", "body": "Hello"},
		{"channel": channelEmail, "body": "Hello"},
		{"channel": channelEmail, "to": "customer@example.net", "body": "Hello", "reply_all": false},
	} {
		if _, err := (&App{}).toolSendMessage(ctx, args); err == nil {
			t.Fatalf("invalid new send accepted: %v", args)
		}
	}
	if len(platform.executeCalls) != 0 {
		t.Fatal("invalid new send called a provider")
	}
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{
		"channel": channelEmail, "from": fromAcme, "to": "customer@example.net",
		"subject": "New message", "body": "Hello", "reply_to": "support@acme.com",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", r["id"].(int64))
	if err != nil || r["status"] != "sent" || m.Subject != "New message" || m.InReplyTo != "" {
		t.Fatalf("new send changed behavior: %+v %v", out, err)
	}
	var headers map[string]any
	if err := json.Unmarshal(m.Headers, &headers); err != nil || headers["Reply-To"] != "support@acme.com" {
		t.Fatalf("reply_to address confused with a message anchor: %s %v", m.Headers, err)
	}
	if !reflect.DeepEqual(platform.executeCalls[0].Input["ReplyToAddresses"], []string{"support@acme.com"}) {
		t.Fatalf("new send Reply-To address not sent: %+v", platform.executeCalls[0])
	}
}

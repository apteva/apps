package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func smsFixture(t *testing.T) (*sdk.AppCtx, *stubPlatform, ed25519.PrivateKey, *tk.EmitRecorder) {
	t.Helper()
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	platform := &stubPlatform{
		connectionSlugs:  map[int64]string{2: "twilio", 4: "twilio", 147: "telnyx", 148: "telnyx"},
		bindingsOverride: map[string]any{"phone_provider": float64(2), "sms_provider": map[string]any{"ids": []any{float64(2), float64(4), float64(147), float64(148)}, "default_id": float64(147)}},
		whoAmIOverride:   &sdk.InstallIdentity{AppName: "messaging", InstallID: 7, ProjectID: "test-proj", PublicURL: "https://example.com"},
		connectionCreds:  map[int64]map[string]string{2: {"auth_token": "old-token", "account_sid": "ACold"}, 4: {"auth_token": "second-token", "account_sid": "ACsecond"}, 147: {"public_key": base64.StdEncoding.EncodeToString(pub)}, 148: {"public_key": base64.StdEncoding.EncodeToString(make([]byte, 32))}},
	}
	recorder := tk.NewEmitRecorder()
	ctx := newTestCtx(t, platform, tk.WithEmitter(recorder))
	return ctx, platform, key, recorder
}
func seedSMSSender(t *testing.T, ctx *sdk.AppCtx, conn int64, provider, address, profile string, receiveOnly bool) {
	t.Helper()
	preseedSender(t, ctx, senderUpsert{Channel: channelSMS, Address: address, Kind: "phone", Provider: provider, ProviderConnectionID: conn, ProviderIdentityID: "number-" + address, Verified: true, SendingEnabled: true, InboundBootstrapped: true, InboundConfig: string(mustJSON(map[string]string{"messaging_profile_id": profile}))})
	if err := setReceiveOnly(ctx, "test-proj", channelSMS, address, &receiveOnly); err != nil {
		t.Fatal(err)
	}
}
func smsEvent(providerID, profile, to, from, text string) []byte {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	return mustJSON(map[string]any{"data": map[string]any{"id": "event-" + providerID, "event_type": "message.received", "occurred_at": now, "payload": map[string]any{"id": providerID, "direction": "inbound", "received_at": now, "messaging_profile_id": profile, "from": map[string]any{"phone_number": from}, "to": []any{map[string]any{"phone_number": to}}, "text": text}}})
}
func postTelnyx(t *testing.T, raw []byte, key ed25519.PrivateKey, query string, stamp time.Time) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodPost, "/webhooks/telnyx-inbound"+query, bytes.NewReader(raw))
	timestamp := fmt.Sprint(stamp.Unix())
	r.Header.Set("telnyx-timestamp", timestamp)
	r.Header.Set("telnyx-signature-ed25519", base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(timestamp+"|"), raw...))))
	w := httptest.NewRecorder()
	(&App{}).handleTelnyxWebhook(w, r)
	return w
}
func TestSMSMultiProviderSendAndReplyStayOnRegisteredAccount(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	for _, r := range []struct {
		conn             int64
		provider, number string
	}{{2, "twilio", "+15550000002"}, {4, "twilio", "+15550000004"}, {147, "telnyx", "+15550000147"}, {148, "telnyx", "+15550000148"}} {
		seedSMSSender(t, ctx, r.conn, r.provider, r.number, "profile", false)
	}
	p.repliesByConnection = map[int64]map[string]*sdk.ExecuteResult{
		2: {"send_sms": gmailTestReply(map[string]any{"sid": "sms-old"})}, 4: {"send_sms": gmailTestReply(map[string]any{"sid": "sms-second"})},
		147: {"send_sms": gmailTestReply(map[string]any{"data": map[string]any{"id": "telnyx-one"}})}, 148: {"send_sms": gmailTestReply(map[string]any{"data": map[string]any{"id": "telnyx-two"}})},
	}
	for _, conn := range []int64{2, 4, 147, 148} {
		from := fmt.Sprintf("+15550000%03d", conn)
		// Fixture 2/4 use the same E.164 width as 147/148.
		if conn == 2 {
			from = "+15550000002"
		}
		if conn == 4 {
			from = "+15550000004"
		}
		out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "sms", "from": from, "to": "+15559999999", "body": "code 001234"})
		if err != nil {
			t.Fatal(conn, err)
		}
		result := out.(map[string]any)
		if result["status"] != "sent" {
			t.Fatalf("send failed: %+v", result)
		}
		m, err := dbMessageGet(ctx.AppDB(), "test-proj", result["id"].(int64))
		if err != nil || m.ProviderConnectionID != conn {
			t.Fatalf("account not persisted: %+v %v", m, err)
		}
		call := p.executeCalls[len(p.executeCalls)-1]
		if call.ConnID != conn {
			t.Fatalf("wrong account: %+v", call)
		}
		if conn == 147 || conn == 148 {
			if call.Input["text"] != "code 001234" || call.Input["from"] != from {
				t.Fatalf("Telnyx wire payload: %+v", call)
			}
		}
		// Follow-up mode derives the same original sender and account.
		_, err = (&App{}).toolSendMessage(ctx, map[string]any{"reply_to_message_id": m.ID, "body": "follow up"})
		if err != nil {
			t.Fatal(err)
		}
		if p.executeCalls[len(p.executeCalls)-1].ConnID != conn {
			t.Fatal("reply changed providers")
		}
	}
	before := len(p.executeCalls)
	_, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "sms", "from": "+15550000147", "to": "+15559999999", "body": "x", "connection_id": 4})
	if err == nil || len(p.executeCalls) != before {
		t.Fatal("mismatched explicit connection was used")
	}
}
func TestSMSTelnyxWebhookDurableInboxDedupAndReceiveOnly(t *testing.T) {
	ctx, p, key, recorder := smsFixture(t)
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", true)
	// Receive-only numbers never enter configured responders.
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "sms", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	body := "TikTok: 001234\nComplete unicode text ✓"
	raw := smsEvent("sms-in-1", "profile-1", "+15550000147", "12345", body)
	for i := 0; i < 3; i++ {
		w := postTelnyx(t, raw, key, "?connection_id=147&project_id=test-proj", time.Now())
		if w.Code != 200 {
			t.Fatalf("HTTP %d: %s", w.Code, w.Body.String())
		}
	}
	var count, id int64
	var text, job string
	if err := ctx.AppDB().QueryRow(`SELECT count(*),id,body_text FROM messages WHERE provider_message_id='sms-in-1'`).Scan(&count, &id, &text); err != nil || count != 1 || text != body {
		t.Fatalf("inbox count=%d text=%q err=%v", count, text, err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT status FROM inbound_jobs WHERE message_id=?`, id).Scan(&job); err != nil || job != "pending" {
		t.Fatalf("job not durable before ack: %s %v", job, err)
	}
	// A fresh application object resumes work from the durable job, with no
	// webhook request or volatile state available to it.
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	if len(p.callAppCalls) != 0 {
		t.Fatal("receive-only SMS triggered an automatic responder")
	}
	if len(recorder.EventsByTopic("message.received")) != 1 {
		t.Fatalf("receive events=%d", len(recorder.EventsByTopic("message.received")))
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.From != "12345" || m.RouteStatus != "inbox_only" {
		t.Fatalf("SMS lost: %+v %v", m, err)
	}
	if _, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "sms", "from": "+15550000147", "to": "+15559999999", "body": "reply"}); err == nil {
		t.Fatal("receive-only number sent a message")
	}
	health := senderMapWithHealth(ctx, mustFindSender(t, ctx, "+15550000147"))
	if health["last_received_at"] == "" || health["outbound_enabled"] != false {
		t.Fatalf("missing receiver health: %+v", health)
	}
}
func mustFindSender(t *testing.T, ctx *sdk.AppCtx, address string) *senderRow {
	t.Helper()
	s, err := dbFindSender(ctx.AppDB(), "test-proj", "sms", address)
	if err != nil || s == nil {
		t.Fatalf("sender: %v", err)
	}
	return s
}
func TestSMSTelnyxWebhookRejectsInvalidSignatureReplayAndOwnership(t *testing.T) {
	for _, scenario := range []string{"invalid-key", "stale", "future", "wrong-project", "wrong-number", "wrong-profile", "wrong-connection", "tampered-body"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, _, key, _ := smsFixture(t)
			seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", false)
			raw := smsEvent("sms-forbidden", "profile-1", "+15550000147", "TikTok", "001234")
			stamp := time.Now()
			query := "?connection_id=147&project_id=test-proj"
			switch scenario {
			case "invalid-key":
				_, key, _ = ed25519.GenerateKey(rand.Reader)
			case "stale":
				stamp = stamp.Add(-6 * time.Minute)
			case "future":
				stamp = stamp.Add(6 * time.Minute)
			case "wrong-project":
				t.Setenv("APTEVA_PROJECT_ID", "")
				query = "?connection_id=147&project_id=other-project"
			case "wrong-number":
				raw = smsEvent("sms-forbidden", "profile-1", "+15550000999", "12345", "001234")
			case "wrong-profile":
				raw = smsEvent("sms-forbidden", "profile-other", "+15550000147", "12345", "001234")
			case "wrong-connection":
				query = "?connection_id=148&project_id=test-proj"
			}
			var w *httptest.ResponseRecorder
			if scenario == "tampered-body" {
				r := httptest.NewRequest(http.MethodPost, "/webhooks/telnyx-inbound"+query, bytes.NewReader(append(raw, ' ')))
				timestamp := fmt.Sprint(stamp.Unix())
				r.Header.Set("telnyx-timestamp", timestamp)
				r.Header.Set("telnyx-signature-ed25519", base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(timestamp+"|"), raw...))))
				w = httptest.NewRecorder()
				(&App{}).handleTelnyxWebhook(w, r)
			} else {
				w = postTelnyx(t, raw, key, query, stamp)
			}
			if w.Code != 403 {
				t.Fatalf("unexpected acceptance: %d %s", w.Code, w.Body.String())
			}
			var n int
			ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
			if n != 0 {
				t.Fatal("invalid webhook persisted a message")
			}
		})
	}
}
func TestSMSTelnyxSignatureAcceptsExistingHexKeys(t *testing.T) {
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	raw := []byte(`{"a":1}`)
	stamp := fmt.Sprint(time.Now().Unix())
	sig := ed25519.Sign(key, append([]byte(stamp+"|"), raw...))
	if !verifyTelnyxWebhook(raw, stamp, hex.EncodeToString(sig), hex.EncodeToString(pub), time.Now()) {
		t.Fatal("valid hex credentials rejected")
	}
}
func TestSMSRefreshDoesNotDeleteOtherAccountsOrResetPolicy(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	for _, r := range []struct {
		conn             int64
		provider, number string
	}{{2, "twilio", "+15550000002"}, {4, "twilio", "+15550000004"}, {147, "telnyx", "+15550000147"}, {148, "telnyx", "+15550000148"}} {
		seedSMSSender(t, ctx, r.conn, r.provider, r.number, "profile-1", true)
	}
	p.repliesByConnection = map[int64]map[string]*sdk.ExecuteResult{2: {"list_phone_numbers": gmailTestReply(map[string]any{"incoming_phone_numbers": []any{map[string]any{"sid": "PN2", "phone_number": "+15550000002"}}})}, 147: {"list_phone_numbers": gmailTestReply(map[string]any{"data": []any{map[string]any{"id": "number-+15550000147", "phone_number": "+15550000147"}}}), "get_phone_number_messaging_settings": gmailTestReply(map[string]any{"data": map[string]any{"messaging_profile_id": "profile-1"}}), "get_messaging_profile": gmailTestReply(map[string]any{"data": map[string]any{"id": "profile-1", "webhook_url": phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", "test-proj", 147)}})}}
	if err := (&App{}).refreshTwilioChannel(ctx, "test-proj", 2, "sms"); err != nil {
		t.Fatal(err)
	}
	bound, err := choosePhoneBinding(ctx, 147, nil, "sms")
	if err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).refreshTelnyxNumbers(ctx, "test-proj", bound); err != nil {
		t.Fatal(err)
	}
	for _, number := range []string{"+15550000002", "+15550000004", "+15550000147", "+15550000148"} {
		s := mustFindSender(t, ctx, number)
		if s.DeletedAt != nil || s.OutboundEnabled {
			t.Fatalf("refresh changed another account/policy: %+v", s)
		}
	}
}
func TestSMSTelnyxRoutingRetriesWithoutDuplicateInboxRows(t *testing.T) {
	ctx, p, key, _ := smsFixture(t)
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", false)
	if _, err := dbInboundRouteUpsert(ctx.AppDB(), "test-proj", "sms", "*", "crm", "/inbound", 0); err != nil {
		t.Fatal(err)
	}
	p.callAppResultErr = errors.New("receiver unavailable")
	raw := smsEvent("route-retry", "profile-1", "+15550000147", "+15559999999", "hello")
	if w := postTelnyx(t, raw, key, "?connection_id=147", time.Now()); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	var id int64
	ctx.AppDB().QueryRow(`SELECT id FROM messages WHERE provider_message_id='route-retry'`).Scan(&id)
	p.callAppResultErr = nil
	if err := scheduleInboundRetry(ctx, "test-proj", id); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM messages`).Scan(&n)
	if n != 1 {
		t.Fatal("routing retry duplicated SMS")
	}
	m, err := dbMessageGet(ctx.AppDB(), "test-proj", id)
	if err != nil || m.RouteStatus != "ok" {
		t.Fatalf("routing not resumed: %+v %v", m, err)
	}
}
func TestSMSMultiAccountTwilioWebhookUsesItsOwnKey(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	seedSMSSender(t, ctx, 4, "twilio", "+15550000004", "", false)
	form := url.Values{"From": {"+15559999999"}, "To": {"+15550000004"}, "MessageSid": {"SM-in"}, "AccountSid": {"ACsecond"}, "Body": {"001234"}}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/twilio-inbound?connection_id=4&project_id=test-proj", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// Existing test helper signs the full public URL exactly as Twilio does.
	req.Header.Set("X-Twilio-Signature", computeTwilioSignature(reconstructPublicURL(req), form, "second-token"))
	w := httptest.NewRecorder()
	(&App{}).handleTwilioInboundWebhook(w, req)
	if w.Code != 200 {
		t.Fatalf("HTTP %d %s", w.Code, w.Body.String())
	}
	var conn int64
	ctx.AppDB().QueryRow(`SELECT provider_connection_id FROM messages WHERE provider_message_id='SM-in'`).Scan(&conn)
	if conn != 4 {
		t.Fatalf("stored connection=%d", conn)
	}
	if len(p.callAppCalls) != 0 {
		t.Fatal("synchronous webhook routing")
	}
}

func computeTwilioSignature(fullURL string, form url.Values, token string) string {
	keys := make([]string, 0, len(form))
	for key := range form {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	raw := fullURL
	for _, key := range keys {
		raw += key + form.Get(key)
	}
	mac := hmac.New(sha1.New, []byte(token))
	mac.Write([]byte(raw))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func TestSMSTelnyxAdoptionChecksProfilesAndPreservesVoice(t *testing.T) {
	for _, shared := range []bool{false, true} {
		t.Run(fmt.Sprint("shared=", shared), func(t *testing.T) {
			ctx, p, _, _ := smsFixture(t)
			callback := phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", "test-proj", 147)
			members := []any{map[string]any{"id": "phone-1", "phone_number": "+15550000147"}}
			if shared {
				members = append(members, map[string]any{"id": "another-phone", "phone_number": "+15550000222"})
			}
			p.repliesByConnection = map[int64]map[string]*sdk.ExecuteResult{147: {
				"list_phone_numbers":                   gmailTestReply(map[string]any{"data": []any{map[string]any{"id": "phone-1", "phone_number": "+15550000147", "country_code": "US"}}}),
				"get_phone_number_messaging_settings":  gmailTestReply(map[string]any{"data": map[string]any{"messaging_profile_id": "profile-1"}}),
				"get_messaging_profile":                gmailTestReply(map[string]any{"data": map[string]any{"id": "profile-1", "webhook_url": "https://previous.example/inbound"}}),
				"list_messaging_profile_phone_numbers": gmailTestReply(map[string]any{"data": members}),
				"update_messaging_profile":             gmailTestReply(map[string]any{"data": map[string]any{"id": "profile-1", "webhook_url": callback}}),
			}}
			receiveOnly := true
			req := sendersCreateReq{Address: "+15550000147", Channel: "sms", ConnectionID: 147, Inbound: "true", ReceiveOnly: &receiveOnly, ProjectID: "test-proj"}
			_, err := (&App{}).sendersCreateImpl(ctx, req)
			if shared {
				if err == nil || !strings.Contains(err.Error(), "shared") {
					t.Fatalf("shared webhook silently overwritten: %v", err)
				}
				for _, call := range p.executeCalls {
					if call.Tool == "update_messaging_profile" {
						t.Fatal("shared profile changed without explicit choice")
					}
				}
				req.AllowSharedProfile = true
				_, err = (&App{}).sendersCreateImpl(ctx, req)
			}
			if err != nil {
				t.Fatal(err)
			}
			row := mustFindSender(t, ctx, req.Address)
			if row.Provider != "telnyx" || row.ProviderConnectionID != 147 || row.OutboundEnabled || !row.InboundBootstrapped {
				t.Fatalf("adoption did not pin account/policy: %+v", row)
			}
			for _, call := range p.executeCalls {
				if call.Tool == "update_phone_number" || strings.Contains(call.Tool, "voice") {
					t.Fatalf("SMS setup changed voice: %+v", call)
				}
				if call.Tool == "update_messaging_profile" {
					for key := range call.Input {
						if key != "id" && key != "webhook_url" && key != "webhook_api_version" {
							t.Fatalf("unrelated profile settings overwritten: %v", call.Input)
						}
					}
				}
			}
		})
	}
}

func TestSMSDeliveryEventsAreAccountScopedAndIdempotent(t *testing.T) {
	ctx, _, key, _ := smsFixture(t)
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", false)
	// Same provider message ID on two accounts must never update both rows.
	for _, conn := range []int64{147, 148} {
		if _, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,provider_slug,provider_connection_id,provider_message_id) VALUES('test-proj','sms','out','+15550000147','["+15559999999"]','sent','telnyx',?,'same-id')`, conn); err != nil {
			t.Fatal(err)
		}
	}
	raw := mustJSON(map[string]any{"data": map[string]any{"id": "delivery-1", "event_type": "message.finalized", "occurred_at": time.Now().UTC().Format(time.RFC3339), "payload": map[string]any{"id": "same-id", "direction": "outbound", "messaging_profile_id": "profile-1", "from": map[string]any{"phone_number": "+15550000147"}, "to": []any{map[string]any{"phone_number": "+15559999999", "status": "delivered"}}}}})
	for i := 0; i < 2; i++ {
		w := postTelnyx(t, raw, key, "?connection_id=147", time.Now())
		if w.Code != 200 {
			t.Fatal(w.Code, w.Body.String())
		}
	}
	var a, b string
	ctx.AppDB().QueryRow(`SELECT status FROM messages WHERE provider_connection_id=147`).Scan(&a)
	ctx.AppDB().QueryRow(`SELECT status FROM messages WHERE provider_connection_id=148`).Scan(&b)
	if a != "delivered" || b != "sent" {
		t.Fatalf("cross-account delivery update: %s %s", a, b)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM delivery_events`).Scan(&n)
	if n != 1 {
		t.Fatalf("duplicate delivery events: %d", n)
	}
}

func TestSMSDeliveryBeforeSendResponseIsReplayedOnCorrectAccount(t *testing.T) {
	ctx, _, key, _ := smsFixture(t)
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", false)
	raw := mustJSON(map[string]any{"data": map[string]any{"id": "early-delivery", "event_type": "message.finalized", "occurred_at": time.Now().UTC().Format(time.RFC3339), "payload": map[string]any{"id": "early-id", "direction": "outbound", "messaging_profile_id": "profile-1", "from": map[string]any{"phone_number": "+15550000147"}, "to": []any{map[string]any{"phone_number": "+15559999999", "status": "delivered"}}}}})
	if w := postTelnyx(t, raw, key, "?connection_id=147", time.Now()); w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	if _, err := ctx.AppDB().Exec(`INSERT INTO messages(project_id,channel,direction,from_addr,to_addrs,status,provider_slug,provider_connection_id,provider_message_id) VALUES('test-proj','sms','out','+15550000147','["+15559999999"]','sent','telnyx',147,'early-id')`); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).retryMessagingWork(ctx); err != nil {
		t.Fatal(err)
	}
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM messages WHERE provider_message_id='early-id'`).Scan(&status)
	if status != "delivered" {
		t.Fatalf("early delivery not replayed: %s", status)
	}
	var queued int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM unmatched_provider_events`).Scan(&queued)
	if queued != 0 {
		t.Fatal("delivery event not consumed")
	}
}

func TestSMSLegacyUnregisteredTwilioStillWorksWithNewProvidersBound(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	p.repliesByConnection = map[int64]map[string]*sdk.ExecuteResult{2: {"send_sms": gmailTestReply(map[string]any{"sid": "legacy-sms"})}}
	out, err := (&App{}).toolSendMessage(ctx, map[string]any{"channel": "sms", "from": "+15550000002", "to": "+15559999999", "body": "legacy call"})
	if err != nil || out.(map[string]any)["status"] != "sent" || p.executeCalls[len(p.executeCalls)-1].ConnID != 2 {
		t.Fatalf("legacy sending changed: %v %v", out, err)
	}
	form := url.Values{"From": {"+15559999999"}, "To": {"+15550000002"}, "MessageSid": {"legacy-in"}, "AccountSid": {"ACold"}, "Body": {"001234"}}
	req := httptest.NewRequest(http.MethodPost, "/webhooks/twilio-inbound?project_id=test-proj", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("X-Twilio-Signature", computeTwilioSignature(reconstructPublicURL(req), form, "old-token"))
	w := httptest.NewRecorder()
	(&App{}).handleTwilioInboundWebhook(w, req)
	if w.Code != 200 {
		t.Fatalf("legacy receiving changed: %d %s", w.Code, w.Body.String())
	}
}

func TestSMSConcurrentWebhookRetriesCommitOneMessageAndJob(t *testing.T) {
	ctx, _, key, _ := smsFixture(t)
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "profile-1", true)
	raw := smsEvent("concurrent-sms", "profile-1", "+15550000147", "12345", "001234")
	codes := make(chan int, 12)
	var wg sync.WaitGroup
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); codes <- postTelnyx(t, raw, key, "?connection_id=147", time.Now()).Code }()
	}
	wg.Wait()
	close(codes)
	for code := range codes {
		if code != 200 {
			t.Fatalf("concurrent webhook failed: %d", code)
		}
	}
	for _, table := range []string{"messages", "inbound_jobs"} {
		var n int
		if err := ctx.AppDB().QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil || n != 1 {
			t.Fatalf("%s count=%d err=%v", table, n, err)
		}
	}
}
func TestSMSLegacySenderRefreshPinsOriginalTwilioAccount(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	preseedSender(t, ctx, senderUpsert{Channel: "sms", Address: "+15550000002", Kind: "phone", Provider: "twilio", Verified: true, SendingEnabled: true})
	receiveOnly := true
	if err := setReceiveOnly(ctx, "test-proj", "sms", "+15550000002", &receiveOnly); err != nil {
		t.Fatal(err)
	}
	p.repliesByConnection = map[int64]map[string]*sdk.ExecuteResult{2: {"list_phone_numbers": gmailTestReply(map[string]any{"incoming_phone_numbers": []any{map[string]any{"phone_number": "+15550000002", "sid": "PN2"}}})}}
	if err := (&App{}).refreshTwilioChannel(ctx, "test-proj", 2, "sms"); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).refreshTwilioChannel(ctx, "test-proj", 4, "sms"); err != nil {
		t.Fatal(err)
	}
	row := mustFindSender(t, ctx, "+15550000002")
	if row.ProviderConnectionID != 2 || row.OutboundEnabled || row.DeletedAt != nil {
		t.Fatalf("legacy number changed ownership or policy: %+v", row)
	}
}

func TestSMSSharedGlobalProfileRoutesByNumberNotProjectQuery(t *testing.T) {
	ctx, _, key, _ := smsFixture(t)
	t.Setenv("APTEVA_PROJECT_ID", "")
	seedSMSSender(t, ctx, 147, "telnyx", "+15550000147", "shared-profile", true)
	if _, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{ProjectID: "other-project", Channel: "sms", Address: "+15550000149", Kind: "phone", Provider: "telnyx", ProviderConnectionID: 147, ProviderIdentityID: "second-phone", Verified: true, SendingEnabled: true, InboundConfig: string(mustJSON(map[string]string{"messaging_profile_id": "shared-profile"}))}); err != nil {
		t.Fatal(err)
	}
	for _, entry := range []struct{ id, number, project string }{{"global-one", "+15550000147", "test-proj"}, {"global-two", "+15550000149", "other-project"}} {
		w := postTelnyx(t, smsEvent(entry.id, "shared-profile", entry.number, "12345", "001234"), key, "?connection_id=147", time.Now())
		if w.Code != 200 {
			t.Fatalf("shared profile message rejected: %d %s", w.Code, w.Body.String())
		}
		var project string
		ctx.AppDB().QueryRow(`SELECT project_id FROM messages WHERE provider_message_id=?`, entry.id).Scan(&project)
		if project != entry.project {
			t.Fatalf("SMS routed to %q instead of %q", project, entry.project)
		}
	}
	if strings.Contains(phoneWebhookURL(ctx, "/webhooks/telnyx-inbound", "test-proj", 147), "project_id=") {
		t.Fatal("shared profile URL must not force every number into one project")
	}
}

func TestSMSInventoryHonorsProviderPaginationBeforeReconcile(t *testing.T) {
	ctx, p, _, _ := smsFixture(t)
	p.executeOverride = func(tool string, prior int) *sdk.ExecuteResult {
		if tool != "list_phone_numbers" {
			return nil
		}
		return gmailTestReply(map[string]any{"data": []any{map[string]any{"id": fmt.Sprint(prior), "phone_number": fmt.Sprintf("+15550000%03d", prior)}}, "meta": map[string]any{"total_pages": 2}})
	}
	rows, err := telnyxListNumbers(ctx, 147, "list_phone_numbers", nil)
	if err != nil || len(rows) != 2 || len(p.executeCalls) != 2 {
		t.Fatalf("partial account inventory treated as complete: rows=%v calls=%d err=%v", rows, len(p.executeCalls), err)
	}
}

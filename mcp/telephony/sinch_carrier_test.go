package main

import (
	"crypto/hmac"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestSinchOutboundSessionAndNamedLegHangup(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"create_call": json.RawMessage(`{"sessionId":"session-1","serviceId":"service-1"}`),
	}}
	app, ctx := withTelephonyTestContext(t, platform)
	carrier := &sinchCarrier{app: app, connID: 18, fields: map[string]string{"service_id": "service-1", "service_secret": "F5wrP9SKYU6w8sbZXkp7GA=="}}
	result, err := carrier.Place(ctx, carrierPlaceRequest{CallID: "local-1", CallbackSecret: "secret", ProjectID: "project-a",
		To: "+33123456789", From: "+33123456780", TimeoutSec: 25, MaxDurationSec: 3600})
	if err != nil || result.CarrierSID != "session-1" {
		t.Fatalf("place result=%+v err=%v", result, err)
	}
	call := platform.integrationCalls[0]
	if call.Tool != "create_call" || call.Input["serviceId"] != "service-1" {
		t.Fatalf("create call: %#v", call)
	}
	commands, _ := json.Marshal(call.Input["commands"])
	for _, part := range []string{`"callName":"customer"`, `"type":"PHONE"`, `"command":"webhook"`, `/webhook/status/local-1`} {
		if !strings.Contains(string(commands), part) {
			t.Fatalf("Sinch call graph missing %q: %s", part, commands)
		}
	}
	answer, _ := json.Marshal(sinchAnswerCommands("wss://example.test/media/sinch/local-1/secret"))
	for _, part := range []string{`"type":"STREAM"`, `"sampleRate":16000`, `"command":"bridgeCall"`, `/media/sinch/local-1/secret`} {
		if !strings.Contains(string(answer), part) {
			t.Fatalf("Sinch answer commands missing %q: %s", part, answer)
		}
	}
	if err := carrier.Hangup(ctx, &callRow{CarrierSID: result.CarrierSID}); err != nil {
		t.Fatal(err)
	}
	if got := platform.integrationCalls[1]; got.Tool != "patch_call_by_name" || got.Input["sessionId"] != "session-1" || got.Input["callName"] != "customer" {
		t.Fatalf("hangup: %#v", got)
	}
	if _, err := (&sinchCarrier{app: app, fields: map[string]string{"service_id": "service-1"}}).Place(ctx, carrierPlaceRequest{}); err == nil {
		t.Fatal("placed a call without a webhook signing secret")
	}
}

func TestSinchLifecycleIgnoresStreamAndUsesSessionIdentity(t *testing.T) {
	for _, tc := range []struct{ event, result, want string }{
		{"call.webhook.answered", "IN_PROGRESS", "answered"},
		{"call.webhook.completed", "COMPLETED", "completed"},
		{"call.webhook.no_answer", "NO_ANSWER", "no-answer"},
		{"call.webhook.completed", "CANCEL", "canceled"},
	} {
		body := `{"event":"` + tc.event + `","call":{"callId":"call-1","sessionId":"session-1","callType":"PHONE","callResult":"` + tc.result + `"}}`
		r := httptest.NewRequest(http.MethodPost, "/webhook/status/local-1", strings.NewReader(body))
		r.Header.Set("ce-id", "event-1")
		update := sinchCallbackUpdate(r)
		if update.Status != tc.want || update.CarrierSID != "session-1" || update.Facts.ProviderEventID != "event-1" {
			t.Fatalf("event %s: %+v", tc.event, update)
		}
	}
	r := httptest.NewRequest(http.MethodPost, "/webhook/status/local-1", strings.NewReader(`{"event":"call.webhook.completed","call":{"callId":"call-2","sessionId":"session-1","callType":"STREAM","callResult":"COMPLETED"}}`))
	if update := sinchCallbackUpdate(r); update.Status != "" {
		t.Fatalf("stream leg changed customer status: %+v", update)
	}
}

func TestSinchSignedAnswerWebhookReturnsStreamCommands(t *testing.T) {
	const serviceID = "a74b1566-0f18-4f8e-9c23-8e6b5df8fd3e"
	const serviceSecret = "F5wrP9SKYU6w8sbZXkp7GA=="
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{
		Slug: "sinch", Fields: map[string]string{"service_id": serviceID, "service_secret": serviceSecret},
	}}
	app, _ := withTelephonyTestContext(t, platform)
	row := phoneTestCall(t, app, "sinch-answer", "initiated")
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_slug='sinch',carrier_sid='session-1',carrier_connection_id=18,direction='outbound' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	callbackPath := "/webhook/status/" + row.ID + "?token=" + row.CallbackSecret + "&project_id=" + row.ProjectID
	publicURL, err := url.Parse(app.statusCallbackURL(row.ID, row.CallbackSecret, row.ProjectID))
	if err != nil {
		t.Fatal(err)
	}
	body := `{"event":"call.webhook.answered","call":{"callId":"call-1","sessionId":"session-1","callType":"PHONE","callResult":"IN_PROGRESS"}}`
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	key, _ := base64.StdEncoding.DecodeString(serviceSecret)
	digest := md5.Sum([]byte(body))
	canonical := "POST\n" + base64.StdEncoding.EncodeToString(digest[:]) + "\napplication/json\nx-timestamp:" + timestamp + "\n" + publicURL.EscapedPath()
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(canonical))
	signature := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	request := func(signature string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, callbackPath, strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("x-timestamp", timestamp)
		r.Header.Set("Authorization", "service "+serviceID+":"+signature)
		return r
	}
	w := httptest.NewRecorder()
	app.handleStatusCallback(w, request("invalid"))
	if w.Code != http.StatusForbidden {
		t.Fatalf("invalid signature returned %d", w.Code)
	}
	w = httptest.NewRecorder()
	app.handleStatusCallback(w, request(signature))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"command":"bridgeCall"`) ||
		!strings.Contains(w.Body.String(), `/media/sinch/sinch-answer/`) {
		t.Fatalf("Sinch answer response: %d %s", w.Code, w.Body.String())
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil || stored.Status != "answered" {
		t.Fatalf("Sinch answer state: row=%+v err=%v", stored, err)
	}
}

package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestSinchStreamConnectRequiresNegotiatedPCM(t *testing.T) {
	valid := `{"command":"connect","version":1,"callId":"call-1","applicationKey":"service-1","codec":"PCM","sampleRate":16000,"callHeaders":{}}`
	if err := validateSinchConnect([]byte(valid), "service-1"); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{
		`{"command":"answer","version":1,"callId":"call-1","applicationKey":"service-1","codec":"PCM","sampleRate":16000}`,
		`{"command":"connect","version":1,"callId":"call-1","applicationKey":"service-1","codec":"PCMU","sampleRate":16000}`,
		`{"command":"connect","version":1,"callId":"call-1","applicationKey":"service-1","codec":"PCM","sampleRate":8000}`,
		`{"command":"connect","version":1,"callId":"","applicationKey":"service-1","codec":"PCM","sampleRate":16000}`,
	} {
		if err := validateSinchConnect([]byte(invalid), "service-1"); err == nil {
			t.Fatalf("accepted unsupported Sinch stream: %s", invalid)
		}
	}
	if err := validateSinchConnect([]byte(valid), "other-service"); err == nil {
		t.Fatal("accepted stream from another Sinch service")
	}
}

func TestSinchOnlyHandlesNamedOutboundWebhookEvents(t *testing.T) {
	path := "/webhook/status/call-1"
	call := `"call":{"callId":"provider-call","sessionId":"session-1","callType":"PHONE","callResult":"IN_PROGRESS"}`
	answered := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"event":"call.webhook.answered",`+call+`}`))
	update := sinchCallbackUpdate(answered)
	if update.Status != "answered" || update.CarrierSID != "session-1" {
		t.Fatalf("named answer webhook: %+v", update)
	}
	// An ordinary service event must not receive the named webhook's SVAML
	// response; otherwise the status handler could hang up an active call.
	ordinary := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"event":"call.answered",`+call+`}`))
	if update := sinchCallbackUpdate(ordinary); update.Status != "" {
		t.Fatalf("ordinary answer event was treated as our webhook: %+v", update)
	}
}

func TestSinchWebhookOfficialSigningVector(t *testing.T) {
	const body = `{"event":"call.incoming","call":{"callId":"01AN4Z07BY79KA1307SR9X4MV3"}}`
	const timestamp = "2026-04-01T12:00:00.0000000Z"
	const serviceID = "a74b1566-0f18-4f8e-9c23-8e6b5df8fd3e"
	const secret = "F5wrP9SKYU6w8sbZXkp7GA=="
	makeRequest := func(payload, path, signature string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(payload))
		r.Header.Set("Content-Type", "application/json; charset=utf-8")
		r.Header.Set("x-timestamp", timestamp)
		r.Header.Set("Authorization", "service "+serviceID+":"+signature)
		return r
	}
	now := time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC)
	const signature = "EWFtVTrykdhMTdyYSbn40GBJpf5UBeggO9T99sdwLyY="
	if err := verifySinchWebhook(makeRequest(body, "/voice-webhooks", signature), []byte(body), serviceID, secret, "/voice-webhooks", now); err != nil {
		t.Fatalf("official Sinch vector: %v", err)
	}
	if err := verifySinchWebhook(makeRequest(body+" ", "/voice-webhooks", signature), []byte(body+" "), serviceID, secret, "/voice-webhooks", now); err == nil {
		t.Fatal("modified body was accepted")
	}
	if err := verifySinchWebhook(makeRequest(body, "/other", signature), []byte(body), serviceID, secret, "/other", now); err == nil {
		t.Fatal("modified path was accepted")
	}
	if err := verifySinchWebhook(makeRequest(body, "/voice-webhooks", signature), []byte(body), serviceID, secret, "/voice-webhooks", now.Add(6*time.Minute)); err == nil {
		t.Fatal("stale webhook was accepted")
	}
}

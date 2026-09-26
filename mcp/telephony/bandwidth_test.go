package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestBandwidthOutboundUsesVerifiedBXMLAndSameCallHangup(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"create_call": json.RawMessage(`{"callId":"c-bandwidth-1"}`),
	}}
	a, ctx := withTelephonyTestContext(t, platform)
	carrier := &bandwidthCarrier{app: a, connID: 19, fields: map[string]string{"application_id": "app-1"}}
	request := carrierPlaceRequest{CallID: "local-1", CallbackSecret: "callback-secret", ProjectID: "project-a",
		To: "+33123456789", From: "+33123456780", TimeoutSec: 25}
	result, err := carrier.Place(ctx, request)
	if err != nil || result.CarrierSID != "c-bandwidth-1" {
		t.Fatalf("place result=%+v err=%v", result, err)
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "create_call" {
		t.Fatalf("carrier calls: %#v", platform.integrationCalls)
	}
	input := platform.integrationCalls[0].Input
	if input["applicationId"] != "app-1" || input["username"] != "apteva" || input["password"] != "callback-secret" ||
		!strings.Contains(input["answerUrl"].(string), "/xml/bandwidth/local-1") {
		t.Fatalf("unsafe or incomplete Bandwidth call input: %#v", input)
	}
	if err := carrier.Hangup(ctx, &callRow{CarrierSID: result.CarrierSID}); err != nil {
		t.Fatal(err)
	}
	if got := platform.integrationCalls[1]; got.Tool != "update_call" || got.Input["callId"] != "c-bandwidth-1" || got.Input["state"] != "completed" {
		t.Fatalf("hangup: %#v", got)
	}
	missingApp := &bandwidthCarrier{app: a, connID: 19}
	if _, err := missingApp.Place(ctx, request); err == nil {
		t.Fatal("call was placed without an application ID")
	}
	if _, err := carrier.Place(ctx, carrierPlaceRequest{MachineDetection: "detect"}); err == nil {
		t.Fatal("unsupported machine detection was silently accepted")
	}
}

func TestBandwidthAnswerRequiresCallbackCredentialsAndReturnsBidirectionalBXML(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := phoneTestCall(t, app, "bandwidth-answer", "ringing")
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_slug='bandwidth',carrier_sid='c-bandwidth-1' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findCall(row.ID)
	row = *stored
	path := "/xml/bandwidth/" + row.ID + "?token=" + row.CallbackSecret + "&project_id=" + row.ProjectID
	makeRequest := func(user, password, body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		if user != "" {
			r.SetBasicAuth(user, password)
		}
		w := httptest.NewRecorder()
		app.handleBandwidthXML(w, r)
		return w
	}
	answer := `{"eventType":"answer","callId":"c-bandwidth-1"}`
	if w := makeRequest("", "", answer); w.Code != http.StatusForbidden {
		t.Fatalf("missing Basic authentication: %d", w.Code)
	}
	if w := makeRequest("apteva", "wrong", answer); w.Code != http.StatusForbidden {
		t.Fatalf("wrong Basic authentication: %d", w.Code)
	}
	if w := makeRequest("apteva", row.CallbackSecret, `{"eventType":"answer","callId":"c-other"}`); w.Code != http.StatusForbidden {
		t.Fatalf("mismatched call ID: %d", w.Code)
	}
	w := makeRequest("apteva", row.CallbackSecret, answer)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `mode="bidirectional"`) ||
		!strings.Contains(w.Body.String(), `wait="true"`) || !strings.Contains(w.Body.String(), "/media/bandwidth/") {
		t.Fatalf("BXML response: %d %s", w.Code, w.Body.String())
	}
}

func TestBandwidthFramesAndLifecycleAreProviderSpecific(t *testing.T) {
	var frame carrierMediaFrame
	if err := json.Unmarshal([]byte(`{"eventType":"start","metadata":{"callId":"c-bandwidth-1","streamId":"s-1","tracks":[{"name":"inbound","mediaFormat":{"encoding":"PCMU","sampleRate":8000}}]}}`), &frame); err != nil {
		t.Fatal(err)
	}
	if err := validateCarrierStartFormat(jsonMediaBridgeConfig{Provider: "bandwidth"}, frame); err != nil {
		t.Fatal(err)
	}
	if frameCallID(frame) != "c-bandwidth-1" || frameStreamID(frame) != "s-1" {
		t.Fatalf("stream identity: %+v", frame.Metadata)
	}
	if got := buildCarrierOutbound("bandwidth", "s-1", "audio").(map[string]any); got["eventType"] != "playAudio" {
		t.Fatalf("outbound frame: %#v", got)
	}
	request := httptest.NewRequest(http.MethodPost, "/webhook/status/call", strings.NewReader(`{"eventType":"disconnect","eventTime":"2026-09-24T10:00:00Z","callId":"c-bandwidth-1","cause":"normal"}`))
	update := callbackUpdateFor("bandwidth", request)
	if update.Status != "completed" || update.CarrierSID != "c-bandwidth-1" || update.Facts.TerminationCause != "normal" {
		t.Fatalf("callback update: %+v", update)
	}
}

func TestBandwidthRecordingControlAndDuration(t *testing.T) {
	platform := &answerPlatform{}
	_, ctx := withTelephonyTestContext(t, platform)
	carrier := &bandwidthCarrier{connID: 19}
	row := &callRow{CarrierSID: "c-bandwidth-1"}
	if err := carrier.PauseRecording(ctx, row, "pause-1"); err != nil {
		t.Fatal(err)
	}
	if err := carrier.ResumeRecording(ctx, row, "resume-1"); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Tool != "update_call_recording" ||
		platform.integrationCalls[0].Input["state"] != "paused" || platform.integrationCalls[1].Input["state"] != "recording" {
		t.Fatalf("recording controls: %#v", platform.integrationCalls)
	}
	if got := bandwidthDurationMS("PT1M13.67S"); got != 73670 {
		t.Fatalf("ISO-8601 recording duration: %d", got)
	}
}

func TestBandwidthRecordingCallbackIsAuthenticatedAndScoped(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := phoneTestCall(t, app, "bandwidth-recording", "in-progress")
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_slug='bandwidth',carrier_sid='c-bandwidth-1',recording_mode='always' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	stored, _ := app.db().findCall(row.ID)
	path := "/webhook/recording/bandwidth/" + row.ID + "?token=" + stored.CallbackSecret + "&project_id=" + stored.ProjectID
	body := `{"eventType":"recordingAvailable","callId":"c-bandwidth-1","recordingId":"r-bandwidth-1","duration":"PT13.67S","fileFormat":"wav"}`
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	w := httptest.NewRecorder()
	app.handleBandwidthRecordingStatus(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("unsigned recording callback: %d", w.Code)
	}
	req = httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.SetBasicAuth("apteva", stored.CallbackSecret)
	w = httptest.NewRecorder()
	app.handleBandwidthRecordingStatus(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("recording callback: %d %s", w.Code, w.Body.String())
	}
	if got := bandwidthDurationMS("PT13.67S"); got != 13670 {
		t.Fatalf("recording duration: %d", got)
	}
}

func TestBandwidthStatusCallbackRejectsDifferentCarrierCall(t *testing.T) {
	app, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := phoneTestCall(t, app, "bandwidth-status", "ringing")
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_slug='bandwidth',carrier_sid='c-bandwidth-1' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	stored, err := app.db().findCall(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	path := "/webhook/status/" + row.ID + "?token=" + stored.CallbackSecret + "&project_id=" + stored.ProjectID
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"eventType":"disconnect","callId":"c-other","eventTime":"2026-09-24T10:00:00Z"}`))
	req.SetBasicAuth("apteva", stored.CallbackSecret)
	w := httptest.NewRecorder()
	app.handleStatusCallback(w, req)
	if w.Code != http.StatusForbidden {
		t.Fatalf("foreign carrier call callback returned %d: %s", w.Code, w.Body.String())
	}
	current, err := app.db().findCall(row.ID)
	if err != nil || current.Status != "ringing" {
		t.Fatalf("callback changed call status: row=%+v err=%v", current, err)
	}
}

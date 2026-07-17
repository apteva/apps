package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestTwilioStreamTwiMLRecordingIsOptIn(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}

	call := testCall("twiml-recording", "answered")
	call.RecordingMode = recordingModeOff
	without := app.twilioStreamTwiML(&call)
	if strings.Contains(without, "<Recording") {
		t.Fatalf("recording appeared while disabled: %s", without)
	}

	call.RecordingMode = recordingModeAlways
	call.RecordingChannels = "dual"
	with := app.twilioStreamTwiML(&call)
	for _, expected := range []string{"<Start><Recording", `channels="dual"`, `track="both"`, "/webhook/recording/twilio/", "<Connect><Stream"} {
		if !strings.Contains(with, expected) {
			t.Fatalf("recording TwiML missing %q: %s", expected, with)
		}
	}
	if strings.Index(with, "<Recording") > strings.Index(with, "<Connect>") {
		t.Fatalf("recording must start before the media stream: %s", with)
	}
}

func TestTwilioRecordingCallbackIsVerifiedAndIdempotent(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}
	call := testCall("recording-callback", "completed")
	call.RecordingMode = recordingModeAlways
	call.RecordingChannels = "dual"
	call.RecordingStorageMode = recordingStorageCopy
	call.EndedAt = time.Now().UTC().Format(time.RFC3339)
	if err := app.db().insertCall(call); err != nil {
		t.Fatal(err)
	}

	form := url.Values{
		"CallSid":           {call.CarrierSID},
		"RecordingSid":      {"RE00000000000000000000000000000001"},
		"RecordingStatus":   {"completed"},
		"RecordingDuration": {"12"},
		"RecordingChannels": {"2"},
	}
	path := "/webhook/recording/twilio/" + call.ID + "?token=" + call.CallbackSecret + "&project_id=" + call.ProjectID
	invoke := func() *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(form.Encode()))
		signTwilioTestRequest(t, app, req, form)
		response := httptest.NewRecorder()
		app.handleTwilioRecordingStatus(response, req)
		return response
	}
	if response := invoke(); response.Code != http.StatusNoContent {
		t.Fatalf("callback status=%d body=%s", response.Code, response.Body.String())
	}
	if response := invoke(); response.Code != http.StatusNoContent {
		t.Fatalf("duplicate callback status=%d body=%s", response.Code, response.Body.String())
	}
	recordings, err := app.db().listRecordings(call.ProjectID, call.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(recordings) != 1 {
		t.Fatalf("recording count=%d, want one idempotent row", len(recordings))
	}
	got := recordings[0]
	if got.DurationMS != 12000 || got.Channels != 2 || got.StorageStatus != "pending" {
		t.Fatalf("unexpected recording row: %+v", got)
	}
}

func TestTwilioRecordingCallbackRejectsMismatchedCall(t *testing.T) {
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	app := &App{installID: 42}
	call := testCall("recording-mismatch", "completed")
	call.RecordingMode = recordingModeAlways
	if err := app.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"CallSid": {"CA-other"}, "RecordingSid": {"RE00000000000000000000000000000002"},
		"RecordingStatus": {"completed"},
	}
	path := "/webhook/recording/twilio/" + call.ID + "?token=" + call.CallbackSecret + "&project_id=" + call.ProjectID
	req := httptest.NewRequest(http.MethodPost, "https://example.test"+path, strings.NewReader(form.Encode()))
	signTwilioTestRequest(t, app, req, form)
	response := httptest.NewRecorder()
	app.handleTwilioRecordingStatus(response, req)
	if response.Code != http.StatusForbidden {
		t.Fatalf("mismatched call status=%d body=%s", response.Code, response.Body.String())
	}
}

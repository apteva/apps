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

func TestRecordingStorageDependencyIsOptional(t *testing.T) {
	manifest := (&App{}).Manifest()
	if manifest.Version != "0.1.12" {
		t.Fatalf("manifest version=%q, want 0.1.12", manifest.Version)
	}
	for _, dependency := range manifest.Requires.Apps {
		if dependency.Name == "storage" {
			if !dependency.Optional {
				t.Fatal("Storage dependency must remain optional")
			}
			return
		}
	}
	t.Fatal("optional Storage dependency is not declared")
}

func TestStorageBindingProbeTreatsMissingBindingAsProviderOnly(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != storageRecordingProxyPath+"/health" {
			t.Fatalf("unexpected probe path %q", r.URL.Path)
		}
		http.Error(w, "target app is not bound: storage", http.StatusForbidden)
	}))
	t.Cleanup(server.Close)
	client := &recordingStorageClient{base: server.URL, token: "test", http: server.Client()}
	available, err := client.bindingAvailable(t.Context(), "project-a")
	if err != nil {
		t.Fatal(err)
	}
	if available {
		t.Fatal("missing optional Storage binding reported as available")
	}
}

func TestProviderRecordingHasPrivatePlaybackURLWithoutStorage(t *testing.T) {
	out := recordingPublic(recordingRow{
		ID: "rec-provider", ProjectID: "project-a", Provider: "twilio",
		ProviderStatus: "completed", ProviderRecordingID: "RE00000000000000000000000000000003",
		StorageStatus: recordingStorageProvider,
	})
	if out["playback_source"] != "provider" {
		t.Fatalf("playback source=%v, want provider", out["playback_source"])
	}
	want := "/api/apps/telephony/recordings/rec-provider/content?project_id=project-a"
	if out["playback_url"] != want {
		t.Fatalf("playback URL=%v, want %q", out["playback_url"], want)
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

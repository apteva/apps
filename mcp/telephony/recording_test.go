package main

import (
	"encoding/binary"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
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
	if manifest.Version != "0.1.17" {
		t.Fatalf("manifest version=%q, want 0.1.17", manifest.Version)
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
	urls, ok := out["playback_urls"].(map[string]string)
	if !ok || urls[recordingVariantMix] != want || !strings.Contains(urls[recordingVariantOriginal], "variant=original") {
		t.Fatalf("unexpected playback variants: %#v", out["playback_urls"])
	}
}

func writeTestDualRecording(t *testing.T, callerAmplitude, agentAmplitude int16) string {
	t.Helper()
	const sampleRate = 8000
	const frames = sampleRate / 2
	path := t.TempDir() + "/dual.wav"
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMonoWAVHeader(file, sampleRate, frames*4); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(22, 0); err != nil {
		t.Fatal(err)
	}
	channels := make([]byte, 2)
	binary.LittleEndian.PutUint16(channels, 2)
	if _, err := file.Write(channels); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(28, 0); err != nil {
		t.Fatal(err)
	}
	byteRate := make([]byte, 4)
	binary.LittleEndian.PutUint32(byteRate, sampleRate*4)
	if _, err := file.Write(byteRate); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(32, 0); err != nil {
		t.Fatal(err)
	}
	blockAlign := make([]byte, 2)
	binary.LittleEndian.PutUint16(blockAlign, 4)
	if _, err := file.Write(blockAlign); err != nil {
		t.Fatal(err)
	}
	if _, err := file.Seek(44, 0); err != nil {
		t.Fatal(err)
	}
	frame := make([]byte, 4)
	for i := 0; i < frames; i++ {
		caller := int16(float64(callerAmplitude) * math.Sin(2*math.Pi*440*float64(i)/sampleRate))
		agent := int16(float64(agentAmplitude) * math.Sin(2*math.Pi*660*float64(i)/sampleRate))
		binary.LittleEndian.PutUint16(frame[0:2], uint16(caller))
		binary.LittleEndian.PutUint16(frame[2:4], uint16(agent))
		if _, err := file.Write(frame); err != nil {
			t.Fatal(err)
		}
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestRecordingVariantsPreserveTracksAndBalanceMix(t *testing.T) {
	source := writeTestDualRecording(t, 800, 8000)
	callerPath, err := buildRecordingVariant(source, recordingVariantCaller)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(callerPath)
	agentPath, err := buildRecordingVariant(source, recordingVariantAgent)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(agentPath)
	mixPath, err := buildRecordingVariant(source, recordingVariantMix)
	if err != nil {
		t.Fatal(err)
	}
	defer os.Remove(mixPath)

	for name, path := range map[string]string{"caller": callerPath, "agent": agentPath, "mix": mixPath} {
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		info, err := inspectPCM16WAV(file)
		_ = file.Close()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if info.Channels != 1 || info.SampleRate != 8000 || info.DataSize == 0 {
			t.Fatalf("%s metadata: %+v", name, info)
		}
	}

	mix, err := os.Open(mixPath)
	if err != nil {
		t.Fatal(err)
	}
	defer mix.Close()
	info, err := inspectPCM16WAV(mix)
	if err != nil {
		t.Fatal(err)
	}
	var correlation440, correlation660 float64
	index := 0
	if err := forEachWAVFrame(mix, info, func(samples []int16) error {
		value := float64(samples[0])
		correlation440 += value * math.Sin(2*math.Pi*440*float64(index)/8000)
		correlation660 += value * math.Sin(2*math.Pi*660*float64(index)/8000)
		index++
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	ratio := math.Abs(correlation440 / correlation660)
	if ratio < 0.7 || ratio > 1.3 {
		t.Fatalf("mixed tracks are not balanced: 440/660 correlation ratio %.3f", ratio)
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

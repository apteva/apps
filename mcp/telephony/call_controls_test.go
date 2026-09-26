package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

func callControlFixture(t *testing.T) (*App, *answerPlatform, string) {
	t.Helper()
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"pause_call_recording":  json.RawMessage(`{"data":{"result":"ok"}}`),
		"resume_call_recording": json.RawMessage(`{"data":{"result":"ok"}}`),
	}}
	app, _ := withTelephonyTestContext(t, platform)
	phoneTestPolicy(t, app)
	row := phoneTestCall(t, app, "control-call", "in-progress")
	if _, err := app.db().db.Exec(`UPDATE calls SET carrier_slug='telnyx',carrier_sid='v3:control-call',
		carrier_connection_id=17,recording_mode='always' WHERE id=?`, row.ID); err != nil {
		t.Fatal(err)
	}
	alice := phoneTestIdentity("alice")
	if w := phoneTestRequest(app, nil, "POST", "/access/assign/"+row.ID, alice); w.Code != http.StatusOK {
		t.Fatalf("assign: %d %s", w.Code, w.Body.String())
	}
	return app, platform, row.ID
}

func TestCarrierControlCapabilitiesAreAdapterDriven(t *testing.T) {
	row := callRow{CarrierSlug: "telnyx", CarrierSID: "call-control-id", PeerKind: peerKindHuman,
		RecordingMode: recordingModeAlways, HoldMusicURL: "https://media.example.test/hold.mp3"}
	if got := callControlCapabilities(row); !got["hold_music"] || !got["recording_pause"] {
		t.Fatalf("Telnyx capabilities: %#v", got)
	}
	row.CarrierSlug = "bandwidth"
	if got := callControlCapabilities(row); got["hold_music"] || !got["recording_pause"] {
		t.Fatalf("Bandwidth capabilities: %#v", got)
	}
	for _, slug := range []string{"twilio", "signalwire", "plivo", "vonage", "sinch", "didww", "unknown"} {
		row.CarrierSlug = slug
		if got := callControlCapabilities(row); got["hold_music"] || got["recording_pause"] {
			t.Fatalf("unsupported %s controls were advertised: %#v", slug, got)
		}
	}
	row.CarrierSlug = "telnyx"
	row.IngressPath = "sip_direct"
	if got := callControlCapabilities(row); got["hold_music"] || got["recording_pause"] {
		t.Fatalf("direct SIP controls were advertised: %#v", got)
	}
}

func TestStorageHoldMusicIsValidatedAndSignedForEachHold(t *testing.T) {
	app, platform, id := callControlFixture(t)
	alice := phoneTestIdentity("alice")
	file := map[string]any{"found": true, "file": map[string]any{
		"id": 7, "project_id": "project-a", "content_type": "audio/mpeg", "size_bytes": 1234}}
	platform.appResponses = map[string]any{"storage/files_get": file,
		"storage/files_get_url": map[string]any{"file_id": 7, "url": "https://storage.example.test/audio?sig=secret", "expires_at": time.Now().Add(24 * time.Hour).Unix()}}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_storage_file_id": 7}); w.Code != http.StatusOK {
		t.Fatalf("configure Storage: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusAccepted {
		t.Fatalf("hold with Storage: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Input["audio_url"] != "https://storage.example.test/audio?sig=secret" {
		t.Fatalf("carrier did not receive signed URL: %#v", platform.integrationCalls)
	}
	if len(platform.appCalls) != 4 || platform.appCalls[3].Input["delivery"] != "proxy" || platform.appCalls[3].Input["_project_id"] != "project-a" {
		t.Fatalf("Storage scope or signing: %#v", platform.appCalls)
	}
	if w := phoneTestRequest(app, nil, "GET", "/call-control-settings", nil); strings.Contains(w.Body.String(), "sig=secret") {
		t.Fatal("signed URL leaked into settings")
	}
}

func TestStorageHoldMusicFailsClosed(t *testing.T) {
	app, platform, _ := callControlFixture(t)
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_storage_file_id": 7}); w.Code != http.StatusBadRequest {
		t.Fatalf("unbound Storage: %d %s", w.Code, w.Body.String())
	}
	platform.appResponses = map[string]any{"storage/files_get": map[string]any{"found": true,
		"file": map[string]any{"id": 7, "project_id": "project-b", "content_type": "audio/mpeg", "size_bytes": 1234}}}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_storage_file_id": 7}); w.Code != http.StatusBadRequest {
		t.Fatalf("cross-project Storage: %d %s", w.Code, w.Body.String())
	}
	platform.appResponses["storage/files_get"] = map[string]any{"found": true,
		"file": map[string]any{"id": 7, "project_id": "project-a", "content_type": "text/plain", "size_bytes": 1234}}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_storage_file_id": 7}); w.Code != http.StatusBadRequest {
		t.Fatalf("non-audio Storage: %d %s", w.Code, w.Body.String())
	}
	platform.appResponses["storage/files_get"] = map[string]any{"found": true,
		"file": map[string]any{"id": 7, "project_id": "project-a", "content_type": "audio/wav", "size_bytes": 1234}}
	platform.appResponses["storage/files_get_url"] = map[string]any{"file_id": 7, "url": "/relative/audio", "expires_at": time.Now().Add(24 * time.Hour).Unix()}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_storage_file_id": 7}); w.Code != http.StatusBadRequest {
		t.Fatalf("relative Storage URL: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatal("invalid Storage music reached carrier")
	}
}

func TestHoldRetryCommandSurvivesRecordingAction(t *testing.T) {
	app, platform, id := callControlFixture(t)
	alice := phoneTestIdentity("alice")
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_url": "https://media.example.com/hold.mp3"}); w.Code != http.StatusOK {
		t.Fatal(w.Body.String())
	}
	platform.failTool = "play_audio"
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusBadGateway {
		t.Fatalf("first hold: %d %s", w.Code, w.Body.String())
	}
	first := platform.integrationCalls[0].Input["command_id"]
	platform.failTool = ""
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/pause-recording", nil); w.Code != http.StatusOK {
		t.Fatalf("pause: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusAccepted {
		t.Fatalf("retry hold: %d %s", w.Code, w.Body.String())
	}
	if got := platform.integrationCalls[2].Input["command_id"]; got != first {
		t.Fatalf("retry command changed: %v != %v", got, first)
	}
}

func TestCallControlsRequireOwnerAndMusic(t *testing.T) {
	app, platform, id := callControlFixture(t)
	alice, eve := phoneTestIdentity("alice"), phoneTestIdentity("eve")
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured hold: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, &eve, "POST", "/calls/"+id+"/pause-recording", nil); w.Code != http.StatusNotFound {
		t.Fatalf("other user: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, nil, "POST", "/calls/"+id+"/pause-recording", nil); w.Code != http.StatusNotFound {
		t.Fatalf("non-owner panel request: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 0 {
		t.Fatal("unauthorized control reached carrier")
	}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_url": "http://example.com/music.mp3"}); w.Code != http.StatusBadRequest {
		t.Fatalf("insecure music: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, nil, "POST", "/call-control-settings", map[string]any{"hold_music_url": "https://media.example.com/approved.mp3"}); w.Code != http.StatusOK {
		t.Fatalf("configure music: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusAccepted {
		t.Fatalf("hold: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "play_audio" ||
		platform.integrationCalls[0].Input["target_legs"] != "self" {
		t.Fatalf("hold command: %#v", platform.integrationCalls)
	}
	row, err := app.db().findCall(id)
	if err != nil {
		t.Fatal(err)
	}
	started, _ := json.Marshal(map[string]any{"data": map[string]any{"event_type": "call.playback.started", "payload": map[string]any{"call_control_id": row.CarrierSID, "client_state": row.HoldClientState}}})
	if handled, err := app.handleTelnyxRecordingEvent(row, started); err != nil || !handled {
		t.Fatalf("playback started: %v", err)
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusOK {
		t.Fatalf("repeat hold: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 1 {
		t.Fatal("repeat hold sent another carrier command")
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/resume", nil); w.Code != http.StatusAccepted {
		t.Fatalf("resume: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "stop_audio" {
		t.Fatalf("resume command: %#v", platform.integrationCalls)
	}
	ended, _ := json.Marshal(map[string]any{"data": map[string]any{"event_type": "call.playback.ended", "payload": map[string]any{"call_control_id": row.CarrierSID, "client_state": row.HoldClientState}}})
	if handled, err := app.handleTelnyxRecordingEvent(row, ended); err != nil || !handled {
		t.Fatalf("playback ended: %v", err)
	}
	row, err = app.db().findCall(id)
	if err != nil || row.HoldState != "active" {
		t.Fatalf("resume state: %#v %v", row, err)
	}
}

func TestRecordingPauseRequiresProviderSuccess(t *testing.T) {
	app, platform, id := callControlFixture(t)
	alice := phoneTestIdentity("alice")
	w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/pause-recording", nil)
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"recording_state":"paused"`) {
		t.Fatalf("pause acknowledgement: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 1 || platform.integrationCalls[0].Tool != "pause_call_recording" {
		t.Fatalf("source pause command: %#v", platform.integrationCalls)
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/pause-recording", nil); w.Code != http.StatusOK || len(platform.integrationCalls) != 1 {
		t.Fatalf("repeat pause: %d %s", w.Code, w.Body.String())
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/resume-recording", nil); w.Code != http.StatusOK {
		t.Fatalf("resume recording: %d %s", w.Code, w.Body.String())
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[1].Tool != "resume_call_recording" {
		t.Fatalf("source resume: %#v", platform.integrationCalls)
	}
	if err := app.db().updateStatus(id, "completed", ""); err != nil {
		t.Fatal(err)
	}
	row, err := app.db().findCall(id)
	if err != nil || row.HoldState != "ended" || effectiveRecordingControlState(*row) != "ended" {
		t.Fatalf("terminal controls: %#v %v", row, err)
	}
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/hold", nil); w.Code != http.StatusGone {
		t.Fatalf("terminal action: %d %s", w.Code, w.Body.String())
	}
}

func TestCallControlCarrierFailureIsNotSuccess(t *testing.T) {
	app, platform, id := callControlFixture(t)
	platform.failTool = "pause_call_recording"
	alice := phoneTestIdentity("alice")
	if w := phoneTestRequest(app, &alice, "POST", "/calls/"+id+"/pause-recording", nil); w.Code != http.StatusBadGateway {
		t.Fatalf("carrier failure: %d %s", w.Code, w.Body.String())
	}
	row, err := app.db().findCall(id)
	if err != nil || effectiveRecordingControlState(*row) != "unknown" {
		t.Fatalf("failure state: %#v %v", row, err)
	}
	if len(platform.integrationCalls) != 1 {
		t.Fatal("carrier command missing")
	}
}

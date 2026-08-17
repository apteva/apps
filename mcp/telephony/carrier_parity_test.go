package main

import (
	"context"
	"crypto/ed25519"
	"crypto/hmac"
	"crypto/rand"
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
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func withTelephonyTestContext(t *testing.T, platform *answerPlatform) (*App, *sdk.AppCtx) {
	t.Helper()
	t.Setenv("APTEVA_PUBLIC_URL", "https://example.test")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previous := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previous })
	return &App{installID: 42}, ctx
}

func TestPlivoRouteConfigurationAndRestore(t *testing.T) {
	platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
		"list_owned_phone_numbers": json.RawMessage(`{"objects":[{"number":"14155550101","application":"/v1/Account/test/Application/old-app/"}]}`),
		"create_application":       json.RawMessage(`{"app_id":"new-app"}`),
	}}
	a, ctx := withTelephonyTestContext(t, platform)
	route := routeRow{
		ID: "route-plivo", ProjectID: "project-a", CarrierSlug: "plivo", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, Secret: "route-secret",
	}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, _ := a.db().findRoute(route.ID)
	if err := a.configurePlivoRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	stored, _ = a.db().findRoute(route.ID)
	var config plivoRouteConfig
	if json.Unmarshal([]byte(stored.PreviousVoiceURL), &config) != nil || config.ApplicationID != "new-app" || config.PreviousApplicationID != "old-app" {
		t.Fatalf("unexpected saved Plivo route config: %+v", config)
	}
	if len(platform.integrationCalls) != 3 || platform.integrationCalls[1].Tool != "create_application" || platform.integrationCalls[2].Tool != "update_owned_phone_number" {
		t.Fatalf("unexpected Plivo configuration calls: %#v", platform.integrationCalls)
	}
	createInput := platform.integrationCalls[1].Input
	for _, field := range []string{"answer_url", "hangup_url"} {
		value, _ := createInput[field].(string)
		if !strings.Contains(value, "#ct=2000") || !strings.Contains(value, "rc=2") || !strings.Contains(value, "er=nearest") {
			t.Fatalf("Plivo %s lacks callback resilience policy: %q", field, value)
		}
	}
	platform.integrationCalls = nil
	if err := a.disablePlivoRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 2 || platform.integrationCalls[0].Input["app_id"] != "old-app" || platform.integrationCalls[1].Tool != "delete_application" {
		t.Fatalf("Plivo route was not restored safely: %#v", platform.integrationCalls)
	}
}

func TestTelnyxRouteRequiresSignedWebhooksAndSetsTimeout(t *testing.T) {
	withoutKey := &answerPlatform{credentials: &sdk.ConnectionCredentials{
		Slug: "telnyx", Fields: map[string]string{},
	}}
	a, ctx := withTelephonyTestContext(t, withoutKey)
	route := routeRow{
		ID: "route-telnyx-unsigned", ProjectID: "project-a", CarrierSlug: "telnyx", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, Secret: "route-secret",
	}
	if err := a.configureTelnyxRoute(ctx, &route); err == nil || !strings.Contains(err.Error(), "public key") {
		t.Fatalf("unsigned Telnyx route error=%v", err)
	}
	if len(withoutKey.integrationCalls) != 0 {
		t.Fatalf("carrier was modified before credential validation: %#v", withoutKey.integrationCalls)
	}

	publicKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	platform := &answerPlatform{
		credentials: &sdk.ConnectionCredentials{
			Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(publicKey)},
		},
		integrationResponse: map[string]json.RawMessage{
			"list_phone_numbers":              json.RawMessage(`{"data":[{"id":"number-id","phone_number":"+14155550101","connection_id":"old-app"}]}`),
			"create_call_control_application": json.RawMessage(`{"data":{"id":"new-app"}}`),
		},
	}
	a, ctx = withTelephonyTestContext(t, platform)
	route.ID = "route-telnyx-signed"
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	stored, err := a.db().findRoute(route.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := a.configureTelnyxRoute(ctx, stored); err != nil {
		t.Fatal(err)
	}
	if len(platform.integrationCalls) != 3 {
		t.Fatalf("Telnyx configuration calls=%#v", platform.integrationCalls)
	}
	createInput := platform.integrationCalls[1].Input
	if createInput["webhook_timeout_secs"] != 5 || createInput["webhook_api_version"] != "2" {
		t.Fatalf("Telnyx webhook resilience settings=%#v", createInput)
	}
}

func TestPlivoImmediateInboundSpawnsRealtimeAndRecords(t *testing.T) {
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{
		Slug: "plivo", Fields: map[string]string{"password": "plivo-auth-token", "phone_number": "+14155550101"},
	}}
	a, _ := withTelephonyTestContext(t, platform)
	route := routeRow{
		ID: "route-plivo-inbound", ProjectID: "project-a", CarrierSlug: "plivo", CarrierConnectionID: 9,
		PhoneNumber: "+14155550101", AgentID: 7, Enabled: true, Secret: "route-secret", TimeoutSec: 60,
		AnswerMode: answerModeRealtimeImmediate, AutoDirective: "Answer professionally.", AutoGreeting: "Say hello.",
		RecordingMode: recordingModeAlways,
	}
	if err := a.db().insertRoute(route); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"CallUUID": {"plivo-call-1"}, "From": {"+14155550100"}, "To": {route.PhoneNumber},
		"Direction": {"inbound"}, "CallStatus": {"in-progress"},
	}
	path := "/inbound/plivo/" + route.ID + "?project_id=project-a&secret=route-secret"
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signPlivoTestRequest(a, req, form, "plivo-auth-token")
	response := httptest.NewRecorder()
	a.handlePlivoInbound(response, req)
	if response.Code != http.StatusOK {
		t.Fatalf("inbound response=%d body=%s", response.Code, response.Body.String())
	}
	if !strings.Contains(response.Body.String(), `<Stream bidirectional="true"`) || !strings.Contains(response.Body.String(), `<Record recordSession="true"`) {
		t.Fatalf("inbound XML lacks realtime stream or recording: %s", response.Body.String())
	}
	if len(platform.spawned) != 1 ||
		platform.spawned[0].Directive != route.AutoDirective ||
		platform.spawned[0].CapabilityMode != sdk.RealtimeCapabilitiesInheritAgent ||
		platform.spawned[0].CallContext == nil ||
		platform.spawned[0].CallContext.Direction != "inbound" ||
		platform.spawned[0].CallContext.FromNumber != "+14155550100" ||
		platform.spawned[0].TurnDetection == nil ||
		platform.spawned[0].TurnDetection.Profile != "telephony" {
		t.Fatalf("realtime thread was not spawned from route config: %#v", platform.spawned)
	}
	call, err := a.db().findInboundCallByCarrierSID(route.ID, route.CarrierConnectionID, "plivo-call-1")
	if err != nil || call == nil || call.Status != "answered" || call.AudioBridgeURL == "pending" {
		t.Fatalf("inbound call was not prepared: call=%+v err=%v", call, err)
	}
}

func TestPlivoRecordingCallbackPersistsProviderRecording(t *testing.T) {
	platform := &answerPlatform{credentials: &sdk.ConnectionCredentials{Slug: "plivo", Fields: map[string]string{"password": "plivo-auth-token"}}}
	a, _ := withTelephonyTestContext(t, platform)
	call := testCall("plivo-recording", "completed")
	call.CarrierSlug = "plivo"
	call.CarrierSID = "plivo-call-1"
	call.RecordingMode = recordingModeAlways
	call.RecordingChannels = "dual"
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	form := url.Values{
		"CallUUID": {call.CarrierSID}, "RecordingID": {"plivo-recording-1"}, "RecordingDurationMs": {"1250"},
	}
	path := "/webhook/recording/plivo/" + call.ID + "?project_id=project-a&token=" + call.CallbackSecret
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	signPlivoTestRequest(a, req, form, "plivo-auth-token")
	response := httptest.NewRecorder()
	a.handlePlivoRecordingStatus(response, req)
	if response.Code != http.StatusNoContent {
		t.Fatalf("recording callback=%d body=%s", response.Code, response.Body.String())
	}
	rows, err := a.db().listRecordings(call.ProjectID, call.ID, 10)
	if err != nil || len(rows) != 1 || rows[0].Provider != "plivo" || rows[0].Channels != 2 || rows[0].DurationMS != 1250 {
		t.Fatalf("recording was not normalized: rows=%+v err=%v", rows, err)
	}
}

func TestTelnyxRecordingSavedEventPersistsProviderRecording(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	call := testCall("telnyx-recording", "completed")
	call.CarrierSlug = "telnyx"
	call.CarrierSID = "telnyx-call-1"
	call.RecordingChannels = "dual"
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}
	body := []byte(`{"data":{"event_type":"call.recording.saved","payload":{"call_control_id":"telnyx-call-1","recording_id":"telnyx-recording-1","channels":"dual","format":"wav","recording_started_at":"2026-07-18T10:00:00Z","recording_ended_at":"2026-07-18T10:00:02.5Z"}}}`)
	handled, err := a.handleTelnyxRecordingEvent(&call, body)
	if err != nil || !handled {
		t.Fatalf("Telnyx recording event failed: handled=%v err=%v", handled, err)
	}
	rows, err := a.db().listRecordings(call.ProjectID, call.ID, 10)
	if err != nil || len(rows) != 1 || rows[0].DurationMS != 2500 || rows[0].Channels != 2 {
		t.Fatalf("Telnyx recording was not normalized: rows=%+v err=%v", rows, err)
	}
}

func TestPlivoSignatureV3KnownVector(t *testing.T) {
	const (
		fullURL  = "https://example.test/apps/42/inbound/plivo/route?project_id=project-a&secret=route-secret"
		nonce    = "1700000000"
		token    = "plivo-auth-token"
		expected = "VNI9pTaNCXtgDSmW4nDwiEAG6hF+qZSet1hb6UZs6LM="
	)
	params := map[string]string{"CallUUID": "plivo-call-1", "From": "+14155550100"}
	if !verifyPlivoSignatureV3(fullURL, http.MethodPost, nonce, expected, token, params) {
		t.Fatal("valid Plivo V3 signature was rejected")
	}
	params["From"] = "+14155550999"
	if verifyPlivoSignatureV3(fullURL, http.MethodPost, nonce, expected, token, params) {
		t.Fatal("tampered Plivo callback was accepted")
	}
}

func TestProviderCarrierPlacementUsesParityCallbacksAndRecording(t *testing.T) {
	t.Run("telnyx", func(t *testing.T) {
		platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
			"make_call": json.RawMessage(`{"data":{"call_control_id":"telnyx-call-1"}}`),
		}}
		a, ctx := withTelephonyTestContext(t, platform)
		carrier := &telnyxCarrier{app: a, connID: 9, fields: map[string]string{"connection_id": "connection-1"}}
		result, err := carrier.Place(ctx, carrierPlaceRequest{
			CallID: "call-1", CallbackSecret: "secret-1", ProjectID: "project-a", To: "+14155550100", From: "+14155550101",
			TimeoutSec: 30, MaxDurationSec: 600, RecordingMode: recordingModeAlways, RecordingChannels: "dual",
		})
		if err != nil || result.CarrierSID != "telnyx-call-1" {
			t.Fatalf("place Telnyx call: result=%+v err=%v", result, err)
		}
		input := platform.integrationCalls[0].Input
		if input["record"] != "record-from-answer" || input["record_channels"] != "dual" || input["webhook_url"] == "" || input["command_id"] == "" {
			t.Fatalf("Telnyx call lacks recording or status callback parity: %#v", input)
		}
		if err := carrier.Hangup(ctx, &callRow{ID: "call-1", CarrierSID: result.CarrierSID}); err != nil {
			t.Fatal(err)
		}
		if got := platform.integrationCalls[1].Tool; got != "hangup_call" {
			t.Fatalf("Telnyx hangup used %q", got)
		}
		if hangupID := platform.integrationCalls[1].Input["command_id"]; hangupID == "" || hangupID == input["command_id"] {
			t.Fatalf("Telnyx hangup command id is missing or reused: place=%#v hangup=%#v", input["command_id"], hangupID)
		}
	})

	t.Run("plivo", func(t *testing.T) {
		platform := &answerPlatform{integrationResponse: map[string]json.RawMessage{
			"make_call": json.RawMessage(`{"request_uuid":"plivo-request-1"}`),
		}}
		a, ctx := withTelephonyTestContext(t, platform)
		carrier := &plivoCarrier{app: a, connID: 9}
		result, err := carrier.Place(ctx, carrierPlaceRequest{
			CallID: "call-1", CallbackSecret: "secret-1", ProjectID: "project-a", To: "+14155550100", From: "+14155550101",
			TimeoutSec: 30, MaxDurationSec: 600, RecordingMode: recordingModeAlways, RecordingChannels: "dual",
		})
		if err != nil || result.CarrierRequestID != "plivo-request-1" {
			t.Fatalf("place Plivo call: result=%+v err=%v", result, err)
		}
		input := platform.integrationCalls[0].Input
		ringURL, _ := input["ring_url"].(string)
		if ringURL == "" || input["ring_url"] != input["hangup_url"] || input["ring_method"] != "POST" || !strings.Contains(ringURL, "rc=2") {
			t.Fatalf("Plivo call lacks ring-time CallUUID callback: %#v", input)
		}
	})
}

func TestProviderInboundAnswerAndRejectCommands(t *testing.T) {
	for _, provider := range []string{"telnyx", "plivo"} {
		t.Run(provider, func(t *testing.T) {
			platform := &answerPlatform{}
			a, ctx := withTelephonyTestContext(t, platform)
			row := testCall(provider+"-answer", "answering")
			row.CarrierSlug = provider
			row.CarrierSID = provider + "-call-1"
			row.RecordingMode = recordingModeAlways
			row.RecordingChannels = "dual"
			if err := a.answerInboundCarrierCall(ctx, &row); err != nil {
				t.Fatal(err)
			}
			answer := platform.integrationCalls[0]
			if provider == "telnyx" {
				if answer.Tool != "answer_call" || answer.Input["record"] != "record-from-answer" || answer.Input["stream_url"] == "" || answer.Input["command_id"] == "" {
					t.Fatalf("Telnyx answer lacks realtime or recording controls: %#v", answer)
				}
			} else {
				alegURL, _ := answer.Input["aleg_url"].(string)
				if answer.Tool != "update_call" || !strings.Contains(alegURL, "rc=2") {
					t.Fatalf("Plivo answer does not redirect to resilient realtime XML: %#v", answer)
				}
			}
			if err := a.rejectInboundCarrierCall(ctx, &row); err != nil {
				t.Fatal(err)
			}
			if got := platform.integrationCalls[1].Tool; got != map[string]string{"telnyx": "reject_call", "plivo": "hangup_call"}[provider] {
				t.Fatalf("%s reject used %q", provider, got)
			}
			if provider == "telnyx" && platform.integrationCalls[1].Input["command_id"] == answer.Input["command_id"] {
				t.Fatalf("Telnyx answer and reject reused command id: %#v", platform.integrationCalls)
			}
		})
	}
}

func TestTelnyxTerminalStatusMapping(t *testing.T) {
	for cause, want := range map[string]string{
		"normal_clearing": "completed", "user_busy": "busy", "no_answer": "no-answer",
		"originator_cancel": "canceled", "call_rejected": "failed",
	} {
		if got := telnyxHangupStatus(cause); got != want {
			t.Fatalf("cause %q mapped to %q, want %q", cause, got, want)
		}
	}
}

func TestJSONCarrierMediaBridgesArePacedAndInterruptible(t *testing.T) {
	for _, tc := range []struct {
		provider, codec string
		sampleRate      int
		rawPacketBytes  int
	}{
		{provider: "signalwire", codec: carrierCodecL16_24, sampleRate: 24000, rawPacketBytes: 960},
		{provider: "telnyx", codec: carrierCodecPCMU8, sampleRate: 8000, rawPacketBytes: 160},
		{provider: "plivo", codec: carrierCodecPCMU8, sampleRate: 8000, rawPacketBytes: 160},
	} {
		t.Run(tc.provider, func(t *testing.T) {
			t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
			a, _ := withTelephonyTestContext(t, &answerPlatform{})
			coreBridge, coreServer := newFakeCoreAudioBridge(t)
			call := testCall(tc.provider+"-media", "in-progress")
			call.CarrierSlug = tc.provider
			call.CarrierSID = tc.provider + "-call"
			call.AudioBridgeURL = "ws" + strings.TrimPrefix(coreServer.URL, "http")
			if err := a.db().insertCall(call); err != nil {
				t.Fatal(err)
			}
			done := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(done)
				if tc.provider == "signalwire" {
					a.handleSignalWireMediaStream(w, r)
				} else if tc.provider == "telnyx" {
					a.handleTelnyxMediaStream(w, r)
				} else {
					a.handlePlivoMediaStream(w, r)
				}
			}))
			defer server.Close()
			path := "/media/" + tc.provider + "/" + call.ID + "/" + call.CallbackSecret
			carrier, _, _, err := ws.DefaultDialer.Dial(context.Background(), "ws"+strings.TrimPrefix(server.URL, "http")+path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				_ = carrier.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
				}
			}()
			start := map[string]any{"event": "start", "stream_id": "stream-1", "streamId": "stream-1"}
			if tc.provider == "telnyx" {
				start["start"] = map[string]string{"call_control_id": call.CarrierSID, "stream_id": "stream-1"}
			} else {
				start["start"] = map[string]string{"callUuid": call.CarrierSID, "streamId": "stream-1"}
			}
			encodedStart, _ := json.Marshal(start)
			if err := wsutil.WriteClientText(carrier, encodedStart); err != nil {
				t.Fatal(err)
			}
			core := waitTestConnection(t, coreBridge.conn)

			caller := sinePCM(tc.sampleRate, 440, tc.sampleRate/50)
			var raw []byte
			if tc.codec == carrierCodecPCMU8 {
				raw = pcm16ToUlaw(caller)
			} else {
				raw = pcm16ToBytes(caller)
			}
			media, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(raw)}})
			if err := wsutil.WriteClientText(carrier, media); err != nil {
				t.Fatal(err)
			}
			select {
			case inbound := <-coreBridge.inbound:
				if len(inbound) < 800 || rmsPCM(bytesToPCM16(inbound)) < 3000 {
					t.Fatalf("caller audio was damaged before Core: bytes=%d rms=%f", len(inbound), rmsPCM(bytesToPCM16(inbound)))
				}
			case <-time.After(time.Second):
				t.Fatal("caller audio did not reach Core")
			}

			agent := sinePCM(24000, 800, 24000)
			for offset := 0; offset < len(agent); offset += 960 {
				end := min(len(agent), offset+960)
				metadata, _ := json.Marshal(realtimeBridgeControl{Type: "audio.frame", ItemID: "item-1", AudioEndMS: end * 1000 / 24000})
				_ = wsutil.WriteServerText(core, metadata)
				_ = wsutil.WriteServerBinary(core, pcm16ToBytes(agent[offset:end]))
			}
			mediaCount := 0
			var mediaTimes []time.Time
			deadline := time.Now().Add(2 * time.Second)
			for mediaCount < 15 && time.Now().Before(deadline) {
				_ = carrier.SetReadDeadline(time.Now().Add(250 * time.Millisecond))
				data, op, err := wsutil.ReadServerData(carrier)
				if err != nil || op != ws.OpText {
					continue
				}
				var frame struct {
					Event string `json:"event"`
					Media struct {
						Payload string `json:"payload"`
					} `json:"media"`
				}
				if json.Unmarshal(data, &frame) != nil {
					continue
				}
				if frame.Event == "mark" {
					_ = wsutil.WriteClientText(carrier, data)
					continue
				}
				if frame.Event != "media" && frame.Event != "playAudio" {
					continue
				}
				decoded, err := base64.StdEncoding.DecodeString(frame.Media.Payload)
				if err != nil || len(decoded) != tc.rawPacketBytes {
					t.Fatalf("carrier packet is not 20ms: bytes=%d err=%v", len(decoded), err)
				}
				mediaCount++
				mediaTimes = append(mediaTimes, time.Now())
			}
			if mediaCount < 15 {
				t.Fatalf("carrier playback stalled after %d packets", mediaCount)
			}
			if mediaTimes[9].Sub(mediaTimes[0]) > 250*time.Millisecond {
				t.Fatalf("carrier lead was not filled promptly: %v", mediaTimes[9].Sub(mediaTimes[0]))
			}
			maxGap := time.Duration(0)
			for i := 11; i < len(mediaTimes); i++ {
				maxGap = max(maxGap, mediaTimes[i].Sub(mediaTimes[i-1]))
			}
			steadyIntervals := len(mediaTimes) - 11
			steadyElapsed := mediaTimes[len(mediaTimes)-1].Sub(mediaTimes[10])
			if maxGap > 150*time.Millisecond || steadyElapsed > time.Duration(steadyIntervals)*20*time.Millisecond+150*time.Millisecond {
				t.Fatalf("carrier playback cadence underrun: max_gap=%v elapsed=%v intervals=%d", maxGap, steadyElapsed, steadyIntervals)
			}
			progressObserved := false
			for until := time.Now().Add(time.Second); !progressObserved && time.Now().Before(until); {
				select {
				case control := <-coreBridge.controls:
					progressObserved = control.Type == "playback.progress" && control.ItemID == "item-1" && control.AudioEndMS > 0
				case <-time.After(20 * time.Millisecond):
				}
			}
			if !progressObserved {
				t.Fatal("carrier playback progress did not reach Core")
			}

			callerSpeech := telephoneSpeech(tc.sampleRate, 500, 3500)
			frameSamples := tc.sampleRate / 50
			for offset := 0; offset < len(callerSpeech); offset += frameSamples {
				frame := callerSpeech[offset:min(len(callerSpeech), offset+frameSamples)]
				if tc.codec == carrierCodecPCMU8 {
					raw = pcm16ToUlaw(frame)
				} else {
					raw = pcm16ToBytes(frame)
				}
				speechFrame, _ := json.Marshal(map[string]any{"event": "media", "media": map[string]string{"payload": base64.StdEncoding.EncodeToString(raw)}})
				_ = wsutil.WriteClientText(carrier, speechFrame)
			}
			speechObserved := false
			for until := time.Now().Add(time.Second); !speechObserved && time.Now().Before(until); {
				select {
				case control := <-coreBridge.controls:
					speechObserved = control.Type == "input.speech_started"
				case <-time.After(20 * time.Millisecond):
				}
			}
			if !speechObserved {
				t.Fatal("local caller speech-start did not reach Core during playback")
			}

			interrupt, _ := json.Marshal(realtimeBridgeControl{Type: "interrupt", ItemID: "item-1"})
			if err := wsutil.WriteServerText(core, interrupt); err != nil {
				t.Fatal(err)
			}
			cleared := false
			for until := time.Now().Add(time.Second); !cleared && time.Now().Before(until); {
				_ = carrier.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
				data, op, err := wsutil.ReadServerData(carrier)
				if err == nil && op == ws.OpText {
					var event struct {
						Event string `json:"event"`
					}
					if json.Unmarshal(data, &event) == nil && (event.Event == "clear" || event.Event == "clearAudio") {
						cleared = true
					}
				}
			}
			if !cleared {
				t.Fatal("interruption did not clear carrier playback")
			}
			stop, _ := json.Marshal(map[string]string{"event": "stop"})
			_ = wsutil.WriteClientText(carrier, stop)
			_ = carrier.Close()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("carrier bridge did not stop")
			}
		})
	}
}

func signPlivoTestRequest(a *App, req *http.Request, form url.Values, token string) {
	params := make(map[string]string, len(form))
	for key := range form {
		params[key] = form.Get(key)
	}
	nonce := "test-nonce"
	req.Header.Set("X-Plivo-Signature-V3-Nonce", nonce)
	req.Header.Set("X-Plivo-Signature-V3", plivoTestSignature(a.publicRequestURL(req), req.Method, nonce, token, params))
}

func plivoTestSignature(fullURL, method, nonce, token string, params map[string]string) string {
	u, _ := url.Parse(fullURL)
	canonical := u.Scheme + "://" + u.Host + u.Path
	query := map[string]string{}
	for key := range u.Query() {
		query[key] = u.Query().Get(key)
	}
	if len(query) > 0 || len(params) > 0 {
		canonical += "?"
	}
	if len(query) > 0 {
		canonical += plivoSortedParams(query, true)
		if strings.EqualFold(method, http.MethodPost) && len(params) > 0 {
			canonical += "."
		}
	}
	if strings.EqualFold(method, http.MethodPost) && len(params) > 0 {
		canonical += plivoSortedParams(params, false)
	}
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte(canonical + "." + nonce))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

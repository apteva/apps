package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

type fakeCoreAudioBridge struct {
	conn     chan net.Conn
	inbound  chan []byte
	controls chan realtimeBridgeControl
	closed   chan wsutil.ClosedError
}

func newFakeCoreAudioBridge(t *testing.T) (*fakeCoreAudioBridge, *httptest.Server) {
	t.Helper()
	bridge := &fakeCoreAudioBridge{
		conn: make(chan net.Conn, 1), inbound: make(chan []byte, 64), controls: make(chan realtimeBridgeControl, 16),
		closed: make(chan wsutil.ClosedError, 1),
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, _, _, err := ws.UpgradeHTTP(r, w)
		if err != nil {
			return
		}
		bridge.conn <- conn
		defer conn.Close()
		for {
			data, op, err := wsutil.ReadClientData(conn)
			if err != nil {
				var closed wsutil.ClosedError
				if errors.As(err, &closed) {
					bridge.closed <- closed
				}
				return
			}
			switch op {
			case ws.OpBinary:
				bridge.inbound <- append([]byte(nil), data...)
			case ws.OpText:
				var control realtimeBridgeControl
				if json.Unmarshal(data, &control) == nil && control.Type != "" {
					bridge.controls <- control
				}
			}
		}
	}))
	t.Cleanup(server.Close)
	return bridge, server
}

func waitTestConnection(t *testing.T, connections <-chan net.Conn) net.Conn {
	t.Helper()
	select {
	case conn := <-connections:
		return conn
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for WebSocket connection")
		return nil
	}
}

func TestTwilioMediaBridgeFullDuplexAudioContinuity(t *testing.T) {
	t.Setenv("APTEVA_PUBLIC_URL", "https://public.example.test")
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
	platform := &answerPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(platform))
	previousCtx := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = previousCtx })

	coreBridge, coreServer := newFakeCoreAudioBridge(t)
	call := testCall("full-duplex", "in-progress")
	call.Direction = "inbound"
	call.AudioBridgeURL = "ws" + strings.TrimPrefix(coreServer.URL, "http")
	a := &App{installID: 42}
	if err := a.db().insertCall(call); err != nil {
		t.Fatal(err)
	}

	handlerDone := make(chan struct{})
	telephonyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(handlerDone)
		a.handleTwilioMediaStream(w, r)
	}))
	t.Cleanup(telephonyServer.Close)
	mediaPath := "/media/twilio/" + call.ID + "/" + call.CallbackSecret
	publicRequest := httptest.NewRequest(http.MethodGet, mediaPath, nil)
	signature := twilioTestSignature(a.publicRequestURL(publicRequest), url.Values{}, "test-auth-token")
	dialer := ws.Dialer{Header: ws.HandshakeHeaderHTTP(http.Header{"X-Twilio-Signature": {signature}})}
	twilio, _, _, err := dialer.Dial(context.Background(), "ws"+strings.TrimPrefix(telephonyServer.URL, "http")+mediaPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = twilio.Close()
		select {
		case <-handlerDone:
		case <-time.After(2 * time.Second):
		}
	}()

	start, _ := json.Marshal(map[string]any{
		"event": "start", "streamSid": "MZ-full-duplex",
		"start": map[string]string{"callSid": call.CarrierSID},
	})
	if err := wsutil.WriteClientText(twilio, start); err != nil {
		t.Fatal(err)
	}
	core := waitTestConnection(t, coreBridge.conn)

	// Caller -> Core: Twilio sends 20 ms μ-law/8 kHz packets. The real bridge
	// must produce one continuous PCM16/24 kHz stream without boundary holes.
	callerPCM := sinePCM(8000, 440, 160*20)
	for offset := 0; offset < len(callerPCM); offset += 160 {
		payload := base64.StdEncoding.EncodeToString(pcm16ToUlaw(callerPCM[offset : offset+160]))
		media, _ := json.Marshal(map[string]any{
			"event": "media", "streamSid": "MZ-full-duplex", "media": map[string]string{"payload": payload},
		})
		if err := wsutil.WriteClientText(twilio, media); err != nil {
			t.Fatal(err)
		}
	}
	var corePCM []int16
	deadline := time.After(time.Second)
	for len(corePCM) < 9400 {
		select {
		case frame := <-coreBridge.inbound:
			corePCM = append(corePCM, bytesToPCM16(frame)...)
		case <-deadline:
			t.Fatalf("caller audio stalled before Core: samples=%d", len(corePCM))
		}
	}
	if rmsPCM(corePCM) < 5000 {
		t.Fatalf("caller audio was attenuated before Core: rms=%f", rmsPCM(corePCM))
	}
	if gap := longestNearZeroRun(corePCM, 8); gap >= 24 {
		t.Fatalf("caller audio gained a %.2fms hole before Core", float64(gap)*1000/24000)
	}

	// Core -> caller: emit audio faster than real time, as the Realtime API
	// does. Telephony must absorb that burst and feed Twilio continuously.
	agentPCM := sinePCM(24000, 1000, 24000)
	for offset := 0; offset < len(agentPCM); offset += 960 {
		end := min(len(agentPCM), offset+960)
		metadata, _ := json.Marshal(realtimeBridgeControl{
			Type: "audio.frame", ResponseID: "response-1", ItemID: "item-1", AudioEndMS: end * 1000 / 24000,
		})
		if err := wsutil.WriteServerText(core, metadata); err != nil {
			t.Fatal(err)
		}
		if err := wsutil.WriteServerBinary(core, pcm16ToBytes(agentPCM[offset:end])); err != nil {
			t.Fatal(err)
		}
	}

	var (
		twilioPCM  []int16
		mediaTimes []time.Time
	)
	deadline = time.After(2 * time.Second)
	for len(twilioPCM) < 7900 {
		if err := twilio.SetReadDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		data, op, err := wsutil.ReadServerData(twilio)
		if err != nil {
			select {
			case <-deadline:
				t.Fatalf("Core audio stalled before Twilio: samples=%d err=%v", len(twilioPCM), err)
			default:
				continue
			}
		}
		if op != ws.OpText {
			continue
		}
		var frame struct {
			Event string `json:"event"`
			Media struct {
				Payload string `json:"payload"`
			} `json:"media"`
			Mark struct {
				Name string `json:"name"`
			} `json:"mark"`
		}
		if json.Unmarshal(data, &frame) != nil {
			continue
		}
		if frame.Event == "mark" {
			if err := wsutil.WriteClientText(twilio, data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if frame.Event != "media" {
			continue
		}
		encoded, err := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if err != nil {
			t.Fatal(err)
		}
		twilioPCM = append(twilioPCM, ulawToPCM16(encoded)...)
		mediaTimes = append(mediaTimes, time.Now())
	}
	if rmsPCM(twilioPCM) < 5000 {
		t.Fatalf("Core audio was attenuated before Twilio: rms=%f", rmsPCM(twilioPCM))
	}
	if gap := longestNearZeroRun(twilioPCM, 8); gap >= 8 {
		t.Fatalf("Core audio gained a %.2fms hole before Twilio", float64(gap)*1000/8000)
	}
	if len(mediaTimes) < 12 {
		t.Fatalf("too few Twilio media packets: %d", len(mediaTimes))
	}
	if initialLead := mediaTimes[9].Sub(mediaTimes[0]); initialLead > 250*time.Millisecond {
		t.Fatalf("Twilio jitter buffer was not filled promptly: %v", initialLead)
	}
	maxGap := time.Duration(0)
	for i := 11; i < len(mediaTimes); i++ {
		maxGap = max(maxGap, mediaTimes[i].Sub(mediaTimes[i-1]))
	}
	steadyIntervals := len(mediaTimes) - 11
	steadyElapsed := mediaTimes[len(mediaTimes)-1].Sub(mediaTimes[10])
	if maxGap > 150*time.Millisecond || steadyElapsed > time.Duration(steadyIntervals)*20*time.Millisecond+150*time.Millisecond {
		t.Fatalf("Twilio playback cadence underrun: max_gap=%v elapsed=%v intervals=%d", maxGap, steadyElapsed, steadyIntervals)
	}

	// Re-arm the local speech detector, queue a longer second response, and
	// verify the complete caller -> Core -> Telephony -> Twilio interruption
	// control loop while outbound audio is still buffered.
	silence := make([]int16, 160)
	for i := 0; i < 25; i++ {
		writeTwilioTestMedia(t, twilio, "MZ-full-duplex", silence)
	}
	longResponse := sinePCM(24000, 700, 24000*3)
	for offset := 0; offset < len(longResponse); offset += 960 {
		end := min(len(longResponse), offset+960)
		metadata, _ := json.Marshal(realtimeBridgeControl{
			Type: "audio.frame", ResponseID: "response-2", ItemID: "item-2", AudioEndMS: end * 1000 / 24000,
		})
		if err := wsutil.WriteServerText(core, metadata); err != nil {
			t.Fatal(err)
		}
		if err := wsutil.WriteServerBinary(core, pcm16ToBytes(longResponse[offset:end])); err != nil {
			t.Fatal(err)
		}
	}
	waitForTwilioTestMedia(t, twilio)
	callerSpeech := telephoneSpeech(8000, 500, 3500)
	for offset := 0; offset < len(callerSpeech); offset += 160 {
		writeTwilioTestMedia(t, twilio, "MZ-full-duplex", callerSpeech[offset:min(len(callerSpeech), offset+160)])
	}
	bargeInDeadline := time.After(time.Second)
	bargeInObserved := false
	for !bargeInObserved {
		select {
		case control := <-coreBridge.controls:
			bargeInObserved = control.Type == "input.speech_started"
		case <-bargeInDeadline:
			t.Fatal("caller barge-in did not reach Core")
		}
	}
	interrupt, _ := json.Marshal(realtimeBridgeControl{Type: "interrupt", ResponseID: "response-2", ItemID: "item-2"})
	if err := wsutil.WriteServerText(core, interrupt); err != nil {
		t.Fatal(err)
	}
	cleared := false
	clearDeadline := time.Now().Add(time.Second)
	for !cleared && time.Now().Before(clearDeadline) {
		_ = twilio.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		data, op, err := wsutil.ReadServerData(twilio)
		if err != nil || op != ws.OpText {
			continue
		}
		var event struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(data, &event) == nil && event.Event == "clear" {
			cleared = true
		}
	}
	if !cleared {
		t.Fatal("Core interruption did not clear Twilio playback")
	}
	stop, _ := json.Marshal(map[string]any{"event": "stop", "streamSid": "MZ-full-duplex"})
	if err := wsutil.WriteClientText(twilio, stop); err != nil {
		t.Fatal(err)
	}
	_ = twilio.Close()
	select {
	case <-handlerDone:
	case <-time.After(time.Second):
		t.Fatal("Telephony media handler did not stop")
	}
	select {
	case closed := <-coreBridge.closed:
		if closed.Code != ws.StatusNormalClosure {
			t.Fatalf("Core close = code %d reason %q", closed.Code, closed.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("Core did not receive a WebSocket close frame")
	}
}

func writeTwilioTestMedia(t *testing.T, conn net.Conn, streamSID string, pcm []int16) {
	t.Helper()
	payload := base64.StdEncoding.EncodeToString(pcm16ToUlaw(pcm))
	media, _ := json.Marshal(map[string]any{
		"event": "media", "streamSid": streamSID, "media": map[string]string{"payload": payload},
	})
	if err := wsutil.WriteClientText(conn, media); err != nil {
		t.Fatal(err)
	}
}

func waitForTwilioTestMedia(t *testing.T, conn net.Conn) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if err := conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
			t.Fatal(err)
		}
		data, op, err := wsutil.ReadServerData(conn)
		if err != nil || op != ws.OpText {
			continue
		}
		var event struct {
			Event string `json:"event"`
		}
		if json.Unmarshal(data, &event) != nil {
			continue
		}
		if event.Event == "mark" {
			if err := wsutil.WriteClientText(conn, data); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if event.Event == "media" {
			return
		}
	}
	t.Fatal("timed out waiting for queued Twilio playback")
}

func longestNearZeroRun(samples []int16, threshold int) int {
	longest, current := 0, 0
	for _, sample := range samples {
		if absInt16(sample) <= threshold {
			current++
			longest = max(longest, current)
		} else {
			current = 0
		}
	}
	return longest
}

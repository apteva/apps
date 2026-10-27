package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestMicrophoneCapabilityPolicyAndValidation(t *testing.T) {
	for _, raw := range []string{`{"version":2}`, `{"version":1,"sample_rate":8000}`, `{"version":1,"audio_format":"pcmu"}`, `{"version":1,"channels":2}`, `{"version":1,"continuous_conversation":"yes"}`, `{"version":1,"local_interruption_required":true,"provider_interruption":true}`, strings.Repeat("x", 4097)} {
		if _, err := parseVoiceAudioCapabilities([]byte(raw)); err == nil {
			t.Fatalf("accepted %s", raw[:min(len(raw), 100)])
		}
	}
	caps, err := parseVoiceAudioCapabilities([]byte(`{"version":1,"audio_format":"pcm16","sample_rate":24000,"channels":1,"continuous_conversation":true,"provider_interruption":true}`))
	if err != nil {
		t.Fatal(err)
	}
	if !continuousVoiceInput("auto", caps, false) || continuousVoiceInput("discontinuous", caps, false) || !continuousVoiceInput("continuous", voiceAudioCapabilities{}, false) {
		t.Fatal("mode override ignored")
	}
	no := false
	caps.ContinuousInputRequired = &no
	if continuousVoiceInput("auto", caps, false) {
		t.Fatal("explicit input requirement lost precedence")
	}
	if continuousVoiceInput("auto", voiceAudioCapabilities{}, false) || !continuousVoiceInput("auto", voiceAudioCapabilities{}, true) {
		t.Fatal("legacy/carrier default changed")
	}
}

func TestMicrophoneSilenceDoesNotBecomeSpeechOrPendingPlayback(t *testing.T) {
	var relay voiceMediaRelay
	activity := newVoiceMediaActivity(time.Now())
	_, before := activity.snapshot()
	for i := 0; i < 2000; i++ {
		frame, started, acks, conditions, ok := relay.nextInputFrame(true)
		if !ok || len(frame) != 960 || !isSilentPCM(frame) || started || len(acks) > 0 || conditions || relay.hasPendingPlayback() {
			t.Fatalf("idle frame %d misclassified", i)
		}
		activity.frameActivity("caller", &relay)
	}
	if active, last := activity.snapshot(); active || !last.Equal(before) {
		t.Fatal("idle transport kept conversation active")
	}
	caller, _, _ := activity.deliverySnapshot()
	if caller.GeneratedBytes != 0 || caller.ForwardedBytes != 0 || caller.PlaybackAcknowledgedBytes != 0 {
		t.Fatalf("idle speech evidence=%+v", caller)
	}
	payload := bytes.Repeat([]byte{0x42, 0x12}, voiceFrameBytes)
	relay.append(voiceAudioChunk{audio: payload, itemID: "speech", audioEndMS: 40})
	var heard []byte
	var receipts []voicePlaybackAck
	for i := 0; i < 2; i++ {
		frame, _, acks, _, _ := relay.nextInputFrame(true)
		heard = append(heard, frame...)
		receipts = append(receipts, acks...)
	}
	if !bytes.Equal(heard, payload) || len(receipts) != 1 {
		t.Fatal("speech did not replace idle silence in order")
	}
	for i := 0; i < voiceSilenceFrames+5; i++ {
		relay.nextInputFrame(true)
	}
	if relay.hasPendingPlayback() {
		t.Fatal("continuous silence kept playback pending")
	}
	if frame, started, acks, _, ok := relay.nextInputFrame(true); !ok || !isSilentPCM(frame) || started || len(acks) > 0 {
		t.Fatal("idle did not resume after tail")
	}
}

func TestMicrophoneScopedInterruptKeepsNewPlayback(t *testing.T) {
	var relay voiceMediaRelay
	for _, chunk := range []voiceAudioChunk{{audio: make([]byte, 960), itemID: "old", generation: 1}, {audio: bytes.Repeat([]byte{0x24}, 960), itemID: "new", generation: 2}} {
		relay.append(chunk)
	}
	if dropped := relay.interruptPlayback("old", 1); dropped != 960 {
		t.Fatalf("discarded=%d", dropped)
	}
	frame, _, acks, _, ok := relay.nextInputFrame(true)
	if !ok || !bytes.Equal(frame, bytes.Repeat([]byte{0x24}, 960)) || len(acks) != 1 || acks[0].generation != 2 {
		t.Fatal("scoped cancellation removed new playback")
	}
}

func TestMicrophoneDirectSilencePacingAndCancellation(t *testing.T) {
	source, producer := voiceTestSocketPair(t)
	destination, receiver := voiceTestSocketPair(t)
	defer producer.Close()
	defer receiver.Close()
	destination.setAudioCapabilities(voiceAudioCapabilities{Version: 1, ContinuousConversation: true, ProviderInterruption: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newVoiceMediaActivity(time.Now())
	exits := make(chan voiceBridgeExit, 2)
	done := make(chan struct{})
	var capture cappedAudio
	go func() {
		defer close(done)
		pumpVoiceAudio(ctx, source, destination, voiceBridgeLegCallerToTarget, voiceBridgeCaller, voiceBridgeTarget, "caller", activity, &capture, nil, nil, exits)
	}()
	for i := 0; i < 6; i++ {
		_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
		kind, data, err := receiver.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if kind != websocket.BinaryMessage || len(data) != 960 || !isSilentPCM(data) {
			t.Fatalf("unexpected idle message kind=%d bytes=%d", kind, len(data))
		}
	}
	if active, _ := activity.snapshot(); active {
		t.Fatal("transport silence is active speech")
	}
	diag := activity.inputSnapshot()[string(voiceBridgeLegCallerToTarget)]
	deadline := time.Now().Add(time.Second)
	for diag.Frames < 6 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
		diag = activity.inputSnapshot()[string(voiceBridgeLegCallerToTarget)]
	}
	if diag.SmallestInputGapMS < 15 {
		t.Fatalf("writer emitted a catch-up burst: %+v", diag)
	}
	if diag.Frames < 6 || diag.SpeechAudioMS != 0 || diag.IdleAudioMS < 120 || diag.FirstFrameAt == nil {
		t.Fatalf("diagnostics=%+v", diag)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled microphone writer leaked")
	}
}

func TestMicrophoneCarrierSends160SamplesBeforeSpeech(t *testing.T) {
	caller, producer := voiceTestSocketPair(t)
	carrier, receiver := voiceTestSocketPair(t)
	defer producer.Close()
	defer receiver.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	activity := newVoiceMediaActivity(time.Now())
	done := make(chan struct{})
	var capture cappedAudio
	go func() {
		defer close(done)
		relayCallerToCarrier(ctx, caller, &carrierMediaSocket{conn: carrier.conn}, "stream", activity, &capture, nil, nil, make(chan voiceBridgeExit, 2))
	}()
	for i := 0; i < 4; i++ {
		_ = receiver.SetReadDeadline(time.Now().Add(time.Second))
		_, raw, err := receiver.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var frame struct {
			Event string `json:"event"`
			Media struct {
				Payload string `json:"payload"`
			} `json:"media"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		pcm, err := base64.StdEncoding.DecodeString(frame.Media.Payload)
		if err != nil || frame.Event != "media" || len(pcm) != 160 {
			t.Fatalf("carrier event=%s bytes=%d err=%v", frame.Event, len(pcm), err)
		}
		for _, b := range pcm {
			if b != 0xff && b != 0x7f {
				t.Fatal("carrier idle was not mu-law silence")
			}
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("carrier writer leaked")
	}
	callerEvidence, _, _ := activity.deliverySnapshot()
	if callerEvidence.GeneratedBytes != 0 || callerEvidence.ForwardedBytes != 0 || callerEvidence.PlaybackAcknowledgedBytes != 0 {
		t.Fatal("carrier idle counted as speech")
	}
}

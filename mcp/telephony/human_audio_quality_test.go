package main

import (
	"encoding/binary"
	"os"
	"strings"
	"testing"
)

func TestLiveHumanPacerPolicyUsesAdaptiveConversationalWindow(t *testing.T) {
	policy := liveHumanCarrierPacerPolicy()
	if policy.maxQueueMS != 180 || policy.adaptiveMaxQueueMS < 180 || policy.trimToMS < 100 || policy.trimToMS > 120 {
		t.Fatalf("unexpected live policy: %+v", policy)
	}
}

func TestDroppedAudioCrossfadeRemovesSharpWaveformSplice(t *testing.T) {
	previous := make([]int16, 80)
	next := make([]int16, 160)
	for i := range previous {
		previous[i] = 10000
	}
	for i := range next {
		next[i] = -10000
	}
	rawJump := int(previous[len(previous)-1]) - int(next[0])
	applyPCMOverlapCrossfade(previous, next, 80)
	smoothedJump := int(previous[len(previous)-1]) - int(next[0])
	if absInt(smoothedJump) >= absInt(rawJump)/4 {
		t.Fatalf("crossfade jump=%d, raw=%d", smoothedJump, rawJump)
	}
}

func TestHumanPacerRecordsExactDirectionalDrop(t *testing.T) {
	policy := liveHumanCarrierPacerPolicy()
	if policy.trimToMS != 110 {
		t.Fatalf("trim target=%dms", policy.trimToMS)
	}
	// The asynchronous pacer behavior is covered in human_audio_pacer_test;
	// this source contract guards the diagnostic fields operators rely on.
	source, err := os.ReadFile("carrier_pacer.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"Timestamp:", `Direction: "operator_to_carrier"`, "QueueBeforeMS:", "QueueAfterMS:"} {
		if !strings.Contains(string(source), required) {
			t.Fatalf("drop telemetry missing %q", required)
		}
	}
}

func TestSoftphoneFramedCapturePreservesSequenceAndPayload(t *testing.T) {
	payload := []byte{1, 2, 3, 4}
	frame := make([]byte, 16+len(payload))
	binary.LittleEndian.PutUint32(frame[:4], softphoneAudioFrameMagic)
	binary.LittleEndian.PutUint32(frame[4:8], 42)
	copy(frame[16:], payload)
	decoded, sequence, framed := decodeSoftphoneAudioFrame(frame)
	if !framed || sequence != 42 || string(decoded) != string(payload) {
		t.Fatalf("decoded framed capture = %v, %d, %v", decoded, sequence, framed)
	}
}

func TestBrowserMediaPathHasHeadroomWorkerAndTimestampedFrames(t *testing.T) {
	worklet, err := os.ReadFile("ui/softphone-worklet.js")
	if err != nil {
		t.Fatal(err)
	}
	worker, err := os.ReadFile("ui/softphone-worker.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"inputGainDB ?? -6", "lookaheadSamples", "ceiling", "timestamp_ms", "sequence", "applyCrossfade"} {
		if !strings.Contains(string(worklet), required) {
			t.Fatalf("worklet missing %q", required)
		}
	}
	for _, required := range []string{"new WebSocket", "framedCapture", "websocket_backpressure", "playbackSequence"} {
		if !strings.Contains(string(worker), required) {
			t.Fatalf("worker missing %q", required)
		}
	}
}

func TestTelnyxMediaProfileTargetsCorrectLegAndKeepsRTPClockAlive(t *testing.T) {
	input := map[string]any{}
	applyTelnyxMediaProfile(input)
	if input["stream_bidirectional_target_legs"] != "self" || input["send_silence_when_idle"] != true {
		t.Fatalf("Telnyx live media controls = %#v", input)
	}
}

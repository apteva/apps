package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

func TestVoiceMediaRelayPacesSpeechAndAddsSilenceTail(t *testing.T) {
	payload := bytes.Repeat([]byte{0x24}, voiceFrameBytes+voiceFrameBytes/2)
	var relay voiceMediaRelay
	relay.append(voiceAudioChunk{audio: payload, itemID: "item-one", audioEndMS: 30})

	first, started, acks, applyConditions, ok := relay.nextFrame()
	if !ok || !started || len(acks) != 0 || !applyConditions {
		t.Fatalf("first frame ok=%v started=%v acks=%v apply_conditions=%v", ok, started, acks, applyConditions)
	}
	if !bytes.Equal(first, payload[:voiceFrameBytes]) {
		t.Fatal("first frame did not preserve source audio")
	}

	second, started, acks, applyConditions, ok := relay.nextFrame()
	if !ok || started || len(acks) != 1 || acks[0].itemID != "item-one" || acks[0].audioEndMS != 30 || !applyConditions {
		t.Fatalf("second frame ok=%v started=%v acks=%v apply_conditions=%v", ok, started, acks, applyConditions)
	}
	if !bytes.Equal(second[:voiceFrameBytes/2], payload[voiceFrameBytes:]) {
		t.Fatal("second frame did not preserve remaining source audio")
	}
	if !isSilentPCM(second[voiceFrameBytes/2:]) {
		t.Fatal("partial speech frame was not padded with silence")
	}

	for i := 0; i < voiceSilenceFrames; i++ {
		frame, started, acks, applyConditions, ok := relay.nextFrame()
		if !ok || started || len(acks) != 0 || !isSilentPCM(frame) || !applyConditions {
			t.Fatalf("silence frame %d ok=%v started=%v acks=%v apply_conditions=%v", i, ok, started, acks, applyConditions)
		}
	}
	if frame, started, acks, _, ok := relay.nextFrame(); ok || started || len(frame) != 0 || len(acks) != 0 {
		t.Fatalf("relay continued after silence tail: frame=%d started=%v acks=%v ok=%v", len(frame), started, acks, ok)
	}

	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x42}, voiceFrameBytes)})
	if _, started, _, _, ok := relay.nextFrame(); !ok || !started {
		t.Fatalf("new utterance ok=%v started=%v", ok, started)
	}
}

func TestVoiceMediaRelayInterruptDropsQueuedPlayback(t *testing.T) {
	var relay voiceMediaRelay
	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x24}, voiceFrameBytes*2)})
	if _, started, _, _, ok := relay.nextFrame(); !ok || !started {
		t.Fatalf("initial frame ok=%v started=%v", ok, started)
	}

	relay.interrupt()
	if relay.queue.bytes != 0 || relay.silenceFrames != 0 || relay.utteranceActive {
		t.Fatalf("relay retained interrupted state: %#v", relay)
	}
	if frame, _, _, _, ok := relay.nextFrame(); ok || len(frame) != 0 {
		t.Fatalf("interrupted relay produced frame bytes=%d ok=%v", len(frame), ok)
	}
}

func TestVoiceMediaRelayRemainsPendingUntilBufferedAudioAndTailDrain(t *testing.T) {
	var relay voiceMediaRelay
	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x24}, voiceFrameBytes*2)})
	if !relay.hasPendingPlayback() {
		t.Fatal("queued audio was not pending")
	}
	if _, _, _, _, ok := relay.nextFrame(); !ok || !relay.hasPendingPlayback() {
		t.Fatal("relay stopped pending before queued audio drained")
	}
	if _, _, _, _, ok := relay.nextFrame(); !ok || !relay.hasPendingPlayback() {
		t.Fatal("relay stopped pending before silence tail drained")
	}
	for relay.hasPendingPlayback() {
		if _, _, _, _, ok := relay.nextFrame(); !ok {
			t.Fatal("pending relay failed to produce its silence tail")
		}
	}
	if _, _, _, _, ok := relay.nextFrame(); ok {
		t.Fatal("drained relay still produced playback")
	}
}

func TestVoiceMediaRelayAddsCleanVADCommitTailAfterConditionedAudio(t *testing.T) {
	relay := newVoiceMediaRelay(true)
	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x24}, voiceFrameBytes)})

	if _, _, _, applyConditions, ok := relay.nextFrame(); !ok || !applyConditions {
		t.Fatalf("speech frame ok=%v apply_conditions=%v", ok, applyConditions)
	}
	for i := 0; i < voiceSilenceFrames; i++ {
		if _, _, _, applyConditions, ok := relay.nextFrame(); !ok || !applyConditions {
			t.Fatalf("conditioned tail frame %d ok=%v apply_conditions=%v", i, ok, applyConditions)
		}
	}
	for i := 0; i < voiceVADCommitFrames; i++ {
		frame, _, _, applyConditions, ok := relay.nextFrame()
		if !ok || applyConditions || !isSilentPCM(frame) {
			t.Fatalf("commit tail frame %d ok=%v apply_conditions=%v", i, ok, applyConditions)
		}
	}
	if relay.hasPendingPlayback() {
		t.Fatal("relay remained pending after VAD commit tail drained")
	}
}

func TestVoiceMediaQueueCombinesChunksAndAcknowledgesInOrder(t *testing.T) {
	var queue voiceMediaQueue
	queue.append(voiceAudioChunk{
		audio: bytes.Repeat([]byte{0x11}, voiceFrameBytes/2), itemID: "first", audioEndMS: 10,
	})
	queue.append(voiceAudioChunk{
		audio: bytes.Repeat([]byte{0x22}, voiceFrameBytes/2), itemID: "second", audioEndMS: 20,
	})

	frame, speechBytes, acks := queue.nextFrame()
	if speechBytes != voiceFrameBytes || queue.bytes != 0 || len(acks) != 2 {
		t.Fatalf("speech=%d remaining=%d acks=%v", speechBytes, queue.bytes, acks)
	}
	if acks[0].itemID != "first" || acks[1].itemID != "second" {
		t.Fatalf("ack order=%v", acks)
	}
	if !bytes.Equal(frame[:voiceFrameBytes/2], bytes.Repeat([]byte{0x11}, voiceFrameBytes/2)) ||
		!bytes.Equal(frame[voiceFrameBytes/2:], bytes.Repeat([]byte{0x22}, voiceFrameBytes/2)) {
		t.Fatal("combined frame did not preserve chunk order")
	}
}

func TestVoiceCallValidityRequiresTwoSidedConversation(t *testing.T) {
	call := &VoiceCall{
		Transcript: []VoiceTranscriptTurn{{Speaker: "receptionist", Text: "Hello"}},
		Metrics: VoiceCallMetrics{
			EndedBy: "audio_disconnected", ReceptionistAudioS: 1.2,
		},
	}

	validity := assessVoiceCall(call)
	if validity.Status != "invalid" {
		t.Fatalf("status=%q reasons=%v", validity.Status, validity.Reasons)
	}
	joined := strings.Join(validity.Reasons, "\n")
	for _, expected := range []string{"audio_disconnected", "caller produced no audio", "no caller turn", "no speaker turn-taking"} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("missing %q in %q", expected, joined)
		}
	}
}

func TestVoiceCallValidityAcceptsCompletedExchange(t *testing.T) {
	call := &VoiceCall{
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "Hello"},
			{Speaker: "caller", Text: "Please call me tomorrow"},
			{Speaker: "receptionist", Text: "Certainly"},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "caller_done", ReceptionistAudioS: 1.2, CallerAudioS: 1,
		},
	}

	validity := assessVoiceCall(call)
	if validity.Status != "valid" || len(validity.Reasons) != 0 {
		t.Fatalf("validity=%#v", validity)
	}
}

func TestVoiceCallValidityAcceptsSettledConversation(t *testing.T) {
	call := &VoiceCall{
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "Hello"},
			{Speaker: "caller", Text: "Please call me tomorrow"},
			{Speaker: "receptionist", Text: "Certainly, goodbye"},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "conversation_idle", ReceptionistAudioS: 1.2, CallerAudioS: 1,
		},
	}

	validity := assessVoiceCall(call)
	if validity.Status != "valid" || len(validity.Reasons) != 0 {
		t.Fatalf("validity=%#v", validity)
	}
}

func TestVoiceCallValidityAcceptsTargetCompletion(t *testing.T) {
	call := &VoiceCall{
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "Hello"},
			{Speaker: "caller", Text: "Please call me tomorrow"},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "target_done", ReceptionistAudioS: 1.2, CallerAudioS: 1,
		},
	}

	validity := assessVoiceCall(call)
	if validity.Status != "valid" || len(validity.Reasons) != 0 {
		t.Fatalf("validity=%#v", validity)
	}
}

func TestVoiceCallValidityRejectsUnrecognizedCallerTurn(t *testing.T) {
	call := &VoiceCall{
		Transcript: []VoiceTranscriptTurn{
			{Speaker: "receptionist", Text: "Would you like me to reserve it?"},
			{Speaker: "caller", Text: "Oui."},
		},
		Metrics: VoiceCallMetrics{
			EndedBy: "timeout", ReceptionistAudioS: 1.2, CallerAudioS: 1,
			CallerSourceTurns: 2, ReceptionistReceivedTurns: 1, PendingCallerTurns: 1,
			CallerResponseUndelivered: true,
		},
	}

	validity := assessVoiceCall(call)
	if validity.Status != "invalid" {
		t.Fatalf("validity=%#v", validity)
	}
	if joined := strings.Join(validity.Reasons, "\n"); !strings.Contains(joined, "generated but not recognized") {
		t.Fatalf("missing caller turn diagnostic in %q", joined)
	}
}

func TestVoiceConversationIdleRequiresSettledTwoSidedExchange(t *testing.T) {
	now := time.Date(2026, time.July, 23, 16, 0, 0, 0, time.UTC)
	quietAt := now.Add(-voiceConversationIdle - time.Second)
	activity := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   quietAt,
	}
	transcript := []VoiceTranscriptTurn{
		{Speaker: "receptionist", Text: "When should we call?"},
		{Speaker: "caller", Text: "Monday at four."},
		{Speaker: "receptionist", Text: "Your callback is booked. Goodbye."},
	}
	listening := func(threadID string) []sdk.RuntimeTelemetryEvent {
		events := []sdk.RuntimeTelemetryEvent{{
			ThreadID: threadID, Type: "realtime.state", Time: quietAt,
			Data: json.RawMessage(`{"state":"listening"}`),
		}}
		if threadID == "target" {
			events = append(events,
				sdk.RuntimeTelemetryEvent{ThreadID: threadID, Type: "realtime.assistant", Time: quietAt.Add(-4 * time.Second)},
				sdk.RuntimeTelemetryEvent{ThreadID: threadID, Type: "realtime.user", Time: quietAt.Add(-2 * time.Second)},
			)
		} else {
			events = append(events,
				sdk.RuntimeTelemetryEvent{ThreadID: threadID, Type: "realtime.user", Time: quietAt.Add(-3 * time.Second)},
				sdk.RuntimeTelemetryEvent{ThreadID: threadID, Type: "realtime.assistant", Time: quietAt.Add(-2500 * time.Millisecond)},
			)
		}
		return events
	}

	if !voiceConversationIsIdle(
		now, activity, transcript,
		listening("target"), "target", listening("caller"), "caller",
	) {
		t.Fatal("settled conversation was not detected as idle")
	}

	recent := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   now.Add(-time.Second),
	}
	if voiceConversationIsIdle(
		now, recent, transcript,
		listening("target"), "target", listening("caller"), "caller",
	) {
		t.Fatal("recent media activity was detected as idle")
	}

	speaking := []sdk.RuntimeTelemetryEvent{{
		ThreadID: "caller", Type: "realtime.state", Time: quietAt,
		Data: json.RawMessage(`{"state":"speaking"}`),
	}}
	if voiceConversationIsIdle(
		now, activity, transcript,
		listening("target"), "target", speaking, "caller",
	) {
		t.Fatal("active caller was detected as idle")
	}

	pendingTool := append(listening("target"), sdk.RuntimeTelemetryEvent{
		ThreadID: "target", Type: "tool.call", Time: quietAt,
		Data: json.RawMessage(`{"id":"call-one","name":"calendar_commit"}`),
	})
	if voiceConversationIsIdle(
		now, activity, transcript,
		pendingTool, "target", listening("caller"), "caller",
	) {
		t.Fatal("pending tool call was detected as idle")
	}
}

func TestVoiceConversationIdleWaitsForFinalCallerAudioDelivery(t *testing.T) {
	now := time.Date(2026, time.July, 23, 17, 8, 0, 0, time.UTC)
	quietAt := now.Add(-voiceConversationIdle - time.Second)
	activity := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   quietAt,
	}
	transcript := []VoiceTranscriptTurn{
		{Speaker: "receptionist", Text: "When should we call?"},
		{Speaker: "caller", Text: "Monday at four."},
		{Speaker: "receptionist", Text: "Your callback is booked."},
	}
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.assistant", Time: quietAt.Add(-4 * time.Second)},
		// This is a late transcription of the previous caller turn. Its timestamp
		// is newer than the caller's next output, but it only matches one turn.
		{ThreadID: "target", Type: "realtime.user", Time: quietAt.Add(-500 * time.Millisecond)},
		{ThreadID: "target", Type: "realtime.assistant", Time: quietAt.Add(-2 * time.Second)},
		{ThreadID: "target", Type: "realtime.state", Time: quietAt, Data: json.RawMessage(`{"state":"listening"}`)},
	}
	callerEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.user", Time: quietAt.Add(-1500 * time.Millisecond)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: quietAt.Add(-3500 * time.Millisecond)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: quietAt.Add(-time.Second)},
		{ThreadID: "caller", Type: "realtime.state", Time: quietAt, Data: json.RawMessage(`{"state":"listening"}`)},
	}

	if voiceConversationIsIdle(
		now, activity, transcript,
		targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("conversation completed before the target acknowledged the caller's final utterance")
	}

	targetEvents = append(targetEvents, sdk.RuntimeTelemetryEvent{
		ThreadID: "target", Type: "realtime.user", Time: quietAt.Add(-250 * time.Millisecond),
	})
	if !voiceConversationIsIdle(
		now, activity, append(transcript, VoiceTranscriptTurn{Speaker: "receptionist", Text: "Goodbye."}),
		targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("conversation did not complete after final caller audio was acknowledged")
	}
}

func TestVoiceConversationIdleAcceptsFinalGoodbyeWithoutCallerTelemetryAck(t *testing.T) {
	now := time.Date(2026, time.July, 28, 12, 0, 0, 0, time.UTC)
	quietAt := now.Add(-voiceConversationIdle - time.Second)
	activity := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   quietAt,
	}
	transcript := []VoiceTranscriptTurn{
		{Speaker: "receptionist", Text: "How can I help?"},
		{Speaker: "caller", Text: "Please reserve tomorrow at three."},
		{Speaker: "receptionist", Text: "It is reserved. Goodbye."},
	}
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.user", Time: quietAt.Add(-4 * time.Second)},
		{ThreadID: "target", Type: "realtime.assistant", Time: quietAt.Add(-7 * time.Second)},
		{ThreadID: "target", Type: "realtime.assistant", Time: quietAt.Add(-2 * time.Second)},
		{ThreadID: "target", Type: "realtime.state", Time: quietAt, Data: json.RawMessage(`{"state":"listening"}`)},
	}
	callerEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.assistant", Time: quietAt.Add(-5 * time.Second)},
		{ThreadID: "caller", Type: "realtime.user", Time: quietAt.Add(-6 * time.Second)},
		{ThreadID: "caller", Type: "realtime.state", Time: quietAt, Data: json.RawMessage(`{"state":"listening"}`)},
	}

	if voiceRelayDeliverySettled(activity, targetEvents, "target", callerEvents, "caller") {
		t.Fatal("strict relay check unexpectedly accepted missing final caller telemetry")
	}
	if !voiceConversationIsIdle(
		now, activity, transcript,
		targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("completed final goodbye did not become conversation idle")
	}

	callerEvents = append(callerEvents, sdk.RuntimeTelemetryEvent{
		ThreadID: "caller", Type: "realtime.assistant", Time: quietAt.Add(-time.Second),
	})
	if voiceConversationIsIdle(
		now, activity, transcript,
		targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("conversation completed before the caller's last turn reached the target")
	}
}

func TestVoiceRelayDeliverySettledWaitsForMediaAndBothDestinations(t *testing.T) {
	base := time.Date(2026, time.July, 23, 17, 0, 0, 0, time.UTC)
	activity := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   base,
	}
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.assistant", Time: base},
		{ThreadID: "target", Type: "realtime.user", Time: base.Add(3 * time.Second)},
	}
	callerEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.user", Time: base.Add(time.Second)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: base.Add(2 * time.Second)},
	}
	if !voiceRelayDeliverySettled(activity, targetEvents, "target", callerEvents, "caller") {
		t.Fatal("fully delivered relay was not settled")
	}

	activity.active["caller"] = true
	if voiceRelayDeliverySettled(activity, targetEvents, "target", callerEvents, "caller") {
		t.Fatal("active media relay was considered settled")
	}
	activity.active["caller"] = false

	callerEvents = append(callerEvents, sdk.RuntimeTelemetryEvent{
		ThreadID: "caller", Type: "realtime.assistant", Time: base.Add(4 * time.Second),
	})
	if voiceRelayDeliverySettled(activity, targetEvents, "target", callerEvents, "caller") {
		t.Fatal("unacknowledged caller output was considered settled")
	}
}

func TestVoiceCallerCompletedAcceptsDoneEvidence(t *testing.T) {
	threadID := "caller-one"
	if !voiceCallerCompleted([]sdk.RuntimeTelemetryEvent{{
		ThreadID: threadID,
		Type:     "tool.call",
		Data:     json.RawMessage(`{"name":"done"}`),
	}}, threadID) {
		t.Fatal("done tool call was not accepted as completion evidence")
	}
	if !voiceCallerCompleted([]sdk.RuntimeTelemetryEvent{{
		ThreadID: threadID,
		Type:     "thread.done",
	}}, threadID) {
		t.Fatal("thread.done was not accepted as completion evidence")
	}
	if voiceCallerCompleted([]sdk.RuntimeTelemetryEvent{{
		ThreadID: threadID,
		Type:     "tool.call",
		Data:     json.RawMessage(`{"name":"send"}`),
	}}, threadID) {
		t.Fatal("unrelated tool call was accepted as completion evidence")
	}
}

func TestVoiceBridgeEndReasonUsesEndpointAndCompletionEvidence(t *testing.T) {
	abnormal := errors.New("unexpected websocket closure")
	tests := []struct {
		name       string
		exit       voiceBridgeExit
		completion bool
		want       string
	}{
		{
			name:       "cached caller completion wins over target failure",
			exit:       voiceBridgeExit{Endpoint: voiceBridgeTarget, Operation: "read", Err: abnormal},
			completion: true,
			want:       "caller_done",
		},
		{
			name:       "caller failure with settled completion",
			exit:       voiceBridgeExit{Endpoint: voiceBridgeCaller, Operation: "read", Err: abnormal},
			completion: true,
			want:       "caller_done",
		},
		{
			name: "caller normal close before completion",
			exit: voiceBridgeExit{
				Endpoint: voiceBridgeCaller, Operation: "read",
				CloseCode: websocket.CloseNormalClosure,
			},
			want: "caller_done",
		},
		{
			name: "caller abnormal close before completion",
			exit: voiceBridgeExit{Endpoint: voiceBridgeCaller, Operation: "read", Err: abnormal},
			want: "audio_disconnected",
		},
		{
			name: "target normal close before completion",
			exit: voiceBridgeExit{Endpoint: voiceBridgeTarget, Operation: "read"},
			want: "audio_disconnected",
		},
		{
			name: "carrier normal close before completion",
			exit: voiceBridgeExit{
				Endpoint: voiceBridgeCarrier, Operation: "read",
				CloseCode: websocket.CloseNormalClosure,
			},
			want: "caller_done",
		},
		{
			name: "structured caller completion wins over abnormal socket close",
			exit: voiceBridgeExit{
				Endpoint: voiceBridgeCaller, Operation: "read", Err: abnormal,
				TerminalReason: sdk.RealtimeTerminalCallerDone,
			},
			want: "caller_done",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := voiceBridgeEndReason(test.exit, test.completion); got != test.want {
				t.Fatalf("voiceBridgeEndReason() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestVoiceBridgeExitAllowsSettledExchangeOnlyForReadClosures(t *testing.T) {
	abnormal := errors.New("unexpected websocket closure")
	for _, endpoint := range []voiceBridgeEndpoint{voiceBridgeCaller, voiceBridgeTarget, voiceBridgeCarrier} {
		if !voiceBridgeExitAllowsSettledExchange(voiceBridgeExit{Endpoint: endpoint, Operation: "read"}) {
			t.Fatalf("%s read closure did not allow settled completion", endpoint)
		}
		if voiceBridgeExitAllowsSettledExchange(voiceBridgeExit{Endpoint: endpoint, Operation: "write_audio"}) {
			t.Fatalf("%s write failure allowed settled completion", endpoint)
		}
		if voiceBridgeExitAllowsSettledExchange(voiceBridgeExit{
			Endpoint: endpoint, Operation: "read", Err: abnormal,
			CloseCode: websocket.CloseAbnormalClosure,
		}) {
			t.Fatalf("%s abnormal read failure allowed transcript settlement", endpoint)
		}
	}
	if voiceBridgeExitAllowsSettledExchange(voiceBridgeExit{
		Endpoint: voiceBridgeCarrier, Operation: "read",
		TerminalReason: sdk.RealtimeTerminalAudioDisconnected,
		CloseCode:      websocket.CloseNormalClosure,
	}) {
		t.Fatal("structured audio disconnection allowed transcript settlement")
	}
	if !voiceBridgeExitAllowsSettledExchange(voiceBridgeExit{
		Endpoint: voiceBridgeCaller, Operation: "read", Err: abnormal,
		TerminalReason: sdk.RealtimeTerminalCallerDone,
	}) {
		t.Fatal("structured caller completion was treated as a transport failure")
	}
}

func TestVoiceBridgeExitDiagnosticsPersistSeparateLegs(t *testing.T) {
	started := time.Date(2026, time.July, 29, 9, 0, 0, 0, time.UTC)
	call := &VoiceCall{
		StartedAt: started,
		Metrics:   VoiceCallMetrics{CallerResponseUndelivered: true},
	}
	appendVoiceBridgeExit(call, voiceBridgeExit{
		Leg: voiceBridgeLegCallerToCarrier, Endpoint: voiceBridgeCaller,
		Operation: "read", CloseCode: websocket.CloseAbnormalClosure,
		TerminalReason: sdk.RealtimeTerminalAudioDisconnected,
		Err:            errors.New("websocket transport lost"),
		ObservedAt:     started.Add(60 * time.Second),
	})
	appendVoiceBridgeExit(call, voiceBridgeExit{
		Leg: voiceBridgeLegCarrierToCaller, Endpoint: voiceBridgeCarrier,
		Operation:      "bridge_shutdown",
		TerminalReason: sdk.RealtimeTerminalStopped,
		ObservedAt:     started.Add(60*time.Second + 25*time.Millisecond),
	})

	if len(call.BridgeExits) != 2 {
		t.Fatalf("bridge exits=%#v", call.BridgeExits)
	}
	first := call.BridgeExits[0]
	if first.Leg != "caller_to_carrier" || first.Endpoint != "caller" || first.Operation != "read" {
		t.Fatalf("first exit=%#v", first)
	}
	if first.CloseCode != websocket.CloseAbnormalClosure ||
		first.Reason != string(sdk.RealtimeTerminalAudioDisconnected) ||
		first.Error != "websocket transport lost" ||
		first.ElapsedMS != 60000 ||
		!first.TransportFailure ||
		!first.CallerResponseUndelivered {
		t.Fatalf("first exit diagnostics=%#v", first)
	}
	second := call.BridgeExits[1]
	if second.Leg != "carrier_to_caller" || second.Operation != "bridge_shutdown" ||
		second.ElapsedMS != 60025 || second.TransportFailure {
		t.Fatalf("second exit diagnostics=%#v", second)
	}
}

func TestVoiceBridgeExitReportersProduceOneResultPerLeg(t *testing.T) {
	exits := make(chan voiceBridgeExit, 2)
	caller := newVoiceBridgeExitReporter(context.Background(), exits, voiceBridgeLegCallerToCarrier)
	carrier := newVoiceBridgeExitReporter(context.Background(), exits, voiceBridgeLegCarrierToCaller)
	caller.report(voiceBridgeExit{
		Endpoint: voiceBridgeCaller, Operation: "read", Err: errors.New("caller lost"),
	})
	caller.finish(nil, voiceBridgeCaller)
	carrier.finish(nil, voiceBridgeCarrier)

	call := &VoiceCall{StartedAt: time.Now().Add(-time.Second)}
	(&service{}).collectVoiceBridgeExits(call, exits, 2)
	if len(call.BridgeExits) != 2 {
		t.Fatalf("bridge exits=%#v", call.BridgeExits)
	}
	if call.BridgeExits[0].Leg != "caller_to_carrier" ||
		call.BridgeExits[1].Leg != "carrier_to_caller" {
		t.Fatalf("bridge exits=%#v", call.BridgeExits)
	}
}

func TestWaitForVoiceCompletionEvidenceAllowsDelayedTelemetry(t *testing.T) {
	probes := 0
	if !waitForVoiceCompletionEvidence(context.Background(), 200*time.Millisecond, 5*time.Millisecond, func() bool {
		probes++
		return probes == 3
	}) {
		t.Fatal("delayed completion evidence was not accepted")
	}

	started := time.Now()
	if waitForVoiceCompletionEvidence(context.Background(), 25*time.Millisecond, 5*time.Millisecond, func() bool {
		return false
	}) {
		t.Fatal("missing completion evidence was accepted")
	}
	if time.Since(started) < 20*time.Millisecond {
		t.Fatal("completion grace returned before its deadline")
	}
}

func TestVoiceFinalExchangeSettledRequiresDeliveredInactiveConversation(t *testing.T) {
	base := time.Date(2026, time.July, 27, 16, 0, 0, 0, time.UTC)
	activity := &voiceMediaActivity{
		active: map[string]bool{"receptionist": false, "caller": false},
		last:   base,
	}
	transcript := []VoiceTranscriptTurn{
		{Speaker: "receptionist", Text: "When should we call?"},
		{Speaker: "caller", Text: "Monday at four."},
		{Speaker: "receptionist", Text: "Your callback is booked. Goodbye."},
	}
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.assistant", Time: base},
		{ThreadID: "target", Type: "realtime.user", Time: base.Add(3 * time.Second)},
		{ThreadID: "target", Type: "realtime.state", Time: base.Add(4 * time.Second), Data: json.RawMessage(`{"state":"listening"}`)},
	}
	callerEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.user", Time: base.Add(time.Second)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: base.Add(2 * time.Second)},
		{ThreadID: "caller", Type: "realtime.state", Time: base.Add(4 * time.Second), Data: json.RawMessage(`{"state":"disconnected"}`)},
	}
	if !voiceFinalExchangeSettled(
		activity, transcript, targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("completed final exchange was not settled")
	}

	callerEvents[0].Time = base.Add(-time.Second)
	if !voiceFinalExchangeSettled(
		activity, transcript, targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("final exchange required caller telemetry for the receptionist goodbye")
	}

	active := &voiceMediaActivity{
		active: map[string]bool{"receptionist": true, "caller": false},
		last:   base,
	}
	if voiceFinalExchangeSettled(
		active, transcript, targetEvents, "target", callerEvents, "caller",
	) {
		t.Fatal("active media was accepted as a settled exchange")
	}

	pendingTool := append(slices.Clone(targetEvents), sdk.RuntimeTelemetryEvent{
		ThreadID: "target", Type: "tool.call", Time: base.Add(5 * time.Second),
		Data: json.RawMessage(`{"id":"call-one","name":"calendar_commit"}`),
	})
	if voiceFinalExchangeSettled(
		activity, transcript, pendingTool, "target", callerEvents, "caller",
	) {
		t.Fatal("pending target tool was accepted as a settled exchange")
	}
}

func TestVoiceMetricsSortsTelemetryChronologically(t *testing.T) {
	started := time.Date(2026, time.July, 23, 12, 0, 0, 0, time.UTC)
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.state", Time: started.Add(4 * time.Second), Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "target", Type: "realtime.state", Time: started.Add(2500 * time.Millisecond), Data: json.RawMessage(`{"state":"thinking"}`)},
		{ThreadID: "target", Type: "realtime.user", Time: started.Add(2 * time.Second)},
		{ThreadID: "target", Type: "realtime.state", Time: started.Add(time.Second), Data: json.RawMessage(`{"state":"speaking"}`)},
	}

	metrics := voiceMetrics(targetEvents, nil, "target", "caller", started, nil, nil, "caller_done")
	if metrics.FirstResponseMS != 1000 {
		t.Fatalf("first response = %d, want 1000", metrics.FirstResponseMS)
	}
	if metrics.AverageResponseMS != 2000 {
		t.Fatalf("average response = %d, want 2000", metrics.AverageResponseMS)
	}
}

func TestVoiceMetricsPersistsPendingTurnCounters(t *testing.T) {
	started := time.Date(2026, time.July, 29, 8, 0, 0, 0, time.UTC)
	targetEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "target", Type: "realtime.state", Time: started.Add(time.Second), Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "target", Type: "realtime.assistant", Time: started.Add(2 * time.Second)},
		{ThreadID: "target", Type: "realtime.user", Time: started.Add(4 * time.Second)},
	}
	callerEvents := []sdk.RuntimeTelemetryEvent{
		{ThreadID: "caller", Type: "realtime.user", Time: started.Add(3 * time.Second)},
		{ThreadID: "caller", Type: "realtime.state", Time: started.Add(3500 * time.Millisecond), Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: started.Add(4 * time.Second)},
		{ThreadID: "caller", Type: "realtime.state", Time: started.Add(5 * time.Second), Data: json.RawMessage(`{"state":"speaking"}`)},
		{ThreadID: "caller", Type: "realtime.assistant", Time: started.Add(6 * time.Second)},
	}

	metrics := voiceMetrics(targetEvents, callerEvents, "target", "caller", started, nil, nil, "timeout")
	if metrics.CallerSourceTurns != 2 || metrics.ReceptionistReceivedTurns != 1 || metrics.PendingCallerTurns != 1 {
		t.Fatalf("caller delivery metrics=%#v", metrics)
	}
	if !metrics.CallerResponseUndelivered {
		t.Fatalf("undelivered caller response was not exposed: %#v", metrics)
	}
	if metrics.ReceptionistSourceTurns != 1 || metrics.CallerReceivedTurns != 1 || metrics.PendingReceptionistTurns != 0 {
		t.Fatalf("receptionist delivery metrics=%#v", metrics)
	}
}

func TestVoiceAudioTerminalDetailsUsesSDKReasonAndCloseCode(t *testing.T) {
	reason, closeCode := voiceAudioTerminalDetails(
		&websocket.CloseError{Code: websocket.CloseAbnormalClosure, Text: "transport lost"},
		sdk.RealtimeAudioTerminalMessage{
			Type: sdk.RealtimeAudioTerminalMessageType, Reason: sdk.RealtimeTerminalCallerDone,
			CloseCode: websocket.CloseNormalClosure,
		},
	)
	if reason != sdk.RealtimeTerminalCallerDone || closeCode != websocket.CloseNormalClosure {
		t.Fatalf("terminal reason=%q close_code=%d", reason, closeCode)
	}

	reason, closeCode = voiceAudioTerminalDetails(
		&websocket.CloseError{Code: websocket.CloseNormalClosure, Text: "caller_done"},
		sdk.RealtimeAudioTerminalMessage{},
	)
	if reason != sdk.RealtimeTerminalCallerDone || closeCode != websocket.CloseNormalClosure {
		t.Fatalf("close reason=%q close_code=%d", reason, closeCode)
	}
}

func TestVoiceTranscriptExcludesAssistantTextWithoutSpokenAudio(t *testing.T) {
	started := time.Date(2026, time.July, 28, 8, 14, 0, 0, time.UTC)
	events := []sdk.RuntimeTelemetryEvent{
		{
			ThreadID: "target", Type: "realtime.assistant", Time: started.Add(9 * time.Second),
			Data: json.RawMessage(`{"text":"User input is needed to continue."}`),
		},
		{
			ThreadID: "target", Type: "tool.result", Time: started.Add(8 * time.Second),
			Data: json.RawMessage(`{"name":"create_booking","success":true}`),
		},
		{
			ThreadID: "target", Type: "realtime.assistant", Time: started.Add(7 * time.Second),
			Data: json.RawMessage(`{"text":"Your callback is confirmed."}`),
		},
		{
			ThreadID: "target", Type: "realtime.state", Time: started.Add(6 * time.Second),
			Data: json.RawMessage(`{"state":"speaking"}`),
		},
		{
			ThreadID: "target", Type: "realtime.user", Time: started.Add(5 * time.Second),
			Data: json.RawMessage(`{"text":"Yes, please book it."}`),
		},
		{
			ThreadID: "target", Type: "realtime.assistant", Time: started.Add(2 * time.Second),
			Data: json.RawMessage(`{"text":"How can I help?"}`),
		},
		{
			ThreadID: "target", Type: "realtime.state", Time: started.Add(time.Second),
			Data: json.RawMessage(`{"state":"speaking"}`),
		},
		{
			ThreadID: "other", Type: "realtime.assistant", Time: started,
			Data: json.RawMessage(`{"text":"Ignore me."}`),
		},
	}

	transcript := voiceTranscript(events, "target", started)
	if len(transcript) != 3 {
		t.Fatalf("transcript=%#v", transcript)
	}
	if transcript[0].Speaker != "receptionist" || transcript[0].Text != "How can I help?" {
		t.Fatalf("opening=%#v", transcript[0])
	}
	if transcript[1].Speaker != "caller" || transcript[1].Text != "Yes, please book it." {
		t.Fatalf("caller=%#v", transcript[1])
	}
	if transcript[2].Speaker != "receptionist" || transcript[2].Text != "Your callback is confirmed." {
		t.Fatalf("confirmation=%#v", transcript[2])
	}
}

func TestVoiceOpeningGuidanceDoesNotBecomeInitialConversationPrompt(t *testing.T) {
	spec := VoiceFixtureSpec{TargetDirective: "Be a receptionist.", Greeting: "Ask when to call back."}
	directive := voiceTargetDirective(spec)
	if !strings.Contains(directive, "Opening guidance: Ask when to call back.") {
		t.Fatalf("directive=%q", directive)
	}
	if strings.Contains(voiceOpeningCue(), spec.Greeting) {
		t.Fatalf("opening cue contains scenario guidance: %q", voiceOpeningCue())
	}
	if !strings.Contains(voiceOpeningCue(), "stop and wait") {
		t.Fatalf("opening cue=%q", voiceOpeningCue())
	}
}

func TestVoiceCarrierRouteUsesAgentDirectiveAndNeutralOpeningCue(t *testing.T) {
	spec := VoiceFixtureSpec{
		TargetDirective: "  Be the project's receptionist.  ",
		Greeting:        "Bonjour, Flexylead à votre écoute.",
		Voice:           "Kore",
		TimeoutSeconds:  90,
	}

	input := voiceCarrierRouteInput(spec, "+15550100001")
	if got := input["directive"]; got != "Be the project's receptionist." {
		t.Fatalf("directive=%q", got)
	}
	if got := input["greeting"]; got != voiceOpeningCue() {
		t.Fatalf("greeting=%q", got)
	}
	if strings.Contains(input["greeting"].(string), spec.Greeting) {
		t.Fatalf("carrier opening cue contains scripted greeting: %q", input["greeting"])
	}
	if !strings.Contains(input["greeting"].(string), "You are the receptionist") {
		t.Fatalf("carrier opening cue does not anchor the target role: %q", input["greeting"])
	}
}

func TestVoiceCarrierRouteFallsBackWhenAgentDirectiveIsEmpty(t *testing.T) {
	input := voiceCarrierRouteInput(VoiceFixtureSpec{}, "+15550100001")
	if strings.TrimSpace(input["directive"].(string)) == "" {
		t.Fatal("carrier route directive is empty")
	}
}

func TestVoiceAudioConditionsAreDeterministic(t *testing.T) {
	samples := make([]int16, voiceFrameBytes/2)
	for i := range samples {
		samples[i] = int16(5000 * math.Sin(2*math.Pi*440*float64(i)/voiceSampleRate))
	}
	frame := pcm16ToBytes(samples)
	spec := &VoiceAudioConditions{Preset: "train_station", Intensity: "moderate", Codec: "none", Seed: 73}
	first := newVoiceAudioPipeline(spec, true).process(frame)
	second := newVoiceAudioPipeline(spec, true).process(frame)
	if !bytes.Equal(first, second) {
		t.Fatal("same audio condition seed produced different output")
	}
	if bytes.Equal(first, frame) {
		t.Fatal("train station condition did not alter caller audio")
	}
	delivered := pcm16FromBytes(first)
	residual := make([]int16, len(samples))
	for i := range samples {
		residual[i] = delivered[i] - samples[i]
	}
	measuredSNR := 20 * math.Log10(pcmRMS(samples)/pcmRMS(residual))
	if measuredSNR < 9.5 || measuredSNR > 10.5 {
		t.Fatalf("measured SNR=%.2f dB, want approximately 10 dB", measuredSNR)
	}
}

func TestVoiceAudioConditionsProcessSilenceAndTelephoneCodec(t *testing.T) {
	silence := make([]byte, voiceFrameBytes)
	noisy := newVoiceAudioPipeline(&VoiceAudioConditions{
		Preset: "office", Intensity: "heavy", Codec: "g711_mulaw", Seed: 42,
	}, true)
	delivered := noisy.process(silence)
	if isSilentPCM(delivered) {
		t.Fatal("conditioned silence did not contain ambient audio")
	}
	if len(delivered) != len(silence) {
		t.Fatalf("conditioned frame size=%d, want %d", len(delivered), len(silence))
	}
	metrics := noisy.metrics()
	if metrics == nil || metrics.ProcessedFrames != 1 || metrics.Codec != "g711_mulaw" || metrics.TargetSNRDB != 4 ||
		metrics.VADCommitSilenceMS != int(voiceVADCommitTail/time.Millisecond) {
		t.Fatalf("unexpected condition metrics: %+v", metrics)
	}
}

func TestVoiceAudioConditionNormalizationAndValidation(t *testing.T) {
	spec := VoiceFixtureSpec{
		CallerGoal: "Ask for help", TimeoutSeconds: 30,
		AudioConditions: &VoiceAudioConditions{Preset: " Poor_Phone "},
	}
	normalizeVoiceSpec(&spec)
	if spec.AudioConditions == nil || spec.AudioConditions.Preset != "poor_phone" ||
		spec.AudioConditions.Intensity != "moderate" || spec.AudioConditions.Codec != "g711_mulaw" ||
		spec.AudioConditions.Seed != defaultVoiceAudioSeed {
		t.Fatalf("unexpected normalized conditions: %+v", spec.AudioConditions)
	}
	if err := validateVoiceSpec(spec); err != nil {
		t.Fatalf("valid conditions rejected: %v", err)
	}

	spec.AudioConditions.Preset = "spaceship"
	if err := validateVoiceSpec(spec); err == nil {
		t.Fatal("unknown audio preset accepted")
	}

	clean := VoiceFixtureSpec{
		CallerGoal: "Ask for help", TimeoutSeconds: 30,
		AudioConditions: &VoiceAudioConditions{Preset: "clean", Codec: "none"},
	}
	normalizeVoiceSpec(&clean)
	if clean.AudioConditions != nil {
		t.Fatalf("clean default should not allocate a pipeline: %+v", clean.AudioConditions)
	}
}

func TestVoiceTelephoneRoundTripPreservesFrameLengthAndChangesSignal(t *testing.T) {
	input := make([]int16, voiceFrameBytes/2)
	for i := range input {
		input[i] = int16((i%101 - 50) * 400)
	}
	output := voiceTelephoneMuLawRoundTrip(input)
	if len(output) != len(input) {
		t.Fatalf("telephone output length=%d, want %d", len(output), len(input))
	}
	if slices.Equal(output, input) {
		t.Fatal("telephone codec did not change input")
	}
}

func isSilentPCM(audio []byte) bool {
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVoiceMediaRelayPacesSpeechAndAddsSilenceTail(t *testing.T) {
	payload := bytes.Repeat([]byte{0x24}, voiceFrameBytes+voiceFrameBytes/2)
	var relay voiceMediaRelay
	relay.append(voiceAudioChunk{audio: payload, itemID: "item-one", audioEndMS: 30})

	first, started, acks, ok := relay.nextFrame()
	if !ok || !started || len(acks) != 0 {
		t.Fatalf("first frame ok=%v started=%v acks=%v", ok, started, acks)
	}
	if !bytes.Equal(first, payload[:voiceFrameBytes]) {
		t.Fatal("first frame did not preserve source audio")
	}

	second, started, acks, ok := relay.nextFrame()
	if !ok || started || len(acks) != 1 || acks[0].itemID != "item-one" || acks[0].audioEndMS != 30 {
		t.Fatalf("second frame ok=%v started=%v acks=%v", ok, started, acks)
	}
	if !bytes.Equal(second[:voiceFrameBytes/2], payload[voiceFrameBytes:]) {
		t.Fatal("second frame did not preserve remaining source audio")
	}
	if !isSilentPCM(second[voiceFrameBytes/2:]) {
		t.Fatal("partial speech frame was not padded with silence")
	}

	for i := 0; i < voiceSilenceFrames; i++ {
		frame, started, acks, ok := relay.nextFrame()
		if !ok || started || len(acks) != 0 || !isSilentPCM(frame) {
			t.Fatalf("silence frame %d ok=%v started=%v acks=%v", i, ok, started, acks)
		}
	}
	if frame, started, acks, ok := relay.nextFrame(); ok || started || len(frame) != 0 || len(acks) != 0 {
		t.Fatalf("relay continued after silence tail: frame=%d started=%v acks=%v ok=%v", len(frame), started, acks, ok)
	}

	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x42}, voiceFrameBytes)})
	if _, started, _, ok := relay.nextFrame(); !ok || !started {
		t.Fatalf("new utterance ok=%v started=%v", ok, started)
	}
}

func TestVoiceMediaRelayInterruptDropsQueuedPlayback(t *testing.T) {
	var relay voiceMediaRelay
	relay.append(voiceAudioChunk{audio: bytes.Repeat([]byte{0x24}, voiceFrameBytes*2)})
	if _, started, _, ok := relay.nextFrame(); !ok || !started {
		t.Fatalf("initial frame ok=%v started=%v", ok, started)
	}

	relay.interrupt()
	if relay.queue.bytes != 0 || relay.silenceFrames != 0 || relay.utteranceActive {
		t.Fatalf("relay retained interrupted state: %#v", relay)
	}
	if frame, _, _, ok := relay.nextFrame(); ok || len(frame) != 0 {
		t.Fatalf("interrupted relay produced frame bytes=%d ok=%v", len(frame), ok)
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

func isSilentPCM(audio []byte) bool {
	for _, value := range audio {
		if value != 0 {
			return false
		}
	}
	return true
}

package main

import (
	"context"
	"testing"
	"time"
)

func TestJSONHumanAudioPacerBoundsLatencyAndDropsOnlyStaleAudio(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pacer := newJSONCarrierAudioPacer(
		ctx, 16000, carrierCodecL16_16, "telnyx", "stream-1", false,
		newTwilioPlaybackTracker(), liveHumanCarrierPacerPolicy(),
		func([]byte) error { return nil }, nil, nil,
	)
	packets := make([]carrierPacedPacket, 20)
	for i := range packets {
		packets[i] = carrierPacedPacket{PCM: make([]int16, 320)}
	}
	queuedMS, droppedMS, err := pacer.enqueue(context.Background(), packets)
	if err != nil {
		t.Fatal(err)
	}
	if queuedMS > 60 {
		t.Fatalf("live human queue=%dms, want <=60ms after trimming", queuedMS)
	}
	if droppedMS < 300 {
		t.Fatalf("stale audio dropped=%dms, want at least 300ms from a 400ms burst", droppedMS)
	}
}

func TestTwilioHumanAudioPacerUsesSameBoundedPolicy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pacer := newTwilioAudioPacerWithPolicy(
		ctx, "stream-1", newTwilioPlaybackTracker(), liveHumanCarrierPacerPolicy(),
		func([]byte) error { return nil }, nil,
	)
	packets := make([]twilioAudioPacket, 20)
	for i := range packets {
		packets[i] = twilioAudioPacket{PCM: make([]int16, 160)}
	}
	queuedMS, droppedMS, err := pacer.enqueueWithDiagnostics(context.Background(), packets, realtimeBridgeControl{})
	if err != nil {
		t.Fatal(err)
	}
	if queuedMS > 60 {
		t.Fatalf("Twilio live human queue=%dms, want <=60ms after trimming", queuedMS)
	}
	if droppedMS < 300 {
		t.Fatalf("Twilio stale audio dropped=%dms, want at least 300ms from a 400ms burst", droppedMS)
	}
}

func TestSIPHumanAudioPacerDropsOldestBeforeOverflow(t *testing.T) {
	pacer := &sipRTPPacer{
		playback:      &sipPlaybackState{},
		queue:         make(chan sipRTPOutboundPacket, 6),
		dropStale:     true,
		trimToPackets: 3,
	}
	first := make([]sipRTPOutboundPacket, 6)
	if _, err := pacer.enqueue(first); err != nil {
		t.Fatal(err)
	}
	if _, err := pacer.enqueue([]sipRTPOutboundPacket{{}}); err != nil {
		t.Fatalf("live SIP queue overflowed instead of dropping stale audio: %v", err)
	}
	if got := len(pacer.queue) * int(sipRTPPacketTime/time.Millisecond); got > 80 {
		t.Fatalf("live SIP queue=%dms after trim", got)
	}
	if pacer.droppedMS() < 60 {
		t.Fatalf("live SIP stale drop=%dms, want at least 60ms", pacer.droppedMS())
	}
}

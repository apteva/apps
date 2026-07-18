package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"
)

type twilioWriteObservation struct {
	event string
	at    time.Time
}

func testTwilioPacer(t *testing.T) (*twilioAudioPacer, context.CancelFunc, <-chan twilioWriteObservation) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	writes := make(chan twilioWriteObservation, 64)
	pacer := newTwilioAudioPacer(ctx, "stream-test", newTwilioPlaybackTracker(), func(payload []byte) error {
		var envelope struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return err
		}
		writes <- twilioWriteObservation{event: envelope.Event, at: time.Now()}
		return nil
	}, nil)
	return pacer, cancel, writes
}

func waitTwilioWrite(t *testing.T, writes <-chan twilioWriteObservation) twilioWriteObservation {
	t.Helper()
	select {
	case observation := <-writes:
		return observation
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Twilio write")
		return twilioWriteObservation{}
	}
}

func TestTwilioAudioPacerWritesAtCarrierCadence(t *testing.T) {
	pacer, cancel, writes := testTwilioPacer(t)
	defer cancel()
	packets := []twilioAudioPacket{
		{PCM: make([]int16, 160), AudioEndMS: 20},
		{PCM: make([]int16, 160), AudioEndMS: 40},
		{PCM: make([]int16, 160), AudioEndMS: 60},
	}
	if _, err := pacer.enqueue(context.Background(), packets, realtimeBridgeControl{ItemID: "item-1"}); err != nil {
		t.Fatal(err)
	}
	var mediaTimes []time.Time
	for len(mediaTimes) < len(packets) {
		observation := waitTwilioWrite(t, writes)
		if observation.event == "media" {
			mediaTimes = append(mediaTimes, observation.at)
		}
	}
	for i := 1; i < len(mediaTimes); i++ {
		if spacing := mediaTimes[i].Sub(mediaTimes[i-1]); spacing < 15*time.Millisecond {
			t.Fatalf("media packets were burst: spacing=%v", spacing)
		}
	}
}

func TestTwilioAudioPacerClearPreemptsQueuedMedia(t *testing.T) {
	pacer, cancel, writes := testTwilioPacer(t)
	defer cancel()
	packets := make([]twilioAudioPacket, 6)
	for i := range packets {
		packets[i] = twilioAudioPacket{PCM: make([]int16, 160), AudioEndMS: (i + 1) * 20}
	}
	if _, err := pacer.enqueue(context.Background(), packets, realtimeBridgeControl{ItemID: "item-2"}); err != nil {
		t.Fatal(err)
	}
	for waitTwilioWrite(t, writes).event != "media" {
	}
	clearedMS, err := pacer.clear(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if clearedMS < 80 {
		t.Fatalf("cleared queue=%dms, want at least 80ms", clearedMS)
	}
	foundClear := false
	deadline := time.After(150 * time.Millisecond)
	for !foundClear {
		select {
		case observation := <-writes:
			if observation.event == "clear" {
				foundClear = true
			}
		case <-deadline:
			t.Fatal("clear did not reach Twilio promptly")
		}
	}
	select {
	case observation := <-writes:
		if observation.event == "media" {
			t.Fatal("queued media was sent after clear")
		}
	case <-time.After(60 * time.Millisecond):
	}
}

func TestTwilioAudioPacerOverflowClearsAndRemainsUsable(t *testing.T) {
	pacer, cancel, writes := testTwilioPacer(t)
	defer cancel()
	pacer.maxQueuedSamples = 159
	_, err := pacer.enqueue(context.Background(), []twilioAudioPacket{{PCM: make([]int16, 160), AudioEndMS: 20}}, realtimeBridgeControl{ItemID: "item-overflow"})
	if !errors.Is(err, errTwilioPacerOverflow) {
		t.Fatalf("overflow error=%v", err)
	}
	if observation := waitTwilioWrite(t, writes); observation.event != "clear" {
		t.Fatalf("overflow event=%q, want clear", observation.event)
	}
	pacer.maxQueuedSamples = 160
	if _, err := pacer.enqueue(context.Background(), []twilioAudioPacket{{PCM: make([]int16, 160), AudioEndMS: 20}}, realtimeBridgeControl{ItemID: "item-recovered"}); err != nil {
		t.Fatal(err)
	}
	if observation := waitTwilioWrite(t, writes); observation.event != "media" {
		t.Fatalf("post-overflow event=%q, want media", observation.event)
	}
}

func TestPCMSpeechStartDetectorRequiresSpeechAndRearms(t *testing.T) {
	detector := newPCMSpeechStartDetector()
	silence := make([]int16, 160)
	loud := make([]int16, 160)
	for i := range loud {
		loud[i] = 4000
	}
	if detector.observe(loud) || !detector.observe(loud) {
		t.Fatal("speech detector did not require exactly the configured loud-frame run")
	}
	if detector.observe(loud) {
		t.Fatal("active speech emitted a duplicate start")
	}
	for i := 0; i < detector.requiredSilence; i++ {
		if detector.observe(silence) {
			t.Fatal("silence emitted speech start")
		}
	}
	if detector.observe(loud) || !detector.observe(loud) {
		t.Fatal("speech detector did not rearm after silence")
	}
}

func TestTwilioAudioPacerSerializesConcurrentCommands(t *testing.T) {
	pacer, cancel, _ := testTwilioPacer(t)
	defer cancel()
	packet := []twilioAudioPacket{{PCM: make([]int16, 80), AudioEndMS: 10}}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = pacer.enqueue(context.Background(), packet, realtimeBridgeControl{ItemID: "item-concurrent"})
		}()
	}
	wg.Wait()
}

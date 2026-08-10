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
	packets := make([]twilioAudioPacket, 12)
	for i := range packets {
		packets[i] = twilioAudioPacket{PCM: make([]int16, 160), AudioEndMS: (i + 1) * 20}
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
	// The initial 200 ms is deliberately sent ahead to prevent carrier
	// underruns. Replenishment after that lead follows the media cadence.
	if leadSpan := mediaTimes[9].Sub(mediaTimes[0]); leadSpan > 15*time.Millisecond {
		t.Fatalf("initial carrier buffer took too long to fill: %v", leadSpan)
	}
	if firstRefill := mediaTimes[10].Sub(mediaTimes[0]); firstRefill < 15*time.Millisecond || firstRefill > 45*time.Millisecond {
		t.Fatalf("carrier buffer was not replenished on time: %v", firstRefill)
	}
}

func TestTwilioAudioPacerWriteOverheadDoesNotCausePlaybackUnderrun(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan twilioWriteObservation, 128)
	pacer := newTwilioAudioPacer(ctx, "stream-test", newTwilioPlaybackTracker(), func(payload []byte) error {
		time.Sleep(time.Millisecond)
		var envelope struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(payload, &envelope); err != nil {
			return err
		}
		writes <- twilioWriteObservation{event: envelope.Event, at: time.Now()}
		return nil
	}, nil)
	packets := make([]twilioAudioPacket, 30)
	for i := range packets {
		packets[i] = twilioAudioPacket{PCM: make([]int16, 160), AudioEndMS: (i + 1) * 20}
	}
	if _, err := pacer.enqueue(context.Background(), packets, realtimeBridgeControl{ItemID: "item-delayed"}); err != nil {
		t.Fatal(err)
	}
	var mediaTimes []time.Time
	for len(mediaTimes) < len(packets) {
		observation := waitTwilioWrite(t, writes)
		if observation.event == "media" {
			mediaTimes = append(mediaTimes, observation.at)
		}
	}
	// Thirty 20 ms packets contain 600 ms of audio. With a 200 ms carrier
	// lead, the final packet must arrive well before the first 500 ms plays.
	if elapsed := mediaTimes[len(mediaTimes)-1].Sub(mediaTimes[0]); elapsed >= 500*time.Millisecond {
		t.Fatalf("write overhead accumulated into carrier underrun: elapsed=%v", elapsed)
	}
}

func TestTwilioAudioPacerEnqueueTrafficDoesNotStarvePlayback(t *testing.T) {
	pacer, cancel, writes := testTwilioPacer(t)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 80; i++ {
			_, _ = pacer.enqueue(context.Background(), []twilioAudioPacket{{
				PCM: make([]int16, 160), AudioEndMS: (i + 1) * 20,
			}}, realtimeBridgeControl{ItemID: "item-streaming"})
			time.Sleep(time.Millisecond)
		}
	}()
	var mediaTimes []time.Time
	deadline := time.After(time.Second)
	for len(mediaTimes) < 8 {
		select {
		case observation := <-writes:
			if observation.event == "media" {
				mediaTimes = append(mediaTimes, observation.at)
			}
		case <-deadline:
			t.Fatal("streaming enqueue traffic starved Twilio playback")
		}
	}
	for i := 1; i < len(mediaTimes); i++ {
		if gap := mediaTimes[i].Sub(mediaTimes[i-1]); gap > 35*time.Millisecond {
			t.Fatalf("streaming enqueue traffic opened a carrier gap: %v", gap)
		}
	}
	<-done
}

func TestTwilioAudioPacerDoesNotRequirePlaybackMetadataForCadence(t *testing.T) {
	pacer, cancel, writes := testTwilioPacer(t)
	defer cancel()
	packets := make([]twilioAudioPacket, 12)
	for i := range packets {
		packets[i] = twilioAudioPacket{PCM: make([]int16, 160)}
	}
	if _, err := pacer.enqueue(context.Background(), packets, realtimeBridgeControl{}); err != nil {
		t.Fatal(err)
	}
	var mediaTimes []time.Time
	for len(mediaTimes) < len(packets) {
		observation := waitTwilioWrite(t, writes)
		if observation.event == "media" {
			mediaTimes = append(mediaTimes, observation.at)
		}
	}
	if firstRefill := mediaTimes[10].Sub(mediaTimes[0]); firstRefill < 15*time.Millisecond || firstRefill > 45*time.Millisecond {
		t.Fatalf("metadata-free packets lost carrier cadence: refill=%v", firstRefill)
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
	_, err := pacer.clear(context.Background())
	if err != nil {
		t.Fatal(err)
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

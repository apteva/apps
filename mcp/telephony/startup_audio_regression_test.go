package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

type startupCarrierWrite struct {
	data []byte
	at   time.Time
}

func TestHumanPacerStartupBatchUsesExistingLeadWithoutDiscard(t *testing.T) {
	for _, sendAhead := range []int{20, 40, 60, 80} {
		for _, provider := range []string{"telnyx", "twilio"} {
			t.Run(fmt.Sprintf("%s/lead_%d", provider, sendAhead), func(t *testing.T) {
				policy := humanCarrierPacerPolicy(nil)
				policy.bufferMS = sendAhead
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				writes := make(chan startupCarrierWrite, 32)
				write := func(data []byte) error {
					writes <- startupCarrierWrite{data: append([]byte(nil), data...), at: time.Now()}
					return nil
				}
				var queued, dropped int
				var err error
				if provider == "telnyx" {
					p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream-test", false, newTwilioPlaybackTracker(), policy, write, nil, nil)
					defer func() { cancel(); <-p.done }()
					packets := make([]carrierPacedPacket, 10)
					for i := range packets {
						pcm := make([]int16, 320)
						for j := range pcm {
							pcm[j] = int16(i + 1)
						}
						packets[i].PCM = pcm
					}
					queued, dropped, err = p.enqueue(ctx, packets)
				} else {
					p := newTwilioAudioPacerWithPolicy(ctx, "stream-test", newTwilioPlaybackTracker(), policy, write, nil)
					defer func() { cancel(); <-p.done }()
					packets := make([]twilioAudioPacket, 10)
					for i := range packets {
						packets[i].PCM = make([]int16, 160)
					}
					queued, dropped, err = p.enqueueWithDiagnostics(ctx, packets, realtimeBridgeControl{})
				}
				if err != nil || dropped != 0 || queued > 180 {
					t.Fatalf("200ms startup batch: queued=%d dropped=%d err=%v", queued, dropped, err)
				}
				deadline := time.After(time.Second)
				var firstWrite time.Time
				for i := 0; i < 10; i++ {
					select {
					case observation := <-writes:
						data := observation.data
						if i == 0 {
							firstWrite = observation.at
						}
						if i == sendAhead/20 && observation.at.Sub(firstWrite) < 10*time.Millisecond {
							t.Fatalf("carrier lead exceeded %dms: refill at %v", sendAhead, observation.at.Sub(firstWrite))
						}
						var f struct {
							Event string `json:"event"`
							Media struct {
								Payload string `json:"payload"`
							} `json:"media"`
						}
						if err := json.Unmarshal(data, &f); err != nil {
							t.Fatal(err)
						}
						payload, err := base64.StdEncoding.DecodeString(f.Media.Payload)
						if err != nil || f.Event != "media" || len(payload) == 0 {
							t.Fatalf("invalid carrier media: %s", data)
						}
						if provider == "telnyx" {
							pcm := bytesToPCM16(payload)
							for _, sample := range pcm {
								if sample != int16(i+1) {
									t.Fatalf("startup PCM changed or reordered: frame=%d sample=%d", i, sample)
								}
							}
						}
					case <-deadline:
						t.Fatalf("only %d/10 startup frames delivered", i)
					}
				}
			})
		}
	}
}

func TestNormalLocalCloseWriteFailureHasSeparateDiagnostics(t *testing.T) {
	server, peer := net.Pipe()
	_ = peer.Close()
	p := newWebSocketWriterPump(server, ws.StateServerSide)
	newGracefulWebSocket(server, p).Close(ws.StatusNormalClosure, "call completed")
	<-p.done
	s := p.audioSnapshot().Transport
	if s.CleanupWriteErrors != 1 || s.WriteErrors != 0 || s.WriteTimeouts != 0 || len(s.Events) != 1 || s.Events[0].Reason != "local_close_write_error" || !s.Events[0].LocalShutdown || s.Events[0].Opcode != 8 {
		t.Fatalf("normal cleanup classified as live failure: %+v", s)
	}
}

func TestHumanPacerStartupLeadStillExpiresOldSamples(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	writes := make(chan []byte, 32)
	p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream-test", false, newTwilioPlaybackTracker(), humanCarrierPacerPolicy(nil), func(data []byte) error { writes <- data; return nil }, nil, nil)
	defer func() { cancel(); <-p.done }()
	packets := make([]carrierPacedPacket, 10)
	for i := range packets {
		packets[i].PCM = make([]int16, 320)
		if i < 5 {
			packets[i].EnqueuedAt = time.Now().Add(-time.Second)
		}
	}
	queued, dropped, err := p.enqueue(ctx, packets)
	if err != nil || dropped != 100 || queued > 100 {
		t.Fatalf("old startup batch queued=%d dropped=%d err=%v", queued, dropped, err)
	}
	events := p.dropEvents()
	if len(events) != 5 {
		t.Fatalf("old audio not accounted: %+v", events)
	}
	for _, e := range events {
		if e.Reason != "live_audio_age_limit" || e.DurationMS != 20 {
			t.Fatalf("bad expiry event: %+v", e)
		}
	}
	for i := 0; i < 5; i++ {
		select {
		case <-writes:
		case <-time.After(time.Second):
			t.Fatal("fresh audio was not preserved")
		}
	}
}

func TestProtocolCloseFailureRemainsAnError(t *testing.T) {
	server, peer := net.Pipe()
	_ = peer.Close()
	p := newWebSocketWriterPump(server, ws.StateServerSide)
	if err := p.Write(ws.OpClose, ws.NewCloseFrameBody(ws.StatusProtocolError, "bad frame")); err == nil {
		t.Fatal("expected write failure")
	}
	<-p.done
	s := p.audioSnapshot().Transport
	if s.WriteErrors != 1 || s.CleanupWriteErrors != 0 || len(s.Events) != 1 || s.Events[0].Reason != "socket_write_error" {
		t.Fatalf("unexpected close failure hidden: %+v", s)
	}
}

func TestLocalCloseTimeoutHasSeparateDiagnostics(t *testing.T) {
	server, peer := net.Pipe()
	defer peer.Close()
	p := newWebSocketWriterPump(server, ws.StateServerSide)
	newGracefulWebSocket(server, p).Close(ws.StatusNormalClosure, "call completed")
	<-p.done
	s := p.audioSnapshot().Transport
	if s.CleanupWriteErrors != 1 || s.CleanupWriteTimeouts != 1 || s.WriteErrors != 0 || s.WriteTimeouts != 0 || len(s.Events) != 1 || s.Events[0].Reason != "local_close_write_timeout" {
		t.Fatalf("cleanup timeout classified as live error: %+v", s)
	}
	merged := mergeWebsocketTransport(s, s)
	if merged.CleanupWriteErrors != 2 || merged.CleanupWriteTimeouts != 2 || len(merged.Events) != 2 {
		t.Fatalf("cleanup evidence lost on merge: %+v", merged)
	}
}

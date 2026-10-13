package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"net"
	"os"
	"testing"
	"time"
)

func TestSoftphoneStartupFirstWordsSurviveAttachment(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		for _, transport := range []string{"websocket", "webrtc_decoded_pcm"} {
			t.Run(direction+"/"+transport, func(t *testing.T) {
				browser := &websocketWriterPump{}
				h := &softphoneHub{browser: browser, direction: direction, status: "answered"}
				// The shared handoff accepts the exact PCM produced by either transport.
				// Every distinguishable opening frame must emerge once, in source order.
				voice, err := os.ReadFile("testdata/startup/opening.pcm")
				if err != nil {
					t.Fatal(err)
				}
				if transport == "webrtc_decoded_pcm" {
					enc, e := newRTCOpusEncoderWithFEC(32000, true)
					if e != nil {
						t.Fatal(e)
					}
					defer enc.Close()
					dec, e := newRTCOpusDecoderWithFEC(true)
					if e != nil {
						t.Fatal(e)
					}
					defer dec.Close()
					var decoded []byte
					for off := 0; off < 6*960; off += 960 {
						input := make([]float32, 960)
						for n := 0; n < 480; n++ {
							v := float32(int16(binary.LittleEndian.Uint16(voice[off+n*2:]))) / 32768
							input[n*2] = v
							input[n*2+1] = v
						}
						packet := make([]byte, 1275)
						size, e := enc.EncodeFloat32(input, packet)
						if e != nil {
							t.Fatal(e)
						}
						pcm := make([]int16, 480)
						n, e := dec.decode(packet[:size], pcm)
						if e != nil || n != 480 {
							t.Fatalf("decode %d %v", n, e)
						}
						decoded = append(decoded, pcm16ToBytes(pcm)...)
					}
					voice = decoded
				}
				speech := make([][]byte, 3)
				for i := range speech {
					speech[i] = append([]byte(nil), voice[i*960:(i+1)*960]...)
					h.forwardMicrophone(browser, speech[i])
				}
				a, b := net.Pipe()
				defer a.Close()
				defer b.Close()
				w := newWebSocketWriterPump(a, ws.StateServerSide)
				defer w.Stop()
				h.setPeer(w)
				for _, want := range speech {
					b.SetReadDeadline(time.Now().Add(time.Second))
					got, op, err := wsutil.ReadServerData(b)
					if err != nil || op != ws.OpBinary || !bytes.Equal(got, want) {
						t.Fatalf("opening speech changed/lost: op=%v err=%v", op, err)
					}
				}
				if h.startup.ForwardedMS != 60 || h.startup.DroppedMS != 0 || h.startup.FirstForwardedAt == "" {
					t.Fatalf("startup %+v", h.startup)
				}
			})
		}
	}
}
func TestSoftphoneStartupBoundsAndOwnership(t *testing.T) {
	now := time.Now()
	frame := make([]byte, 960)
	t.Run("bounded burst and expiry", func(t *testing.T) {
		h := &softphoneHub{status: "answered"}
		for i := 0; i < 100; i++ {
			h.bufferStartupMicrophoneLocked(frame, now)
		}
		if len(h.pendingMicrophone) != 6 || h.startup.DroppedMS != 1880 {
			t.Fatalf("unbounded: %+v", h.startup)
		}
		h.flushStartupMicrophoneLocked(now.Add(startupAudioLimit + time.Millisecond))
		if len(h.pendingMicrophone) != 0 || h.startup.DroppedMS != 2000 {
			t.Fatal("stale opening speech retained")
		}
	})
	for _, action := range []string{"hold", "end", "replace", "unanswered"} {
		t.Run(action, func(t *testing.T) {
			old := &websocketWriterPump{}
			h := &softphoneHub{status: "answered", browser: old}
			h.forwardMicrophone(old, frame)
			switch action {
			case "hold":
				h.setHeld(true)
			case "end":
				h.setCallState("", "completed")
			case "replace":
				h.setBrowser(&websocketWriterPump{})
			case "unanswered":
				h.setCallState("", "ringing")
			}
			if len(h.pendingMicrophone) != 0 || h.startup.DroppedMS != 20 {
				t.Fatalf("audio survived %s", action)
			}
		})
	}
	t.Run("ringing never replayed", func(t *testing.T) {
		b := &websocketWriterPump{}
		h := &softphoneHub{status: "ringing", browser: b}
		h.forwardMicrophone(b, frame)
		h.setCallState("outbound", "answered")
		if len(h.pendingMicrophone) != 0 || h.preAnswerDroppedMS() != 20 || h.startup.LastPreAnswerAt == "" {
			t.Fatal("ringing gate")
		}
		h.forwardMicrophone(&websocketWriterPump{}, frame)
		if len(h.pendingMicrophone) != 0 {
			t.Fatal("replaced browser injected audio")
		}
	})
}
func TestSIPStartupCodecEvidence(t *testing.T) {
	cfg := directSIPTestConfig()
	for _, codec := range []string{"G722", "PCMU", "PCMA"} {
		payload := map[string]uint8{"G722": 9, "PCMU": 0, "PCMA": 8}[codec]
		body, err := buildSIPMediaAnswer(cfg, sipMediaOffer{Codec: codec, PayloadType: payload, DTMFPayloadType: 101}, 20400, nil)
		if err != nil {
			t.Fatal(err)
		}
		e := codecEvidence(body, []string{"G722", "PCMU", "PCMA"}, codec, "outbound_final_answer")
		if e == nil || len(e.Remote) != 2 || e.Remote[0].Codec != codec || e.LocalPreference[0] != "G722" {
			t.Fatalf("evidence %+v", e)
		}
		raw, _ := json.Marshal(e)
		if bytes.Contains(raw, []byte(cfg.PublicHost)) && cfg.PublicHost != "" {
			t.Fatal("SDP address leaked")
		}
	}
}

func TestSoftphoneStartupReadinessEvents(t *testing.T) {
	w := &websocketWriterPump{requests: make(chan websocketWriteRequest, 16)}
	w.setDiagnosticID("browser-startup")
	h := &softphoneHub{callID: "call", browser: w, status: "ringing"}
	h.markReady(w)
	h.setCallState("outbound", "answered")
	h.setPeer(&websocketWriterPump{})
	h.publishReadiness() // no duplicate
	h.clearPeer(h.peer)
	h.setPeer(&websocketWriterPump{})
	h.setHeld(true)
	h.setHeld(false)
	expected := []string{"call_not_answered", "carrier_not_attached", "ready", "carrier_not_attached", "ready", "hold", "ready"}
	for i, reason := range expected {
		select {
		case req := <-w.requests:
			var e mediaReadinessEvent
			if err := json.Unmarshal(req.payload, &e); err != nil {
				t.Fatal(err)
			}
			if e.Reason != reason || e.Revision != uint64(i+1) || e.ConnectionID != "browser-startup" || e.CallID != "call" || e.At == "" {
				t.Fatalf("event %+v", e)
			}
			if (e.Type == "media.ready") != (reason == "ready") {
				t.Fatalf("wrong ready event %+v", e)
			}
		default:
			t.Fatal("missing readiness event")
		}
	}
	if len(w.requests) != 0 {
		t.Fatal("duplicate readiness")
	}
}
func TestSoftphoneStartupReadinessNeverBlocksAudio(t *testing.T) {
	w := &websocketWriterPump{requests: make(chan websocketWriteRequest, 1)}
	w.requests <- websocketWriteRequest{}
	h := &softphoneHub{callID: "call", browser: w, readyBrowser: w, status: "answered", peer: &websocketWriterPump{}}
	done := make(chan struct{})
	go func() { h.publishReadiness(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("observer blocked audio")
	}
	<-w.requests
	h.publishReadiness()
	if len(w.requests) != 1 {
		t.Fatal("state not retried")
	}
}

func TestSoftphoneAttachmentDropPhases(t *testing.T) {
	for _, direction := range []string{"inbound", "outbound"} {
		t.Run(direction, func(t *testing.T) {
			now := time.Now()
			w := &websocketWriterPump{}
			w.setDiagnosticID("browser-one")
			h := &softphoneHub{direction: direction, status: "answered", browser: w}
			frame := make([]byte, 960)
			// A late initial frame and a fresh one share an attachment. Consumption
			// must not relabel the expired frame as a later reconnection.
			h.bufferStartupMicrophoneLocked(frame, now)
			h.flushStartupMicrophoneLocked(now.Add(time.Second))
			h.startup.FirstForwardedAt = ringTime(now.Add(2 * time.Second))
			h.bufferStartupMicrophoneLocked(frame, now.Add(time.Hour))
			h.flushStartupMicrophoneLocked(now.Add(time.Hour + time.Second))
			h.bufferStartupMicrophoneLocked(frame, now.Add(2*time.Hour))
			h.status = "completed"
			h.clearStartupMicrophoneLocked("call_not_answered", now.Add(2*time.Hour))
			h.status = "answered"
			h.bufferStartupMicrophoneLocked(frame, now.Add(3*time.Hour))
			h.held = true
			h.clearStartupMicrophoneLocked("hold", now.Add(3*time.Hour))
			got := h.serverAudioSnapshot().Startup
			if got.PhaseSchema != 1 || got.DroppedMS != 80 || got.InitialDroppedMS != 20 || got.ReconnectDroppedMS != 20 || got.EndingDroppedMS != 20 || got.HoldDroppedMS != 20 {
				t.Fatalf("phases: %+v", got)
			}
			for i, phase := range []string{"initial", "reconnect", "ending", "hold"} {
				if got.Events[i].Phase != phase || got.Events[i].ConnectionID != "browser-one" {
					t.Fatalf("event: %+v", got.Events[i])
				}
			}
			// API summary exposes only measured phase totals, never infers old rows.
			raw, _ := json.Marshal(browserAudioDiagnostics{Server: &serverAudioDiagnostics{Startup: got}})
			summary := summarizeAudioDashboard(&callRow{BrowserAudioDiagnostics: string(raw)})
			if summary.Metrics["microphone_initial_dropped_ms"] != 20 || summary.Metrics["microphone_ending_dropped_ms"] != 20 {
				t.Fatal(summary.Metrics)
			}
			raw, _ = json.Marshal(browserAudioDiagnostics{Server: &serverAudioDiagnostics{Startup: microphoneStartup{DroppedMS: 80}}})
			legacy := summarizeAudioDashboard(&callRow{BrowserAudioDiagnostics: string(raw)})
			if _, fabricated := legacy.Metrics["microphone_initial_dropped_ms"]; fabricated {
				t.Fatal("fabricated legacy phase")
			}
		})
	}
}

func TestSoftphoneAttachmentPhasesRemainBounded(t *testing.T) {
	h := &softphoneHub{status: "answered"}
	now := time.Now()
	for i := 0; i < 1000; i++ {
		h.bufferStartupMicrophoneLocked(make([]byte, 960), now)
	}
	if len(h.startup.Events) != 16 || len(h.pendingMicrophone) != 6 {
		t.Fatal("unbounded phase collection")
	}
	h.startup.FirstForwardedAt = ringTime(now)
	// Buffered samples retain initial attribution after another frame starts.
	h.flushStartupMicrophoneLocked(now.Add(time.Second))
	if h.startup.InitialDroppedMS != 20000 || h.startup.ReconnectDroppedMS != 0 {
		t.Fatal(h.startup)
	}
}

func BenchmarkSoftphoneAttachmentDropDiagnostics(b *testing.B) {
	h := &softphoneHub{status: "answered", browser: &websocketWriterPump{}}
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.startupDropLocked("attachment_expired", 960, now)
	}
}

func TestSoftphoneAttachmentAfterReadyButMutedIsReconnection(t *testing.T) {
	w := &websocketWriterPump{requests: make(chan websocketWriteRequest, 4)}
	h := &softphoneHub{status: "answered", browser: w, readyBrowser: w, peer: &websocketWriterPump{}}
	h.publishReadiness()
	h.clearPeer(h.peer)
	if h.startup.FirstForwardedAt != "" {
		t.Fatal("fixture must not contain microphone speech")
	}
	h.bufferStartupMicrophoneLocked(make([]byte, 960), time.Now())
	h.flushStartupMicrophoneLocked(time.Now().Add(time.Second))
	if h.startup.InitialDroppedMS != 0 || h.startup.ReconnectDroppedMS != 20 {
		t.Fatal("muted established call mislabeled startup", h.startup)
	}
}

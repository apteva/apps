package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/gobwas/ws"
)

func idleAudioWriter() *websocketWriterPump {
	return &websocketWriterPump{audio: make(chan websocketWriteRequest, 6), requests: make(chan websocketWriteRequest, 64), done: make(chan struct{}), stop: make(chan struct{})}
}
func TestLiveAudioDurationAgeAndExactDropAccounting(t *testing.T) {
	p := idleAudioWriter()
	// Six variable packets are NOT automatically 120ms. Keep only the latest
	// 120ms even when a provider coalesces seconds of PCM in one message.
	p.QueueAudio(make([]byte, 48000))
	s := p.audioSnapshot()
	if s.QueuedMS != 120 || s.OverflowBytes != 42240 {
		t.Fatalf("oversized: %+v", s)
	}
	p.FlushAudio()
	s = p.audioSnapshot()
	if s.FlushedBytes != 5760 || s.QueuedMS != 0 {
		t.Fatalf("flush: %+v", s)
	}
	p.QueueAudio(make([]byte, 960))
	old := <-p.audio
	old.enqueued = time.Now().Add(-time.Second)
	p.audio <- old
	expected := bytes.Repeat([]byte{2, 7}, 480)
	p.QueueAudio(expected)
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	p.conn = server
	p.state = ws.StateServerSide
	go p.run()
	defer p.Stop()
	client.SetReadDeadline(time.Now().Add(time.Second))
	frame, err := ws.ReadFrame(client)
	if err != nil || !bytes.Equal(frame.Payload, expected) {
		t.Fatalf("stale frame replayed: %v", err)
	}
	// Barrier: writer completes accounting before this control response.
	done := make(chan error, 1)
	go func() { done <- p.Write(ws.OpText, []byte("barrier")) }()
	if _, err = ws.ReadFrame(client); err != nil {
		t.Fatal(err)
	}
	if err = <-done; err != nil {
		t.Fatal(err)
	}
	s = p.audioSnapshot()
	if s.StaleBytes != 960 || s.SentBytes != 960 || s.EnqueuedBytes != s.StaleBytes+s.SentBytes+s.OverflowBytes+s.FlushedBytes {
		t.Fatalf("accounting: %+v", s)
	}
}

type signaledWriteConn struct {
	net.Conn
	started chan struct{}
	once    sync.Once
}

func (c *signaledWriteConn) Write(b []byte) (int, error) {
	c.once.Do(func() { close(c.started) })
	return c.Conn.Write(b)
}

func TestLiveAudioBlockedPeerDoesNotBlockHoldOrOtherDirection(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()
	started := make(chan struct{})
	writer := newWebSocketWriterPump(&signaledWriteConn{Conn: server, started: started}, ws.StateServerSide)
	defer writer.Stop()
	writer.QueueAudio(make([]byte, 960))
	<-started
	browser := idleAudioWriter()
	h := &softphoneHub{peer: writer, browser: browser, status: "answered"}
	finished := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			h.forwardMicrophone(browser, make([]byte, 960))
		}
		h.toBrowser(ws.OpBinary, make([]byte, 960))
		h.setHeld(true)
		close(finished)
	}()
	select {
	case <-finished:
	case <-time.After(100 * time.Millisecond):
		t.Fatal("network I/O blocked hub state/opposite direction")
	}
	if len(browser.audio) != 0 || len(writer.audio) != 0 {
		t.Fatal("hold did not flush both directions")
	}
	select {
	case <-writer.done:
	case <-time.After(time.Second):
		t.Fatal("wedged audio write exceeded deadline")
	}
	if writer.audioSnapshot().WriteErrors != 1 {
		t.Fatal("missing stalled write diagnostic")
	}
}

func TestLiveAudioCaptureOwnershipAnswerAndHoldGates(t *testing.T) {
	peer, old, current := idleAudioWriter(), idleAudioWriter(), idleAudioWriter()
	h := &softphoneHub{peer: peer, browser: current, status: "pending"}
	h.forwardMicrophone(current, make([]byte, 960))
	if len(peer.audio) != 0 || h.preAnswerDroppedMS() != 20 {
		t.Fatal("pre-answer microphone escaped")
	}
	h.setCallState("inbound", "answered")
	h.forwardMicrophone(old, make([]byte, 960))
	if len(peer.audio) != 0 {
		t.Fatal("replaced browser injected speech")
	}
	h.forwardMicrophone(current, make([]byte, 960))
	if len(peer.audio) != 1 {
		t.Fatal("current browser audio lost")
	}
	h.setHeld(true)
	h.forwardMicrophone(current, make([]byte, 960))
	if len(peer.audio) != 0 {
		t.Fatal("held audio escaped")
	}
	h.setHeld(false)
	h.forwardMicrophone(current, make([]byte, 960))
	if len(peer.audio) != 1 {
		t.Fatal("unhold did not restore audio")
	}
}

func TestLiveAudioConcurrentBidirectionalPCMIsUnchanged(t *testing.T) {
	// Independent full-duplex calls share the process/scheduler but never audio.
	const calls = 24
	var wg sync.WaitGroup
	for call := 0; call < calls; call++ {
		wg.Add(1)
		go func(call int) {
			defer wg.Done()
			a, b := net.Pipe()
			c, d := net.Pipe()
			defer a.Close()
			defer b.Close()
			defer c.Close()
			defer d.Close()
			peer := newWebSocketWriterPump(a, ws.StateServerSide)
			browser := newWebSocketWriterPump(c, ws.StateServerSide)
			defer peer.Stop()
			defer browser.Stop()
			h := &softphoneHub{peer: peer, browser: browser, status: "answered"}
			var directions sync.WaitGroup
			for direction, reader := range []net.Conn{b, d} {
				directions.Add(1)
				go func(direction int, reader net.Conn) {
					defer directions.Done()
					for frame := 0; frame < 100; frame++ {
						pcm := make([]byte, 960)
						for i := 0; i < len(pcm)/2; i++ {
							binary.LittleEndian.PutUint16(pcm[2*i:], uint16(call*123+frame+i+direction*1000))
						}
						if direction == 0 {
							h.forwardMicrophone(browser, pcm)
						} else {
							h.toBrowser(ws.OpBinary, pcm)
						}
						reader.SetReadDeadline(time.Now().Add(time.Second))
						wire, err := ws.ReadFrame(reader)
						if err != nil || !bytes.Equal(wire.Payload, pcm) {
							t.Errorf("call %d direction %d frame %d changed/lost: %v", call, direction, frame, err)
							return
						}
					}
				}(direction, reader)
			}
			directions.Wait()
			for _, p := range []*websocketWriterPump{peer, browser} {
				s := p.audioSnapshot()
				if s.OverflowBytes+s.StaleBytes+s.FailedBytes != 0 {
					t.Errorf("unexpected audio loss: %+v", s)
				}
			}
		}(call)
	}
	wg.Wait()
}

func TestLiveAudioNegotiatedEnvelopeKeepsPCMAndTimes(t *testing.T) {
	pcm := []byte{1, 2, 3, 4}
	f := encodePlaybackFrame(pcm, 123)
	decoded, seq, framed := decodeSoftphoneAudioFrame(f)
	if !framed || seq != 123 || !bytes.Equal(pcm, decoded) {
		t.Fatal("framing changed PCM")
	}
	binary.LittleEndian.PutUint64(f[8:], math.Float64bits(700))
	binary.LittleEndian.PutUint64(f[16:], math.Float64bits(10000))
	binary.LittleEndian.PutUint64(f[24:], math.Float64bits(45))
	h := &softphoneHub{}
	h.observeCaptureTiming(f)
	binary.LittleEndian.PutUint64(f[16:], math.Float64bits(9000))
	h.observeCaptureTiming(f)
	s := h.serverAudioSnapshot()
	if s.CaptureTimestampMS != 700 || s.CaptureWorkerAgeMS != 45 || s.CaptureTransitExcessMS < 999 {
		t.Fatalf("timing discarded: %+v", s)
	}
}

func TestLiveAudioDiagnosticsPersistWithoutErasingBrowserMeasurements(t *testing.T) {
	softphoneTestCtx(t)
	app := &App{}
	row := insertSoftphoneCall(t, app, "in-progress")
	if err := app.db().updateBrowserAudioDiagnostics(row.ID, browserAudioDiagnostics{PlaybackDroppedMS: 25}); err != nil {
		t.Fatal(err)
	}
	h := &softphoneHub{}
	h.timeline.observe("carrier_media_read", 100, time.Time{}, "123", "4")
	if err := app.db().updateServerAudioDiagnostics(row.ID, h.serverAudioSnapshot()); err != nil {
		t.Fatal(err)
	}
	var raw string
	if err := app.db().db.QueryRow(`SELECT browser_audio_diagnostics FROM calls WHERE id=?`, row.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var got browserAudioDiagnostics
	if err := json.Unmarshal([]byte(raw), &got); err != nil {
		t.Fatal(err)
	}
	if got.PlaybackDroppedMS != 25 || got.Server == nil || got.Server.Stages["carrier_media_read"].Frames != 1 {
		t.Fatalf("diagnostics lost: %s", raw)
	}
}

func TestLiveAudioPacersExpireOldSpeechButPreserveBufferedAI(t *testing.T) {
	for _, human := range []bool{true, false} {
		t.Run(map[bool]string{true: "human", false: "AI"}[human], func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := bufferedCarrierPacerPolicy()
			if human {
				policy = humanCarrierPacerPolicy(nil)
			}
			var writes int
			p := newJSONCarrierAudioPacer(ctx, 16000, carrierCodecL16_16, "telnyx", "stream", false, newTwilioPlaybackTracker(), policy, func([]byte) error { writes++; return nil }, nil, nil)
			_, dropped, err := p.enqueue(ctx, []carrierPacedPacket{{PCM: make([]int16, 320), EnqueuedAt: time.Now().Add(-time.Second)}})
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			<-p.done
			if human && (writes != 0 || dropped != 20) {
				t.Fatalf("old human speech sent: writes=%d dropped=%d", writes, dropped)
			}
			if !human && (writes != 1 || dropped != 0) {
				t.Fatal("buffered AI policy changed")
			}
			var twWrites int
			ctx2, cancel2 := context.WithCancel(context.Background())
			defer cancel2()
			tw := newTwilioAudioPacerWithPolicy(ctx2, "stream", newTwilioPlaybackTracker(), policy, func([]byte) error { twWrites++; return nil }, nil)
			result := tw.command(ctx2, twilioPacerCommand{packets: []twilioPacedPacket{{PCM: make([]int16, 160), EnqueuedAt: time.Now().Add(-time.Second)}}})
			cancel2()
			<-tw.done
			if result.err != nil {
				t.Fatal(result.err)
			}
			if human && (twWrites != 0 || result.droppedMS != 20) {
				t.Fatal("Twilio stale speech sent")
			}
			if !human && (twWrites != 1 || result.droppedMS != 0) {
				t.Fatal("Twilio AI policy changed")
			}
		})
	}
}

func TestLiveAudioSIPPacerExpiresAgeWithoutChangingAIPolicy(t *testing.T) {
	for _, human := range []bool{true, false} {
		t.Run(map[bool]string{true: "human", false: "AI"}[human], func(t *testing.T) {
			sender, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			receiver, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			policy := bufferedCarrierPacerPolicy()
			if human {
				policy = humanCarrierPacerPolicy(nil)
			}
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMU"}}
			p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, policy, nil)
			old, fresh := bytes.Repeat([]byte{0x21}, 160), bytes.Repeat([]byte{0x7f}, 160)
			if _, err = p.enqueue([]sipRTPOutboundPacket{{payload: old, enqueuedAt: time.Now().Add(-time.Second)}, {payload: fresh}}); err != nil {
				t.Fatal(err)
			}
			receiver.SetReadDeadline(time.Now().Add(time.Second))
			wire := make([]byte, 1500)
			n, _, err := receiver.ReadFromUDP(wire)
			if err != nil {
				t.Fatal(err)
			}
			cancel()
			<-p.done
			want := old
			if human {
				want = fresh
			}
			if n != 172 || !bytes.Equal(wire[12:n], want) {
				t.Fatalf("SIP stale/good frame selection differs: human=%t len=%d", human, n)
			}
			if human && p.diagnostics.snapshot().DroppedMS != 20 {
				t.Fatal("SIP age loss missing from cumulative diagnostic")
			}
			if !human && p.droppedMS() != 0 {
				t.Fatal("AI speech expired")
			}
		})
	}
}

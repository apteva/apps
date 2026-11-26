package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"net"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v4"
)

// coachRTCClient plays a browser's coaching uplink: one send-only Opus track
// that carries silence, or a 1700 Hz tone while the test holds the microphone
// on. RTP timestamps follow the wall clock, as a running Web Audio track does.
type coachRTCClient struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticRTP
	tone  atomic.Bool
	hold  atomic.Bool
	mu    sync.Mutex
	held  []*rtp.Packet
	done  chan struct{}
}

func newCoachRTCClient(t *testing.T) *coachRTCClient {
	t.Helper()
	api, err := softphoneRTCAPI(softphoneRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	track, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypeOpus, ClockRate: 48000, Channels: 2}, "coach", "coach")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = pc.AddTransceiverFromTrack(track, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionSendonly}); err != nil {
		t.Fatal(err)
	}
	c := &coachRTCClient{pc: pc, track: track, done: make(chan struct{})}
	t.Cleanup(func() { close(c.done); pc.Close() })
	return c
}

func (c *coachRTCClient) offer(t *testing.T) string {
	t.Helper()
	offer, err := c.pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(c.pc)
	if err = c.pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("coach gather timeout")
	}
	return c.pc.LocalDescription().SDP
}

func (c *coachRTCClient) connect(t *testing.T, answer string) {
	t.Helper()
	if err := c.pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for c.pc.ConnectionState() != webrtc.PeerConnectionStateConnected {
		if time.Now().After(deadline) {
			t.Fatal("coach uplink did not connect", c.pc.ConnectionState())
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// send streams 20 ms Opus packets until the test ends. Packets made while hold
// is set are kept back, to be delivered late by flushHeld.
func (c *coachRTCClient) send(t *testing.T) {
	t.Helper()
	encoder, err := newRTCOpusEncoder(32000)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		defer encoder.Close()
		start := time.Now()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		pcm, out := make([]float32, 960), make([]byte, 1500)
		var frames uint32
		seq := uint16(1000)
		for {
			select {
			case <-c.done:
				return
			case <-ticker.C:
			}
			for due := uint32(time.Since(start) / (20 * time.Millisecond)); frames < due; frames++ {
				tone := c.tone.Load()
				for i := range pcm {
					pcm[i] = 0
					if tone {
						pcm[i] = float32(0.3 * math.Sin(2*math.Pi*1700*float64(int(frames)*960+i)/48000))
					}
				}
				n, e := encoder.EncodeFloat32(pcm, out)
				if e != nil {
					continue
				}
				p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 111, SequenceNumber: seq, Timestamp: 90000 + frames*960, SSRC: 1}, Payload: append([]byte(nil), out[:n]...)}
				seq++
				if c.hold.Load() {
					c.mu.Lock()
					c.held = append(c.held, p)
					c.mu.Unlock()
					continue
				}
				_ = c.track.WriteRTP(p)
			}
		}
	}()
}

func (c *coachRTCClient) flushHeld() int {
	c.mu.Lock()
	held := c.held
	c.held = nil
	c.mu.Unlock()
	for _, p := range held {
		_ = c.track.WriteRTP(p)
	}
	return len(held)
}

// coachRTCEvents reads the listener socket's text events in the background.
func coachRTCEvents(socket net.Conn) <-chan map[string]any {
	events := make(chan map[string]any, 1024)
	go func() {
		defer close(events)
		for {
			data, op, err := wsutil.ReadServerData(socket)
			if err != nil {
				return
			}
			var event map[string]any
			if op == ws.OpText && json.Unmarshal(data, &event) == nil {
				select {
				case events <- event:
				default:
				}
			}
		}
	}()
	return events
}

func waitCoachRTCEvent(t *testing.T, events <-chan map[string]any, kind string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	timeout := time.After(4 * time.Second)
	for {
		select {
		case event, ok := <-events:
			if !ok {
				t.Fatalf("listener closed waiting for %s", kind)
			}
			if event["type"] == kind && (match == nil || match(event)) {
				return event
			}
		case <-timeout:
			t.Fatalf("timed out waiting for %s", kind)
		}
	}
}

func sendCoachRTC(t *testing.T, socket net.Conn, value map[string]any) {
	t.Helper()
	data, _ := json.Marshal(value)
	if err := wsutil.WriteClientText(socket, data); err != nil {
		t.Fatal(err)
	}
}

func eventNumber(event map[string]any, key string) uint32 {
	value, _ := event[key].(float64)
	return uint32(value)
}

// whisperLog records the adviser's coaching audio: arrival time and loudness.
type whisperLog struct {
	mu     sync.Mutex
	frames []whisperFrame
}
type whisperFrame struct {
	at    time.Time
	rms   float64
	epoch uint32
}

func recordWhisper(t *testing.T, frames <-chan coachingWireFrame) *whisperLog {
	log := &whisperLog{}
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })
	go func() {
		for {
			select {
			case <-done:
				return
			case f := <-frames:
				if f.op != ws.OpBinary || len(f.data) < 17 || binary.LittleEndian.Uint32(f.data) != coachPlaybackMagic {
					continue
				}
				log.mu.Lock()
				log.frames = append(log.frames, whisperFrame{time.Now(), rmsPCM(ulawToPCM16(f.data[16:])), binary.LittleEndian.Uint32(f.data[4:])})
				log.mu.Unlock()
			}
		}
	}()
	return log
}

// firstTone waits for the first loud frame that arrived after since.
func (w *whisperLog) firstTone(since time.Time, timeout time.Duration) (time.Time, bool) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		for _, f := range w.frames {
			if f.at.After(since) && f.rms > 1000 {
				w.mu.Unlock()
				return f.at, true
			}
		}
		w.mu.Unlock()
		time.Sleep(2 * time.Millisecond)
	}
	return time.Time{}, false
}

func (w *whisperLog) tonesBetween(from, to time.Time) int {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := 0
	for _, f := range w.frames {
		if f.at.After(from) && f.at.Before(to) && f.rms > 1000 {
			n++
		}
	}
	return n
}

func openRTCCoaching(t *testing.T) (*callAudioTap, *callListener, net.Conn, <-chan map[string]any, *whisperLog, <-chan coachingWireFrame) {
	t.Helper()
	app, row, _, tap, _, browser, carrier := coachingFixture(t, true)
	session := issueCoach(t, app, row)
	server := listenerServer(t, app)
	socket, _, _ := connectRTCListenerTest(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42")+"?transport=webrtc")
	tap.mu.Lock()
	listener := tap.listeners[phoneHash(session.SessionToken)]
	tap.mu.Unlock()
	return tap, listener, socket, coachRTCEvents(socket), recordWhisper(t, browser), carrier
}

func TestCoachingWebRTCOneUplinkCarriesEveryPress(t *testing.T) {
	tap, _, socket, events, whisper, carrier := openRTCCoaching(t)
	observer, _ := tap.add("observer", 4)

	uplink := newCoachRTCClient(t)
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 1, "sdp": uplink.offer(t)})
	answer := waitCoachRTCEvent(t, events, "coach.webrtc.answer", nil)
	if eventNumber(answer, "connection") != 1 {
		t.Fatalf("answer for the wrong connection: %v", answer)
	}
	uplink.connect(t, answer["sdp"].(string))
	uplink.send(t)
	// Silence before any press reaches nobody.
	time.Sleep(200 * time.Millisecond)

	const presses = 30
	var latencies []time.Duration
	for i := uint32(0); i < presses; i++ {
		start, stop := 2*i+1, 2*i+2
		sendCoachRTC(t, socket, map[string]any{"type": "coach.start", "generation": start})
		waitCoachRTCEvent(t, events, "coach.started", func(e map[string]any) bool { return eventNumber(e, "generation") == start })
		acked := time.Now()

		if i == 11 {
			// Press 10's tail, held back, arrives during press 11: it must
			// never play, even though press 11 is live.
			if uplink.flushHeld() == 0 {
				t.Fatal("no held packets")
			}
			time.Sleep(250 * time.Millisecond)
			if n := whisper.tonesBetween(acked, time.Now()); n != 0 {
				whisper.mu.Lock()
				for _, f := range whisper.frames {
					if f.at.After(acked.Add(-300 * time.Millisecond)) {
						t.Logf("frame +%v rms %.0f epoch %d", f.at.Sub(acked), f.rms, f.epoch)
					}
				}
				whisper.mu.Unlock()
				t.Fatalf("%d frames of the previous press played in the next one", n)
			}
		}
		// As the browser does: the microphone joins only after coach.started.
		uplink.tone.Store(true)
		micReady := time.Now()
		first, ok := whisper.firstTone(micReady, 2*time.Second)
		if !ok {
			t.Fatalf("press %d: the adviser heard nothing", i)
		}
		latencies = append(latencies, first.Sub(micReady))
		time.Sleep(time.Until(micReady.Add(250 * time.Millisecond)))

		if i == 10 {
			// Hold back the last 60 ms of this press, as a delayed network would.
			uplink.hold.Store(true)
			time.Sleep(60 * time.Millisecond)
		}
		if i == 5 {
			// A browser that keeps sending after its stop: nothing may pass once
			// the server has processed the stop.
			sendCoachRTC(t, socket, map[string]any{"type": "coach.stop", "generation": stop})
			waitCoachRTCEvent(t, events, "coach.stopped", nil)
			stopped := time.Now()
			time.Sleep(200 * time.Millisecond)
			uplink.tone.Store(false)
			// Frames queued before the stop may still be in flight briefly.
			if n := whisper.tonesBetween(stopped.Add(40*time.Millisecond), time.Now()); n != 0 {
				t.Fatalf("%d frames played after stop", n)
			}
			continue
		}
		uplink.tone.Store(false)
		uplink.hold.Store(false)
		sendCoachRTC(t, socket, map[string]any{"type": "coach.stop", "generation": stop})
		waitCoachRTCEvent(t, events, "coach.stopped", nil)
	}

	if state := uplink.pc.ConnectionState(); state != webrtc.PeerConnectionStateConnected {
		t.Fatalf("the uplink did not survive the presses: %s", state)
	}
	sort.Slice(latencies, func(a, b int) bool { return latencies[a] < latencies[b] })
	p50, p95 := latencies[len(latencies)/2], latencies[len(latencies)*95/100]
	t.Logf("mic ready to adviser audio over %d presses: p50 %v p95 %v max %v", presses, p50, p95, latencies[len(latencies)-1])
	if p95 > 200*time.Millisecond {
		t.Fatalf("p95 %v over the 200 ms target", p95)
	}

	// Coaching stays private: nothing reached the customer or another listener.
	select {
	case f := <-carrier:
		t.Fatalf("coaching reached the customer: %d bytes", len(f.data))
	default:
	}
	if len(observer.audio) != 0 {
		t.Fatal("coaching reached another listener")
	}
}

func TestCoachingWebRTCPrepareLimits(t *testing.T) {
	_, listener, socket, events, _, _ := openRTCCoaching(t)
	uplink := newCoachRTCClient(t)
	offer := uplink.offer(t)

	// One send-only audio section only.
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 1, "sdp": strings.ReplaceAll(offer, "a=sendonly", "a=sendrecv")})
	if got := eventNumber(waitCoachRTCEvent(t, events, "coach.webrtc.rejected", nil), "connection"); got != 1 {
		t.Fatalf("rejected %d", got)
	}
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 2, "sdp": offer})
	waitCoachRTCEvent(t, events, "coach.webrtc.answer", func(e map[string]any) bool { return eventNumber(e, "connection") == 2 })
	// A second prepare within a second, or an ID that does not grow, is refused.
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 3, "sdp": offer})
	waitCoachRTCEvent(t, events, "coach.webrtc.rejected", func(e map[string]any) bool { return eventNumber(e, "connection") == 3 })
	time.Sleep(coachPrepareInterval)
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 2, "sdp": offer})
	waitCoachRTCEvent(t, events, "coach.webrtc.rejected", func(e map[string]any) bool { return eventNumber(e, "connection") == 2 })

	// An expired lease refuses a prepare.
	listener.coaching.expires.Store(time.Now().Add(-time.Second).Unix())
	sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.prepare", "connection": 9, "sdp": offer})
	waitCoachRTCEvent(t, events, "coach.webrtc.rejected", func(e map[string]any) bool { return eventNumber(e, "connection") == 9 })
}

// Tabs that loaded the SDK before 0.31.0 negotiate one PC per press.
func TestCoachingWebRTCLegacyPerPressOffer(t *testing.T) {
	_, _, socket, events, whisper, _ := openRTCCoaching(t)
	for _, generation := range []uint32{1, 3} {
		sendCoachRTC(t, socket, map[string]any{"type": "coach.start", "generation": generation})
		waitCoachRTCEvent(t, events, "coach.started", func(e map[string]any) bool { return eventNumber(e, "generation") == generation })
		uplink := newCoachRTCClient(t)
		sendCoachRTC(t, socket, map[string]any{"type": "coach.webrtc.offer", "generation": generation, "sdp": uplink.offer(t)})
		answer := waitCoachRTCEvent(t, events, "coach.webrtc.answer", func(e map[string]any) bool { return eventNumber(e, "generation") == generation })
		uplink.connect(t, answer["sdp"].(string))
		uplink.tone.Store(true)
		uplink.send(t)
		ready := time.Now()
		if _, ok := whisper.firstTone(ready, 2*time.Second); !ok {
			t.Fatalf("legacy press %d inaudible", generation)
		}
		sendCoachRTC(t, socket, map[string]any{"type": "coach.stop", "generation": generation + 1})
		waitCoachRTCEvent(t, events, "coach.stopped", nil)
		// The server closes a per-press PC with its press.
		deadline := time.Now().Add(3 * time.Second)
		for uplink.pc.ConnectionState() == webrtc.PeerConnectionStateConnected {
			if time.Now().After(deadline) {
				t.Fatal("legacy PC outlived its press")
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
}

func TestCoachRTCPressRejectsEarlierAudio(t *testing.T) {
	l := &callListener{done: make(chan struct{})}
	l.coaching.grant.Enabled = true
	l.coaching.expires.Store(time.Now().Add(time.Minute).Unix())
	now := time.Now()
	l.coaching.generation.Store(7)
	l.coaching.startedAt.Store(now.UnixMilli())
	l.coaching.deadline.Store(now.Add(2 * time.Second).UnixMilli())
	if _, ok := coachRTCPress(l, now.Add(5*time.Millisecond)); ok {
		t.Fatal("accepted while not talking")
	}
	l.coaching.talking.Store(true)
	if generation, ok := coachRTCPress(l, now.Add(5*time.Millisecond)); !ok || generation != 7 {
		t.Fatal("rejected the current press", generation, ok)
	}
	if _, ok := coachRTCPress(l, now.Add(-20*time.Millisecond)); ok {
		t.Fatal("audio captured before the press was relabeled as it")
	}
	l.coaching.deadline.Store(now.Add(-time.Millisecond).UnixMilli())
	if _, ok := coachRTCPress(l, now.Add(5*time.Millisecond)); ok {
		t.Fatal("accepted after the talk lease lapsed")
	}
	l.coaching.deadline.Store(now.Add(2 * time.Second).UnixMilli())
	l.coaching.expires.Store(now.Add(-time.Second).Unix())
	if _, ok := coachRTCPress(l, now.Add(5*time.Millisecond)); ok {
		t.Fatal("accepted after the session expired")
	}
	if listenerRTCCoachAlive(l) {
		t.Fatal("an expired session keeps its uplink")
	}
	l.coaching.expires.Store(now.Add(time.Minute).Unix())
	if !listenerRTCCoachAlive(l) {
		t.Fatal("a live session lost its uplink")
	}
	close(l.done)
	if listenerRTCCoachAlive(l) {
		t.Fatal("a closed listener keeps its uplink")
	}
}

func TestCoachRTCFirstSampleIsOneFrameBeforeSource(t *testing.T) {
	source := time.Unix(100, 0)
	if got := coachRTCFirstSample(source, 480); !got.Equal(source.Add(-20 * time.Millisecond)) {
		t.Fatal(got)
	}
	if got := coachRTCFirstSample(source, 1440); !got.Equal(source.Add(-60 * time.Millisecond)) {
		t.Fatal(got)
	}
}

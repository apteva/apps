package main

import (
	"encoding/binary"
	"encoding/json"
	"github.com/gobwas/ws/wsutil"
	"github.com/pion/webrtc/v4"
	"math"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func connectRTCListenerTest(t *testing.T, url string) (net.Conn, *webrtc.PeerConnection, <-chan struct {
	direction string
	pcm       []int16
}) {
	t.Helper()
	socket := dialWS(t, url)
	socket.SetReadDeadline(time.Now().Add(10 * time.Second))
	var config struct {
		Type string `json:"type"`
	}
	data, _, err := wsutil.ReadServerData(socket)
	if err != nil || json.Unmarshal(data, &config) != nil || config.Type != "webrtc.config" {
		t.Fatalf("config %s %v", data, err)
	}
	api, err := softphoneRTCAPI(softphoneRTCConfig{})
	if err != nil {
		t.Fatal(err)
	}
	pc, err := api.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { socket.Close(); pc.Close() })
	for d := 0; d < 2; d++ {
		if _, err = pc.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio, webrtc.RTPTransceiverInit{Direction: webrtc.RTPTransceiverDirectionRecvonly}); err != nil {
			t.Fatal(err)
		}
	}
	received := make(chan struct {
		direction string
		pcm       []int16
	}, 256)
	pc.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		go func() {
			decoder, e := newRTCOpusDecoderWithFEC(true)
			if e != nil {
				return
			}
			defer decoder.Close()
			buffer := make([]int16, 2880)
			for {
				packet, _, e := track.ReadRTP()
				if e != nil {
					return
				}
				n, e := decoder.decode(packet.Payload, buffer)
				if e == nil {
					select {
					case received <- struct {
						direction string
						pcm       []int16
					}{track.ID(), append([]int16(nil), buffer[:n]...)}:
					default:
					}
				}
			}
		}()
	})
	offer, err := pc.CreateOffer(nil)
	if err != nil {
		t.Fatal(err)
	}
	gather := webrtc.GatheringCompletePromise(pc)
	if err = pc.SetLocalDescription(offer); err != nil {
		t.Fatal(err)
	}
	select {
	case <-gather:
	case <-time.After(5 * time.Second):
		t.Fatal("gather timeout")
	}
	value, _ := json.Marshal(map[string]any{"type": "webrtc.offer", "sdp": pc.LocalDescription().SDP})
	if err = wsutil.WriteClientText(socket, value); err != nil {
		t.Fatal(err)
	}
	data, _, err = wsutil.ReadServerData(socket)
	var answer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err != nil || json.Unmarshal(data, &answer) != nil || answer.Type != "webrtc.answer" {
		t.Fatalf("answer %s %v", data, err)
	}
	if err = pc.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer.SDP}); err != nil {
		t.Fatal(err)
	}
	readListenerRTCReady(t, socket, 8*time.Second)
	socket.SetReadDeadline(time.Time{})
	return socket, pc, received
}

func TestListenerRTCNegotiatedDirectionsAndTermination(t *testing.T) {
	app, row, tap, _ := listenerFixture(t, true)
	session := listenerIssue(t, app, row.ID, nil)
	server := listenerServer(t, app)
	socket, _, received := connectRTCListenerTest(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42")+"?transport=webrtc")
	seen := map[string]bool{}
	deadline := time.Now().Add(3 * time.Second)
	for len(seen) < 2 && time.Now().Before(deadline) {
		pcm := make([]int16, 480)
		for i := range pcm {
			pcm[i] = int16(9000 * math.Sin(float64(i)*.13))
		}
		tap.publish(0, pcm16ToBytes(pcm))
		tap.publish(1, pcm16ToBytes(pcm))
		select {
		case frame := <-received:
			if rmsPCM(frame.pcm) > 500 {
				seen[frame.direction] = true
			}
		case <-time.After(20 * time.Millisecond):
		}
	}
	if !seen["caller"] || !seen["adviser"] {
		t.Fatal("missing directional RTP", seen)
	}
	// The carrier and its tap remain live when the listener stops.
	tap.mu.Lock()
	l := tap.listeners[phoneHash(session.SessionToken)]
	tap.mu.Unlock()
	if l == nil || l.rtc == nil {
		t.Fatal("RTC listener not registered")
	}
	if err := app.db().updateStatus(row.ID, "completed", ""); err != nil {
		t.Fatal(err)
	}
	assertListenerClosed(t, socket, "call_ended")
	if len(l.rtc.stats[0].snapshot().Events) > 32 {
		t.Fatal("unbounded diagnostics")
	}
}

func TestListenerRTCUnavailableDoesNotConsumeSession(t *testing.T) {
	app, row, _, _ := listenerFixture(t)
	session := listenerIssue(t, app, row.ID, nil)
	server := listenerServer(t, app)
	path := server.URL + strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42")
	response, err := http.Get(path + "?transport=webrtc")
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 503 {
		t.Fatal(response.StatusCode)
	}
	// Automatic fallback must reuse the still-valid passive grant.
	socket := dialWS(t, path)
	defer socket.Close()
	readListenerRTCReady(t, socket, time.Second)
}

func TestRTCWhisperQueuePinnedValidityBoundsAndMix(t *testing.T) {
	frames := make(chan rtcWhisperFrame, 6)
	done := make(chan struct{})
	var valid atomic.Bool
	valid.Store(true)
	frame := make([]byte, 176)
	binary.LittleEndian.PutUint32(frame, coachPlaybackMagic)
	binary.LittleEndian.PutUint64(frame[8:], math.Float64bits(mediaClockMS()))
	for i := 16; i < len(frame); i++ {
		frame[i] = ulawEncodeOne(8000)
	}
	// The hub already holds its mutex here. Enqueue must never call the validator.
	called := false
	if !queueRTCWhisper(frames, frame, func() bool { called = true; return valid.Load() }, done) || called {
		t.Fatal("enqueue waits on pinned target")
	}
	for i := 0; i < 100; i++ {
		queueRTCWhisper(frames, frame, func() bool { return valid.Load() }, done)
	}
	if len(frames) > 6 {
		t.Fatal("unbounded coaching queue")
	}
	valid.Store(false)
	if nextRTCWhisper(frames, time.Now()) != nil {
		t.Fatal("stopped coaching played")
	}
	valid.Store(true)
	queueRTCWhisper(frames, frame, func() bool { return valid.Load() }, done)
	if nextRTCWhisper(frames, time.Now().Add(time.Second)) != nil {
		t.Fatal("stale coaching played")
	}
	main := make([]float32, 960)
	for i := range main {
		main[i] = .8
	}
	overlay := make([]int16, 160)
	for i := range overlay {
		overlay[i] = 10000
	}
	mixed := mixRTCWhisper(main, overlay, newPCMResampler(8000, 48000))
	if main[100] != .8 {
		t.Fatal("carrier/recording source modified")
	}
	for _, v := range mixed {
		if v > 1 || v < -1 {
			t.Fatal("clipped overlay")
		}
	}
	close(done)
	if queueRTCWhisper(frames, frame, func() bool { return true }, done) {
		t.Fatal("closed browser accepted whisper")
	}
}

func TestListenerRTCUploadRequiresLiveGenerationAndLease(t *testing.T) {
	l := &callListener{done: make(chan struct{})}
	l.coaching.grant.Enabled = true
	l.coaching.expires.Store(time.Now().Add(time.Minute).Unix())
	l.coaching.deadline.Store(time.Now().Add(time.Second).UnixMilli())
	l.coaching.generation.Store(4)
	l.coaching.talking.Store(true)
	if !listenerRTCCoachValid(l, 4) || listenerRTCCoachValid(l, 3) {
		t.Fatal("generation check")
	}
	l.coaching.talking.Store(false)
	if listenerRTCCoachValid(l, 4) {
		t.Fatal("released mic accepted")
	}
	l.coaching.talking.Store(true)
	l.coaching.expires.Store(time.Now().Add(-time.Second).Unix())
	if listenerRTCCoachValid(l, 4) {
		t.Fatal("expired mic accepted")
	}
	l.coaching.expires.Store(time.Now().Add(time.Minute).Unix())
	close(l.done)
	if listenerRTCCoachValid(l, 4) {
		t.Fatal("terminated listener accepted")
	}
}

func readListenerRTCReady(t *testing.T, socket net.Conn, timeout time.Duration) {
	t.Helper()
	socket.SetReadDeadline(time.Now().Add(timeout))
	for {
		data, _, err := wsutil.ReadServerData(socket)
		if err != nil {
			t.Fatal("listener ready", err)
		}
		var event struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(data, &event) == nil && event.Type == "listener.ready" {
			socket.SetReadDeadline(time.Time{})
			return
		}
	}
}

func BenchmarkRTCPrivateCoachingMix(b *testing.B) {
	main := make([]float32, 960)
	overlay := make([]int16, 160)
	for i := range overlay {
		overlay[i] = int16(5000 * math.Sin(float64(i)*.1))
	}
	resampler := newPCMResampler(8000, 48000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		mixRTCWhisper(main, overlay, resampler)
	}
}

func TestListenerRTCFECOverride(t *testing.T) {
	app, row, tap, _ := listenerFixture(t, true)
	session := listenerIssue(t, app, row.ID, nil)
	server := listenerServer(t, app)
	socket, _, _ := connectRTCListenerTest(t, server.URL+strings.TrimPrefix(session.MediaURL, "/api/apps/telephony/_install/42")+"?transport=webrtc&webrtc_fec=false")
	defer socket.Close()
	tap.mu.Lock()
	listener := tap.listeners[phoneHash(session.SessionToken)]
	tap.mu.Unlock()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot := listener.rtc.stats[0].snapshot()
		if snapshot.Encoder != nil {
			if snapshot.Encoder.FECRequested || snapshot.Encoder.FEC {
				t.Fatal("listener ignored FEC opt-out", snapshot.Encoder)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no listener encoder")
		}
		time.Sleep(time.Millisecond)
	}
}

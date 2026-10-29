package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gobwas/ws"
	"github.com/gobwas/ws/wsutil"
)

func continuousAgentFixture() *agentAudioSession {
	p, _ := resolveAgentAudioProfile(agentAudioProfile{})
	return &agentAudioSession{profile: p, capabilities: agentAudioCapabilities{Version: 1, Continuous: true, LocalInterruption: true, ScopedCancellation: true, ReceiptEvidence: true}, cancelled: map[agentPlaybackKey]bool{}}
}

func TestAgentAudioPlaybackCapacityNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name           string
		flow, receipts bool
		mode           string
		backlog, want  int
	}{
		{"new_core", true, true, "auto", 1000, 1000},
		{"small_pinned_profile", true, true, "continuous", 200, 200},
		{"large_pinned_profile", true, true, "auto", 2000, 2000},
		{"older_core", false, true, "auto", 1000, 0},
		{"traditional", true, true, "traditional", 1000, 0},
		{"no_receipts", true, false, "auto", 1000, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := continuousAgentFixture()
			s.capabilities.PlaybackFlowControl, s.capabilities.ReceiptEvidence = tc.flow, tc.receipts
			s.profile.Mode, s.profile.BacklogMS = tc.mode, tc.backlog
			if s.playbackCapacityMS() != tc.want || s.snapshot().PlaybackCapacityMS != tc.want {
				t.Fatal("wrong pinned playback capacity", s.snapshot())
			}
			if !tc.receipts {
				if err := s.ready(nil, "estimated"); err != nil {
					t.Fatal(err)
				}
				return
			}
			client, server := net.Pipe()
			defer server.Close()
			defer client.Close()
			w := newWebSocketWriterPump(client, ws.StateClientSide)
			defer w.Stop()
			done := make(chan error, 1)
			go func() { done <- s.ready(w, "estimated") }()
			_ = server.SetReadDeadline(time.Now().Add(time.Second))
			raw, op, err := wsutil.ReadClientData(server)
			if err != nil || op != ws.OpText {
				t.Fatal(op, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			var ready map[string]any
			if json.Unmarshal(raw, &ready) != nil || ready["type"] != "playback.ready" {
				t.Fatal(string(raw))
			}
			capacity, present := ready["capacity_ms"]
			if tc.want == 0 && present || tc.want > 0 && capacity != float64(tc.want) {
				t.Fatal(string(raw))
			}
		})
	}
	var human *agentAudioSession
	if human.playbackCapacityMS() != 0 {
		t.Fatal("human transport opted in")
	}
	if err := human.ready(nil, "estimated"); err != nil {
		t.Fatal(err)
	}
}

func TestAgentAudioProfilesPinnedAndIsolated(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := testCall("agent-profile", "answered")
	row.PeerKind = peerKindRealtime
	row.AgentID = 7
	row.Direction = "outbound"
	row.ProjectID = ctx.CurrentProject()
	row.FromNumber = "+12025550100"
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	save := func(scope string, ms int) {
		t.Helper()
		if _, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"scope": scope, "config": map[string]any{"speech_start_ms": ms}}); err != nil {
			t.Fatal(err)
		}
	}
	save("project", 200)
	save("agent/7", 120)
	p, err := a.pinAgentAudioProfile(&row)
	if err != nil || p.SpeechStartMS != 120 {
		t.Fatal(p, err)
	}
	save("agent/7", 240)
	p, err = a.pinAgentAudioProfile(&row)
	if err != nil || p.SpeechStartMS != 120 {
		t.Fatal("active call changed", p, err)
	}
	row.ID = "next-agent-profile"
	row.ThreadID = "tel-next-agent-profile"
	if err = a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	p, err = a.pinAgentAudioProfile(&row)
	if err != nil || p.SpeechStartMS != 240 {
		t.Fatal("next call did not update", p, err)
	}
	row.ProjectID = "other-project"
	p, err = a.resolveAgentAudioForCall(&row)
	if err != nil || p.SpeechStartMS != 160 || p.SendAheadMS != 60 {
		t.Fatal("project isolation", p, err)
	}
	for _, bad := range []agentAudioProfile{{SpeechStartMS: 20}, {SpeechStartMS: 121}, {BacklogMS: 15000}, {SendAheadMS: 201}, {Mode: "gpt-live"}, {Interruption: "loud"}} {
		if _, err = resolveAgentAudioProfile(bad); err == nil {
			t.Fatal("accepted unsafe policy", bad)
		}
	}
}

func TestAgentAudioCapabilityHandshakeAndLegacyFallback(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	row := testCall("agent-contract", "answered")
	row.ProjectID = ctx.CurrentProject()
	row.PeerKind = peerKindRealtime
	if err := a.db().insertCall(row); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(agentAudioHeader, `{"version":1,"continuous_conversation":true,"local_interruption_required":true,"scoped_cancellation":true,"receipt_evidence":true}`)
		upgrader := ws.HTTPUpgrader{Header: w.Header()}
		c, _, _, err := upgrader.Upgrade(r, w)
		if err == nil {
			defer c.Close()
			buf := make([]byte, 1)
			_, _ = c.Read(buf)
		}
	}))
	defer server.Close()
	dialer, response := a.agentBridgeDialer(&row)
	conn, _, _, err := dialer.Dial(context.Background(), agentBridgeURL("ws"+strings.TrimPrefix(server.URL, "http"), &row))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	s, err := a.newAgentAudioSession(&row, response)
	if err != nil || !s.continuous() || !s.capabilities.LocalInterruption {
		t.Fatal(s, err)
	}
	legacy, err := a.newAgentAudioSession(&row, nil)
	if err != nil || legacy.continuous() || legacy.policy().maxQueueMS != 15000 {
		t.Fatal("legacy behavior changed", legacy, err)
	}
	row.PeerKind = peerKindHuman
	if s, err = a.newAgentAudioSession(&row, response); err != nil || s != nil {
		t.Fatal("human call opted into AI audio")
	}
}

func TestAgentAudioAnalysisNeverGatesOrAttenuatesCaller(t *testing.T) {
	s := continuousAgentFixture()
	f := newCarrierAudioFrontend(24000)
	s.configureFrontend(f)
	if f.mode != localBargeInFallback || f.speechStartMS != 160 {
		t.Fatal("local onset detection absent", f.mode, f.speechStartMS)
	}
	var original, output []int16
	for frame := 0; frame < 100; frame++ {
		pcm := make([]int16, 480)
		for i := range pcm {
			pcm[i] = int16((i*17+frame*3)%121 - 60)
		}
		if frame%3 == 0 {
			clear(pcm)
		}
		original = append(original, pcm...)
		output = append(output, f.process(pcm).PCM...)
	}
	if !bytes.Equal(pcm16ToBytes(original), pcm16ToBytes(output)) {
		t.Fatal("quiet speech/silence changed by interruption analysis")
	}
}

func TestAgentAudioLateFramesAndGenerations(t *testing.T) {
	s := continuousAgentFixture()
	s.cancel("reply", 7)
	if s.accept(realtimeBridgeControl{ItemID: "reply", Generation: 7}) {
		t.Fatal("cancelled speech revived")
	}
	if !s.accept(realtimeBridgeControl{ItemID: "reply", Generation: 8}) || !s.accept(realtimeBridgeControl{ItemID: "next", Generation: 7}) {
		t.Fatal("unrelated speech rejected")
	}
	for i := 0; i < 1000; i++ {
		s.cancel(fmt.Sprint(i), 7)
	}
	if len(s.order) > 256 || len(s.cancelled) > 256 {
		t.Fatal("unbounded cancellation history")
	}
}

func TestAgentAudioScopedTwilioClearPreservesNextReply(t *testing.T) {
	s := continuousAgentFixture()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type wire struct {
		Event string `json:"event"`
		Media struct {
			Payload string `json:"payload"`
		} `json:"media"`
		Mark struct {
			Name string `json:"name"`
		} `json:"mark"`
	}
	var mu sync.Mutex
	var writes []wire
	tracker := newTwilioPlaybackTracker()
	p := newTwilioAudioPacerWithPolicy(ctx, "stream", tracker, s.policy(), func(raw []byte) error {
		var v wire
		if err := json.Unmarshal(raw, &v); err != nil {
			return err
		}
		mu.Lock()
		writes = append(writes, v)
		mu.Unlock()
		return nil
	}, nil)
	defer func() { cancel(); <-p.done }()
	packets := func(value int16) []twilioAudioPacket {
		out := make([]twilioAudioPacket, 10)
		for i := range out {
			pcm := make([]int16, 160)
			for j := range pcm {
				pcm[j] = value
			}
			out[i] = twilioAudioPacket{PCM: pcm, AudioEndMS: (i + 1) * 20}
		}
		return out
	}
	a, b := packets(1500), packets(6000)
	if _, err := p.enqueue(ctx, a, realtimeBridgeControl{ItemID: "A", Generation: 7}); err != nil {
		t.Fatal(err)
	}
	if _, err := p.enqueue(ctx, b, realtimeBridgeControl{ItemID: "B", Generation: 7}); err != nil {
		t.Fatal(err)
	}
	c := realtimeBridgeControl{ItemID: "A", Generation: 7}
	s.cancel(c.ItemID, c.Generation)
	r, err := p.cancelPlayback(ctx, c)
	if err != nil || r.Scope != "stream" || !r.RemoteCommandSent || r.RemoteConfirmed || len(r.Collateral) > 0 {
		t.Fatal(r, err)
	}
	if _, err = p.enqueue(ctx, a, realtimeBridgeControl{ItemID: "A", Generation: 7}); err != nil {
		t.Fatal(err)
	}
	// The pacer dequeues before it writes: wait for the final packet on the
	// wire, not only for an empty queue.
	writtenAfterClear := func() int {
		mu.Lock()
		defer mu.Unlock()
		n, cleared := 0, false
		for _, v := range writes {
			cleared = cleared || v.Event == "clear"
			if cleared && v.Event == "media" {
				n++
			}
		}
		return n
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if p.pendingSamples.Load() == 0 && writtenAfterClear() >= 10 {
			break
		}
		time.Sleep(time.Millisecond)
	}
	mu.Lock()
	observed := append([]wire(nil), writes...)
	mu.Unlock()
	clearAt := -1
	after := 0
	want, _ := base64.StdEncoding.DecodeString(encodeCarrierPacket(b[0].PCM, carrierCodecPCMU8))
	lastMedia := -1
	for i, v := range observed {
		if v.Event == "media" {
			lastMedia = i
		}
	}
	for i, v := range observed {
		if v.Event == "clear" {
			if clearAt >= 0 {
				t.Fatal("extra clear")
			}
			clearAt = i
		}
		if v.Event == "media" && clearAt >= 0 {
			got, e := base64.StdEncoding.DecodeString(v.Media.Payload)
			expected := want
			if after == 0 || i == lastMedia {
				// Only the first 3 ms after an explicit clear has a start ramp, and
				// the final speech before silence has a 3 ms tail fade.
				pcm := append([]int16(nil), b[0].PCM...)
				if after == 0 {
					for j := 0; j < 24; j++ {
						pcm[j] = int16(int64(pcm[j]) * int64(j+1) / 24)
					}
				}
				if i == lastMedia {
					applyCarrierTailFade(pcm, twilioMediaSampleRate)
				}
				expected, _ = base64.StdEncoding.DecodeString(encodeCarrierPacket(pcm, carrierCodecPCMU8))
			}
			if e != nil || !bytes.Equal(got, expected) {
				t.Fatal("cancelled speech escaped after clear")
			}
			after++
		}
		if v.Event == "mark" && clearAt < 0 {
			if _, ok := tracker.acknowledge(v.Mark.Name); ok {
				t.Fatal("cleared mark became playback success")
			}
		}
	}
	if clearAt < 0 || after != 10 {
		t.Fatal("next reply/opening lost", clearAt, after)
	}
	if r, err = p.cancelPlayback(ctx, c); err != nil || r.RemoteCommandSent {
		t.Fatal("late duplicate clear affected new speech", r, err)
	}
}

func TestAgentAudioScopedSIPClearPreservesNextReply(t *testing.T) {
	for _, codec := range []string{"PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			s := continuousAgentFixture()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			progress := make(chan twilioPlaybackProgress, 20)
			p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, s.policy(), func(v twilioPlaybackProgress) error { progress <- v; return nil })
			defer func() { cancel(); <-p.done }()
			packets := []sipRTPOutboundPacket{}
			for _, id := range []string{"A", "B"} {
				for i := 0; i < 4; i++ {
					packets = append(packets, sipRTPOutboundPacket{pcm: make([]int16, media.sampleRate()/50), itemID: id, generation: 3, audioEndMS: (i + 1) * 20})
				}
			}
			if _, err = p.enqueue(packets); err != nil {
				t.Fatal(err)
			}
			s.cancel("A", 3)
			r, err := p.cancelPlayback(ctx, realtimeBridgeControl{ItemID: "A", Generation: 3})
			if err != nil || r.Scope != "local" || r.RemoteCommandSent || r.RemoteConfirmed {
				t.Fatal(r, err)
			}
			// Receipts coalesce to the latest position per playback, so wait for
			// B's final position rather than an exact receipt count.
			for done := false; !done; {
				select {
				case got := <-progress:
					if got.ItemID != "B" || got.Generation != 3 {
						t.Fatal("old speech sent", got)
					}
					done = got.AudioEndMS == 80
				case <-time.After(2 * time.Second):
					t.Fatal("next reply stalled")
				}
			}
		})
	}
}

func BenchmarkAgentAudioCancellationFence(b *testing.B) {
	s := continuousAgentFixture()
	s.cancel("old", 1)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.rejects("new", 1) {
			b.Fatal("fresh audio rejected")
		}
	}
}

func TestAgentAudioPacketizerPreservesItemBoundaries(t *testing.T) {
	p := newCarrierAudioPacketizer(8000)
	a := realtimeBridgeControl{ItemID: "a", Generation: 1, AudioEndMS: 10}
	b := realtimeBridgeControl{ItemID: "b", Generation: 1, AudioEndMS: 10}
	if got := p.add(make([]int16, 80), 240, a); len(got) != 0 {
		t.Fatal("premature frame")
	}
	got := p.add(make([]int16, 80), 240, b)
	if len(got) != 1 || got[0].ItemID != "a" || len(got[0].PCM) != 80 {
		t.Fatal("mixed playback identities", got)
	}
	p.cancelPlayback(a)
	tail := p.flushPartial()
	if len(tail) != 1 || tail[0].ItemID != "b" || len(tail[0].PCM) != 80 {
		t.Fatal("new opening lost", tail)
	}
}

func TestAgentAudioStrictProfileArgumentsAndExpiry(t *testing.T) {
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	for _, args := range []map[string]any{{"scope": 123}, {"reset": "true"}, {"ttl_seconds": 1.5}, {"scope": "agent/x"}, {"scope": "agent/-3"}, {"scope": "number/invalid"}, {"scope": "account/999"}, {"config": map[string]any{"unknown": true}}, {"config": nil}} {
		if _, err := a.toolAgentAudioProfile(context.Background(), ctx, args); err == nil {
			t.Fatalf("accepted malformed settings: %v", args)
		}
	}
	_, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{"config": map[string]any{"speech_start_ms": 240}, "ttl_seconds": 1})
	if err != nil {
		t.Fatal(err)
	}
	_, err = a.db().db.Exec(`UPDATE agent_audio_profiles SET expires_at=?`, time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano))
	if err != nil {
		t.Fatal(err)
	}
	row := testCall("expired", "answered")
	row.ProjectID = ctx.CurrentProject()
	p, err := a.resolveAgentAudioForCall(&row)
	if err != nil || p.SpeechStartMS != 160 {
		t.Fatal(p, err)
	}
	result, err := a.toolAgentAudioProfile(context.Background(), ctx, map[string]any{})
	if err != nil || result.(map[string]any)["override_active"] != false {
		t.Fatal(result, err)
	}
}

func TestAgentAudioSIPPartialChunksAndSilentActivity(t *testing.T) {
	for _, codec := range []string{"PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			s := continuousAgentFixture()
			// Exact packetization of partial chunks; drift has its own tests.
			s.profile.Playout.Drift = "off"
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer sender.Close()
			receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
			if err != nil {
				t.Fatal(err)
			}
			defer receiver.Close()
			media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			activity := make(chan bool, 20)
			progress := make(chan twilioPlaybackProgress, 20)
			policy := s.policy()
			policy.onSent = func(silent bool) { activity <- silent }
			p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, policy, func(v twilioPlaybackProgress) error { progress <- v; return nil })
			defer func() { cancel(); <-p.done }()
			samples := media.sampleRate() / 100
			chunks := []sipRTPOutboundPacket{}
			for i := 0; i < 4; i++ {
				chunks = append(chunks, sipRTPOutboundPacket{pcm: make([]int16, samples), itemID: "speech", generation: 1, audioEndMS: (i + 1) * 10})
			}
			if _, err = p.enqueue(chunks); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				select {
				case v := <-progress:
					if v.AudioEndMS != (i+1)*20 {
						t.Fatal("chunk padding extended speech", v)
					}
				case <-time.After(time.Second):
					t.Fatal("speech stalled")
				}
				if <-activity {
					t.Fatal("speech marked silent")
				}
			}
			// A lone 10 ms tail must be sent, not left buffered indefinitely.
			if _, err = p.enqueue([]sipRTPOutboundPacket{{pcm: make([]int16, samples), itemID: "silence", generation: 1, silence: true, audioEndMS: 10}}); err != nil {
				t.Fatal(err)
			}
			select {
			case v := <-progress:
				if v.ItemID != "silence" || v.AudioEndMS != 10 {
					t.Fatal(v)
				}
			case <-time.After(time.Second):
				t.Fatal("partial tail stuck")
			}
			if !<-activity {
				t.Fatal("intentional silence counted as agent speech")
			}
			select {
			case v := <-progress:
				t.Fatal("extra padded packet", v)
			case <-time.After(45 * time.Millisecond):
			}
		})
	}
}

func TestAgentAudioSIPConcurrentCancelAndEnqueue(t *testing.T) {
	s := continuousAgentFixture()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	sender, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer sender.Close()
	receiver, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer receiver.Close()
	media := &sipRTPMedia{conn: sender, remote: receiver.LocalAddr().(*net.UDPAddr), offer: sipMediaOffer{Codec: "PCMA", PacketSamples: 160}}
	p := newSIPRTPPacerWithPolicy(ctx, media, &sipPlaybackState{}, s.policy(), nil)
	defer func() { cancel(); <-p.done }()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			_, _ = p.enqueue([]sipRTPOutboundPacket{{pcm: make([]int16, 160), itemID: fmt.Sprint(i), generation: 1}})
		}
	}()
	for i := 0; i < 100; i++ {
		if _, err = p.cancelPlayback(ctx, realtimeBridgeControl{ItemID: fmt.Sprint(i), Generation: 1}); err != nil {
			t.Fatal("cancel blocked", err)
		}
	}
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("producer deadlocked")
	}
}

func TestAgentAudioReceiptLedgerBoundedAndClearedMarksStayCancelled(t *testing.T) {
	tracker := newTwilioPlaybackTracker()
	first := ""
	for i := 1; i < 10000; i++ {
		mark := tracker.addGeneration("reply", 7, i*20)
		if first == "" {
			first = mark
		}
	}
	if len(tracker.pending) > 1024 || tracker.evicted == 0 {
		t.Fatal("unbounded marks")
	}
	if _, ok := tracker.acknowledge(first); ok {
		t.Fatal("evicted mark confirmed playback")
	}
	tracker.clear()
	if tracker.hasPending() {
		t.Fatal("clear retained old marks")
	}
}

func TestAgentAudioCollateralFencedWithoutReceiptSupport(t *testing.T) {
	s := continuousAgentFixture()
	s.capabilities.ReceiptEvidence = false
	if err := s.cleared(nil, realtimeBridgeControl{ItemID: "a"}, agentClearReceipt{Scope: "stream", Collateral: []string{"b"}}); err != nil {
		t.Fatal(err)
	}
	if s.accept(realtimeBridgeControl{ItemID: "b", Generation: 1}) {
		t.Fatal("globally cleared reply revived")
	}
	a := &App{}
	stop := a.trackAgentAudio("call", s)
	if a.liveAgentAudio("call") == nil {
		t.Fatal("no live diagnostics")
	}
	stop()
	if a.liveAgentAudio("call") != nil {
		t.Fatal("live session leaked")
	}
}

func TestAgentAudioNegotiatedSocketWriteBudget(t *testing.T) {
	conn, peer := net.Pipe()
	defer peer.Close()
	pump := newWebSocketWriterPump(conn, ws.StateClientSide)
	defer func() { conn.Close(); pump.Stop() }()
	s := continuousAgentFixture()
	s.configureWriter(pump)
	if pump.maxWriteBudget.Load() != int64(250*time.Millisecond) {
		t.Fatal("negotiated budget absent")
	}
	started := time.Now()
	if err := pump.Write(ws.OpBinary, make([]byte, 960)); err == nil {
		t.Fatal("blocked write succeeded")
	}
	if time.Since(started) > time.Second {
		t.Fatal("legacy write deadline used")
	}
}

func BenchmarkAgentAudioFrontend(b *testing.B) {
	for _, mode := range []string{"legacy", "analysis_only"} {
		b.Run(mode, func(b *testing.B) {
			f := newCarrierAudioFrontend(16000)
			if mode == "analysis_only" {
				continuousAgentFixture().configureFrontend(f)
			}
			pcm := sinePCM(16000, 430, 320)
			b.ReportAllocs()
			b.SetBytes(640)
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if len(f.process(pcm).PCM) != len(pcm) {
					b.Fatal("audio changed length")
				}
			}
		})
	}
}

func TestAgentAudioCollateralKeepsGenerationFence(t *testing.T) {
	s := continuousAgentFixture()
	s.capabilities.ReceiptEvidence = false
	receipt := agentClearReceipt{Scope: "stream", Collateral: []string{"same"}, CollateralPlaybacks: []agentCollateralPlayback{{ItemID: "same", Generation: 7}}}
	if err := s.cleared(nil, realtimeBridgeControl{ItemID: "target", Generation: 7}, receipt); err != nil {
		t.Fatal(err)
	}
	if !s.rejects("same", 7) || s.rejects("same", 8) {
		t.Fatal("global carrier clear leaked into next generation")
	}
}
func TestAgentAudioProfileHTTPAndDatabaseFailure(t *testing.T) {
	a, _, _ := carrierMediaFixture(t, "inbound")
	w := phoneTestRequest(a, nil, "POST", "/numbers/agent-audio-profile", map[string]any{"scope": "outbound", "config": map[string]any{"mode": "continuous", "speech_start_ms": 200}, "ttl_seconds": 3600})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var result map[string]any
	if json.Unmarshal(w.Body.Bytes(), &result) != nil || result["override_active"] != true {
		t.Fatal(result)
	}
	if _, err := a.db().db.Exec(`DROP TABLE agent_audio_profiles`); err != nil {
		t.Fatal(err)
	}
	row := testCall("failed-profile", "answered")
	if _, err := a.resolveAgentAudioForCall(&row); err == nil {
		t.Fatal("database error silently became default profile")
	}
}

func TestAgentAudioContinuousDetectorNoiseQuietSpeechAndRearm(t *testing.T) {
	for _, rate := range []int{8000, 16000, 24000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			session := continuousAgentFixture()
			for name, pcm := range map[string][]int16{"traffic": trafficNoise(rate, 1800), "cafe": cafeNoise(rate, 1800), "music": holdMusic(rate, 1600, 850), "quiet_echo": telephoneSpeech(rate, 1200, 260), "short_ack": telephoneSpeech(rate, 80, 1500)} {
				frontend := newCarrierAudioFrontend(rate)
				session.configureFrontend(frontend)
				warmAudioFrontend(frontend, telephoneNoise(rate, 500, 100))
				if start := processInChunks(frontend, pcm, []int{rate / 100, rate / 50}); start != 0 {
					t.Fatal(name, "interrupted speech", start)
				}
			}
			frontend := newCarrierAudioFrontend(rate)
			session.configureFrontend(frontend)
			warmAudioFrontend(frontend, telephoneNoise(rate, 600, 70))
			for repeat := 0; repeat < 3; repeat++ {
				start := processInChunks(frontend, telephoneSpeech(rate, 800, 1000), []int{rate / 50})
				if start < 160 || start > 500 {
					t.Fatal("quiet speech onset", start)
				}
				processInChunks(frontend, make([]int16, rate*600/1000), []int{rate / 50})
			}
			if frontend.snapshot().LocalSpeechStarts != 3 {
				t.Fatal("repeated interruptions failed to rearm")
			}
		})
	}
}

func TestAgentAudioRecordedNaturalInterruptionPreservesPCM(t *testing.T) {
	pcm24, err := readLocalPromptWAV("testdata/agent-audio/caller-interrupt-fr.wav")
	if err != nil {
		t.Fatal(err)
	}
	for _, rate := range []int{8000, 16000, 24000} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			pcm := newPCMResampler(24000, rate).Process(pcm24)
			f := newCarrierAudioFrontend(rate)
			continuousAgentFixture().configureFrontend(f)
			warmAudioFrontend(f, make([]int16, rate))
			var got []int16
			onset := 0
			for offset := 0; offset < len(pcm); offset += rate / 50 {
				result := f.process(pcm[offset:min(offset+rate/50, len(pcm))])
				got = append(got, result.PCM...)
				if onset == 0 && result.SpeechStarted {
					onset = (offset + rate/50) * 1000 / rate
				}
			}
			if onset < 160 || onset > 500 {
				t.Fatal("natural interruption onset", onset)
			}
			if !bytes.Equal(pcm16ToBytes(got), pcm16ToBytes(pcm[:len(got)])) {
				t.Fatal("detector altered natural speech")
			}
		})
	}
}

func TestAgentAudioOverflowKeepsUnrelatedQueuedSpeech(t *testing.T) {
	for _, carrier := range []string{"twilio", "telnyx"} {
		t.Run(carrier, func(t *testing.T) {
			s := continuousAgentFixture()
			s.profile.BacklogMS = 200
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var media, clears atomic.Int64
			write := func(raw []byte) error {
				var frame struct {
					Event string `json:"event"`
				}
				_ = json.Unmarshal(raw, &frame)
				if frame.Event == "media" {
					media.Add(1)
				}
				if frame.Event == "clear" {
					clears.Add(1)
				}
				return nil
			}
			tracker := newTwilioPlaybackTracker()
			if carrier == "twilio" {
				p := newTwilioAudioPacerWithPolicy(ctx, "stream", tracker, s.policy(), write, nil)
				defer func() { cancel(); <-p.done }()
				a := twilioAudioPackets(make([]int16, 1600), 4800, realtimeBridgeControl{ItemID: "a", Generation: 1, AudioEndMS: 200})
				if _, err := p.enqueue(ctx, a, realtimeBridgeControl{ItemID: "a", Generation: 1}); err != nil {
					t.Fatal(err)
				}
				b := twilioAudioPackets(make([]int16, 1920), 5760, realtimeBridgeControl{ItemID: "b", Generation: 1, AudioEndMS: 240})
				if _, err := p.enqueue(ctx, b, realtimeBridgeControl{ItemID: "b", Generation: 1}); !errors.Is(err, errTwilioPacerOverflow) {
					t.Fatal("unbounded queue", err)
				}
			} else {
				p := newJSONCarrierAudioPacer(ctx, 8000, carrierCodecPCMU8, "telnyx", "stream", true, tracker, s.policy(), write, nil, nil)
				defer func() { cancel(); <-p.done }()
				packetizer := newCarrierAudioPacketizer(8000)
				if _, _, err := p.enqueue(ctx, packetizer.add(make([]int16, 1600), 4800, realtimeBridgeControl{ItemID: "a", Generation: 1, AudioEndMS: 200})); err != nil {
					t.Fatal(err)
				}
				if _, _, err := p.enqueue(ctx, packetizer.add(make([]int16, 1920), 5760, realtimeBridgeControl{ItemID: "b", Generation: 1, AudioEndMS: 240})); !errors.Is(err, errCarrierPacerOverflow) {
					t.Fatal("unbounded queue", err)
				}
			}
			if clears.Load() != 0 || media.Load() == 0 || !tracker.hasPending() {
				t.Fatal("overflow broadly cleared unrelated speech")
			}
		})
	}
}

// The same frontend policy is used by SIP, Twilio and the other carrier bridges.
// Automatic duplex overlap must preserve all microphone samples and must not
// create an application cancellation for a greeting/backchannel collision.
func TestAgentDuplexOverlapPreservesSpeech(t *testing.T) {
	for _, rate := range []int{8000, 16000, 24000} {
		for _, mode := range []string{"auto", "local"} {
			t.Run(fmt.Sprintf("%d/%s", rate, mode), func(t *testing.T) {
				s := continuousAgentFixture()
				s.capabilities.ProviderInterruption = true
				s.profile.Interruption = mode
				f := newCarrierAudioFrontend(rate)
				s.configureFrontend(f)
				if mode == "auto" && f.mode != localBargeInOff {
					t.Fatal("duplex provider forced local cancellation")
				}
				if mode == "local" && f.mode != localBargeInFallback {
					t.Fatal("explicit local override lost")
				}
				speech := telephoneSpeech(rate, 580, 1500)
				result := f.process(speech)
				if !bytes.Equal(pcm16ToBytes(speech), pcm16ToBytes(result.PCM)) {
					t.Fatal("overlap changed caller samples")
				}
				if mode == "local" && !result.SpeechStarted {
					t.Fatal("explicit interruption did not detect sustained speech")
				}
				if mode == "auto" && result.SpeechStarted {
					t.Fatal("automatic overlap emitted hard cancel")
				}
			})
		}
	}
}

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/gobwas/ws"
	"math"
	"net"
	"strings"
	"testing"
	"time"
)

func TestSIPAudioProfilesScopesExpiryAndPinnedCalls(t *testing.T) {
	a, ctx, target := carrierMediaFixture(t, "inbound")
	save := func(args map[string]any) map[string]any {
		t.Helper()
		v, e := a.sipAudioProfileSettings(ctx, args)
		if e != nil {
			t.Fatal(e)
		}
		return v
	}
	save(map[string]any{"config": map[string]any{"preset": "balanced"}})
	save(map[string]any{"direction": "inbound", "config": map[string]any{"preset": "resilient", "codecs": []string{"PCMA"}}})
	incoming, e := a.localMediaSettingsForCall("project-a", target["route_id"].(string), "inbound", 10, "+12025550100")
	if e != nil {
		t.Fatal(e)
	}
	if incoming.RTPPlayback.TargetMS != 80 || len(incoming.Codecs) != 1 || incoming.Codecs[0] != "PCMA" {
		t.Fatal("inbound profile missing", incoming)
	}
	outbound, e := a.localMediaSettingsForCall("project-a", "", "outbound", 10, "+12025550100")
	if e != nil || outbound.RTPPlayback.TargetMS != 60 || outbound.AudioProfileCodecs {
		t.Fatal("direction leaked", outbound, e)
	}
	pinned := newRTPJitterBufferWithPolicy(*incoming.RTPPlayback)
	revision := pinned.policy.ProfileRevision
	save(map[string]any{"route_id": target["route_id"], "config": map[string]any{"preset": "reference", "codecs": []string{"G722"}}, "expires_at": time.Now().Add(time.Hour).Format(time.RFC3339Nano)})
	next, e := a.localMediaSettingsForCall("project-a", target["route_id"].(string), "inbound", 10, "")
	if e != nil || next.RTPPlayback.Receiver != "reference" || next.RTPPlayback.ProfileRevision == revision {
		t.Fatal(next, e)
	}
	if pinned.policy.TargetMS != 80 || pinned.policy.Receiver != "adaptive" || pinned.policy.ProfileRevision != revision {
		t.Fatal("active policy changed")
	}
	// Expiration is evaluated at setup; no worker or restart is required.
	occupancyExec(t, ctx.AppDB(), `UPDATE sip_audio_profiles SET expires_at=? WHERE scope_key LIKE 'inbound/route/%'`, time.Now().Add(-time.Second).Format(time.RFC3339Nano))
	reverted, e := a.localMediaSettingsForCall("project-a", target["route_id"].(string), "inbound", 10, "")
	if e != nil || reverted.RTPPlayback.TargetMS != 80 || reverted.Codecs[0] != "PCMA" {
		t.Fatal("expiry did not inherit", reverted, e)
	}
	save(map[string]any{"route_id": target["route_id"], "reset": true})
	foreign, e := a.localMediaSettingsForCall("project-b", "", "inbound", 10, "")
	if e != nil || foreign.RTPPlayback.TargetMS != 60 || foreign.AudioProfileCodecs {
		t.Fatal("project isolation", foreign, e)
	}
	save(map[string]any{"direction": "outbound", "carrier_connection_id": 10, "config": map[string]any{"preset": "resilient"}})
	save(map[string]any{"direction": "outbound", "carrier_connection_id": 10, "phone_number": "+12025550100", "config": map[string]any{"preset": "balanced", "codecs": []string{"PCMU"}}})
	own, e := a.localMediaSettingsForCall("project-a", "", "outbound", 10, "+12025550100")
	if e != nil || own.RTPPlayback.TargetMS != 60 || own.Codecs[0] != "PCMU" {
		t.Fatal(own, e)
	}
	other, e := a.localMediaSettingsForCall("project-a", "", "outbound", 10, "+12025550101")
	if e != nil || other.RTPPlayback.TargetMS != 80 {
		t.Fatal("number override leaked", other, e)
	}
}

func TestSIPAudioProfilesFailClosed(t *testing.T) {
	a, ctx, _ := carrierMediaFixture(t, "inbound")
	for _, args := range []map[string]any{
		{"direction": "wrong"}, {"route_id": "foreign"}, {"direction": "outbound", "carrier_connection_id": 999},
		{"direction": "inbound", "carrier_connection_id": 10}, {"config": nil}, {"config": map[string]any{"preset": "unknown"}},
		{"config": map[string]any{"rtp_playback": map[string]any{"max_age_ms": 401}}},
		{"config": map[string]any{"rtp_playback": map[string]any{"receiver": "off"}}},
		{"config": map[string]any{"codecs": []string{"OPUS"}}},
		{"config": map[string]any{"preset": "balanced"}, "expires_at": "yesterday"},
		{"config": map[string]any{"preset": "balanced"}, "expires_at": time.Now().Add(25 * time.Hour).Format(time.RFC3339Nano)},
		{"config": map[string]any{"preset": "balanced"}, "reset": true},
	} {
		if r, e := a.sipAudioProfileSettings(ctx, args); e == nil || r != nil {
			t.Fatalf("bad policy accepted: %v %v %v", args, r, e)
		}
	}
	for _, tool := range a.MCPTools() {
		if tool.Name == "telephony_sip_audio_profile" {
			if _, e := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 7}), ctx, nil); e == nil {
				t.Fatal("agent administered profiles")
			}
			if _, e := tool.HandlerCtx(sdk.WithCaller(context.Background(), &sdk.Caller{SubjectType: "user", SubjectID: "1"}), ctx, nil); e == nil {
				t.Fatal("app user administered profiles")
			}
		}
	}
	occupancyExec(t, ctx.AppDB(), `DROP TABLE sip_audio_profiles`)
	if _, e := a.localMediaSettingsForCall("project-a", "", "inbound", 10, ""); e == nil {
		t.Fatal("read failure became a default policy")
	}
}

func TestSIPAdaptiveLocalAgeSeparateFromRelativeTiming(t *testing.T) {
	for _, mode := range []string{"adaptive", "reference"} {
		t.Run(mode, func(t *testing.T) {
			b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Receiver: mode})
			base := time.Now()
			b.haveSourceAnchor = true
			b.sourceAnchorAt = base.Add(-350 * time.Millisecond)
			for i := 0; i < 5; i++ {
				at := base.Add(time.Duration(i) * 20 * time.Millisecond)
				b.pushRTPAt(uint16(i), make([]byte, 160), uint32(i*160), at, at)
			}
			if mode == "adaptive" {
				if b.lateDroppedSamples != 0 || b.buffered != 800 {
					t.Fatal("obsolete phase blocked fresh packets")
				}
			} else if b.lateDroppedSamples != 800 {
				t.Fatal("reference comparison changed")
			}
			// Physical residence always wins, irrespective of phase recovery.
			b.pushRTPAt(5, make([]byte, 160), 800, base.Add(100*time.Millisecond), base.Add(400*time.Millisecond))
			if b.events[(b.eventHead+63)%64].Reason != "ingress_processing_age" {
				t.Fatal("physical stale protection missing")
			}
			b.popAt(base.Add(time.Second))
			if b.buffered != 0 {
				t.Fatal("retained old packets did not expire")
			}
		})
	}
}

func TestSIPAdaptiveContinuousOutputAndHold(t *testing.T) {
	for _, codec := range []string{"PCMA", "PCMU", "G722"} {
		t.Run(codec, func(t *testing.T) {
			b := newRTPJitterBuffer()
			m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			f := newRTPAudioFramer(b, m.offer)
			base := time.Now()
			encoder := &sipRTPMedia{offer: m.offer}
			if _, ready := f.popPCMAt(m, base); ready {
				t.Fatal("silence started before any speech")
			}
			for i := 0; i < 6; i++ {
				b.pushRTPAt(uint16(i), encoder.encodeAudio(make([]int16, m.sampleRate()/50)), uint32(i*160), base.Add(time.Duration(i)*20*time.Millisecond), base.Add(time.Duration(i)*20*time.Millisecond))
			}
			for i := 6; i < 100; i++ {
				if pcm, ready := f.popPCMAt(m, base.Add(time.Duration(i)*20*time.Millisecond)); !ready || len(pcm) != m.sampleRate()/50 {
					t.Fatal("active output stalled", i)
				}
			}
			if b.lost != 0 {
				t.Fatal("silence counted as lost RTP")
			}
			b.setPaused(true, base.Add(2*time.Second))
			if _, ready := f.popPCMAt(m, base.Add(2*time.Second)); ready {
				t.Fatal("intentional hold generated audio")
			}
		})
	}
}

func TestSIPKernelClockStepsCannotFreshenBacklog(t *testing.T) {
	for _, jump := range []time.Duration{time.Second, -time.Second} {
		t.Run(jump.String(), func(t *testing.T) {
			var c sipKernelClock
			base := time.Now()
			wall := base.UnixNano()
			stamp := time.Unix(0, wall-10*int64(time.Millisecond))
			if at, ok := c.arrival(stamp.Unix(), int64(stamp.Nanosecond()), base, wall); !ok || base.Sub(at) != 10*time.Millisecond {
				t.Fatal("normal residence changed")
			}
			read := base.Add(20 * time.Millisecond)
			newWall := wall + int64(20*time.Millisecond+jump)
			old := time.Unix(0, wall)
			if at, ok := c.arrival(old.Unix(), int64(old.Nanosecond()), read, newWall); ok || read.Sub(at) < time.Second {
				t.Fatal("clock boundary relabeled queued audio", at, ok)
			}
			fresh := time.Unix(0, newWall+int64(20*time.Millisecond))
			if at, ok := c.arrival(fresh.Unix(), int64(fresh.Nanosecond()), read.Add(20*time.Millisecond), newWall+int64(20*time.Millisecond)); !ok || !at.Equal(read.Add(20*time.Millisecond)) {
				t.Fatal("fresh post-step audio rejected", at, ok)
			}
			if c.steps != 1 || c.uncertain != 1 {
				t.Fatal("clock incident not retained", c)
			}
		})
	}
}

func TestSIPForwardingUsesRemainingResidenceBudget(t *testing.T) {
	left, right := net.Pipe()
	defer right.Close()
	p := newWebSocketWriterPump(left, ws.StateServerSide)
	defer p.Stop()
	p.sipResidenceBudget.Store(int64(200 * time.Millisecond))
	p.queueAudioAt(make([]byte, 960), 0, time.Now().Add(-220*time.Millisecond))
	// A subsequent control write fences processing of the previously queued audio
	// only after the audio writer has consumed it; poll bounded diagnostic state.
	deadline := time.Now().Add(time.Second)
	for p.audioSnapshot().StaleBytes == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if p.audioSnapshot().StaleBytes != 960 {
		t.Fatal("SIP forwarding renewed old audio", p.audioSnapshot())
	}
}

func TestSIPPacketTraceBoundedAndPrivate(t *testing.T) {
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{DiagnosticWindowMS: 1000})
	defer b.releaseTrace()
	if b.trace == nil {
		t.Fatal("capture budget unavailable")
	}
	now := time.Now()
	for i := 0; i < 200; i++ {
		b.pushRTPAt(uint16(i), make([]byte, 160), uint32(i*160), now.Add(time.Duration(i)*20*time.Millisecond), now.Add(time.Duration(i)*20*time.Millisecond))
		b.popAt(now.Add(time.Duration(i) * 20 * time.Millisecond))
	}
	if b.trace.size != 50 {
		t.Fatal("capture is not bounded", b.trace.size)
	}
	raw, _ := json.Marshal(b.trace.snapshot())
	for _, word := range []string{"token", "password", "payload\"", "sip_username", "audio_bridge"} {
		if strings.Contains(string(raw), word) {
			t.Fatal("private capture field", word)
		}
	}
}

func TestSIPPacketTraceSamplingHasFixedIncidentBudget(t *testing.T) {
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{DiagnosticWindowMS: 1000})
	defer b.releaseTrace()
	now := time.Now()
	b.pushRTPAt(1, make([]byte, 160), 0, now, now)
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMA", PacketSamples: 160}}
	m.jitterTelemetry.Store(b)
	a := &App{installID: 42}
	d := &sipLiveDiagnostics{app: a, row: callRow{ID: "trace-budget", ProjectID: "project-a"}, id: "trace-budget"}
	d.media.Store(m)
	if len(d.sample(now).SIP.Jitter.PacketTiming) != 0 {
		t.Fatal("routine sample copied the header ring")
	}
	b.droppedSamples = 160
	if len(d.sample(now.Add(time.Second)).SIP.Jitter.PacketTiming) == 0 {
		t.Fatal("incident context missing")
	}
	if len(d.sample(now.Add(2*time.Second)).SIP.Jitter.PacketTiming) != 0 {
		t.Fatal("packet ring copied every second")
	}
	if len(d.sample(now.Add(3*time.Second)).SIP.Jitter.PacketTiming) == 0 {
		t.Fatal("post-incident context missing")
	}
	for i := 4; i < 20; i++ {
		b.droppedSamples += 160
		if len(d.sample(now.Add(time.Duration(i)*time.Second)).SIP.Jitter.PacketTiming) != 0 {
			t.Fatal("repeated incidents exceeded capture budget")
		}
	}
	d.ended.Store(true)
	if len(d.sample(now.Add(20*time.Second)).SIP.Jitter.PacketTiming) == 0 {
		t.Fatal("final context missing")
	}
	if len(d.sample(now.Add(21*time.Second)).SIP.Jitter.PacketTiming) != 0 {
		t.Fatal("final capture repeated")
	}
}

func TestSIPPacketTraceProcessCap(t *testing.T) {
	before := sipPacketTraceSlots.Load()
	var buffers []*rtpJitterBuffer
	defer func() {
		for _, b := range buffers {
			b.releaseTrace()
		}
	}()
	enabled := 0
	for i := 0; i < 70; i++ {
		b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{DiagnosticWindowMS: 20000})
		buffers = append(buffers, b)
		if b.trace != nil {
			enabled++
		}
	}
	if int64(enabled) != 64-before || sipPacketTraceSlots.Load() > 64 {
		t.Fatal("global diagnostic memory cap failed", enabled, before)
	}
}

func TestSIPRuntimeEvidenceIsSharedAndCached(t *testing.T) {
	var collector sipRuntimeCollector
	now := time.Now()
	a, b := collector.read(now), collector.read(now)
	if !a.Available || a.At == "" || a.At != b.At {
		t.Fatal("runtime evidence unavailable or repeated collection", a, b)
	}
}

func BenchmarkSIPRuntimeEvidenceCached(b *testing.B) {
	sipRuntimeTiming()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		sipRuntimeTiming()
	}
}

func BenchmarkSIPAdaptiveIngress(b *testing.B) {
	for _, mode := range []string{"adaptive", "reference"} {
		b.Run(mode, func(b *testing.B) {
			j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Receiver: mode})
			base := time.Now()
			payload := make([]byte, 160)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				at := base.Add(time.Duration(i) * 20 * time.Millisecond)
				j.pushRTPAt(uint16(i), payload, uint32(i*160), at, at)
				j.popAt(at)
			}
		})
	}
}

func TestSIPAudioProfilePresetBounds(t *testing.T) {
	for _, preset := range []string{"balanced", "resilient", "reference", "custom"} {
		p, e := resolveSIPAudioProfile(sipAudioProfile{Preset: preset})
		if e != nil {
			t.Fatal(preset, e)
		}
		t.Log(fmt.Sprintf("%s: %+v", preset, p.Playback))
	}
}

func TestSIPG722WarmupPreservesDecoderOrderAndIsBounded(t *testing.T) {
	offer := sipMediaOffer{Codec: "G722", PacketSamples: 160}
	enc, m, reference := &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}
	var packets [][]byte
	for frame := 0; frame < 8; frame++ {
		pcm := make([]int16, 320)
		for i := range pcm {
			pcm[i] = int16(4000 * math.Sin(float64(frame*320+i)*.05))
		}
		packets = append(packets, enc.encodeAudio(pcm))
	}
	m.decodeAudio(packets[0])
	reference.decodeAudio(packets[0])
	reference.decodeAudio(packets[1])
	reference.decodeAudio(packets[2])
	b := newRTPJitterBuffer()
	b.rememberSkippedDecode(160, packets[1])
	b.rememberSkippedDecode(320, packets[2])
	b.rememberSkippedDecode(640, packets[4])
	b.lastPoppedTimestamp = 480
	b.warmDecoder(m)
	if !bytes.Equal(pcm16ToBytes(m.decodeAudio(packets[3])), pcm16ToBytes(reference.decodeAudio(packets[3]))) {
		t.Fatal("discarded packets corrupted ADPCM state")
	}
	if b.decodeSkipCount != 1 || b.decodeSkips[0].ts != 640 || b.decodeWarmups != 2 {
		t.Fatal("future packet decoded before its predecessor")
	}
	b.lastPoppedTimestamp = 800
	b.warmDecoder(m)
	reference.decodeAudio(packets[4])
	if !bytes.Equal(pcm16ToBytes(m.decodeAudio(packets[5])), pcm16ToBytes(reference.decodeAudio(packets[5]))) {
		t.Fatal("deferred warm-up reordered audio")
	}
	for i := 0; i < 100; i++ {
		b.rememberSkippedDecode(uint32(1000+i*160), packets[0])
	}
	total := 0
	for i := 0; i < b.decodeSkipCount; i++ {
		total += len(b.decodeSkips[i].payload)
	}
	if b.decodeSkipCount > 4 || total > 640 || !b.decodeSkipUnknown {
		t.Fatal("decoder catch-up is unbounded")
	}
	b.lastPoppedTimestamp = 20000
	b.warmDecoder(m)
	if b.decodeResets != 1 || b.decodeSkipPending.Load() {
		t.Fatal("oversized backlog did not reset bounded decoder state")
	}
}

func TestSIPTimestampRestartReleasesOldEpoch(t *testing.T) {
	b := newRTPJitterBuffer()
	base := time.Now()
	for i := 0; i < 4; i++ {
		b.pushSourceRTPAt(42, uint16(i), make([]byte, 160), uint32(1000+i*160), base.Add(time.Duration(i)*20*time.Millisecond), base.Add(time.Duration(i)*20*time.Millisecond))
	}
	b.pushSourceRTPAt(42, 4, make([]byte, 160), 0, base.Add(80*time.Millisecond), base.Add(80*time.Millisecond))
	if b.clockResets != 1 || b.generation.Load() != 1 || b.buffered != 160 || b.resetDroppedSamples != 640 || len(b.packets) != 1 || b.sourceAnchorTS != 0 {
		t.Fatal("timestamp restart mixed source epochs")
	}
}

func TestSIPG722RebufferPreservesDecoderUntilSourceRestart(t *testing.T) {
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "G722", PacketSamples: 160}}
	b := newRTPJitterBuffer()
	f := newRTPAudioFramer(b, m.offer)
	m.decodeAudio(make([]byte, 160))
	decoder := m.g722Decoder
	// A local rebuffer invalidates PCM but does not change the ADPCM source.
	b.generation.Add(1)
	f.popPCMAt(m, time.Now())
	if m.g722Decoder != decoder || b.decodeResets != 0 {
		t.Fatal("ordinary rebuffer reset the stateful decoder")
	}
	b.decoderGeneration.Add(1)
	f.popPCMAt(m, time.Now())
	if m.g722Decoder == decoder || b.decodeResets != 1 {
		t.Fatal("source restart retained obsolete decoder state")
	}
}

func TestSIPHumanHeaderPreservesResidenceAcrossInternalHop(t *testing.T) {
	p := idleAudioWriter()
	defer p.Stop()
	arrival := time.Now().Add(-150 * time.Millisecond)
	pcm := make([]int16, 480)
	for i := range pcm {
		pcm[i] = 3
	}
	queueSIPHumanAudio(p, pcm, arrival, 7)
	frame := <-p.audio
	if !frame.enqueued.Equal(arrival) || sourceAudioHeader(frame.payload) != 64 || !bytes.Equal(frame.payload[64:], pcm16ToBytes(pcm)) {
		t.Fatal("internal hop erased age or changed PCM")
	}
	age := mediaClockMS() - mathFloat64(frame.payload[48:56])
	if age < 150 || age > 250 {
		t.Fatal("source residence not propagated", age)
	}
}

func TestSIPAudioProfileHTTPAndServerRelativeExpiry(t *testing.T) {
	a, ctx, _ := carrierMediaFixture(t, "inbound")
	w := phoneTestRequest(a, nil, "POST", "/numbers/sip-audio-profile", map[string]any{"config": map[string]any{"preset": "balanced"}, "ttl_seconds": 3600})
	if w.Code != 200 {
		t.Fatal(w.Code, w.Body.String())
	}
	var response struct {
		Expires string `json:"expires_at"`
		Active  bool   `json:"active_override"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	until, e := time.Parse(time.RFC3339Nano, response.Expires)
	if e != nil || time.Until(until) < 59*time.Minute || time.Until(until) > 61*time.Minute || !response.Active {
		t.Fatal("expiry depends on browser clock", response, e)
	}
	if _, e = a.sipAudioProfileSettings(ctx, map[string]any{"direction": 123, "config": map[string]any{"preset": "reference"}}); e == nil {
		t.Fatal("malformed scope became project default")
	}
	if _, e = a.sipAudioProfileSettings(ctx, map[string]any{"config": map[string]any{"preset": "balanced"}, "ttl_seconds": 1.5}); e == nil {
		t.Fatal("fractional TTL accepted")
	}
}

func BenchmarkSIPG722BoundedWarmup(b *testing.B) {
	offer := sipMediaOffer{Codec: "G722", PacketSamples: 160}
	m := &sipRTPMedia{offer: offer}
	payload := make([]byte, 160)
	j := newRTPJitterBuffer()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for n := 0; n < 4; n++ {
			j.rememberSkippedDecode(uint32(n*160), payload)
		}
		j.lastPoppedTimestamp = 640
		j.warmDecoder(m)
	}
}

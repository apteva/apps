package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"sync"
	"testing"
	"time"
)

func TestSIPPlaybackPolicyValidation(t *testing.T) {
	for _, p := range []sipPlaybackPolicy{{}, {TargetMS: 80}, {TargetMS: 100, CapacityMS: 200}, {TargetMS: 60, MaxTargetMS: 60, CapacityMS: 120, MaxAgeMS: 120}} {
		if err := p.validate(); err != nil {
			t.Fatal(p, err)
		}
	}
	for _, p := range []sipPlaybackPolicy{{TargetMS: 20}, {TargetMS: 121}, {TargetMS: 80, MaxTargetMS: 60}, {CapacityMS: 120}, {CapacityMS: 401}, {MaxAgeMS: 401}, {CapacityMS: 200, MaxAgeMS: 160}} {
		if err := validateLocalMediaSettings(localMediaSettings{RTPPlayback: &p}); err == nil {
			t.Fatal("unsafe policy accepted", p)
		}
	}
}

func TestSIPPlaybackSettingsAreScopedAndSnapshot(t *testing.T) {
	a, ctx, _ := localSpeechFixture(t)
	p := sipPlaybackPolicy{TargetMS: 80}
	if err := a.saveLocalMediaSettings(ctx, "", localMediaSettings{RTPPlayback: &p}); err != nil {
		t.Fatal(err)
	}
	cfg, err := a.localMediaSettings(ctx.CurrentProject(), "")
	if err != nil || cfg.RTPPlayback == nil {
		t.Fatal(err)
	}
	active := newRTPJitterBufferWithPolicy(*cfg.RTPPlayback)
	p.TargetMS = 90
	if err := a.saveLocalMediaSettings(ctx, "", localMediaSettings{RTPPlayback: &p}); err != nil {
		t.Fatal(err)
	}
	updated, _ := a.localMediaSettings(ctx.CurrentProject(), "")
	other, _ := a.localMediaSettings("project-b", "")
	if active.targetMS != 80 || updated.RTPPlayback.TargetMS != 90 || other.RTPPlayback != nil {
		t.Fatal("active or foreign policy changed")
	}
}

func TestSIPPlaybackConcealmentReducesVoicedGapError(t *testing.T) {
	for _, rate := range []int{8000, 16000} {
		var plc sipPCMConcealer
		pcm := make([]int16, rate/25)
		for i := range pcm {
			pcm[i] = int16(6000 * math.Sin(2*math.Pi*200*float64(i)/float64(rate)))
		}
		plc.received(pcm, rate)
		out := plc.missing(rate/50, rate, true)
		var errorPLC, errorSilence float64
		for i, v := range out {
			want := 6000 * math.Sin(2*math.Pi*200*float64(len(pcm)+i)/float64(rate))
			errorPLC += math.Pow(float64(v)-want, 2)
			errorSilence += want * want
		}
		if errorPLC >= errorSilence*.4 {
			t.Fatal("PLC did not soften periodic speech gap", rate, errorPLC/errorSilence)
		}
	}
}

func TestSIPPlaybackHealthyPCMMatchesCodecDecode(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
		encoder := &sipRTPMedia{offer: m.offer}
		reference := &sipRTPMedia{offer: m.offer}
		j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
		f := newRTPAudioFramer(j, m.offer)
		expected := []int16{}
		actual := []int16{}
		now := time.Unix(100, 0)
		for frame := 0; frame < 100; frame++ {
			pcm := make([]int16, m.sampleRate()/50)
			for i := range pcm {
				pcm[i] = int16(5000 * math.Sin(float64(frame*len(pcm)+i)*.07))
			}
			payload := encoder.encodeAudio(pcm)
			expected = append(expected, reference.decodeAudio(payload)...)
			j.pushRTP(uint16(frame), payload, uint32(frame*160), now.Add(time.Duration(frame)*20*time.Millisecond))
			if out, ready := f.popPCMAt(m, now.Add(time.Duration(frame)*20*time.Millisecond)); ready {
				actual = append(actual, out...)
			}
		}
		if len(actual) == 0 || !bytes.Equal(pcm16ToBytes(actual), pcm16ToBytes(expected[:len(actual)])) {
			t.Fatal("healthy codec audio changed", codec)
		}
		if j.tailPaddedFrames != 0 || j.tailPaddingNS != 0 {
			t.Fatal("healthy continuous speech was padded", codec)
		}
	}
}

func TestSIPPlaybackTelemetryRetainsStagesAndEvents(t *testing.T) {
	a, _ := withTelephonyTestContext(t, &answerPlatform{})
	row := recoveryCall(t, a, "sip-jitter-evidence", "telnyx")
	d := a.newSIPDiagnostics(row, "dialog")
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
	d.attach(m, "dialog")
	b := newRTPJitterBuffer()
	m.jitterTelemetry.Store(b)
	now := time.Now()
	for i := 0; i < 20; i++ {
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), now)
	}
	b.popAt(now.Add(time.Second))
	sample := d.sample(now.Add(time.Second))
	if sample.Kind != "incident" || sample.SIP.Jitter.OverflowDroppedMS == 0 || sample.SIP.Jitter.ExpiredDroppedMS == 0 || len(sample.SIP.Jitter.Events) == 0 || sample.SIP.Jitter.SourceClockConfidence != "relative_rtp_baseline" {
		t.Fatal("missing jitter evidence", sample)
	}
	if sample.SIP.RTP.SequenceLoss != 0 {
		t.Fatal("local drops called network loss")
	}
	d.end("local", nil)
}
func TestSIPPlaybackMinimumReserveAndShortUtterance(t *testing.T) {
	b := newRTPJitterBuffer()
	now := time.Unix(100, 0)
	b.pushRTP(1, make([]byte, 160), 0, now)
	for i := 0; i < 6; i++ {
		if _, ready := b.popAt(now.Add(time.Duration(i) * 20 * time.Millisecond)); ready {
			t.Fatal("started with only 20ms")
		}
	}
	b.pushRTP(2, make([]byte, 160), 160, now.Add(120*time.Millisecond))
	if _, ready := b.popAt(now.Add(120 * time.Millisecond)); !ready {
		t.Fatal("short utterance failed to start")
	}
	if b.events[(b.eventHead+63)%64].QueueMS < 40 {
		t.Fatal("no real reserve")
	}
}
func TestSIPPlaybackDropReasonsAndBoundedHistory(t *testing.T) {
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
	now := time.Unix(100, 0)
	for i := 0; i < 12; i++ {
		b.pushRTP(uint16(i), bytes.Repeat([]byte{byte(i)}, 160), uint32(i*160), now)
	}
	if b.overflowSamples != 640 || b.buffered > 1280 {
		t.Fatal(b.overflowSamples, b.buffered)
	}
	b.popAt(now)
	b.pushRTP(0, make([]byte, 160), 0, now) // Duplicate never counted as fresh loss.
	b.pushRTP(65535, make([]byte, 160), 0, now)
	b.popAt(now.Add(201 * time.Millisecond))
	if b.lateDroppedSamples != 160 || b.expiredSamples == 0 || b.droppedSamples != b.overflowSamples+b.expiredSamples {
		t.Fatal("drop reasons conflated", b)
	}
	for i := 0; i < 200; i++ {
		b.event(now.Add(time.Duration(i)*time.Second), "test", uint16(i), 0, 160, 0)
	}
	if b.eventCount != 64 {
		t.Fatal("unbounded history")
	}
	if b.events[(b.eventHead+63)%64].At.IsZero() {
		t.Fatal("missing timestamp")
	}
}
func TestSIPPlaybackDTMFAndDTXAreNotMissingSpeech(t *testing.T) {
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
	now := time.Unix(100, 0)
	b.pushRTP(1, make([]byte, 160), 0, now)
	b.control(2)
	b.pushRTP(3, make([]byte, 160), 160, now.Add(20*time.Millisecond))
	b.pushRTP(4, make([]byte, 160), 320, now.Add(40*time.Millisecond))
	for i := 0; i < 3; i++ {
		p, ready := b.popAt(now.Add(time.Duration(i+3) * 20 * time.Millisecond))
		if !ready || len(p) == 0 {
			t.Fatal("telephone event became silence")
		}
	}
	for i := 0; i < 15; i++ {
		b.popAt(now.Add(time.Duration(i+6) * 20 * time.Millisecond))
	}
	if b.lost != 0 {
		t.Fatal("empty queue classified as carrier loss", b.lost)
	}
	for i := 0; i < 3; i++ {
		b.pushRTP(uint16(5+i), make([]byte, 160), uint32(8000+i*160), now.Add(time.Second+time.Duration(i)*20*time.Millisecond))
	}
	if p, ready := b.popAt(now.Add(1060 * time.Millisecond)); !ready || len(p) == 0 || b.lost != 0 {
		t.Fatal("DTX did not resume")
	}
}
func TestSIPPlaybackPacketTimesWrapAndOldPartial(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
			f := newRTPAudioFramer(b, m.offer)
			now := time.Unix(100, 0)
			pcm := make([]int16, m.sampleRate()*18/1000)
			for i := range pcm {
				pcm[i] = 2000
			}
			p := m.encodeAudio(pcm)
			for i := 0; i < 4; i++ {
				b.pushRTP(uint16(65534+i), p, 0xfffff000+uint32(i*len(p)), now)
			}
			if out, ready := f.popPCMAt(m, now); !ready || len(out) != m.sampleRate()/50 {
				t.Fatal("partial framing/wrap failed")
			}
			for i := 0; i < 3; i++ {
				b.pushRTP(uint16(10+i), m.encodeAudio(make([]int16, m.sampleRate()/50)), uint32(2000+i*160), now.Add(time.Second))
			}
			out, ready := f.popPCMAt(m, now.Add(time.Second))
			if ready {
				for _, v := range out {
					if math.Abs(float64(v)) > 1000 {
						t.Fatal("stale decoded partial replayed")
					}
				}
			}
			if b.expiredSamples == 0 {
				t.Fatal("expiry protection removed")
			}
		})
	}
}
func TestSIPPlaybackConcealmentFadeAndHealthyQuality(t *testing.T) {
	for _, rate := range []int{8000, 16000} {
		var p sipPCMConcealer
		pcm := make([]int16, rate/25)
		for i := range pcm {
			pcm[i] = int16(8000 * math.Sin(2*math.Pi*200*float64(i)/float64(rate)))
		}
		original := append([]int16(nil), pcm...)
		got := p.received(pcm, rate)
		if !bytes.Equal(pcm16ToBytes(original), pcm16ToBytes(got)) {
			t.Fatal("healthy speech changed")
		}
		var previousEnergy float64 = math.MaxFloat64
		for frame := 0; frame < 5; frame++ {
			x := p.missing(rate/50, rate, true)
			energy := 0.0
			for _, v := range x {
				energy += float64(v) * float64(v)
			}
			if energy > previousEnergy+1 {
				t.Fatal("concealment grows louder")
			}
			previousEnergy = energy
			if frame == 0 && energy == 0 {
				t.Fatal("short loss still silence")
			}
			if frame >= 3 && energy != 0 {
				t.Fatal("long outage invents speech")
			}
		}
		p.received(append([]int16(nil), original...), rate)
		if x := p.missing(rate/50, rate, false); !bytes.Equal(pcm16ToBytes(x), make([]byte, len(x)*2)) {
			t.Fatal("disabled PLC synthesized audio")
		}
	}
}
func TestSIPPlaybackConcurrentCollection(t *testing.T) {
	b := newRTPJitterBuffer()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), time.Now())
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			b.pop()
		}
	}()
	wg.Wait()
	if b.buffered > b.policy.CapacityMS*8 || b.eventCount > 64 {
		t.Fatal("unbounded concurrent buffer")
	}
}

func TestSIPPlaybackShortPacketsConcealmentAndLongLoss(t *testing.T) {
	for _, rate := range []int{8000, 16000} {
		for _, ms := range []int{10, 18, 20, 40} {
			var plc sipPCMConcealer
			pcm := make([]int16, rate*ms/1000)
			for i := range pcm {
				pcm[i] = int16(3000 * math.Sin(float64(i)*0.1))
			}
			plc.received(pcm, rate)
			if out := plc.missing(rate/50, rate, true); len(out) != rate/50 {
				t.Fatal("short packet PLC failed")
			}
		}
	}
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		b.pushRTP(uint16(i+1), make([]byte, 160), uint32(i*160), now)
	}
	for i := 0; i < 3; i++ {
		b.popAt(now.Add(time.Duration(i) * 20 * time.Millisecond))
	}
	b.pushRTP(47, bytes.Repeat([]byte{47}, 160), 46*160, now.Add(60*time.Millisecond))
	for i := 0; i < 4; i++ {
		p, _ := b.popAt(now.Add(time.Duration(i+3) * 20 * time.Millisecond))
		if i == 3 && (len(p) == 0 || p[0] != 47) {
			t.Fatal("loss held up new speech beyond 60ms")
		}
	}
	if b.lost != 43 {
		t.Fatal("wrong missing packet estimate", b.lost)
	}
}

func TestSIPPlaybackDecodedReserveCountsTowardCapacity(t *testing.T) {
	b := newRTPJitterBuffer()
	b.pendingPCMSamples.Store(800)
	b.pushRTP(1, make([]byte, 640), 0, time.Now())
	if b.buffered+int(b.pendingPCMSamples.Load()) > b.policy.CapacityMS*8 || b.overflowSamples != 640 {
		t.Fatal("decoded reserve bypassed queue cap")
	}
}

func TestSIPPlaybackIntentionalHoldAllowsFrozenSourceClock(t *testing.T) {
	for _, codec := range []string{"PCMU", "G722"} {
		m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
		j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
		f := newRTPAudioFramer(j, m.offer)
		now := time.Unix(100, 0)
		payload := m.encodeAudio(make([]int16, m.sampleRate()/50))
		for i := 0; i < 3; i++ {
			j.pushRTP(uint16(i), payload, uint32(i*160), now)
		}
		f.popPCMAt(m, now)
		j.setPaused(true, now)
		if _, ready := f.popPCMAt(m, now); ready {
			t.Fatal("held speech continued")
		}
		j.setPaused(false, now.Add(5*time.Second))
		for i := 3; i < 6; i++ {
			j.pushRTP(uint16(i), payload, uint32(i*160), now.Add(5*time.Second))
		}
		if _, ready := f.popPCMAt(m, now.Add(5*time.Second)); !ready || j.lateDroppedSamples != 0 || j.lost != 0 || j.holdSkippedSamples == 0 {
			t.Fatal("hold mistaken for transport delay")
		}
	}
}

func TestSIPPlaybackSourceChangeDoesNotExpireNewClock(t *testing.T) {
	j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
	now := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		j.pushSourceRTP(1, uint16(100+i), make([]byte, 160), uint32(i*160), now)
	}
	j.popAt(now)
	for i := 0; i < 3; i++ {
		j.pushSourceRTP(2, uint16(i), bytes.Repeat([]byte{2}, 160), uint32(i*160), now.Add(time.Minute))
	}
	if p, ready := j.popAt(now.Add(time.Minute)); !ready || len(p) == 0 || p[0] != 2 || j.lateDroppedSamples != 0 || j.resetDroppedSamples == 0 {
		t.Fatal("fresh SSRC inherited the old clock")
	}
}

type sipReplayPacket struct {
	at      time.Duration
	seq     uint16
	ts      uint32
	payload []byte
}
type sipReplayResult struct {
	Profile             string  `json:"profile"`
	Codec               string  `json:"codec"`
	Capacity            int     `json:"capacity_ms"`
	Target              int     `json:"target_ms"`
	DroppedMS           float64 `json:"discarded_ms"`
	ReplacementMS       float64 `json:"replacement_ms"`
	LegacyDroppedMS     float64 `json:"legacy_discarded_ms"`
	LegacyReplacementMS float64 `json:"legacy_replacement_ms"`
	MaxQueueMS          float64 `json:"max_queue_ms"`
	MaxResidenceMS      int     `json:"max_residence_ms"`
	MissingPackets      int     `json:"estimated_playout_missing_packets"`
	DriftPPM            float64 `json:"drift_ppm"`
	AdjustedSamples     int64   `json:"adjusted_samples"`
	Frames              int     `json:"output_frames"`
}

func replaySIP(t *testing.T, profile, codec string, target, capacity int, seconds int, drift float64, speech []int16) sipReplayResult {
	t.Helper()
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	enc := &sipRTPMedia{offer: m.offer}
	rate := m.sampleRate()
	p := sipPlaybackPolicy{TargetMS: target, MaxTargetMS: 100, CapacityMS: capacity, MaxAgeMS: 200}
	b := newRTPJitterBufferWithPolicy(p)
	f := newRTPAudioFramer(b, m.offer)
	old := newLegacySIPJitterBuffer()
	packets := []sipReplayPacket{}
	seq := uint16(1)
	for frame := 0; frame < int(math.Ceil(float64(seconds*50)*(1+drift/1e6)))+2; frame++ {
		source := time.Duration(float64(frame) * float64(20*time.Millisecond) / (1 + drift/1e6))
		delay := time.Duration(0)
		switch profile {
		case "slow_jitter":
			delay = time.Duration((1 + math.Sin(float64(frame)/500)) * 17.5 * float64(time.Millisecond))
		case "clock_jitter":
			delay = time.Duration(frame%2) * 12 * time.Millisecond
		case "ordinary_jitter":
			delay = time.Duration((frame*17)%41) * time.Millisecond
		case "80ms_batches":
			if frame%50 < 4 {
				delay = time.Duration(4-frame%50) * 20 * time.Millisecond
			}
		case "180ms_batches":
			if frame%50 < 9 {
				delay = time.Duration(9-frame%50) * 20 * time.Millisecond
			}
		case "loss":
			if frame%37 == 12 {
				seq++
				continue
			}
		case "scheduling_pause":
			if frame >= 100 && frame < 120 {
				delay = time.Duration(120-frame) * 20 * time.Millisecond
			}
		case "ten_second_catchup":
			if frame >= 100 && frame < 600 {
				delay = time.Duration(600-frame) * 20 * time.Millisecond
			}
		case "dtx":
			if frame%150 >= 80 && frame%150 < 120 {
				continue
			}
		}
		pcm := make([]int16, rate/50)
		for i := range pcm {
			if len(speech) > 0 {
				pcm[i] = speech[(frame*len(pcm)+i)%len(speech)]
			} else {
				x := float64(frame*len(pcm)+i) / float64(rate)
				pcm[i] = int16(5000*math.Sin(2*math.Pi*180*x) + 2000*math.Sin(2*math.Pi*670*x))
			}
		}
		packets = append(packets, sipReplayPacket{at: source + delay, seq: seq, ts: uint32(frame * 160), payload: enc.encodeAudio(pcm)})
		seq++
	}
	sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
	start := time.Unix(100, 0)
	next := 0
	r := sipReplayResult{Profile: profile, Codec: codec, Target: target, Capacity: capacity}
	for tick := time.Duration(0); tick < time.Duration(seconds)*time.Second; tick += 20 * time.Millisecond {
		now := start.Add(tick)
		for next < len(packets) && packets[next].at <= tick {
			q := packets[next]
			b.pushRTP(q.seq, q.payload, q.ts, start.Add(q.at))
			old.pushRTP(q.seq, q.payload, q.ts, start.Add(q.at))
			next++
		}
		r.MaxQueueMS = max(r.MaxQueueMS, float64(int64(b.buffered)+b.pendingPCMSamples.Load())/8)
		if out, ready := f.popPCMAt(m, now); ready {
			if len(out) != rate/50 {
				t.Fatal("invalid frame duration")
			}
			r.Frames++
		}
		old.nowTime = now
		if payload, ready := old.pop(); ready && payload == nil {
			r.LegacyReplacementMS += 20
		}
	}
	r.DroppedMS = float64(b.droppedSamples+b.lateDroppedSamples) / 8
	r.ReplacementMS = float64(b.missingFilledSamples.Load()) / 8
	r.LegacyDroppedMS = float64(old.droppedSamples+old.lateDroppedSamples) / 8
	r.MaxResidenceMS = b.maxAgeMS
	r.MissingPackets = b.lost
	r.DriftPPM = b.driftPPM
	r.AdjustedSamples = b.driftAdjustedSamples.Load()
	if r.MaxQueueMS > float64(capacity) || b.eventCount > 64 {
		t.Fatal("latency bound violated", r)
	}
	return r
}
func TestSIPPlaybackNetworkReplay(t *testing.T) {
	results := []sipReplayResult{}
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, profile := range []string{"normal", "ordinary_jitter", "80ms_batches", "180ms_batches", "loss", "scheduling_pause", "dtx", "ten_second_catchup"} {
			for _, target := range []int{60, 80, 100} {
				for _, capacity := range []int{160, 200} {
					seconds := 12
					if profile == "ten_second_catchup" {
						seconds = 15
					}
					r := replaySIP(t, profile, codec, target, capacity, seconds, 0, nil)
					results = append(results, r)
					if profile == "normal" && (r.DroppedMS != 0 || r.ReplacementMS != 0) {
						t.Fatal("healthy stream degraded", r)
					}
					if profile == "dtx" && r.MissingPackets != 0 {
						t.Fatal("DTX counted as network loss", r)
					}
				}
			}
		}
	}
	for _, ppm := range []float64{-300, 300} {
		r := replaySIP(t, "normal", "PCMU", 60, 160, 120, ppm, nil)
		results = append(results, r)
		if r.DroppedMS > 20 || r.ReplacementMS > 40 || math.Abs(r.DriftPPM) < 50 {
			t.Fatal("clock drift not compensated", r)
		}
	}
	if path := os.Getenv("TELEPHONY_SIP_REPLAY_RESULTS"); path != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range results {
		t.Logf("%+v", r)
	}
}
func TestSIPPlaybackRealSpeechReplay(t *testing.T) {
	path := os.Getenv("TELEPHONY_SIP_SPEECH_WAV")
	if path == "" {
		t.Skip("optional local speech WAV")
	}
	speech, err := readLocalPromptWAV(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		rate := 8000
		if codec == "G722" {
			rate = 16000
		}
		input := newPCMResampler(24000, rate).Process(speech)
		for _, profile := range []string{"normal", "ordinary_jitter", "80ms_batches", "loss"} {
			t.Logf("speech %+v", replaySIP(t, profile, codec, 80, 160, 30, 0, input))
		}
	}
}
func BenchmarkSIPPlayback(b *testing.B) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, loss := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/loss_%t", codec, loss), func(b *testing.B) {
				m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
				j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
				f := newRTPAudioFramer(j, m.offer)
				pcm := make([]int16, m.sampleRate()/50)
				for i := range pcm {
					pcm[i] = int16(5000 * math.Sin(2*math.Pi*200*float64(i)/float64(m.sampleRate())))
				}
				payload := m.encodeAudio(pcm)
				at := time.Unix(100, 0)
				for i := 0; i < 3; i++ {
					j.pushSourceRTP(1, uint16(i), payload, uint32(i*160), at.Add(time.Duration(i)*20*time.Millisecond))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					now := at.Add(time.Duration(i+3) * 20 * time.Millisecond)
					if !loss || i%37 != 12 {
						j.pushSourceRTP(1, uint16(i+3), payload, uint32((i+3)*160), now)
					}
					f.popPCMAt(m, now)
				}
			})
		}
	}
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("queue_only/legacy_%t", legacy), func(b *testing.B) {
			payload := make([]byte, 160)
			at := time.Unix(100, 0)
			j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
			old := newLegacySIPJitterBuffer()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				now := at.Add(time.Duration(i) * 20 * time.Millisecond)
				if legacy {
					old.pushRTP(uint16(i), payload, uint32(i*160), now)
					old.nowTime = now
					old.pop()
				} else {
					j.pushSourceRTP(1, uint16(i), payload, uint32(i*160), now)
					j.popAt(now)
				}
			}
		})
	}
}

func BenchmarkSIPPlaybackLegacyFrame(b *testing.B) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, loss := range []bool{false, true} {
			b.Run(fmt.Sprintf("%s/loss_%t", codec, loss), func(b *testing.B) {
				m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
				j := newLegacySIPJitterBuffer()
				f := newLegacySIPAudioFramer(j, m.offer)
				pcm := make([]int16, m.sampleRate()/50)
				for i := range pcm {
					pcm[i] = int16(5000 * math.Sin(2*math.Pi*200*float64(i)/float64(m.sampleRate())))
				}
				payload := m.encodeAudio(pcm)
				at := time.Unix(100, 0)
				for i := 0; i < 3; i++ {
					j.pushRTP(uint16(i), payload, uint32(i*160), at.Add(time.Duration(i)*20*time.Millisecond))
				}
				b.ReportAllocs()
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					now := at.Add(time.Duration(i+3) * 20 * time.Millisecond)
					if !loss || i%37 != 12 {
						j.pushRTP(uint16(i+3), payload, uint32((i+3)*160), now)
					}
					j.nowTime = now
					f.popPCM(m)
				}
			})
		}
	}
}

func BenchmarkSIPPlaybackConcurrentCalls(b *testing.B) {
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		m := &sipRTPMedia{offer: sipMediaOffer{Codec: "G722", PacketSamples: 160}}
		j := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
		f := newRTPAudioFramer(j, m.offer)
		payload := m.encodeAudio(make([]int16, 320))
		at := time.Unix(100, 0)
		frame := 0
		for pb.Next() {
			now := at.Add(time.Duration(frame) * 20 * time.Millisecond)
			j.pushSourceRTP(1, uint16(frame), payload, uint32(frame*160), now)
			f.popPCMAt(m, now)
			frame++
		}
	})
}

func BenchmarkSIPPlaybackTelemetryHistory(b *testing.B) {
	now := time.Now()
	j := newRTPJitterBuffer()
	for i := 0; i < 64; i++ {
		j.event(now.Add(time.Duration(i)*time.Second), "queue_age", uint16(i), uint32(i*160), 160, 180*time.Millisecond)
	}
	d := &sipLiveDiagnostics{}
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "G722"}}
	m.jitterTelemetry.Store(j)
	d.media.Store(m)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s := d.snapshot(now)
		if _, err := json.Marshal(s); err != nil {
			b.Fatal(err)
		}
	}
}

package main

import (
	"math"
	"math/rand"
	"sort"
	"testing"
	"time"
)

// Review A: receive-path regressions. A small deterministic harness drives the
// production jitter buffer and PCM framer with an arrival schedule on a virtual
// 20 ms playout clock (8 ms phase, as the speech comparison uses).
type zzraPacket struct {
	seq uint16
	ts  uint32
	at  time.Duration
}

type zzraResult struct {
	SentMS, PlayedMS, LateDropMS, DroppedMS, OverflowMS, ConcealMS float64
	MedianLatencyMS, MaxLatencyMS                                  float64
	Targets                                                        []int // per second
	DriftPPM                                                       float64
	b                                                              *rtpJitterBuffer
}

func zzraRun(t *testing.T, p sipPlaybackPolicy, packets []zzraPacket, duration time.Duration) zzraResult {
	t.Helper()
	offer := sipMediaOffer{Codec: "PCMU", PacketSamples: 160}
	m := &sipRTPMedia{offer: offer}
	b := newRTPJitterBufferWithPolicy(p)
	f := newRTPAudioFramer(b, offer)
	pcm := make([]int16, 160)
	for i := range pcm {
		pcm[i] = int16(4000 * math.Sin(2*math.Pi*float64(i)/40))
	}
	payload := m.encodeAudio(pcm)
	sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
	base := time.Unix(100, 0)
	var r zzraResult
	var latencies []float64
	next := 0
	for tick := 8 * time.Millisecond; tick < duration; tick += 20 * time.Millisecond {
		for next < len(packets) && packets[next].at <= tick {
			q := packets[next]
			next++
			at := base.Add(q.at)
			b.pushSourceRTPAt(7, q.seq, payload, q.ts, at, at)
			r.SentMS += 20
		}
		now := base.Add(tick)
		if _, ok := f.popPCMAt(m, now); ok {
			if l := float64(now.Sub(f.frameArrival)) / float64(time.Millisecond); l > 0 {
				latencies = append(latencies, l)
			}
		}
		if int(tick/time.Millisecond)%1000 == 8 {
			b.mu.Lock()
			r.Targets = append(r.Targets, b.targetMS)
			b.mu.Unlock()
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	r.LateDropMS = float64(b.lateDroppedSamples) / 8
	r.DroppedMS = float64(b.droppedSamples) / 8
	r.OverflowMS = float64(b.overflowSamples) / 8
	r.ConcealMS = float64(b.missingFilledSamples.Load()) / 8
	r.PlayedMS = r.SentMS - r.LateDropMS - r.DroppedMS - float64(b.buffered+int(b.pendingPCMSamples.Load()))/8
	r.DriftPPM = b.driftPPM
	if len(latencies) > 0 {
		sort.Float64s(latencies)
		r.MedianLatencyMS = latencies[len(latencies)/2]
		r.MaxLatencyMS = latencies[len(latencies)-1]
	}
	r.b = b
	return r
}

// Scenario generator. skewPPM > 0 is a FASTER sender clock.
func zzraScenario(name string, seconds int) []zzraPacket {
	rng := rand.New(rand.NewSource(42))
	skew := 0.0
	switch name {
	case "skew+1%":
		skew = 10000
	case "skew-1%":
		skew = -10000
	}
	var out []zzraPacket
	seq := uint16(1000)
	ts := uint32(5000)
	shift := time.Duration(0)
	n := int(float64(seconds*50) * (1 + skew/1e6))
	for i := 0; i < n; i++ {
		send := time.Duration(float64(i) * float64(20*time.Millisecond) / (1 + skew/1e6))
		delay := time.Duration(0)
		switch name {
		case "jitter3", "loss1":
			delay = time.Duration(rng.Intn(10)) * time.Millisecond
		case "bursty":
			if i%250 >= 240 {
				delay = time.Duration(250-i%250) * 12 * time.Millisecond
			}
		case "stall":
			// The sender stalls 1.2 s at 10 s and resumes with RTP timestamps
			// continuing (no jump).
			if i == 500 {
				shift = 1200 * time.Millisecond
			}
		}
		seq++
		ts += 160
		if name == "loss1" && rng.Intn(100) == 0 {
			continue
		}
		out = append(out, zzraPacket{seq, ts, send + delay + shift})
	}
	return out
}

func TestZZReviewAStallDoesNotMuteForever(t *testing.T) {
	for _, estimator := range []string{"ewma", "distribution"} {
		r := zzraRun(t, sipPlaybackPolicy{DelayEstimator: estimator}, zzraScenario("stall", 60), 62*time.Second)
		t.Logf("%s %+v", estimator, r)
		// Qualification costs at most the recovery window; never the rest of the call.
		if r.LateDropMS > 1500 || r.PlayedMS < r.SentMS-2000 {
			t.Fatalf("%s: stall muted the stream: late=%.0fms played=%.0f/%.0fms", estimator, r.LateDropMS, r.PlayedMS, r.SentMS)
		}
		if r.b.reanchors != 1 {
			t.Fatal("expected one guarded re-anchor", r.b.reanchors)
		}
	}
}

func TestZZReviewASenderSkewOnePercent(t *testing.T) {
	for _, name := range []string{"skew-1%", "skew+1%"} {
		r := zzraRun(t, sipPlaybackPolicy{}, zzraScenario(name, 150), 151*time.Second)
		t.Logf("%s %+v", name, r)
		if r.LateDropMS != 0 || r.OverflowMS > 100 || r.PlayedMS < r.SentMS-300 {
			t.Fatalf("%s lost audio: %+v", name, r)
		}
		if math.Abs(math.Abs(r.DriftPPM)-10000) > 1500 {
			t.Fatalf("%s skew not tracked: %.0f ppm", name, r.DriftPPM)
		}
	}
}

func TestZZReviewASameSSRCSequenceRebase(t *testing.T) {
	var packets []zzraPacket
	seq, ts := uint16(5000), uint32(900000)
	for i := 0; i < 1500; i++ {
		if i == 500 {
			seq -= 1000
			ts -= 500000
		}
		packets = append(packets, zzraPacket{seq, ts, time.Duration(i) * 20 * time.Millisecond})
		seq++
		ts += 160
	}
	r := zzraRun(t, sipPlaybackPolicy{}, packets, 31*time.Second)
	t.Logf("%+v", r)
	if r.LateDropMS > 100 || r.PlayedMS < r.SentMS-300 {
		t.Fatalf("rebase froze the stream: %+v", r)
	}
	if r.b.sequenceRebases != 1 {
		t.Fatal("rebase not detected", r.b.sequenceRebases)
	}
}

func TestZZReviewASingleReorderedPacketIsNotARebase(t *testing.T) {
	b := newRTPJitterBuffer()
	base := time.Unix(100, 0)
	payload := make([]byte, 160)
	for i := 0; i < 200; i++ {
		at := base.Add(time.Duration(i) * 20 * time.Millisecond)
		b.pushSourceRTPAt(1, uint16(1000+i), payload, uint32(i*160), at, at)
		if i%50 == 49 {
			// One stray very old packet and a duplicate.
			b.pushSourceRTPAt(1, uint16(1000+i-100), payload, uint32((i-100)*160), at, at)
			b.pushSourceRTPAt(1, uint16(1000+i), payload, uint32(i*160), at, at)
		}
	}
	if b.sequenceRebases != 0 || b.clockResets != 0 {
		t.Fatal("isolated reorder/duplicate reset the stream", b.sequenceRebases, b.clockResets)
	}
}

func TestZZReviewAEWMATargetDecaysUnderOrdinaryJitterAndLoss(t *testing.T) {
	r := zzraRun(t, sipPlaybackPolicy{DelayEstimator: "ewma"}, zzraScenario("loss1", 90), 90*time.Second)
	t.Logf("%+v targets=%v", r, r.Targets)
	late := r.Targets[len(r.Targets)-20:]
	for _, v := range late {
		if v > 70 {
			t.Fatalf("target ratcheted under 3 ms jitter and 1%% loss: %v", r.Targets)
		}
	}
}

// One packet > 160 ms late, opening a new two-second window, must not wipe
// the clock-fit history (32 short and 256 long windows).
func TestZZReviewAOneLatePacketKeepsClockHistory(t *testing.T) {
	b := newRTPJitterBuffer()
	base := time.Unix(100, 0)
	payload := make([]byte, 160)
	steps := 0
	for i := 0; i < 50*40; i++ {
		at := base.Add(time.Duration(i) * 20 * time.Millisecond)
		if i >= 50*20 && i < 50*20+10 {
			// A delay spike releases ten packets in one burst 200 ms late.
			at = base.Add(20*time.Second + 200*time.Millisecond)
		}
		b.pushRTPAt(uint16(i), payload, uint32(i*160), at, at)
		if i == 50*20-1 || i == 50*20+1 {
			b.mu.Lock()
			if i == 50*20-1 {
				steps = b.clock.count
			} else if b.clock.count < steps {
				b.mu.Unlock()
				t.Fatalf("history wiped: %d -> %d windows", steps, b.clock.count)
			}
			b.mu.Unlock()
		}
	}
	for i := 0; i < b.clock.fitCount; i++ {
		if b.clock.fits[i].Method == "delivery_step" {
			t.Fatal("single late packet recorded as delivery step")
		}
	}
	if b.clock.count < 19 {
		t.Fatal("history not retained", b.clock.count)
	}
}

// A sustained delivery step is still detected from window minima.
func TestZZReviewASustainedStepStillDetected(t *testing.T) {
	b := newRTPJitterBuffer()
	base := time.Unix(100, 0)
	payload := make([]byte, 160)
	for i := 0; i < 50*30; i++ {
		at := base.Add(time.Duration(i) * 20 * time.Millisecond)
		if i >= 50*20 {
			at = at.Add(300 * time.Millisecond)
		}
		b.pushRTPAt(uint16(i), payload, uint32(i*160), at, at)
	}
	found := false
	for i := 0; i < b.clock.fitCount; i++ {
		found = found || b.clock.fits[i].Method == "delivery_step"
	}
	if !found {
		t.Fatal("sustained delivery step not detected")
	}
}

// Same inputs, both estimators. Logged table backs the default-estimator
// decision; asserts only the shared safety bounds.
func TestZZReviewAEstimatorComparison(t *testing.T) {
	for _, name := range []string{"clean", "jitter3", "bursty", "loss1", "stall", "skew+1%", "skew-1%"} {
		seconds := 60
		if name[:4] == "skew" {
			seconds = 150
		}
		for _, estimator := range []string{"ewma", "distribution"} {
			r := zzraRun(t, sipPlaybackPolicy{DelayEstimator: estimator}, zzraScenario(name, seconds), time.Duration(seconds+1)*time.Second)
			mean, lo, hi := 0.0, 1000, 0
			for _, v := range r.Targets {
				mean += float64(v)
				lo, hi = min(lo, v), max(hi, v)
			}
			mean /= float64(len(r.Targets))
			t.Logf("| %-8s | %-12s | %8.0f | %6.0f | %6.0f | %6.0f | %5.1f | %5.1f | %3d/%5.1f/%3d |", name, estimator, r.PlayedMS, r.LateDropMS, r.DroppedMS, r.ConcealMS, r.MedianLatencyMS, r.MaxLatencyMS, lo, mean, hi)
			if r.PlayedMS < r.SentMS-1500 {
				t.Errorf("%s/%s lost audio: %+v", name, estimator, r)
			}
		}
	}
}

func TestZZReviewARecoveryCountersInDiagnostics(t *testing.T) {
	r := zzraRun(t, sipPlaybackPolicy{DelayEstimator: "distribution"}, zzraScenario("stall", 30), 30*time.Second)
	r.b.sequenceRebases = 2 // set directly; exercised by TestZZReviewASameSSRCSequenceRebase
	m := &sipRTPMedia{}
	m.jitterTelemetry.Store(r.b)
	d := &sipLiveDiagnostics{}
	d.media.Store(m)
	s := d.snapshot(time.Unix(130, 0))
	if s.Jitter.SourceReanchors != 1 || s.Jitter.SequenceRebases != 2 || s.Jitter.DelayOutOfRange == 0 {
		t.Fatal("recovery counters missing from diagnostics", s.Jitter.SourceReanchors, s.Jitter.SequenceRebases, s.Jitter.DelayOutOfRange)
	}
}

// Item 4: a constant delivery offset outside ±320 ms (here while a -1% skew is
// being calibrated) must not freeze the distribution target at its maximum.
func TestZZReviewADistributionTracksOffsetNotAbsoluteRange(t *testing.T) {
	r := zzraRun(t, sipPlaybackPolicy{DelayEstimator: "distribution"}, zzraScenario("skew-1%", 110), 110*time.Second)
	for _, v := range r.Targets[len(r.Targets)-20:] {
		if v != 60 {
			t.Fatalf("distribution target frozen by an absolute delay range: %v", r.Targets)
		}
	}
}

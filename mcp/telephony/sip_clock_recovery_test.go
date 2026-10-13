package main

import (
	"fmt"
	"math"
	"sort"
	"testing"
	"time"
)

func BenchmarkSIPClockV13Comparison(b *testing.B) {
	for _, legacy := range []bool{true, false} {
		b.Run(fmt.Sprintf("v13_%t", legacy), func(b *testing.B) {
			j := newRTPJitterBuffer()
			old := &v13SIPClockBuffer{j}
			payload := make([]byte, 160)
			start := time.Unix(100, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				at := start.Add(time.Duration(i) * 20 * time.Millisecond)
				if legacy {
					old.pushRTP(uint16(i), payload, uint32(i*160), at)
				} else {
					j.pushRTP(uint16(i), payload, uint32(i*160), at)
				}
				j.popAt(at)
			}
		})
	}
}

type sipClockRecoveryResult struct {
	LateMS, ExpiredMS, OverflowMS, TailDroppedMS, MaxQueueMS float64
	Updates, Rejected, PhaseWithheld, RecentRecoveries       int
	PPM                                                      float64
}

// Real codec decode/framing with deterministic timing; excludes sockets, CPU
// saturation and any claim that these synthetic packets are the incident trace.
func replaySIPClockRecovery(t *testing.T, codec, profile string, legacy bool, seconds int) sipClockRecoveryResult {
	t.Helper()
	b := newRTPJitterBuffer()
	old := &v13SIPClockBuffer{b}
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	f := newRTPAudioFramer(b, m.offer)
	enc := &sipRTPMedia{offer: m.offer}
	pcm := make([]int16, m.sampleRate()/50)
	for i := range pcm {
		pcm[i] = int16(5000 * math.Sin(2*math.Pi*180*float64(i)/float64(m.sampleRate())))
	}
	payload := enc.encodeAudio(pcm)
	var packets []sipReplayPacket
	for frame := 0; frame < seconds*50; frame++ {
		source := time.Duration(float64(frame) * float64(20*time.Millisecond) * 1.00025)
		if profile == "rate_change" && frame >= 1600 {
			source = 32008*time.Millisecond + time.Duration(float64(frame-1600)*float64(20*time.Millisecond)*1.0005)
		}
		delay := time.Duration(frame%2) * 10 * time.Millisecond
		switch profile {
		case "incident_147s", "age_budget_edge":
			if frame >= 100 {
				delay += 160 * time.Millisecond
				if profile == "age_budget_edge" {
					delay += 20 * time.Millisecond
				}
			}
			if frame >= 1500 && frame < 1521 {
				delay += time.Duration(1521-frame) * 20 * time.Millisecond
			}
		case "early_offset", "rate_change":
			if frame >= 100 {
				delay += 80 * time.Millisecond
			}
		case "variable_windows":
			if (frame/100)%2 == 1 {
				delay += 80 * time.Millisecond
			}
		case "stale_recovery":
			if frame >= 100 && frame < 3000 {
				delay += 400 * time.Millisecond
			}
		}
		packets = append(packets, sipReplayPacket{at: source + delay, seq: uint16(frame), ts: uint32(frame * 160), payload: payload})
	}
	sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
	start := time.Unix(100, 0)
	next := 0
	r := sipClockRecoveryResult{}
	for tick := time.Duration(0); tick < time.Duration(seconds)*time.Second; tick += 20 * time.Millisecond {
		before := b.lateDroppedSamples + b.droppedSamples
		for next < len(packets) && packets[next].at <= tick {
			p := packets[next]
			if legacy {
				old.pushRTP(p.seq, p.payload, p.ts, start.Add(p.at))
			} else {
				b.pushRTP(p.seq, p.payload, p.ts, start.Add(p.at))
			}
			next++
		}
		f.popPCMAt(m, start.Add(tick))
		r.MaxQueueMS = max(r.MaxQueueMS, float64(int64(b.buffered)+b.pendingPCMSamples.Load())/8)
		if tick > 100*time.Second {
			r.TailDroppedMS += float64(b.lateDroppedSamples+b.droppedSamples-before) / 8
		}
	}
	r.LateMS, r.ExpiredMS, r.OverflowMS = float64(b.lateDroppedSamples)/8, float64(b.expiredSamples)/8, float64(b.overflowSamples)/8
	r.Updates, r.Rejected, r.PhaseWithheld, r.RecentRecoveries = b.clock.updates, b.clock.rejected, b.clock.phaseRejected, b.clock.recentRecoveries
	r.PPM = b.driftPPM
	if profile == "age_budget_edge" && !legacy {
		for _, e := range b.events {
			if (e.Reason == "queue_age" || e.Reason == "decoded_partial_age") && e.AgeMS <= 200 {
				t.Fatal("fresh audio expired before age limit", e)
			}
		}
	}
	if r.MaxQueueMS > 160 {
		t.Fatal("capacity bound changed", r)
	}
	return r
}

func TestSIPClockRecoveryV13Comparison(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, profile := range []string{"early_offset", "variable_windows", "rate_change"} {
			t.Run(codec+"/"+profile, func(t *testing.T) {
				before := replaySIPClockRecovery(t, codec, profile, true, 1200)
				after := replaySIPClockRecovery(t, codec, profile, false, 1200)
				t.Logf("before=%+v after=%+v", before, after)
				if profile != "variable_windows" && before.TailDroppedMS < 60000 {
					t.Fatal("0.12.13 failure was not reproduced", before)
				}
				if after.TailDroppedMS != 0 || after.Updates == 0 {
					t.Fatal("healthy recovered stream still loses audio", after)
				}
				want := -249.938
				if profile == "rate_change" {
					want = -499.75
				}
				if math.Abs(after.PPM-want) > 60 {
					t.Fatal("rate failed to recover", after)
				}
			})
		}
	}
}

func TestSIPClockRecoveryStaleBacklogStillExpires(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			r := replaySIPClockRecovery(t, codec, "stale_recovery", false, 180)
			t.Logf("%+v", r)
			if r.LateMS < 55000 || r.TailDroppedMS != 0 || r.PhaseWithheld == 0 {
				t.Fatal("stale delivery absorbed or fresh recovery failed", r)
			}
		})
	}
}

func TestSIPClockFitEvidenceIsBoundedAndExplainsRejection(t *testing.T) {
	b := newRTPJitterBuffer()
	start := time.Unix(100, 0)
	for i := 0; i < 12000; i++ {
		delay := time.Duration(0)
		if i > 100 {
			delay = 400 * time.Millisecond
		}
		now := start.Add(time.Duration(i)*20*time.Millisecond + delay)
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), now)
		b.popAt(now)
	}
	if b.clock.fitCount != len(b.clock.fits) || b.clock.phaseRejected == 0 || b.clock.updates == 0 {
		t.Fatal("evidence or independent rate recovery missing", b.clock)
	}
	for _, fit := range b.clock.fits {
		if fit.At.IsZero() || fit.Reason == "" || math.IsNaN(fit.CandidatePPM) || math.IsInf(fit.PhaseMS, 0) {
			t.Fatal("invalid fit evidence", fit)
		}
	}
	m := &sipRTPMedia{}
	m.jitterTelemetry.Store(b)
	d := &sipLiveDiagnostics{}
	d.media.Store(m)
	s := d.snapshot(start.Add(240 * time.Second))
	if len(s.Jitter.ClockFits) != 16 || s.Jitter.ClockPhaseRejected == 0 {
		t.Fatal("fit evidence lost in live diagnostics", s.Jitter)
	}
}

func TestSIPClockLowerEnvelopeRecoversRateWithoutAbsorbingDelay(t *testing.T) {
	c := &sipSourceClock{count: 32}
	start := time.Unix(100, 0)
	for i := 0; i < 32; i++ {
		c.points[i] = sipClockObservation{at: start.Add(time.Duration(float64(i)*float64(2*time.Second)*1.00025) + time.Duration(i%2)*80*time.Millisecond), ts: uint32(i * 16000)}
	}
	ts := uint32(32 * 16000)
	previous := start.Add(64 * time.Second)
	full, _ := c.fit(32, ts, previous, previous)
	fast, _ := c.fitEnvelope(32, ts, previous, previous)
	if full.RateAccepted || full.ResidualMS <= 40 || !fast.RateAccepted || math.Abs(fast.CandidatePPM+249.938) > 1 {
		t.Fatal("lower envelope did not isolate delivery variation", full, fast)
	}
	for i := range c.points {
		c.points[i].at = c.points[i].at.Add(400 * time.Millisecond)
	}
	fast, _ = c.fitEnvelope(32, ts, previous, previous)
	if !fast.RateAccepted || fast.PhaseMS < 400 {
		t.Fatal("delay was erased from the fit", fast)
	}
}

func TestSIPIngressAndConsumerTimingAreDistinct(t *testing.T) {
	b := newRTPJitterBuffer()
	start := time.Unix(100, 0)
	b.pushSourceRTPAt(1, 1, make([]byte, 160), 160, start, start.Add(250*time.Millisecond))
	if b.lateDroppedSamples != 160 || b.events[0].Reason != "ingress_processing_age" || b.events[0].LocalQueueAgeMS != 250 || b.events[0].SourceAgeMS != 250 || b.events[0].DeliveryAgeMS != 0 {
		t.Fatal("read backlog was not distinguished", b.events[0])
	}
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
	f := newRTPAudioFramer(b, m.offer)
	f.popPCMAt(m, start.Add(250*time.Millisecond))
	f.popPCMAt(m, start.Add(650*time.Millisecond))
	if b.maxConsumerIntervalNS.Load() != int64(400*time.Millisecond) {
		t.Fatal("consumer scheduling gap missing")
	}
}

func TestSIPFreshPacketAfterConcealmentRebuffersWithoutClockReset(t *testing.T) {
	b := newRTPJitterBuffer()
	start := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), start.Add(time.Duration(i)*20*time.Millisecond))
	}
	for tick := 40; tick <= 100; tick += 20 {
		b.popAt(start.Add(time.Duration(tick) * time.Millisecond))
	}
	if !b.lastPoppedMissing {
		t.Fatal("test did not render concealment")
	}
	anchor := b.sourceAnchorAt
	b.pushRTP(3, make([]byte, 160), 480, start.Add(120*time.Millisecond))
	if b.freshResumes != 1 || b.lateDroppedSamples != 0 || len(b.packets) != 1 || b.clockResets != 0 || b.sourceAnchorAt != anchor {
		t.Fatal("fresh speech was dropped or clock reset", b.freshResumes, b.lateDroppedSamples, b.clockResets)
	}
	b.pushRTP(3, make([]byte, 160), 480, start.Add(121*time.Millisecond))
	if len(b.packets) != 1 {
		t.Fatal("duplicate packet bypassed protection")
	}
}

func TestSIPStartupReserveFitsRemainingAgeBudget(t *testing.T) {
	for _, delay := range []time.Duration{140 * time.Millisecond, 190 * time.Millisecond} {
		t.Run(delay.String(), func(t *testing.T) {
			b := newRTPJitterBuffer()
			start := time.Unix(100, 0)
			b.haveSourceAnchor = true
			b.sourceAnchorAt = start
			b.sourceAnchorTS = 0
			b.targetMS = 100
			played := 0
			for i := 0; i < 50; i++ {
				now := start.Add(time.Duration(i)*20*time.Millisecond + delay)
				b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), now)
				if p, ready := b.popAt(now); ready && p != nil {
					played++
				}
			}
			if played < 45 || b.expiredSamples != 0 || b.lateDroppedSamples != 0 {
				t.Fatal("buffer waited for an impossible reserve", played, b.expiredSamples, b.lateDroppedSamples)
			}
			found := false
			for _, e := range b.events {
				if e.Reason == "playback_started" {
					found = true
					if e.RequiredReserveMS > 60 || e.RequiredReserveMS < 20 {
						t.Fatal("actual start reserve missing", e)
					}
				}
			}
			if !found {
				t.Fatal("start was not timestamped")
			}
		})
	}
}

func TestSIPDecodedPartialDeadlineFollowsRetainedSamples(t *testing.T) {
	b := newRTPJitterBuffer()
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "G722", PacketSamples: 160}}
	f := newRTPAudioFramer(b, m.offer)
	start := time.Unix(100, 0)
	f.pendingPCM = make([]int16, 640)
	for i := range f.pendingPCM {
		f.pendingPCM[i] = int16(i)
	}
	f.segments = []sipPCMSegment{{samples: 640, at: start, source: start, arrival: start.Add(160 * time.Millisecond)}}
	if _, ready := f.popPCMAt(m, start.Add(180*time.Millisecond)); !ready {
		t.Fatal("first frame did not play")
	}
	if len(f.segments) != 1 || f.segments[0].source.Sub(start) != 20*time.Millisecond {
		t.Fatal("consumed prefix kept ownership of deadline", f.segments)
	}
	out, ready := f.popPCMAt(m, start.Add(210*time.Millisecond))
	if !ready || b.expiredSamples != 0 {
		t.Fatal("fresh remaining samples expired", b.expiredSamples)
	}
	for i, v := range out {
		if v != int16(i+320) {
			t.Fatal("remaining audio changed", i, v)
		}
	}
	f.pendingPCM = make([]int16, 320)
	f.segments = []sipPCMSegment{{samples: 320, at: start, source: start, arrival: start.Add(160 * time.Millisecond)}}
	if _, ready := f.popPCMAt(m, start.Add(400*time.Millisecond)); ready || b.expiredSamples != 160 {
		t.Fatal("genuinely stale partial survived", b.expiredSamples)
	}
}

func TestSIPClockRecoveryShortIncidentShapedTiming(t *testing.T) {
	// Same codec and approximate duration as the report, with a controlled
	// 160ms offset, 250ppm drift, modest jitter and one 420ms delivery hold.
	// This is a hypothesis probe; the report does not contain a packet trace.
	for _, codec := range []string{"G722", "PCMU", "PCMA"} {
		t.Run(codec, func(t *testing.T) {
			before := replaySIPClockRecovery(t, codec, "incident_147s", true, 147)
			after := replaySIPClockRecovery(t, codec, "incident_147s", false, 147)
			t.Logf("before=%+v after=%+v", before, after)
			if before.LateMS+before.ExpiredMS < 30000 {
				t.Fatal("short old-clock rejection did not reproduce", before)
			}
			if after.TailDroppedMS != 0 || after.LateMS+after.ExpiredMS+after.OverflowMS > 600 {
				t.Fatal("recovered audio remains rejected outside genuine held backlog", after)
			}
		})
	}
}

func TestSIPClockRecoveryDeliveryAtAgeBudgetEdge(t *testing.T) {
	// At 180ms delivery offset + 10ms jitter, a 20ms playout tick can
	// legitimately push packets over the unchanged 200ms age limit. Do not
	// assert lossless playback when delivery leaves insufficient headroom.
	r := replaySIPClockRecovery(t, "G722", "age_budget_edge", false, 147)
	t.Logf("%+v", r)
	if r.Updates == 0 || math.Abs(r.PPM+249.938) > 60 || r.ExpiredMS == 0 {
		t.Fatal("clock recovery or genuine age expiry missing", r)
	}
}

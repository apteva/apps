package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sync"
	"testing"
	"time"
)

func TestSIPPlaybackLongClockAndJitter(t *testing.T) {
	for _, ppm := range []float64{-250, 250} {
		r := replaySIP(t, "clock_jitter", "PCMU", 60, 160, 1200, ppm, nil)
		t.Logf("%+v", r)
		if r.DroppedMS > 20 || r.ReplacementMS != 0 || math.Abs(r.DriftPPM-ppm) > 60 {
			t.Fatalf("ordered, modest-jitter audio falsely expired: %+v", r)
		}
	}
}

func TestSIPPlaybackLongClockMatrix(t *testing.T) {
	var results []sipReplayResult
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, seconds := range []int{1800, 7200} {
			for _, profile := range []string{"clock_jitter", "ordinary_jitter"} {
				for _, ppm := range []float64{-250, 250} {
					t.Run(fmt.Sprintf("%s/%ds/%s/%gppm", codec, seconds, profile, ppm), func(t *testing.T) {
						r := replaySIP(t, profile, codec, 60, 160, seconds, ppm, nil)
						results = append(results, r)
						if r.DroppedMS != 0 || r.ReplacementMS != 0 || math.Abs(r.DriftPPM-ppm) > 60 {
							t.Fatalf("long healthy stream degraded: %+v", r)
						}
						t.Logf("%+v", r)
					})
				}
			}
		}
	}
	if path := os.Getenv("TELEPHONY_SIP_CLOCK_RESULTS"); path != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSIPPlaybackClockDoesNotAbsorbDeliveryStall(t *testing.T) {
	for _, delay := range []time.Duration{400 * time.Millisecond, 10 * time.Second} {
		t.Run(delay.String(), func(t *testing.T) {
			b := newRTPJitterBuffer()
			at := time.Unix(100, 0)
			payload := make([]byte, 160)
			for i := 0; i < 3000; i++ {
				now := at.Add(time.Duration(i) * 20 * time.Millisecond)
				b.pushRTP(uint16(i), payload, uint32(i*160), now)
				b.popAt(now)
			}
			before := b.lateDroppedSamples
			// Sustained delayed delivery, not just a single old batch. Even when
			// all estimator windows become delayed, source-clock evidence must remain.
			// A bounded live-phase recovery is separate from that original clock.
			for i := 3000; i < 12000; i++ {
				now := at.Add(time.Duration(i)*20*time.Millisecond + delay)
				b.pushRTP(uint16(i), payload, uint32(i*160), now)
				b.popAt(now)
			}
			if b.clock.rejected == 0 || math.Abs(b.driftPPM) > 1 || b.maxDeliveryAgeMS < float64(delay/time.Millisecond)-1 {
				t.Fatal("source-clock evidence was erased by recovery", b.driftPPM, b.maxDeliveryAgeMS)
			}
			if delay <= time.Second && (b.recovery.recoveries != 1 || b.lateDroppedSamples-before > 160*50) {
				t.Fatal("bounded phase failed to recover")
			}
			if delay > time.Second && (b.lateDroppedSamples-before != 9000*160 || b.recovery.recoveries != 0) {
				t.Fatal("genuine stale delivery absorbed into clock", b.lateDroppedSamples-before, b.driftPPM)
			}
			// Fresh packets resume immediately; rejected packets did not freeze
			// observation ownership. No old queued audio survives the recovery.
			for i := 12000; i < 12003; i++ {
				b.pushRTP(uint16(i), payload, uint32(i*160), at.Add(time.Duration(i)*20*time.Millisecond+delay))
			}
			// Skip the catch-up timeline to the current live source timestamp.
			ts := uint32(12003*160 + int(delay/time.Millisecond)*8)
			for i := 0; i < 6; i++ {
				now := at.Add(time.Duration(12003+i)*20*time.Millisecond + delay)
				b.pushRTP(uint16(12003+i), payload, ts+uint32(i*160), now)
			}
			if p, ready := b.popAt(at.Add(12008*20*time.Millisecond + delay)); !ready || len(p) != 160 {
				t.Fatal("fresh source did not recover")
			}
		})
	}
}

func TestSIPPlaybackClockObservationSurvivesRejection(t *testing.T) {
	b := newRTPJitterBuffer()
	at := time.Unix(100, 0)
	b.pushRTP(1, make([]byte, 160), 0, at)
	b.pushRTP(2, make([]byte, 160), 160, at.Add(time.Second))
	if b.lateDroppedSamples != 160 || b.lastSequence != 2 || !b.lastArrival.Equal(at.Add(time.Second)) {
		t.Fatal("rejection froze timing observations")
	}
}

func TestSIPPlaybackClockTimestampAndSequenceWrap(t *testing.T) {
	b := newRTPJitterBuffer()
	at := time.Unix(100, 0)
	for i := 0; i < 12000; i++ {
		now := at.Add(time.Duration(float64(i)*float64(20*time.Millisecond)/.99975) + time.Duration(i%2)*12*time.Millisecond)
		b.pushRTP(uint16(65530+i), make([]byte, 160), 0xfffff000+uint32(i*160), now)
		// Consume at arrival with a retained startup reserve; no wall-clock
		// sleep, signed timestamp/sequence wrap exercised on production ingress.
		b.popAt(now)
	}
	if b.lateDroppedSamples != 0 || b.droppedSamples != 0 || math.Abs(b.driftPPM+250) > 60 {
		t.Fatal("wrap lost clock continuity", b.driftPPM, b.lateDroppedSamples, b.droppedSamples)
	}
}

func TestSIPPlaybackClockConcurrentLongCalls(t *testing.T) {
	var wg sync.WaitGroup
	for call := 0; call < 32; call++ {
		wg.Add(1)
		go func(call int) {
			defer wg.Done()
			r := replaySIP(t, "clock_jitter", "G722", 60, 160, 1200, float64((call%2)*500-250), nil)
			if r.DroppedMS != 0 || r.ReplacementMS != 0 {
				t.Errorf("call %d: %+v", call, r)
			}
		}(call)
	}
	wg.Wait()
}

// Keep the exact released ingress in a test-only reference, so the reported
// mechanism remains reproducible independently of the replacement estimator.
func TestSIPPlaybackReleasedClockRegression(t *testing.T) {
	for _, jitter := range []bool{false, true} {
		for _, ppm := range []float64{0, -250} {
			j := &releasedSIPClockBuffer{rtpJitterBuffer: newRTPJitterBuffer()}
			m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
			f := newRTPAudioFramer(j.rtpJitterBuffer, m.offer)
			at := time.Unix(100, 0)
			payload := make([]byte, 160)
			frame := 0
			for tick := 0; tick < 60000; tick++ {
				now := at.Add(time.Duration(tick) * 20 * time.Millisecond)
				for {
					delay := time.Duration(0)
					if jitter {
						delay = time.Duration(frame%2) * 12 * time.Millisecond
					}
					arrival := at.Add(time.Duration(float64(frame)*float64(20*time.Millisecond)/(1+ppm/1e6)) + delay)
					if arrival.After(now) {
						break
					}
					j.pushRTP(uint16(frame), payload, uint32(frame*160), arrival)
					frame++
				}
				f.popPCMAt(m, now)
			}
			late := float64(j.lateDroppedSamples) / 8
			t.Logf("released jitter=%t ppm=%g late_ms=%g total_discarded_ms=%g", jitter, ppm, late, float64(j.lateDroppedSamples+j.droppedSamples)/8)
			if jitter && ppm != 0 && late < 60000 {
				t.Fatal("released regression not reproduced", late)
			}
			if (!jitter || ppm == 0) && late != 0 {
				t.Fatal("control unexpectedly failed", late)
			}
		}
	}
}

func TestSIPPlaybackClockSlowChangingJitter(t *testing.T) {
	for _, ppm := range []float64{-250, 250} {
		r := replaySIP(t, "slow_jitter", "PCMU", 60, 160, 3600, ppm, nil)
		t.Logf("%+v", r)
		if r.DroppedMS != 0 || r.ReplacementMS != 0 {
			t.Fatalf("slow arrival jitter starved clock fit: %+v", r)
		}
	}
}

func BenchmarkSIPPlaybackClockIngress(b *testing.B) {
	for _, released := range []bool{true, false} {
		b.Run(fmt.Sprintf("released_%t", released), func(b *testing.B) {
			j := newRTPJitterBuffer()
			old := &releasedSIPClockBuffer{rtpJitterBuffer: j}
			payload := make([]byte, 160)
			at := time.Unix(100, 0)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				now := at.Add(time.Duration(i) * 20 * time.Millisecond)
				if released {
					old.pushRTP(uint16(i), payload, uint32(i*160), now)
				} else {
					j.pushRTP(uint16(i), payload, uint32(i*160), now)
				}
				j.popAt(now)
			}
		})
	}
}

func TestSIPPlaybackClockPhaseRecovers(t *testing.T) {
	b := newRTPJitterBuffer()
	at := time.Unix(100, 0)
	for i := 0; i < 6000; i++ {
		now := at.Add(time.Duration(float64(i)*float64(20*time.Millisecond)/.99975) + time.Duration(i%2)*12*time.Millisecond)
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), now)
		b.popAt(now)
		if i == 3000 {
			// Small estimator error, within the admissible envelope; frequency
			// continuity alone would never remove the accumulated phase offset.
			b.sourceAnchorAt = b.sourceAnchorAt.Add(-30 * time.Millisecond)
		}
	}
	if age := b.lastArrival.Sub(b.sourceTime(b.lastTimestamp)); age > 20*time.Millisecond || b.clock.phaseAdjustedMS < 20 {
		t.Fatal("phase error retained", age, b.clock.phaseAdjustedMS)
	}
}

func TestSIPPlaybackRetainedPartialAndConcurrentIngressStayBounded(t *testing.T) {
	b := newRTPJitterBuffer()
	at := time.Unix(100, 0)
	// Packet ingress filled the capacity after a frame could not be completed,
	// before the consumer republished its retained 19.875 ms decoded partial.
	for i := 0; i < 8; i++ {
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), at)
	}
	b.storeDecodedReserve(159, at)
	if b.buffered+int(b.pendingPCMSamples.Load()) > b.policy.CapacityMS*8 || b.overflowSamples != 160 {
		t.Fatal("retained partial exceeded shared capacity", b.buffered, b.pendingPCMSamples.Load(), b.overflowSamples)
	}
	if _, oldRetained := b.packets[0]; oldRetained {
		t.Fatal("overflow retained oldest speech")
	}
}

func TestSIPPlaybackClockReportedReproduction(t *testing.T) {
	for _, released := range []bool{true, false} {
		b := newRTPJitterBuffer()
		old := &releasedSIPClockBuffer{rtpJitterBuffer: b}
		at := time.Unix(1700000000, 0)
		payload := make([]byte, 160)
		for i := 0; i < 60000; i++ {
			now := at.Add(time.Duration(float64(i)*20000000*1.00025) + time.Duration(i%2)*10*time.Millisecond)
			if released {
				old.pushRTP(uint16(i), payload, uint32(i*160), now)
			} else {
				b.pushRTP(uint16(i), payload, uint32(i*160), now)
			}
			b.popAt(now)
		}
		t.Logf("reported input released=%t late_ms=%g total_ms=%g", released, float64(b.lateDroppedSamples)/8, float64(b.lateDroppedSamples+b.droppedSamples)/8)
		if released && b.lateDroppedSamples != 220000*8 {
			t.Fatal("exact incident reproduction changed", b.lateDroppedSamples)
		}
		if !released && b.lateDroppedSamples+b.droppedSamples != 0 {
			t.Fatal("reported drift/jitter input still rejected")
		}
	}
}

func TestSIPPlaybackClockMultiCallComparison(t *testing.T) {
	type result struct {
		Profile           string  `json:"profile"`
		Released          bool    `json:"released_ingress"`
		Calls             int     `json:"calls"`
		Seconds           int     `json:"simulated_seconds_per_call"`
		DiscardedMS       float64 `json:"discarded_ms_total"`
		WorstDiscardedMS  float64 `json:"discarded_ms_worst_call"`
		ReplacementMS     float64 `json:"replacement_ms_total"`
		MaxQueueMS        float64 `json:"max_queue_ms"`
		LateRejectedMS    float64 `json:"late_rejected_ms_total"`
		OverflowDroppedMS float64 `json:"overflow_dropped_ms_total"`
		ExpiredDroppedMS  float64 `json:"expired_dropped_ms_total"`
		TailDroppedMS     float64 `json:"discarded_outside_recovery_window_ms"`
		MaxResidenceMS    int     `json:"max_residence_ms"`
	}
	var results []result
	for _, profile := range []string{"stable", "clock_and_jitter", "clock_jitter_80ms_process_pauses", "clock_jitter_400ms_process_pauses"} {
		for _, released := range []bool{true, false} {
			r := result{Profile: profile, Released: released, Calls: 64, Seconds: 1200}
			buffers := make([]*rtpJitterBuffer, r.Calls)
			old := make([]*releasedSIPClockBuffer, r.Calls)
			framers := make([]*rtpAudioFramer, r.Calls)
			media := make([]*sipRTPMedia, r.Calls)
			next := make([]int, r.Calls)
			at := time.Unix(100, 0)
			payload := make([]byte, 160)
			for c := range buffers {
				buffers[c] = newRTPJitterBuffer()
				old[c] = &releasedSIPClockBuffer{rtpJitterBuffer: buffers[c]}
				media[c] = &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU", PacketSamples: 160}}
				framers[c] = newRTPAudioFramer(buffers[c], media[c].offer)
			}
			pause := time.Duration(0)
			if profile == "clock_jitter_80ms_process_pauses" {
				pause = 80 * time.Millisecond
			}
			if profile == "clock_jitter_400ms_process_pauses" {
				pause = 400 * time.Millisecond
			}
			for tick := time.Duration(0); tick < time.Duration(r.Seconds)*time.Second; tick += 20 * time.Millisecond {
				// A shared process scheduling pause affects receipt and playback
				// across all calls. Replays are deterministic virtual time, with no
				// reliance on this computer's scheduling or network conditions.
				if tick >= 20*time.Second && tick%(20*time.Second) < pause {
					continue
				}
				for c, b := range buffers {
					beforeDrops := b.droppedSamples + b.lateDroppedSamples
					for {
						i := next[c]
						source := time.Duration(i) * 20 * time.Millisecond
						if profile != "stable" {
							source = time.Duration(float64(i)*float64(20*time.Millisecond)*1.00025) + time.Duration(i%2)*10*time.Millisecond
						}
						source += time.Duration(c%10) * time.Millisecond
						arrival := source
						if arrival >= 20*time.Second && arrival%(20*time.Second) < pause {
							arrival += pause - arrival%(20*time.Second)
						}
						if arrival > tick {
							break
						}
						if released {
							old[c].pushRTP(uint16(i), payload, uint32(i*160), at.Add(arrival))
						} else {
							b.pushRTP(uint16(i), payload, uint32(i*160), at.Add(arrival))
						}
						next[c]++
					}
					_, _ = framers[c].popPCMAt(media[c], at.Add(tick))
					if pause > 0 && tick%(20*time.Second) > pause+2*time.Second {
						r.TailDroppedMS += float64(b.droppedSamples+b.lateDroppedSamples-beforeDrops) / 8
					}
					r.MaxQueueMS = max(r.MaxQueueMS, float64(b.buffered+int(b.pendingPCMSamples.Load()))/8)
				}
			}
			for _, b := range buffers {
				d := float64(b.droppedSamples+b.lateDroppedSamples) / 8
				r.LateRejectedMS += float64(b.lateDroppedSamples) / 8
				r.OverflowDroppedMS += float64(b.overflowSamples) / 8
				r.ExpiredDroppedMS += float64(b.expiredSamples) / 8
				r.DiscardedMS += d
				r.WorstDiscardedMS = max(r.WorstDiscardedMS, d)
				r.ReplacementMS += float64(b.missingFilledSamples.Load()) / 8
				r.MaxResidenceMS = max(r.MaxResidenceMS, b.maxAgeMS)
			}
			results = append(results, r)
			t.Logf("%+v", r)
			if !released && r.MaxQueueMS > 160 {
				t.Fatal("multi-call capacity violated", r)
			}
			if !released && pause == 0 && (r.DiscardedMS != 0 || r.ReplacementMS != 0) {
				t.Error("healthy/modest scheduling jitter still discarded", r)
			}
			if released && profile == "clock_and_jitter" && r.WorstDiscardedMS < 100000 {
				t.Fatal("release defect not reproduced", r)
			}
			if !released && pause > 0 && (r.WorstDiscardedMS > float64((r.Seconds/20-1)*int(pause/time.Millisecond+20)) || r.TailDroppedMS != 0) {
				t.Fatal("process pauses must retain bounded loss and recover", r)
			}
		}
	}
	if path := os.Getenv("TELEPHONY_SIP_MULTI_RESULTS"); path != "" {
		data, err := json.MarshalIndent(map[string]any{"scope": "64 interleaved production SIP buffers and PCMU framers, deterministic virtual time; released ingress versus corrected ingress; common framer; no sockets, Opus/browser, routing/database or real CPU saturation", "results": results}, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSIPPlaybackClockDiagnosticsConcurrent(t *testing.T) {
	b := newRTPJitterBuffer()
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: "PCMU"}}
	m.jitterTelemetry.Store(b)
	d := &sipLiveDiagnostics{}
	d.media.Store(m)
	at := time.Unix(100, 0)
	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), at.Add(time.Duration(float64(i)*float64(20*time.Millisecond)*1.00025)))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			b.popAt(at.Add(time.Duration(i) * 20 * time.Millisecond))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 1000; i++ {
			_ = d.snapshot(at.Add(time.Duration(i) * time.Second))
		}
	}()
	wg.Wait()
	snapshot := d.snapshot(at.Add(400 * time.Second))
	if !snapshot.Jitter.Available || snapshot.Jitter.ClockEstimatorUpdates == 0 || snapshot.Jitter.ClockObservationCount != 32 {
		t.Fatal("clock estimator evidence missing", snapshot.Jitter)
	}
	summary := &audioDashboardSummary{Metrics: map[string]float64{}}
	addCarrierAudioSummary(summary, &carrierTelemetryRecord{SIP: &snapshot})
	if summary.Metrics["sip_clock_estimator_updates"] == 0 || summary.Metrics["sip_clock_observation_windows"] != 32 {
		t.Fatal("clock evidence lost in dashboard summary", summary.Metrics)
	}
	if b.buffered+int(b.pendingPCMSamples.Load()) > b.policy.CapacityMS*8 {
		t.Fatal("concurrent diagnostics bypassed capacity")
	}
}

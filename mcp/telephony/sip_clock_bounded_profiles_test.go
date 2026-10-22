package main

import (
	"math"
	"sort"
	"testing"
	"time"
)

type probePacket struct {
	at  time.Duration
	seq uint16
	ts  uint32
}

func TestSIPClockBoundedVariableProfiles(t *testing.T) {
	for _, ppm := range []float64{-500, 0, 250, 500} {
		for _, period := range []int{50, 100, 300, 600, 1500, 3000} {
			for _, pattern := range []string{"triangle", "sine", "missed_fast_window"} {
				sourceDrops := 0
				arrivalDrops := 0
				b := newRTPJitterBuffer()
				payload := make([]byte, 160)
				start := time.Unix(100, 0)
				packets := make([]probePacket, 60000)
				for i := range packets {
					phase := float64(i%period) / float64(period)
					delay := 0.0
					switch pattern {
					case "triangle":
						delay = 120 * (1 - math.Abs(2*phase-1))
					case "sine":
						delay = 60 + 60*math.Sin(2*math.Pi*phase)
					case "missed_fast_window":
						if (i/100)%2 == 1 {
							delay = 120
						} else {
							delay = float64(i%50) * 2.4
						}
					}
					packets[i] = probePacket{at: time.Duration(float64(i)*float64(20*time.Millisecond)*(1+ppm/1e6) + delay*float64(time.Millisecond)), seq: uint16(i), ts: uint32(i * 160)}
				}
				sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
				next := 0
				for tick := time.Duration(0); tick < 1202*time.Second; tick += 20 * time.Millisecond {
					for next < len(packets) && packets[next].at <= tick {
						p := packets[next]
						before := b.lateDroppedSamples
						b.pushRTP(p.seq, payload, p.ts, start.Add(p.at))
						if change := b.lateDroppedSamples - before; change > 0 {
							reason := b.events[(b.eventHead+63)%64].Reason
							if reason == "source_delivery_age" {
								sourceDrops += change
							} else {
								arrivalDrops += change
							}
						}
						next++
					}
					b.popAt(start.Add(tick))
				}
				if b.lateDroppedSamples > 0 {
					t.Errorf("bounded-delay source ppm=%g period=%d pattern=%s late_ms=%g max_relative_age=%g estimated_ppm=%g phase_withheld=%d source_drop_ms=%g arrival_drop_ms=%g\n", ppm, period, pattern, float64(b.lateDroppedSamples)/8, b.maxDeliveryAgeMS, b.driftPPM, b.clock.phaseRejected, float64(sourceDrops)/8, float64(arrivalDrops)/8)
				}
			}
		}
	}
}
func TestSIPAutomaticStablePhaseRecovers(t *testing.T) {
	b := newRTPJitterBuffer()
	start := time.Unix(100, 0)
	payload := make([]byte, 160)
	for i := 0; i < 60000; i++ {
		delay := time.Duration(0)
		if i >= 30000 {
			delay = 240 * time.Millisecond
		}
		at := start.Add(time.Duration(i)*20*time.Millisecond + delay)
		b.pushRTP(uint16(i), payload, uint32(i*160), at)
		b.popAt(at)
	}
	t.Logf("stable 240ms delivery step: rejected_ms=%g phase_withheld=%d queue_ms=%g", float64(b.lateDroppedSamples)/8, b.clock.phaseRejected, float64(b.buffered)/8)
	if b.recovery.recoveries != 1 || b.lateDroppedSamples == 0 || b.lateDroppedSamples > 16000 {
		t.Fatal("stable phase did not recover within a bounded interruption", b.recovery.recoveries, b.lateDroppedSamples)
	}
}

func BenchmarkSIPClockObservation(b *testing.B) {
	for _, released := range []bool{true, false} {
		name := "fixed"
		if released {
			name = "v0.12.21"
		}
		b.Run(name, func(b *testing.B) {
			base := time.Unix(100, 0)
			buffer := newRTPJitterBuffer()
			buffer.sourceAnchorAt = base
			buffer.haveSourceAnchor = true
			var old v21SIPSourceClock
			// Include the long estimator in the benchmark, not just its startup phase.
			for i := 0; i < 30000; i++ {
				at := base.Add(time.Duration(i) * 20 * time.Millisecond)
				if released {
					old.observe(buffer, uint32(i*160), at)
				} else {
					buffer.clock.observe(buffer, uint32(i*160), at)
				}
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				n := i + 30000
				at := base.Add(time.Duration(n) * 20 * time.Millisecond)
				if released {
					old.observe(buffer, uint32(n*160), at)
				} else {
					buffer.clock.observe(buffer, uint32(n*160), at)
				}
			}
		})
	}
}

func TestSIPClockTwoHourVariableDelivery(t *testing.T) {
	for _, ppm := range []float64{-500, -250, 250, 500} {
		for _, period := range []int{1500, 3000} {
			b := newRTPJitterBuffer()
			payload := make([]byte, 160)
			start := time.Unix(100, 0)
			next := 0
			arrival := func(i int) time.Duration {
				return time.Duration(float64(i)*float64(20*time.Millisecond)*(1+ppm/1e6) + (60+60*math.Sin(2*math.Pi*float64(i)/float64(period)))*float64(time.Millisecond))
			}
			for tick := time.Duration(0); tick < 7205*time.Second; tick += 20 * time.Millisecond {
				for next < 360000 && arrival(next) <= tick {
					b.pushRTP(uint16(next), payload, uint32(next*160), start.Add(arrival(next)))
					next++
				}
				b.popAt(start.Add(tick))
			}
			if b.lateDroppedSamples != 0 {
				t.Errorf("two-hour ppm=%g period=%d late=%.0fms", ppm, period, float64(b.lateDroppedSamples)/8)
			}
		}
	}
}

func TestSIPAutomaticConfiguredAgeRetainsLocalBound(t *testing.T) {
	for _, age := range []int{120, 160, 200} {
		b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{TargetMS: 60, MaxTargetMS: 60, CapacityMS: age, MaxAgeMS: age})
		start := time.Unix(100, 0)
		payload := make([]byte, 160)
		for i := 0; i < 60000; i++ {
			delay := time.Duration(0)
			if i >= 30000 {
				delay = time.Duration(age+40) * time.Millisecond
			}
			at := start.Add(time.Duration(i)*20*time.Millisecond + delay)
			b.pushRTP(uint16(i), payload, uint32(i*160), at)
			b.popAt(at)
		}
		if b.recovery.recoveries != 1 || b.lateDroppedSamples == 0 || b.lateDroppedSamples > 16000 {
			t.Errorf("configured age %dms: stable phase did not recover (late %.0fms, recoveries %d)", age, float64(b.lateDroppedSamples)/8, b.recovery.recoveries)
		}
		if b.buffered+int(b.pendingPCMSamples.Load()) > b.policy.CapacityMS*8 {
			t.Fatal("local queue budget exceeded")
		}
	}
}

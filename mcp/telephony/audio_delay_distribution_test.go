package main

import (
	"fmt"
	"sort"
	"testing"
	"time"
)

func TestAudioDelayDistributionBoundedAndForgets(t *testing.T) {
	var d audioDelayDistribution
	base := time.Unix(100, 0)
	for i := 0; i < 500; i++ {
		offset := 0.0
		if i%10 == 5 {
			offset = 80
		}
		target := d.observe(base.Add(time.Duration(i)*20*time.Millisecond), offset, 60, 100)
		if target < 60 || target > 100 {
			t.Fatal("unbounded target")
		}
	}
	if d.target != 100 {
		t.Fatal("distribution missed bursts", d.target)
	}
	for i := 500; i < 1500; i++ {
		d.observe(base.Add(time.Duration(i)*20*time.Millisecond), 0, 60, 100)
	}
	if d.target != 60 {
		t.Fatal("old jitter never forgotten", d.target)
	}
	// Duplicated batched observations cannot give one interval extra votes.
	var a, b audioDelayDistribution
	for i := 0; i < 100; i++ {
		now := base.Add(time.Duration(i) * 100 * time.Millisecond)
		a.observe(now, float64(i%3), 60, 100)
		for n := 0; n < 1000; n++ {
			b.observe(now, float64(i%3), 60, 100)
		}
	}
	if a.target != b.target {
		t.Fatal("batch size biased estimator")
	}
}
func TestSIPDelayDistributionSpeechComparison(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		t.Run(codec, func(t *testing.T) {
			for _, bursty := range []bool{false, true} {
				t.Run(fmt.Sprintf("bursty=%v", bursty), func(t *testing.T) {
					type packet struct {
						seq     uint16
						ts      uint32
						at      time.Duration
						payload []byte
					}
					offer := sipMediaOffer{Codec: codec, PacketSamples: 160}
					enc := &sipRTPMedia{offer: offer}
					clean := &sipRTPMedia{offer: offer}
					var reference []int16
					rate := enc.sampleRate()
					source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, rate)
					var packets []packet
					for i := 0; i < 2500; i++ {
						pcm := make([]int16, rate/50)
						for k := range pcm {
							pcm[k] = source[(i*len(pcm)+k)%len(source)]
						}
						arrival := i * 20
						// Ordinary bounded variation fits the base reserve; the
						// distribution should avoid raising it unnecessarily.
						arrival += (i * 7) % 19
						if bursty && i%25 >= 10 && i%25 < 14 {
							arrival += 80
						}
						payload := enc.encodeAudio(pcm)
						reference = append(reference, clean.decodeAudio(payload)...)
						packets = append(packets, packet{uint16(i), uint32(i * 160), time.Duration(arrival) * time.Millisecond, payload})
					}
					sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
					tailAge := [2]float64{}
					drops := [2]int{}
					var speech [2]sipSpeechEvidence
					for mode, name := range []string{"ewma", "distribution"} {
						b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{DelayEstimator: name})
						dec := &sipRTPMedia{offer: offer}
						f := newRTPAudioFramer(b, offer)
						base := time.Unix(100, 0)
						next := 0
						tailFrames := 0
						var frames []sipSpeechFrame
						var positions []int
						for tick := 8 * time.Millisecond; tick < 50*time.Second; tick += 20 * time.Millisecond {
							now := base.Add(tick)
							for next < len(packets) && packets[next].at <= tick {
								p := packets[next]
								next++
								b.pushRTPAt(p.seq, p.payload, p.ts, base.Add(p.at), base.Add(p.at))
								if b.buffered+int(b.pendingPCMSamples.Load()) > 1280 {
									t.Fatal("capacity exceeded")
								}
							}
							before := b.driftAdjustedSamples.Load()
							if pcm, ok := f.popPCMAt(dec, now); ok {
								take := len(pcm) + int(b.driftAdjustedSamples.Load()-before)
								frames = append(frames, sipSpeechFrame{at: tick, pcm: append([]int16(nil), pcm...), sourceSamples: take})
								positions = append(positions, int(b.expectedTimestamp)*rate/8000-len(f.pendingPCM)-take)
								if len(pcm) != rate/50 {
									t.Fatal("cadence changed")
								}
								if tick >= 35*time.Second {
									tailAge[mode] += float64(now.Sub(f.frameArrival)) / float64(time.Millisecond)
									tailFrames++
								}
							}
						}
						speech[mode] = analyzeSIPSpeech(reference, rate, frames, positions, 60)
						t.Logf("estimator=%s coverage=%.4f damaged=%dms", name, speech[mode].ActiveSpeechCoverage, speech[mode].LongestDamagedSpeechMS)
						tailAge[mode] /= float64(tailFrames)
						drops[mode] = b.droppedSamples + b.lateDroppedSamples
						t.Logf("estimator=%s tail_residence=%.2fms dropped=%.1fms target=%d concealed=%.1fms", name, tailAge[mode], float64(drops[mode])/8, b.targetMS, float64(b.concealedSamples.Load())/8)
						if b.maxAgeMS > 200 || b.expiredSamples != 0 {
							t.Fatal("age safety failed")
						}
					}
					if speech[1].ActiveSpeechCoverage < speech[0].ActiveSpeechCoverage || speech[1].LongestDamagedSpeechMS > speech[0].LongestDamagedSpeechMS {
						t.Fatal("speech quality regressed", speech)
					}
					if (!bursty && tailAge[1] >= tailAge[0]-5) || drops[1] > drops[0] {
						t.Fatal("distribution did not improve stable latency without extra discard", tailAge, drops)
					}
				})
			}
		})
	}
}
func TestReceiveEstimatorConfiguration(t *testing.T) {
	for _, name := range []string{"fixed", "distribution"} {
		c, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_receive_estimator": name})
		if e != nil || c.ReceiveEstimator != name {
			t.Fatal(c, e)
		}
	}
	if _, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_receive_estimator": "unknown"}); e == nil {
		t.Fatal("invalid estimator accepted")
	}
	if e := (sipPlaybackPolicy{DelayEstimator: "distribution"}).validate(); e != nil {
		t.Fatal(e)
	}
	if e := (sipPlaybackPolicy{DelayEstimator: "unknown"}).validate(); e == nil {
		t.Fatal("invalid SIP estimator accepted")
	}
}

func TestAudioDelayDistributionNoIngressAllocations(t *testing.T) {
	var d audioDelayDistribution
	now := time.Unix(100, 0)
	n := 0
	allocations := testing.AllocsPerRun(1000, func() {
		n++
		d.observe(now.Add(time.Duration(n)*20*time.Millisecond), float64(n%90), 60, 100)
	})
	if allocations != 0 {
		t.Fatal("delay estimation allocated on ingress", allocations)
	}
}

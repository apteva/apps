package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"
)

// Constructed delivery interruptions, not an exact replay of b1a0f1a...:
// that call did not retain packet timing. Use actual encoded speech and the
// production decoder/framer, keeping capacity and age limits unchanged.
func TestSIPStarvationReturningSpeech(t *testing.T) {
	var results []map[string]any
	t.Cleanup(func() {
		if path := os.Getenv("TELEPHONY_RECEIVE_RECOVERY_RESULTS"); path != "" {
			data, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err = os.WriteFile(path, append(data, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	})
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		for _, gap := range []int{140, 180, 220} {
			for _, burst := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%d/burst=%v", codec, gap, burst), func(t *testing.T) {
					base := time.Unix(100, 0)
					offer := sipMediaOffer{Codec: codec, PacketSamples: 160}
					enc, dec, clean := &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}
					b := newRTPJitterBuffer()
					f := newRTPAudioFramer(b, offer)
					rate := dec.sampleRate()
					speech := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, rate)
					var packets []productionAudioPacket
					var reference []int16
					for i := 0; i < 250; i++ {
						pcm := make([]int16, rate/50)
						for j := range pcm {
							pcm[j] = speech[(i*len(pcm)+j)%len(speech)]
						}
						at := time.Duration(i*20) * time.Millisecond
						// Three independent stalls, either followed by catch-up or a new
						// stable delivery offset. The latter exposes the mandatory reprime.
						for _, start := range []int{40, 100, 160} {
							if i >= start {
								if burst {
									at = max(at, time.Duration(start*20+gap)*time.Millisecond)
								} else {
									at += time.Duration(gap) * time.Millisecond
								}
							}
						}
						payload := enc.encodeAudio(pcm)
						reference = append(reference, clean.decodeAudio(payload)...)
						packets = append(packets, productionAudioPacket{at, at, uint16(i + 100), uint32(i * 160), payload})
					}
					next := 0
					var returns []time.Time
					var resume []time.Duration
					waiting := false
					var returned time.Time
					var previousArrival time.Duration
					maxQueue := 0
					var frames []sipSpeechFrame
					var positions []int
					var maxResidenceMS float64
					first := time.Duration(-1)
					lastOutput := time.Duration(-1)
					maxOutputGap := time.Duration(0)
					for tick := 8 * time.Millisecond; tick < packets[len(packets)-1].arrival+80*time.Millisecond; tick += 20 * time.Millisecond {
						for next < len(packets) && packets[next].arrival <= tick {
							p := packets[next]
							if next > 0 && p.arrival-previousArrival >= time.Duration(gap)*time.Millisecond {
								returned = base.Add(p.arrival)
								returns = append(returns, returned)
								waiting = true
							}
							previousArrival = p.arrival
							b.pushRTPAt(p.seq, p.payload, p.ts, base.Add(p.arrival), base.Add(p.processed))
							maxQueue = max(maxQueue, b.buffered+int(b.pendingPCMSamples.Load()))
							next++
						}
						before := b.driftAdjustedSamples.Load()
						pcm, ready := f.popPCMAt(dec, base.Add(tick))
						if ready {
							if first < 0 {
								first = tick
							}
							if lastOutput >= 0 {
								maxOutputGap = max(maxOutputGap, tick-lastOutput)
							}
							lastOutput = tick
							take := len(pcm) + int(b.driftAdjustedSamples.Load()-before)
							position := int(b.expectedTimestamp)*rate/8000 - len(f.pendingPCM) - take
							positions = append(positions, position)
							frames = append(frames, sipSpeechFrame{tick, append([]int16(nil), pcm...), take})
							maxResidenceMS = max(maxResidenceMS, float64(base.Add(tick).Sub(f.frameArrival))/float64(time.Millisecond))
						} else if first >= 0 {
							positions = append(positions, int((tick-first)*time.Duration(rate)/time.Second))
							frames = append(frames, sipSpeechFrame{at: tick, sourceSamples: rate / 50})
						}
						if ready && len(pcm) != rate/50 {
							t.Fatal("non-20ms output")
						}
						// A rendered frame must actually contain newly decoded audio; do not
						// mistake continuous silence/PLC for recovered speech.
						if waiting && ready && !b.lastPoppedMissing && !f.lastRealAt.Before(returned) {
							resume = append(resume, base.Add(tick).Sub(returned))
							waiting = false
						}
					}

					speechEvidence := analyzeSIPSpeech(reference, rate, frames, positions, float64(first)/float64(time.Millisecond))
					speechEvidence.Codec = codec
					speechEvidence.Fixture = sipSpeechFixtures[0].id
					speechEvidence.Profile = fmt.Sprintf("gap_%d_catch_up_%v", gap, burst)
					// These severe network fixtures cannot demand uninterrupted received speech:
					// source gaps and bounded capacity still remove time. Gate actual decoded
					// output as well as resume latency, allowing the configured 5ms crossfade.
					coverage := 0.99
					if burst {
						coverage = 0.93
					}
					speechEvidence.Accepted = (sipSpeechBudget{coverage: .99, gapMS: 80, delayMS: 100}).accepts(speechEvidence)
					if !speechEvidence.FirstWordPreserved || speechEvidence.ActiveSpeechCoverage < coverage || speechEvidence.LongestPlaybackInterruptionMS > gap {
						t.Errorf("decoded speech recovery gate failed: %+v", speechEvidence)
					}
					results = append(results, map[string]any{"codec": codec, "gap_ms": gap, "catch_up": burst, "resume_ns": resume, "overflow_ms": float64(b.overflowSamples) / 8, "concealed_ms": float64(b.concealedSamples.Load()) / 8, "silence_ms": float64(b.silenceSamples.Load()) / 8, "max_queue_ms": float64(maxQueue) / 8, "max_residence_ms": maxResidenceMS, "max_output_gap_ms": float64(maxOutputGap) / float64(time.Millisecond), "speech": speechEvidence})
					if maxOutputGap > 20*time.Millisecond || maxResidenceMS > 200 {
						t.Errorf("output cadence or residence bound exceeded: %v %.1fms", maxOutputGap, maxResidenceMS)
					}
					t.Logf("resume=%v returns=%d overflow=%.1fms conceal=%.1fms queue=%.1fms decoder resets=%d warmups=%d", resume, len(returns), float64(b.overflowSamples)/8, float64(b.concealedSamples.Load())/8, float64(maxQueue)/8, b.decodeResets, b.decodeWarmups)
					if len(resume) != 3 {
						t.Fatalf("missing real recoveries: %v", resume)
					}
					for _, delay := range resume {
						if delay > 20*time.Millisecond {
							t.Errorf("returning real audio waited %v; want next tick", delay)
						}
					}
					if maxQueue > 160*8 || b.expiredSamples != 0 || b.lost != 0 {
						t.Fatalf("queue/age/loss regression: max=%d expired=%d lost=%d", maxQueue, b.expiredSamples, b.lost)
					}
					if codec == "G722" {
						for tick := packets[len(packets)-1].arrival + 88*time.Millisecond; tick <= packets[len(packets)-1].arrival+288*time.Millisecond; tick += 20 * time.Millisecond {
							f.popPCMAt(dec, base.Add(tick))
						}
						probe := enc.encodeAudio(speech[:rate/50])
						if !bytes.Equal(pcm16ToBytes(dec.decodeAudio(probe)), pcm16ToBytes(clean.decodeAudio(probe))) {
							t.Fatal("G.722 decoder lost continuity through recovery/trim")
						}
					}
				})
			}
		}
	}
}

func TestSIPReceiveRecoveryPreservesRealRemainder(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		t.Run(codec, func(t *testing.T) {
			now := time.Unix(100, 0)
			b := newRTPJitterBuffer()
			f := newRTPAudioFramer(b, sipMediaOffer{Codec: codec, PacketSamples: 160})
			b.beginReceiveRecovery(now)
			// Stale locally generated material surrounds still-fresh received PCM.
			f.pendingPCM = []int16{1, 2, 30, 40, 5, 6}
			f.segments = []sipPCMSegment{{samples: 2, synthetic: true}, {samples: 2, arrival: now, at: now}, {samples: 2, synthetic: true}}
			b.pushRTPAt(10, make([]byte, 160), 1600, now, now)
			generation := b.generation.Load()
			f.discardRecoverySynthetic(bPCMTestRate(codec))
			if !reflect.DeepEqual(f.pendingPCM, []int16{30, 40}) || len(f.segments) != 1 || f.segments[0].synthetic {
				t.Fatal("received remainder changed", f.pendingPCM, f.segments)
			}
			if b.generation.Load() != generation || b.pendingPCMSamples.Load() != int64(2*8000/bPCMTestRate(codec)) {
				t.Fatal("decoder epoch/capacity changed")
			}
		})
	}
}
func bPCMTestRate(codec string) int {
	if codec == "G722" {
		return 16000
	}
	return 8000
}

func TestSIPReceiveRecoveryOnlyRealAudioCompletes(t *testing.T) {
	now := time.Unix(100, 0)
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{DiagnosticWindowMS: 2000})
	defer b.releaseTrace()
	f := newRTPAudioFramer(b, sipMediaOffer{Codec: "PCMA", PacketSamples: 160})
	b.beginReceiveRecovery(now)
	b.pushRTPAt(10, make([]byte, 160), 1600, now, now)
	f.segments = []sipPCMSegment{{samples: 160, rtpTimestamp: 1600, arrival: now, synthetic: true}}
	f.noteRealRender(now.Add(time.Millisecond), 160)
	if b.receiveRecovery.state != "recovering" || !b.receiveRecovery.rendered.IsZero() || b.trace.snapshot()[0].PlayedNS != 0 {
		t.Fatal("synthetic frame claimed real recovery")
	}
	f.segments[0].synthetic = false
	f.noteRealRender(now.Add(8*time.Millisecond), 160)
	if b.receiveRecovery.state != "playing" || b.receiveRecovery.resumeMS != 8 {
		t.Fatal("missing real render evidence", b.receiveRecovery)
	}
	f.noteRealRender(now.Add(28*time.Millisecond), 160)
	if b.trace.snapshot()[0].PlayedNS != now.Add(8*time.Millisecond).UnixNano() {
		t.Fatal("partial continuation overwrote first playback")
	}
	b.trace.insert(11, 1600, now, now, 160, 0, 0)
	b.trace.render(1600, now.Add(40*time.Millisecond))
	b.trace.render(1600, now.Add(60*time.Millisecond))
	if b.trace.snapshot()[0].PlayedNS != now.Add(8*time.Millisecond).UnixNano() || b.trace.snapshot()[1].PlayedNS != now.Add(40*time.Millisecond).UnixNano() {
		t.Fatal("timestamp reuse modified prior render evidence")
	}
	m := &sipRTPMedia{}
	m.jitterTelemetry.Store(b)
	d := &sipLiveDiagnostics{}
	d.media.Store(m)
	snap := d.snapshot(now.Add(time.Second)).Jitter
	if snap.QueueMS != snap.EncodedQueueMS+snap.DecodedQueueMS {
		t.Fatal("inconsistent queue snapshot", snap)
	}
	if snap.ReceiveState != "playing" || snap.ReceiveRecoveries != 1 || snap.ReceiveResumeMS != 8 || !snap.ReceiveReturningAt.Equal(now) || !snap.ReceiveRenderedAt.Equal(now.Add(8*time.Millisecond)) {
		t.Fatal("incorrect snapshot", snap)
	}
}

func TestSIPReceiveRecoveryResetsAndReferenceStillPrime(t *testing.T) {
	for _, reset := range []string{"initial", "hold", "source", "reference"} {
		t.Run(reset, func(t *testing.T) {
			now := time.Unix(100, 0)
			policy := sipPlaybackPolicy{MaxTargetMS: 60}
			if reset == "reference" {
				policy.Receiver = "reference"
			}
			b := newRTPJitterBufferWithPolicy(policy)
			b.pushSourceRTPAt(1, 1, make([]byte, 160), 160, now, now)
			if reset != "initial" {
				// Establish the old stream, then starve it.
				b.pushSourceRTPAt(1, 2, make([]byte, 160), 320, now, now)
				b.pushSourceRTPAt(1, 3, make([]byte, 160), 480, now, now)
				for i := 0; i < 7; i++ {
					b.popAt(now.Add(time.Duration(i) * 20 * time.Millisecond))
				}
				now = now.Add(180 * time.Millisecond)
				switch reset {
				case "hold":
					b.setPaused(true, now)
					b.setPaused(false, now)
				case "source": // pushSource below replaces the old SSRC.
				}
				ssrc := uint32(1)
				if reset == "source" {
					ssrc = 2
				}
				b.pushSourceRTPAt(ssrc, 4, make([]byte, 160), 640, now, now)
			}
			if _, ready := b.popAt(now.Add(time.Millisecond)); ready {
				t.Fatalf("initial/reset/reference playback bypassed reserve: state=%+v primed=%v buffered=%d missing=%d events=%+v", b.receiveRecovery, b.primed, b.buffered, b.missing, b.events[:b.eventCount])
			}
		})
	}
}

func TestSIPReceiveRecoveryCrossfadesAfterLongSilence(t *testing.T) {
	p := sipPCMConcealer{lossSamples: 1, crossfadeMS: 5}
	pcm := make([]int16, 160)
	for i := range pcm {
		pcm[i] = 10000
	}
	got := p.received(pcm, 8000)
	if got[0] != 250 || got[39] != 10000 || got[40] != 10000 {
		t.Fatal("resume did not use bounded 5ms fade", got[:41])
	}
}

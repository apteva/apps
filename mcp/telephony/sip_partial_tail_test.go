package main

import (
	"fmt"
	"reflect"
	"testing"
	"time"
)

// A stopped/repriming jitter buffer can still own a decoded packet remainder.
// Model that boundary directly so packet concealment cannot hide the tail loss.
func sipPartialTailFixture(codec string, rtpSamples int, at time.Time) (*rtpAudioFramer, *sipRTPMedia, []int16) {
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	f := newRTPAudioFramer(newRTPJitterBuffer(), m.offer)
	pcm := make([]int16, rtpSamples*m.sampleRate()/8000)
	for i := range pcm {
		pcm[i] = int16(1000 + i)
	}
	f.pendingPCM = append([]int16(nil), pcm...)
	f.segments = []sipPCMSegment{{samples: len(pcm), at: at, arrival: at, source: at}}
	return f, m, pcm
}

func TestSIPDecodedPartialTailPreserved(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, count := range []int{1, 126, 127, 159} {
			t.Run(fmt.Sprintf("%s/%d", codec, count), func(t *testing.T) {
				at := time.Unix(100, 0)
				f, m, real := sipPartialTailFixture(codec, count, at)
				out, ready := f.popPCMAt(m, at.Add(20*time.Millisecond))
				if !ready || len(out) != m.sampleRate()/50 {
					t.Fatal("fresh trailing audio was withheld", ready, len(out))
				}
				if !reflect.DeepEqual(out[:len(real)], real) {
					t.Fatal("padding changed real speech samples")
				}
				if !f.frameArrival.Equal(at) {
					t.Fatal("padding renewed the trailing samples' forwarding age")
				}
				for _, sample := range out[len(real):] {
					if sample != 0 {
						t.Fatal("tail was stretched or filled with repeated speech")
					}
				}
				if len(f.pendingPCM) != 0 || len(f.segments) != 0 || f.jitter.pendingPCMSamples.Load() != 0 {
					t.Fatal("played tail retained in buffer")
				}
				if f.jitter.expiredSamples != 0 || f.jitter.missingFilledSamples.Load() != 0 || f.jitter.lastDiscardNS.Load() != 0 {
					t.Fatal("tail padding counted as loss/discard")
				}
				padding := float64(160-count) / 8
				m.jitterTelemetry.Store(f.jitter)
				d := &sipLiveDiagnostics{}
				d.media.Store(m)
				snapshot := d.snapshot(at.Add(20 * time.Millisecond))
				if snapshot.Jitter.TailPaddingMS != padding || snapshot.Jitter.TailPaddedFrames != 1 || snapshot.ProcessingState != "healthy" {
					t.Fatal("tail padding telemetry missing or misclassified", snapshot.Jitter)
				}
				events := snapshot.Jitter.Events
				if len(events) != 1 || events[0].Reason != "decoded_partial_padded" || events[0].DurationMS != padding || events[0].PreservedAudioMS != float64(count)/8 {
					t.Fatal("fractional preserved/padded durations missing", events)
				}
				summary := audioDashboardSummary{Metrics: map[string]float64{}}
				addCarrierAudioSummary(&summary, &carrierTelemetryRecord{SIP: &snapshot})
				if summary.Metrics["sip_tail_padding_ms"] != padding || summary.Metrics["sip_tail_padded_frames"] != 1 || len(summary.Issues) != 0 {
					t.Fatal("dashboard counted tail padding as an audio failure", summary)
				}
				for tick := 2; tick <= 15; tick++ {
					if out, _ := f.popPCMAt(m, at.Add(time.Duration(tick)*20*time.Millisecond)); speechRMS(out) != 0 {
						t.Fatal("tail replayed during silence")
					}
				}
				if f.jitter.expiredSamples != 0 || f.jitter.expected != 0 || f.jitter.haveTimestamp {
					t.Fatal("padding changed RTP sequence/timestamp tracking or expired played speech")
				}
			})
		}
	}
}

func TestSIPDecodedPartialTailBurstAndResume(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			at := time.Unix(100, 0)
			m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			encoder := &sipRTPMedia{offer: m.offer}
			b := newRTPJitterBuffer()
			f := newRTPAudioFramer(b, m.offer)
			pcm := make([]int16, 126*m.sampleRate()/8000)
			for i := range pcm {
				pcm[i] = int16(1000 + i)
			}
			for i := 0; i < 4; i++ {
				b.pushRTP(uint16(i), encoder.encodeAudio(pcm), uint32(i*126), at.Add(time.Duration(i)*15750*time.Microsecond))
			}
			for tick := 0; tick < 20; tick++ {
				f.popPCMAt(m, at.Add(60*time.Millisecond+time.Duration(tick)*20*time.Millisecond))
			}
			if b.tailPaddedFrames != 1 || b.expiredSamples != 0 || b.lost != 0 || len(f.pendingPCM) != 0 {
				t.Fatal("decoded burst tail did not drain cleanly", b.tailPaddedFrames, b.expiredSamples, b.lost, len(f.pendingPCM))
			}
			// Resume after ordinary silence with contiguous real RTP sequence
			// numbers, advancing timestamps for the source's silent interval.
			for i := 0; i < 3; i++ {
				payload := encoder.encodeAudio(make([]int16, m.sampleRate()/50))
				b.pushRTP(uint16(4+i), payload, uint32(8000+i*160), at.Add(time.Second+time.Duration(i)*20*time.Millisecond))
			}
			if out, ready := f.popPCMAt(m, at.Add(1060*time.Millisecond)); !ready || len(out) != m.sampleRate()/50 {
				t.Fatal("padding blocked normal speech resumption")
			}
			if b.lost != 0 || b.lateDroppedSamples != 0 || b.expiredSamples != 0 || b.tailPaddedFrames != 1 {
				t.Fatal("tail padding damaged RTP continuity")
			}
		})
	}
}

func TestSIPDecodedPartialTailExactFreshResume(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		t.Run(codec, func(t *testing.T) {
			at := time.Unix(100, 0)
			f, m, _ := sipPartialTailFixture(codec, 127, at)
			if _, ready := f.popPCMAt(m, at.Add(20*time.Millisecond)); !ready {
				t.Fatal("tail withheld")
			}
			// Isolate framing from intentional PLC recovery fades.
			f.jitter.policy.Concealment = new(bool)
			encoder, reference := &sipRTPMedia{offer: m.offer}, &sipRTPMedia{offer: m.offer}
			var expected []int16
			for i := 0; i < 3; i++ {
				pcm := make([]int16, m.sampleRate()/50)
				for sample := range pcm {
					pcm[sample] = int16(-2000 - sample)
				}
				payload := encoder.encodeAudio(pcm)
				expected = append(expected, reference.decodeAudio(payload)...)
				f.jitter.pushRTP(uint16(i), payload, uint32(8000+i*160), at.Add(time.Second+time.Duration(i)*20*time.Millisecond))
			}
			out, ready := f.popPCMAt(m, at.Add(1060*time.Millisecond))
			if !ready || !reflect.DeepEqual(out, expected[:m.sampleRate()/50]) {
				t.Fatal("resumed real audio changed or mixed with the played tail")
			}
		})
	}
}

func TestSIPDecodedPartialTailFractionalG722(t *testing.T) {
	at := time.Unix(100, 0)
	f, m, _ := sipPartialTailFixture("G722", 126, at)
	pending := append(f.pendingPCM, 2345) // 253 / 16 kHz = 15.8125 ms.
	for tick := 1; tick <= 2; tick++ {
		f.pendingPCM = pending
		f.segments = []sipPCMSegment{{samples: len(pending), at: at, arrival: at, source: at}}
		if _, ready := f.popPCMAt(m, at.Add(time.Duration(tick)*20*time.Millisecond)); !ready {
			t.Fatal("fractional G.722 tail withheld")
		}
	}
	if f.jitter.tailPaddedFrames != 2 || f.jitter.tailPaddingNS != int64(8375*time.Microsecond) || f.jitter.eventCount != 1 {
		t.Fatal("fractional padding counters or event coalescing incorrect")
	}
	e := f.jitter.events[0]
	if e.Frames != 2 || e.DurationMS != 8.375 || e.PreservedAudioMS != 31.625 {
		t.Fatal("coalesced event lost sub-millisecond precision", e)
	}
}

func BenchmarkSIPDecodedPartialTail(b *testing.B) {
	for _, tail := range []bool{false, true} {
		b.Run(fmt.Sprint("tail=", tail), func(b *testing.B) {
			at := time.Unix(100, 0)
			f, m, _ := sipPartialTailFixture("G722", 126, at)
			pending, segments := f.pendingPCM, f.segments
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if tail {
					f.pendingPCM, f.segments = pending, segments
				} else {
					f.pendingPCM, f.segments = nil, nil
				}
				f.popPCMAt(m, at.Add(20*time.Millisecond))
			}
		})
	}
}

func TestSIPDecodedPartialTailSafety(t *testing.T) {
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, condition := range []string{"expired", "priming", "hold", "replacement"} {
			t.Run(codec+"/"+condition, func(t *testing.T) {
				at := time.Unix(100, 0)
				f, m, _ := sipPartialTailFixture(codec, 126, at)
				now := at.Add(20 * time.Millisecond)
				switch condition {
				case "expired":
					now = at.Add(201 * time.Millisecond)
				case "priming":
					f.jitter.pushRTP(1, make([]byte, 160), 0, at)
				case "hold":
					f.jitter.setPaused(true, at)
				case "replacement":
					f.jitter.generation.Add(1)
				}
				if _, ready := f.popPCMAt(m, now); ready {
					t.Fatal("tail bypassed expiry, priming, hold or generation protection")
				}
				if condition == "expired" && f.jitter.expiredSamples != 126 {
					t.Fatal("stale speech no longer expires")
				}
				if condition == "priming" && len(f.pendingPCM) != 126*m.sampleRate()/8000 {
					t.Fatal("priming lost its fresh remainder")
				}
			})
		}
	}
}

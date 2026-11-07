package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

type sipShortGapResult struct {
	GapFilledMS, GapSkippedMS                                             float64
	ClockResets, DecoderResets, DecoderWarmups                            int
	Codec, Scenario                                                       string
	Speech                                                                sipSpeechEvidence
	PlaybackStartsMS, RebufferAtMS                                        []float64
	DeclaredGapMS, ConcealedMS, MissingFilledMS, DiscardedMS              float64
	MaxQueueMS, MaxPlayedResidenceMS, MaxTimelineLeadMS, MaxTimelineLagMS float64
	MaxOutputGapMS                                                        float64
	SequenceLoss                                                          int
	ConsumerMaxMS                                                         float64
}

// This is a constructed reproduction, not a packet replay of 0874dda6.... Its
// per-packet timing was disabled. Use the retained initial sequence/timestamp,
// 40+20 ms source jumps, 20 ms packet size, tiny ingress age and arrival variation.
func replaySIPShortSourceGaps(t *testing.T, codec, scenario string) sipShortGapResult {
	t.Helper()
	base := time.Unix(100, 0)
	initialSeq, initialTS := uint16(4909), uint32(42400)
	if scenario == "wrapped_report" {
		initialSeq, initialTS = 65500, 0xfffff000
	}
	offer := sipMediaOffer{Codec: codec, PacketSamples: 160}
	encoder, decoder, clean := &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}
	b := newRTPJitterBuffer()
	f := newRTPAudioFramer(b, offer)
	rate := decoder.sampleRate()
	source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, rate)
	var packets []productionAudioPacket
	var reference []int16
	for i := 0; i < 350; i++ {
		gapMS := 0
		switch scenario {
		case "report_shaped", "report_jitter", "wrapped_report":
			if i >= 5 {
				gapMS += 40
			}
			if i >= 75 {
				gapMS += 20
			}
		case "10ms_partial":
			if i >= 5 {
				gapMS = 10
			}
		case "timestamp_discontinuity_2s":
			if i >= 5 {
				gapMS = 2000
			}
		case "40ms":
			if i >= 5 {
				gapMS = 40
			}
		case "20ms":
			if i >= 5 {
				gapMS = 20
			}
		case "repeated_short_silence":
			gapMS = (i / 25) * 20
		}
		sourceSamples := (i*20 + gapMS) * rate / 1000
		pcm := make([]int16, rate/50)
		for j := range pcm {
			pcm[j] = source[(i*len(pcm)+j)%len(source)]
		}
		// A finite quiet tail flushes all real speech before deliberate shutdown.
		if i >= 325 {
			clear(pcm)
		}
		payload := encoder.encodeAudio(pcm)
		decoded := clean.decodeAudio(payload)
		for len(reference) < sourceSamples+len(decoded) {
			reference = append(reference, 0)
		}
		copy(reference[sourceSamples:], decoded)
		arrival := 67*time.Millisecond + time.Duration(i*20+gapMS)*time.Millisecond
		if scenario == "timestamp_discontinuity_2s" {
			arrival -= 2 * time.Second
			if i < 5 {
				arrival += 2 * time.Second
			}
		}
		if (scenario == "report_shaped" || scenario == "wrapped_report") && i >= 75 {
			arrival += 8 * time.Millisecond
		}
		if scenario == "arrival_gap_60" && i >= 75 {
			arrival += 40 * time.Millisecond
		}
		if scenario == "jitter" || scenario == "report_jitter" {
			arrival += time.Duration(i*7%19) * time.Millisecond
		}
		if scenario == "long_stall" && i >= 75 && i < 100 {
			arrival = 2067 * time.Millisecond
		}
		if scenario == "loss" && i == 75 {
			continue
		}
		packets = append(packets, productionAudioPacket{arrival, arrival + 39*time.Microsecond, initialSeq + uint16(i), initialTS + uint32((i*20+gapMS)*8), payload})
	}
	// Delayed batches keep source order in these fixtures.
	next := 0
	var output []sipSpeechFrame
	var positions []int
	var actual []int16
	r := sipShortGapResult{Codec: codec, Scenario: scenario}
	first := time.Duration(-1)
	lastOutput := time.Duration(-1)
	end := packets[len(packets)-1].processed + 400*time.Millisecond
	for tick := 8 * time.Millisecond; tick <= end; tick += 20 * time.Millisecond {
		for next < len(packets) && packets[next].processed <= tick {
			p := packets[next]
			b.pushRTPAt(p.seq, p.payload, p.ts, base.Add(p.arrival), base.Add(p.processed))
			r.MaxQueueMS = max(r.MaxQueueMS, float64(b.buffered+int(b.pendingPCMSamples.Load()))/8)
			next++
		}
		before := b.driftAdjustedSamples.Load()
		if pcm, ready := f.popPCMAt(decoder, base.Add(tick)); ready {
			actual = append(actual, pcm...)
			if lastOutput >= 0 {
				r.MaxOutputGapMS = max(r.MaxOutputGapMS, float64(tick-lastOutput)/float64(time.Millisecond))
			}
			lastOutput = tick
			take := len(pcm) + int(b.driftAdjustedSamples.Load()-before)
			position := int(int32(b.expectedTimestamp-initialTS))*rate/8000 - len(f.pendingPCM) - take
			positions = append(positions, position)
			output = append(output, sipSpeechFrame{tick, append([]int16(nil), pcm...), take})
			if first < 0 {
				first = tick
			}
			if position >= 0 && position+take <= len(reference) && speechRMS(pcm) > 200 {
				phase := float64(tick-first)/float64(time.Millisecond) - float64(position)*1000/float64(rate)
				r.MaxTimelineLeadMS = max(r.MaxTimelineLeadMS, -phase)
				r.MaxTimelineLagMS = max(r.MaxTimelineLagMS, phase)
			}
			r.MaxPlayedResidenceMS = max(r.MaxPlayedResidenceMS, float64(base.Add(tick).Sub(f.frameArrival))/float64(time.Millisecond))
		} else if first >= 0 {
			// Missing output is a real playback hole even when all RTP eventually
			// arrives. Do not omit it from the speech-continuity measurements.
			position := int((tick - first) * time.Duration(rate) / time.Second)
			positions = append(positions, position)
			output = append(output, sipSpeechFrame{at: tick, sourceSamples: rate / 50})
		}
		if next == len(packets) && b.buffered == 0 && len(f.pendingPCM) < rate/50 {
			break
		}
	}
	r.ClockResets, r.DecoderResets, r.DecoderWarmups = b.clockResets, b.decodeResets, b.decodeWarmups
	r.Speech = analyzeSIPSpeech(reference, rate, output, positions, float64(first)/float64(time.Millisecond))
	if scenario == "normal" {
		n := 325 * rate / 50
		r.Speech.CompleteHealthyPCMIdentical = len(actual) >= n && bytes.Equal(pcm16ToBytes(actual[:n]), pcm16ToBytes(reference[:n]))
	}
	r.Speech.Accepted = (sipSpeechBudget{coverage: .99, gapMS: 80, delayMS: 100}).accepts(r.Speech)
	r.ConcealedMS = float64(b.concealedSamples.Load()) / 8
	r.DeclaredGapMS = float64(b.sourceGapDeclaredSamples) / 8
	r.GapFilledMS = float64(b.sourceGapFilledSamples.Load()) / 8
	r.GapSkippedMS = float64(b.sourceGapSkippedSamples) / 8
	r.MissingFilledMS = float64(b.missingFilledSamples.Load()) / 8
	r.DiscardedMS = float64(b.droppedSamples+b.lateDroppedSamples) / 8
	r.SequenceLoss = b.lost
	r.ConsumerMaxMS = float64(b.maxConsumerIntervalNS.Load()) / 1e6
	for i := 0; i < b.eventCount; i++ {
		e := b.events[(b.eventHead-b.eventCount+i+64)%64]
		switch e.Reason {
		case "playback_started":
			r.PlaybackStartsMS = append(r.PlaybackStartsMS, float64(e.At.Sub(base))/float64(time.Millisecond))
		case "fresh_delivery_rebuffer":
			r.RebufferAtMS = append(r.RebufferAtMS, float64(e.At.Sub(base))/float64(time.Millisecond))
		}
	}
	return r
}

func TestSIPShortSourceGapContinuity(t *testing.T) {
	var results []sipShortGapResult
	t.Cleanup(func() {
		if path := os.Getenv("TELEPHONY_SHORT_GAP_RESULTS"); path != "" {
			raw, err := json.MarshalIndent(results, "", "  ")
			if err != nil {
				t.Error(err)
				return
			}
			if err = os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
				t.Error(err)
			}
		}
	})
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		for _, scenario := range []string{"normal", "report_shaped", "report_jitter", "wrapped_report", "40ms", "20ms", "10ms_partial", "repeated_short_silence", "jitter", "arrival_gap_60", "loss", "long_stall", "timestamp_discontinuity_2s"} {
			t.Run(codec+"/"+scenario, func(t *testing.T) {
				r := replaySIPShortSourceGaps(t, codec, scenario)
				results = append(results, r)
				t.Logf("%s", mustShortGapJSON(r))
				if r.MaxQueueMS > 160 || r.MaxPlayedResidenceMS > 200 || r.ConsumerMaxMS > 20.01 {
					t.Fatal("unchanged latency/queue/scheduling bounds violated")
				}
				switch scenario {
				case "normal", "report_shaped", "report_jitter", "wrapped_report", "40ms", "20ms", "10ms_partial", "repeated_short_silence", "jitter", "arrival_gap_60":
					if scenario == "normal" && !r.Speech.CompleteHealthyPCMIdentical {
						t.Fatal("healthy speech samples changed")
					}
					if r.DiscardedMS != 0 || r.SequenceLoss != 0 || r.ClockResets != 0 || r.DecoderResets != 0 {
						t.Fatal("fresh speech discarded or classified as network loss")
					}
					if len(r.RebufferAtMS) > 0 || len(r.PlaybackStartsMS) != 1 {
						t.Fatal("short transition unnecessarily re-primed", r.RebufferAtMS, r.PlaybackStartsMS)
					}
					if !r.Speech.FirstWordPreserved || r.Speech.ActiveSpeechCoverage < .995 || r.Speech.LongestDamagedSpeechMS > 40 {
						t.Fatal("speech-continuity qualification failed")
					}
					if scenario != "arrival_gap_60" && (r.MaxTimelineLeadMS > 5 || r.MaxTimelineLagMS > 40) {
						t.Fatal("source timeline compressed or delayed", r.MaxTimelineLeadMS, r.MaxTimelineLagMS)
					}
				case "timestamp_discontinuity_2s":
					if r.GapSkippedMS != 2000 || r.GapFilledMS != 0 || r.DiscardedMS != 0 || r.MaxOutputGapMS > 20.01 {
						t.Fatal("unbounded source gap delayed or discarded fresh speech")
					}
				case "loss":
					if r.SequenceLoss != 1 {
						t.Fatal("genuine loss accounting changed", r.SequenceLoss)
					}
				case "long_stall":
					if r.DiscardedMS == 0 {
						t.Fatal("stall did not exercise stale/overflow protection")
					}
					if r.Speech.LongestDamagedSpeechMS > 700 || r.Speech.MaxAddedDelayMS > 200 {
						t.Fatal("long stall failed bounded recovery")
					}
				}
				if r.MaxOutputGapMS > 20.01 {
					t.Fatal("playback output stalled", r.MaxOutputGapMS)
				}
				if scenario == "report_shaped" && (r.DeclaredGapMS != 60 || r.GapFilledMS != 60 || r.GapSkippedMS != 0 || r.ConcealedMS != 0) {
					t.Fatal("declared source gap was misclassified as packet loss or skipped", r.DeclaredGapMS, r.GapFilledMS, r.ConcealedMS)
				}
			})
		}
	}
}
func mustShortGapJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprint(err))
	}
	return string(raw)
}

func TestSIPSourceGapTelemetrySeparateFromConcealment(t *testing.T) {
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		t.Run(codec, func(t *testing.T) {
			a, _ := withTelephonyTestContext(t, &answerPlatform{})
			row := recoveryCall(t, a, "source-gap-telemetry", "telnyx")
			d := a.newSIPDiagnostics(row, "dialog")
			defer d.end("local", nil)
			m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
			enc := &sipRTPMedia{offer: m.offer}
			d.attach(m, "dialog")
			b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
			m.jitterTelemetry.Store(b)
			f := newRTPAudioFramer(b, m.offer)
			base := time.Now()
			payload := enc.encodeAudio(make([]int16, m.sampleRate()/50))
			for i := 0; i < 3; i++ {
				b.pushRTP(uint16(i), payload, uint32(i*160), base.Add(time.Duration(i)*20*time.Millisecond))
			}
			for tick := 40; tick <= 80; tick += 20 {
				f.popPCMAt(m, base.Add(time.Duration(tick)*time.Millisecond))
			}
			b.pushRTP(3, payload, 800, base.Add(100*time.Millisecond))
			for tick := 100; tick <= 140; tick += 20 {
				f.popPCMAt(m, base.Add(time.Duration(tick)*time.Millisecond))
			}
			s := d.sample(base.Add(140 * time.Millisecond))
			j := s.SIP.Jitter
			if j.SourceTimestampGapMS != 40 || j.SourceGapFilledMS != 40 || j.SourceGapSkippedMS != 0 || j.ConcealedMS != 0 || j.MissingFilledMS != 0 || j.DroppedMS != 0 || j.LateDroppedMS != 0 || s.SIP.RTP.SequenceLoss != 0 {
				t.Fatalf("source gap hidden or misreported: %+v", j)
			}
			seen := false
			for _, e := range j.Events {
				if e.Reason == "source_silence" && e.SourceGapMS == 40 {
					seen = true
				}
			}
			if !seen {
				t.Fatal("original source timestamp evidence was not retained")
			}
		})
	}
}

func TestSIPSourceGapNeverExpiresFreshPacketToFillTimeline(t *testing.T) {
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{MaxTargetMS: 60})
	base := time.Unix(100, 0)
	for i := 0; i < 3; i++ {
		b.pushRTP(uint16(i), make([]byte, 160), uint32(i*160), base.Add(time.Duration(i)*20*time.Millisecond))
	}
	for tick := 40; tick <= 80; tick += 20 {
		b.popAt(base.Add(time.Duration(tick) * time.Millisecond))
	}
	b.pushRTP(3, make([]byte, 160), 800, base.Add(90*time.Millisecond))
	// The real packet still fits its 200 ms local age bound. Waiting through a
	// 40 ms source gap would expire it, so skip only the obsolete gap interval.
	pcm, ready := b.popAt(base.Add(280 * time.Millisecond))
	if !ready || len(pcm) != 160 || b.sourceGapSkippedSamples != 320 || b.sourceGapFilledSamples.Load() != 0 || b.droppedSamples != 0 {
		t.Fatal("timestamp padding displaced fresh speech", ready, len(pcm), b.sourceGapSkippedSamples, b.droppedSamples)
	}
}

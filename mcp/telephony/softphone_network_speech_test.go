package main

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"sort"
	"testing"
	"time"

	"github.com/pion/rtp"
)

// Identical source-index faults for every bitrate. Uses the production codec,
// jitter, decoder/repair and release functions with a virtual monotonic clock.
// Excludes browser capture, ICE, carrier/PSTN and native browser congestion control.
func TestNetworkSpeechReplay(t *testing.T) {
	var results []map[string]any
	for _, fixture := range sipSpeechFixtures {
		source := loadSIPSpeech(t, fixture.id, fixture.hash, 48000)
		for _, bitrate := range []int{32000, 20000, 16000} {
			encoder, err := newRTCOpusEncoder(bitrate)
			if err != nil {
				t.Fatal(err)
			}
			clean, err := newRTCOpusDecoder()
			if err != nil {
				t.Fatal(err)
			}
			packets := make([][]byte, 1000)
			var reference []int16
			frame, buf, decoded := make([]float32, 960), make([]byte, 1275), make([]int16, 2880)
			for i := range packets {
				for k := range frame {
					frame[k] = float32(source[(i*960+k)%len(source)]) / 32768
				}
				n, e := encoder.EncodeFloat32(frame, buf)
				if e != nil {
					t.Fatal(e)
				}
				packets[i] = append([]byte(nil), buf[:n]...)
				n, e = clean.decode(packets[i], decoded)
				if e != nil || n != 480 {
					t.Fatal(n, e)
				}
				reference = append(reference, decoded[:n]...)
			}
			encoder.Close()
			clean.Close()
			for _, scenario := range []string{"healthy", "jitter", "one-percent-loss", "60ms-burst", "420ms-delivery-pause", "constrained-48k"} {
				t.Run(fmt.Sprintf("%s/%d/%s", fixture.id, bitrate, scenario), func(t *testing.T) {
					type arrival struct{ index, ms int }
					var arrivals []arrival
					missing := 0
					serialFreeMS := 0.0
					for i := range packets {
						if scenario == "one-percent-loss" && i > 50 && i < 900 && i%100 == 75 || scenario == "60ms-burst" && i >= 250 && i < 253 {
							missing++
							continue
						}
						ms := i*20 + 20
						if scenario == "constrained-48k" {
							capacity := 96000.0
							if i >= 250 && i < 750 {
								capacity = 48000
							}
							// Encoded Opus bytes plus modeled RTP/SRTP/UDP/IP overhead.
							next := max(serialFreeMS, float64(i*20)) + float64(len(packets[i])+64)*8000/capacity
							if next-float64(i*20) > 200 {
								missing++
								continue
							}
							serialFreeMS = next
							ms = int(next + 20)
						}
						if scenario != "healthy" {
							ms += (i * 17) % 21
						}
						if scenario == "420ms-delivery-pause" && ms >= 5000 && ms < 5420 {
							ms = 5420
						}
						arrivals = append(arrivals, arrival{i, ms})
					}
					sort.SliceStable(arrivals, func(i, j int) bool { return arrivals[i].ms < arrivals[j].ms })
					decoder, e := newRTCOpusDecoder()
					if e != nil {
						t.Fatal(e)
					}
					defer decoder.Close()
					epoch := time.Unix(1000, 0)
					stats := &rtcMediaStats{}
					j := rtcJitter{discard: func(p *rtp.Packet) {
						stats.drop("webrtc_rtp_rejected", "operator_to_carrier", uint64(p.SequenceNumber), 0)
					}}
					var timeline rtcDecodeTimeline
					var release rtcAudioRelease
					pcm := make([]int16, 2880)
					var output []sipSpeechFrame
					var positions []int
					var all []int16
					var lastOutput, maxOutputGap time.Duration
					index := 0
					sourceOrigin := epoch.Add(time.Duration(arrivals[0].ms) * time.Millisecond)
					for ms := 0; ms < 20500; ms++ {
						now := epoch.Add(time.Duration(ms) * time.Millisecond)
						for index < len(arrivals) && arrivals[index].ms <= ms {
							a := arrivals[index]
							index++
							// Exercise RTP sequence wrap as well as ordinary progression.
							p := &rtp.Packet{Header: rtp.Header{SSRC: 42, SequenceNumber: uint16(65000 + a.index), Timestamp: uint32(a.index * 960)}, Payload: packets[a.index]}
							if !j.push(p, now) {
								stats.drop("webrtc_rtp_rejected", "operator_to_carrier", uint64(p.SequenceNumber), 0)
							}
						}
						for p := j.pop(now); p != nil; p = j.pop(now) {
							decodeRTCPacket(p, &j, &timeline, &release, decoder, pcm, stats, "operator_to_carrier", true, now, 0)
						}
						if f, ok := release.pop(now, stats, "operator_to_carrier"); ok {
							if lastOutput > 0 {
								maxOutputGap = max(maxOutputGap, now.Sub(epoch)-lastOutput)
							}
							lastOutput = now.Sub(epoch)
							pos := int((f.source.Sub(sourceOrigin)+10*time.Millisecond)/(20*time.Millisecond)) * 480
							positions = append(positions, pos)
							output = append(output, sipSpeechFrame{at: now.Sub(epoch), pcm: append([]int16(nil), f.pcm...)})
							all = append(all, f.pcm...)
						}
					}
					r := analyzeSIPSpeech(reference, 24000, output, positions, 80)
					r.CompleteHealthyPCMIdentical = slices.Equal(reference, all)
					snapshot := stats.snapshot()
					results = append(results, map[string]any{"fixture": fixture.id, "bitrate": bitrate, "scenario": scenario, "speech": r, "network_missing_packets": missing, "local_discard_ms": snapshot.IngressDiscardedMS, "rejected_packets": snapshot.IngressRejectedPackets, "replacement_ms": snapshot.IngressConcealedMS, "longest_output_gap_ms": float64(maxOutputGap) / float64(time.Millisecond)})
					t.Logf("coverage=%.3f longest damaged=%dms delay=%.1fms discard=%dms replacement=%dms", r.ActiveSpeechCoverage, r.LongestDamagedSpeechMS, r.MaxAddedDelayMS, snapshot.IngressDiscardedMS, snapshot.IngressConcealedMS)
					if snapshot.DecodeErrors != 0 || r.MaxAddedDelayMS > 180 || maxOutputGap > 460*time.Millisecond {
						t.Fatal("decode failure or unbounded delay", snapshot.DecodeErrors, r)
					}
					if scenario == "healthy" || scenario == "jitter" {
						if maxOutputGap > 22*time.Millisecond || !r.CompleteHealthyPCMIdentical || !r.FirstWordPreserved || snapshot.IngressDiscardedMS != 0 || snapshot.IngressRejectedPackets != 0 || snapshot.IngressConcealedMS != 0 {
							t.Fatal("ordinary delivery altered source audio", r, snapshot)
						}
					} else {
						if !r.FirstWordPreserved || scenario != "constrained-48k" && (r.ActiveSpeechCoverage < .8 || r.LongestDamagedSpeechMS > 700) {
							t.Fatal("fault scenario exceeded speech continuity budget", r)
						}
						// A stall must not poison the remainder of the call or add permanent delay.
						// Loss changes predictive decoder state: compare the recovery tail
						// by strict correlation/error, not floating-point bit identity.
						if len(all) < 24000 {
							t.Fatal("no recovery tail")
						}
						correlation, relativeError := speechSimilarity(all[len(all)-24000:], reference[len(reference)-24000:])
						t.Logf("recovery tail correlation=%.6f relative error=%.6f", correlation, relativeError)
						if correlation < .99 || relativeError > .1 {
							t.Fatal("final second did not recover", correlation, relativeError)
						}
					}
				})
			}
		}
	}
	// The constrained link is an adverse comparison, not a claim that 32 kbps
	// was usable. The explicit low-bandwidth profile must improve both speech
	// preservation and delay using the same natural speech and link schedule.
	for _, fixture := range sipSpeechFixtures {
		var before, after map[string]any
		for _, r := range results {
			if r["fixture"] == fixture.id && r["scenario"] == "constrained-48k" {
				if r["bitrate"] == 32000 {
					before = r
				}
				if r["bitrate"] == 20000 {
					after = r
				}
			}
		}
		b, a := before["speech"].(sipSpeechEvidence), after["speech"].(sipSpeechEvidence)
		if a.ActiveSpeechCoverage < b.ActiveSpeechCoverage+.1 || a.MaxAddedDelayMS >= b.MaxAddedDelayMS || after["local_discard_ms"].(int64) != 0 {
			t.Fatal("low-bandwidth profile did not improve actual speech replay", before, after)
		}
	}
	if path := os.Getenv("TELEPHONY_NETWORK_SPEECH_RESULTS"); path != "" {
		raw, _ := json.MarshalIndent(map[string]any{"scope": "production Opus + RTP jitter + decode/repair + paced release; deterministic natural speech replay, not MOS or browser/PSTN", "results": results}, "", "  ")
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

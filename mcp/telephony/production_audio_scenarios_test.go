package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

// These deterministic profiles combine timing boundaries observed in production.
// They are deliberately synthetic: retained counters/events cannot reconstruct
// per-packet carrier send times, microphone speech, or a browser's exact network.
// Every profile returns to normal delivery, so recovery must not keep rejecting
// healthy speech after an incident. All supported SIP codecs share an 8k RTP clock.
type productionAudioProfile struct {
	name    string
	seconds int
	ppm     float64
}
type productionAudioPacket struct {
	arrival, processed time.Duration
	seq                uint16
	ts                 uint32
	payload            []byte
}
type productionAudioResult struct {
	Profile, Codec, Version                                    string
	LateMS, BufferMS, ReplacementMS, MaxQueueMS, TailDiscardMS float64
	Recoveries, TailFrames                                     int
}

func productionAudioPackets(p productionAudioProfile, payload []byte) []productionAudioPacket {
	out := make([]productionAudioPacket, 0, p.seconds*50)
	for i := 0; i < p.seconds*50; i++ {
		source := time.Duration(float64(i) * float64(20*time.Millisecond) * (1 + p.ppm/1e6))
		delay, residence := time.Duration(0), time.Duration(0)
		switch p.name {
		case "long_drift_and_jitter":
			if i < (p.seconds-15)*50 {
				delay = time.Duration((60 + 60*math.Sin(2*math.Pi*float64(i)/1500)) * float64(time.Millisecond))
			}
		case "stable_shift", "stable_shift_small_jitter", "stable_shift_alternating_12ms":
			if i >= 500 && i < (p.seconds-15)*50 {
				delay = 240 * time.Millisecond
				if p.name == "stable_shift_alternating_12ms" {
					delay += time.Duration(i%2) * 12 * time.Millisecond
				}
				if p.name == "stable_shift_small_jitter" {
					delay += time.Duration(2 * math.Sin(float64(i)/30) * float64(time.Millisecond))
				}
			}
		case "second_shift_during_cooldown":
			if i >= 500 && i < 2000 {
				delay = 240 * time.Millisecond
				if i >= 750 {
					delay = 480 * time.Millisecond
				}
			}
		case "delivery_bursts_300_360_420":
			if i >= 100 && i < (p.seconds-15)*50 {
				block := i / 200
				frames := []int{15, 18, 21}[block%3]
				if offset := i % 200; offset < frames {
					delay = time.Duration(frames-offset) * 20 * time.Millisecond
				}
			}
		case "age_budget_oscillation":
			if i >= 500 && i < (p.seconds-15)*50 {
				delay = time.Duration((160 + 60*math.Sin(2*math.Pi*float64(i)/50)) * float64(time.Millisecond))
			}
		case "startup_socket_residence_440ms":
			if i < 22 {
				residence = 440*time.Millisecond - source
			}
		case "local_handler_stall_283ms":
			if i >= 500 && i < 515 {
				residence = 283 * time.Millisecond
			}
		case "loss_reorder_duplicates":
			if i == 650 || i == 1050 || i == 1350 {
				continue
			}
			if i >= 500 && i < 1500 && i%100 == 0 {
				delay = 60 * time.Millisecond
			}
		case "fourteen_minute_mixed_incident":
			// Healthy opening, slow delivery variation, sparse loss/reordering,
			// repeated genuine 420ms bursts, then an uncontaminated recovery tail.
			if i >= 15000 && i < (p.seconds-15)*50 {
				delay = time.Duration((60 + 60*math.Sin(2*math.Pi*float64(i)/1500)) * float64(time.Millisecond))
				if k := i % 1000; k < 21 {
					delay += time.Duration(21-k) * 20 * time.Millisecond
				}
			}
			if i == 6000 || i == 16000 || i == 29000 || i == 40000 {
				continue
			}
			if i%5000 == 4999 {
				delay += 60 * time.Millisecond
			}
		}
		q := productionAudioPacket{source + delay, source + delay + residence, uint16(65500 + i), uint32(0xffffff00 + uint64(i)*160), payload}
		out = append(out, q)
		if p.name == "loss_reorder_duplicates" && i%137 == 0 {
			out = append(out, q)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].processed < out[j].processed })
	return out
}
func runProductionAudioProfile(t *testing.T, p productionAudioProfile, codec, version string) productionAudioResult {
	t.Helper()
	enabled := version == "automatic_legacy_flag_true"
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: &enabled})
	old := &v21SIPClockBuffer{rtpJitterBuffer: b}
	m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	enc := &sipRTPMedia{offer: m.offer}
	pcm := make([]int16, m.sampleRate()/50)
	for i := range pcm {
		pcm[i] = int16(4000*math.Sin(2*math.Pi*180*float64(i)/float64(m.sampleRate())) + 1500*math.Sin(2*math.Pi*670*float64(i)/float64(m.sampleRate())))
	}
	packets := productionAudioPackets(p, enc.encodeAudio(pcm))
	f := newRTPAudioFramer(b, m.offer)
	base := time.Unix(100, 0)
	next := 0
	r := productionAudioResult{Profile: p.name, Codec: codec, Version: version}
	for tick := time.Duration(0); tick < time.Duration(p.seconds)*time.Second; tick += 20 * time.Millisecond {
		before := b.lateDroppedSamples + b.droppedSamples
		for next < len(packets) && packets[next].processed <= tick {
			q := packets[next]
			if version == "0.12.21_timing_reference" {
				old.pushRTPAt(q.seq, q.payload, q.ts, base.Add(q.arrival), base.Add(q.processed))
			} else {
				b.pushRTPAt(q.seq, q.payload, q.ts, base.Add(q.arrival), base.Add(q.processed))
			}
			next++
			r.MaxQueueMS = max(r.MaxQueueMS, float64(b.buffered+int(b.pendingPCMSamples.Load()))/8)
		}
		if p.name == "consumer_pause_441ms" && tick >= 10*time.Second && tick < 10*time.Second+441*time.Millisecond {
			continue
		}
		out, ready := f.popPCMAt(m, base.Add(tick))
		r.MaxQueueMS = max(r.MaxQueueMS, float64(b.buffered+int(b.pendingPCMSamples.Load()))/8)
		if ready && len(out) != m.sampleRate()/50 {
			t.Fatal("incorrect output frame duration")
		}
		if tick > time.Duration(p.seconds-10)*time.Second && tick < time.Duration(p.seconds-1)*time.Second {
			r.TailDiscardMS += float64(b.lateDroppedSamples+b.droppedSamples-before) / 8
			if ready {
				r.TailFrames++
			}
		}
	}
	r.LateMS = float64(b.lateDroppedSamples) / 8
	r.BufferMS = float64(b.droppedSamples) / 8
	r.ReplacementMS = float64(b.missingFilledSamples.Load()) / 8
	r.Recoveries = b.recovery.recoveries
	if r.MaxQueueMS > 160 || b.eventCount > 64 || version != "0.12.21_timing_reference" && (r.TailDiscardMS > 0 || r.TailFrames < 440) {
		t.Fatalf("bounds or healthy tail violated: %+v", r)
	}
	return r
}
func TestProductionAudioScenarioMatrix(t *testing.T) {
	profiles := []productionAudioProfile{{"healthy", 60, 0}, {"long_drift_and_jitter", 1200, 250}, {"stable_shift", 60, 0}, {"stable_shift_small_jitter", 60, 0}, {"stable_shift_alternating_12ms", 60, 0}, {"second_shift_during_cooldown", 60, 0}, {"delivery_bursts_300_360_420", 60, 0}, {"age_budget_oscillation", 60, 0}, {"startup_socket_residence_440ms", 60, 0}, {"local_handler_stall_283ms", 60, 0}, {"consumer_pause_441ms", 60, 0}, {"loss_reorder_duplicates", 60, 0}, {"fourteen_minute_mixed_incident", 840, -250}}
	var results []productionAudioResult
	for _, codec := range []string{"G722", "PCMA", "PCMU"} {
		for _, p := range profiles {
			t.Run(codec+"/"+p.name, func(t *testing.T) {
				before := runProductionAudioProfile(t, p, codec, "0.12.21_timing_reference")
				off := runProductionAudioProfile(t, p, codec, "automatic_legacy_flag_false")
				on := runProductionAudioProfile(t, p, codec, "automatic_legacy_flag_true")
				results = append(results, before, off, on)
				t.Logf("before=%+v default=%+v resync=%+v", before, off, on)
				switch p.name {
				case "healthy", "long_drift_and_jitter":
					if off.LateMS != 0 || on.LateMS != 0 || off.BufferMS > 20 || on.BufferMS > 20 || on.Recoveries != 0 {
						t.Fatal("healthy bounded delivery was degraded")
					}
				case "stable_shift", "stable_shift_small_jitter", "stable_shift_alternating_12ms":
					if off.LateMS > 1600 || on.LateMS > 1600 || off.Recoveries != 1 || on.Recoveries != 1 {
						t.Fatal("stable live shift did not recover within bound")
					}
				case "second_shift_during_cooldown":
					if on.Recoveries != 2 || on.LateMS > 9000 {
						t.Fatal("cooldown caused indefinite rejection or repeated resets")
					}
				case "delivery_bursts_300_360_420":
					// Adaptive admission retains locally fresh burst packets; overflow
					// still discards excess backlog within the unchanged 160ms cap.
					if on.BufferMS == 0 || on.MaxQueueMS > 160 || on.Recoveries != 0 {
						t.Fatal("burst backlog escaped bounded storage")
					}
				case "startup_socket_residence_440ms", "local_handler_stall_283ms":
					if on.LateMS == 0 || on.Recoveries != 0 {
						t.Fatal("real stale audio admitted by resync")
					}
				case "consumer_pause_441ms":
					if on.BufferMS == 0 || on.Recoveries != 0 {
						t.Fatal("stalled consumer lost expiry protection")
					}
				case "loss_reorder_duplicates":
					if on.ReplacementMS == 0 || on.Recoveries != 0 {
						t.Fatal("network loss did not stay separate from phase recovery")
					}
				}
			})
		}
	}
	if path := os.Getenv("TELEPHONY_PRODUCTION_SCENARIO_RESULTS"); path != "" {
		data, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

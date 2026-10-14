package main

import (
	"encoding/json"
	"math"
	"os"
	"sort"
	"testing"
	"time"
)

type variableDeliveryResult struct {
	Codec, Condition                                                             string
	ClockPPM, JitterMaxMS, LateMS, ExpiredMS, OverflowMS, MaxAgeMS, EstimatedPPM float64
	Updates, RateRejected, PhaseWithheld                                         int
}

func TestSIPClockVariableDeliveryRegression(t *testing.T) {
	results := []variableDeliveryResult{}
	for _, codec := range []string{"PCMU", "PCMA", "G722"} {
		for _, condition := range []string{"normal", "drift_only", "jitter_only", "drift_and_jitter"} {
			t.Run(codec+"/"+condition, func(t *testing.T) {
				b := newRTPJitterBuffer()
				old := &v21SIPClockBuffer{rtpJitterBuffer: newRTPJitterBuffer()}
				m := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
				f := newRTPAudioFramer(b, m.offer)
				oldFramer := newRTPAudioFramer(old.rtpJitterBuffer, m.offer)
				oldMedia := &sipRTPMedia{offer: m.offer}
				enc := &sipRTPMedia{offer: m.offer}
				pcm := make([]int16, m.sampleRate()/50)
				for i := range pcm {
					pcm[i] = int16(5000 * math.Sin(2*math.Pi*180*float64(i)/float64(m.sampleRate())))
				}
				payload := enc.encodeAudio(pcm)
				ppm := 0.0
				if condition == "drift_only" || condition == "drift_and_jitter" {
					ppm = 250
				}
				jitter := condition == "jitter_only" || condition == "drift_and_jitter"
				packets := make([]sipReplayPacket, 60000)
				for i := range packets {
					delay := 0.0
					if jitter {
						delay = 60 + 60*math.Sin(2*math.Pi*float64(i)/1500)
					}
					at := time.Duration(float64(i)*float64(20*time.Millisecond)*(1+ppm/1e6) + delay*float64(time.Millisecond))
					packets[i] = sipReplayPacket{at: at, seq: uint16(i), ts: uint32(i * 160), payload: payload}
				}
				sort.SliceStable(packets, func(i, j int) bool { return packets[i].at < packets[j].at })
				start := time.Unix(100, 0)
				next := 0
				for tick := time.Duration(0); tick < 1202*time.Second; tick += 20 * time.Millisecond {
					for next < len(packets) && packets[next].at <= tick {
						p := packets[next]
						b.pushRTP(p.seq, p.payload, p.ts, start.Add(p.at))
						old.pushRTPAt(p.seq, p.payload, p.ts, start.Add(p.at), start.Add(p.at))
						next++
					}
					f.popPCMAt(m, start.Add(tick))
					oldFramer.popPCMAt(oldMedia, start.Add(tick))
				}
				r := variableDeliveryResult{Codec: codec, Condition: condition, ClockPPM: ppm, JitterMaxMS: 0, LateMS: float64(b.lateDroppedSamples) / 8, ExpiredMS: float64(b.expiredSamples) / 8, OverflowMS: float64(b.overflowSamples) / 8, MaxAgeMS: b.maxDeliveryAgeMS, EstimatedPPM: b.driftPPM, Updates: b.clock.updates, RateRejected: b.clock.rateRejected, PhaseWithheld: b.clock.phaseRejected}
				if jitter {
					r.JitterMaxMS = 120
				}
				before := r
				before.Condition = "before/" + condition
				before.LateMS = float64(old.lateDroppedSamples) / 8
				before.ExpiredMS = float64(old.expiredSamples) / 8
				before.OverflowMS = float64(old.overflowSamples) / 8
				results = append(results, before, r)
				if condition == "drift_and_jitter" && before.LateMS < 600000 {
					t.Fatalf("reference no longer reproduces released defect: %+v", before)
				}
				t.Logf("before late=%.0fms expired=%.2fms; after late=%.0fms expired=%.2fms", before.LateMS, before.ExpiredMS, r.LateMS, r.ExpiredMS)
				t.Logf("%+v", r)
				if condition != "drift_and_jitter" && r.LateMS != 0 {
					t.Errorf("control rejected bounded audio: %+v", r)
				}
				if r.LateMS != 0 || r.ExpiredMS+r.OverflowMS > 20 {
					t.Errorf("bounded healthy delivery falsely expired: %+v", r)
				}
			})
		}
	}
	if path := os.Getenv("TELEPHONY_VARIABLE_DELIVERY_RESULTS"); path != "" {
		data, _ := json.MarshalIndent(results, "", "  ")
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

type sipCapturedTiming struct {
	Packets      [][3]int64 `json:"packets"`
	ClockPPM     float64    `json:"clock_ppm"`
	InitialAgeMS float64    `json:"initial_relative_age_ms"`
}

func TestSIPAutomaticCapturedTiming(t *testing.T) {
	raw, err := os.ReadFile("testdata/sip/variable-arrival-20261022.json")
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != "2515d08a74712eeb1fe9bfc54db5a5c0317197c562d453b29bd76e83e17de76c" {
		t.Fatal("immutable captured timing fixture changed")
	}
	var trace sipCapturedTiming
	if err = json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	if len(trace.Packets) != 1069 {
		t.Fatal("captured timing changed")
	}
	for _, codec := range []string{"PCMA", "PCMU", "G722"} {
		t.Run(codec, func(t *testing.T) {
			base := time.Unix(100, 0)
			yes := true
			policy := sipPlaybackPolicy{}
			if raw := os.Getenv("TELEPHONY_SIP_CAPTURE_POLICY"); raw != "" {
				if e := json.Unmarshal([]byte(raw), &policy); e != nil {
					t.Fatal(e)
				}
				if e := policy.validate(); e != nil {
					t.Fatal(e)
				}
			}
			current := newRTPJitterBufferWithPolicy(policy)
			released := v28NewRTPJitterBufferWithPolicy(v28SipPlaybackPolicy{Resynchronize: &yes})
			current.sourceAnchorAt = base.Add(-time.Duration(trace.InitialAgeMS * float64(time.Millisecond)))
			current.haveSourceAnchor = true
			current.driftPPM = trace.ClockPPM
			released.sourceAnchorAt = current.sourceAnchorAt
			released.haveSourceAnchor = true
			released.driftPPM = trace.ClockPPM
			offer := sipMediaOffer{Codec: codec, PacketSamples: 160}
			cm, om, encoder := &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}
			cf, of := newRTPAudioFramer(current, offer), v28NewRTPAudioFramer(released, offer)
			decoder := &sipRTPMedia{offer: offer}
			var reference []int16
			var output, oldOutput []sipSpeechFrame
			var positions, oldPositions []int
			source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, cm.sampleRate())
			packets := append([][3]int64(nil), trace.Packets...)
			last := packets[len(packets)-1]
			// Synthetic stable continuation is kept separate from the immutable
			// capture; require recovery to remain healthy after its bursts stop.
			for i := int64(1); i <= 250; i++ {
				packets = append(packets, [3]int64{last[0] + i*int64(20*time.Millisecond), last[1] + i, last[2] + i*160})
			}
			next := 0
			tailDiscard := 0
			emitted, oldEmitted := 0, 0
			maxQueue := 0
			for tick := time.Duration(0); tick < 28*time.Second; tick += 20 * time.Millisecond {
				beforeDiscard := current.droppedSamples + current.lateDroppedSamples
				for next < len(packets) && time.Duration(packets[next][0]) <= tick {
					q := packets[next]
					pcm := make([]int16, cm.sampleRate()/50)
					start := int(q[2]) * cm.sampleRate() / 8000
					for i := range pcm {
						pcm[i] = source[(start+i)%len(source)]
					}
					payload := encoder.encodeAudio(pcm)
					decoded := decoder.decodeAudio(payload)
					for len(reference) < start+len(decoded) {
						reference = append(reference, 0)
					}
					copy(reference[start:], decoded)
					at := base.Add(time.Duration(q[0]))
					current.pushRTPAt(uint16(q[1]), payload, uint32(q[2]), at, at)
					released.pushRTPAt(uint16(q[1]), payload, uint32(q[2]), at, at)
					next++
				}
				before := current.driftAdjustedSamples.Load()
				if pcm, ready := cf.popPCMAt(cm, base.Add(tick)); ready {
					emitted++
					take := len(pcm) + int(current.driftAdjustedSamples.Load()-before)
					positions = append(positions, int(current.expectedTimestamp)*cm.sampleRate()/8000-len(cf.pendingPCM)-take)
					output = append(output, sipSpeechFrame{tick, append([]int16(nil), pcm...), take})
				}
				before = released.driftAdjustedSamples.Load()
				if pcm, ready := of.popPCMAt(om, base.Add(tick)); ready {
					oldEmitted++
					take := len(pcm) + int(released.driftAdjustedSamples.Load()-before)
					oldPositions = append(oldPositions, int(released.expectedTimestamp)*cm.sampleRate()/8000-len(of.pendingPCM)-take)
					oldOutput = append(oldOutput, sipSpeechFrame{tick, append([]int16(nil), pcm...), take})
				}
				if tick > 24*time.Second && tick < 26*time.Second {
					tailDiscard += current.droppedSamples + current.lateDroppedSamples - beforeDiscard
				}
				maxQueue = max(maxQueue, current.buffered+int(current.pendingPCMSamples.Load()))
			}
			speech := analyzeSIPSpeech(reference, cm.sampleRate(), output, positions, 40-trace.InitialAgeMS)
			oldSpeech := analyzeSIPSpeech(reference, cm.sampleRate(), oldOutput, oldPositions, 40-trace.InitialAgeMS)
			speech.Codec, oldSpeech.Codec = codec, codec
			speech.DiscardedMS = float64(current.droppedSamples+current.lateDroppedSamples) / 8
			oldSpeech.DiscardedMS = float64(released.droppedSamples+released.lateDroppedSamples) / 8
			// Evaluate the smooth-speech flag explicitly; severe trace acceptance
			// below is a separate bounded-damage contract, not a smoothness claim.
			speech.Accepted = (sipSpeechBudget{coverage: .99, gapMS: 80, delayMS: 100}).accepts(speech)
			oldSpeech.Accepted = (sipSpeechBudget{coverage: .99, gapMS: 80, delayMS: 100}).accepts(oldSpeech)
			if speech.ActiveSpeechCoverage < .86 || speech.LongestDamagedSpeechMS > 240 || speech.LongestPlaybackInterruptionMS > 240 || speech.MaxAddedDelayMS > 420 || speech.DiscardedMS > 1400 {
				t.Fatalf("captured trace exceeded explicit fault acceptance: %+v", speech)
			}
			t.Logf("speech candidate=%+v baseline=%+v", speech, oldSpeech)
			t.Logf("discard late=%.3f overflow=%.3f expired=%.3f lost=%d corrected=%d", float64(current.lateDroppedSamples)/8, float64(current.overflowSamples)/8, float64(current.expiredSamples)/8, current.lost, current.driftAdjustedSamples.Load())
			if speech.ActiveSpeechCoverage <= oldSpeech.ActiveSpeechCoverage || speech.LongestDamagedSpeechMS >= oldSpeech.LongestDamagedSpeechMS || speech.MaxAddedDelayMS > 550 {
				t.Fatal("captured speech continuity/delay did not improve within bound")
			}
			dropped := float64(current.droppedSamples+current.lateDroppedSamples) / 8
			oldDropped := float64(released.droppedSamples+released.lateDroppedSamples) / 8
			t.Logf("current discarded=%.3fms frames=%d recoveries=%d offset=%g reason=%s; released discarded=%.3fms frames=%d recoveries=%d; max_queue=%.3fms", dropped, emitted, current.recovery.recoveries, current.recovery.lastOffsetMS, current.recovery.reason, oldDropped, oldEmitted, released.recovery.recoveries, float64(maxQueue)/8)
			if released.recovery.recoveries != 0 || oldDropped < 1000 {
				t.Fatal("released failure not reproduced")
			}
			if current.recovery.recoveries == 0 || dropped >= oldDropped*.6 || emitted <= oldEmitted {
				t.Fatal("captured variable delivery did not materially recover")
			}
			if tailDiscard != 0 {
				t.Fatalf("persistent rejection after stable delivery: %.3f ms", float64(tailDiscard)/8)
			}
			if current.recovery.recoveryMS > 200 {
				t.Fatal("reserve did not resume promptly after phase decision")
			}
			if maxQueue > current.policy.CapacityMS*8 {
				t.Fatal("shared reserve bound exceeded")
			}
		})
	}
}

func TestSIPAutomaticLegacyFlagCannotDisableRecovery(t *testing.T) {
	for _, legacy := range []*bool{nil, new(bool), func() *bool { v := true; return &v }()} {
		t.Run(fmt.Sprint(legacy), func(t *testing.T) {
			b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: legacy})
			base := time.Unix(100, 0)
			for i := 0; i < 200; i++ {
				at := base.Add(time.Duration(i) * 20 * time.Millisecond)
				b.recovery.consider(b, uint16(i), uint32(i*160), at, at, 240*time.Millisecond, 160)
			}
			if b.recovery.recoveries != 1 {
				t.Fatal("automatic recovery depends on legacy setting", b.recovery.recoveries)
			}
		})
	}
}

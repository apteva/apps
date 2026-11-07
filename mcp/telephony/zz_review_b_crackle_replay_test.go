package main

// Replays staging-like AI delivery (real-time pace, upstream holds, jitter and
// faster-than-real-time bursts) through the real converter, adaptive tuning,
// packetizer, SIP admission and transmit shaping (shapeAgentPCM), then measures
// artefacts on the outgoing 8 kHz timeline as a receiver hears it.

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"testing"
	"time"
)

type crackleArrival struct {
	at         time.Duration
	start, end int // 24 kHz sample offsets
}

type crackleMetrics struct {
	packets, paddedPackets          int
	padMS, gapMS                    float64
	exhaustions, recoveries, starts int
	targetMS                        int
	clickEdges, loudRestarts        int
	restarts                        int
	firstMS, endLagMS               float64
	gapEvents                       int
	endFades                        int
	fillMS                          float64
}

func (m crackleMetrics) String() string {
	return fmt.Sprintf("packets=%d padded=%d pad_ms=%.1f gap_ms=%.0f exhaustions=%d recoveries=%d starts=%d target_ms=%d gap_events=%d click_edges=%d restarts=%d loud_restarts=%d first_ms=%.0f end_lag_ms=%.0f end_fades=%d fill_ms=%.1f",
		m.packets, m.paddedPackets, m.padMS, m.gapMS, m.exhaustions, m.recoveries, m.starts, m.targetMS, m.gapEvents, m.clickEdges, m.restarts, m.loudRestarts, m.firstMS, m.endLagMS, m.endFades, m.fillMS)
}

func crackleInput(t *testing.T) []int16 {
	var input []int16
	for _, f := range sipSpeechFixtures {
		input = append(input, loadSIPSpeech(t, f.id, f.hash, 24000)...)
	}
	return input
}

// Core splits provider deltas into 480-sample (20 ms) writes.
func crackleSplit(at time.Duration, start, end int) []crackleArrival {
	var out []crackleArrival
	for s := start; s < end; s += 480 {
		out = append(out, crackleArrival{at, s, min(end, s+480)})
	}
	return out
}

func crackleRealtime(input []int16, stalls [][2]time.Duration) []crackleArrival {
	var out []crackleArrival
	for start := 0; start < len(input); start += 480 {
		at := time.Duration(start) * time.Second / 24000
		for _, s := range stalls {
			if at >= s[0] && at < s[0]+s[1] {
				at = s[0] + s[1] // held upstream, then delivered as a burst
			}
		}
		out = append(out, crackleArrival{at, start, min(len(input), start+480)})
	}
	return out
}

func crackleJitter(input []int16) []crackleArrival {
	r := rand.New(rand.NewSource(7))
	var out []crackleArrival
	for start := 0; start < len(input); start += 480 {
		at := time.Duration(start)*time.Second/24000 + time.Duration(r.Intn(41))*time.Millisecond
		out = append(out, crackleArrival{at, start, min(len(input), start+480)})
	}
	return out
}

// Variable provider deltas, 1.25x faster than real time, and (optionally) an
// upstream hold of 80..380 ms every fourth delta: the staging pattern.
func crackleDeltas(input []int16, holds bool) []crackleArrival {
	sizes := []int{2412, 3612, 1210, 4818, 2890, 1730}
	held := []time.Duration{380, 240, 160, 140, 120, 80}
	var out []crackleArrival
	shift := time.Duration(0)
	for di, start := 0, 0; start < len(input); di++ {
		end := min(len(input), start+sizes[di%len(sizes)])
		if holds && di > 0 && di%4 == 0 {
			shift += held[(di/4)%len(held)] * time.Millisecond
		}
		nominal := time.Duration(start) * time.Second / 24000
		at := nominal*4/5 + shift
		if !holds {
			// Core's playback credit bounds how far ahead a burst can run.
			at = max(at, nominal-600*time.Millisecond)
		}
		out = append(out, crackleSplit(at, start, end)...)
		start = end
	}
	return out
}

// crackleDrift overrides drift compensation for a replay ("off"); "" keeps the default.
var crackleDrift string

func runCrackle(t *testing.T, input []int16, arrivals []crackleArrival, mode string, speed int, reserveMS ...int) crackleMetrics {
	t.Helper()
	reserve := 60
	if len(reserveMS) > 0 {
		reserve = reserveMS[0]
	}
	p := manualAgentPacer("PCMU", time.Duration(reserve)*time.Millisecond)
	p.agentSession.profile.Playout = agentPlayoutConfig{Mode: mode, MinReserveMS: reserve, MaxReserveMS: max(200, reserve), RecoveryIdleMS: 1000, StableMS: 5000, SpeedPercent: speed, MatchCorrelation: 0.97, Drift: crackleDrift}
	converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
	base := time.Unix(100, 0)
	var src, out []int16
	var m crackleMetrics
	next, started, inGap := 0, false, false
	var first, lastSpeech time.Duration
	end := arrivals[len(arrivals)-1].at + 3*time.Second
	var currentTick time.Duration
	if os.Getenv("TELEPHONY_PLAYOUT_DEBUG") != "" {
		agentPlayoutSpliceHook = func(frame, history, at, other, period, sign int, corr float64, voiced bool) {
			if currentTick < 1500*time.Millisecond {
				t.Logf("splice tick=%s frame=%d hist=%d at=%d other=%d d=%d sign=%d corr=%.2f voiced=%t", currentTick, frame, history, at, other, period, sign, corr, voiced)
			}
		}
		defer func() { agentPlayoutSpliceHook = nil }()
	}
	for tick := time.Duration(0); tick <= end; tick += 20 * time.Millisecond {
		currentTick = tick
		for next < len(arrivals) && arrivals[next].at <= tick {
			a := arrivals[next]
			pcm := converter.output(pcm16ToBytes(input[a.start:a.end]))
			src = append(src, pcm...)
			if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, audioEndMS: a.end / 24}}, base.Add(a.at)); err != nil {
				t.Fatal(err)
			}
			next++
		}
		if p.prepareAgentTick(base.Add(tick)) {
			if packet, ok := p.dequeueLivePacket(base.Add(tick)); ok {
				if len(packet.pcm) < 160 {
					m.paddedPackets++
				}
				frame := p.shapeAgentPCM(packet, base.Add(tick))
				p.playback.adjust(packet, -1)
				if !started {
					first = tick
				}
				m.packets++
				started = true
				lastSpeech = tick
				inGap = false
				out = append(out, frame...)
				continue
			}
		}
		if started && next < len(arrivals) {
			m.gapMS += 20 // nothing sent mid-reply; receiver must conceal
			if !inGap {
				m.gapEvents++
			}
			inGap = true
			out = append(out, make([]int16, 160)...)
		}
	}
	var sum float64
	for _, v := range src {
		sum += float64(v) * float64(v)
	}
	rms := math.Sqrt(sum / float64(len(src)))
	// Boundary level where real audio meets >= 1 ms of zeros (padding or a
	// gap) and where it resumes. A click is an edge above a quarter of the
	// speech RMS (about -12 dB re speech); a loud restart is above the RMS.
	zeroRun := func(i, dir int) bool {
		for k := 0; k < 8; k++ {
			j := i + dir*k
			if j < 0 || j >= len(out) || out[j] != 0 {
				return false
			}
		}
		return true
	}
	if at := os.Getenv("TELEPHONY_PLAYOUT_DUMP"); at != "" {
		var i int
		fmt.Sscan(at, &i)
		if i+10 <= len(out) {
			t.Logf("dump[%d]: %v", i, out[i-30:i+10])
		}
	}
	debugClick := func(kind string, i int) {
		if os.Getenv("TELEPHONY_PLAYOUT_DEBUG") != "" {
			t.Logf("%s at sample %d (packet %d, offset %d): %v | rms %.0f", kind, i, i/160, i%160, out[max(0, i-30):min(len(out), i+10)], rms)
		}
	}
	for i := 1; i < len(out); i++ {
		if out[i-1] != 0 && zeroRun(i, 1) && math.Abs(float64(out[i-1])) > rms/4 {
			m.clickEdges++
			debugClick("cut-to-zero", i)
		}
		if out[i] != 0 && zeroRun(i-1, -1) {
			m.restarts++
			level := math.Abs(float64(out[i]))
			if level > rms/4 {
				m.clickEdges++
				debugClick("restart", i)
			}
			if level > rms {
				m.loudRestarts++
			}
		}
	}
	st := p.agentPlayout
	if os.Getenv("TELEPHONY_PLAYOUT_DEBUG") != "" {
		d := st.tuning.cfg.driftPercent()
		t.Logf("tuning cfg mode=%s speed=%d drift=%d stats=%+v", st.tuning.cfg.Mode, st.tuning.cfg.SpeedPercent, d, st.tuning.stats)
	}
	m.padMS = float64(st.paddedSamples) / 8
	m.exhaustions, m.recoveries, m.starts, m.targetMS = st.exhaustions, st.recoveries, st.starts, st.tuning.stats.TargetMS
	m.endFades = st.endFades
	m.fillMS = float64(st.filledSamples) / 8
	m.firstMS = float64(first-arrivals[0].at) / float64(time.Millisecond)
	audio := time.Duration(len(input)) * time.Second / 24000
	m.endLagMS = float64(lastSpeech+20*time.Millisecond-arrivals[0].at-audio) / float64(time.Millisecond)
	if p.playback.hasPendingSpeech() {
		t.Fatal("pending speech leaked")
	}
	return m
}

// Staging deltas with 80..380 ms holds. Before (0.12.61): 15 exhaustions, 14
// immediate recoveries / gap events, 293 ms padding, 1580 ms gaps, 4/14
// restarts louder than speech RMS and 18 unfaded cuts to zero padding.
func TestReviewBCrackleReplayStagingStalls(t *testing.T) {
	input := crackleInput(t)
	arrivals := crackleDeltas(input, true)
	for _, mode := range []string{"fixed", "adaptive"} {
		m := runCrackle(t, input, arrivals, mode, 0)
		t.Logf("%-8s %s", mode, m)
		if m.loudRestarts != 0 || m.clickEdges != 0 {
			t.Fatalf("%s: underrun edges still click: %s", mode, m)
		}
		// The cushion trades a bounded extra wait at a recovery (<= 60 ms per
		// event, here +60 ms in total) for fewer separate underruns.
		if m.exhaustions > 11 || m.gapEvents > 10 || m.padMS > 233 || m.gapMS > 1580+60 {
			t.Fatalf("%s: recovery cushion did not reduce the underrun cascade: %s", mode, m)
		}
	}
}

// Real-time 20 ms writes with five upstream holds (the original harness).
func TestReviewBCrackleReplayRealtimeStalls(t *testing.T) {
	input := crackleInput(t)
	stalls := [][2]time.Duration{{2 * time.Second, 380 * time.Millisecond}, {5 * time.Second, 160 * time.Millisecond}, {7 * time.Second, 140 * time.Millisecond}, {9 * time.Second, 120 * time.Millisecond}, {11 * time.Second, 80 * time.Millisecond}}
	for _, mode := range []string{"fixed", "adaptive"} {
		clean := runCrackle(t, input, crackleRealtime(input, nil), mode, 0)
		stalled := runCrackle(t, input, crackleRealtime(input, stalls), mode, 0)
		t.Logf("%-8s clean   %s", mode, clean)
		t.Logf("%-8s stalled %s", mode, stalled)
		if clean.gapMS != 0 || clean.clickEdges != 0 || stalled.clickEdges != 0 {
			t.Fatalf("%s: clicks or gaps: clean=%s stalled=%s", mode, clean, stalled)
		}
	}
}

// The default decision table: fixed vs improved adaptive, speed_percent 0.
func TestReviewBPlayoutModeComparison(t *testing.T) {
	input := crackleInput(t)
	scenarios := []struct {
		name     string
		arrivals []crackleArrival
	}{
		{"no_stalls", crackleRealtime(input, nil)},
		{"jitter_0_40ms", crackleJitter(input)},
		{"staging_stalls", crackleDeltas(input, true)},
		{"bursty_1.25x", crackleDeltas(input, false)},
	}
	for _, s := range scenarios {
		fixed := runCrackle(t, input, s.arrivals, "fixed", 0)
		adaptive := runCrackle(t, input, s.arrivals, "adaptive", 0)
		t.Logf("%-15s fixed    %s", s.name, fixed)
		t.Logf("%-15s adaptive %s", s.name, adaptive)
		if adaptive.gapMS > fixed.gapMS || adaptive.clickEdges > fixed.clickEdges {
			t.Fatalf("%s: adaptive worse on gaps/clicks", s.name)
		}
		// Healthy delivery must not pay for the adaptive cushion.
		if (s.name == "no_stalls" || s.name == "bursty_1.25x") && adaptive.endLagMS > fixed.endLagMS {
			t.Fatalf("%s: adaptive added latency on healthy delivery: %s vs %s", s.name, adaptive, fixed)
		}
	}
}

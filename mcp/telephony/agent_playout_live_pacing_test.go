package main

import (
	"encoding/json"
	"math"
	"os"
	"slices"
	"testing"
	"time"
)

// Live OpenAI pacing captured on 2026-10-29: 100 ms deltas delivered at about
// real time, 0.965..0.994x over an 18 s reply, with occasional stalls. Real
// speech is replayed on that exact schedule through the real SIP agent path.
type livePacingFixture struct {
	Runs []struct {
		Name       string    `json:"name"`
		DeltaMS    int       `json:"delta_ms"`
		ArrivalsMS []float64 `json:"arrivals_ms"`
	} `json:"runs"`
}

func loadLivePacing(t *testing.T) livePacingFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/agent-audio/openai-live-pacing-20261029.json")
	if err != nil {
		t.Fatal(err)
	}
	var f livePacingFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// livePacingArrivals maps each captured delta onto the next 100 ms of speech,
// split into Core's 20 ms writes.
func livePacingArrivals(input []int16, arrivalsMS []float64, deltaMS int) ([]int16, []crackleArrival) {
	per := deltaMS * 24
	need := len(arrivalsMS) * per
	speech := make([]int16, 0, need)
	for len(speech) < need {
		speech = append(speech, input[:min(len(input), need-len(speech))]...)
	}
	// A real reply ends in silence; do not end the replay mid-word at full level.
	fade := 240
	for i := 0; i < fade; i++ {
		j := len(speech) - fade + i
		speech[j] = int16(int(speech[j]) * (fade - 1 - i) / fade)
	}
	var out []crackleArrival
	for i, ms := range arrivalsMS {
		at := time.Duration(ms * float64(time.Millisecond))
		out = append(out, crackleSplit(at, i*per, (i+1)*per)...)
	}
	return speech, out
}

// Drift compensation must remove most underruns on real provider pacing
// without adding clicks or loud restarts, and without delaying playback start.
func TestAgentPlayoutLivePacingReplay(t *testing.T) {
	input := crackleInput(t)
	fixture := loadLivePacing(t)
	type total struct{ gaps, gapMS, clicks, loud, starts int }
	measure := func(drift string) total {
		crackleDrift = drift
		defer func() { crackleDrift = "" }()
		var sum total
		for _, run := range fixture.Runs {
			speech, arrivals := livePacingArrivals(input, run.ArrivalsMS, run.DeltaMS)
			m := runCrackle(t, speech, arrivals, "fixed", 0)
			t.Logf("%s drift=%q: %s", run.Name, drift, m)
			if m.firstMS > 60 {
				t.Errorf("%s: playback started after %.0f ms", run.Name, m.firstMS)
			}
			sum.gaps += m.gapEvents
			sum.gapMS += int(m.gapMS)
			sum.clicks += m.clickEdges
			sum.loud += m.loudRestarts
		}
		return sum
	}
	before, after := measure("off"), measure("")
	t.Logf("drift off: %+v | default drift: %+v", before, after)
	if after.clicks != 0 || after.loud != 0 {
		t.Fatalf("drift compensation added edges: %+v", after)
	}
	if after.gaps*3 > before.gaps || after.gapMS*3 > before.gapMS {
		t.Fatalf("drift compensation did not remove most gaps: before %+v after %+v", before, after)
	}
}

// dominantPeriod returns the lag (2.5..15 ms) with the strongest normalized
// autocorrelation: the pitch period of voiced speech.
func dominantPeriod(x []int16, rate int) int {
	lag, _ := dominantPeriodStrength(x, rate)
	return lag
}

func dominantPeriodStrength(x []int16, rate int) (int, float64) {
	best, lag := -1.0, 0
	for d := rate / 400; d <= rate*15/1000 && d < len(x)/2; d++ {
		var ab, aa, bb float64
		for i := 0; i+d < len(x); i++ {
			u, v := float64(x[i]), float64(x[i+d])
			ab, aa, bb = ab+u*v, aa+u*u, bb+v*v
		}
		if aa > 0 && bb > 0 && ab/math.Sqrt(aa*bb) > best {
			best, lag = ab/math.Sqrt(aa*bb), d
		}
	}
	return lag, best
}

// Every splice on real speech keeps the frame's edges, changes its length by
// exactly the reported period, adds no sharper step than the source has, and
// keeps the pitch of voiced audio.
func TestAgentPlayoutStretchQualityOnRealSpeech(t *testing.T) {
	input := crackleInput(t)
	for _, rate := range []int{8000, 16000} {
		converted := newPCMResampler(24000, rate).Process(input)
		frame := rate / 50
		tuning := agentPlayoutTuning{cfg: agentPlayoutConfig{MatchCorrelation: .97}}
		splices, voicedChecked, pitchChanged := 0, 0, 0
		for start := 0; start+frame <= len(converted); start += frame {
			x := converted[start : start+frame]
			for _, debt := range []int{rate * 12 / 1000, -rate * 6 / 1000} {
				probe := tuning
				out, delta := probe.stretchSpeech(x, rate, debt)
				if delta == 0 {
					continue
				}
				splices++
				if len(out) != len(x)+delta || out[0] != x[0] || out[len(out)-1] != x[len(x)-1] {
					t.Fatalf("rate %d frame %d: edges or length changed (len %d->%d, delta %d)", rate, start/frame, len(x), len(out), delta)
				}
				// Compare against the source including the history the splice may use.
				context := append(append([]int16(nil), tuning.history[:tuning.historyLen]...), x...)
				maxStep := 0.0
				for i := 1; i < len(context); i++ {
					maxStep = math.Max(maxStep, math.Abs(float64(context[i])-float64(context[i-1])))
				}
				for i := 1; i < len(out); i++ {
					if step := math.Abs(float64(out[i]) - float64(out[i-1])); step > maxStep*1.1+64 {
						t.Fatalf("rate %d frame %d: splice step %.0f exceeds source %.0f", rate, start/frame, step, maxStep)
					}
				}
				var energy float64
				for _, v := range x {
					energy += float64(v) * float64(v)
				}
				// Pitch is only defined for clearly periodic (voiced) audio.
				if _, strength := dominantPeriodStrength(context, rate); delta > 0 && energy/float64(len(x)) > 1500*1500 && strength >= .8 {
					voicedChecked++
					// Same history on both sides; octave-related periods are one pitch.
					stretched := append(append([]int16(nil), tuning.history[:tuning.historyLen]...), out...)
					before, after := dominantPeriod(context, rate), dominantPeriod(stretched, rate)
					same := false
					for _, k := range []float64{1, 2, 3, .5, 1. / 3} {
						if math.Abs(float64(after)-float64(before)*k) <= float64(before)*k*.06+1 {
							same = true
						}
					}
					if before > 0 && !same {
						pitchChanged++
						t.Logf("rate %d frame %d: pitch period %d -> %d", rate, start/frame, before, after)
					}
				}
			}
			tuning.remember(x, rate)
		}
		if splices < 50 || voicedChecked < 20 {
			t.Fatalf("rate %d: too few splices to judge (%d, voiced %d)", rate, splices, voicedChecked)
		}
		t.Logf("rate %d: %d splices checked, %d voiced pitch checks, %d pitch changes", rate, splices, voicedChecked, pitchChanged)
		if pitchChanged*20 > voicedChecked {
			t.Fatalf("rate %d: pitch changed on %d of %d voiced splices", rate, pitchChanged, voicedChecked)
		}
	}
}

// Drift compensation never touches healthy delivery: real-time speech in
// 20 ms or 100 ms writes is transmitted sample for sample.
func TestAgentPlayoutDriftLeavesHealthyDeliveryUnchanged(t *testing.T) {
	input := crackleInput(t)[:24000*8]
	for _, chunk := range []int{480, 2400} {
		for _, codec := range []string{"PCMU", "G722"} {
			p := manualAgentPacer(codec, 60*time.Millisecond)
			converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
			base := time.Unix(100, 0)
			var expected, actual []int16
			next := 0
			for tick := time.Duration(0); tick <= time.Duration(len(input))*time.Second/24000+time.Second; tick += 20 * time.Millisecond {
				for ; next < len(input) && time.Duration(next)*time.Second/24000 <= tick; next += chunk {
					for start := next; start < min(len(input), next+chunk); start += 480 {
						stop := min(start+480, len(input))
						pcm := converter.output(pcm16ToBytes(input[start:stop]))
						expected = append(expected, pcm...)
						if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, audioEndMS: stop / 24}}, base.Add(time.Duration(next)*time.Second/24000)); err != nil {
							t.Fatal(err)
						}
					}
				}
				if !p.prepareAgentTick(base.Add(tick)) {
					continue
				}
				if packet, ok := p.dequeueLivePacket(base.Add(tick)); ok {
					actual = append(actual, packet.pcm...)
					p.playback.adjust(packet, -1)
				}
			}
			if !slices.Equal(actual, expected) {
				t.Fatalf("%s chunk %d: healthy speech was modified (%d -> %d samples, stats %+v)", codec, chunk, len(expected), len(actual), p.agentPlayout.tuning.stats)
			}
		}
	}
}

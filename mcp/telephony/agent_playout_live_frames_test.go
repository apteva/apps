package main

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"testing"
	"time"
)

// Ten live OpenAI gpt-live-1 reply streams captured on 2026-10-29: each Core
// frame's arrival time at Telephony and whether Core marked it silence. The
// provider streams at real time, never catches up after a stall, and sends
// 49-93% explicit silence. Real speech replaces the speech frames.
type liveFramesFixture struct {
	Runs []struct {
		Name   string       `json:"name"`
		Frames [][2]float64 `json:"frames"` // [arrival ms, silence 0/1]
	} `json:"runs"`
}

type liveFramesResult struct {
	speechMS, holeMS, silenceAddedMS, gapMS, paddedMS, filledMS, expandedMS float64
	holes, gaps, clicks, loudRestarts                                       int
	firstMS, maxQueueMS, meanQueueMS, maxTroughMS                           float64
}

func (r liveFramesResult) String() string {
	return fmt.Sprintf("speech=%.1fs holes=%d/%.0fms gaps=%d/%.0fms clicks=%d loud=%d first=%.0f trough max=%.0f queue max=%.0f mean=%.0f silence+=%.0f stretch+=%.0f pad=%.0f fill=%.0f",
		r.speechMS/1000, r.holes, r.holeMS, r.gaps, r.gapMS, r.clicks, r.loudRestarts, r.firstMS, r.maxTroughMS, r.maxQueueMS, r.meanQueueMS, r.silenceAddedMS, r.expandedMS, r.paddedMS, r.filledMS)
}

func loadLiveFrames(t *testing.T) liveFramesFixture {
	t.Helper()
	raw, err := os.ReadFile("testdata/agent-audio/openai-live-frames-20261029.json")
	if err != nil {
		t.Fatal(err)
	}
	var f liveFramesFixture
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Sample kinds on the transmitted timeline.
const (
	kindSilence = iota
	kindSpeech
	kindInserted // nothing to send mid-stream, or zero padding in a speech packet
)

// replayLiveFrames sends one captured stream through the real SIP agent path
// (converter, tuning, packetizer, admission, transmit shaping) on a simulated
// 20 ms clock and classifies every transmitted sample.
func replayLiveFrames(t *testing.T, frames [][2]float64, input []int16, configure func(*sipRTPPacer)) liveFramesResult {
	t.Helper()
	p := manualAgentPacer("PCMU", 60*time.Millisecond)
	if configure != nil {
		configure(p)
	}
	converter := newAgentMediaConverter(p.agentSession, p.media.sampleRate(), false)
	base := time.Unix(100, 0)
	var out []int16
	var kind []int
	var res liveFramesResult
	next, speechAt, started, silenceIn := 0, 0, false, 0
	var queueSum float64
	var queueTicks int
	end := time.Duration(frames[len(frames)-1][0]*float64(time.Millisecond)) + 2*time.Second
	for tick := time.Duration(0); tick <= end; tick += 20 * time.Millisecond {
		// Frames within 5 ms are one provider delivery (as in tuning); a
		// delivery straddling a tick is measured once, before its first frame.
		if started && next < len(frames) && time.Duration(frames[next][0]*float64(time.Millisecond)) <= tick && (next == 0 || frames[next][0]-frames[next-1][0] > 5) {
			// What playback had left when the next delivery arrived: the delay
			// the buffer adds, excluding the delivery's own length.
			res.maxTroughMS = math.Max(res.maxTroughMS, float64(len(p.queue)*20)+float64(p.packetizer.queuedSamples)/8)
		}
		for next < len(frames) && time.Duration(frames[next][0]*float64(time.Millisecond)) <= tick {
			silent := frames[next][1] == 1
			src := make([]int16, 480)
			if !silent {
				for i := range src {
					src[i] = input[(speechAt+i)%len(input)]
				}
				speechAt += 480
			}
			pcm := converter.output(pcm16ToBytes(src))
			if silent {
				silenceIn += len(pcm)
			}
			at := base.Add(time.Duration(frames[next][0] * float64(time.Millisecond)))
			if _, err := p.enqueueAt([]sipRTPOutboundPacket{{pcm: pcm, itemID: "reply", generation: 1, silence: silent, audioEndMS: (next + 1) * 20}}, at); err != nil {
				t.Fatal(err)
			}
			next++
		}
		now := base.Add(tick)
		if started {
			q := float64(len(p.queue)*20) + float64(p.packetizer.queuedSamples)/8
			queueSum += q
			queueTicks++
			res.maxQueueMS = math.Max(res.maxQueueMS, q)
		}
		if p.prepareAgentTick(now) {
			if packet, ok := p.dequeueLivePacket(now); ok {
				real := len(packet.pcm)
				filledBefore := p.agentPlayout.filledSamples
				frame := p.shapeAgentPCM(packet, now)
				p.playback.adjust(packet, -1)
				if p.agentPlayout.filledSamples > filledBefore {
					real += p.agentPlayout.filledSamples - filledBefore
				}
				if !started {
					res.firstMS = float64(tick-time.Duration(frames[0][0]*float64(time.Millisecond))) / float64(time.Millisecond)
				}
				started = true
				for i, v := range frame {
					out = append(out, v)
					switch {
					case packet.silence:
						kind = append(kind, kindSilence)
					case i < real:
						kind = append(kind, kindSpeech)
					default:
						kind = append(kind, kindInserted)
					}
				}
				continue
			}
		}
		if started && next < len(frames) {
			for i := 0; i < 160; i++ {
				out = append(out, 0)
				kind = append(kind, kindInserted)
			}
		}
	}
	var sum float64
	n := 0
	for i, v := range out {
		if kind[i] == kindSpeech {
			sum += float64(v) * float64(v)
			n++
		}
	}
	rms := math.Sqrt(sum / math.Max(1, float64(n)))
	res.speechMS = float64(n) / 8
	// An audible hole is inserted audio with real speech on both sides (silence
	// in between makes it a longer pause, not a hole). Edges must be faded.
	for i := 0; i < len(kind); {
		if kind[i] != kindInserted {
			i++
			continue
		}
		j := i
		for j < len(kind) && kind[j] == kindInserted {
			j++
		}
		before, after := i-1, j
		if before >= 0 && after < len(kind) {
			// Any mid-stream underrun: a longer pause at best.
			res.gaps++
			res.gapMS += float64(j-i) / 8
		}
		if before >= 0 && after < len(kind) && kind[before] == kindSpeech && kind[after] == kindSpeech && j-i >= 16 {
			res.holes++
			res.holeMS += float64(j-i) / 8
		}
		if before >= 0 && kind[before] == kindSpeech && math.Abs(float64(out[before])) > rms/4 {
			res.clicks++
		}
		if after < len(kind) && kind[after] == kindSpeech {
			if level := math.Abs(float64(out[after])); level > rms/4 {
				res.clicks++
				if level > rms {
					res.loudRestarts++
				}
			}
		}
		i = j
	}
	if queueTicks > 0 {
		res.meanQueueMS = queueSum / float64(queueTicks)
	}
	silenceOut := 0
	for _, k := range kind {
		if k == kindSilence {
			silenceOut++
		}
	}
	res.silenceAddedMS = float64(silenceOut-silenceIn) / 8
	d := p.agentPlayoutSnapshot()
	res.paddedMS, res.filledMS = d.PaddingMS, d.FilledMS
	res.expandedMS = p.agentPlayout.tuning.stats.ExpandedMS
	return res
}

// Acceptance on all ten captured replies: no audible hole inside speech, no
// click or loud restart at any underrun edge, no zero padding, a 60 ms start,
// and the delay before each delivery never above 200 ms. Underruns that only
// lengthen a pause are reported. Without drift compensation (0.14.1) the
// same streams are logged for comparison.
func TestAgentPlayoutLiveFramesReplay(t *testing.T) {
	input := crackleInput(t)
	fixture := loadLiveFrames(t)
	var total, reference liveFramesResult
	for _, run := range fixture.Runs {
		off := replayLiveFrames(t, run.Frames, input, func(p *sipRTPPacer) { p.agentSession.profile.Playout.Drift = "off" })
		reference.holes += off.holes
		reference.holeMS += off.holeMS
		reference.gaps += off.gaps
		reference.gapMS += off.gapMS
		reference.paddedMS += off.paddedMS
		r := replayLiveFrames(t, run.Frames, input, nil)
		t.Logf("%-8s %s", run.Name, r)
		if r.holes > 0 || r.clicks > 0 || r.loudRestarts > 0 || r.paddedMS > 0 || r.firstMS != 60 || r.maxTroughMS > 200 {
			t.Errorf("%s: %s", run.Name, r)
		}
		total.holes += r.holes
		total.holeMS += r.holeMS
		total.clicks += r.clicks
		total.loudRestarts += r.loudRestarts
		total.speechMS += r.speechMS
		total.gaps += r.gaps
		total.gapMS += r.gapMS
		total.paddedMS += r.paddedMS
		total.maxQueueMS = math.Max(total.maxQueueMS, r.maxQueueMS)
		total.firstMS = math.Max(total.firstMS, r.firstMS)
		total.maxTroughMS = math.Max(total.maxTroughMS, r.maxTroughMS)
	}
	t.Logf("TOTAL holes=%d/%.0fms gaps=%d/%.0fms clicks=%d loud=%d pad=%.0fms speech=%.1fs worst_first=%.0f worst_trough=%.0f worst_queue=%.0f", total.holes, total.holeMS, total.gaps, total.gapMS, total.clicks, total.loudRestarts, total.paddedMS, total.speechMS/1000, total.firstMS, total.maxTroughMS, total.maxQueueMS)
	t.Logf("drift off (0.14.1) holes=%d/%.0fms gaps=%d/%.0fms pad=%.0fms", reference.holes, reference.holeMS, reference.gaps, reference.gapMS, reference.paddedMS)
}

func TestAlignAgentSilence(t *testing.T) {
	for _, c := range []struct {
		pending, silence      int
		pendingSilence        bool
		wantHead, wantSilence int
	}{
		{0, 160, false, 0, 160},
		{0, 320, false, 0, 320},
		{100, 160, false, 60, 160}, // 100 left: 60 complete speech, 100 rounds to 160
		{150, 160, false, 10, 160}, // 150 rounds to 160
		{40, 160, false, 120, 160}, // 40 rounds up: a pause is never lost entirely
		{0, 200, false, 0, 160},    // 200 rounds to 160
		{0, 250, false, 0, 320},    // 250 rounds to 320
		{100, 160, true, 0, 220},   // pending silence: 260 rounds to 320
		{0, 0, false, 0, 0},
	} {
		p := newCarrierAudioPacketizer(8000)
		if c.pending > 0 {
			p.add(make([]int16, c.pending), 0, realtimeBridgeControl{ItemID: "i", Silence: c.pendingSilence})
		}
		head, rest := alignAgentSilence(p, make([]int16, c.silence))
		if len(head) != c.wantHead || len(rest) != c.wantSilence {
			t.Errorf("%+v: head=%d silence=%d", c, len(head), len(rest))
		}
		if (c.pending+len(head)+len(rest))%160 != 0 && c.silence > 0 {
			t.Errorf("%+v: not packet aligned", c)
		}
	}
}

// A pause keeps the input history and the stream: concealment and stretching
// right after it look back across the pause, and speech after a pause is not
// treated as a new stream's startup.
func TestAgentPlayoutSilenceKeepsHistory(t *testing.T) {
	tuning := newAgentPlayoutTuning(continuousAgentFixture(), 60)
	tuning.driftAllowed = true
	now := time.Unix(100, 0)
	tone := make([]int16, 160)
	for i := range tone {
		tone[i] = int16(3000 * math.Sin(2*math.Pi*float64(i)/40))
	}
	tuning.process(tone, 8000, 60, realtimeBridgeControl{ItemID: "a", Generation: 1}, now)
	tuning.process(make([]int16, 160), 8000, 60, realtimeBridgeControl{ItemID: "a", Generation: 1, Silence: true}, now.Add(20*time.Millisecond))
	if tuning.historyLen != 240 || tuning.streamAt.IsZero() || pcmAllZero(tuning.history[:80]) || !pcmAllZero(tuning.history[80:240]) {
		t.Fatalf("history=%d stream=%v: silence reset the stream", tuning.historyLen, tuning.streamAt)
	}
}

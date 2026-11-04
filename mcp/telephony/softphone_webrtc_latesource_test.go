package main

import (
	"fmt"
	"math"
	"math/rand"
	"os"
	"sort"
	"testing"
	"time"
)

// ─── Frozen v0.17.1 sender ─────────────────────────────────────────
//
// Only a frame below rtcSilencePeak could be dropped for free, so on a line with
// background noise the delay of a late source stayed for the rest of the stream.

func review171TrimBacklog(f *rtcPCMFramer, frames <-chan rtcPCMFrame, frame rtcPCMFrame, now time.Time, stats *rtcMediaStats) rtcPCMFrame {
	if frame.pcm == nil || len(frames) <= rtcPlaybackTargetFrames {
		f.backlogTicks, f.overBudgetTicks = 0, 0
		return frame
	}
	f.backlogTicks++
	if rtcPCMSilent(frame.pcm) {
		if f.backlogTicks < rtcBacklogSilenceTicks {
			return frame
		}
		next, _ := nextRTCFrame(frames, now, stats)
		if next.pcm == nil {
			return frame
		}
		stats.drop("webrtc_playback_backlog_silence", stats.mediaDirection(), 0, len(frame.pcm)*1000/24000)
		return next
	}
	if f.frameAge(frame, now) < rtcPlaybackSpeechBudget {
		f.overBudgetTicks = 0
		return frame
	}
	if f.overBudgetTicks++; f.overBudgetTicks < rtcBacklogSpeechEvery {
		return frame
	}
	next, _ := nextRTCFrame(frames, now, stats)
	if next.pcm == nil {
		return frame
	}
	stats.drop("webrtc_playback_backlog_speech", stats.mediaDirection(), 0, len(frame.pcm)*1000/24000)
	f.overBudgetTicks = 0
	return rtcPCMFrame{pcm: rtcCrossfadeFrames(frame.pcm, next.pcm), expires: frame.expires}
}

func review171RTCPacingSender(start time.Time) rtcPacingSender {
	clock := rtcRTPClock{base: start}
	silence := make([]int16, 480)
	return func(now time.Time, frames <-chan rtcPCMFrame, pending *rtcPCMFramer, _ time.Time, stats *rtcMediaStats, record rtcPacingRecord) {
		slots, fill, _ := clock.plan(now, len(frames))
		for range slots {
			frame, _ := nextRTCFrame(frames, now, stats)
			frame = review171TrimBacklog(pending, frames, frame, now, stats)
			if frame.pcm == nil {
				if !fill {
					break
				}
				frame = rtcPCMFrame{pcm: silence, expires: now.Add(liveAudioMaxAge)}
			}
			record(frame, clock.take(now), now)
		}
	}
}

// ─── Real audio through the real framer, queue and tick ────────────
//
// A frame's delay is the age of its oldest sample against its source time, so
// the lateness of the source counts in full.

type rtcArrivalScenario struct {
	pcm []int16
	// Arrival of each 20 ms source frame, from the start of the stream.
	arrival []time.Duration
	// From this time on, delay should be back at the target.
	eventEnd time.Duration
}

type rtcArrivalResult struct {
	PeakDelay, MeanDelay, SettledDelay time.Duration
	Settle                             time.Duration // after eventEnd, until delay stays at target
	SpeechTrimMS, PauseTrimMS          int
	OverflowMS, FillFrames             int
	Dropped                            []int // source frames that never reached the wire
}

func onTimeArrivals(frames int) []time.Duration {
	out := make([]time.Duration, frames)
	for i := range out {
		out[i] = time.Duration(i) * 20 * time.Millisecond
	}
	return out
}

// The source holds its audio for `late` from `at`, then delivers everything.
func lateArrivals(frames int, late time.Duration, at ...time.Duration) []time.Duration {
	out := onTimeArrivals(frames)
	for _, e := range at {
		for i, a := range out {
			if a >= e && a < e+late {
				out[i] = e + late
			}
		}
	}
	return out
}

func runRTCArrivals(sender func(time.Time) rtcPacingSender, s rtcArrivalScenario) rtcArrivalResult {
	start := time.Unix(1700000000, 0)
	frames := make(chan rtcPCMFrame, 6)
	stats := &rtcMediaStats{}
	done := make(chan struct{})
	pending := rtcPCMFramer{maxAge: liveAudioMaxAge}
	send := sender(start)
	var last time.Time
	var r rtcArrivalResult
	type sample struct{ at, delay time.Duration }
	var played []sample
	// A crossfade changes the start of a frame; its end still identifies it.
	key := func(pcm []int16) string { return string(pcm16ToBytes(pcm[rtcTrimCrossfade:])) }
	// Frames can repeat exactly, as in a tone: each play accounts for the next one.
	source := make(map[string][]int, len(s.arrival))
	for i := range s.arrival {
		k := key(s.pcm[i*480 : (i+1)*480])
		source[k] = append(source[k], i)
	}
	seen := make([]bool, len(s.arrival))
	sourceEnd := s.arrival[len(s.arrival)-1]
	record := func(frame rtcPCMFrame, _ uint32, now time.Time) {
		k := key(frame.pcm)
		if same := source[k]; len(same) > 0 {
			seen[same[0]] = true
			source[k] = same[1:]
		}
		at := now.Sub(start)
		if allZero(frame.pcm) {
			if len(played) > 0 && at < sourceEnd {
				r.FillFrames++
			}
			return
		}
		if at <= sourceEnd {
			played = append(played, sample{at, pending.frameAge(frame, now)})
		}
	}
	number := 0
	for tms := time.Duration(0); tms <= sourceEnd+time.Second; tms += time.Millisecond {
		now := start.Add(tms)
		for number < len(s.arrival) && s.arrival[number] <= tms {
			at := start.Add(time.Duration(number) * 20 * time.Millisecond)
			last = now
			pending.push(pcm16ToBytes(s.pcm[number*480:(number+1)*480]), at.Add(liveAudioMaxAge), func(f rtcPCMFrame) bool {
				return queueRTCFrame(frames, f, done, stats)
			})
			number++
		}
		if tms%(20*time.Millisecond) == 5*time.Millisecond {
			send(now, frames, &pending, last, stats, record)
		}
	}
	for _, e := range stats.snapshot().Events {
		switch e.Reason {
		case "webrtc_playback_backlog_speech":
			r.SpeechTrimMS += e.DurationMS
		case "webrtc_playback_backlog_silence":
			r.PauseTrimMS += e.DurationMS
		case "webrtc_playback_overflow":
			r.OverflowMS += e.DurationMS
		}
	}
	for i, ok := range seen {
		if !ok && !allZero(s.pcm[i*480:(i+1)*480]) {
			r.Dropped = append(r.Dropped, i)
		}
	}
	var sum float64
	settledAt := time.Duration(-1)
	for _, p := range played {
		sum += float64(p.delay)
		r.PeakDelay = max(r.PeakDelay, p.delay)
		if p.at >= s.eventEnd && p.delay > 25*time.Millisecond {
			settledAt = p.at
		}
	}
	if len(played) > 0 {
		r.MeanDelay = time.Duration(sum / float64(len(played)))
		for _, p := range played[max(0, len(played)-25):] {
			r.SettledDelay = max(r.SettledDelay, p.delay)
		}
	}
	if settledAt >= 0 {
		r.Settle = settledAt - s.eventEnd
	}
	return r
}

func allZero(pcm []int16) bool {
	for _, v := range pcm {
		if v != 0 {
			return false
		}
	}
	return true
}

// Real speech, repeated, at 24 kHz.
func rtcCleanSpeech(t *testing.T, seconds int) []int16 {
	t.Helper()
	var out []int16
	for len(out) < seconds*24000 {
		for _, fixture := range sipSpeechFixtures {
			out = append(out, loadSIPSpeech(t, fixture.id, fixture.hash, 24000)...)
		}
	}
	return append([]int16(nil), out[:seconds*24000]...)
}

// Stationary noise at the given level; gain, if set, modulates it over time.
func rtcWithNoise(clean []int16, dBFS float64, seed int64, gain func(sample int) float64) []int16 {
	out := append([]int16(nil), clean...)
	if math.IsInf(dBFS, -1) {
		return out
	}
	random := rand.New(rand.NewSource(seed))
	sigma := 32768 * math.Pow(10, dBFS/20)
	for i := range out {
		g := 1.0
		if gain != nil {
			g = gain(i)
		}
		out[i] = int16(max(-32768, min(32767, float64(out[i])+random.NormFloat64()*sigma*g)))
	}
	return out
}

func rtcFrameRMS(pcm []int16) float64 {
	var e float64
	for _, v := range pcm {
		e += float64(v) * float64(v)
	}
	return math.Sqrt(e / float64(len(pcm)))
}

func rtcDBFS(rms float64) float64 { return 20 * math.Log10(max(rms, .5)/32768) }

// Level of the loud part of the speech, and of the loudest speech frame removed.
func rtcRemovedSpeech(clean []int16, dropped []int) (speechDB, removedDB float64) {
	var levels []float64
	for i := 0; i+480 <= len(clean); i += 480 {
		levels = append(levels, rtcFrameRMS(clean[i:i+480]))
	}
	sort.Float64s(levels)
	speechDB = rtcDBFS(levels[len(levels)*9/10])
	removedDB = math.Inf(-1)
	for _, i := range dropped {
		removedDB = max(removedDB, rtcDBFS(rtcFrameRMS(clean[i*480:(i+1)*480])))
	}
	return speechDB, removedDB
}

// Sustained music-like audio at a normal level.
func rtcMusicLike(seconds int) []int16 {
	out := make([]int16, seconds*24000)
	chords := [][]float64{{220, 277.18, 329.63}, {196, 246.94, 293.66}, {174.61, 220, 261.63}, {164.81, 207.65, 246.94}}
	for i := range out {
		c := chords[i/12000%len(chords)]
		x := float64(i) / 24000
		v := 0.0
		for k, f := range c {
			v += math.Sin(2*math.Pi*f*x) + .5*math.Sin(2*math.Pi*f*2.01*x+float64(k))
		}
		out[i] = int16(v * 2200)
	}
	return out
}

func logRTCArrivals(t *testing.T, label string, r rtcArrivalResult) {
	t.Helper()
	t.Logf("%-40s peak %3d mean %3d end %3d settle %5d | pause %3d speech %3d overflow %3d fill %2d", label, r.PeakDelay.Milliseconds(), r.MeanDelay.Milliseconds(), r.SettledDelay.Milliseconds(), r.Settle.Milliseconds(), r.PauseTrimMS, r.SpeechTrimMS, r.OverflowMS, r.FillFrames)
}

// A pause on a noisy line clears the delay of a late source, and what is removed
// is the noise of that pause, not speech.
func TestRTCLateSourceDelayClearsAtPauseOnNoisyLine(t *testing.T) {
	const seconds = 20
	frames := seconds * 50
	clean := rtcCleanSpeech(t, seconds)
	for _, noise := range []float64{-60, -50, -40, -35} {
		pcm := rtcWithNoise(clean, noise, 1, nil)
		for _, late := range []time.Duration{40, 60} {
			late *= time.Millisecond
			t.Run(fmt.Sprintf("noise%.0f/late%d", noise, late.Milliseconds()), func(t *testing.T) {
				s := rtcArrivalScenario{pcm: pcm, arrival: lateArrivals(frames, late, 2*time.Second), eventEnd: 2*time.Second + late}
				old := runRTCArrivals(review171RTCPacingSender, s)
				fixed := runRTCArrivals(currentRTCPacingSender, s)
				logRTCArrivals(t, "v0.17.1", old)
				logRTCArrivals(t, "fixed", fixed)
				if old.PauseTrimMS != 0 || old.SettledDelay <= 25*time.Millisecond || old.Settle < 15*time.Second {
					t.Fatal("did not reproduce delay lasting to the end of the stream", old)
				}
				if fixed.SettledDelay > 25*time.Millisecond || fixed.Settle > 3500*time.Millisecond {
					t.Fatal("delay did not clear at the first pause", fixed)
				}
				if fixed.SpeechTrimMS != 0 || fixed.OverflowMS != 0 || fixed.PauseTrimMS == 0 || fixed.PauseTrimMS > int(late.Milliseconds()) {
					t.Fatal("removed more than the delay, or not from a pause", fixed)
				}
				speech, removed := rtcRemovedSpeech(clean, fixed.Dropped)
				t.Logf("loudest speech removed %.1f dB, speech %.1f dB", removed, speech)
				if removed > speech-30 {
					t.Fatal("removed audible speech", removed, speech)
				}
			})
		}
	}
}

// Every chance to drop a frame is taken here: lateness keeps coming back, over
// other voices and at low speech levels.
func TestRTCPauseDetectionKeepsSpeech(t *testing.T) {
	const seconds = 20
	frames := seconds * 50
	var events []time.Duration
	for e := 2 * time.Second; e < 19*time.Second; e += 2 * time.Second {
		events = append(events, e)
	}
	scaled := func(pcm []int16, dB float64) []int16 {
		out := make([]int16, len(pcm))
		for i, v := range pcm {
			out[i] = int16(float64(v) * math.Pow(10, dB/20))
		}
		return out
	}
	english := rtcCleanSpeech(t, seconds)
	file, err := os.Open("testdata/agent-audio/caller-interrupt-fr.wav")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := inspectPCM16WAV(file)
	if err != nil {
		t.Fatal(err)
	}
	var source []int16
	if err := forEachWAVFrame(file, info, func(p []int16) error { source = append(source, p...); return nil }); err != nil {
		t.Fatal(err)
	}
	french := newPCMResampler(info.SampleRate, 24000).Process(source)
	for len(french) < seconds*24000 {
		french = append(french, french...)
	}
	french = french[:seconds*24000]
	voices := []struct {
		name  string
		clean []int16
		noise float64
	}{
		{"english_clean_line", english, math.Inf(-1)},
		{"english_noise_60", english, -60},
		{"english_noise_50", english, -50},
		{"english_noise_40", english, -40},
		{"french_noise_50", french, -50},
		{"quiet_speaker_noise_60", scaled(english, -20), -60},
		{"quiet_speaker_noise_50", scaled(english, -20), -50},
	}
	for _, voice := range voices {
		t.Run(voice.name, func(t *testing.T) {
			pcm := rtcWithNoise(voice.clean, voice.noise, 1, nil)
			r := runRTCArrivals(currentRTCPacingSender, rtcArrivalScenario{pcm: pcm, arrival: lateArrivals(frames, 60*time.Millisecond, events...), eventEnd: 18060 * time.Millisecond})
			logRTCArrivals(t, voice.name, r)
			speech, removed := rtcRemovedSpeech(voice.clean, r.Dropped)
			t.Logf("removed %d frames, loudest speech removed %.1f dB, speech %.1f dB", len(r.Dropped), removed, speech)
			if r.SpeechTrimMS != 0 || r.OverflowMS != 0 {
				t.Fatal("speech removed by the budget or the queue bound", r)
			}
			// A removed frame holds nothing louder than the noise around it: the
			// 6 dB of the rule, plus the recording's own background.
			limit := speech - 30
			if !math.IsInf(voice.noise, -1) {
				limit = voice.noise + 8
			}
			if removed > limit {
				t.Fatal("removed audible speech", removed, limit)
			}
		})
	}
	// With no lateness there is no backlog, so nothing is touched.
	for _, noise := range []float64{math.Inf(-1), -60, -40} {
		r := runRTCArrivals(currentRTCPacingSender, rtcArrivalScenario{pcm: rtcWithNoise(english, noise, 1, nil), arrival: onTimeArrivals(frames)})
		if r.PauseTrimMS != 0 || len(r.Dropped) != 0 || r.PeakDelay > 5*time.Millisecond {
			t.Fatal("audio changed on a steady call", noise, r)
		}
	}
}

// Steady audio has no pauses: a tone or music keeps every frame until it ends,
// and so does noise loud enough to be taken for it.
func TestRTCPauseDetectionKeepsSteadyAudio(t *testing.T) {
	const seconds = 12
	frames := seconds * 50
	tone := make([]int16, seconds*24000)
	for i := range tone {
		tone[i] = int16(3000 * math.Sin(2*math.Pi*440*float64(i)/24000))
	}
	for name, pcm := range map[string][]int16{
		"tone":      tone,
		"music":     rtcMusicLike(seconds),
		"noise_-30": rtcWithNoise(make([]int16, seconds*24000), -30, 1, nil),
	} {
		t.Run(name, func(t *testing.T) {
			r := runRTCArrivals(currentRTCPacingSender, rtcArrivalScenario{pcm: pcm, arrival: lateArrivals(frames, 60*time.Millisecond, 2*time.Second), eventEnd: 2060 * time.Millisecond})
			logRTCArrivals(t, name, r)
			if r.PauseTrimMS != 0 || r.SpeechTrimMS != 0 || len(r.Dropped) != 0 {
				t.Fatal("steady audio lost frames", r)
			}
		})
	}
}

func TestRTCPauseDetectionFollowsTheLine(t *testing.T) {
	const seconds = 20
	frames := seconds * 50
	clean := rtcCleanSpeech(t, seconds)
	late := lateArrivals(frames, 60*time.Millisecond, 2500*time.Millisecond)
	run := func(t *testing.T, pcm []int16) rtcArrivalResult {
		t.Helper()
		r := runRTCArrivals(currentRTCPacingSender, rtcArrivalScenario{pcm: pcm, arrival: late, eventEnd: 2560 * time.Millisecond})
		logRTCArrivals(t, t.Name(), r)
		if r.SettledDelay > 25*time.Millisecond || r.Settle > 4*time.Second {
			t.Fatal("delay did not clear", r)
		}
		return r
	}
	t.Run("noise_moving_6dB", func(t *testing.T) {
		wobble := func(i int) float64 { return math.Pow(10, 6*math.Sin(2*math.Pi*float64(i)/24000/3.7)/20) }
		run(t, rtcWithNoise(clean, -50, 1, wobble))
	})
	// A silent frame says nothing about the noise of the line.
	t.Run("dropout_each_second", func(t *testing.T) {
		pcm := rtcWithNoise(clean, -50, 1, nil)
		for i := 0; i < seconds; i++ {
			clear(pcm[i*24000 : i*24000+480])
		}
		run(t, pcm)
	})
	// A louder line is learned within two windows.
	t.Run("noise_rises_20dB", func(t *testing.T) {
		rise := func(i int) float64 {
			if i < 24000 {
				return .1
			}
			return 1
		}
		r := runRTCArrivals(currentRTCPacingSender, rtcArrivalScenario{pcm: rtcWithNoise(clean, -40, 1, rise), arrival: lateArrivals(frames, 60*time.Millisecond, 6*time.Second), eventEnd: 6060 * time.Millisecond})
		logRTCArrivals(t, t.Name(), r)
		if r.SettledDelay > 25*time.Millisecond {
			t.Fatal("delay did not clear", r)
		}
	})
}

func TestRTCPauseDetectorNeedsHistoryAndARun(t *testing.T) {
	noise := rtcWithNoise(make([]int16, 480), -50, 1, nil)
	loud := make([]int16, 480)
	for i := range loud {
		loud[i] = int16(8000 * math.Sin(2*math.Pi*200*float64(i)/24000))
	}
	var d rtcPauseDetector
	// Nothing is known about the line yet.
	for range rtcFloorWindowFrames - 1 {
		if d.observe(noise); d.active() {
			t.Fatal("pause without a floor")
		}
	}
	d.observe(noise)
	for range 8 {
		d.observe(loud)
	}
	if d.active() {
		t.Fatal("speech taken for a pause")
	}
	for i := 0; i < rtcPauseRunFrames; i++ {
		if d.observe(noise); d.active() {
			t.Fatal("pause before the run completed", i)
		}
	}
	if d.observe(noise); !d.active() {
		t.Fatal("pause not recognised")
	}
	if d.observe(loud); d.active() {
		t.Fatal("pause outlived the speech that ended it")
	}
}

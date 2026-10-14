package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"
)

// These are speech-continuity proxies, not MOS/PESQ or an assertion that a human
// cannot hear a defect. They measure the actual output PCM against an independent
// clean codec decode. Natural pauses are excluded; PLC is not assumed to restore
// the missing words. Everything here is test-only and uses a virtual clock.
type sipSpeechFrame struct {
	at            time.Duration
	pcm           []int16
	sourceSamples int // measured consumption; bounded sample-clock correction
}

type sipSpeechEvidence struct {
	Fixture, Codec, Profile, Mode string
	FirstWordPreserved            bool
	ActiveSpeechCoverage          float64
	LongestDamagedSpeechMS        int
	LongestPlaybackInterruptionMS int
	MaxAddedDelayMS               float64
	P95AddedDelayMS               float64
	DiscardedMS, ReplacementMS    float64
	CompleteHealthyPCMIdentical   bool
	Accepted                      bool
}

type sipSpeechBudget struct {
	coverage float64
	gapMS    int
	delayMS  float64
}

func (b sipSpeechBudget) accepts(r sipSpeechEvidence) bool {
	return r.FirstWordPreserved && r.ActiveSpeechCoverage >= b.coverage &&
		r.LongestDamagedSpeechMS <= b.gapMS && r.LongestPlaybackInterruptionMS <= b.gapMS && r.MaxAddedDelayMS <= b.delayMS
}

var sipSpeechFixtures = []struct{ id, hash string }{
	{"1272-128104-0000", "d1a34a518cd53e6a2e6640991fb12a2114309b3776df06c8319338a0fe6ce4aa"},
	{"1272-128104-0001", "40862be5067c9757843d16913dfd03b93d3551a11ce6cbd9c171371947483f22"},
}

func loadSIPSpeech(t *testing.T, id, hash string, rate int) []int16 {
	t.Helper()
	path := filepath.Join("testdata", "speech", id+".wav")
	raw, err := os.ReadFile(path)
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != hash {
		t.Fatalf("missing or changed human-speech fixture %s: %v", id, err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := inspectPCM16WAV(file)
	if err != nil || info.SampleRate != 16000 || info.Channels != 1 {
		t.Fatalf("fixture format: %+v %v", info, err)
	}
	var pcm []int16
	if err := forEachWAVFrame(file, info, func(p []int16) error { pcm = append(pcm, p...); return nil }); err != nil {
		t.Fatal(err)
	}
	// Remove the recording's long quiet lead-in, retaining 40ms before speech.
	// This explicitly exercises speaking immediately after the media path opens.
	onset := speechOnset(pcm, info.SampleRate)
	pcm = pcm[max(0, onset-info.SampleRate*40/1000):]
	if rate != info.SampleRate {
		pcm = newPCMResampler(info.SampleRate, rate).Process(pcm)
	}
	return pcm
}

func speechRMS(p []int16) float64 {
	var e float64
	for _, v := range p {
		e += float64(v) * float64(v)
	}
	if len(p) == 0 {
		return 0
	}
	return math.Sqrt(e / float64(len(p)))
}

func speechOnset(p []int16, rate int) int {
	n := rate / 50
	for i := 0; i+n <= len(p); i += n {
		if speechRMS(p[i:i+n]) >= 200 {
			return i
		}
	}
	return len(p)
}

// Positive correlation plus normalized waveform error detects muted/repeated/
// clipped speech. Gain or energy alone must not turn replacement audio into a
// preserved word. Subsampling only reduces test analysis cost, not media data.
func speechSimilarity(a, b []int16) (float64, float64) {
	if len(a) != len(b) || len(a) == 0 {
		return 0, math.Inf(1)
	}
	var aa, bb, ab, err float64
	for i := 0; i < len(a); i += 4 {
		x, y := float64(a[i]), float64(b[i])
		aa += x * x
		bb += y * y
		ab += x * y
		err += (x - y) * (x - y)
	}
	if aa == 0 || bb == 0 {
		return 0, math.Inf(1)
	}
	return ab / math.Sqrt(aa*bb), math.Sqrt(err / bb)
}

// Account for the documented <=125us/frame timing correction without confusing
// its interpolation of high-frequency consonants with missing words. The
// reference is independently interpolated; larger time warps are never allowed.
func speechClockReference(p []int16, n int) []int16 {
	if len(p) == n {
		return p
	}
	out := make([]int16, n)
	for i := range out {
		x := float64(i) * float64(len(p)-1) / float64(n-1)
		lo := int(x)
		fraction := x - float64(lo)
		out[i] = int16(math.Round(float64(p[lo])*(1-fraction) + float64(p[min(lo+1, len(p)-1)])*fraction))
	}
	return out
}

// Locate each output near its consumed RTP position. The small search tolerates
// sample clock correction/decoder phase; it cannot match words seconds away or
// conceal an accumulated delay. Each source frame is credited at most once.
func analyzeSIPSpeech(reference []int16, rate int, output []sipSpeechFrame, positions []int, baselineMS float64) sipSpeechEvidence {
	n := rate / 50
	covered := make([]bool, (len(reference)+n-1)/n)
	delays := []float64{}
	playbackRun, maxPlaybackRun := 0, 0
	for k, frame := range output {
		if k >= len(positions) {
			break
		}
		position := positions[k]
		activeExpected := position >= 0 && position+n <= len(reference) && speechRMS(reference[position:position+n]) >= 200
		interrupted := func() {
			if activeExpected {
				playbackRun += 20
				maxPlaybackRun = max(maxPlaybackRun, playbackRun)
			} else {
				playbackRun = 0
			}
		}
		if len(frame.pcm) != n {
			interrupted()
			continue
		}
		take := frame.sourceSamples
		if take == 0 {
			take = n
		}
		if absInt(take-n) > rate/8000 {
			interrupted()
			continue
		}
		bestScore, bestErr, best := -1., math.Inf(1), -1
		for shift := -rate / 500; shift <= rate/500; shift++ {
			start := positions[k] + shift
			if start < 0 || start+take > len(reference) {
				continue
			}
			corr, err := speechSimilarity(frame.pcm, speechClockReference(reference[start:start+take], n))
			if corr > bestScore {
				bestScore, bestErr, best = corr, err, start
			}
		}
		if best < 0 || bestScore < .92 || bestErr > .4 {
			interrupted()
			continue
		}
		playbackRun = 0
		index := (best + n/2) / n
		if index >= len(covered) || covered[index] {
			continue
		}
		covered[index] = true
		if speechRMS(reference[index*n:min((index+1)*n, len(reference))]) >= 200 {
			delays = append(delays, max(0, float64(frame.at)/float64(time.Millisecond)-float64(best)*1000/float64(rate)-baselineMS))
		}
	}
	onset := speechOnset(reference, rate)
	r := sipSpeechEvidence{FirstWordPreserved: true, LongestPlaybackInterruptionMS: maxPlaybackRun}
	active, delivered, run := 0, 0, 0
	for i := range covered {
		start := i * n
		if speechRMS(reference[start:min(start+n, len(reference))]) < 200 {
			run = 0
			continue
		}
		active++
		if covered[i] {
			delivered++
			run = 0
		} else {
			run += 20
			r.LongestDamagedSpeechMS = max(r.LongestDamagedSpeechMS, run)
			// Conservative onset window: all active audio in the first 500ms
			// after speech starts, rather than an unverified word alignment.
			if start >= onset && start < onset+rate/2 {
				r.FirstWordPreserved = false
			}
		}
	}
	if active > 0 {
		r.ActiveSpeechCoverage = float64(delivered) / float64(active)
	} else {
		r.FirstWordPreserved = false
	}
	sort.Float64s(delays)
	if len(delays) > 0 {
		r.MaxAddedDelayMS = delays[len(delays)-1]
		r.P95AddedDelayMS = delays[int(math.Ceil(.95*float64(len(delays))))-1]
	} else {
		r.MaxAddedDelayMS = 1e9
		r.P95AddedDelayMS = 1e9
	}
	return r
}

func replaySIPSpeech(t *testing.T, id, hash, codec, profile string, resync bool) sipSpeechEvidence {
	t.Helper()
	media := &sipRTPMedia{offer: sipMediaOffer{Codec: codec, PacketSamples: 160}}
	encoder, decoder := &sipRTPMedia{offer: media.offer}, &sipRTPMedia{offer: media.offer}
	rate, n := media.sampleRate(), media.sampleRate()/50
	input := loadSIPSpeech(t, id, hash, rate)
	speechSampleCount := len(input)
	// Silence after the complete utterance allows every retained speech sample
	// to leave the buffer. We don't compare only a prefix or forget the last word.
	input = append(input, make([]int16, rate/2)...)
	input = append(input, make([]int16, (n-len(input)%n)%n)...)
	var reference []int16
	var packets []productionAudioPacket
	lossIndex, highest := 0, 0.
	for i := rate; i+n <= len(input)-rate/2; i += n {
		if e := speechRMS(input[i : i+n]); e > highest {
			lossIndex, highest = i/n, e
		}
	}
	for i := 0; i < len(input)/n; i++ {
		payload := encoder.encodeAudio(input[i*n : (i+1)*n])
		reference = append(reference, decoder.decodeAudio(payload)...)
		source := time.Duration(i) * 20 * time.Millisecond
		arrival, processed := source, source
		switch profile {
		case "ordinary_jitter":
			arrival += time.Duration(i*17%21) * time.Millisecond
			processed = arrival
		case "80ms_batch":
			if i >= lossIndex && i < lossIndex+4 {
				arrival = time.Duration(lossIndex+4) * 20 * time.Millisecond
				processed = arrival
			}
		case "single_packet_loss":
			if i == lossIndex {
				continue
			}
		case "420ms_burst":
			if i >= lossIndex && i < lossIndex+21 {
				arrival = time.Duration(lossIndex+21) * 20 * time.Millisecond
				processed = arrival
			}
		case "startup_handler_delay_440ms":
			if i < 22 {
				processed = 440 * time.Millisecond
			}
		case "stable_shift_240ms":
			if i >= 50 {
				arrival += 240 * time.Millisecond
				processed = arrival
			}
		}
		packets = append(packets, productionAudioPacket{arrival, processed, uint16(65000 + i), uint32(0xffffff00 + uint64(i)*160), payload})
	}
	sort.SliceStable(packets, func(i, j int) bool { return packets[i].processed < packets[j].processed })
	b := newRTPJitterBufferWithPolicy(sipPlaybackPolicy{Resynchronize: &resync})
	f := newRTPAudioFramer(b, media.offer)
	base := time.Unix(100, 0)
	var output []sipSpeechFrame
	var positions []int
	var actual []int16
	next := 0
	for tick := time.Duration(0); tick < time.Duration(len(input))*time.Second/time.Duration(rate)+time.Second; tick += 20 * time.Millisecond {
		for next < len(packets) && packets[next].processed <= tick {
			q := packets[next]
			b.pushRTPAt(q.seq, q.payload, q.ts, base.Add(q.arrival), base.Add(q.processed))
			next++
		}
		before := b.driftAdjustedSamples.Load()
		if pcm, ready := f.popPCMAt(media, base.Add(tick)); ready {
			if len(pcm) != n {
				t.Fatal("invalid frame duration")
			}
			take := n + int(b.driftAdjustedSamples.Load()-before)
			end := int(int32(b.expectedTimestamp-uint32(0xffffff00))) * rate / 8000
			positions = append(positions, end-len(f.pendingPCM)-take)
			output = append(output, sipSpeechFrame{tick, append([]int16(nil), pcm...), take})
			actual = append(actual, pcm...)
		} else {
			// No returned PCM is silence on the actual playback timeline. Its
			// expected source position uses the healthy scheduling baseline.
			positions = append(positions, int((tick-40*time.Millisecond)*time.Duration(rate)/time.Second))
			output = append(output, sipSpeechFrame{at: tick, sourceSamples: n})
		}
		if b.buffered+int(b.pendingPCMSamples.Load()) > b.policy.CapacityMS*8 {
			t.Fatal("queue bounds violated")
		}
		// End of finite input is deliberate shutdown, not a new packet outage.
		if next == len(packets) && b.buffered == 0 && len(f.pendingPCM) < n {
			break
		}
	}
	r := analyzeSIPSpeech(reference, rate, output, positions, 40)
	r.Fixture, r.Codec, r.Profile = id, codec, profile
	r.Mode = "default"
	if resync {
		r.Mode = "resync_enabled"
	}
	r.DiscardedMS = float64(b.droppedSamples+b.lateDroppedSamples) / 8
	r.ReplacementMS = float64(b.missingFilledSamples.Load()) / 8
	r.CompleteHealthyPCMIdentical = len(actual) >= speechSampleCount && bytes.Equal(pcm16ToBytes(actual[:speechSampleCount]), pcm16ToBytes(reference[:speechSampleCount]))
	if dir := os.Getenv("TELEPHONY_SPEECH_ACCEPTANCE_AUDIO_DIR"); dir != "" && codec == "G722" && !resync && id == sipSpeechFixtures[0].id {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
		var timeline []int16
		for _, frame := range output {
			if len(frame.pcm) == n {
				timeline = append(timeline, frame.pcm...)
			} else {
				timeline = append(timeline, make([]int16, n)...)
			}
		}
		writeSpeechAcceptanceWAV(t, filepath.Join(dir, profile+".wav"), rate, timeline)
		if profile == "normal" {
			clean := append(make([]int16, rate*40/1000), reference[:speechSampleCount]...)
			writeSpeechAcceptanceWAV(t, filepath.Join(dir, "clean-codec-reference.wav"), rate, clean)
		}
	}
	return r
}

func writeSpeechAcceptanceWAV(t *testing.T, path string, rate int, pcm []int16) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeMonoWAVHeader(file, rate, int64(len(pcm)*2)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if _, err := file.Write(pcm16ToBytes(pcm)); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSIPNaturalSpeechAcceptance(t *testing.T) {
	var results []sipSpeechEvidence
	for _, fixture := range sipSpeechFixtures {
		for _, codec := range []string{"PCMU", "PCMA", "G722"} {
			for _, resync := range []bool{false, true} {
				for _, profile := range []string{"normal", "ordinary_jitter", "80ms_batch", "single_packet_loss", "420ms_burst", "startup_handler_delay_440ms", "stable_shift_240ms"} {
					name := fmt.Sprintf("%s/%s/resync_%t/%s", fixture.id, codec, resync, profile)
					t.Run(name, func(t *testing.T) {
						r := replaySIPSpeech(t, fixture.id, fixture.hash, codec, profile, resync)
						budget := sipSpeechBudget{coverage: .99, gapMS: 20, delayMS: 100}
						if profile == "normal" {
							budget = sipSpeechBudget{coverage: 1, gapMS: 0, delayMS: 2}
						}
						if profile == "ordinary_jitter" {
							budget = sipSpeechBudget{coverage: 1, gapMS: 0, delayMS: 40}
						}
						if profile == "single_packet_loss" {
							budget = sipSpeechBudget{coverage: .98, gapMS: 80, delayMS: 100}
						}
						r.Accepted = budget.accepts(r)
						results = append(results, r)
						if profile == "normal" && (!r.CompleteHealthyPCMIdentical || r.ActiveSpeechCoverage != 1 || !r.FirstWordPreserved || r.DiscardedMS != 0) {
							t.Fatalf("complete healthy speech changed: %+v", r)
						}
						switch profile {
						case "normal", "ordinary_jitter", "single_packet_loss":
							if !r.Accepted {
								t.Errorf("speech acceptance failed: %+v", r)
							}
						case "80ms_batch":
							if !r.FirstWordPreserved || r.ActiveSpeechCoverage != 1 || r.MaxAddedDelayMS > 100 || r.LongestPlaybackInterruptionMS > 40 {
								t.Errorf("moderate batch exceeded retained-speech/recovery bound: %+v", r)
							}
						case "420ms_burst", "startup_handler_delay_440ms", "stable_shift_240ms":
							if r.Accepted {
								t.Errorf("severe fault mislabeled smooth speech: %+v", r)
							}
						}
						t.Logf("speech %+v", r)
					})
				}
			}
		}
	}
	if path := os.Getenv("TELEPHONY_SPEECH_ACCEPTANCE_RESULTS"); path != "" {
		raw, err := json.MarshalIndent(results, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSIPSpeechAcceptanceDetectsRegressions(t *testing.T) {
	fixture := sipSpeechFixtures[0]
	reference := loadSIPSpeech(t, fixture.id, fixture.hash, 16000)
	n := 320
	reference = reference[:len(reference)/n*n]
	for _, fault := range []string{"clean", "first_word_clipped", "middle_200ms_muted", "middle_200ms_no_output", "duplicate_speech", "extra_300ms_delay", "excessive_clock_warp"} {
		t.Run(fault, func(t *testing.T) {
			var frames []sipSpeechFrame
			var positions []int
			for i := 0; i < len(reference); i += n {
				pcm := append([]int16(nil), reference[i:i+n]...)
				at := time.Duration(i)*time.Second/16000 + 40*time.Millisecond
				switch fault {
				case "first_word_clipped":
					if i < 8000 {
						clear(pcm)
					}
				case "middle_200ms_muted":
					if i >= 16000 && i < 19200 {
						clear(pcm)
					}
				case "middle_200ms_no_output":
					if i >= 16000 && i < 19200 {
						pcm = nil
					}
				case "duplicate_speech":
					if i >= 16000 && i < 19200 {
						copy(pcm, reference[8000:8320])
					}
				case "extra_300ms_delay":
					at += 300 * time.Millisecond
				}
				take := n
				if fault == "excessive_clock_warp" {
					take = n + 10
				}
				frames = append(frames, sipSpeechFrame{at, pcm, take})
				positions = append(positions, i)
			}
			r := analyzeSIPSpeech(reference, 16000, frames, positions, 40)
			if (sipSpeechBudget{coverage: .99, gapMS: 20, delayMS: 100}).accepts(r) != (fault == "clean") {
				t.Fatalf("detector failed %s: %+v", fault, r)
			}
			switch fault {
			case "first_word_clipped":
				if r.FirstWordPreserved {
					t.Fatal("opening speech loss missed")
				}
			case "middle_200ms_muted", "middle_200ms_no_output", "duplicate_speech":
				if r.LongestPlaybackInterruptionMS < 100 {
					t.Fatalf("interruption missed: %+v", r)
				}
			case "extra_300ms_delay":
				if math.Abs(r.MaxAddedDelayMS-300) > 2 || math.Abs(r.P95AddedDelayMS-300) > 2 {
					t.Fatalf("delay measurement inaccurate: %+v", r)
				}
			case "clean":
				if r.ActiveSpeechCoverage != 1 || r.LongestPlaybackInterruptionMS != 0 || !r.FirstWordPreserved {
					t.Fatalf("natural pauses were called interruptions: %+v", r)
				}
			}
		})
	}
}

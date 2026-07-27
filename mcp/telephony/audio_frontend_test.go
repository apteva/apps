package main

import (
	"math"
	"testing"
)

func TestAudioFrontendRequiresSustainedSpeechIndependentOfCarrierFrames(t *testing.T) {
	for _, tc := range []struct {
		name       string
		sampleRate int
		chunks     []int
		roundTrip  bool
	}{
		{name: "twilio_pcmu_20ms", sampleRate: 8000, chunks: []int{160}, roundTrip: true},
		{name: "pcmu_variable", sampleRate: 8000, chunks: []int{80, 320, 40, 200}},
		{name: "signalwire_variable", sampleRate: 24000, chunks: []int{120, 960, 240, 600}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
			frontend := newCarrierAudioFrontend(tc.sampleRate)
			warmAudioFrontend(frontend, telephoneNoise(tc.sampleRate, 500, 110))
			speech := telephoneSpeech(tc.sampleRate, 700, 1500)
			if tc.roundTrip {
				speech = ulawToPCM16(pcm16ToUlaw(speech))
			}
			startedAt := processInChunks(frontend, speech, tc.chunks)
			if startedAt < defaultSpeechStartMS || startedAt > 500 {
				t.Fatalf("speech started at %dms, want %d-500ms", startedAt, defaultSpeechStartMS)
			}
		})
	}
}

func TestAudioFrontendRejectsShortClatterAndTelephoneNoise(t *testing.T) {
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
	frontend := newCarrierAudioFrontend(8000)
	warmAudioFrontend(frontend, telephoneNoise(8000, 500, 180))

	clatter := make([]int16, 8000*40/1000)
	for i := range clatter {
		if i%17 == 0 {
			clatter[i] = 22000
		}
	}
	if got := processInChunks(frontend, clatter, []int{160}); got != 0 {
		t.Fatalf("40ms clatter triggered speech at %dms", got)
	}
	if got := processInChunks(frontend, trafficNoise(8000, 1800), []int{80, 240, 160}); got != 0 {
		t.Fatalf("traffic noise triggered speech at %dms", got)
	}
	if snapshot := frontend.snapshot(); snapshot.LocalSpeechStarts != 0 {
		t.Fatalf("local speech starts=%d after non-speech fixtures", snapshot.LocalSpeechStarts)
	}
}

func TestAudioFrontendRejectsMusicAndLowBackgroundSpeech(t *testing.T) {
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")

	musicFrontend := newCarrierAudioFrontend(8000)
	warmAudioFrontend(musicFrontend, telephoneNoise(8000, 500, 100))
	if got := processInChunks(musicFrontend, holdMusic(8000, 1600, 850), []int{160}); got != 0 {
		t.Fatalf("hold music triggered speech at %dms", got)
	}

	backgroundFrontend := newCarrierAudioFrontend(8000)
	background := telephoneSpeech(8000, 1400, 260)
	warmAudioFrontend(backgroundFrontend, background[:8000*600/1000])
	if got := processInChunks(backgroundFrontend, background[8000*600/1000:], []int{160}); got != 0 {
		t.Fatalf("low background speech triggered speech at %dms", got)
	}
}

func TestAudioFrontendHandlesCallerSpeechInCafeNoiseAfterPCMU(t *testing.T) {
	for _, ingress := range []string{"direct_twilio", "ringover_forwarded_to_twilio"} {
		t.Run(ingress, func(t *testing.T) {
			t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
			frontend := newCarrierAudioFrontend(8000)
			cafe := cafeNoise(8000, 1800)
			cafe = ulawToPCM16(pcm16ToUlaw(cafe))
			if got := processInChunks(frontend, cafe, []int{80, 160, 320}); got != 0 {
				t.Fatalf("cafe background triggered speech at %dms", got)
			}

			mixed := mixPCM(cafeNoise(8000, 800), telephoneSpeech(8000, 800, 2200))
			mixed = ulawToPCM16(pcm16ToUlaw(mixed))
			if got := processInChunks(frontend, mixed, []int{320, 80, 160}); got < defaultSpeechStartMS || got > 600 {
				t.Fatalf("caller over cafe noise detected at %dms, want %d-600ms", got, defaultSpeechStartMS)
			}
		})
	}
}

func TestAudioFrontendDetectsQuietDistantCallerAndRearms(t *testing.T) {
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "fallback")
	frontend := newCarrierAudioFrontend(8000)
	warmAudioFrontend(frontend, telephoneNoise(8000, 600, 70))

	if got := processInChunks(frontend, telephoneSpeech(8000, 800, 1000), []int{160}); got == 0 {
		t.Fatal("quiet distant caller was not detected")
	}
	processInChunks(frontend, make([]int16, 8000*600/1000), []int{160})
	if got := processInChunks(frontend, telephoneSpeech(8000, 800, 1000), []int{160}); got == 0 {
		t.Fatal("detector did not rearm after sustained silence")
	}
	if starts := frontend.snapshot().LocalSpeechStarts; starts != 2 {
		t.Fatalf("local speech starts=%d, want 2", starts)
	}
}

func TestAudioFrontendProviderModeDisablesOnlyLocalFallback(t *testing.T) {
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "provider")
	frontend := newCarrierAudioFrontend(8000)
	if got := processInChunks(frontend, telephoneSpeech(8000, 900, 1800), []int{160}); got != 0 {
		t.Fatalf("provider mode emitted local speech start at %dms", got)
	}
	snapshot := frontend.snapshot()
	if snapshot.VADSpeechMS == 0 {
		t.Fatal("provider mode disabled VAD diagnostics")
	}
	if snapshot.LocalSpeechStarts != 0 {
		t.Fatalf("provider mode local starts=%d", snapshot.LocalSpeechStarts)
	}
}

func TestAudioFrontendDefaultsToProviderSpeechDetection(t *testing.T) {
	t.Setenv("TELEPHONY_LOCAL_BARGE_IN_MODE", "")
	frontend := newCarrierAudioFrontend(8000)
	if frontend.mode != localBargeInOff {
		t.Fatalf("default local barge-in mode=%v, want provider-authoritative mode", frontend.mode)
	}
	if got := processInChunks(frontend, telephoneSpeech(8000, 900, 1800), []int{160}); got != 0 {
		t.Fatalf("default mode emitted local speech start at %dms", got)
	}
	if snapshot := frontend.snapshot(); snapshot.VADSpeechMS == 0 {
		t.Fatal("default provider mode disabled VAD diagnostics")
	}
}

func TestAudioFrontendSuppressesNoiseWithoutAttenuatingSpeech(t *testing.T) {
	t.Setenv("TELEPHONY_NOISE_SUPPRESSION", "true")
	noiseFrontend := newCarrierAudioFrontend(8000)
	noise := telephoneNoise(8000, 600, 180)
	noiseResult := noiseFrontend.process(noise)
	if len(noiseResult.PCM) == 0 {
		t.Fatal("noise frontend returned no audio")
	}
	if got, wantMax := pcmRMS(noiseResult.PCM), pcmRMS(noise)*0.65; got >= wantMax {
		t.Fatalf("noise RMS=%f, want below %f", got, wantMax)
	}

	speechFrontend := newCarrierAudioFrontend(8000)
	speech := telephoneSpeech(8000, 600, 1800)
	speechResult := speechFrontend.process(speech)
	if got, wantMin := pcmRMS(speechResult.PCM), pcmRMS(speech)*0.75; got <= wantMin {
		t.Fatalf("speech RMS=%f, want above %f", got, wantMin)
	}
}

func TestAudioFrontendAttributesLocalAndProviderInterrupts(t *testing.T) {
	frontend := newCarrierAudioFrontend(8000)
	if source := frontend.markInterrupt(""); source != "provider_or_core" {
		t.Fatalf("unattributed interruption source=%q", source)
	}
	frontend.markLocalSignal()
	if source := frontend.markInterrupt(""); source != "local" {
		t.Fatalf("local interruption source=%q", source)
	}
	if source := frontend.markInterrupt("provider"); source != "provider_or_core" {
		t.Fatalf("provider interruption source=%q", source)
	}
	snapshot := frontend.snapshot()
	if snapshot.LocalInterrupts != 1 || snapshot.ProviderCoreInterrupts != 2 {
		t.Fatalf("interrupt attribution=%+v", snapshot)
	}
}

func TestAudioFrontendDiagnosticsIncludeCarrierNoiseAndIngress(t *testing.T) {
	frontend := newCarrierAudioFrontend(8000)
	frontend.process(ulawToPCM16(pcm16ToUlaw(cafeNoise(8000, 500))))
	frontend.markLocalSignal()
	frontend.markInterrupt("")

	logger := &capturingAudioLogger{}
	row := &callRow{
		ID: "call-diagnostics", ForwardedFrom: "+34930494946", IngressPath: "forwarded",
	}
	logAudioFrontendDiagnostics(logger, frontend, row, "twilio", carrierCodecPCMU8, 240)
	for key, want := range map[string]any{
		"provider":         "twilio",
		"call":             "call-diagnostics",
		"codec":            carrierCodecPCMU8,
		"sample_rate":      8000,
		"ingress_path":     "forwarded",
		"forwarded":        true,
		"local_interrupts": int64(1),
		"max_queued_ms":    240,
	} {
		if got := logger.fields[key]; got != want {
			t.Fatalf("diagnostic %s=%#v, want %#v; fields=%#v", key, got, want, logger.fields)
		}
	}
	for _, key := range []string{
		"input_rms_avg_dbfs", "input_rms_max_dbfs", "adaptive_noise_floor_dbfs",
		"vad_speech_ms", "local_vad_max_activation_ms", "suppressed_frames",
		"provider_or_core_interrupts",
	} {
		if _, ok := logger.fields[key]; !ok {
			t.Fatalf("diagnostic field %q missing: %#v", key, logger.fields)
		}
	}
}

type capturingAudioLogger struct {
	message string
	fields  map[string]any
}

func (l *capturingAudioLogger) Info(message string, values ...any) {
	l.message = message
	l.fields = make(map[string]any, len(values)/2)
	for i := 0; i+1 < len(values); i += 2 {
		key, _ := values[i].(string)
		l.fields[key] = values[i+1]
	}
}

func warmAudioFrontend(frontend *carrierAudioFrontend, pcm []int16) {
	processInChunks(frontend, pcm, []int{frontend.frameSamples})
}

func processInChunks(frontend *carrierAudioFrontend, pcm []int16, chunks []int) int {
	offset := 0
	chunkIndex := 0
	processedSamples := 0
	for offset < len(pcm) {
		size := chunks[chunkIndex%len(chunks)]
		chunkIndex++
		end := min(len(pcm), offset+size)
		result := frontend.process(pcm[offset:end])
		processedSamples += len(result.PCM)
		if result.SpeechStarted {
			return processedSamples * 1000 / frontend.sampleRate
		}
		offset = end
	}
	return 0
}

func telephoneSpeech(sampleRate, durationMS int, amplitude float64) []int16 {
	samples := sampleRate * durationMS / 1000
	out := make([]int16, samples)
	var seed uint32 = 7
	for i := range out {
		ms := i * 1000 / sampleRate
		syllable := ms / 90
		fundamental := 105.0 + float64((syllable%5)*23)
		t := float64(i) / float64(sampleRate)
		envelope := 0.55 + 0.45*math.Sin(math.Pi*float64(ms%90)/90)
		seed = seed*1664525 + 1013904223
		fricative := (float64(int32(seed>>16)-32768) / 32768) * 0.08
		value := amplitude * envelope * (0.58*math.Sin(2*math.Pi*fundamental*t) +
			0.27*math.Sin(2*math.Pi*2*fundamental*t) +
			0.12*math.Sin(2*math.Pi*3*fundamental*t) + fricative)
		out[i] = clampPCM16(value)
	}
	return out
}

func telephoneNoise(sampleRate, durationMS int, amplitude float64) []int16 {
	samples := sampleRate * durationMS / 1000
	out := make([]int16, samples)
	var seed uint32 = 19
	for i := range out {
		seed = seed*1664525 + 1013904223
		out[i] = int16((float64(int32(seed>>16)-32768) / 32768) * amplitude)
	}
	return out
}

func trafficNoise(sampleRate, durationMS int) []int16 {
	out := telephoneNoise(sampleRate, durationMS, 220)
	for i := range out {
		t := float64(i) / float64(sampleRate)
		out[i] = clampPCM16(float64(out[i]) +
			500*math.Sin(2*math.Pi*55*t) +
			180*math.Sin(2*math.Pi*110*t))
	}
	return out
}

func holdMusic(sampleRate, durationMS int, amplitude float64) []int16 {
	samples := sampleRate * durationMS / 1000
	out := make([]int16, samples)
	for i := range out {
		t := float64(i) / float64(sampleRate)
		value := amplitude * (0.55*math.Sin(2*math.Pi*440*t) +
			0.3*math.Sin(2*math.Pi*554.37*t) +
			0.15*math.Sin(2*math.Pi*659.25*t))
		out[i] = clampPCM16(value)
	}
	return out
}

func cafeNoise(sampleRate, durationMS int) []int16 {
	out := mixPCM(
		telephoneNoise(sampleRate, durationMS, 420),
		holdMusic(sampleRate, durationMS, 180),
		telephoneSpeech(sampleRate, durationMS, 240),
	)
	for offsetMS := 230; offsetMS < durationMS; offsetMS += 610 {
		start := offsetMS * sampleRate / 1000
		end := min(len(out), start+sampleRate*25/1000)
		for i := start; i < end; i++ {
			if (i-start)%23 == 0 {
				out[i] = clampPCM16(float64(out[i]) + 9000)
			}
		}
	}
	return out
}

func mixPCM(tracks ...[]int16) []int16 {
	length := 0
	for _, track := range tracks {
		length = max(length, len(track))
	}
	out := make([]int16, length)
	for i := range out {
		var sample float64
		for _, track := range tracks {
			if i < len(track) {
				sample += float64(track[i])
			}
		}
		out[i] = clampPCM16(sample)
	}
	return out
}

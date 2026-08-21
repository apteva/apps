package main

import (
	"math"
	"testing"
)

// This is the browser-to-carrier path used by a Telnyx human call. Keep this
// test separate from carrier contract tests: a codec can be configured
// correctly while a resampler silently changes speech level or timing.
func TestTelnyxWidebandSoftphonePathPreservesSpeechLevelAndTiming(t *testing.T) {
	const (
		inputRate  = 24000
		outputRate = 16000
		duration   = 3 * inputRate
	)

	input := make([]int16, duration)
	// A deterministic speech-like signal spanning the useful telephone band.
	components := []struct {
		frequency float64
		amplitude float64
	}{
		{frequency: 170, amplitude: 2600},
		{frequency: 420, amplitude: 1800},
		{frequency: 1100, amplitude: 1300},
		{frequency: 2400, amplitude: 800},
		{frequency: 3400, amplitude: 500},
	}
	for i := range input {
		timeSeconds := float64(i) / inputRate
		var sample float64
		for _, component := range components {
			sample += component.amplitude * math.Sin(2*math.Pi*component.frequency*timeSeconds)
		}
		input[i] = clampPCM16(sample)
	}

	resampler := newPCMResampler(inputRate, outputRate)
	var output []int16
	for start := 0; start < len(input); start += inputRate / 50 {
		end := min(len(input), start+inputRate/50)
		output = append(output, resampler.Process(input[start:end])...)
	}

	wantSamples := duration * outputRate / inputRate
	if lost := wantSamples - len(output); lost < 0 || lost > 40 {
		t.Fatalf("24 kHz to 16 kHz duration drift: want=%d got=%d", wantSamples, len(output))
	}

	// Ignore the filter's short startup transient when comparing levels.
	inputRMS := rmsPCM(input[inputRate/20:])
	outputRMS := rmsPCM(output[outputRate/20:])
	deltaDB := 20 * math.Log10(outputRMS/inputRMS)
	t.Logf("24 kHz to 16 kHz speech level delta: %.3f dB; duration: %.3f s -> %.3f s", deltaDB, float64(len(input))/inputRate, float64(len(output))/outputRate)
	if math.Abs(deltaDB) > 0.5 {
		t.Fatalf("24 kHz to 16 kHz changed speech level by %.2f dB (input=%.1f output=%.1f)", deltaDB, inputRMS, outputRMS)
	}
}

func TestTelnyxWidebandDoesNotLoseLevelVersusLegacyPCMU(t *testing.T) {
	const inputRate = 24000
	input := sinePCM(inputRate, 1000, inputRate*2)

	wideband := newPCMResampler(inputRate, 16000).Process(input)
	legacyLinear := newPCMResampler(inputRate, 8000).Process(input)
	legacy := ulawToPCM16(pcm16ToUlaw(legacyLinear))

	widebandRMS := rmsPCM(wideband[800:])
	legacyRMS := rmsPCM(legacy[400:])
	deltaDB := 20 * math.Log10(widebandRMS/legacyRMS)
	t.Logf("L16/16 kHz level versus legacy PCMU/8 kHz: %.3f dB", deltaDB)
	if math.Abs(deltaDB) > 0.5 {
		t.Fatalf("wideband level differs from legacy PCMU by %.2f dB (wideband=%.1f legacy=%.1f)", deltaDB, widebandRMS, legacyRMS)
	}
}

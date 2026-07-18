package main

import (
	"math"
	"testing"
)

func TestPCMResamplerPreservesDurationAcrossFrames(t *testing.T) {
	up := newPCMResampler(8000, 24000)
	down := newPCMResampler(24000, 8000)
	input := sinePCM(8000, 1000, 800)
	var upsampled []int16
	for start := 0; start < len(input); start += 160 {
		end := min(len(input), start+160)
		upsampled = append(upsampled, up.Process(input[start:end])...)
	}
	var roundTrip []int16
	for start := 0; start < len(upsampled); start += 480 {
		end := min(len(upsampled), start+480)
		roundTrip = append(roundTrip, down.Process(upsampled[start:end])...)
	}
	if delta := len(input) - len(roundTrip); delta < 0 || delta > 64 {
		t.Fatalf("duration drift: input=%d output=%d", len(input), len(roundTrip))
	}
	if rmsPCM(roundTrip) < 2000 {
		t.Fatalf("round-trip signal was attenuated: rms=%f", rmsPCM(roundTrip))
	}
}

func TestPCMResamplerSuppressesDownsampleAliasing(t *testing.T) {
	down := newPCMResampler(24000, 8000)
	passband := down.Process(sinePCM(24000, 1000, 2400))
	down = newPCMResampler(24000, 8000)
	stopband := down.Process(sinePCM(24000, 7000, 2400))
	passRMS, stopRMS := rmsPCM(passband), rmsPCM(stopband)
	if passRMS < 5000 || stopRMS > passRMS*0.15 {
		t.Fatalf("anti-alias filter ineffective: pass=%f stop=%f", passRMS, stopRMS)
	}
}

func TestTwilioAudioTransformKeepsContinuousToneAcrossPackets(t *testing.T) {
	down := newPCMResampler(24000, 8000)
	input := sinePCM(24000, 1000, 24000)
	var output []int16
	for start := 0; start < len(input); start += 960 {
		end := min(len(input), start+960)
		pcm8 := down.Process(input[start:end])
		for packetStart := 0; packetStart < len(pcm8); packetStart += twilioMediaPacketSamples {
			packetEnd := min(len(pcm8), packetStart+twilioMediaPacketSamples)
			output = append(output, ulawToPCM16(pcm16ToUlaw(pcm8[packetStart:packetEnd]))...)
		}
	}
	if len(output) < 7900 {
		t.Fatalf("audio transform lost duration: samples=%d", len(output))
	}
	longestQuietRun := 0
	quietRun := 0
	for _, sample := range output {
		if absInt16(sample) <= 8 {
			quietRun++
			longestQuietRun = max(longestQuietRun, quietRun)
		} else {
			quietRun = 0
		}
	}
	if longestQuietRun >= 8 {
		t.Fatalf("audio transform inserted a %.2fms gap", float64(longestQuietRun)*1000/8000)
	}
}

func absInt16(value int16) int {
	if value < 0 {
		return -int(value)
	}
	return int(value)
}

func sinePCM(rate, frequency, samples int) []int16 {
	out := make([]int16, samples)
	for i := range out {
		out[i] = int16(math.Round(12000 * math.Sin(2*math.Pi*float64(frequency*i)/float64(rate))))
	}
	return out
}

func rmsPCM(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var sum float64
	for _, sample := range samples {
		sum += float64(sample) * float64(sample)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

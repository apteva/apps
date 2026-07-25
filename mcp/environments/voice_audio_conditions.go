package main

import (
	"encoding/binary"
	"math"
)

const defaultVoiceAudioSeed int64 = 42

type voiceAudioPipeline struct {
	spec            VoiceAudioConditions
	state           uint32
	sample          int64
	colored         float64
	babble          float64
	lastSpeechRMS   float64
	processedFrames int64
	clippedSamples  int64
	applyCodec      bool
}

func newVoiceAudioPipeline(spec *VoiceAudioConditions, applyCodec bool) *voiceAudioPipeline {
	if spec == nil || (spec.Preset == "clean" && spec.Codec == "none") {
		return nil
	}
	seed := uint32(spec.Seed)
	if seed == 0 {
		seed = uint32(defaultVoiceAudioSeed)
	}
	return &voiceAudioPipeline{spec: *spec, state: seed, applyCodec: applyCodec}
}

func (p *voiceAudioPipeline) process(frame []byte) []byte {
	if p == nil {
		return frame
	}
	samples := pcm16FromBytes(frame)
	if len(samples) == 0 {
		return frame
	}

	foregroundRMS := pcmRMS(samples)
	if foregroundRMS > 250 {
		if p.lastSpeechRMS == 0 {
			p.lastSpeechRMS = foregroundRMS
		} else {
			p.lastSpeechRMS = 0.8*p.lastSpeechRMS + 0.2*foregroundRMS
		}
	}
	referenceRMS := foregroundRMS
	if referenceRMS <= 250 {
		referenceRMS = p.lastSpeechRMS
	}
	if referenceRMS == 0 {
		referenceRMS = 4500
	}

	ambient := make([]float64, len(samples))
	var ambientEnergy float64
	for i := range ambient {
		value := p.sceneSample()
		ambient[i] = value
		ambientEnergy += value * value
	}
	ambientRMS := math.Sqrt(ambientEnergy / float64(len(ambient)))
	targetSNR := voiceAudioSNR(p.spec.Intensity)
	targetAmbientRMS := referenceRMS / math.Pow(10, targetSNR/20)
	scale := 0.0
	if p.spec.Preset != "clean" && ambientRMS > 0 {
		scale = targetAmbientRMS / ambientRMS
	}

	for i := range samples {
		value := float64(samples[i]) + ambient[i]*scale
		if value > math.MaxInt16 {
			value = math.MaxInt16
			p.clippedSamples++
		} else if value < math.MinInt16 {
			value = math.MinInt16
			p.clippedSamples++
		}
		samples[i] = int16(math.Round(value))
	}
	if p.applyCodec && p.spec.Codec == "g711_mulaw" {
		samples = voiceTelephoneMuLawRoundTrip(samples)
	}
	p.processedFrames++
	return pcm16ToBytes(samples)
}

func (p *voiceAudioPipeline) metrics() *VoiceAudioConditionMetrics {
	if p == nil {
		return nil
	}
	codec := p.spec.Codec
	if !p.applyCodec {
		codec = "carrier_g711_mulaw"
	}
	return &VoiceAudioConditionMetrics{
		Preset: p.spec.Preset, Intensity: p.spec.Intensity, Codec: codec, Seed: p.spec.Seed,
		TargetSNRDB: voiceAudioSNR(p.spec.Intensity), ProcessedFrames: p.processedFrames,
		ClippedSamples: p.clippedSamples,
	}
}

func (p *voiceAudioPipeline) sceneSample() float64 {
	const sampleRate = 24000.0
	seconds := float64(p.sample) / sampleRate
	p.sample++
	white := p.nextNoise()
	p.colored = 0.9*p.colored + 0.1*white
	p.babble = 0.96*p.babble + 0.04*p.nextNoise()

	switch p.spec.Preset {
	case "office":
		hum := 0.12*math.Sin(2*math.Pi*120*seconds) + 0.06*math.Sin(2*math.Pi*240*seconds)
		return 0.58*p.colored + hum + 0.25*p.speechLikeBabble(seconds)
	case "cafe":
		clink := pulseAt(seconds, 1.7, 0.025, 0.9) + pulseAt(seconds, 4.1, 0.018, 0.7)
		return 0.34*p.colored + 0.58*p.speechLikeBabble(seconds) + clink
	case "street":
		traffic := 0.28*math.Sin(2*math.Pi*78*seconds) + 0.18*math.Sin(2*math.Pi*135*seconds)
		horn := pulseAt(seconds, 2.6, 0.22, 0.45) * math.Sin(2*math.Pi*410*seconds)
		return 0.52*p.colored + traffic + horn
	case "train_station":
		chime := 0.0
		if phase := math.Mod(seconds, 6); (phase >= 0.65 && phase < 0.85) || (phase >= 0.95 && phase < 1.15) {
			chime = 0.3*math.Sin(2*math.Pi*660*seconds) + 0.14*math.Sin(2*math.Pi*880*seconds)
		}
		rolling := 0.22*math.Sin(2*math.Pi*170*seconds) + 0.14*math.Sin(2*math.Pi*255*seconds)
		return 0.45*p.colored + 0.34*p.speechLikeBabble(seconds) + rolling + chime
	case "poor_phone":
		return 0.82*p.colored + 0.12*math.Sin(2*math.Pi*60*seconds)
	default:
		return 0
	}
}

func (p *voiceAudioPipeline) speechLikeBabble(seconds float64) float64 {
	envelope := 0.55 + 0.25*math.Sin(2*math.Pi*3.1*seconds) + 0.15*math.Sin(2*math.Pi*5.3*seconds)
	carrier := 0.5*math.Sin(2*math.Pi*185*seconds) + 0.3*math.Sin(2*math.Pi*242*seconds)
	return envelope*carrier + 0.45*p.babble
}

func (p *voiceAudioPipeline) nextNoise() float64 {
	p.state = 1664525*p.state + 1013904223
	return (float64(p.state>>8)/float64(1<<24))*2 - 1
}

func voiceAudioSNR(intensity string) float64 {
	switch intensity {
	case "light":
		return 18
	case "heavy":
		return 4
	default:
		return 10
	}
}

func pulseAt(seconds, at, width, gain float64) float64 {
	phase := math.Mod(seconds, 6)
	delta := phase - at
	if delta < 0 || delta >= width {
		return 0
	}
	return gain * math.Exp(-45*delta) * math.Sin(2*math.Pi*1800*delta)
}

func pcmRMS(samples []int16) float64 {
	if len(samples) == 0 {
		return 0
	}
	var energy float64
	for _, sample := range samples {
		value := float64(sample)
		energy += value * value
	}
	return math.Sqrt(energy / float64(len(samples)))
}

func pcm16FromBytes(data []byte) []int16 {
	out := make([]int16, len(data)/2)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
	}
	return out
}

func pcm16ToBytes(samples []int16) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		binary.LittleEndian.PutUint16(out[i*2:], uint16(sample))
	}
	return out
}

func voiceTelephoneMuLawRoundTrip(input []int16) []int16 {
	if len(input) == 0 {
		return nil
	}
	downsampled := make([]int16, 0, (len(input)+2)/3)
	for i := 0; i < len(input); i += 3 {
		end := min(i+3, len(input))
		sum := 0
		for _, sample := range input[i:end] {
			sum += int(sample)
		}
		downsampled = append(downsampled, int16(sum/(end-i)))
	}
	out := make([]int16, 0, len(downsampled)*3)
	for _, sample := range downsampled {
		decoded := voiceMuLawToLinear(voiceLinearToMuLaw(sample))
		out = append(out, decoded, decoded, decoded)
	}
	return out[:min(len(out), len(input))]
}

func voiceLinearToMuLaw(sample int16) byte {
	const bias, clip = 0x84, 32635
	value, sign := int(sample), 0
	if value < 0 {
		sign, value = 0x80, -value
	}
	if value > clip {
		value = clip
	}
	value += bias
	exponent := 7
	for mask := 0x4000; exponent > 0 && value&mask == 0; exponent-- {
		mask >>= 1
	}
	mantissa := (value >> (exponent + 3)) & 0x0f
	return ^byte(sign | exponent<<4 | mantissa)
}

func voiceMuLawToLinear(value byte) int16 {
	value = ^value
	sign, exponent, mantissa := value&0x80, (value>>4)&0x07, value&0x0f
	sample := ((int(mantissa) << 3) + 0x84) << exponent
	sample -= 0x84
	if sign != 0 {
		sample = -sample
	}
	return int16(sample)
}

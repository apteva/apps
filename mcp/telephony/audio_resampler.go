package main

import "math"

// pcmResampler performs stateful, band-limited mono sample-rate conversion.
// It retains adjacent input across carrier frames, avoiding boundary clicks
// and the aliasing caused by sample repetition or unfiltered decimation.
type pcmResampler struct {
	inRate, outRate float64
	step            float64
	cutoff          float64
	half            int
	base            int64
	next            float64
	samples         []float64
}

func newPCMResampler(inRate, outRate int) *pcmResampler {
	const half = 16
	cutoff := 0.94
	if outRate < inRate {
		cutoff *= float64(outRate) / float64(inRate)
	}
	return &pcmResampler{
		inRate: float64(inRate), outRate: float64(outRate),
		step: float64(inRate) / float64(outRate), cutoff: cutoff,
		half: half, base: -half, samples: make([]float64, half),
	}
}

func (r *pcmResampler) Process(input []int16) []int16 {
	if r == nil || len(input) == 0 {
		return nil
	}
	for _, sample := range input {
		r.samples = append(r.samples, float64(sample))
	}
	last := r.base + int64(len(r.samples)) - 1
	estimate := int(math.Ceil(float64(len(input))*r.outRate/r.inRate)) + 2
	out := make([]int16, 0, estimate)
	for r.next+float64(r.half) <= float64(last) {
		center := int64(math.Floor(r.next))
		var sum, weights float64
		for n := center - int64(r.half) + 1; n <= center+int64(r.half); n++ {
			index := n - r.base
			if index < 0 || index >= int64(len(r.samples)) {
				continue
			}
			distance := r.next - float64(n)
			weight := r.cutoff * sinc(r.cutoff*distance) * blackman(distance/float64(r.half))
			sum += r.samples[index] * weight
			weights += weight
		}
		if math.Abs(weights) > 1e-12 {
			sum /= weights
		}
		out = append(out, clampPCM16(sum))
		r.next += r.step
	}

	keepFrom := int64(math.Floor(r.next)) - int64(r.half) - 1
	if drop := keepFrom - r.base; drop > 0 {
		if drop > int64(len(r.samples)) {
			drop = int64(len(r.samples))
		}
		r.samples = append(r.samples[:0], r.samples[drop:]...)
		r.base += drop
	}
	return out
}

func sinc(value float64) float64 {
	if math.Abs(value) < 1e-12 {
		return 1
	}
	value *= math.Pi
	return math.Sin(value) / value
}

func blackman(position float64) float64 {
	if position <= -1 || position >= 1 {
		return 0
	}
	return 0.42 + 0.5*math.Cos(math.Pi*position) + 0.08*math.Cos(2*math.Pi*position)
}

func clampPCM16(value float64) int16 {
	if value > 32767 {
		return 32767
	}
	if value < -32768 {
		return -32768
	}
	return int16(math.Round(value))
}

func resamplePCM(input []int16, inRate, outRate int) []int16 {
	r := newPCMResampler(inRate, outRate)
	padded := append(append([]int16(nil), input...), make([]int16, r.half+1)...)
	out := r.Process(padded)
	want := int(math.Round(float64(len(input)) * float64(outRate) / float64(inRate)))
	if len(out) > want {
		out = out[:want]
	}
	return out
}

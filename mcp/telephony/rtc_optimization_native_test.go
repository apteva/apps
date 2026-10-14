//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"bytes"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestRTCNativeDirectBindingAnd24kFEC(t *testing.T) {
	for _, rate := range []int{24000, 48000} {
		for _, fec := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/fec_%v", rate, fec), func(t *testing.T) {
				selected, e := newRTCNativeEncoderAtRate(32000, fec, rate)
				if e != nil {
					t.Fatal(e)
				}
				defer selected.Close()
				reference, e := newRTCNativeEncoderAtRate(32000, fec, rate)
				if e != nil {
					t.Fatal(e)
				}
				defer reference.Close()
				native := reference.(*rtcNativeEncoder)
				decoder, e := newRTCOpusDecoder()
				if e != nil {
					t.Fatal(e)
				}
				defer decoder.Close()
				pcm := make([]float32, rate/50)
				out, old := make([]byte, 1275), make([]byte, 1275)
				decoded := make([]int16, 480)
				redundant := 0
				for frame := 0; frame < 100; frame++ {
					for i := range pcm {
						at := float64(frame*len(pcm)+i) / float64(rate)
						pcm[i] = float32(.2*math.Sin(2*math.Pi*181*at) + .09*math.Sin(2*math.Pi*543*at))
					}
					n, e := selected.EncodeFloat32(pcm, out)
					if e != nil {
						t.Fatal(e)
					}
					before := native.lib.encode(native.state, pcm, int32(len(pcm)), old, int32(len(old)))
					if before != int32(n) || !bytes.Equal(out[:n], old[:before]) {
						t.Fatal("native binding changed packet", frame)
					}
					if native.lib.hasLBRR != nil && native.lib.hasLBRR(out, int32(n)) == 1 {
						redundant++
					}
					count, e := decoder.decode(out[:n], decoded)
					if e != nil || count != 480 {
						t.Fatal("packet timing/decoder compatibility", count, e)
					}
					if frame > 20 && rmsPCM(decoded) < 500 {
						t.Fatal("voice attenuated")
					}
				}
				if fec && redundant < 10 {
					t.Fatal("24/48k FEC unavailable", redundant)
				}
				state := rtcOpusEncoderState(selected, fec, 32000, 10, time.Now())
				if state.InputRate != rate || state.RTPClockRate != 48000 {
					t.Fatal(state)
				}
				t.Logf("native %d Hz: bit-identical binding; %d redundant packets", rate, redundant)
			})
		}
	}
}
func TestRTCInputRateConfigAndPortableFallback(t *testing.T) {
	for _, s := range []string{"", "24000", "48000"} {
		c, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_encoder_input_rate": s})
		if e != nil || (s == "" && c.InputRate != 48000) {
			t.Fatal(c, e)
		}
	}
	for _, s := range []string{"22050", "0", "48000.0", "garbage"} {
		if _, e := parseSoftphoneRTCConfig(map[string]string{"softphone_webrtc_enabled": "true", "softphone_webrtc_encoder_input_rate": s}); e == nil {
			t.Fatal("invalid input rate", s)
		}
	}
	portable, e := newRTCGoEncoder(32000)
	if e != nil {
		t.Fatal(e)
	}
	defer portable.Close()
	if rtcEncoderInputRate(portable) != 48000 {
		t.Fatal("portable format changed")
	}
	for _, rate := range []int{24000, 48000} {
		main := make([]float32, rate/50)
		for i := range main {
			main[i] = .4
		}
		overlay := newPCMResampler(8000, rate).Process(sinePCM(8000, 410, 160))
		dst := make([]float32, len(main))
		mixed := mixRTCWhisperInto(main, overlay, dst)
		if len(mixed) != len(main) || main[100] != .4 {
			t.Fatal("coaching altered caller PCM")
		}
		for _, v := range mixed {
			if v < -1 || v > 1 {
				t.Fatal("clipping")
			}
		}
	}
}
func BenchmarkRTCOptimizedPipeline(b *testing.B) {
	pcm := sinePCM(24000, 731, 480)
	for _, kind := range []string{"reference_48k", "reused_48k", "direct_24k"} {
		b.Run(kind, func(b *testing.B) {
			rate := 48000
			if kind == "direct_24k" {
				rate = 24000
			}
			codec, e := newRTCNativeEncoderAtRate(32000, true, rate)
			if e != nil {
				b.Fatal(e)
			}
			defer codec.Close()
			native := codec.(*rtcNativeEncoder)
			r := newPCMResampler(24000, rate)
			converted := make([]int16, 0, rate/50+2)
			samples := make([]float32, 0, rate/25)
			out := make([]byte, 1275)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if kind == "reference_48k" {
					converted = r.Process(pcm)
				} else if rate == 24000 {
					converted = append(converted[:0], pcm...)
				} else {
					converted = r.ProcessInto(pcm, converted)
				}
				for _, v := range converted {
					samples = append(samples, float32(v)/32768)
				}
				if len(samples) < rate/50 {
					continue
				}
				if kind == "reference_48k" {
					if n := native.lib.encode(native.state, samples[:960], 960, out, int32(len(out))); n < 0 {
						b.Fatal(n)
					}
				} else {
					if _, e = codec.EncodeFloat32(samples[:rate/50], out); e != nil {
						b.Fatal(e)
					}
				}
				if kind == "reference_48k" {
					samples = samples[960:]
				} else {
					samples = samples[:copy(samples, samples[rate/50:])]
				}
			}
		})
	}
}

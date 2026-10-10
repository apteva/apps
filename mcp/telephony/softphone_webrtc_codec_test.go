package main

import (
	"errors"
	"github.com/pion/opus"
	"math"
	"testing"
)

func TestRTCNativeFailureRetainsPortableCodec(t *testing.T) {
	e, err := selectRTCOpusEncoder(32000, func(int) (rtcOpusEncoder, error) { return nil, errors.New("library unavailable") })
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	if e.capability() != "go_opus_plc" {
		t.Fatal("claimed FEC on the portable fallback")
	}
	pcm, out := make([]float32, 960), make([]byte, 1275)
	if n, err := e.EncodeFloat32(pcm, out); err != nil || n < 1 {
		t.Fatal("fallback stopped audio", n, err)
	}
}

func TestRTCOpusCodecCompatibility(t *testing.T) {
	for _, constructor := range []struct {
		name string
		make func(int) (rtcOpusEncoder, error)
	}{{"portable", newRTCGoEncoder}, {"selected", newRTCOpusEncoder}} {
		t.Run(constructor.name, func(t *testing.T) {
			e, err := constructor.make(32000)
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			d, err := opus.NewDecoderWithOutput(24000, 1)
			if err != nil {
				t.Fatal(err)
			}
			pcm, out, decoded := make([]float32, 960), make([]byte, 1275), make([]int16, 2880)
			for k := 0; k < 20; k++ {
				for i := range pcm {
					pcm[i] = float32(.2 * math.Sin(2*math.Pi*440*float64(k*960+i)/48000))
				}
				n, err := e.EncodeFloat32(pcm, out)
				if err != nil || n < 1 {
					t.Fatal(n, err)
				}
				m, err := d.DecodeToInt16(out[:n], decoded)
				if err != nil || m != 480 {
					t.Fatal(m, err)
				}
			}
			if err := e.configure(16000, 20); err != nil {
				t.Fatal(err)
			}
			if e.capability() == "" {
				t.Fatal("missing codec capability")
			}
		})
	}
}

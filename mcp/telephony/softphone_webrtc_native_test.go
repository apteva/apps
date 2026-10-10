//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"errors"
	"math"
	"testing"
)

func TestRTCNativeFECControlsAndRecovery(t *testing.T) {
	e, err := newRTCNativeEncoder(32000)
	if err != nil {
		t.Skip(err)
	}
	defer e.Close()
	native := e.(*rtcNativeEncoder)
	if native.lib.hasLBRR == nil {
		t.Skip("installed libopus cannot inspect in-band redundancy")
	}
	out := make([]byte, 1275)
	pcm := make([]float32, 960)
	redundant := 0
	var packets [][]byte
	for k := 0; k < 80; k++ {
		for i := range pcm {
			at := float64(k*960+i) / 48000
			pcm[i] = float32(.25*math.Sin(2*math.Pi*180*at) + .08*math.Sin(2*math.Pi*540*at))
		}
		n, err := e.EncodeFloat32(pcm, out)
		if err != nil {
			t.Fatal(err)
		}
		p := append([]byte(nil), out[:n]...)
		packets = append(packets, p)
		if native.lib.hasLBRR(p, int32(n)) == 1 {
			redundant++
		}
	}
	if redundant < 10 {
		t.Fatalf("FEC advertised but no useful redundant packets: %d", redundant)
	}
	d, err := newRTCNativeDecoder()
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	decoded := make([]int16, 480)
	for k, p := range packets {
		var n int
		if k == 20 {
			n, err = d.recover(packets[21], decoded, true)
		} else {
			n, err = d.decode(p, decoded)
		}
		if err != nil || n != 480 {
			t.Fatal(k, n, err)
		}
	}
	t.Logf("Verified %d/%d packets contain in-band redundancy; decoder recovery retains 20 ms timing", redundant, len(packets))
	e.Close()
	if _, err := e.EncodeFloat32(pcm, out); err == nil {
		t.Fatal("closed codec reused")
	}
}

func BenchmarkRTCVoiceEncoding(b *testing.B) {
	for _, factory := range []struct {
		name string
		make func(int) (rtcOpusEncoder, error)
	}{{"portable", newRTCGoEncoder}, {"native_no_fec", func(bitrate int) (rtcOpusEncoder, error) {
		e, err := newRTCNativeEncoder(bitrate)
		if err != nil {
			return nil, err
		}
		native := e.(*rtcNativeEncoder)
		if native.lib.set(native.state, 4012, 0) != 0 {
			e.Close()
			return nil, errors.New("cannot disable FEC for benchmark control")
		}
		return e, nil
	}}, {"native_fec", newRTCNativeEncoder}} {
		b.Run(factory.name, func(b *testing.B) {
			e, err := factory.make(32000)
			if err != nil {
				b.Skip(err)
			}
			defer e.Close()
			pcm := make([]float32, 960)
			out := make([]byte, 1275)
			for i := range pcm {
				pcm[i] = float32(.2 * math.Sin(2*math.Pi*220*float64(i)/48000))
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := e.EncodeFloat32(pcm, out); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

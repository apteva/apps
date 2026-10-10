//go:build (linux || darwin) && (amd64 || arm64)

package main

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/pion/opus"
)

// An opt-in audible reproduction using the exact production encoder factory
// and resampler. This isolates codec/loss repair, not browser jitter or PSTN.
func TestRTCVoiceSpeechDemo(t *testing.T) {
	input, output := os.Getenv("TELEPHONY_SPEECH_INPUT"), os.Getenv("TELEPHONY_SPEECH_OUTPUT")
	if input == "" || output == "" {
		t.Skip("local speech artifact generation is opt-in")
	}
	file, err := os.Open(input)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	info, err := inspectPCM16WAV(file)
	if err != nil {
		t.Fatal(err)
	}
	if info.SampleRate != 24000 || info.Channels != 1 {
		t.Fatal("demo requires 24 kHz mono PCM")
	}
	var frames [][]int16
	var pending []int16
	if err := forEachWAVFrame(file, info, func(samples []int16) error {
		pending = append(pending, samples...)
		for len(pending) >= 480 {
			frames = append(frames, append([]int16(nil), pending[:480]...))
			pending = pending[480:]
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(pending) > 0 {
		frame := make([]int16, 480)
		copy(frame, pending)
		frames = append(frames, frame)
	}
	if len(frames) < 100 {
		t.Fatal("speech clip too short")
	}
	if err := os.MkdirAll(output, 0755); err != nil {
		t.Fatal(err)
	}
	before, err := opus.NewEncoder(opus.WithChannels(1), opus.WithBitrate(32000))
	if err != nil {
		t.Fatal(err)
	}
	after, err := newRTCOpusEncoder(32000)
	if err != nil {
		t.Fatal(err)
	}
	defer after.Close()
	if after.capability() != "libopus_fec" {
		t.Fatal("demo requires actual native FEC; fallback must not claim it")
	}
	encode := func(encoder interface {
		EncodeFloat32([]float32, []byte) (int, error)
	}) [][]byte {
		resampler := newPCMResampler(24000, 48000)
		out := make([]byte, 1275)
		var packets [][]byte
		var pending []float32
		emit := func() {
			n, err := encoder.EncodeFloat32(pending[:960], out)
			if err != nil {
				t.Fatal(err)
			}
			packets = append(packets, append([]byte(nil), out[:n]...))
			pending = pending[960:]
		}
		for _, frame := range frames {
			for _, v := range resampler.Process(frame) {
				pending = append(pending, float32(v)/32768)
			}
			for len(pending) >= 960 {
				emit()
			}
		}
		if len(pending) > 0 {
			pending = append(pending, make([]float32, 960-len(pending))...)
			emit()
		}
		return packets
	}
	oldPackets, newPackets := encode(before), encode(after)
	l, err := loadRTCOpusLibrary()
	if err != nil || l.hasLBRR == nil {
		t.Fatal("demo requires redundancy verification", err)
	}
	lossFrame, energy, redundancy := 0, 0., 0
	for i, p := range newPackets {
		if l.hasLBRR(p, int32(len(p))) == 1 {
			redundancy++
		}
		if i < 75 || i > 225 || i+1 >= len(newPackets) || l.hasLBRR(newPackets[i+1], int32(len(newPackets[i+1]))) != 1 {
			continue
		}
		e := 0.
		for _, v := range frames[i] {
			e += float64(v) * float64(v)
		}
		if e > energy {
			energy = e
			lossFrame = i
		}
	}
	if lossFrame == 0 {
		t.Fatal("no voiced segment with recoverable redundancy")
	}
	decode := func(packets [][]byte, count int, fec bool) []int16 {
		d, err := newRTCNativeDecoder()
		if err != nil {
			t.Fatal(err)
		}
		defer d.Close()
		out := make([]int16, 480)
		var audio []int16
		for i, p := range packets {
			var n int
			if i >= lossFrame && i < lossFrame+count {
				next := lossFrame + count
				if fec && i == next-1 && next < len(packets) {
					n, err = d.recover(packets[next], out, true)
				} else {
					n, err = d.recover(nil, out, false)
				}
			} else {
				n, err = d.decode(p, out)
			}
			if err != nil || n != 480 {
				t.Fatal(i, n, err)
			}
			audio = append(audio, out[:n]...)
		}
		return audio
	}
	write := func(name string, pcm []int16) {
		data := make([]byte, 44+len(pcm)*2)
		copy(data, "RIFF")
		binary.LittleEndian.PutUint32(data[4:], uint32(len(data)-8))
		copy(data[8:], "WAVEfmt ")
		binary.LittleEndian.PutUint32(data[16:], 16)
		binary.LittleEndian.PutUint16(data[20:], 1)
		binary.LittleEndian.PutUint16(data[22:], 1)
		binary.LittleEndian.PutUint32(data[24:], 24000)
		binary.LittleEndian.PutUint32(data[28:], 48000)
		binary.LittleEndian.PutUint16(data[32:], 2)
		binary.LittleEndian.PutUint16(data[34:], 16)
		copy(data[36:], "data")
		binary.LittleEndian.PutUint32(data[40:], uint32(len(pcm)*2))
		for i, v := range pcm {
			binary.LittleEndian.PutUint16(data[44+i*2:], uint16(v))
		}
		if err := os.WriteFile(filepath.Join(output, name), data, 0644); err != nil {
			t.Fatal(err)
		}
	}
	clean := decode(newPackets, 0, false)
	write("clean.wav", clean)
	results := map[string]any{"source": "local synthesized French speech", "codec_capability": after.capability(), "frame_ms": 20, "bitrate_bps": 32000, "outage_seconds": float64(lossFrame) * .02, "packets": len(newPackets), "packets_with_redundancy": redundancy, "scope": "production codec factory and PCM resampler; offline loss replay, not a full browser call", "selection": "same voiced loss position in every clip, selected where next packet contains FEC"}
	for _, count := range []int{1, 6, 30} {
		write("before-"+demoInt(count*20)+"ms.wav", decode(oldPackets, count, false))
		write("after-"+demoInt(count*20)+"ms.wav", decode(newPackets, count, true))
	}
	// Same-codec ablation isolates repair from the change of encoder.
	plc, repaired := decode(newPackets, 1, false), decode(newPackets, 1, true)
	write("same-codec-plc-20ms.wav", plc)
	mse := func(a, b []int16) float64 {
		sum := 0.
		for i := lossFrame * 480; i < (lossFrame+1)*480; i++ {
			d := float64(a[i]) - float64(b[i])
			sum += d * d
		}
		return sum / 480
	}
	plcMSE, fecMSE := mse(clean, plc), mse(clean, repaired)
	results["same_codec_lost_frame_plc_mse"] = plcMSE
	results["same_codec_lost_frame_fec_mse"] = fecMSE
	results["lost_frame_mse_improvement_db"] = 10 * math.Log10(plcMSE/fecMSE)
	if fecMSE >= plcMSE {
		t.Fatal("selected voiced sample did not improve with actual FEC", plcMSE, fecMSE)
	}
	raw, _ := json.MarshalIndent(results, "", "  ")
	if err := os.WriteFile(filepath.Join(output, "results.json"), raw, 0644); err != nil {
		t.Fatal(err)
	}
	t.Log(string(raw))
}

func demoInt(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

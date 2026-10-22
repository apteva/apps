package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// Compile an offline, pinned, BSD-licensed mature packet jitter buffer. This
// process is test-only; production builds keep their CGO-disabled portability.
func TestSIPMatureJitterReferenceCapturedSpeech(t *testing.T) {
	cc, err := exec.LookPath("cc")
	if err != nil {
		t.Skip("C compiler required for the test-only SpeexDSP reference")
	}
	root := "testdata/sip/speexdsp-reference"
	bin := filepath.Join(t.TempDir(), "speex-jitter-reference")
	build := exec.CommandContext(t.Context(), cc, "-O2", "-DFLOATING_POINT", "-DEXPORT=", "-I"+root+"/include", "-I"+root+"/libspeexdsp", root+"/replay.c", root+"/libspeexdsp/jitter.c", "-o", bin)
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build mature reference: %v %s", e, out)
	}
	var trace sipCapturedTiming
	raw, err := os.ReadFile("testdata/sip/variable-arrival-20261022.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &trace); err != nil {
		t.Fatal(err)
	}
	last := trace.Packets[len(trace.Packets)-1]
	for i := int64(1); i <= 250; i++ {
		trace.Packets = append(trace.Packets, [3]int64{last[0] + i*int64(20*time.Millisecond), last[1] + i, last[2] + i*160})
	}
	var input bytes.Buffer
	fmt.Fprintln(&input, len(trace.Packets))
	for _, p := range trace.Packets {
		fmt.Fprintln(&input, p[0], p[1], p[2])
	}
	run := exec.CommandContext(t.Context(), bin)
	run.Stdin = &input
	result, err := run.Output()
	if err != nil {
		t.Fatal("run reference", err)
	}
	for _, codec := range []string{"PCMA", "PCMU", "G722"} {
		t.Run(codec, func(t *testing.T) {
			offer := sipMediaOffer{Codec: codec, PacketSamples: 160}
			enc, decoder, referenceDecoder := &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}, &sipRTPMedia{offer: offer}
			source := loadSIPSpeech(t, sipSpeechFixtures[0].id, sipSpeechFixtures[0].hash, decoder.sampleRate())
			payloads := map[uint32][]byte{}
			var reference []int16
			for _, p := range trace.Packets {
				start := int(p[2]) * decoder.sampleRate() / 8000
				pcm := make([]int16, decoder.sampleRate()/50)
				for i := range pcm {
					pcm[i] = source[(start+i)%len(source)]
				}
				encoded := enc.encodeAudio(pcm)
				payloads[uint32(p[1])] = encoded
				decoded := referenceDecoder.decodeAudio(encoded)
				for len(reference) < start+len(decoded) {
					reference = append(reference, 0)
				}
				copy(reference[start:], decoded)
			}
			var output []sipSpeechFrame
			var positions []int
			var plc sipPCMConcealer
			scanner := bufio.NewScanner(bytes.NewReader(result))
			real := 0
			for scanner.Scan() {
				var at int64
				var status, offset int
				var seq, ts uint32
				if _, e := fmt.Sscan(scanner.Text(), &at, &status, &seq, &ts, &offset); e != nil {
					t.Fatal(e)
				}
				var pcm []int16
				if status == 0 {
					pcm = plc.received(decoder.decodeAudio(payloads[seq]), decoder.sampleRate())
					real++
				} else {
					pcm = plc.missing(decoder.sampleRate()/50, decoder.sampleRate(), true)
				}
				if len(pcm) != decoder.sampleRate()/50 {
					t.Fatal("invalid reference output frame")
				}
				output = append(output, sipSpeechFrame{time.Duration(at), pcm, len(pcm)})
				positions = append(positions, int(ts)*decoder.sampleRate()/8000)
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if real == 0 {
				t.Fatal("reference never rendered input")
			}
			speech := analyzeSIPSpeech(reference, decoder.sampleRate(), output, positions, 40-trace.InitialAgeMS)
			speech.Accepted = (sipSpeechBudget{coverage: .99, gapMS: 80, delayMS: 100}).accepts(speech)
			if speech.ActiveSpeechCoverage < .95 || speech.LongestDamagedSpeechMS > 100 || speech.MaxAddedDelayMS > 500 {
				t.Fatalf("mature reference integrity/quality regression: %+v", speech)
			}
			t.Logf("SpeexDSP 1.2.1 / shared %s decoder+PLC / own capacity policy: %+v", codec, speech)
		})
	}
}

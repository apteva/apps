package main

import (
	"context"
	"encoding/binary"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Pure-function unit tests — no external deps.

func TestParseDurationMs(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"0", 0},
		{"1.5", 1500},
		{"42.123", 42123},
		{"garbage", 0},
	}
	for _, c := range cases {
		if got := parseDurationMs(c.in); got != c.want {
			t.Errorf("parseDurationMs(%q)=%d want %d", c.in, got, c.want)
		}
	}
}

func TestParseRational(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"", 0},
		{"30", 30},
		{"30/1", 30},
		{"30000/1001", 29.97002997002997},
		{"0/0", 0},     // den==0 — graceful zero
		{"garbage", 0}, // bad input — graceful zero
	}
	for _, c := range cases {
		got := parseRational(c.in)
		if abs(got-c.want) > 0.001 {
			t.Errorf("parseRational(%q)=%v want %v", c.in, got, c.want)
		}
	}
}

func abs(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

func TestIsImageCodec(t *testing.T) {
	for _, c := range []string{"mjpeg", "png", "gif", "webp"} {
		if !isImageCodec(c) {
			t.Errorf("%s should be an image codec", c)
		}
	}
	for _, c := range []string{"h264", "vp9", "av1", "hevc"} {
		if isImageCodec(c) {
			t.Errorf("%s should NOT be an image codec", c)
		}
	}
}

// runProbe end-to-end against a hand-crafted silent WAV. WAV's
// header format is well-defined enough to write 60 bytes by hand —
// no ffmpeg required to produce the fixture, only ffprobe to read
// it (and we skip the test if ffprobe isn't on PATH).
func TestRunProbe_Wav(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "silent.wav")
	if err := writeSilentWav(path, 100); err != nil {
		t.Fatalf("writeSilentWav: %v", err)
	}
	probe, err := runProbe(context.Background(), "ffprobe", path)
	if err != nil {
		t.Fatalf("runProbe: %v", err)
	}
	if !probe.HasAudio {
		t.Errorf("expected has_audio, got probe=%+v", probe)
	}
	if probe.HasVideo {
		t.Errorf("unexpected has_video for an audio-only WAV")
	}
	if probe.SampleRate != 8000 {
		t.Errorf("sample_rate=%d want 8000", probe.SampleRate)
	}
	if probe.Channels != 1 {
		t.Errorf("channels=%d want 1", probe.Channels)
	}
	if probe.DurationMs < 50 || probe.DurationMs > 200 {
		t.Errorf("duration_ms=%d want ~100", probe.DurationMs)
	}
	if probe.Raw == "" {
		t.Errorf("raw probe empty")
	}
	if !strings.Contains(probe.FormatName, "wav") {
		t.Logf("format_name=%q (this is informational, not a hard failure)", probe.FormatName)
	}
}

func TestRunProbe_BadFile(t *testing.T) {
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH — skipping")
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "garbage.bin")
	if err := os.WriteFile(path, []byte("not a media file"), 0644); err != nil {
		t.Fatal(err)
	}
	probe, err := runProbe(context.Background(), "ffprobe", path)
	// ffprobe may either error out or return a zero-stream Probe —
	// the indexer treats both as unsupported. We accept both shapes
	// so the test isn't tied to a specific ffprobe build's behaviour.
	if err == nil && (probe.HasAudio || probe.HasVideo || probe.IsImage) {
		t.Errorf("expected unsupported result, got %+v", probe)
	}
}

// writeSilentWav writes a mono 8kHz 8-bit PCM WAV file of `ms`
// milliseconds of silence. Hand-crafted because it's faster than
// shelling out to ffmpeg in test setup and removes one tooling dep
// from the test path.
func writeSilentWav(path string, ms int) error {
	const sampleRate = 8000
	const bitsPerSample = 8
	const channels = 1
	samples := sampleRate * ms / 1000
	dataSize := samples * channels * (bitsPerSample / 8)

	buf := make([]byte, 0, 44+dataSize)
	// RIFF header
	buf = append(buf, []byte("RIFF")...)
	buf = appendU32LE(buf, uint32(36+dataSize))
	buf = append(buf, []byte("WAVE")...)
	// fmt chunk
	buf = append(buf, []byte("fmt ")...)
	buf = appendU32LE(buf, 16)        // chunk size
	buf = appendU16LE(buf, 1)         // PCM
	buf = appendU16LE(buf, channels)  // channels
	buf = appendU32LE(buf, sampleRate)
	buf = appendU32LE(buf, sampleRate*channels*bitsPerSample/8)         // byte rate
	buf = appendU16LE(buf, uint16(channels*bitsPerSample/8))            // block align
	buf = appendU16LE(buf, bitsPerSample)
	// data chunk
	buf = append(buf, []byte("data")...)
	buf = appendU32LE(buf, uint32(dataSize))
	// 8-bit unsigned PCM silence is 0x80 (mid-rail), not 0x00 — use
	// the right value or some decoders flag it as a "click."
	for i := 0; i < dataSize; i++ {
		buf = append(buf, 0x80)
	}
	return os.WriteFile(path, buf, 0644)
}

func appendU32LE(b []byte, v uint32) []byte {
	var x [4]byte
	binary.LittleEndian.PutUint32(x[:], v)
	return append(b, x[:]...)
}

func appendU16LE(b []byte, v uint16) []byte {
	var x [2]byte
	binary.LittleEndian.PutUint16(x[:], v)
	return append(b, x[:]...)
}

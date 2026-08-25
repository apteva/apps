package main

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func floatClose(got *float64, want float64) bool {
	if got == nil {
		return false
	}
	d := *got - want
	if d < 0 {
		d = -d
	}
	return d < 0.01
}

func TestParseAudioAnalysis(t *testing.T) {
	log := `
  Integrated loudness:
    I:         -17.8 LUFS
  Loudness range:
    LRA:         5.2 LU
  True peak:
    Peak:       -0.2 dBFS
[Parsed_astats_1] Overall
[Parsed_astats_1] DC offset: 0.001000
[Parsed_astats_1] Peak level dB: -0.400000
[Parsed_astats_1] RMS level dB: -21.300000
[silencedetect] silence_start: 1.5
[silencedetect] silence_end: 3.9 | silence_duration: 2.4
`
	opts := analysisOptions{StartMs: 10_000, EndMs: 70_000, SilenceThresholdDB: -50, SilenceMinMs: 1_000}
	got := parseAudioAnalysis(log, opts)
	if !floatClose(got.IntegratedLUFS, -17.8) || !floatClose(got.LoudnessRangeLU, 5.2) {
		t.Fatalf("loudness not parsed: %+v", got)
	}
	if !floatClose(got.MaxTruePeakDBTP, -0.2) || !floatClose(got.RMSDBFS, -21.3) {
		t.Fatalf("peaks not parsed: %+v", got)
	}
	if got.ClippingDetected {
		t.Fatal("-0.2 dBTP should remain below the -0.1 dB clipping-risk threshold")
	}
	if len(got.SilenceSegments) != 1 || got.SilenceSegments[0].StartMs != 11_500 || got.SilenceSegments[0].EndMs != 13_900 {
		t.Fatalf("silence segments=%+v", got.SilenceSegments)
	}
	if got.SilenceTotalMs != 2_400 || got.LongestSilenceMs != 2_400 {
		t.Fatalf("silence totals=%+v", got)
	}
}

func TestParseVisualAnalysis(t *testing.T) {
	log := `
[metadata] frame:0 pts:0 pts_time:0
[metadata] lavfi.signalstats.YMIN=16
[metadata] lavfi.signalstats.YAVG=30
[metadata] lavfi.signalstats.YMAX=220
[metadata] lavfi.signalstats.SATAVG=40
[metadata] lavfi.blur=3.5
[metadata] lavfi.block=1.25
[metadata] frame:1 pts:1 pts_time:5
[metadata] lavfi.signalstats.YMIN=20
[metadata] lavfi.signalstats.YAVG=50
[metadata] lavfi.signalstats.YMAX=235
[metadata] lavfi.signalstats.SATAVG=60
[metadata] lavfi.blur=4.5
[metadata] lavfi.block=1.75
[blackdetect] black_start:2 black_end:4.5 black_duration:2.5
[freezedetect] lavfi.freezedetect.freeze_start: 6
[freezedetect] lavfi.freezedetect.freeze_duration: 3
[freezedetect] lavfi.freezedetect.freeze_end: 9
`
	got := parseVisualAnalysis(log, 10_000, 20_000)
	if got.SampledFrames != 2 || !floatClose(got.MeanLuma, 40) || !floatClose(got.MeanBlurScore, 4) {
		t.Fatalf("visual metrics=%+v", got)
	}
	if len(got.BlackSegments) != 1 || got.BlackSegments[0].StartMs != 12_000 || got.BlackSegments[0].EndMs != 14_500 {
		t.Fatalf("black segments=%+v", got.BlackSegments)
	}
	if len(got.FrozenSegments) != 1 || got.FrozenSegments[0].StartMs != 16_000 || got.FrozenSegments[0].EndMs != 19_000 {
		t.Fatalf("frozen segments=%+v", got.FrozenSegments)
	}
}

func TestBaseAnalysisResultIncludesEncodingDetails(t *testing.T) {
	row := &MediaRow{
		FileID: "9", FormatName: "mov,mp4", DurationMs: 120_000, Bitrate: 5_000_000,
		HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 29.97,
		VideoCodec: "h264", AudioCodec: "aac", Channels: 2, SampleRate: 48_000,
		RawProbe: json.RawMessage(`{"streams":[{"index":0,"codec_type":"video","codec_name":"h264","profile":"High","pix_fmt":"yuv420p","color_space":"bt709","width":1920,"height":1080},{"index":1,"codec_type":"audio","codec_name":"aac","sample_fmt":"fltp","sample_rate":"48000","channels":2}]}`),
	}
	got := baseAnalysisResult(row, analysisOptions{Depth: "standard", EndMs: 60_000})
	if got.MediaType != "video" || got.Analysis.Complete || got.Analysis.Ratio != 0.5 {
		t.Fatalf("analysis envelope=%+v", got)
	}
	streams, ok := got.Technical["streams"].([]map[string]any)
	if !ok || len(streams) != 2 || streams[0]["pixel_format"] != "yuv420p" || streams[1]["sample_rate_hz"] != "48000" {
		t.Fatalf("technical streams=%#v", got.Technical["streams"])
	}
	if len(got.Issues) != 1 || got.Issues[0].Code != "PARTIAL_ANALYSIS" {
		t.Fatalf("issues=%+v", got.Issues)
	}
}

func TestAnalysisCommandsAreReadOnlyNullOutputs(t *testing.T) {
	row := &MediaRow{HasVideo: true, HasAudio: true}
	opts := analysisOptions{StartMs: 1_000, EndMs: 11_000, SilenceThresholdDB: -50, SilenceMinMs: 1_000}
	for name, args := range map[string][]string{
		"visual": visualAnalysisArgs("https://example.test/source.mp4", row, opts),
		"audio":  audioAnalysisArgs("https://example.test/source.mp4", opts),
	} {
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-f null -") {
			t.Fatalf("%s command is not null-output: %s", name, joined)
		}
		for _, forbidden := range []string{"-y", "/renders/", ".jpg", ".wav"} {
			if strings.Contains(joined, forbidden) {
				t.Fatalf("%s command contains output marker %q: %s", name, forbidden, joined)
			}
		}
	}
}

func TestAnalyzeExistingSourceWithFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	source := filepath.Join(t.TempDir(), "sample.mp4")
	cmd := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=c=blue:s=320x240:d=3",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=3",
		"-shortest", "-c:v", "mpeg4", "-c:a", "aac", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("create fixture: %v: %s", err, out)
	}
	ctx := newTestCtx(t)
	row := &MediaRow{FileID: "1", HasVideo: true, HasAudio: true, DurationMs: 3_000}
	opts := analysisOptions{
		Depth: "full", EndMs: 3_000, SilenceThresholdDB: -50,
		SilenceMinMs: 1_000, Timeout: 30 * time.Second,
	}
	got, err := analyzeExistingSource(ctx, source, row, opts)
	if err != nil {
		t.Fatalf("analyzeExistingSource: %v", err)
	}
	if !got.DecodeOK || got.Visual == nil || got.Visual.SampledFrames == 0 {
		t.Fatalf("visual analysis incomplete: %+v", got)
	}
	if got.Audio == nil || got.Audio.IntegratedLUFS == nil || got.Audio.MaxTruePeakDBTP == nil {
		t.Fatalf("audio analysis incomplete: %+v", got.Audio)
	}
	// The deliberately static video remains frozen through EOF. This
	// proves terminal freeze_start events are closed at the range end.
	if len(got.Visual.FrozenSegments) != 1 || got.Visual.FrozenSegments[0].EndMs != 3_000 {
		t.Fatalf("terminal freeze not reported: %+v", got.Visual.FrozenSegments)
	}
}

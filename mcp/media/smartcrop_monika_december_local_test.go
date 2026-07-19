package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const (
	monikaDecemberPlayingInstrumentsSHA256 = "e421dea5ed7d7c054f744c027eef775676dcef3a66ebed04ea77f58b0d3a5544"
	monikaDecemberResistSHA256             = "e3b0d499ee3a507c86ffe4e72c097359c6506d24f8fcb7ed0c24c27776b32c1c"
)

// TestMonikaDecemberLocalRegression covers the production-identical sources
// that exposed an upright-to-horizontal tracking failure. It is opt-in because
// the private source files are intentionally kept outside the repository.
func TestMonikaDecemberLocalRegression(t *testing.T) {
	playing := os.Getenv("MONIKA_DECEMBER_PLAYING_VIDEO")
	resist := os.Getenv("MONIKA_DECEMBER_RESIST_VIDEO")
	if playing == "" || resist == "" {
		t.Skip("set MONIKA_DECEMBER_PLAYING_VIDEO and MONIKA_DECEMBER_RESIST_VIDEO")
	}
	assertFileSHA256(t, playing, monikaDecemberPlayingInstrumentsSHA256)
	assertFileSHA256(t, resist, monikaDecemberResistSHA256)

	t.Run("first-sleep-cue-still", func(t *testing.T) {
		x := localSmartCropStillX(t, resist, 725_033, 436_680, 1920, 1080)
		t.Logf("crop x=%d", x)
		if x < 1_200 || x > 1_420 {
			t.Errorf("reclining face lacks a safe portrait margin: x=%d want [1200,1420]", x)
		}
		if outputDir := os.Getenv("MONIKA_DECEMBER_OUTPUT_DIR"); outputDir != "" {
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			runFFmpeg(t, "-ss", secondsString(436_680), "-i", resist, "-frames:v", "1",
				"-vf", fmt.Sprintf("crop=606:1076:%d:0", x), "-y", filepath.Join(outputDir, "first-sleep-cue.png"))
		}
	})

	tests := []struct {
		name       string
		video      string
		duration   int64
		start, end int64
		checks     []struct {
			at, min, max int64
		}
	}{
		{
			name: "guitar-response", video: playing, duration: 487_700,
			start: 339_944, end: 369_080,
			checks: []struct{ at, min, max int64 }{
				{360_444, 0, 420}, {362_000, 0, 380},
			},
		},
		{
			name: "first-resistance", video: resist, duration: 725_033,
			start: 429_975, end: 461_085,
			checks: []struct{ at, min, max int64 }{
				{440_000, 1_150, 1_314}, {445_000, 1_150, 1_314},
				{450_000, 1_150, 1_314}, {455_000, 1_150, 1_314},
			},
		},
		{
			name: "second-sleep", video: resist, duration: 725_033,
			start: 500_199, end: 542_065,
			checks: []struct{ at, min, max int64 }{
				{524_000, 0, 340}, {530_000, 0, 260}, {536_000, 0, 300},
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := localSmartCropReelPath(t, tc.video, tc.duration, tc.start, tc.end, 1920, 1080)
			t.Logf("crop path: %v", path)
			for _, check := range tc.checks {
				x, ok := mariaInterpolatedPathX(path, check.at)
				if !ok {
					t.Errorf("no crop at %dms: %v", check.at, path)
					continue
				}
				if int64(x) < check.min || int64(x) > check.max {
					t.Errorf("face lacks safe margin at %dms: x=%d want [%d,%d]", check.at, x, check.min, check.max)
				}
			}
			if outputDir := os.Getenv("MONIKA_DECEMBER_OUTPUT_DIR"); outputDir != "" {
				if err := os.MkdirAll(outputDir, 0o755); err != nil {
					t.Fatal(err)
				}
				filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1076, 0, tc.start, path) +
					",scale=1080:1920,setsar=1"
				runFFmpeg(t, "-ss", secondsString(tc.start), "-i", tc.video,
					"-t", secondsString(tc.end-tc.start), "-vf", filter, "-an",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
					filepath.Join(outputDir, tc.name+".mp4"))
			}
		})
	}
}

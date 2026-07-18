package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// These production-identical sources cover long, nearly motionless stretches
// where generic saliency previously parked the portrait crop on the vanity.
// They remain opt-in because the three 4K source videos are too large for git.
func TestMariaExtendedLocalRegression(t *testing.T) {
	root := os.Getenv("MARIA_EXTENDED_FIXTURE_DIR")
	if root == "" {
		t.Skip("set MARIA_EXTENDED_FIXTURE_DIR to run the extended Maria regressions")
	}

	fixtures := []struct {
		name, file, sha256   string
		duration, start, end int64
		minX, maxX           int
	}{
		{
			name: "loop-work-answer", file: "10942-loop.mp4",
			sha256:   "868553a3bd0f3fa92260e1e0146ecbf9abb36cfbe04c9c638937c127c7d03374",
			duration: 445_333, start: 181_565, end: 212_135,
			minX: 700, maxX: 1_900,
		},
		{
			name: "resist-first-drop", file: "11229-resist.mp4",
			sha256:   "199cbd2d76122479bb1ece33d37b699e8722ac9788500963cc8a663e532a0eb8",
			duration: 514_266, start: 299_605, end: 327_065,
			minX: 700, maxX: 1_300,
		},
		{
			name: "resist-final-drop", file: "11229-resist.mp4",
			sha256:   "199cbd2d76122479bb1ece33d37b699e8722ac9788500963cc8a663e532a0eb8",
			duration: 514_266, start: 455_480, end: 499_225,
			minX: 700, maxX: 1_300,
		},
		{
			name: "unduction-first-sleep", file: "11387-unduction.mp4",
			sha256:   "4a5f75d061659cdee864fe725a433e9db6c408d12560762887743a12debf7982",
			duration: 532_733, start: 205_790, end: 246_240,
			minX: 800, maxX: 1_400,
		},
		{
			name: "unduction-wake-and-sleep", file: "11387-unduction.mp4",
			sha256:   "4a5f75d061659cdee864fe725a433e9db6c408d12560762887743a12debf7982",
			duration: 532_733, start: 311_155, end: 354_480,
			minX: 800, maxX: 1_400,
		},
		{
			name: "unduction-fractionation", file: "11387-unduction.mp4",
			sha256:   "4a5f75d061659cdee864fe725a433e9db6c408d12560762887743a12debf7982",
			duration: 532_733, start: 416_900, end: 451_340,
			minX: 700, maxX: 1_400,
		},
	}

	verified := make(map[string]bool)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			video := filepath.Join(root, fixture.file)
			if !verified[video] {
				assertFileSHA256(t, video, fixture.sha256)
				verified[video] = true
			}
			path := mariaReelPath(t, video, fixture.duration, fixture.start, fixture.end)
			t.Logf("crop path: %v", path)
			if len(path) == 0 {
				t.Fatal("empty crop path")
			}
			for _, point := range path {
				if point.X < fixture.minX || point.X > fixture.maxX {
					t.Errorf("subject lost at %dms: crop x=%d; path=%v", point.AtMs, point.X, path)
				}
			}
			if outputDir := os.Getenv("MARIA_EXTENDED_OUTPUT_DIR"); outputDir != "" {
				if err := os.MkdirAll(outputDir, 0o755); err != nil {
					t.Fatal(err)
				}
				filter := "setpts=PTS-STARTPTS," + cropFilterForPath(1214, 2158, 0, fixture.start, path) +
					",scale=1080:1920,setsar=1"
				runFFmpeg(t, "-ss", secondsString(fixture.start), "-i", video,
					"-t", secondsString(fixture.end-fixture.start), "-vf", filter, "-an",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
					filepath.Join(outputDir, fixture.name+".mp4"))
			}
			if t.Failed() {
				t.Logf("%s path: %s", fixture.name, fmt.Sprint(path))
			}
		})
	}
}

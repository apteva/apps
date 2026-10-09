package main

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
)

func TestReelHEVCAndSilentSourceTimeline(t *testing.T) {
	skipIfNoFFmpeg(t)
	source := guardedFixture(t, true)
	for _, silent := range []bool{false, true} {
		t.Run(map[bool]string{false: "audio", true: "silent"}[silent], func(t *testing.T) {
			src := source
			if silent {
				src = filepath.Join(t.TempDir(), "silent.mov")
				if out, err := exec.Command("ffmpeg", "-y", "-v", "error", "-i", source, "-map", "0:v:0", "-c", "copy", src).CombinedOutput(); err != nil {
					t.Fatalf("silent fixture: %v %s", err, out)
				}
			}
			out := runOpAgainstFile(t, "extract_reel", []string{src}, map[string]any{"start_ms": 1017, "end_ms": 2533, "target_ratio": "1:1", "output_width": 64}, "reel.mp4", t.TempDir())
			log, err := runCompactedFFmpeg(context.Background(), "ffmpeg", trimValidationArgs(out))
			if err != nil {
				t.Fatal(err)
			}
			validation := parseTrimValidation(log)
			if err := checkTrimValidation(validation, 1516); err != nil {
				t.Fatal(err)
			}
			if validation.Video == nil || validation.Video.FramesChecked != 45 || validation.Video.FirstFrameMs != 0 || validation.Video.OpeningBlack || validation.Video.EndingBlack {
				t.Fatalf("video=%+v", validation.Video)
			}
			if silent != (validation.AudioFirstMs == nil) {
				t.Fatalf("unexpected audio: %+v", validation)
			}
			meta, err := exec.Command("ffprobe", "-v", "error", "-select_streams", "v:0", "-show_entries", "stream=duration", "-of", "json", out).Output()
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Streams []struct {
					Duration string `json:"duration"`
				} `json:"streams"`
			}
			if err := json.Unmarshal(meta, &probe); err != nil || len(probe.Streams) != 1 {
				t.Fatalf("probe=%s err=%v", meta, err)
			}
			end, err := strconv.ParseFloat(probe.Streams[0].Duration, 64)
			if err != nil || math.Abs(end-1.516) > 0.002 {
				t.Fatalf("ending picture does not cover requested endpoint: %s", meta)
			}
			// Run the exact remote pre-upload gate against the finished encoded file.
			cmd := exec.Command("bash", "-c", trimValidationScript("ffmpeg", out, 1516))
			cmd.Dir = t.TempDir()
			if log, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("remote validation: %v %s", err, log)
			}
		})
	}
}

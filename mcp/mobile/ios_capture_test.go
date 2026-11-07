package main

import (
	"context"
	"encoding/json"
	"math"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Exercise the embedded Swift writer with backward device-clock jumps and a
// static tail. This needs neither a phone nor Camera permission.
func TestIOSCaptureMonotonicTimeline(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("AVFoundation is macOS only")
	}
	if _, err := exec.LookPath("xcrun"); err != nil {
		t.Skip("Xcode tools unavailable")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	d := newDrivers(runnerConfig{ToolDir: t.TempDir()})
	bin, err := d.captureHelper(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, audio := range []bool{false, true} {
		name := "video-only-static-tail"
		if audio {
			name = "audio-and-video-clock-jumps"
		}
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "timing.mov")
			args := []string{"selftest", "--out", path, "--seconds", "18"}
			if !audio {
				args = append(args, "--no-audio")
			}
			output, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
			if err != nil {
				t.Fatalf("writer fixture: %v: %s", err, output)
			}
			if !strings.Contains(string(output), "READY\n") || !strings.Contains(string(output), "DONE\n") {
				t.Fatalf("incomplete writer lifecycle: %s", output)
			}
			raw, err := exec.CommandContext(ctx, "ffprobe", "-v", "error", "-show_streams", "-show_format", "-show_packets", "-show_entries", "stream=index,codec_type,duration,start_time:format=duration:packet=stream_index,dts_time", "-of", "json", path).Output()
			if err != nil {
				t.Fatal(err)
			}
			var probe struct {
				Format struct {
					Duration string `json:"duration"`
				} `json:"format"`
				Streams []struct {
					Type     string `json:"codec_type"`
					Duration string `json:"duration"`
					Start    string `json:"start_time"`
				} `json:"streams"`
				Packets []struct {
					Stream int    `json:"stream_index"`
					DTS    string `json:"dts_time"`
				} `json:"packets"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatal(err)
			}
			duration, _ := strconv.ParseFloat(probe.Format.Duration, 64)
			if math.Abs(duration-18) > .05 {
				t.Fatalf("18-second capture became %.3f seconds", duration)
			}
			wantStreams := 1
			if audio {
				wantStreams = 2
			}
			if len(probe.Streams) != wantStreams {
				t.Fatalf("got %d streams, want %d", len(probe.Streams), wantStreams)
			}
			for _, s := range probe.Streams {
				duration, _ := strconv.ParseFloat(s.Duration, 64)
				start, _ := strconv.ParseFloat(s.Start, 64)
				if math.Abs(duration-18) > .05 || math.Abs(start) > .05 {
					t.Errorf("%s timing: start %.3f, duration %.3f", s.Type, start, duration)
				}
			}
			previous := map[int]float64{}
			for _, p := range probe.Packets {
				dts, err := strconv.ParseFloat(p.DTS, 64)
				if err != nil {
					t.Fatal(err)
				}
				if last, ok := previous[p.Stream]; ok && dts <= last {
					t.Fatalf("stream %d timestamp went backward or repeated: %.6f -> %.6f", p.Stream, last, dts)
				}
				previous[p.Stream] = dts
			}
		})
	}
}

//go:build integration

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// Opt-in, local synthetic media only. Alternating profiles and fixed thread
// budgets make results reviewable; this is not a production-host benchmark.
func TestRenderBenchmark(t *testing.T) {
	if os.Getenv("RUN_MEDIA_BENCHMARK") != "1" {
		t.Skip("set RUN_MEDIA_BENCHMARK=1")
	}
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	source := filepath.Join(root, "source.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=1920x1080:rate=30", "-f", "lavfi", "-i", "sine=frequency=440:sample_rate=48000", "-t", "4", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-threads", "2", "-c:a", "aac", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fixture: %v %s", err, out)
	}
	type measurement struct {
		Operation   string  `json:"operation"`
		Profile     string  `json:"profile"`
		Concurrency int     `json:"concurrency"`
		WallMs      int64   `json:"wall_ms"`
		UserMs      int64   `json:"user_ms"`
		SystemMs    int64   `json:"system_ms"`
		Bytes       int64   `json:"bytes"`
		SSIM        float64 `json:"ssim,omitempty"`
	}
	var measurements []measurement
	var mu sync.Mutex
	run := func(op, profile string, concurrency, index int) error {
		params := map[string]any{"encoder_profile": profile}
		sources := []string{"1"}
		name := fmt.Sprintf("%s-%s-%d-%d.mp4", op, profile, concurrency, index)
		switch op {
		case "transcode":
			params["format"] = "mp4"
		case "extract_reel":
			params["start_ms"] = 0
			params["end_ms"] = 3000
			params["target_ratio"] = "9:16"
			params["crop_mode"] = "center"
			params["output_width"] = 360
		case "extract_frame":
			params["at_ms"] = 1500
			name = strings.TrimSuffix(name, ".mp4") + ".jpg"
		case "concat":
			sources = append(sources, "2")
		}
		raw, _ := json.Marshal(params)
		plan, err := buildPlan(op, sources, raw, name, ".mp4")
		if err != nil {
			return err
		}
		inputs := []string{source}
		if op == "concat" {
			inputs = append(inputs, source)
		}
		job := filepath.Join(root, strings.TrimSuffix(name, filepath.Ext(name)))
		_ = os.Mkdir(job, 0700)
		args, err := materialiseArgs(plan.Args, inputs, job)
		if err != nil {
			return err
		}
		output := filepath.Join(job, name)
		args = append([]string{"-filter_threads", "1", "-filter_complex_threads", "1"}, args...)
		args = append(args, "-threads", "2", output)
		start := time.Now()
		cmd := exec.Command(ffmpeg, args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s: %w %s", op, err, out)
		}
		stat, err := os.Stat(output)
		if err != nil {
			return err
		}
		m := measurement{op, profile, concurrency, time.Since(start).Milliseconds(), cmd.ProcessState.UserTime().Milliseconds(), cmd.ProcessState.SystemTime().Milliseconds(), stat.Size(), 0}
		probe, err := runProbeForBenchmark(output)
		if err != nil {
			return err
		}
		if op == "extract_frame" {
			if !probe.IsImage || probe.Width == 0 {
				return fmt.Errorf("invalid frame output %+v", probe)
			}
		} else {
			if !probe.HasVideo || !probe.HasAudio || probe.DurationMs < 2900 {
				return fmt.Errorf("invalid video output %+v", probe)
			}
		}
		if op == "extract_reel" && (probe.Width != 360 || probe.Height != 640) {
			return fmt.Errorf("reel dimensions %dx%d", probe.Width, probe.Height)
		}
		if op == "transcode" {
			comparison := exec.Command(ffmpeg, "-hide_banner", "-i", source, "-i", output, "-filter_complex_threads", "1", "-lavfi", "[0:v][1:v]ssim", "-an", "-f", "null", "-")
			out, err := comparison.CombinedOutput()
			if err != nil {
				return err
			}
			match := regexp.MustCompile(`All:([0-9.]+)`).FindStringSubmatch(string(out))
			if len(match) < 2 {
				return fmt.Errorf("no SSIM result")
			}
			m.SSIM, _ = strconv.ParseFloat(match[1], 64)
			if m.SSIM < 0.94 {
				return fmt.Errorf("quality regression SSIM=%f", m.SSIM)
			}
		}
		mu.Lock()
		measurements = append(measurements, m)
		encoded, _ := json.Marshal(m)
		t.Log(string(encoded))
		mu.Unlock()
		return nil
	}
	for _, profile := range []string{"legacy", "low", "medium", "high"} {
		if err := run("transcode", profile, 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, op := range []string{"extract_frame", "extract_reel", "concat"} {
		if err := run(op, "medium", 1, 0); err != nil {
			t.Fatal(err)
		}
	}
	for _, concurrency := range []int{2, 4} {
		var wg sync.WaitGroup
		errs := make(chan error, concurrency)
		for i := 0; i < concurrency; i++ {
			wg.Add(1)
			go func(i int) { defer wg.Done(); errs <- run("extract_reel", "medium", concurrency, i) }(i)
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	encoded, _ := json.MarshalIndent(measurements, "", "  ")
	if path := os.Getenv("MEDIA_BENCHMARK_OUTPUT"); path != "" {
		if err := os.WriteFile(path, encoded, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
func runProbeForBenchmark(path string) (*Probe, error) {
	cmd := exec.Command("ffprobe", "-v", "quiet", "-print_format", "json", "-show_format", "-show_streams", path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseProbeBytes(out)
}

func TestRender4KGeometry(t *testing.T) {
	ffmpeg, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg unavailable")
	}
	root := t.TempDir()
	source := filepath.Join(root, "4k.mp4")
	cmd := exec.Command(ffmpeg, "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=3840x2160:rate=12", "-t", "1", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "18", "-threads", "2", source)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("4K fixture: %v %s", err, out)
	}
	for _, op := range []string{"extract_frame", "extract_reel"} {
		raw := json.RawMessage(`{"at_ms":500,"start_ms":0,"end_ms":800,"target_ratio":"9:16","crop_mode":"center","output_width":360,"encoder_profile":"medium"}`)
		plan, err := buildPlan(op, []string{"1"}, raw, "", ".mp4")
		if err != nil {
			t.Fatal(err)
		}
		args, err := materialiseArgs(plan.Args, []string{source}, root)
		if err != nil {
			t.Fatal(err)
		}
		outPath := filepath.Join(root, plan.Filename)
		args = append([]string{"-filter_threads", "1"}, args...)
		args = append(args, "-threads", "2", outPath)
		cmd := exec.Command(ffmpeg, args...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("4K %s: %v %s", op, err, output)
		}
		probe, err := runProbeForBenchmark(outPath)
		if err != nil {
			t.Fatal(err)
		}
		if probe.Width != 360 || probe.Height != 640 {
			t.Fatalf("4K %s dimensions %dx%d", op, probe.Width, probe.Height)
		}
		if op == "extract_frame" && !probe.IsImage {
			t.Fatal("still rendered as video")
		}
	}
}

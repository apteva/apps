package main

// Tier 1 (live-fire) — exercise each render operation against a real
// ffmpeg child against generated test files. These tests skip if
// ffmpeg / ffprobe aren't on PATH, so they double as a CI smoke check
// that the binary's host has the toolchain it needs.
//
// We deliberately bypass the storage upload + DB layer: this file
// validates only the plan → argv → ffmpeg → output pipeline. The
// renderpool's full lifecycle (download → run → upload → mark) is
// covered by the Tier 2 + Tier 3 tests where storage is actually
// available.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func skipIfNoFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	if _, err := exec.LookPath("ffprobe"); err != nil {
		t.Skip("ffprobe not on PATH")
	}
}

// generateSampleVideo writes a 5-second 320x240 mp4 with a 1kHz
// audio track to dir. h264 + aac so it's broadly parseable. Returns
// the file path. ~150KB.
func generateSampleVideo(t *testing.T, dir string) string {
	t.Helper()
	out := filepath.Join(dir, "sample.mp4")
	cmd := exec.Command("ffmpeg",
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=5:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=1000:duration=5",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		// All-intra: every frame is a keyframe. Lets stream-copy trim
		// land on exact frame boundaries instead of jumping back to
		// the nearest GOP. Source is tiny so the size hit is fine.
		"-g", "1", "-keyint_min", "1",
		"-c:a", "aac", "-b:a", "96k", "-shortest",
		out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate sample.mp4: %v: %s", err, b)
	}
	return out
}

// runOpAgainstFile drives the plan → materialiseArgs → ffmpeg
// pipeline for one operation against a real source file. Returns
// the produced output path (in dir/<plan.Filename>).
func runOpAgainstFile(t *testing.T, op string, srcPaths []string, params map[string]any, outputName, dir string) string {
	t.Helper()
	raw, _ := json.Marshal(params)
	plan, err := buildPlan(op, fakeFileIDs(srcPaths), raw, outputName)
	if err != nil {
		t.Fatalf("buildPlan(%s): %v", op, err)
	}
	args, err := materialiseArgs(plan.Args, srcPaths, dir)
	if err != nil {
		t.Fatalf("materialiseArgs: %v", err)
	}
	outPath := filepath.Join(dir, plan.Filename)
	args = append(args, outPath)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\nargv: ffmpeg %s\noutput:\n%s",
			op, err, strings.Join(args, " "), b)
	}
	st, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("output file missing: %v", err)
	}
	if st.Size() == 0 {
		t.Fatalf("output file is empty: %s", outPath)
	}
	return outPath
}

// fakeFileIDs invents storage-style file ids derived from the source
// path indices. The argv builders only use them in the default
// output filename, never to look anything up.
func fakeFileIDs(paths []string) []string {
	out := make([]string, len(paths))
	for i := range paths {
		out[i] = strconv.Itoa(100 + i)
	}
	return out
}

// ─── ffprobe helpers ────────────────────────────────────────────────

type probedStream struct {
	CodecType string  `json:"codec_type"`
	CodecName string  `json:"codec_name"`
	Width     int     `json:"width"`
	Height    int     `json:"height"`
	NbFrames  string  `json:"nb_frames"`
	Duration  string  `json:"duration"`
	Channels  int     `json:"channels"`
	SampRate  string  `json:"sample_rate"`
	Profile   string  `json:"profile"`
	BitRate   string  `json:"bit_rate"`
	_         float64 // pad to keep zero values readable in failures
}

type probedFormat struct {
	Duration   string `json:"duration"`
	FormatName string `json:"format_name"`
	Size       string `json:"size"`
}

type probeResult struct {
	Streams []probedStream `json:"streams"`
	Format  probedFormat   `json:"format"`
}

func probe(t *testing.T, path string) probeResult {
	t.Helper()
	cmd := exec.Command("ffprobe",
		"-v", "error",
		"-print_format", "json",
		"-show_streams", "-show_format",
		path,
	)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("ffprobe %s: %v", path, err)
	}
	var r probeResult
	if err := json.Unmarshal(out, &r); err != nil {
		t.Fatalf("ffprobe parse: %v: %s", err, out)
	}
	return r
}

func (r probeResult) videoStream() (probedStream, error) {
	for _, s := range r.Streams {
		if s.CodecType == "video" {
			return s, nil
		}
	}
	return probedStream{}, errors.New("no video stream")
}

func (r probeResult) audioStream() (probedStream, error) {
	for _, s := range r.Streams {
		if s.CodecType == "audio" {
			return s, nil
		}
	}
	return probedStream{}, errors.New("no audio stream")
}

func (r probeResult) durationSec() float64 {
	if r.Format.Duration == "" {
		return 0
	}
	d, _ := strconv.ParseFloat(r.Format.Duration, 64)
	return d
}

// ─── Per-operation live-fire tests ──────────────────────────────────

func TestFFmpeg_Trim(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "trim", []string{src},
		map[string]any{"start_ms": 1000, "end_ms": 3000},
		"clip.mp4", dir)

	r := probe(t, out)
	d := r.durationSec()
	// Stream-copy trims are keyframe-aligned so duration may be
	// slightly off — tolerate ±0.5s either side of the 2s target.
	if d < 1.5 || d > 2.5 {
		t.Errorf("trim duration=%.2fs want ~2.0s", d)
	}
	if _, err := r.videoStream(); err != nil {
		t.Errorf("trim output missing video: %v", err)
	}
}

func TestFFmpeg_ExtractFrame(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "extract_frame", []string{src},
		map[string]any{"at_ms": 2500, "width": 160},
		"", dir)

	if !strings.HasSuffix(out, ".png") {
		t.Errorf("extract_frame output isn't PNG: %s", out)
	}
	r := probe(t, out)
	v, err := r.videoStream()
	if err != nil {
		t.Fatalf("no video stream in PNG output: %v", err)
	}
	if v.Width != 160 {
		t.Errorf("width=%d want 160 (height auto)", v.Width)
	}
	// PNG codec — single frame, ffprobe still surfaces it as video.
	if v.CodecName != "png" {
		t.Errorf("codec=%q want png", v.CodecName)
	}
}

func TestFFmpeg_Resize_KeepAspect(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "resize", []string{src},
		map[string]any{"width": 160, "keep_aspect": true},
		"", dir)

	r := probe(t, out)
	v, _ := r.videoStream()
	if v.Width != 160 {
		t.Errorf("resize width=%d want 160", v.Width)
	}
	// 320x240 source with keep_aspect → 160x120 (-2 = even).
	if v.Height != 120 {
		t.Errorf("keep_aspect height=%d want 120", v.Height)
	}
}

func TestFFmpeg_Transcode_ToMatroska(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "transcode", []string{src},
		map[string]any{"format": "mkv", "video_codec": "libx264", "audio_codec": "aac"},
		"", dir)

	if !strings.HasSuffix(out, ".mkv") {
		t.Errorf("expected .mkv output, got %s", out)
	}
	r := probe(t, out)
	if !strings.Contains(r.Format.FormatName, "matroska") {
		t.Errorf("format=%q want matroska", r.Format.FormatName)
	}
}

func TestFFmpeg_Crop(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "crop", []string{src},
		map[string]any{"x": 40, "y": 20, "width": 200, "height": 150},
		"", dir)

	r := probe(t, out)
	v, _ := r.videoStream()
	if v.Width != 200 || v.Height != 150 {
		t.Errorf("crop dims=%dx%d want 200x150", v.Width, v.Height)
	}
}

func TestFFmpeg_AudioExtract_MP3(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir)

	out := runOpAgainstFile(t, "audio_extract", []string{src},
		map[string]any{"format": "mp3"},
		"", dir)

	if !strings.HasSuffix(out, ".mp3") {
		t.Errorf("expected .mp3 output, got %s", out)
	}
	r := probe(t, out)
	a, err := r.audioStream()
	if err != nil {
		t.Fatalf("no audio stream in extract output: %v", err)
	}
	if a.CodecName != "mp3" {
		t.Errorf("codec=%q want mp3", a.CodecName)
	}
	// Output must NOT have video.
	if _, err := r.videoStream(); err == nil {
		t.Error("audio_extract leaked video stream")
	}
}

func TestFFmpeg_Concat(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src1 := generateSampleVideo(t, dir)
	// Make a second source (same encoder settings → concat-friendly).
	src2 := filepath.Join(dir, "sample2.mp4")
	cmd := exec.Command("ffmpeg",
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=3:size=320x240:rate=30",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "1", "-keyint_min", "1",
		"-c:a", "aac", "-b:a", "96k", "-shortest",
		src2,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate sample2: %v: %s", err, b)
	}

	out := runOpAgainstFile(t, "concat", []string{src1, src2},
		map[string]any{},
		"merged.mp4", dir)

	r := probe(t, out)
	d := r.durationSec()
	if d < 7.0 || d > 9.0 {
		t.Errorf("concat duration=%.2fs want ~8.0s (5+3)", d)
	}
}

// TestFFmpeg_Cancellation kills a render mid-encode and verifies
// ffmpeg dies promptly. This test uses a longer source so the encode
// is genuinely in flight when we cancel.
func TestFFmpeg_Cancellation(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	// 30 seconds at 30fps with veryslow preset gives a long enough
	// transcode to interrupt cleanly.
	src := filepath.Join(dir, "long.mp4")
	gen := exec.Command("ffmpeg", "-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "testsrc=duration=30:size=640x480:rate=30",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		src)
	if b, err := gen.CombinedOutput(); err != nil {
		t.Fatalf("generate long.mp4: %v: %s", err, b)
	}

	raw, _ := json.Marshal(map[string]any{"format": "mp4", "video_codec": "libx264"})
	plan, _ := buildPlan("transcode", []string{"100"}, raw, "")
	args, _ := materialiseArgs(plan.Args, []string{src}, dir)
	args = append(args, "-preset", "veryslow") // make encode actually take time
	args = append(args, filepath.Join(dir, plan.Filename))

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Let the encode get going, then cancel.
	time.Sleep(200 * time.Millisecond)
	start := time.Now()
	cancel()

	err := cmd.Wait()
	dur := time.Since(start)
	if err == nil {
		t.Error("expected ffmpeg to exit non-zero after cancel")
	}
	if ctx.Err() != context.Canceled {
		t.Errorf("ctx.Err()=%v want Canceled", ctx.Err())
	}
	// ffmpeg should die within a couple of seconds of SIGKILL.
	if dur > 5*time.Second {
		t.Errorf("ffmpeg took %v to die after cancel — too slow", dur)
	}
	_ = fmt.Sprintf("ok") // silence unused import in alt builds
}

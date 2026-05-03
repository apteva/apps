package main

// Tests for the smart video thumbnail extractor: candidate-seek
// schedule, luminance check, end-to-end with a video that opens
// with a multi-second black fade.

import (
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

// ─── candidateThumbnailSeeks ───────────────────────────────────────

func TestCandidateThumbnailSeeks_UnknownDuration(t *testing.T) {
	got := candidateThumbnailSeeks(0, 1.0)
	if !reflect.DeepEqual(got, []float64{1.0}) {
		t.Errorf("zero duration: got %v want [1.0]", got)
	}
}

func TestCandidateThumbnailSeeks_LongVideo(t *testing.T) {
	// 60s clip → 5%, 15%, 30%, 50%, 75% = 3, 9, 18, 30, 45 plus
	// the configured fallback (1.0). All distinct so we expect all 6.
	got := candidateThumbnailSeeks(60_000, 1.0)
	want := []float64{1.0, 3, 9, 18, 30, 45}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("60s: got %v want %v", got, want)
	}
}

func TestCandidateThumbnailSeeks_ShortVideo(t *testing.T) {
	// 5s clip → 5%, 15% = 0.25, 0.75 — first one is below 0.5s
	// guard so dropped. Expect 0.75 + 1.0 fallback + 1.5 (30%) +
	// 2.5 (50%) + 3.75 (75%).
	got := candidateThumbnailSeeks(5_000, 1.0)
	if len(got) < 3 {
		t.Errorf("5s clip should produce multiple candidates, got %v", got)
	}
	for _, s := range got {
		if s >= 5.0 {
			t.Errorf("seek %.2f exceeds duration 5s", s)
		}
		if s < 0.5 {
			t.Errorf("seek %.2f below 0.5s floor", s)
		}
	}
}

func TestCandidateThumbnailSeeks_FallbackBeforeOthers(t *testing.T) {
	// User explicitly configured thumbnail_seek_seconds=2; that value
	// should be tried FIRST so a known-good moment is honoured.
	got := candidateThumbnailSeeks(60_000, 2.0)
	if len(got) == 0 || got[0] != 2.0 {
		t.Errorf("fallback should be tried first: got %v", got)
	}
}

func TestCandidateThumbnailSeeks_VeryShortClipFallback(t *testing.T) {
	// 200ms clip — every percentage is below 0.5s. Should still
	// return at least one candidate so the worker has something
	// to try (rather than degrading to no thumbnail).
	got := candidateThumbnailSeeks(200, 1.0)
	if len(got) == 0 {
		t.Fatal("expected at least one fallback candidate for very short clip")
	}
}

// ─── meanLumaJPEG ──────────────────────────────────────────────────

func TestMeanLumaJPEG_BlackImage(t *testing.T) {
	path := writeSolidJPEG(t, color.Black, 320, 180)
	luma, err := meanLumaJPEG(path)
	if err != nil {
		t.Fatal(err)
	}
	if luma > 5 {
		t.Errorf("solid black should be near 0, got %.2f", luma)
	}
}

func TestMeanLumaJPEG_WhiteImage(t *testing.T) {
	path := writeSolidJPEG(t, color.White, 320, 180)
	luma, err := meanLumaJPEG(path)
	if err != nil {
		t.Fatal(err)
	}
	if luma < 240 {
		t.Errorf("solid white should be near 255, got %.2f", luma)
	}
}

func TestMeanLumaJPEG_GreyImage(t *testing.T) {
	// Grey 0x80,0x80,0x80 → luminance ~128
	grey := color.RGBA{0x80, 0x80, 0x80, 0xff}
	path := writeSolidJPEG(t, grey, 320, 180)
	luma, err := meanLumaJPEG(path)
	if err != nil {
		t.Fatal(err)
	}
	if luma < 100 || luma > 150 {
		t.Errorf("grey 0x80 should land near 128, got %.2f", luma)
	}
}

func TestMeanLumaJPEG_ThresholdSeparatesBlackFromContent(t *testing.T) {
	// The threshold's job is to reject pure-black / dark fades but
	// NOT moody indoor scenes. Verify with a 0x30 grey (dim but
	// content) vs black.
	dimGrey := color.RGBA{0x30, 0x30, 0x30, 0xff}
	dim := writeSolidJPEG(t, dimGrey, 100, 100)
	black := writeSolidJPEG(t, color.Black, 100, 100)
	dimL, _ := meanLumaJPEG(dim)
	blackL, _ := meanLumaJPEG(black)
	if blackL >= minAcceptableLuma {
		t.Errorf("threshold too low: black (%.2f) >= %.0f", blackL, minAcceptableLuma)
	}
	if dimL < minAcceptableLuma {
		t.Errorf("threshold too high: dim grey (%.2f) < %.0f — would reject moody content", dimL, minAcceptableLuma)
	}
}

// ─── live ffmpeg: black-opening fixture ────────────────────────────
//
// Runs ffmpeg if available; skips otherwise. Generates a 12-second
// MP4 whose first 5 seconds are pure black, then 7 seconds of
// testsrc (multi-coloured pattern). Confirms:
//   - the smart extractor rejects the first attempts (which would
//     hit black) and lands on the testsrc portion
//   - the legacy fixed-seek behaviour at 1.0s would have returned
//     a black frame (sanity that our test fixture is what we think)

func TestExtractVideoThumbnail_SkipsBlackOpening(t *testing.T) {
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateBlackOpeningVideo(t, dir, 5.0, 7.0)

	out := filepath.Join(dir, "thumb.jpg")
	ctx := context.Background()
	if err := extractVideoThumbnail(ctx, "ffmpeg", src, out, 1.0, 320, 12_000); err != nil {
		t.Fatal(err)
	}
	luma, err := meanLumaJPEG(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("thumbnail luma: %.2f", luma)
	if luma < minAcceptableLuma {
		t.Errorf("smart extractor still landed on dark frame (luma=%.2f); expected >= %.0f",
			luma, minAcceptableLuma)
	}
}

func TestExtractVideoFrame_AtFixed1s_HitsBlack(t *testing.T) {
	// Sanity: confirms the test fixture really has a black opening,
	// so the previous test isn't a false positive.
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateBlackOpeningVideo(t, dir, 5.0, 7.0)

	out := filepath.Join(dir, "thumb-1s.jpg")
	ctx := context.Background()
	if err := extractVideoFrame(ctx, "ffmpeg", src, out, 1.0, 320); err != nil {
		t.Fatal(err)
	}
	luma, err := meanLumaJPEG(out)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("naive 1s seek luma: %.2f (proves the fixture's opening is black)", luma)
	if luma >= minAcceptableLuma {
		t.Errorf("fixture broken — opening at 1s should be ~black, got luma=%.2f", luma)
	}
}

func TestExtractVideoThumbnail_NormalVideo(t *testing.T) {
	// The non-black path: a regular testsrc video should produce
	// a bright thumbnail on the first attempt without falling
	// back through the schedule.
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := generateSampleVideo(t, dir) // from renderffmpeg_test.go

	out := filepath.Join(dir, "thumb.jpg")
	ctx := context.Background()
	if err := extractVideoThumbnail(ctx, "ffmpeg", src, out, 1.0, 320, 5_000); err != nil {
		t.Fatal(err)
	}
	luma, err := meanLumaJPEG(out)
	if err != nil {
		t.Fatal(err)
	}
	if luma < minAcceptableLuma {
		t.Errorf("normal testsrc video produced dark thumbnail: luma=%.2f", luma)
	}
}

func TestExtractVideoThumbnail_AllBlackVideo_FallsBack(t *testing.T) {
	// All-black video — even the smart extractor can't do better.
	// Verify we still produce *some* output (last attempt's bytes).
	skipIfNoFFmpeg(t)
	dir := t.TempDir()
	src := filepath.Join(dir, "all-black.mp4")
	cmd := exec.Command("ffmpeg",
		"-y", "-loglevel", "error",
		"-f", "lavfi", "-i", "color=black:s=320x240:rate=30:duration=5",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-g", "1", "-keyint_min", "1",
		src,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate all-black: %v: %s", err, b)
	}

	out := filepath.Join(dir, "thumb.jpg")
	ctx := context.Background()
	// Should NOT error — fallback is to keep the last attempt's
	// (still-black) output rather than fail outright.
	if err := extractVideoThumbnail(ctx, "ffmpeg", src, out, 1.0, 320, 5_000); err != nil {
		t.Errorf("expected fallback to succeed even on all-black video: %v", err)
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		t.Error("expected a non-empty fallback file")
	}
}

// ─── helpers ───────────────────────────────────────────────────────

// writeSolidJPEG creates a JPEG of solid color at the given path
// for the lifetime of the test.
func writeSolidJPEG(t *testing.T, c color.Color, w, h int) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
	path := filepath.Join(t.TempDir(), "solid-"+strconv.Itoa(w)+".jpg")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := jpeg.Encode(f, img, &jpeg.Options{Quality: 90}); err != nil {
		t.Fatal(err)
	}
	return path
}

// generateBlackOpeningVideo writes an MP4 with `blackSec` seconds
// of pure black followed by `testSec` of testsrc multi-colour pattern.
// All-keyframe encoding so seeks land precisely.
func generateBlackOpeningVideo(t *testing.T, dir string, blackSec, testSec float64) string {
	t.Helper()
	out := filepath.Join(dir, "black-then-testsrc.mp4")
	// concat demuxer requires identical encode params on both clips.
	// Generate each segment, then concat with stream copy.
	black := filepath.Join(dir, "black.mp4")
	test := filepath.Join(dir, "test.mp4")

	gen := func(label, dst string, args []string) {
		full := append([]string{"-y", "-loglevel", "error"}, args...)
		full = append(full,
			"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
			"-g", "1", "-keyint_min", "1",
			dst,
		)
		cmd := exec.Command("ffmpeg", full...)
		if b, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("generate %s: %v: %s", label, err, b)
		}
	}
	gen("black",
		black,
		[]string{"-f", "lavfi", "-i", "color=black:s=320x240:rate=30:duration=" + dtoa(blackSec)},
	)
	gen("testsrc",
		test,
		[]string{"-f", "lavfi", "-i", "testsrc=size=320x240:rate=30:duration=" + dtoa(testSec)},
	)
	listPath := filepath.Join(dir, "concat-list.txt")
	if err := os.WriteFile(listPath,
		[]byte("file '"+black+"'\nfile '"+test+"'\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ffmpeg",
		"-y", "-loglevel", "error",
		"-f", "concat", "-safe", "0",
		"-i", listPath,
		"-c", "copy",
		out,
	)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("concat: %v: %s", err, b)
	}
	return out
}

func dtoa(s float64) string {
	if s == float64(int64(s)) {
		return strings.TrimRight(strings.TrimRight(strconv.FormatFloat(s, 'f', 3, 64), "0"), ".")
	}
	return strconv.FormatFloat(s, 'f', 3, 64)
}

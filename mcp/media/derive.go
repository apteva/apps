package main

// ffmpeg-based derivations: thumbnail (single frame for video/image)
// and waveform (showwavespic for audio). Both write to a temp PNG/JPG
// the caller then uploads back to the storage app.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// makeThumbnail extracts a single frame at seekSeconds, scaled to the
// configured width (height auto, preserves aspect). Output is JPEG so
// it's small + universally renderable. inFile must be a local path —
// ffmpeg can't seek over a streaming HTTP body for arbitrary formats.
func makeThumbnail(ctx context.Context, ffmpegPath, inFile, outFile string, seekSeconds float64, width int, isImage bool) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y",            // overwrite output without asking
		"-loglevel", "error",
	}
	if !isImage {
		// Place -ss before -i so ffmpeg seeks via the demuxer instead
		// of decoding from frame 0. Much faster on long clips.
		args = append(args, "-ss", fmt.Sprintf("%.2f", seekSeconds))
	}
	args = append(args,
		"-i", inFile,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		// `-2` keeps height even (libjpeg requires even dims at some
		// chroma settings); `-1` would let height fall to odd.
		"-q:v", "3", // 1=best, 31=worst. 3 ≈ ~80% quality.
		outFile,
	)
	cmd := exec.CommandContext(cctx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg thumbnail: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// makeWaveform renders a static PNG waveform image of the audio
// content. showwavespic collapses the whole track into one image —
// no animation, no playback — exactly what a list view wants.
func makeWaveform(ctx context.Context, ffmpegPath, inFile, outFile string, width, height int) error {
	cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, ffmpegPath,
		"-y",
		"-loglevel", "error",
		"-i", inFile,
		"-filter_complex",
		fmt.Sprintf("showwavespic=s=%dx%d:colors=#888888", width, height),
		"-frames:v", "1",
		outFile,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg waveform: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

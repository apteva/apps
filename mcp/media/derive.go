package main

// ffmpeg-based derivations: thumbnail (single frame for video/image)
// and waveform (showwavespic for audio).
//
// Video thumbnails are non-trivial: a naive `-ss <fixed seek>` will
// happily land on a black opening / fade / title card / logo intro
// for a meaningful fraction of real-world videos. We mitigate with:
//
//   1. ffmpeg's built-in `thumbnail` filter — picks the most
//      "different" frame from a window using RGB-histogram scoring.
//   2. A multi-attempt seek schedule sampling several positions
//      across the video's duration, not just one fixed offset.
//   3. A post-extraction luminance check: decode the JPEG, compute
//      mean Y; if it's below the dark-threshold, retry at the next
//      candidate position. Last attempt always wins as fallback so
//      we never fail to produce *some* thumbnail.
//
// Net effect: a 30-second black-fade intro followed by content
// produces a thumbnail of the content, not the black.

import (
	"context"
	"errors"
	"fmt"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"strings"
	"time"
)

// minAcceptableLuma — frames with mean luminance below this are
// considered "too dark" (probably black / fade / logo intro) and
// we move on to the next candidate seek. Out of 0..255.
//
// Tuned empirically: pure black is 0, near-black studio fades
// 5–15, dim indoor scenes 30–60, normal content 80+. 25 splits
// fade-vs-content cleanly without rejecting moody / film-noir
// shots wholesale.
const minAcceptableLuma = 25.0

// makeThumbnail extracts a single frame, scaled to width. For
// images it does the obvious one-shot extraction. For videos it
// runs the smart multi-attempt + luminance-check pipeline above.
//
// outFile is overwritten on every attempt; the final state is
// whichever attempt was last to write (the first acceptable one,
// or the last fallback).
func makeThumbnail(ctx context.Context, ffmpegPath, inFile, outFile string, fallbackSeekSeconds float64, width int, isImage bool, durationMs int64) error {
	if isImage {
		return extractImageThumbnail(ctx, ffmpegPath, inFile, outFile, width)
	}
	return extractVideoThumbnail(ctx, ffmpegPath, inFile, outFile, fallbackSeekSeconds, width, durationMs)
}

// extractImageThumbnail is the simple path: one frame, scaled.
// No seek, no thumbnail filter — there's only one frame to pick.
func extractImageThumbnail(ctx context.Context, ffmpegPath, inFile, outFile string, width int) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y",
		"-loglevel", "error",
		"-i", inFile,
		"-frames:v", "1",
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		"-q:v", "3",
		outFile,
	}
	cmd := exec.CommandContext(cctx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg image thumbnail: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// extractVideoThumbnail runs the smart multi-attempt strategy.
//
//	for each candidate seek position:
//	  1. ffmpeg -ss <pos> -i ... -vf "thumbnail=N,scale=..."
//	  2. decode the produced JPEG, compute mean luminance
//	  3. if luma >= minAcceptableLuma → done
//	last attempt's output is used as fallback even if too dark,
//	so we never return without producing a thumbnail.
func extractVideoThumbnail(ctx context.Context, ffmpegPath, inFile, outFile string, fallbackSeek float64, width int, durationMs int64) error {
	seeks := candidateThumbnailSeeks(durationMs, fallbackSeek)
	var lastFFErr error
	for i, seek := range seeks {
		if err := extractVideoFrame(ctx, ffmpegPath, inFile, outFile, seek, width); err != nil {
			lastFFErr = err
			continue
		}
		luma, err := meanLumaJPEG(outFile)
		if err != nil {
			// Decode failure on our own output — try the next position.
			continue
		}
		if luma >= minAcceptableLuma {
			return nil
		}
		// Final attempt: keep the file we just wrote even if dark —
		// better an under-exposed thumbnail than nothing at all.
		if i == len(seeks)-1 {
			return nil
		}
	}
	if lastFFErr != nil {
		return lastFFErr
	}
	return errors.New("no candidate seeks produced a usable frame")
}

// candidateThumbnailSeeks returns the sequence of seek positions
// the extractor tries. Strategy:
//
//   - ALWAYS include the user-configured fallbackSeek first (back-
//     compat with the old single-seek behaviour; also lets users
//     pin a known-good moment via thumbnail_seek_seconds).
//   - Then sample at 5%, 15%, 30%, 50%, 75% of duration. Skips
//     duplicates (close-by values for very short clips).
//   - For unknown durations (durationMs<=0) just returns
//     [fallbackSeek].
//
// We dedupe to one decimal place because two seeks 0.05s apart
// land on the same keyframe and so produce the same frame.
func candidateThumbnailSeeks(durationMs int64, fallbackSeek float64) []float64 {
	if durationMs <= 0 {
		return []float64{fallbackSeek}
	}
	dur := float64(durationMs) / 1000.0
	pcts := []float64{0.05, 0.15, 0.30, 0.50, 0.75}
	candidates := []float64{math.Max(0.5, fallbackSeek)}
	for _, p := range pcts {
		s := dur * p
		if s < 0.5 || s >= dur-0.1 {
			continue
		}
		candidates = append(candidates, s)
	}

	out := make([]float64, 0, len(candidates))
	seen := map[int]bool{}
	for _, s := range candidates {
		if s >= dur {
			continue
		}
		key := int(s * 10) // dedupe to 0.1s granularity
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	if len(out) == 0 {
		out = []float64{math.Max(0.5, math.Min(fallbackSeek, dur*0.5))}
	}
	return out
}

// extractVideoFrame runs ffmpeg with the smart `thumbnail` filter
// at the given seek position. Window of 30 frames = ~1 second at
// 30fps; small enough to run fast, large enough for the filter's
// histogram-distance scoring to find a representative frame.
func extractVideoFrame(ctx context.Context, ffmpegPath, inFile, outFile string, seekSeconds float64, width int) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y",
		"-loglevel", "error",
		// Place -ss BEFORE -i so ffmpeg seeks via the demuxer (fast,
		// keyframe-aligned) rather than decoding from frame 0.
		"-ss", fmt.Sprintf("%.2f", seekSeconds),
		"-i", inFile,
		"-vf", fmt.Sprintf("thumbnail=30,scale=%d:-2", width),
		"-frames:v", "1",
		"-q:v", "3",
		outFile,
	}
	cmd := exec.CommandContext(cctx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg thumbnail @%.2fs: %w: %s",
			seekSeconds, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// meanLumaJPEG decodes the JPEG at path and returns the average
// luminance (Rec. 709 weighting) on a 0..255 scale. Samples every
// 4th pixel in each axis for speed — a 320×180 thumbnail still
// gets ~3,600 samples, plenty for an "is it black" check.
func meanLumaJPEG(path string) (float64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	img, err := jpeg.Decode(f)
	if err != nil {
		return 0, err
	}
	bounds := img.Bounds()
	var sum int64
	var count int64
	const step = 4
	for y := bounds.Min.Y; y < bounds.Max.Y; y += step {
		for x := bounds.Min.X; x < bounds.Max.X; x += step {
			r, g, b, _ := img.At(x, y).RGBA()
			// RGBA() returns 16-bit per channel (0..65535).
			// Rec. 709 luminance, scale to 0..255 by >> 8.
			ly := (2126*int64(r) + 7152*int64(g) + 722*int64(b)) / 10000
			sum += ly >> 8
			count++
		}
	}
	if count == 0 {
		return 0, nil
	}
	return float64(sum) / float64(count), nil
}

// extractKeyframe pulls a single representative frame at the given
// timestamp. Uses the same `thumbnail=30` smart-frame filter as
// extractVideoFrame — picks the most representative frame from a
// 30-frame window starting at seekSeconds via histogram-distance
// scoring, so we don't accidentally land on a transition / black
// frame even when the user asked for an exact timestamp.
//
// Distinct from extractVideoFrame because:
//   - keyframes are part of a SERIES (the indexer iterates positions),
//     so there's no luma-rejection retry loop — a single dark frame
//     in a series is fine, and retrying would push us past the next
//     keyframe's window.
//   - the luminance check would also break for legitimately dark
//     content (night-scene videos, dimly-lit interiors).
func extractKeyframe(ctx context.Context, ffmpegPath, inFile, outFile string, seekSeconds float64, width int) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y",
		"-loglevel", "error",
		"-ss", fmt.Sprintf("%.2f", seekSeconds),
		"-i", inFile,
		"-vf", fmt.Sprintf("thumbnail=30,scale=%d:-2", width),
		"-frames:v", "1",
		"-q:v", "3",
		outFile,
	}
	cmd := exec.CommandContext(cctx, ffmpegPath, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg keyframe @%.2fs: %w: %s",
			seekSeconds, err, strings.TrimSpace(string(out)))
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

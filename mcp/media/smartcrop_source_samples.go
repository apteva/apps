package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const smartCropSupplementIntervalMs = int64(5000)

// smartCropSupplementPositions creates a dense, bounded local storyboard
// around the requested output. Source duration does not affect the amount of
// work: a still uses at most nine nearby frames and a reel uses at most the
// existing 24-frame analysis budget.
func smartCropSupplementPositions(target smartCropTarget, durationMs int64) []int64 {
	lastSeek := durationMs - 100
	if lastSeek < 0 {
		lastSeek = 0
	}
	clampPosition := func(pos int64) int64 {
		if pos < 0 {
			return 0
		}
		if durationMs > 0 && pos > lastSeek {
			return lastSeek
		}
		return pos
	}

	positions := make([]int64, 0, smartCropTemporalMaxSamples)
	if !target.HasRange() {
		for i := -smartCropTemporalMaxSamples / 2; i <= smartCropTemporalMaxSamples/2; i++ {
			positions = append(positions, clampPosition(target.FocusMs+int64(i)*smartCropSupplementIntervalMs))
		}
		return uniqueSortedSmartCropPositions(positions)
	}

	startMs := clampPosition(target.StartMs)
	endMs := clampPosition(target.EndMs)
	if endMs < startMs {
		endMs = startMs
	}
	span := endMs - startMs
	naturalCount := int(span/smartCropSupplementIntervalMs) + 1
	if startMs+int64(naturalCount-1)*smartCropSupplementIntervalMs < endMs {
		naturalCount++
	}
	if naturalCount < 2 {
		naturalCount = 2
	}
	count := minInt(naturalCount, smartCropV2MaxSamples)
	for i := 0; i < count; i++ {
		if count == 1 {
			positions = append(positions, startMs)
			continue
		}
		positions = append(positions, startMs+int64(i)*span/int64(count-1))
	}
	return uniqueSortedSmartCropPositions(positions)
}

func uniqueSortedSmartCropPositions(positions []int64) []int64 {
	sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
	out := positions[:0]
	for _, pos := range positions {
		if len(out) == 0 || out[len(out)-1] != pos {
			out = append(out, pos)
		}
	}
	return out
}

// analyzeSmartCropV2Source is the sparse-storyboard recovery path. It extracts
// temporary screenshots directly from a signed source URL; nothing is uploaded
// or added to the catalog, so long sources do not permanently multiply their
// stored derivatives. Parallelism and total samples stay bounded.
func analyzeSmartCropV2Source(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, sourceFileID string,
	positions []int64,
	srcW, srcH, targetW, targetH int,
) ([]smartCropV2Sample, error) {
	if len(positions) == 0 {
		return nil, fmt.Errorf("no supplemental sample positions")
	}
	fileID, err := strconv.ParseInt(sourceFileID, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid source file_id %q", sourceFileID)
	}
	ffmpegPath := strings.TrimSpace(app.Config().Get("ffmpeg_path"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	if signedURL, signedErr := sc.GetSignedURL(ctx, projectID, fileID, 900); signedErr == nil {
		if samples, sampleErr := analyzeSmartCropV2Input(ctx, ffmpegPath, signedURL, positions, srcW, srcH, targetW, targetH); sampleErr == nil {
			return samples, nil
		}
	}

	// A public URL is optional for local installations, and an otherwise valid
	// URL may briefly become stale when a tunnel changes. Fall back to the same
	// authenticated Storage download the local renderer uses. This costs one
	// full download only on the sparse recovery path, but avoids making smart
	// crop correctness depend on public routing.
	dir, err := os.MkdirTemp("", "apteva-smartcrop-source-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	localPath := filepath.Join(dir, "source")
	f, err := os.Create(localPath)
	if err != nil {
		return nil, err
	}
	downloadErr := sc.DownloadContent(ctx, projectID, fileID, f)
	closeErr := f.Close()
	if downloadErr != nil {
		return nil, fmt.Errorf("storage download: %w", downloadErr)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close downloaded source: %w", closeErr)
	}
	return analyzeSmartCropV2Input(ctx, ffmpegPath, localPath, positions, srcW, srcH, targetW, targetH)
}

func analyzeSmartCropV2Input(
	ctx context.Context,
	ffmpegPath, input string,
	positions []int64,
	srcW, srcH, targetW, targetH int,
) ([]smartCropV2Sample, error) {
	dir, err := os.MkdirTemp("", "apteva-smartcrop-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	results := make([]*smartCropV2Sample, len(positions))
	errs := make([]error, len(positions))
	sem := make(chan struct{}, smartCropV2MaxParallelDownloads)
	var wg sync.WaitGroup
	for i, pos := range positions {
		i, pos := i, pos
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				errs[i] = ctx.Err()
				return
			}
			defer func() { <-sem }()
			framePath := filepath.Join(dir, fmt.Sprintf("%02d.jpg", i))
			if err := extractSmartCropFrame(ctx, ffmpegPath, input, framePath, float64(pos)/1000, 320); err != nil {
				errs[i] = err
				return
			}
			f, err := os.Open(framePath)
			if err != nil {
				errs[i] = err
				return
			}
			img, _, err := image.Decode(f)
			f.Close()
			if err != nil {
				errs[i] = err
				return
			}
			win, err := analyzeSmartCropV2Frame(srcW, srcH, targetW, targetH, img)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = &smartCropV2Sample{point: cropPathPoint{AtMs: pos, X: win.X}, img: img}
		}()
	}
	wg.Wait()

	samples := make([]smartCropV2Sample, 0, len(results))
	var firstErr error
	for i, result := range results {
		if result != nil {
			samples = append(samples, *result)
			continue
		}
		if firstErr == nil {
			firstErr = errs[i]
		}
	}
	if len(samples) < 2 {
		if firstErr == nil {
			return nil, fmt.Errorf("only %d/%d supplemental frames decoded", len(samples), len(positions))
		}
		return nil, fmt.Errorf("only %d/%d supplemental frames decoded: %w", len(samples), len(positions), firstErr)
	}
	return samples, nil
}

// extractSmartCropFrame samples the requested instant, not the most
// representative frame from the following second. The thumbnail=30 filter is
// useful for catalog thumbnails but shifts a moving subject in time and makes
// a crop computed for 166.0s describe roughly 166.0-167.0s instead.
func extractSmartCropFrame(ctx context.Context, ffmpegPath, input, output string, seekSeconds float64, width int) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	args := []string{
		"-y", "-loglevel", "error",
		"-ss", fmt.Sprintf("%.3f", seekSeconds),
		"-i", input,
		"-vf", fmt.Sprintf("scale=%d:-2", width),
		"-frames:v", "1",
		"-q:v", "3",
		output,
	}
	out, err := exec.CommandContext(cctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg smart crop frame @%.3fs: %w: %s", seekSeconds, err, strings.TrimSpace(string(out)))
	}
	return nil
}

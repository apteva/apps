package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"image"
	"math"
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

const (
	remoteSmartCropMarker         = "APTEVA_SMARTCROP_FRAME:"
	remoteSmartCropSampleWidth    = 320
	remoteSmartCropMaxOutputBytes = 900 * 1024
)

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
	var directFailed, remoteFailed bool
	if signedURL, signedErr := sc.GetSignedURL(ctx, projectID, fileID, 900); signedErr == nil {
		if samples, sampleErr := analyzeSmartCropV2Input(ctx, ffmpegPath, signedURL, positions, srcW, srcH, targetW, targetH); sampleErr == nil {
			return samples, nil
		} else {
			directFailed = true
		}

		// A remote render installation can be perfectly healthy while the
		// Media sidecar itself cannot decode the signed source (missing local
		// codecs, constrained networking, or temporary disk pressure). In that
		// case, run the same bounded low-resolution sampling pass on the FFmpeg
		// host that will perform the final render. Returning the JPEGs inline is
		// cheap (at most 32 x 320px frames) and avoids both a full source download
		// and permanently storing a denser storyboard.
		if hostID := remoteIndexerHostID(app); hostID > 0 {
			if samples, sampleErr := analyzeSmartCropV2Remote(ctx, app, hostID, signedURL,
				positions, srcW, srcH, targetW, targetH); sampleErr == nil {
				app.Logger().Info("smartcrop v2 using remote source samples",
					"file_id", sourceFileID, "host_id", hostID, "samples", len(samples))
				return samples, nil
			} else {
				remoteFailed = true
			}
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
	samples, sampleErr := analyzeSmartCropV2Input(ctx, ffmpegPath, localPath, positions, srcW, srcH, targetW, targetH)
	if sampleErr == nil {
		return samples, nil
	}
	return nil, fmt.Errorf("source sampling failed (signed_url_failed=%t remote_failed=%t): %w",
		directFailed, remoteFailed, sampleErr)
}

func analyzeSmartCropV2Remote(
	ctx context.Context,
	app *sdk.AppCtx,
	hostID int64,
	signedURL string,
	positions []int64,
	srcW, srcH, targetW, targetH int,
) ([]smartCropV2Sample, error) {
	paths, err := sharedRemoteInstaller().Ensure(ctx, app, hostID)
	if err != nil {
		return nil, fmt.Errorf("remote ffmpeg unavailable on host_id=%d: %w", hostID, err)
	}
	script, err := buildRemoteSmartCropSampleScript(paths.FFmpeg, signedURL, positions)
	if err != nil {
		return nil, err
	}
	timeoutS := maxInt(90, len(positions)*15)
	if timeoutS > 600 {
		timeoutS = 600
	}
	out, exit, runErr := runRemote(ctx, app, hostID, script, timeoutS)
	if runErr != nil {
		return nil, fmt.Errorf("remote smartcrop samples: %w (output: %s)", runErr, truncate(out, 600))
	}
	if exit != 0 {
		return nil, fmt.Errorf("remote smartcrop samples exit=%d: %s", exit, truncate(out, 600))
	}
	return parseRemoteSmartCropSamples(out, positions, srcW, srcH, targetW, targetH)
}

func buildRemoteSmartCropSampleScript(ffmpegPath, signedURL string, positions []int64) (string, error) {
	if strings.TrimSpace(ffmpegPath) == "" || strings.TrimSpace(signedURL) == "" {
		return "", fmt.Errorf("remote smartcrop sampling requires ffmpeg and a signed URL")
	}
	positions = uniqueSortedSmartCropPositions(append([]int64(nil), positions...))
	if len(positions) < 2 || len(positions) > smartCropTrackingMaxExtraFrames {
		return "", fmt.Errorf("remote smartcrop sampling needs 2..%d positions, got %d", smartCropTrackingMaxExtraFrames, len(positions))
	}
	for _, position := range positions {
		if position < 0 {
			return "", fmt.Errorf("remote smartcrop position must be non-negative: %d", position)
		}
	}
	if len(positions) > smartCropV2MaxSamples {
		capped := make([]int64, 0, smartCropV2MaxSamples)
		for i := 0; i < smartCropV2MaxSamples; i++ {
			index := int(math.Round(float64(i) * float64(len(positions)-1) / float64(smartCropV2MaxSamples-1)))
			if len(capped) == 0 || capped[len(capped)-1] != positions[index] {
				capped = append(capped, positions[index])
			}
		}
		positions = capped
	}

	var b strings.Builder
	b.WriteString("set -u\n")
	b.WriteString("WORK=$(mktemp -d /tmp/apteva-smartcrop-samples-XXXXXX)\n")
	b.WriteString("trap 'rm -rf \"$WORK\"' EXIT\n")
	fmt.Fprintf(&b, "FFMPEG=%s\n", shellQuote(ffmpegPath))
	fmt.Fprintf(&b, "SIGNED_URL=%s\n", shellQuote(signedURL))
	b.WriteString("extract_one() {\n")
	b.WriteString("  POS_MS=$1\n")
	b.WriteString("  POS_SEC=$(awk -v p=\"$POS_MS\" 'BEGIN{printf \"%.3f\", p/1000}')\n")
	fmt.Fprintf(&b, "  \"$FFMPEG\" -nostdin -y -loglevel error -ss \"$POS_SEC\" -i \"$SIGNED_URL\" -vf scale=%d:-2 -frames:v 1 -q:v 3 \"$WORK/$POS_MS.jpg\" >/dev/null 2>&1 || true\n", remoteSmartCropSampleWidth)
	b.WriteString("}\n")
	b.WriteString("ACTIVE=0\n")
	b.WriteString("for POS_MS in")
	for _, position := range positions {
		fmt.Fprintf(&b, " %d", position)
	}
	b.WriteString("; do\n")
	b.WriteString("  extract_one \"$POS_MS\" &\n")
	b.WriteString("  ACTIVE=$((ACTIVE+1))\n")
	b.WriteString("  if [ \"$ACTIVE\" -ge 4 ]; then wait || true; ACTIVE=0; fi\n")
	b.WriteString("done\n")
	b.WriteString("wait || true\n")
	// Instances caps command output at roughly 1 MiB. Preserve the 320px/q3
	// frames used by the detector whenever they fit; only unusually detailed
	// batches are recompressed before base64 expansion can cross that cap.
	b.WriteString("TOTAL_BYTES=$(wc -c \"$WORK\"/*.jpg 2>/dev/null | tail -1 | awk '{print $1}')\n")
	b.WriteString("if [ \"${TOTAL_BYTES:-0}\" -gt 620000 ]; then\n")
	b.WriteString("  for SOURCE in \"$WORK\"/*.jpg; do\n")
	b.WriteString("    [ -s \"$SOURCE\" ] || continue\n")
	b.WriteString("    SMALL=\"$SOURCE.small.jpg\"\n")
	b.WriteString("    \"$FFMPEG\" -nostdin -y -loglevel error -i \"$SOURCE\" -vf scale=280:-2 -frames:v 1 -q:v 5 \"$SMALL\" >/dev/null 2>&1 && mv \"$SMALL\" \"$SOURCE\"\n")
	b.WriteString("  done\n")
	b.WriteString("fi\n")
	b.WriteString("COUNT=0\n")
	b.WriteString("for POS_MS in")
	for _, position := range positions {
		fmt.Fprintf(&b, " %d", position)
	}
	b.WriteString("; do\n")
	b.WriteString("  [ -s \"$WORK/$POS_MS.jpg\" ] || continue\n")
	fmt.Fprintf(&b, "  printf '%s%%s:' \"$POS_MS\"\n", remoteSmartCropMarker)
	b.WriteString("  base64 < \"$WORK/$POS_MS.jpg\" | tr -d '\\r\\n'\n")
	b.WriteString("  printf '\\n'\n")
	b.WriteString("  COUNT=$((COUNT+1))\n")
	b.WriteString("done\n")
	b.WriteString("[ \"$COUNT\" -ge 2 ]\n")
	return b.String(), nil
}

func parseRemoteSmartCropSamples(
	out string,
	positions []int64,
	srcW, srcH, targetW, targetH int,
) ([]smartCropV2Sample, error) {
	if len(out) > remoteSmartCropMaxOutputBytes {
		return nil, fmt.Errorf("remote smartcrop output exceeds %d bytes", remoteSmartCropMaxOutputBytes)
	}
	allowed := make(map[int64]struct{}, len(positions))
	for _, position := range positions {
		allowed[position] = struct{}{}
	}
	byTime := make(map[int64]smartCropV2Sample, len(positions))
	for _, line := range strings.Split(out, "\n") {
		if !strings.HasPrefix(line, remoteSmartCropMarker) {
			continue
		}
		fields := strings.SplitN(strings.TrimPrefix(line, remoteSmartCropMarker), ":", 2)
		if len(fields) != 2 {
			continue
		}
		position, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			continue
		}
		if _, ok := allowed[position]; !ok {
			continue
		}
		raw, err := base64.StdEncoding.DecodeString(fields[1])
		if err != nil || len(raw) == 0 || len(raw) > 256*1024 {
			continue
		}
		img, _, err := image.Decode(bytes.NewReader(raw))
		if err != nil {
			continue
		}
		win, err := analyzeSmartCropV2Frame(srcW, srcH, targetW, targetH, img)
		if err != nil {
			continue
		}
		byTime[position] = smartCropV2Sample{
			point: cropPathPoint{AtMs: position, X: win.X},
			img:   img,
		}
	}
	minimum := maxInt(2, (len(allowed)+1)/2)
	if len(byTime) < minimum {
		return nil, fmt.Errorf("remote smartcrop decoded %d/%d frames; need at least %d", len(byTime), len(allowed), minimum)
	}
	samples := make([]smartCropV2Sample, 0, len(byTime))
	for _, sample := range byTime {
		samples = append(samples, sample)
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].point.AtMs < samples[j].point.AtMs })
	return samples, nil
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

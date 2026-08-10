package main

import (
	"bytes"
	"context"
	"encoding/base64"
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

const (
	remoteSmartCropMarker       = "APTEVA_SMARTCROP_FRAME:"
	remoteSmartCropDetailMarker = "APTEVA_SMARTCROP_DETAIL:"
	// 320 px is enough for generic saliency, but it drops small/profile faces
	// during an upright-to-reclining transition. The released 640 px view stays
	// unchanged for HD sources; 4K sources use 960 px so a similarly sized
	// distant/profile face reaches the detector with equivalent detail.
	remoteSmartCropAnalysisWidth   = 320
	remoteSmartCropAnalysisQuality = 3
	remoteSmartCropDetailWidth     = 640
	remoteSmartCropDetailQuality   = 7
	remoteSmartCropExtendedWidth   = 960
	remoteSmartCropExtendedQuality = 9
	remoteSmartCropBatchSize       = 10
	remoteSmartCropMaxOutputBytes  = 900 * 1024
)

func smartCropDetailSpec(srcW int) (width, quality int) {
	if srcW >= 3000 {
		return remoteSmartCropExtendedWidth, remoteSmartCropExtendedQuality
	}
	return remoteSmartCropDetailWidth, remoteSmartCropDetailQuality
}

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
		// cheap (at most 32 bounded analysis/detail pairs, transferred in small
		// batches) and avoids both a full source download and permanently storing a
		// denser storyboard.
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
	positions = uniqueSortedSmartCropPositions(append([]int64(nil), positions...))
	all := make([]smartCropV2Sample, 0, len(positions))
	for batchIndex, batch := range smartCropRemotePositionBatches(positions) {
		script, scriptErr := buildRemoteSmartCropSampleScript(paths.FFmpeg, signedURL, batch, srcW)
		if scriptErr != nil {
			return nil, scriptErr
		}
		timeoutS := maxInt(90, len(batch)*15)
		if timeoutS > 600 {
			timeoutS = 600
		}
		out, exit, runErr := runRemote(ctx, app, hostID, script, timeoutS)
		if runErr != nil {
			return nil, fmt.Errorf("remote smartcrop samples batch %d: %w (output: %s)",
				batchIndex+1, runErr, truncate(out, 600))
		}
		if exit != 0 {
			return nil, fmt.Errorf("remote smartcrop samples batch %d exit=%d: %s",
				batchIndex+1, exit, truncate(out, 600))
		}
		samples, parseErr := parseRemoteSmartCropSamples(out, batch, srcW, srcH, targetW, targetH)
		if parseErr != nil {
			return nil, fmt.Errorf("remote smartcrop samples batch %d: %w", batchIndex+1, parseErr)
		}
		all = append(all, samples...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].point.AtMs < all[j].point.AtMs })
	return all, nil
}

func smartCropRemotePositionBatches(positions []int64) [][]int64 {
	batches := make([][]int64, 0, (len(positions)+remoteSmartCropBatchSize-1)/remoteSmartCropBatchSize)
	for start := 0; start < len(positions); {
		end := minInt(len(positions), start+remoteSmartCropBatchSize)
		// The remote protocol requires at least two decoded samples. Fold a lone
		// remainder into the preceding batch instead of creating a doomed call.
		if len(positions)-end == 1 {
			end++
		}
		batches = append(batches, positions[start:end])
		start = end
	}
	return batches
}

func buildRemoteSmartCropSampleScript(ffmpegPath, signedURL string, positions []int64, srcW int) (string, error) {
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
	var b strings.Builder
	detailWidth, detailQuality := smartCropDetailSpec(srcW)
	b.WriteString("set -u\n")
	b.WriteString("WORK=$(mktemp -d /tmp/apteva-smartcrop-samples-XXXXXX)\n")
	b.WriteString("trap 'rm -rf \"$WORK\"' EXIT\n")
	fmt.Fprintf(&b, "FFMPEG=%s\n", shellQuote(ffmpegPath))
	fmt.Fprintf(&b, "SIGNED_URL=%s\n", shellQuote(signedURL))
	b.WriteString("extract_one() {\n")
	b.WriteString("  POS_MS=$1\n")
	b.WriteString("  POS_SEC=$(awk -v p=\"$POS_MS\" 'BEGIN{printf \"%.3f\", p/1000}')\n")
	fmt.Fprintf(&b, "  \"$FFMPEG\" -nostdin -y -loglevel error -ss \"$POS_SEC\" -i \"$SIGNED_URL\" -filter_complex '[0:v]split=2[a][b];[a]scale=%d:-2[analysis];[b]scale=%d:-2[detail]' -map '[analysis]' -frames:v 1 -q:v %d \"$WORK/$POS_MS.analysis.jpg\" -map '[detail]' -frames:v 1 -q:v %d \"$WORK/$POS_MS.detail.jpg\" >/dev/null 2>&1 || true\n",
		remoteSmartCropAnalysisWidth, detailWidth,
		remoteSmartCropAnalysisQuality, detailQuality)
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
	// Instances caps command output at roughly 1 MiB. The released 320px/q3
	// analysis frame must never be recompressed because that would change generic
	// saliency decisions. Only the supplemental detail frame is compressed more
	// aggressively when base64 expansion could cross the cap.
	b.WriteString("TOTAL_BYTES=$(wc -c \"$WORK\"/*.jpg 2>/dev/null | tail -1 | awk '{print $1}')\n")
	b.WriteString("if [ \"${TOTAL_BYTES:-0}\" -gt 620000 ]; then\n")
	b.WriteString("  for SOURCE in \"$WORK\"/*.detail.jpg; do\n")
	b.WriteString("    [ -s \"$SOURCE\" ] || continue\n")
	b.WriteString("    SMALL=\"$SOURCE.small.jpg\"\n")
	// Retain the pixels needed by the face detector; reducing JPEG quality is
	// safer than shrinking back below the resolution that triggered this pass.
	b.WriteString("    \"$FFMPEG\" -nostdin -y -loglevel error -i \"$SOURCE\" -frames:v 1 -q:v 14 \"$SMALL\" >/dev/null 2>&1 && mv \"$SMALL\" \"$SOURCE\"\n")
	b.WriteString("  done\n")
	b.WriteString("fi\n")
	b.WriteString("COUNT=0\n")
	b.WriteString("for POS_MS in")
	for _, position := range positions {
		fmt.Fprintf(&b, " %d", position)
	}
	b.WriteString("; do\n")
	b.WriteString("  [ -s \"$WORK/$POS_MS.analysis.jpg\" ] || continue\n")
	b.WriteString("  [ -s \"$WORK/$POS_MS.detail.jpg\" ] || continue\n")
	fmt.Fprintf(&b, "  printf '%s%%s:' \"$POS_MS\"\n", remoteSmartCropMarker)
	b.WriteString("  base64 < \"$WORK/$POS_MS.analysis.jpg\" | tr -d '\\r\\n'\n")
	b.WriteString("  printf '\\n'\n")
	fmt.Fprintf(&b, "  printf '%s%%s:' \"$POS_MS\"\n", remoteSmartCropDetailMarker)
	b.WriteString("  base64 < \"$WORK/$POS_MS.detail.jpg\" | tr -d '\\r\\n'\n")
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
	type remoteFramePair struct {
		analysis image.Image
		detail   image.Image
	}
	pairs := make(map[int64]remoteFramePair, len(positions))
	for _, line := range strings.Split(out, "\n") {
		marker := ""
		detail := false
		switch {
		case strings.HasPrefix(line, remoteSmartCropMarker):
			marker = remoteSmartCropMarker
		case strings.HasPrefix(line, remoteSmartCropDetailMarker):
			marker = remoteSmartCropDetailMarker
			detail = true
		default:
			continue
		}
		fields := strings.SplitN(strings.TrimPrefix(line, marker), ":", 2)
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
		pair := pairs[position]
		if detail {
			pair.detail = img
		} else {
			pair.analysis = img
		}
		pairs[position] = pair
	}
	byTime := make(map[int64]smartCropV2Sample, len(pairs))
	for position, pair := range pairs {
		win, face, detailedFace, err := analyzeSmartCropSourceFrame(srcW, srcH, targetW, targetH,
			pair.analysis, pair.detail)
		if err != nil {
			continue
		}
		byTime[position] = smartCropV2Sample{
			point: cropPathPoint{AtMs: position, X: win.X}, img: pair.analysis,
			face: face, detailedFace: detailedFace,
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
			analysisPath := filepath.Join(dir, fmt.Sprintf("%02d.analysis.jpg", i))
			detailPath := filepath.Join(dir, fmt.Sprintf("%02d.detail.jpg", i))
			detailWidth, detailQuality := smartCropDetailSpec(srcW)
			if err := extractSmartCropFramePair(ctx, ffmpegPath, input,
				analysisPath, detailPath, float64(pos)/1000, detailWidth, detailQuality); err != nil {
				errs[i] = err
				return
			}
			analysisImg, err := decodeSmartCropFrame(analysisPath)
			if err != nil {
				errs[i] = err
				return
			}
			detailImg, err := decodeSmartCropFrame(detailPath)
			if err != nil {
				errs[i] = err
				return
			}
			win, face, detailedFace, err := analyzeSmartCropSourceFrame(srcW, srcH, targetW, targetH,
				analysisImg, detailImg)
			if err != nil {
				errs[i] = err
				return
			}
			results[i] = &smartCropV2Sample{
				point: cropPathPoint{AtMs: pos, X: win.X}, img: analysisImg,
				face: face, detailedFace: detailedFace,
			}
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

// analyzeSmartCropSourceFrame feeds the released 320px/q3 frame unchanged to
// generic saliency and temporal analysis while giving only the CPU face
// detector a higher-resolution view. Resizing the detail JPEG in Go is close but
// not pixel-identical to FFmpeg's original 320px frame and proved sufficient to
// alter stable crops, so the two bounded views are extracted in one decode.
func analyzeSmartCropSourceFrame(
	srcW, srcH, targetW, targetH int,
	analysisImg, detailImg image.Image,
) (*cropWindow, *smartCropFace, *smartCropFace, error) {
	if analysisImg == nil || detailImg == nil {
		return nil, nil, nil, fmt.Errorf("invalid smartcrop source frame pair")
	}
	win, face, err := analyzeSmartCropV2FrameDetailed(srcW, srcH, targetW, targetH, analysisImg)
	if err != nil {
		return nil, nil, nil, err
	}
	var detailedFace *smartCropFace
	if _, detected, ok := faceAwareNarrowSmartCropX(detailImg, win.X, srcW, srcH, win.W); ok {
		detailedFace = supportedSmartCropDetailedFace(detected, win)
	}
	return win, face, detailedFace, nil
}

func supportedSmartCropDetailedFace(detected smartCropFace, win *cropWindow) *smartCropFace {
	if win != nil {
		// The rotated cascade can weakly resemble a knee or torso. A real face
		// entering/leaving a portrait crop is close to the generic subject window;
		// an unrelated body false-positive is commonly another half-frame away.
		// Admit a one-third-window halo so fast falls still reacquire the head.
		halo := win.W / 3
		if detected.CenterX >= win.X-halo && detected.CenterX <= win.X+win.W+halo {
			return &detected
		}
	}
	return nil
}

func decodeSmartCropFrame(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

func extractSmartCropFramePair(
	ctx context.Context,
	ffmpegPath, input, analysisOutput, detailOutput string,
	seekSeconds float64, detailWidth, detailQuality int,
) error {
	cctx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	filter := fmt.Sprintf("[0:v]split=2[a][b];[a]scale=%d:-2[analysis];[b]scale=%d:-2[detail]",
		remoteSmartCropAnalysisWidth, detailWidth)
	args := []string{
		"-y", "-loglevel", "error", "-ss", fmt.Sprintf("%.3f", seekSeconds), "-i", input,
		"-filter_complex", filter,
		"-map", "[analysis]", "-frames:v", "1", "-q:v", strconv.Itoa(remoteSmartCropAnalysisQuality), analysisOutput,
		"-map", "[detail]", "-frames:v", "1", "-q:v", strconv.Itoa(detailQuality), detailOutput,
	}
	out, err := exec.CommandContext(cctx, ffmpegPath, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("ffmpeg smart crop frame pair @%.3fs: %w: %s", seekSeconds, err, strings.TrimSpace(string(out)))
	}
	return nil
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

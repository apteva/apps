package main

// Smart Crop v2 keeps the inexpensive cached-frame design, but stops
// treating an entire reel as one still image. Dense storyboard frames
// are analysed independently and converted into either:
//
//   - one fixed crop when the interesting region stays put; or
//   - a short, smoothed crop path when it moves.
//
// Dense indexes stay on the cached fast path. Old, capped, or sparse
// indexes get a bounded temporary storyboard sampled directly from the
// source around the requested output; those screenshots are deleted after
// analysis and do not multiply stored derivatives on long videos.

import (
	"context"
	"fmt"
	"image"
	"math"
	"sort"
	"sync"

	sdk "github.com/apteva/app-sdk"
	"github.com/muesli/smartcrop"
	"github.com/muesli/smartcrop/nfnt"
)

const (
	smartCropV2MaxSamples           = 24
	smartCropV2MaxParallelDownloads = 4
	smartCropTrackingIntervalMs     = int64(1000)
	smartCropTrackingMaxExtraFrames = 32
	// The 120-frame storyboard cap stretches five-second sampling to about
	// eleven seconds on 20-minute sources. Twelve seconds admits that capped
	// cadence while continuing to reject legacy 30-second storyboards.
	smartCropV2MaxGapMs = int64(12000)
)

// cropPathPoint is persisted only in the in-memory render params. AtMs
// uses source-timeline milliseconds; renderops converts it to filter
// time after subtracting the reel start.
type cropPathPoint struct {
	AtMs int64 `json:"at_ms"`
	X    int   `json:"x"`
	Cut  bool  `json:"cut,omitempty"`
}

type smartCropV2Sample struct {
	point         cropPathPoint
	img           image.Image
	motionTracked bool
	temporalTrack bool
}

// computeSmartCropStillV2 reframes both ordinary images and exact video
// frames. Images need one canonical thumbnail. Timed video frames use the
// storyboard frames bracketing the requested timestamp and interpolate their
// crop positions. Up to nine nearby cached frames also provide a conservative
// temporal subject consensus. It suppresses static, high-saliency backgrounds
// (for example blinds) but only overrides a large, high-confidence disagreement.
func computeSmartCropStillV2(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, sourceFileID string,
	targetW, targetH int,
	target smartCropTarget,
) (*cropWindow, error) {
	row, err := getMedia(app.AppDB(), projectID, sourceFileID)
	if err != nil {
		return nil, fmt.Errorf("smartcrop v2 get source: %w", err)
	}
	if row == nil || row.Width <= 0 || row.Height <= 0 {
		return nil, fmt.Errorf("smartcrop v2: source dimensions unavailable")
	}
	cw, ch := cropDimsForRatio(row.Width, row.Height, targetW, targetH)
	if cw <= 0 || ch <= 0 {
		return nil, fmt.Errorf("smartcrop v2: invalid crop dimensions")
	}
	if cw == row.Width && ch == row.Height {
		return &cropWindow{W: cw, H: ch}, nil
	}
	validDerivations, err := resolveValidDerivations(ctx, sc, projectID, row.Derivations)
	if err != nil {
		return nil, err
	}
	row.Derivations = validDerivations

	var derivs []DerivationRow
	sampleSource := "storyboard"
	if target.PreferKeyframe {
		if smartCropStoryboardDenseAtFocus(row.Derivations, target.FocusMs) {
			derivs = selectSmartCropStillDerivations(row.Derivations, target.FocusMs)
		}
	} else if d := pickSmartCropDerivation(row.Derivations, target); d.StorageFileID != "" {
		derivs = []DerivationRow{d}
	}
	var samples []smartCropV2Sample
	if target.PreferKeyframe && len(derivs) == 0 {
		sampleSource = "source"
		positions := smartCropSupplementPositions(target, row.DurationMs)
		var sampleErr error
		samples, sampleErr = analyzeSmartCropV2Source(ctx, app, sc, projectID, sourceFileID,
			positions, row.Width, row.Height, targetW, targetH)
		if sampleErr != nil {
			return nil, fmt.Errorf("smartcrop v2 sample source: %w", sampleErr)
		}
	} else {
		if len(derivs) == 0 {
			return nil, fmt.Errorf("smartcrop v2: no usable frame derivation")
		}
		samples = analyzeSmartCropV2Derivations(ctx, sc, projectID, derivs, row.Width, row.Height, targetW, targetH)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("smartcrop v2: no decodable frame derivation")
	}

	// Resolve the broader storyboard evidence before an exact tracking pass.
	// If the exact +/-500ms frames contain too little motion, this context is a
	// safer fallback than snapping back to a high-contrast room feature.
	contextSamples := smartCropSceneSamples(samples, target.FocusMs)
	contextTemporal, contextTemporalOK := bestSmartCropTemporalConsensus(contextSamples, row.Width, cw)

	// A cached five-second storyboard is ideal for stable shots, but it cannot
	// locate a person accurately while they cross the frame. When the two
	// bracketing analyses disagree materially, pay for only three temporary
	// source frames around the requested instant. The exact frame becomes the
	// primary crop and its close neighbours provide motion evidence. Stable
	// images and video frames remain on the existing cached fast path.
	trackingStill := false
	if target.PreferKeyframe && smartCropStillNeedsTracking(samples, target.FocusMs, cw) {
		positions := smartCropStillTrackingPositions(target.FocusMs, row.DurationMs)
		tracked, sampleErr := analyzeSmartCropV2Source(ctx, app, sc, projectID, sourceFileID,
			positions, row.Width, row.Height, targetW, targetH)
		if sampleErr == nil && len(tracked) >= 2 {
			samples = tracked
			sampleSource = "source-tracking"
			markSmartCropSceneCuts(samples)
			refineSmartCropMotionSamples(samples, row.Width, cw)
			refineSmartCropHeadSamples(samples, row.Width, cw)
			trackingStill = true
		} else if sampleErr != nil {
			app.Logger().Info("smartcrop v2 exact tracking unavailable",
				"file_id", sourceFileID, "focus_ms", target.FocusMs, "err", sampleErr.Error())
		}
	}

	x, method, err := resolveSmartCropStillBase(samples, target.FocusMs)
	if err != nil {
		return nil, err
	}
	temporal := smartCropTemporalResult{}
	if !trackingStill {
		result, ok := contextTemporal, contextTemporalOK
		if ok {
			temporal = result
			if corrected, changed := applySmartCropTemporalOverride(x, result, cw, row.Width); changed {
				x = corrected
				method += "+temporal"
			}
		}
	} else if contextTemporalOK && smartCropTemporalResultConfident(contextTemporal) &&
		(contextTemporal.StaticAnchored || !smartCropStillHasMotionEvidence(samples, target.FocusMs)) {
		temporal = contextTemporal
		if corrected, changed := applySmartCropTemporalOverride(x, contextTemporal, cw, row.Width); changed {
			x = corrected
			method += "+context"
		}
	} else if contextTemporalOK && smartCropTemporalResultConfident(contextTemporal) {
		// Exact +/-500ms motion is useful for a crossing person, but a thin arm
		// can be the only changing region and pull the crop away from the torso.
		// Bound that local answer by half a crop around the broader, confident
		// storyboard subject region.
		maxDrift := cw / 2
		bounded := clampInt(x, contextTemporal.X-maxDrift, contextTemporal.X+maxDrift)
		bounded = clampInt(roundEven(bounded), 0, row.Width-cw)
		if bounded != x {
			x = bounded
			method += "+context-bound"
		}
	}
	if sample := nearestSmartCropSample(samples, target.FocusMs); sample != nil {
		bounds := sample.img.Bounds()
		thumbCropW := clampInt(int(math.Round(float64(cw)*float64(bounds.Dx())/float64(row.Width))), 1, bounds.Dx())
		currentStart := clampInt(int(math.Round(float64(x)*float64(bounds.Dx())/float64(row.Width))), 0, bounds.Dx()-thumbCropW)
		strongX, _, strong := strongSmartCropSilhouetteX(sample.img, row.Width, cw, thumbCropW, currentStart)
		if strong && contextTemporalOK && smartCropTemporalResultConfident(contextTemporal) &&
			absInt(strongX-contextTemporal.X) <= cw/2 {
			x = strongX
			method += "+silhouette"
		} else if corrected, changed := silhouetteAwareNarrowSmartCropX(sample.img, x, row.Width, cw, thumbCropW); changed {
			x = corrected
			method += "+silhouette"
		}
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, row.Width, cw); changed {
			x = corrected
			method += "+head"
		}
	}
	x = clampInt(roundEven(x), 0, row.Width-cw)
	app.Logger().Info("smartcrop v2 resolved still",
		"file_id", sourceFileID,
		"samples", len(samples),
		"sample_source", sampleSource,
		"method", method,
		"focus_ms", target.FocusMs,
		"crop_w", cw,
		"crop_h", ch,
		"crop_x", x,
		"temporal_samples", temporal.Samples,
		"temporal_concentration", temporal.Concentration,
		"temporal_mean_activity", temporal.MeanActivity,
		"temporal_active_fraction", temporal.ActiveFraction)
	return &cropWindow{W: cw, H: ch, X: x, Y: 0}, nil
}

func selectSmartCropStillDerivations(derivs []DerivationRow, focusMs int64) []DerivationRow {
	keyframes := make([]DerivationRow, 0, len(derivs))
	closeEnough := false
	for _, d := range derivs {
		if d.Kind != "keyframe" || d.Status != "ok" || d.StorageFileID == "" {
			continue
		}
		keyframes = append(keyframes, d)
		if absInt64(d.PositionMs-focusMs) <= smartCropV2MaxGapMs {
			closeEnough = true
		}
	}
	if !closeEnough {
		return nil
	}
	sort.Slice(keyframes, func(i, j int) bool {
		di := absInt64(keyframes[i].PositionMs - focusMs)
		dj := absInt64(keyframes[j].PositionMs - focusMs)
		if di == dj {
			return keyframes[i].PositionMs < keyframes[j].PositionMs
		}
		return di < dj
	})
	if len(keyframes) > smartCropTemporalMaxSamples {
		keyframes = keyframes[:smartCropTemporalMaxSamples]
	}
	sort.Slice(keyframes, func(i, j int) bool { return keyframes[i].PositionMs < keyframes[j].PositionMs })
	return keyframes
}

func analyzeSmartCropV2Derivations(
	ctx context.Context,
	sc *storageClient,
	projectID string,
	derivs []DerivationRow,
	srcW, srcH, targetW, targetH int,
) []smartCropV2Sample {
	results := make([]*smartCropV2Sample, len(derivs))
	sem := make(chan struct{}, smartCropV2MaxParallelDownloads)
	var wg sync.WaitGroup
	for i, d := range derivs {
		i, d := i, d
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			img, err := downloadAndDecodeImage(ctx, sc, projectID, d.StorageFileID)
			if err != nil {
				return
			}
			win, err := analyzeSmartCropV2Frame(srcW, srcH, targetW, targetH, img)
			if err != nil {
				return
			}
			results[i] = &smartCropV2Sample{
				point: cropPathPoint{AtMs: d.PositionMs, X: win.X},
				img:   img,
			}
		}()
	}
	wg.Wait()
	samples := make([]smartCropV2Sample, 0, len(results))
	for _, sample := range results {
		if sample != nil {
			samples = append(samples, *sample)
		}
	}
	return samples
}

func resolveSmartCropStillBase(samples []smartCropV2Sample, focusMs int64) (int, string, error) {
	var before, after *smartCropV2Sample
	for i := range samples {
		sample := &samples[i]
		if sample.point.AtMs <= focusMs && (before == nil || sample.point.AtMs > before.point.AtMs) {
			before = sample
		}
		if sample.point.AtMs >= focusMs && (after == nil || sample.point.AtMs < after.point.AtMs) {
			after = sample
		}
	}
	if before != nil && focusMs-before.point.AtMs > smartCropV2MaxGapMs {
		before = nil
	}
	if after != nil && after.point.AtMs-focusMs > smartCropV2MaxGapMs {
		after = nil
	}
	if before == nil && after == nil {
		return 0, "", fmt.Errorf("smartcrop v2: no usable frame near requested timestamp")
	}
	if before == nil {
		return after.point.X, "single-after", nil
	}
	if after == nil || before.point.AtMs == after.point.AtMs {
		return before.point.X, "single-before", nil
	}
	if sceneCutScore(before.img, after.img) >= smartCropSceneCutThreshold {
		if absInt64(after.point.AtMs-focusMs) < absInt64(focusMs-before.point.AtMs) {
			return after.point.X, "scene-cut-nearest", nil
		}
		return before.point.X, "scene-cut-nearest", nil
	}
	return interpolateSmartCropStillX(before.point, after.point, focusMs), "interpolated", nil
}

func interpolateSmartCropStillX(before, after cropPathPoint, focusMs int64) int {
	if after.AtMs <= before.AtMs || focusMs <= before.AtMs {
		return before.X
	}
	if focusMs >= after.AtMs {
		return after.X
	}
	t := float64(focusMs-before.AtMs) / float64(after.AtMs-before.AtMs)
	return roundEven(int(math.Round(float64(before.X) + float64(after.X-before.X)*t)))
}

func computeSmartCropReelV2(
	ctx context.Context,
	app *sdk.AppCtx,
	sc *storageClient,
	projectID, sourceFileID string,
	targetW, targetH int,
	target smartCropTarget,
) (*cropWindow, []cropPathPoint, error) {
	if !target.HasRange() {
		return nil, nil, fmt.Errorf("smartcrop v2: reel range is missing")
	}
	row, err := getMedia(app.AppDB(), projectID, sourceFileID)
	if err != nil {
		return nil, nil, fmt.Errorf("smartcrop v2 get source: %w", err)
	}
	if row == nil || row.Width <= 0 || row.Height <= 0 {
		return nil, nil, fmt.Errorf("smartcrop v2: source dimensions unavailable")
	}
	cw, ch := cropDimsForRatio(row.Width, row.Height, targetW, targetH)
	if cw <= 0 || ch <= 0 {
		return nil, nil, fmt.Errorf("smartcrop v2: invalid crop dimensions")
	}
	if cw == row.Width && ch == row.Height {
		return &cropWindow{W: cw, H: ch}, nil, nil
	}
	validDerivations, err := resolveValidDerivations(ctx, sc, projectID, row.Derivations)
	if err != nil {
		return nil, nil, err
	}
	row.Derivations = validDerivations

	uncapped := selectSmartCropReelDerivationsUncapped(row.Derivations, target)
	var samples []smartCropV2Sample
	sampleSource := "storyboard"
	if smartCropSamplesAreDense(uncapped, target) {
		derivs := capSmartCropReelDerivations(uncapped)
		samples = analyzeSmartCropV2Derivations(ctx, sc, projectID, derivs, row.Width, row.Height, targetW, targetH)
	} else {
		sampleSource = "source"
		positions := smartCropSupplementPositions(target, row.DurationMs)
		var sampleErr error
		samples, sampleErr = analyzeSmartCropV2Source(ctx, app, sc, projectID, sourceFileID,
			positions, row.Width, row.Height, targetW, targetH)
		if sampleErr != nil {
			return nil, nil, fmt.Errorf("smartcrop v2 sample source: %w", sampleErr)
		}
	}
	markSmartCropSceneCuts(samples)
	trackingFrames := 0
	if smartCropReelNeedsTracking(samples, row.Width, cw) {
		positions := smartCropAdaptiveTrackingPositions(samples, target, row.DurationMs, row.Width, cw)
		if len(positions) >= 2 {
			extra, sampleErr := analyzeSmartCropV2Source(ctx, app, sc, projectID, sourceFileID,
				positions, row.Width, row.Height, targetW, targetH)
			if sampleErr == nil {
				trackingFrames = len(extra)
				samples = mergeSmartCropSamples(samples, extra)
				sampleSource += "+tracking"
				markSmartCropSceneCuts(samples)
				refineSmartCropMotionSamples(samples, row.Width, cw)
				refineSmartCropHeadSamples(samples, row.Width, cw)
				fillSmartCropMotionGaps(samples, row.Width, cw)
			} else {
				app.Logger().Info("smartcrop v2 adaptive tracking unavailable",
					"file_id", sourceFileID, "err", sampleErr.Error())
			}
		}
	}
	if len(samples) < 2 {
		return nil, nil, fmt.Errorf("smartcrop v2: fewer than two usable samples")
	}
	temporalCorrections := correctSmartCropReelTemporalOutliers(samples, row.Width, cw)
	headCorrections := refineSmartCropHeadSamples(samples, row.Width, cw)

	path := make([]cropPathPoint, 0, len(samples)+2)
	for _, sample := range samples {
		path = append(path, sample.point)
	}
	path = anchorSmartCropPath(path, target.StartMs, target.EndMs)
	path = stabilizeSmartCropPath(path, cw, row.Width)
	if len(path) == 0 {
		return nil, nil, fmt.Errorf("smartcrop v2: empty crop path")
	}

	if x, ok := staticSmartCropPathX(path, cw); ok {
		app.Logger().Info("smartcrop v2 resolved static reel",
			"file_id", sourceFileID, "samples", len(samples),
			"sample_source", sampleSource,
			"crop_w", cw, "crop_h", ch, "crop_x", x,
			"temporal_corrections", temporalCorrections,
			"head_corrections", headCorrections,
			"tracking_frames", trackingFrames)
		return &cropWindow{W: cw, H: ch, X: x, Y: 0}, nil, nil
	}

	app.Logger().Info("smartcrop v2 resolved tracked reel",
		"file_id", sourceFileID, "samples", len(samples),
		"sample_source", sampleSource,
		"path_points", len(path), "crop_w", cw, "crop_h", ch,
		"temporal_corrections", temporalCorrections,
		"head_corrections", headCorrections,
		"tracking_frames", trackingFrames)
	return &cropWindow{W: cw, H: ch, X: path[0].X, Y: 0}, path, nil
}

// analyzeSmartCropV2Frame preserves v1's generic saliency so animals,
// animation and product shots keep working, then applies the released
// warm-subject guard to narrow social crops. The difference is that v2
// runs it for every relevant storyboard frame instead of only one.
func analyzeSmartCropV2Frame(srcW, srcH, targetW, targetH int, img image.Image) (*cropWindow, error) {
	if img == nil || srcW <= 0 || srcH <= 0 {
		return nil, fmt.Errorf("invalid smartcrop v2 frame")
	}
	cw, ch := cropDimsForRatio(srcW, srcH, targetW, targetH)
	centerX := roundEven((srcW - cw) / 2)
	centerY := roundEven((srcH - ch) / 2)
	b := img.Bounds()
	tw, th := b.Dx(), b.Dy()
	if tw <= 0 || th <= 0 || cw == srcW && ch == srcH {
		return &cropWindow{W: cw, H: ch, X: centerX, Y: centerY}, nil
	}
	tcw, tch := cropDimsForRatio(tw, th, targetW, targetH)
	analyzer := smartcrop.NewAnalyzer(nfnt.NewDefaultResizer())
	rect, err := analyzer.FindBestCrop(img, tcw, tch)
	if err != nil {
		return nil, err
	}
	rawX := clampInt(int(float64(rect.Min.X)*float64(srcW)/float64(tw)), 0, srcW-cw)
	rawY := clampInt(int(float64(rect.Min.Y)*float64(srcH)/float64(th)), 0, srcH-ch)
	x, y := stabilizeNarrowSmartCrop(rawX, rawY, srcW, srcH, cw, ch)
	subjectChanged := false
	if subjectX, ok := subjectAwareNarrowSmartCropX(img, rawX, x, srcW, srcH, cw, ch, tcw); ok {
		x = subjectX
		subjectChanged = true
	}
	if silhouetteX, ok := silhouetteAwareNarrowSmartCropX(img, x, srcW, cw, tcw); ok && !subjectChanged {
		x = silhouetteX
	}
	if headX, ok := headAwareNarrowSmartCropX(img, x, srcW, cw); ok {
		x = headX
	}
	return &cropWindow{W: cw, H: ch, X: roundEven(x), Y: roundEven(y)}, nil
}

func smartCropStillHasMotionEvidence(samples []smartCropV2Sample, focusMs int64) bool {
	for _, sample := range samples {
		if sample.motionTracked && absInt64(sample.point.AtMs-focusMs) <= 750 {
			return true
		}
	}
	return false
}

func nearestSmartCropSample(samples []smartCropV2Sample, focusMs int64) *smartCropV2Sample {
	if len(samples) == 0 {
		return nil
	}
	nearest := 0
	for i := 1; i < len(samples); i++ {
		if absInt64(samples[i].point.AtMs-focusMs) < absInt64(samples[nearest].point.AtMs-focusMs) {
			nearest = i
		}
	}
	return &samples[nearest]
}

func smartCropStillNeedsTracking(samples []smartCropV2Sample, focusMs int64, cropW int) bool {
	if cropW <= 0 {
		return false
	}
	var before, after *smartCropV2Sample
	for i := range samples {
		sample := &samples[i]
		if sample.point.AtMs < focusMs && (before == nil || sample.point.AtMs > before.point.AtMs) {
			before = sample
		}
		if sample.point.AtMs > focusMs && (after == nil || sample.point.AtMs < after.point.AtMs) {
			after = sample
		}
	}
	return before != nil && after != nil && absInt(before.point.X-after.point.X) > maxInt(96, cropW/4)
}

func smartCropStillTrackingPositions(focusMs, durationMs int64) []int64 {
	lastSeek := durationMs - 100
	if lastSeek < 0 {
		lastSeek = 0
	}
	positions := []int64{focusMs - 500, focusMs, focusMs + 500}
	for i := range positions {
		if positions[i] < 0 {
			positions[i] = 0
		}
		if durationMs > 0 && positions[i] > lastSeek {
			positions[i] = lastSeek
		}
	}
	return uniqueSortedSmartCropPositions(positions)
}

func markSmartCropSceneCuts(samples []smartCropV2Sample) {
	for i := range samples {
		samples[i].point.Cut = i > 0 && sceneCutScore(samples[i-1].img, samples[i].img) >= smartCropSceneCutThreshold
	}
}

func smartCropReelNeedsTracking(samples []smartCropV2Sample, srcW, cropW int) bool {
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && !samples[end].point.Cut {
			end++
		}
		scene := samples[start:end]
		if smartCropSceneRawRange(scene) > maxInt(96, cropW/3) {
			return true
		}
		if result, ok := temporalSubjectConsensus(scene, srcW, cropW); ok &&
			result.Concentration >= smartCropTemporalDenseMinConcentration &&
			result.MeanActivity >= smartCropTemporalDenseMinMeanActivity &&
			result.ActiveFraction >= smartCropTemporalDenseMinActiveFraction {
			return true
		}
		start = end
	}
	return false
}

func smartCropAdaptiveTrackingPositions(samples []smartCropV2Sample, target smartCropTarget, durationMs int64, srcW, cropW int) []int64 {
	if len(samples) < 2 || cropW <= 0 {
		return nil
	}
	lastSeek := durationMs - 100
	if lastSeek < 0 {
		lastSeek = 0
	}
	clamp := func(position int64) int64 {
		if position < 0 {
			return 0
		}
		if durationMs > 0 && position > lastSeek {
			return lastSeek
		}
		return position
	}
	positions := []int64{clamp(target.StartMs), clamp(target.EndMs)}
	// Once a reel has proved that it needs source tracking, sample the complete
	// requested interval. Selectively filling only intervals whose saliency X
	// already moved misses a moving person beside a static salient background.
	// Work remains bounded by smartCropTrackingMaxExtraFrames below.
	firstWholeInterval := (target.StartMs/smartCropTrackingIntervalMs + 1) * smartCropTrackingIntervalMs
	for p := firstWholeInterval; p < target.EndMs; p += smartCropTrackingIntervalMs {
		positions = append(positions, clamp(p))
	}
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && !samples[end].point.Cut {
			end++
		}
		scene := samples[start:end]
		rangeMoves := smartCropSceneRawRange(scene) > maxInt(96, cropW/3)
		denseMotion := false
		if result, ok := temporalSubjectConsensus(scene, srcW, cropW); ok {
			denseMotion = result.Concentration >= smartCropTemporalDenseMinConcentration &&
				result.MeanActivity >= smartCropTemporalDenseMinMeanActivity &&
				result.ActiveFraction >= smartCropTemporalDenseMinActiveFraction
		}
		if rangeMoves || denseMotion {
			for i := start; i+1 < end; i++ {
				a, b := samples[i].point.AtMs, samples[i+1].point.AtMs
				if b <= target.StartMs || a >= target.EndMs {
					continue
				}
				// A concentrated moving foreground with a static saliency path
				// needs the whole interval sampled. Otherwise focus the extra
				// work on intervals whose path is actually changing.
				if !denseMotion && absInt(samples[i+1].point.X-samples[i].point.X) <= maxInt(48, cropW/10) {
					continue
				}
				for p := a + smartCropTrackingIntervalMs; p < b; p += smartCropTrackingIntervalMs {
					if p > target.StartMs && p < target.EndMs {
						positions = append(positions, clamp(p))
					}
				}
			}
		}
		start = end
	}
	positions = uniqueSortedSmartCropPositions(positions)
	if len(positions) <= smartCropTrackingMaxExtraFrames {
		return positions
	}
	out := make([]int64, 0, smartCropTrackingMaxExtraFrames)
	for i := 0; i < smartCropTrackingMaxExtraFrames; i++ {
		idx := int(math.Round(float64(i) * float64(len(positions)-1) / float64(smartCropTrackingMaxExtraFrames-1)))
		out = append(out, positions[idx])
	}
	return uniqueSortedSmartCropPositions(out)
}

func smartCropSceneRawRange(samples []smartCropV2Sample) int {
	if len(samples) == 0 {
		return 0
	}
	minX, maxX := samples[0].point.X, samples[0].point.X
	for _, sample := range samples[1:] {
		minX = minInt(minX, sample.point.X)
		maxX = maxInt(maxX, sample.point.X)
	}
	return maxX - minX
}

func mergeSmartCropSamples(base, extra []smartCropV2Sample) []smartCropV2Sample {
	byTime := make(map[int64]smartCropV2Sample, len(base)+len(extra))
	for _, sample := range base {
		byTime[sample.point.AtMs] = sample
	}
	// Exact source frames win over cached storyboard frames at the same time.
	for _, sample := range extra {
		byTime[sample.point.AtMs] = sample
	}
	out := make([]smartCropV2Sample, 0, len(byTime))
	for _, sample := range byTime {
		out = append(out, sample)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].point.AtMs < out[j].point.AtMs })
	return out
}

func refineSmartCropMotionSamples(samples []smartCropV2Sample, srcW, cropW int) int {
	refined := 0
	for i := range samples {
		if samples[i].img == nil {
			continue
		}
		neighbors := make([]image.Image, 0, 2)
		if i > 0 && !samples[i].point.Cut &&
			samples[i].point.AtMs-samples[i-1].point.AtMs <= 2*smartCropTrackingIntervalMs {
			neighbors = append(neighbors, samples[i-1].img)
		}
		if i+1 < len(samples) && !samples[i+1].point.Cut &&
			samples[i+1].point.AtMs-samples[i].point.AtMs <= 2*smartCropTrackingIntervalMs {
			neighbors = append(neighbors, samples[i+1].img)
		}
		bounds := samples[i].img.Bounds()
		thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(bounds.Dx())/float64(srcW))), 1, bounds.Dx())
		x, ok := motionAwareNarrowSmartCropXFromImages(samples[i].img, neighbors,
			samples[i].point.X, srcW, cropW, thumbCropW)
		if ok {
			samples[i].motionTracked = true
			if x != samples[i].point.X {
				samples[i].point.X = x
				refined++
			}
		}
	}
	return refined
}

// fillSmartCropMotionGaps carries motion-certified positions across only a
// short adjacent hole. Exact reel boundaries have a single neighbor and can
// otherwise fall back to unrelated room saliency for the first/last frame;
// an interior dropped frame can produce the same one-second snap. Longer
// gaps remain untouched so this cannot invent a traversal.
func fillSmartCropMotionGaps(samples []smartCropV2Sample, srcW, cropW int) int {
	filled := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		for i := sceneStart; i < sceneEnd; i++ {
			if samples[i].motionTracked {
				continue
			}
			prev, next := i-1, i+1
			prevOK := prev >= sceneStart && samples[prev].motionTracked &&
				samples[i].point.AtMs-samples[prev].point.AtMs <= 2*smartCropTrackingIntervalMs
			nextOK := next < sceneEnd && samples[next].motionTracked &&
				samples[next].point.AtMs-samples[i].point.AtMs <= 2*smartCropTrackingIntervalMs
			var x int
			switch {
			case prevOK && nextOK:
				x = interpolateSmartCropStillX(samples[prev].point, samples[next].point, samples[i].point.AtMs)
			case i == sceneStart && nextOK:
				x = samples[next].point.X
			case i == sceneEnd-1 && prevOK:
				x = samples[prev].point.X
			default:
				continue
			}
			samples[i].point.X = clampInt(roundEven(x), 0, srcW-cropW)
			samples[i].motionTracked = true
			filled++
		}
		sceneStart = sceneEnd
	}
	return filled
}

func refineSmartCropHeadSamples(samples []smartCropV2Sample, srcW, cropW int) int {
	refined := 0
	for i := range samples {
		if samples[i].temporalTrack {
			continue
		}
		if x, ok := headAwareNarrowSmartCropX(samples[i].img, samples[i].point.X, srcW, cropW); ok {
			samples[i].point.X = x
			refined++
		}
	}
	return refined
}

func selectSmartCropReelDerivations(derivs []DerivationRow, target smartCropTarget) []DerivationRow {
	return capSmartCropReelDerivations(selectSmartCropReelDerivationsUncapped(derivs, target))
}

func selectSmartCropReelDerivationsUncapped(derivs []DerivationRow, target smartCropTarget) []DerivationRow {
	keyframes := make([]DerivationRow, 0)
	for _, d := range derivs {
		if d.Kind == "keyframe" && d.Status == "ok" && d.StorageFileID != "" {
			keyframes = append(keyframes, d)
		}
	}
	sort.Slice(keyframes, func(i, j int) bool { return keyframes[i].PositionMs < keyframes[j].PositionMs })
	if len(keyframes) == 0 {
		return nil
	}

	selected := make([]DerivationRow, 0)
	var before, after DerivationRow
	for _, d := range keyframes {
		switch {
		case d.PositionMs < target.StartMs:
			before = d
		case d.PositionMs > target.EndMs:
			if after.StorageFileID == "" {
				after = d
			}
		default:
			selected = append(selected, d)
		}
	}
	if before.StorageFileID != "" {
		selected = append([]DerivationRow{before}, selected...)
	}
	if after.StorageFileID != "" {
		selected = append(selected, after)
	}
	return selected
}

func capSmartCropReelDerivations(selected []DerivationRow) []DerivationRow {
	if len(selected) <= smartCropV2MaxSamples {
		return selected
	}

	// Evenly cap analysis work while keeping both boundaries.
	out := make([]DerivationRow, 0, smartCropV2MaxSamples)
	for i := 0; i < smartCropV2MaxSamples; i++ {
		idx := int(math.Round(float64(i) * float64(len(selected)-1) / float64(smartCropV2MaxSamples-1)))
		if len(out) == 0 || out[len(out)-1].PositionMs != selected[idx].PositionMs {
			out = append(out, selected[idx])
		}
	}
	return out
}

func smartCropStoryboardDenseAtFocus(derivs []DerivationRow, focusMs int64) bool {
	keyframes := make([]DerivationRow, 0, len(derivs))
	for _, d := range derivs {
		if d.Kind == "keyframe" && d.Status == "ok" && d.StorageFileID != "" {
			keyframes = append(keyframes, d)
		}
	}
	if len(keyframes) < 2 {
		return false
	}
	sort.Slice(keyframes, func(i, j int) bool { return keyframes[i].PositionMs < keyframes[j].PositionMs })
	nearest := 0
	for i := 1; i < len(keyframes); i++ {
		if absInt64(keyframes[i].PositionMs-focusMs) < absInt64(keyframes[nearest].PositionMs-focusMs) {
			nearest = i
		}
	}
	if absInt64(keyframes[nearest].PositionMs-focusMs) > smartCropV2MaxGapMs {
		return false
	}
	if nearest > 0 && keyframes[nearest].PositionMs-keyframes[nearest-1].PositionMs > smartCropV2MaxGapMs {
		return false
	}
	if nearest+1 < len(keyframes) && keyframes[nearest+1].PositionMs-keyframes[nearest].PositionMs > smartCropV2MaxGapMs {
		return false
	}
	return true
}

func smartCropSamplesAreDense(samples []DerivationRow, target smartCropTarget) bool {
	if len(samples) < 2 {
		return false
	}
	if samples[0].PositionMs-target.StartMs > smartCropV2MaxGapMs ||
		target.StartMs-samples[0].PositionMs > smartCropV2MaxGapMs {
		return false
	}
	last := samples[len(samples)-1].PositionMs
	if last-target.EndMs > smartCropV2MaxGapMs || target.EndMs-last > smartCropV2MaxGapMs {
		return false
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].PositionMs-samples[i-1].PositionMs > smartCropV2MaxGapMs {
			return false
		}
	}
	return true
}

func anchorSmartCropPath(path []cropPathPoint, startMs, endMs int64) []cropPathPoint {
	if len(path) == 0 {
		return nil
	}
	if path[0].AtMs > startMs {
		path = append([]cropPathPoint{{AtMs: startMs, X: path[0].X}}, path...)
	} else {
		path[0].AtMs = startMs
	}
	for len(path) > 1 && path[0].AtMs == path[1].AtMs {
		// The exact boundary sample wins over a preceding storyboard point that
		// was clamped onto the boundary. Keeping both creates an artificial 1ms
		// pan at the beginning of the output.
		path = path[1:]
	}
	last := len(path) - 1
	if path[last].AtMs < endMs {
		path = append(path, cropPathPoint{AtMs: endMs, X: path[last].X})
	} else {
		path[last].AtMs = endMs
	}
	for len(path) > 1 && path[len(path)-2].AtMs == path[len(path)-1].AtMs {
		// Symmetric boundary rule: keep the exact end sample.
		path = path[:len(path)-1]
	}
	return path
}

func stabilizeSmartCropPath(path []cropPathPoint, cropW, srcW int) []cropPathPoint {
	if len(path) < 2 {
		return path
	}
	maxX := srcW - cropW
	original := append([]cropPathPoint(nil), path...)
	// Three-point smoothing stays inside a scene. Cut boundaries are
	// intentionally discontinuous so we do not pan through an edit.
	maxPull := maxInt(16, cropW/10)
	for i := 1; i < len(path)-1; i++ {
		if path[i].Cut || path[i+1].Cut {
			continue
		}
		x := roundEven((original[i-1].X + 2*original[i].X + original[i+1].X) / 4)
		// Smoothing is allowed to remove jitter, not to erase a confident
		// tracking anchor. Dense traversal samples keep this bound invisible;
		// sparse or isolated points can no longer be dragged half a frame away.
		if x < original[i].X-maxPull {
			x = original[i].X - maxPull
		} else if x > original[i].X+maxPull {
			x = original[i].X + maxPull
		}
		path[i].X = roundEven(x)
	}
	deadZone := maxInt(16, cropW/12)
	for i := range path {
		path[i].X = clampInt(roundEven(path[i].X), 0, maxX)
		if i == 0 || path[i].Cut {
			continue
		}
		if absInt(path[i].X-path[i-1].X) <= deadZone {
			path[i].X = path[i-1].X
		}
	}

	// Remove redundant points; interpolation between the remaining
	// anchors is smoother and produces a shorter ffmpeg expression.
	out := make([]cropPathPoint, 0, len(path))
	for i, p := range path {
		if i > 0 && i < len(path)-1 && !p.Cut && !path[i+1].Cut &&
			p.X == path[i-1].X && p.X == path[i+1].X {
			continue
		}
		out = append(out, p)
	}
	return out
}

func staticSmartCropPathX(path []cropPathPoint, cropW int) (int, bool) {
	if len(path) == 0 {
		return 0, false
	}
	xs := make([]int, len(path))
	minX, maxX := path[0].X, path[0].X
	for i, p := range path {
		xs[i] = p.X
		if p.X < minX {
			minX = p.X
		}
		if p.X > maxX {
			maxX = p.X
		}
	}
	if maxX-minX > maxInt(24, cropW/8) {
		return 0, false
	}
	sort.Ints(xs)
	return roundEven(xs[len(xs)/2]), true
}

// sceneCutScore compares a small normalized luminance grid. Subject
// motion changes only part of the grid; a real edit changes most cells.
func sceneCutScore(a, b image.Image) float64 {
	if a == nil || b == nil {
		return 0
	}
	const gw, gh = 16, 9
	diffs := make([]float64, 0, gw*gh)
	for gy := 0; gy < gh; gy++ {
		for gx := 0; gx < gw; gx++ {
			la := sampledLuma(a, gx, gy, gw, gh)
			lb := sampledLuma(b, gx, gy, gw, gh)
			diffs = append(diffs, math.Abs(la-lb)/255.0)
		}
	}
	// Require roughly 70% of the grid to change. A close foreground traversal
	// can have a large mean difference while much of the room remains stable.
	sort.Float64s(diffs)
	return diffs[(len(diffs)-1)*30/100]
}

func sampledLuma(img image.Image, gx, gy, gw, gh int) float64 {
	b := img.Bounds()
	x := b.Min.X + (2*gx+1)*b.Dx()/(2*gw)
	y := b.Min.Y + (2*gy+1)*b.Dy()/(2*gh)
	r, g, bl, _ := img.At(x, y).RGBA()
	return 0.2126*float64(r>>8) + 0.7152*float64(g>>8) + 0.0722*float64(bl>>8)
}

// cropFilterForPath emits a piecewise-linear x(t) expression. Scene
// cuts remain steps; normal movement interpolates smoothly. Commas are
// escaped for libavfilter's option parser (there is no shell involved,
// but the filter graph itself still treats bare commas as separators).
func cropFilterForPath(w, h, y int, startMs int64, path []cropPathPoint) string {
	if len(path) < 2 {
		x := 0
		if len(path) == 1 {
			x = path[0].X
		}
		return fmt.Sprintf("crop=%d:%d:%d:%d", w, h, x, y)
	}
	expr := fmt.Sprintf("%d", path[len(path)-1].X)
	for i := len(path) - 2; i >= 0; i-- {
		cur, next := path[i], path[i+1]
		t0 := math.Max(0, float64(cur.AtMs-startMs)/1000.0)
		t1 := math.Max(t0+0.001, float64(next.AtMs-startMs)/1000.0)
		segment := fmt.Sprintf("%d", cur.X)
		if !next.Cut && next.X != cur.X {
			segment = fmt.Sprintf("%d+(%d)*(t-%.3f)/%.3f", cur.X, next.X-cur.X, t0, t1-t0)
		}
		expr = fmt.Sprintf("if(lt(t\\,%.3f)\\,%s\\,%s)", t1, segment, expr)
	}
	return fmt.Sprintf("crop=%d:%d:%s:%d", w, h, expr, y)
}

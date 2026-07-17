package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"testing"
)

func TestSmartCropV2DenseStoryboardGate(t *testing.T) {
	target := smartCropTarget{StartMs: 10_000, EndMs: 30_000}
	dense := []DerivationRow{
		{PositionMs: 6_000}, {PositionMs: 11_000}, {PositionMs: 16_000},
		{PositionMs: 21_000}, {PositionMs: 26_000}, {PositionMs: 31_000},
	}
	if !smartCropSamplesAreDense(dense, target) {
		t.Fatal("five-second storyboard should enable v2")
	}
	sparse := []DerivationRow{{PositionMs: 1_000}, {PositionMs: 31_000}}
	if smartCropSamplesAreDense(sparse, target) {
		t.Fatal("legacy 30-second storyboard must fall back to v1")
	}
	capped := []DerivationRow{
		{PositionMs: 758_068}, {PositionMs: 769_040},
		{PositionMs: 780_012}, {PositionMs: 790_984},
	}
	if !smartCropSamplesAreDense(capped, smartCropTarget{StartMs: 760_250, EndMs: 788_260}) {
		t.Fatal("120-frame-capped eleven-second storyboard should enable v2")
	}
}

func TestSmartCropStoryboardDensityCheckedBeforeAnalysisCap(t *testing.T) {
	target := smartCropTarget{StartMs: 100_000, EndMs: 700_000}
	derivs := make([]DerivationRow, 0, 123)
	for pos := int64(95_000); pos <= 705_000; pos += 5_000 {
		derivs = append(derivs, DerivationRow{
			Kind: "keyframe", Status: "ok", StorageFileID: fmt.Sprintf("%d", pos), PositionMs: pos,
		})
	}
	uncapped := selectSmartCropReelDerivationsUncapped(derivs, target)
	if !smartCropSamplesAreDense(uncapped, target) {
		t.Fatal("dense source storyboard was rejected before analysis capping")
	}
	capped := capSmartCropReelDerivations(uncapped)
	if len(capped) != smartCropV2MaxSamples {
		t.Fatalf("analysis samples=%d want=%d", len(capped), smartCropV2MaxSamples)
	}
	if smartCropSamplesAreDense(capped, target) {
		t.Fatal("test requires capping to create gaps above the source-density limit")
	}
}

func TestSmartCropStoryboardDenseAtFocus(t *testing.T) {
	makeDerivs := func(gap int64) []DerivationRow {
		out := make([]DerivationRow, 0, 5)
		for i := int64(0); i < 5; i++ {
			out = append(out, DerivationRow{Kind: "keyframe", Status: "ok", StorageFileID: fmt.Sprint(i + 1), PositionMs: 1_000 + i*gap})
		}
		return out
	}
	if !smartCropStoryboardDenseAtFocus(makeDerivs(11_000), 23_000) {
		t.Fatal("capped eleven-second storyboard should use cached frames")
	}
	if smartCropStoryboardDenseAtFocus(makeDerivs(30_000), 61_000) {
		t.Fatal("thirty-second storyboard should request local supplemental frames")
	}
}

func TestSelectSmartCropReelDerivationsIncludesBoundariesAndCaps(t *testing.T) {
	derivs := make([]DerivationRow, 0, 80)
	for i := 0; i < 80; i++ {
		derivs = append(derivs, DerivationRow{
			Kind: "keyframe", Status: "ok", StorageFileID: "x",
			PositionMs: int64(i) * 1000,
		})
	}
	got := selectSmartCropReelDerivations(derivs, smartCropTarget{StartMs: 10_500, EndMs: 65_500})
	if len(got) > smartCropV2MaxSamples {
		t.Fatalf("sample cap exceeded: %d", len(got))
	}
	if got[0].PositionMs > 10_500 || got[len(got)-1].PositionMs < 65_500 {
		t.Fatalf("boundary coverage lost: first=%d last=%d", got[0].PositionMs, got[len(got)-1].PositionMs)
	}
}

func TestSelectSmartCropStillDerivationsBracketsFocus(t *testing.T) {
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "later", PositionMs: 206_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "before", PositionMs: 201_000},
		{Kind: "thumbnail", Status: "ok", StorageFileID: "thumb"},
	}
	got := selectSmartCropStillDerivations(derivs, 202_129)
	if len(got) != 2 || got[0].StorageFileID != "before" || got[1].StorageFileID != "later" {
		t.Fatalf("expected ordered bracketing frames, got %+v", got)
	}
}

func TestSelectSmartCropStillDerivationsRejectsSparseFrames(t *testing.T) {
	derivs := []DerivationRow{
		{Kind: "keyframe", Status: "ok", StorageFileID: "old", PositionMs: 10_000},
		{Kind: "keyframe", Status: "ok", StorageFileID: "future", PositionMs: 40_000},
	}
	if got := selectSmartCropStillDerivations(derivs, 25_000); len(got) != 0 {
		t.Fatalf("sparse storyboard must use the safe v1 fallback, got %+v", got)
	}
}

func TestSelectSmartCropStillDerivationsAddsBoundedTemporalContext(t *testing.T) {
	derivs := make([]DerivationRow, 0, 20)
	for i := 0; i < 20; i++ {
		derivs = append(derivs, DerivationRow{
			Kind: "keyframe", Status: "ok", StorageFileID: string(rune('a' + i)),
			PositionMs: int64(i) * 5_000,
		})
	}
	got := selectSmartCropStillDerivations(derivs, 51_000)
	if len(got) != smartCropTemporalMaxSamples {
		t.Fatalf("got %d context frames, want %d", len(got), smartCropTemporalMaxSamples)
	}
	for i := 1; i < len(got); i++ {
		if got[i-1].PositionMs >= got[i].PositionMs {
			t.Fatalf("context must remain chronological: %+v", got)
		}
	}
	var hasBefore, hasAfter bool
	for _, d := range got {
		hasBefore = hasBefore || d.PositionMs == 50_000
		hasAfter = hasAfter || d.PositionMs == 55_000
	}
	if !hasBefore || !hasAfter {
		t.Fatalf("focus bracket missing from temporal context: %+v", got)
	}
}

func TestInterpolateSmartCropStillXTracksBetweenFrames(t *testing.T) {
	before := cropPathPoint{AtMs: 201_000, X: 232}
	after := cropPathPoint{AtMs: 206_000, X: 264}
	if got := interpolateSmartCropStillX(before, after, 202_129); got != 238 {
		t.Fatalf("interpolated Tracy crop x=%d, want 238", got)
	}
	if got := interpolateSmartCropStillX(before, after, 199_000); got != 232 {
		t.Fatalf("focus before range should clamp to before x, got %d", got)
	}
}

func TestResolveSmartCropStillBaseUsesFocusBracketInsideContext(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 80, G: 90, B: 100, A: 255})
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 30_000, X: 20}, img: img},
		{point: cropPathPoint{AtMs: 50_000, X: 200}, img: img},
		{point: cropPathPoint{AtMs: 55_000, X: 300}, img: img},
		{point: cropPathPoint{AtMs: 75_000, X: 760}, img: img},
	}
	x, method, err := resolveSmartCropStillBase(samples, 52_000)
	if err != nil {
		t.Fatal(err)
	}
	if x != 240 || method != "interpolated" {
		t.Fatalf("resolved x=%d method=%s, want interpolated x=240", x, method)
	}
}

func TestSmartCropStillTrackingActivatesOnlyForTraversal(t *testing.T) {
	stable := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 156_000, X: 700}},
		{point: cropPathPoint{AtMs: 161_000, X: 760}},
	}
	if smartCropStillNeedsTracking(stable, 160_000, 606) {
		t.Fatal("stable bracket should remain on cached storyboard")
	}
	moving := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 156_000, X: 720}},
		{point: cropPathPoint{AtMs: 161_000, X: 458}},
	}
	if !smartCropStillNeedsTracking(moving, 160_000, 606) {
		t.Fatal("Monika traversal should request the exact source frame")
	}
	got := smartCropStillTrackingPositions(160_000, 298_200)
	want := []int64{159_500, 160_000, 160_500}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("tracking positions=%v want=%v", got, want)
	}
}

func TestHeadAwareNarrowCropProtectsRecliningFace(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// Static poster-like warm detail in the top band must be ignored.
	fillSmartCropTestRect(img, image.Rect(147, 8, 188, 48), color.RGBA{R: 205, G: 145, B: 120, A: 255})
	// A reclining face is low in the frame and partly outside the current
	// portrait crop's left safety margin.
	fillSmartCropTestEllipse(img, 138, 104, 24, 14, color.RGBA{R: 205, G: 160, B: 140, A: 255})

	x, changed := headAwareNarrowSmartCropX(img, 816, 1920, 606)
	if !changed {
		t.Fatal("reclining face at the crop edge was not protected")
	}
	if x < 450 || x > 650 {
		t.Fatalf("head-safe crop x=%d, want [450,650]", x)
	}
}

func TestHeadAwareNarrowCropRecoversRecliningFaceJustOutsideCrop(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// With srcX=906, the thumbnail crop begins at x=151. Model the Monika
	// 490.745s frame: a horizontal face centred just outside that boundary
	// and low enough that the older 80% vertical cutoff rejected it.
	fillSmartCropTestEllipse(img, 130, 132, 22, 15, color.RGBA{R: 205, G: 160, B: 140, A: 255})

	x, changed := headAwareNarrowSmartCropX(img, 906, 1920, 606)
	if !changed {
		t.Fatal("near-edge reclining face outside crop was not recovered")
	}
	if x < 450 || x > 650 {
		t.Fatalf("outside-face recovery x=%d, want [450,650]", x)
	}
}

func TestHeadAwareNarrowCropDoesNotChaseDistantWarmRegion(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// This has face-like size, colour, and height but is well beyond the
	// bounded edge halo. It may be another person or room decoration.
	fillSmartCropTestEllipse(img, 88, 132, 22, 15, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(img, 906, 1920, 606); changed || x != 906 {
		t.Fatalf("distant warm region moved crop: x=%d changed=%v", x, changed)
	}
}

func TestHeadAwareNarrowCropKeepsSafeOrNonFaceContentStable(t *testing.T) {
	centered := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(centered, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	fillSmartCropTestEllipse(centered, 160, 108, 18, 18, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(centered, 720, 1920, 606); changed || x != 720 {
		t.Fatalf("already-safe face moved crop: x=%d changed=%v", x, changed)
	}

	lowerBody := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(lowerBody, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	fillSmartCropTestEllipse(lowerBody, 142, 158, 22, 20, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(lowerBody, 816, 1920, 606); changed || x != 816 {
		t.Fatalf("lower-body warm region moved crop: x=%d changed=%v", x, changed)
	}
}

func TestStaticSmartCropPathStaysFixed(t *testing.T) {
	path := []cropPathPoint{
		{AtMs: 0, X: 300}, {AtMs: 5_000, X: 316}, {AtMs: 10_000, X: 326},
	}
	x, ok := staticSmartCropPathX(path, 404)
	if !ok {
		t.Fatal("small saliency wobble should remain a static crop")
	}
	if x < 300 || x > 326 || x%2 != 0 {
		t.Fatalf("unexpected static x=%d", x)
	}
}

func TestMovingSmartCropPathRemainsTrackedAndSmooth(t *testing.T) {
	path := []cropPathPoint{
		{AtMs: 0, X: 100}, {AtMs: 5_000, X: 250},
		{AtMs: 10_000, X: 500}, {AtMs: 15_000, X: 700},
	}
	got := stabilizeSmartCropPath(path, 404, 1280)
	if _, ok := staticSmartCropPathX(got, 404); ok {
		t.Fatal("large subject movement must not collapse to one crop")
	}
	for i, p := range got {
		if p.X < 0 || p.X > 876 || p.X%2 != 0 {
			t.Fatalf("point %d outside even source bounds: %+v", i, p)
		}
		if i > 0 && p.X < got[i-1].X {
			t.Fatalf("monotonic movement reversed at %d: %+v", i, got)
		}
	}
}

func TestSmartCropSmoothingCannotDragTraversalAnchorAway(t *testing.T) {
	path := []cropPathPoint{
		{AtMs: 191_000, X: 720},
		{AtMs: 196_000, X: 1314},
		{AtMs: 201_000, X: 732},
	}
	got := stabilizeSmartCropPath(path, 606, 1920)
	if len(got) != 3 {
		t.Fatalf("path=%v", got)
	}
	if got[1].X < 1254 {
		t.Fatalf("subject anchor was pulled too far toward neighbours: %v", got)
	}
}

func TestAdaptiveTrackingPositionsFocusOnMovingIntervals(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 151_000, X: 720}, img: img},
		{point: cropPathPoint{AtMs: 156_000, X: 720}, img: img},
		{point: cropPathPoint{AtMs: 161_000, X: 458}, img: img},
		{point: cropPathPoint{AtMs: 166_000, X: 802}, img: img},
		{point: cropPathPoint{AtMs: 171_000, X: 768}, img: img},
		{point: cropPathPoint{AtMs: 176_000, X: 720}, img: img},
	}
	target := smartCropTarget{StartMs: 151_145, EndMs: 175_599}
	got := smartCropAdaptiveTrackingPositions(samples, target, 298_200, 1920, 606)
	if len(got) < 8 || len(got) > smartCropTrackingMaxExtraFrames {
		t.Fatalf("unexpected adaptive sample count=%d positions=%v", len(got), got)
	}
	for _, want := range []int64{157_000, 158_000, 159_000, 160_000, 162_000, 163_000, 164_000, 165_000} {
		found := false
		for _, position := range got {
			found = found || position == want
		}
		if !found {
			t.Fatalf("moving interval position %d missing from %v", want, got)
		}
	}
}

func TestAdaptiveTrackingStaysOffForNearlyStableSubject(t *testing.T) {
	samples := makeTemporalTestSamples([]int{108, 109, 108, 110, 109, 108, 109, 110, 109}, 700)
	for i := range samples {
		samples[i].point.X = 700 + (i%3-1)*12
	}
	markSmartCropSceneCuts(samples)
	if smartCropReelNeedsTracking(samples, 1280, 404) {
		t.Fatal("nearly stable subject should retain the cached fixed-crop fast path")
	}
}

func TestCropFilterForPathInterpolatesAndPreservesCuts(t *testing.T) {
	path := []cropPathPoint{
		{AtMs: 10_000, X: 100},
		{AtMs: 15_000, X: 300},
		{AtMs: 20_000, X: 700, Cut: true},
	}
	got := cropFilterForPath(404, 720, 0, 10_000, path)
	if !strings.Contains(got, "100+(200)*(t-0.000)/5.000") {
		t.Fatalf("missing linear first segment: %s", got)
	}
	if strings.Contains(got, "300+(400)") {
		t.Fatalf("scene cut must not be interpolated: %s", got)
	}
	if !strings.Contains(got, `lt(t\,5.000)`) || !strings.Contains(got, `lt(t\,10.000)`) {
		t.Fatalf("ffmpeg commas/times not escaped correctly: %s", got)
	}
}

func TestSceneCutScoreSeparatesEditFromLocalMotion(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(base, color.RGBA{R: 80, G: 90, B: 100, A: 255})
	local := cloneRGBA(base)
	for y := 50; y < 130; y++ {
		for x := 120; x < 180; x++ {
			local.SetRGBA(x, y, color.RGBA{R: 220, G: 80, B: 50, A: 255})
		}
	}
	cut := image.NewRGBA(base.Bounds())
	fillImage(cut, color.RGBA{R: 235, G: 225, B: 40, A: 255})
	if got := sceneCutScore(base, local); got >= 0.28 {
		t.Fatalf("local subject motion misclassified as a cut: %.3f", got)
	}
	if got := sceneCutScore(base, cut); got < 0.28 {
		t.Fatalf("full-frame edit not detected: %.3f", got)
	}
}

func TestSmartCropSceneSamplesDoNotCrossEdit(t *testing.T) {
	leftScene := temporalTestFrame(320, 180, color.RGBA{R: 35, G: 40, B: 45, A: 255}, 65)
	rightScene := temporalTestFrame(320, 180, color.RGBA{R: 225, G: 215, B: 55, A: 255}, 220)
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 0}, img: leftScene},
		{point: cropPathPoint{AtMs: 5_000}, img: temporalTestFrame(320, 180, color.RGBA{R: 35, G: 40, B: 45, A: 255}, 70)},
		{point: cropPathPoint{AtMs: 10_000}, img: temporalTestFrame(320, 180, color.RGBA{R: 35, G: 40, B: 45, A: 255}, 75)},
		{point: cropPathPoint{AtMs: 15_000}, img: rightScene},
		{point: cropPathPoint{AtMs: 20_000}, img: temporalTestFrame(320, 180, color.RGBA{R: 225, G: 215, B: 55, A: 255}, 225)},
	}
	got := smartCropSceneSamples(samples, 8_000)
	if len(got) != 3 || got[0].point.AtMs != 0 || got[2].point.AtMs != 10_000 {
		t.Fatalf("scene context crossed edit: %+v", got)
	}
}

func TestTemporalSubjectConsensusFindsMovingSubjectOverStaticTexture(t *testing.T) {
	xs := []int{92, 98, 104, 110, 116, 122, 128, 134, 140}
	samples := makeTemporalTestSamples(xs, 740)
	result, ok := temporalSubjectConsensus(samples, 1280, 404)
	if !ok {
		t.Fatal("expected temporal consensus")
	}
	if result.X < 300 || result.X > 360 {
		t.Fatalf("subject crop x=%d does not center the moving person: %+v", result.X, result)
	}
	if result.Concentration < smartCropTemporalMinConcentration ||
		result.MeanActivity < smartCropTemporalMinMeanActivity ||
		result.ActiveFraction < smartCropTemporalMinActiveFraction {
		t.Fatalf("concentrated subject did not pass confidence gates: %+v", result)
	}
	if corrected, changed := applySmartCropTemporalOverride(740, result, 404, 1280); !changed || corrected != result.X {
		t.Fatalf("large background crop was not corrected: x=%d changed=%v result=%+v", corrected, changed, result)
	}
}

func TestTemporalSubjectConsensusRejectsGlobalExposureChange(t *testing.T) {
	samples := make([]smartCropV2Sample, 0, 9)
	for i := 0; i < 9; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		level := uint8(45 + i*18)
		fillImage(img, color.RGBA{R: level, G: level, B: level, A: 255})
		samples = append(samples, smartCropV2Sample{point: cropPathPoint{AtMs: int64(i) * 5_000}, img: img})
	}
	result, ok := temporalSubjectConsensus(samples, 1280, 404)
	if !ok {
		t.Fatal("expected measurable temporal result")
	}
	if result.Concentration >= smartCropTemporalMinConcentration {
		t.Fatalf("full-frame exposure change looks falsely concentrated: %+v", result)
	}
	if x, changed := applySmartCropTemporalOverride(440, result, 404, 1280); changed || x != 440 {
		t.Fatalf("low-confidence global change altered crop: x=%d changed=%v", x, changed)
	}
}

func TestTemporalSubjectConsensusRejectsStaticOrInsufficientFrames(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 90, G: 100, B: 110, A: 255})
	static := []smartCropV2Sample{{img: img}, {img: cloneRGBA(img)}, {img: cloneRGBA(img)}}
	if result, ok := temporalSubjectConsensus(static, 1280, 404); ok {
		t.Fatalf("static frames produced false foreground: %+v", result)
	}
	if result, ok := temporalSubjectConsensus(static[:2], 1280, 404); ok {
		t.Fatalf("two frames must not establish consensus: %+v", result)
	}
	withNil := []smartCropV2Sample{{img: nil}, {img: img}, {img: img}}
	if result, ok := temporalSubjectConsensus(withNil, 1280, 404); ok {
		t.Fatalf("nil image must fail safely: %+v", result)
	}
	if result, ok := temporalSubjectConsensus(makeTemporalTestSamples([]int{92, 98, 104}, 300), 1280, 960); ok {
		t.Fatalf("wide landscape/square crop must stay on normal saliency path: %+v", result)
	}
}

func TestTemporalOverrideKeepsSmallDisagreementsStable(t *testing.T) {
	result := smartCropTemporalResult{
		X: 460, Samples: 9, Concentration: 0.95, MeanActivity: 2.0, ActiveFraction: 0.08,
	}
	if x, changed := applySmartCropTemporalOverride(420, result, 404, 1280); changed || x != 420 {
		t.Fatalf("small disagreement should preserve saliency crop: x=%d changed=%v", x, changed)
	}
}

func TestTemporalWarmAnchorCannotBypassConcentrationGate(t *testing.T) {
	result := smartCropTemporalResult{
		X: 40, Samples: 9, Concentration: 0.39, MeanActivity: 2.0, ActiveFraction: 0.08,
		SubjectAnchored: true, AnchorCoverage: 9, AnchorScore: 1_200,
	}
	if smartCropTemporalResultConfident(result) {
		t.Fatalf("persistent room feature bypassed temporal concentration: %+v", result)
	}
	if x, changed := applySmartCropTemporalOverride(412, result, 404, 1280); changed || x != 412 {
		t.Fatalf("unfocused anchor changed crop: x=%d changed=%v", x, changed)
	}
}

func TestTemporalDenseActivityAcceptsPatriciaRaisedArmProfile(t *testing.T) {
	// Captures the confidence profile from Patricia reel 5223: the person and
	// raised arm span more than one portrait window, but activity is strong and
	// every sampled path point is displaced in the same direction.
	result := smartCropTemporalResult{
		X: 196, Samples: 6, Concentration: 0.534, MeanActivity: 5.116, ActiveFraction: 0.143,
	}
	if !smartCropTemporalResultConfident(result) {
		t.Fatalf("strong dense subject motion was rejected: %+v", result)
	}
	if x, changed := applySmartCropTemporalOverride(0, result, 404, 1280); !changed || x != 196 {
		t.Fatalf("edge-biased crop was not corrected: x=%d changed=%v", x, changed)
	}
}

func TestTemporalDenseActivityFallbackRemainsConservative(t *testing.T) {
	tests := []smartCropTemporalResult{
		{X: 196, Samples: 6, Concentration: 0.499, MeanActivity: 5.2, ActiveFraction: 0.15},
		{X: 196, Samples: 6, Concentration: 0.534, MeanActivity: 1.99, ActiveFraction: 0.15},
		{X: 196, Samples: 6, Concentration: 0.534, MeanActivity: 5.2, ActiveFraction: 0.039},
	}
	for _, result := range tests {
		if smartCropTemporalResultConfident(result) {
			t.Fatalf("weak or diffuse activity passed dense fallback: %+v", result)
		}
	}
}

func TestTemporalStaticPersonAcceptsPatriciaProfile(t *testing.T) {
	result := smartCropTemporalResult{
		X: 284, Samples: 9, Concentration: 0.9897,
		MeanActivity: 0.302, ActiveFraction: 0.0098,
		AnchorCoverage: 9, AnchorScore: 364.6,
	}
	if !smartCropTemporalResultConfident(result) {
		t.Fatalf("static person profile was rejected: %+v", result)
	}
	if x, changed := applySmartCropTemporalOverride(128, result, 404, 1280); !changed || x != 284 {
		t.Fatalf("static edge crop was not corrected: x=%d changed=%v", x, changed)
	}
}

func TestTemporalStaticPersonFallbackRequiresHumanEvidence(t *testing.T) {
	tests := []smartCropTemporalResult{
		{X: 284, Samples: 9, Concentration: 0.979, MeanActivity: 0.31, ActiveFraction: 0.01, AnchorCoverage: 9, AnchorScore: 365},
		{X: 284, Samples: 9, Concentration: 0.99, MeanActivity: 0.24, ActiveFraction: 0.01, AnchorCoverage: 9, AnchorScore: 365},
		{X: 284, Samples: 9, Concentration: 0.99, MeanActivity: 0.31, ActiveFraction: 0.007, AnchorCoverage: 9, AnchorScore: 365},
		{X: 284, Samples: 9, Concentration: 0.99, MeanActivity: 0.31, ActiveFraction: 0.01, AnchorCoverage: 9, AnchorScore: 299},
	}
	for _, result := range tests {
		if smartCropTemporalResultConfident(result) {
			t.Fatalf("static fallback accepted insufficient evidence: %+v", result)
		}
	}
}

func TestTemporalStableMinorityCorrects5912Profile(t *testing.T) {
	result := smartCropTemporalResult{
		X: 368, Samples: 7, Concentration: 0.9495,
		AnchorCoverage: 7, AnchorScore: 512.2,
	}
	if got := smartCropTemporalCorrectionDirection(result, 2, 0, 5, 7); got != -1 {
		t.Fatalf("5912 correction direction=%d want=-1", got)
	}
}

func TestTemporalStableMinorityRejectsMovementAndWeakEvidence(t *testing.T) {
	base := smartCropTemporalResult{
		X: 368, Samples: 7, Concentration: 0.9495,
		AnchorCoverage: 7, AnchorScore: 512.2,
	}
	tests := []struct {
		result               smartCropTemporalResult
		left, right, aligned int
	}{
		{base, 2, 1, 4},
		{func() smartCropTemporalResult { r := base; r.Concentration = 0.89; return r }(), 2, 0, 5},
		{func() smartCropTemporalResult { r := base; r.AnchorCoverage = 4; return r }(), 2, 0, 5},
		{func() smartCropTemporalResult { r := base; r.AnchorScore = 900; return r }(), 2, 0, 5},
	}
	for _, tc := range tests {
		if got := smartCropTemporalCorrectionDirection(tc.result, tc.left, tc.right, tc.aligned, 7); got != 0 {
			t.Fatalf("unstable minority direction=%d result=%+v counts=%d/%d/%d", got, tc.result, tc.left, tc.right, tc.aligned)
		}
	}
}

func TestStaticWarmSubjectConsensusCentersMotionlessPerson(t *testing.T) {
	samples := make([]smartCropV2Sample, 9)
	for i := range samples {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		fillSmartCropTestRect(img, image.Rect(0, 0, 320, 180), color.RGBA{R: 90, G: 95, B: 100, A: 255})
		fillSmartCropTestRect(img, image.Rect(125, 50, 135, 80), color.RGBA{R: 205, G: 160, B: 140, A: 255})
		samples[i] = smartCropV2Sample{point: cropPathPoint{AtMs: int64(i) * 5_000, X: 128}, img: img}
	}
	if _, ok := temporalSubjectConsensus(samples, 1280, 404); ok {
		t.Fatal("identical frames must not manufacture temporal activity")
	}
	result, ok := staticWarmSubjectConsensus(samples, 1280, 404)
	if !ok || !result.StaticAnchored {
		t.Fatalf("motionless person was not anchored: ok=%v result=%+v", ok, result)
	}
	if x, changed := applySmartCropTemporalOverride(128, result, 404, 1280); !changed || x < 280 || x > 350 {
		t.Fatalf("motionless person was not centered: x=%d changed=%v result=%+v", x, changed, result)
	}
}

func TestStaticWarmSubjectConsensusRejectsSmallFurnitureHighlight(t *testing.T) {
	samples := make([]smartCropV2Sample, 9)
	for i := range samples {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		fillSmartCropTestRect(img, image.Rect(0, 0, 320, 180), color.RGBA{R: 90, G: 95, B: 100, A: 255})
		fillSmartCropTestRect(img, image.Rect(245, 130, 255, 140), color.RGBA{R: 205, G: 160, B: 140, A: 255})
		samples[i] = smartCropV2Sample{img: img}
	}
	if result, ok := staticWarmSubjectConsensus(samples, 1280, 404); ok {
		t.Fatalf("small static highlight was accepted as a person: %+v", result)
	}
}

func TestStaticWarmSubjectConsensusRejectsBroadWarmSurface(t *testing.T) {
	samples := make([]smartCropV2Sample, 9)
	for i := range samples {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		fillSmartCropTestRect(img, image.Rect(0, 0, 320, 180), color.RGBA{R: 90, G: 95, B: 100, A: 255})
		fillSmartCropTestRect(img, image.Rect(105, 35, 180, 155), color.RGBA{R: 205, G: 160, B: 140, A: 255})
		samples[i] = smartCropV2Sample{img: img}
	}
	if result, ok := staticWarmSubjectConsensus(samples, 1280, 404); ok {
		t.Fatalf("broad static warm surface was accepted as a person: %+v", result)
	}
}

func fillSmartCropTestRect(img *image.RGBA, rect image.Rectangle, c color.RGBA) {
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		for x := rect.Min.X; x < rect.Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func fillSmartCropTestEllipse(img *image.RGBA, cx, cy, rx, ry int, c color.RGBA) {
	for y := maxInt(img.Bounds().Min.Y, cy-ry); y < minInt(img.Bounds().Max.Y, cy+ry+1); y++ {
		for x := maxInt(img.Bounds().Min.X, cx-rx); x < minInt(img.Bounds().Max.X, cx+rx+1); x++ {
			dx := float64(x-cx) / float64(rx)
			dy := float64(y-cy) / float64(ry)
			if dx*dx+dy*dy <= 1 {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func TestTemporalWarmAnchorCertifiesStaticPersonWithoutCenteringOneLimb(t *testing.T) {
	samples := make([]smartCropV2Sample, 0, 7)
	for i := 0; i < 7; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		// Vertical blinds reproduce the static background that dominated 2655.
		for x := 0; x < 320; x++ {
			shade := uint8(215)
			if (x/10)%2 == 0 {
				shade = 245
			}
			for y := 0; y < 180; y++ {
				img.SetRGBA(x, y, color.RGBA{R: shade, G: shade - 7, B: shade - 14, A: 255})
			}
		}
		// A mostly static dark torso has two separated warm arms. The left arm
		// is larger, so a color-component locator alone would center the limb.
		shift := i % 3
		for y := 45; y < 170; y++ {
			for x := 150 + shift; x < 210+shift; x++ {
				img.SetRGBA(x, y, color.RGBA{R: 35, G: 45, B: 85, A: 255})
			}
		}
		for y := 55; y < 160; y++ {
			for x := 130; x < 148; x++ {
				img.SetRGBA(x, y, color.RGBA{R: 200, G: 130, B: 110, A: 255})
			}
		}
		for y := 65; y < 150; y++ {
			for x := 212; x < 224; x++ {
				img.SetRGBA(x, y, color.RGBA{R: 200, G: 130, B: 110, A: 255})
			}
		}
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: 740}, img: img,
		})
	}
	result, ok := temporalSubjectConsensus(samples, 1280, 404)
	if !ok || !result.SubjectAnchored {
		t.Fatalf("static person was not certified: ok=%v result=%+v", ok, result)
	}
	if result.X < 450 || result.X > 580 {
		t.Fatalf("crop centered a limb instead of the torso: %+v", result)
	}
	if !smartCropTemporalResultConfident(result) {
		t.Fatalf("concentrated static person did not pass anchor confidence: %+v", result)
	}
}

func TestReelTemporalConsensusRejectsLowConfidenceScene(t *testing.T) {
	samples := make([]smartCropV2Sample, 0, 9)
	for i := 0; i < 9; i++ {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		fillImage(img, color.RGBA{R: uint8(70 + i), G: uint8(80 + i), B: uint8(90 + i), A: 255})
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: 740}, img: img,
		})
	}
	if corrected := correctSmartCropReelTemporalOutliers(samples, 1280, 404); corrected != 0 {
		t.Fatalf("low-confidence reel scene changed %d path points", corrected)
	}
}

func TestReelTemporalConsensusCorrectsOnlySceneOutliers(t *testing.T) {
	samples := makeTemporalTestSamples([]int{92, 98, 104, 110, 116, 122, 128, 134, 140}, 740)
	result, ok := temporalSubjectConsensus(samples, 1280, 404)
	if !ok {
		t.Fatal("expected temporal consensus")
	}
	// Preserve one already-good path point to prove this is an outlier guard,
	// not a replacement for the reel tracker.
	samples[4].point.X = result.X + 40
	corrected := correctSmartCropReelTemporalOutliers(samples, 1280, 404)
	if corrected != len(samples)-1 {
		t.Fatalf("corrected %d points, want %d", corrected, len(samples)-1)
	}
	if samples[4].point.X != result.X+40 {
		t.Fatalf("valid tracked point was flattened: got %d want %d", samples[4].point.X, result.X+40)
	}
	for i, sample := range samples {
		if i != 4 && sample.point.X != result.X {
			t.Fatalf("outlier %d not corrected: %+v", i, sample.point)
		}
	}
}

func TestReelTemporalConsensusPreservesOpposingSubjectMovement(t *testing.T) {
	samples := makeTemporalTestSamples([]int{92, 98, 104, 110, 116, 122, 128, 134, 140}, 300)
	result, ok := temporalSubjectConsensus(samples, 1280, 404)
	if !ok {
		t.Fatal("expected temporal consensus")
	}
	original := []int{
		result.X - 180, result.X - 150, result.X - 120,
		result.X, result.X, result.X,
		result.X + 120, result.X + 150, result.X + 180,
	}
	for i := range samples {
		samples[i].point.X = original[i]
	}
	if corrected := correctSmartCropReelTemporalOutliers(samples, 1280, 404); corrected != 0 {
		t.Fatalf("opposing motion was flattened; corrected=%d", corrected)
	}
	for i := range samples {
		if samples[i].point.X != original[i] {
			t.Fatalf("tracked point %d changed from %d to %d", i, original[i], samples[i].point.X)
		}
	}
}

func TestReelTemporalConsensusPreservesSustainedOneSidedTraversal(t *testing.T) {
	samples := makeTemporalTestSamples([]int{92, 98, 104, 110, 116, 122, 128, 134, 140}, 300)
	tracked := []int{420, 450, 500, 560, 630, 700, 760, 790, 810}
	for i := range samples {
		samples[i].point.X = tracked[i]
	}
	if !smartCropSceneHasSustainedTraversal(samples, 404) {
		t.Fatal("dense one-direction movement was not recognized as traversal")
	}
	if corrected := correctSmartCropReelTemporalOutliers(samples, 1280, 404); corrected != 0 {
		t.Fatalf("one-direction traversal was flattened; corrected=%d", corrected)
	}
	for i := range samples {
		if samples[i].point.X != tracked[i] {
			t.Fatalf("tracked point %d changed from %d to %d", i, tracked[i], samples[i].point.X)
		}
	}
}

func TestSustainedTraversalRejectsSingleDriftPoint(t *testing.T) {
	samples := make([]smartCropV2Sample, 9)
	for i := range samples {
		samples[i].point.X = 700
	}
	samples[4].point.X = 1200
	if smartCropSceneHasSustainedTraversal(samples, 404) {
		t.Fatal("one isolated drift point must not disable temporal correction")
	}
}

func TestReelTemporalConsensusPreservesShortScene(t *testing.T) {
	samples := makeTemporalTestSamples([]int{92, 116, 140}, 740)
	original := []int{740, 740, 740}
	if corrected := correctSmartCropReelTemporalOutliers(samples, 1280, 404); corrected != 0 {
		t.Fatalf("short scene was corrected from too little evidence: %d", corrected)
	}
	for i := range samples {
		if samples[i].point.X != original[i] {
			t.Fatalf("short-scene point %d changed to %d", i, samples[i].point.X)
		}
	}
}

func BenchmarkAnalyzeSmartCropV2Frame320(b *testing.B) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	for y := 30; y < 170; y++ {
		for x := 210; x < 285; x++ {
			img.SetRGBA(x, y, color.RGBA{R: 225, G: 92, B: 54, A: 255})
		}
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := analyzeSmartCropV2Frame(1280, 720, 9, 16, img); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkTemporalSubjectConsensusNineFrames(b *testing.B) {
	samples := makeTemporalTestSamples([]int{92, 98, 104, 110, 116, 122, 128, 134, 140}, 740)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := temporalSubjectConsensus(samples, 1280, 404); !ok {
			b.Fatal("expected consensus")
		}
	}
}

func makeTemporalTestSamples(subjectXs []int, saliencyX int) []smartCropV2Sample {
	samples := make([]smartCropV2Sample, 0, len(subjectXs))
	for i, x := range subjectXs {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		// High-contrast static stripes reproduce the class of background that
		// fooled per-frame saliency in the reported Gaby/Christine renders.
		for px := 0; px < 320; px++ {
			shade := uint8(205)
			if (px/8)%2 == 0 {
				shade = 245
			}
			for py := 0; py < 180; py++ {
				img.SetRGBA(px, py, color.RGBA{R: shade, G: shade - 8, B: shade - 15, A: 255})
			}
		}
		for py := 35; py < 170; py++ {
			for px := x; px < x+42; px++ {
				img.SetRGBA(px, py, color.RGBA{R: 55, G: 75, B: 110, A: 255})
			}
		}
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: saliencyX}, img: img,
		})
	}
	return samples
}

func temporalTestFrame(w, h int, background color.RGBA, subjectX int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fillImage(img, background)
	for y := h / 4; y < h*3/4; y++ {
		for x := maxInt(0, subjectX-15); x < minInt(w, subjectX+15); x++ {
			img.SetRGBA(x, y, color.RGBA{R: 180, G: 60, B: 90, A: 255})
		}
	}
	return img
}

func fillImage(img *image.RGBA, c color.RGBA) {
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func cloneRGBA(src *image.RGBA) *image.RGBA {
	dst := image.NewRGBA(src.Bounds())
	copy(dst.Pix, src.Pix)
	return dst
}

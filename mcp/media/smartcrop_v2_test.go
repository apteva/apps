package main

import (
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
	if result.X < 240 || result.X > 400 {
		t.Fatalf("subject crop x=%d is not in the left subject region: %+v", result.X, result)
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
		X: 500, Samples: 9, Concentration: 0.95, MeanActivity: 2.0, ActiveFraction: 0.08,
	}
	if x, changed := applySmartCropTemporalOverride(420, result, 404, 1280); changed || x != 420 {
		t.Fatalf("small disagreement should preserve saliency crop: x=%d changed=%v", x, changed)
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

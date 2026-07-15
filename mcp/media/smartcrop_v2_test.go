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

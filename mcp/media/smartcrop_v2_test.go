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

func TestSmartCropWeakFaceCandidateCannotOverrideEstablishedSubject(t *testing.T) {
	const cropX, cropW = 932, 606
	testCases := []struct {
		name             string
		face             smartCropFace
		weakPixelSupport bool
		want             bool
	}{
		{
			name: "threshold-level-distant-room-pattern",
			face: smartCropFace{CenterX: 1728, MinX: 1651, MaxX: 1805, Scale: 155, Quality: 5.2},
			want: false,
		},
		{
			name:             "pixel-supported-distant-reclining-profile",
			face:             smartCropFace{CenterX: 1728, MinX: 1651, MaxX: 1805, Scale: 155, Quality: 8},
			weakPixelSupport: true,
			want:             true,
		},
		{
			name: "threshold-level-contained-profile",
			face: smartCropFace{CenterX: 1480, MinX: 1410, MaxX: 1550, Scale: 140, Quality: 6},
			want: true,
		},
		{
			name: "medium-overlapping-edge-face",
			face: smartCropFace{CenterX: 1580, MinX: 1500, MaxX: 1660, Scale: 160, Quality: 14},
			want: true,
		},
		{
			name:             "supported-threshold-level-overlapping-profile",
			face:             smartCropFace{CenterX: 1580, MinX: 1500, MaxX: 1660, Scale: 160, Quality: 6},
			weakPixelSupport: true,
			want:             true,
		},
		{
			name: "unsupported-threshold-level-overlapping-pattern",
			face: smartCropFace{CenterX: 1580, MinX: 1500, MaxX: 1660, Scale: 160, Quality: 6},
			want: false,
		},
		{
			name: "strong-detection-can-recover-outside-face",
			face: smartCropFace{CenterX: 1728, MinX: 1651, MaxX: 1805, Scale: 155, Quality: 25},
			want: true,
		},
		{
			name: "huge-threshold-level-room-pattern",
			face: smartCropFace{CenterX: 1300, MinX: 850, MaxX: 1750, Scale: 900, Quality: 5.8},
			want: false,
		},
		{
			name: "hd-reclining-torso-is-not-a-face",
			face: smartCropFace{CenterX: 714, MinX: 510, MaxX: 918, Scale: 414, Quality: 8.7},
			want: false,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := smartCropFaceCandidateSupported(testCase.face, cropX, cropW, testCase.weakPixelSupport); got != testCase.want {
				t.Fatalf("supported=%v want=%v face=%+v", got, testCase.want, testCase.face)
			}
		})
	}
	fourKPattern := smartCropFace{CenterX: 2256, MinX: 1824, MaxX: 2688, Scale: 864, Quality: 7}
	if smartCropFaceCandidateSupported(fourKPattern, 900, 1214, true) {
		t.Fatalf("4K large rotated furniture pattern was accepted: %+v", fourKPattern)
	}
}

func TestSmartCropStillTrackingConsensusKeepsExactRecliningFrames(t *testing.T) {
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 20_500, X: 240}},
		{point: cropPathPoint{AtMs: 21_000, X: 324}},
		{point: cropPathPoint{AtMs: 21_500, X: 240}},
	}
	context := smartCropTemporalResult{
		X: 480, Samples: 9, Concentration: 0.95,
		MeanActivity: 1.0, ActiveFraction: 0.05, StaticAnchored: true,
	}
	x, changed := stabilizeSmartCropStillTrackingX(324, samples, context, 606, 1920)
	if !changed || x != 240 {
		t.Fatalf("stable exact reclining frames lost to broad context: x=%d changed=%v", x, changed)
	}
}

func TestBestColumnWindowCentersNarrowEvidenceOnlyFor4K(t *testing.T) {
	columns := make([]float64, 100)
	for i := 70; i <= 74; i++ {
		columns[i] = 10
	}
	fourKX, _, _, _, ok := bestColumnWindow(columns, 30, 0, 3840)
	if !ok || fourKX < 57 || fourKX > 59 {
		t.Fatalf("4K balanced window x=%d ok=%v want [57,59]", fourKX, ok)
	}
	hdX, _, _, _, ok := bestColumnWindow(columns, 30, 0, 1920)
	if !ok || hdX != 45 {
		t.Fatalf("HD compatibility window x=%d ok=%v want 45", hdX, ok)
	}
}

func TestUnanchoredEdgeFurnitureFallsBackToCenter(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 92, G: 98, B: 104, A: 255})
	// A broad low wooden table dominates the right-edge saliency crop.
	fillSmartCropTestRect(img, image.Rect(255, 105, 317, 170), color.RGBA{R: 160, G: 110, B: 90, A: 255})
	// A central subject is intentionally dark/neutral and supplies no warm or
	// ML anchor; the scene-level furniture rejection must still avoid the room.
	fillSmartCropTestRect(img, image.Rect(145, 45, 176, 166), color.RGBA{R: 45, G: 49, B: 54, A: 255})
	samples := make([]smartCropV2Sample, 5)
	for i := range samples {
		samples[i] = smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: 2626},
			img:   img,
		}
	}
	x, ok := smartCropUnanchoredEdgeFurnitureFallbackX(samples, 3840, 1214)
	if !ok || x != 1312 {
		t.Fatalf("fallback x=%d ok=%v want centered 1312", x, ok)
	}
}

func TestIsolatedFaceExcursionAfterStableAnchorsIsRejected(t *testing.T) {
	const cropW = 606
	samples := make([]smartCropV2Sample, 6)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * 2_000, X: 700}
	}
	for i := 0; i < 4; i++ {
		samples[i].face = &smartCropFace{CenterX: 1000 + i*4, Quality: 24}
	}
	samples[4].face = &smartCropFace{CenterX: 1600, Quality: 13}
	samples[4].detailedFace = samples[4].face

	if got := filterSmartCropIsolatedFaceExcursions(samples, cropW); got != 1 {
		t.Fatalf("filtered=%d want=1; samples=%+v", got, samples)
	}
	if samples[4].face != nil || samples[4].detailedFace != nil {
		t.Fatalf("isolated furniture face was retained: %+v", samples[4])
	}
}

func TestIsolatedFaceExcursionPreservesConfirmedTraversal(t *testing.T) {
	const cropW = 606
	samples := make([]smartCropV2Sample, 7)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * 2_000, X: 700}
	}
	for i := 0; i < 4; i++ {
		samples[i].face = &smartCropFace{CenterX: 1000 + i*4, Quality: 24}
	}
	samples[4].face = &smartCropFace{CenterX: 1600, Quality: 13}
	samples[5].face = &smartCropFace{CenterX: 1620, Quality: 14}

	if got := filterSmartCropIsolatedFaceExcursions(samples, cropW); got != 0 {
		t.Fatalf("confirmed traversal filtered %d faces: %+v", got, samples)
	}
	if samples[4].face == nil {
		t.Fatal("confirmed face traversal was removed")
	}
}

func TestBackgroundEdgeDepartureReturnsToStableFaceTrack(t *testing.T) {
	const srcW, cropW = 1920, 606
	samples := make([]smartCropV2Sample, 7)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * 2_000, X: 700}
	}
	for i := 0; i < 4; i++ {
		samples[i].face = &smartCropFace{CenterX: 1000 + i*3, Quality: 24}
	}
	for i := 4; i < len(samples); i++ {
		samples[i].point.X = srcW - cropW
		samples[i].backgroundTracked = true
	}

	if got := correctSmartCropBackgroundEdgeDeparturesFromFaces(samples, srcW, cropW); got != 3 {
		t.Fatalf("corrected=%d want=3; samples=%+v", got, samples)
	}
	for i := 4; i < len(samples); i++ {
		if samples[i].point.X < 690 || samples[i].point.X > 710 || !samples[i].faceTracked {
			t.Fatalf("sample %d did not return to stable face track: %+v", i, samples[i])
		}
	}
}

func TestBackgroundEdgeDeparturePreservesSustainedMotion(t *testing.T) {
	const srcW, cropW = 1920, 606
	samples := make([]smartCropV2Sample, 7)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * 2_000, X: 700}
	}
	for i := 0; i < 4; i++ {
		samples[i].face = &smartCropFace{CenterX: 1000, Quality: 24}
	}
	for i := 4; i < len(samples); i++ {
		samples[i].point.X = srcW - cropW
		samples[i].backgroundTracked = true
		samples[i].motionTracked = true
	}

	if got := correctSmartCropBackgroundEdgeDeparturesFromFaces(samples, srcW, cropW); got != 0 {
		t.Fatalf("sustained motion was overwritten: corrected=%d samples=%+v", got, samples)
	}
}

func TestBackgroundFurnitureClusterReturnsToStableFaceTrack(t *testing.T) {
	const srcW, cropW = 3840, 1214
	samples := make([]smartCropV2Sample, 8)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * 2_000, X: 710}
	}
	for i := 0; i < 4; i++ {
		samples[i].face = &smartCropFace{CenterX: 1320 + i*3, Quality: 24}
	}
	for i, x := range []int{1330, 1500, 1640, 1580} {
		samples[i+4].point.X = x
		samples[i+4].backgroundTracked = true
	}

	if got := correctSmartCropBackgroundEdgeDeparturesFromFaces(samples, srcW, cropW); got != 4 {
		t.Fatalf("corrected=%d want=4; samples=%+v", got, samples)
	}
	for i := 4; i < len(samples); i++ {
		if samples[i].point.X < 700 || samples[i].point.X > 730 {
			t.Fatalf("sample %d remained on furniture cluster: %+v", i, samples[i])
		}
	}
}

func TestFaceTrackInterpolationPreservesCertifiedMotion(t *testing.T) {
	const srcW, cropW = 1920, 606
	samples := []smartCropV2Sample{
		{
			point: cropPathPoint{AtMs: 0, X: 700},
			face:  &smartCropFace{CenterX: 1000, MinX: 940, MaxX: 1060, Quality: 24},
		},
		{point: cropPathPoint{AtMs: 1_000, X: 300}, motionTracked: true},
		{point: cropPathPoint{AtMs: 2_000, X: 350}, motionTracked: true},
		{
			point: cropPathPoint{AtMs: 3_000, X: 1100},
			face:  &smartCropFace{CenterX: 1400, MinX: 1340, MaxX: 1460, Quality: 24},
		},
	}

	correctSmartCropFaceTracks(samples, srcW, cropW)
	if samples[1].point.X != 300 || samples[2].point.X != 350 {
		t.Fatalf("face interpolation overwrote certified traversal: %+v", samples)
	}
	if samples[1].faceTracked || samples[2].faceTracked {
		t.Fatalf("motion samples were mislabeled as face interpolations: %+v", samples)
	}
}

func TestUnanchoredEdgeFurnitureKeepsTallEdgeSubject(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 92, G: 98, B: 104, A: 255})
	fillSmartCropTestRect(img, image.Rect(265, 38, 290, 168), color.RGBA{R: 160, G: 110, B: 90, A: 255})
	samples := make([]smartCropV2Sample, 5)
	for i := range samples {
		samples[i] = smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: 2626},
			img:   img,
		}
	}
	if x, ok := smartCropUnanchoredEdgeFurnitureFallbackX(samples, 3840, 1214); ok {
		t.Fatalf("tall edge subject incorrectly fell back to x=%d", x)
	}
}

func TestPromoteSmartCropCoherentDetailedFaceClusters(t *testing.T) {
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 0, X: 300}, detailedFace: &smartCropFace{CenterX: 1050, Quality: 9}},
		{point: cropPathPoint{AtMs: 1000, X: 300}, detailedFace: &smartCropFace{CenterX: 1060, Quality: 12}},
		{point: cropPathPoint{AtMs: 2000, X: 300}, detailedFace: &smartCropFace{CenterX: 1055, Quality: 24}},
	}
	if got := promoteSmartCropCoherentDetailedFaceClusters(samples, 500); got != 3 {
		t.Fatalf("promoted=%d want 3", got)
	}
	for i := range samples {
		if samples[i].face == nil || samples[i].face.CenterX < 1000 {
			t.Fatalf("sample %d face=%+v want coherent right-side face", i, samples[i].face)
		}
	}
}

func TestPromoteSmartCropCoherentDetailedFaceClustersRejectsUnverifiedVotes(t *testing.T) {
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 0, X: 300}, detailedFace: &smartCropFace{CenterX: 1050, Quality: 9}},
		{point: cropPathPoint{AtMs: 1000, X: 300}, detailedFace: &smartCropFace{CenterX: 1060, Quality: 12}},
		{point: cropPathPoint{AtMs: 2000, X: 300}, detailedFace: &smartCropFace{CenterX: 1055, Quality: 11}},
	}
	if got := promoteSmartCropCoherentDetailedFaceClusters(samples, 500); got != 0 {
		t.Fatalf("promoted=%d want 0 without a strong confirming detection", got)
	}
	for i := range samples {
		if samples[i].face != nil {
			t.Fatalf("sample %d unexpectedly promoted face=%+v", i, samples[i].face)
		}
	}
}

func TestConcentratedRawSubjectCropProtectsOccludedRecliningPerson(t *testing.T) {
	// Mirrors the score profile of the hand-covered reclining regression: the
	// raw saliency window contains 37% of all subject evidence and remains
	// within 82% of the best subject window.
	x, changed := concentratedRawSubjectCropX(330, 520, 1920, 606, 37, 42, 100)
	if !changed || x != 330 {
		t.Fatalf("concentrated raw person crop was not retained: x=%d changed=%v", x, changed)
	}
}

func TestConcentratedRawSubjectCropRejectsUniformWarmRoom(t *testing.T) {
	// A uniform 16:9 background contributes about one crop-width (31.5%) of
	// its score to every portrait window and fails both concentration gates.
	if x, changed := concentratedRawSubjectCropX(330, 520, 1920, 606, 31.5, 31.5, 100); changed || x != 520 {
		t.Fatalf("uniform warm room defeated stabilization: x=%d changed=%v", x, changed)
	}
}

func TestHeadAwareNarrowCropAcceptsSmallTallFace(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// A frontal face at 1280px can shrink to only 6-7% of the 320px analysis
	// frame while retaining a distinctly taller-than-wide head shape.
	fillSmartCropTestEllipse(img, 100, 112, 10, 15, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(img, 448, 1280, 404); !changed || x >= 448 {
		t.Fatalf("small tall face was not contained: x=%d changed=%v", x, changed)
	}
}

func TestLateHeadRefinementActivatesDenseTracking(t *testing.T) {
	plain := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(plain, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	profile := cloneRGBA(plain)
	fillSmartCropTestEllipse(profile, 100, 112, 10, 15, color.RGBA{R: 205, G: 160, B: 140, A: 255})

	samples := make([]smartCropV2Sample, 6)
	for i := range samples {
		samples[i] = smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * 5_000, X: 448},
			img:   plain,
		}
	}
	samples[4].img = profile
	samples[5].img = profile
	if !smartCropLateRefinementNeedsSourceSamples(samples, 1280, 404) {
		t.Fatal("late head/profile correction did not activate dense tracking")
	}
}

func TestTallSubjectExtentCropProtectsConnectedLeaningPerson(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	skin := color.RGBA{R: 205, G: 150, B: 125, A: 255}
	fillSmartCropTestRect(img, image.Rect(203, 54, 228, 154), skin)
	fillSmartCropTestEllipse(img, 202, 53, 14, 22, skin)
	x, changed := tallSubjectExtentAwareNarrowSmartCropX(img, 400, 1280, 404)
	if !changed {
		t.Fatal("connected leaning subject extent was not protected")
	}
	if x < 560 || x > 700 {
		t.Fatalf("extent-safe crop x=%d want [560,700]", x)
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

func TestHeadAwareNarrowCropDoesNotChaseHandsAtCropEdge(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// Production failures in resist/thinking_hypno contained hand-sized warm
	// components at the lower-right edge. Pulling the portrait crop toward the
	// hand removed the subject's face on the opposite side.
	fillSmartCropTestEllipse(img, 227, 124, 16, 10, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(img, 774, 1920, 606); changed || x != 774 {
		t.Fatalf("hand-shaped warm region moved crop: x=%d changed=%v", x, changed)
	}

	forearm := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(forearm, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	fillSmartCropTestEllipse(forearm, 235, 126, 21, 11, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := headAwareNarrowSmartCropX(forearm, 768, 1920, 606); changed || x != 768 {
		t.Fatalf("forearm-shaped warm region moved crop: x=%d changed=%v", x, changed)
	}
}

func TestRecliningSubjectAwareCropProtectsPairedLowerExtent(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	// A head/arm region and torso region are both tall, substantial, low in
	// frame, and close enough to form one horizontal reclining subject.
	fillSmartCropTestEllipse(img, 108, 157, 13, 21, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	fillSmartCropTestEllipse(img, 170, 156, 17, 23, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	x, changed := recliningSubjectAwareNarrowSmartCropX(img, 448, 1280, 404)
	if !changed {
		t.Fatal("paired reclining extent was not protected")
	}
	if x < 250 || x > 380 {
		t.Fatalf("reclining extent crop x=%d want [250,380]", x)
	}
}

func TestRecliningSubjectAwareCropRejectsSingleLowerObject(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 320, 180))
	fillImage(img, color.RGBA{R: 82, G: 88, B: 96, A: 255})
	fillSmartCropTestEllipse(img, 108, 157, 13, 21, color.RGBA{R: 205, G: 160, B: 140, A: 255})
	if x, changed := recliningSubjectAwareNarrowSmartCropX(img, 448, 1280, 404); changed || x != 448 {
		t.Fatalf("single lower object moved crop: x=%d changed=%v", x, changed)
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

func TestSmartCropTraversalRejectsAlternatingBackgroundSaliency(t *testing.T) {
	xs := []int{0, 900, 0, 900, 0, 900, 0, 900, 0}
	samples := make([]smartCropV2Sample, len(xs))
	for i, x := range xs {
		samples[i] = smartCropV2Sample{
			point:         cropPathPoint{AtMs: int64(i) * 1_000, X: x},
			motionTracked: true,
		}
	}
	if smartCropSceneHasSustainedTraversal(samples, 606) {
		t.Fatal("alternating subject/background guesses must not disable temporal correction")
	}
}

func TestSmartCropTraversalAcceptsCoherentFollowing(t *testing.T) {
	xs := []int{0, 140, 300, 470, 650, 820, 960}
	samples := make([]smartCropV2Sample, len(xs))
	for i, x := range xs {
		samples[i] = smartCropV2Sample{
			point:         cropPathPoint{AtMs: int64(i) * 1_000, X: x},
			motionTracked: true,
		}
	}
	if !smartCropSceneHasSustainedTraversal(samples, 606) {
		t.Fatal("directionally coherent subject motion must remain a tracked traversal")
	}
}

func TestFillSmartCropMotionGapsInterpolatesOnlyShortHoles(t *testing.T) {
	samples := []smartCropV2Sample{
		{point: cropPathPoint{AtMs: 0, X: 200}, motionTracked: true},
		{point: cropPathPoint{AtMs: 1_000, X: 0}},
		{point: cropPathPoint{AtMs: 2_000, X: 600}, motionTracked: true},
		{point: cropPathPoint{AtMs: 6_000, X: 0}},
	}
	if got := fillSmartCropMotionGaps(samples, 1920, 606); got != 1 {
		t.Fatalf("filled=%d want 1", got)
	}
	if samples[1].point.X != 400 || !samples[1].motionTracked {
		t.Fatalf("short tracking hole was not interpolated: %+v", samples[1])
	}
	if samples[3].point.X != 0 || samples[3].motionTracked {
		t.Fatalf("long boundary gap must stay untouched: %+v", samples[3])
	}
}

func TestAnchorSmartCropPathDeduplicatesClampedBoundaries(t *testing.T) {
	path := []cropPathPoint{
		{AtMs: 1_000, X: 10},
		{AtMs: 1_500, X: 200},
		{AtMs: 2_000, X: 300},
		{AtMs: 2_500, X: 400},
	}
	got := anchorSmartCropPath(path, 1_500, 2_000)
	if len(got) != 2 || got[0].AtMs != 1_500 || got[0].X != 200 ||
		got[1].AtMs != 2_000 || got[1].X != 300 {
		t.Fatalf("duplicate boundary anchors survived: %v", got)
	}
}

func TestVeryLowMotionRecurringSubjectConfidence(t *testing.T) {
	result := smartCropTemporalResult{
		Samples:        9,
		Concentration:  0.9845,
		MeanActivity:   0.1227,
		ActiveFraction: 0.0072,
		AnchorCoverage: 7,
		AnchorScore:    610,
	}
	if !smartCropTemporalResultConfident(result) {
		t.Fatalf("Maria low-motion person profile was rejected: %+v", result)
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

func TestReelAbruptStaticClustersUseBroadActivityToKeepSubject(t *testing.T) {
	samples := make([]smartCropV2Sample, 10)
	for i := range samples {
		x := 1_020
		if i >= 5 {
			x = 0
		}
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: x}
	}
	result := smartCropTemporalResult{
		X:              760,
		Samples:        len(samples),
		Concentration:  0.40,
		MeanActivity:   4.0,
		ActiveFraction: 0.20,
	}
	corrected := correctSmartCropAbruptStaticClusters(samples, result, 3840, 1214)
	if corrected != 5 {
		t.Fatalf("corrected %d points, want 5", corrected)
	}
	for i, sample := range samples {
		if sample.point.X != 1_020 || !sample.temporalTrack {
			t.Fatalf("sample %d did not retain subject cluster: %+v", i, sample)
		}
	}
}

func TestReelAbruptStaticClustersPreserveTraversalAndAmbiguity(t *testing.T) {
	result := smartCropTemporalResult{
		X: 600, Samples: 8, Concentration: 0.40, MeanActivity: 4.0, ActiveFraction: 0.20,
	}
	gradual := make([]smartCropV2Sample, 8)
	for i := range gradual {
		gradual[i].point.X = i * 220
	}
	if corrected := correctSmartCropAbruptStaticClusters(gradual, result, 3840, 1214); corrected != 0 {
		t.Fatalf("gradual traversal changed %d points: %+v", corrected, gradual)
	}

	ambiguous := make([]smartCropV2Sample, 8)
	for i := range ambiguous {
		if i < 4 {
			ambiguous[i].point.X = 200
		} else {
			ambiguous[i].point.X = 1_000
		}
	}
	result.X = 600
	if corrected := correctSmartCropAbruptStaticClusters(ambiguous, result, 3840, 1214); corrected != 0 {
		t.Fatalf("ambiguous clusters changed %d points: %+v", corrected, ambiguous)
	}
}

func TestReelIsolatedMotionBoundarySceneKeepsBracketedSubject(t *testing.T) {
	samples := make([]smartCropV2Sample, 18)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 0}
	}
	for i, x := range []int{1_008, 996, 984} {
		samples[i].point.X = x
	}
	samples[3].point.X = 1_716
	samples[3].motionTracked = true
	for i, x := range []int{996, 984, 972} {
		samples[i+4].point.X = x
	}

	corrected := correctSmartCropIsolatedMotionBoundaryScenes(samples, 3840, 1214)
	if corrected != len(samples) {
		t.Fatalf("corrected %d points, want %d", corrected, len(samples))
	}
	for i, sample := range samples {
		if sample.point.X < 970 || sample.point.X > 1_010 || !sample.temporalTrack {
			t.Fatalf("sample %d did not retain the bracketed subject: %+v", i, sample)
		}
	}
}

func TestReelIsolatedMotionBoundarySceneRejectsTraversalAndWeakEvidence(t *testing.T) {
	fixture := func() []smartCropV2Sample {
		samples := make([]smartCropV2Sample, 18)
		for i := range samples {
			samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 0}
		}
		for i := 0; i < 3; i++ {
			samples[i].point.X = 1_000
		}
		samples[3].point.X = 1_700
		samples[3].motionTracked = true
		for i := 4; i < 7; i++ {
			samples[i].point.X = 1_000
		}
		return samples
	}

	multipleMotion := fixture()
	multipleMotion[8].point.X = 1_800
	multipleMotion[8].motionTracked = true
	if corrected := correctSmartCropIsolatedMotionBoundaryScenes(multipleMotion, 3840, 1214); corrected != 0 {
		t.Fatalf("multiple motion anchors changed %d points: %+v", corrected, multipleMotion)
	}

	unbracketed := fixture()
	for i := 4; i < 7; i++ {
		unbracketed[i].point.X = 0
	}
	if corrected := correctSmartCropIsolatedMotionBoundaryScenes(unbracketed, 3840, 1214); corrected != 0 {
		t.Fatalf("unbracketed transition changed %d points: %+v", corrected, unbracketed)
	}

	gradual := fixture()
	for i := range gradual {
		gradual[i].point.X = i * 120
	}
	gradual[3].motionTracked = true
	if corrected := correctSmartCropIsolatedMotionBoundaryScenes(gradual, 3840, 1214); corrected != 0 {
		t.Fatalf("gradual traversal changed %d points: %+v", corrected, gradual)
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

func TestReelStationaryRunsRecoverInsideSustainedTraversal(t *testing.T) {
	left := makeTemporalTestSamples([]int{92, 98, 104, 110, 116}, 740)
	moving := makeTemporalTestSamples([]int{128, 176, 224}, 300)
	right := makeTemporalTestSamples([]int{140, 134, 128, 122, 116}, 740)
	samples := append(append(left, moving...), right...)
	for i := range samples {
		samples[i].point.AtMs = int64(i) * smartCropTrackingIntervalMs
	}
	for i, x := range []int{300, 500, 700} {
		idx := len(left) + i
		samples[idx].point.X = x
		samples[idx].motionTracked = true
	}
	if !smartCropSceneHasSustainedTraversal(samples, 404) {
		t.Fatal("fixture must remain a traversal so scene-wide correction stays disabled")
	}
	if corrected := correctSmartCropStationaryRuns(samples, 1280, 404); corrected != len(left)+len(right) {
		t.Fatalf("corrected %d stationary points, want %d", corrected, len(left)+len(right))
	}
	for i, sample := range samples {
		if sample.motionTracked {
			want := []int{300, 500, 700}[i-len(left)]
			if sample.point.X != want || sample.temporalTrack {
				t.Fatalf("motion anchor %d changed: %+v want x=%d", i, sample, want)
			}
			continue
		}
		if sample.point.X < 250 || sample.point.X > 500 || !sample.temporalTrack {
			t.Fatalf("stationary point %d was not recovered around the person: %+v", i, sample)
		}
	}
}

func TestReelStationaryRunsRejectLowConfidenceBackground(t *testing.T) {
	samples := make([]smartCropV2Sample, 5)
	for i := range samples {
		img := image.NewRGBA(image.Rect(0, 0, 320, 180))
		fillImage(img, color.RGBA{R: uint8(70 + i), G: uint8(80 + i), B: uint8(90 + i), A: 255})
		samples[i] = smartCropV2Sample{
			point: cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 740},
			img:   img,
		}
	}
	if corrected := correctSmartCropStationaryRuns(samples, 1280, 404); corrected != 0 {
		t.Fatalf("low-confidence background changed %d points", corrected)
	}
	for i, sample := range samples {
		if sample.point.X != 740 || sample.temporalTrack {
			t.Fatalf("low-confidence point %d changed: %+v", i, sample)
		}
	}
}

func TestStationaryRunRecognizesBackgroundMotionContinuity(t *testing.T) {
	const cropW = 404
	samples := make([]smartCropV2Sample, 15)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 488}
	}
	samples[0].point.X, samples[0].motionTracked = 732, true
	samples[14].point.X, samples[14].motionTracked = 724, true
	for _, i := range []int{1, 3, 4, 5, 6, 7, 9, 10, 11} {
		samples[i].point.X = 716
		samples[i].backgroundTracked = true
	}
	if !smartCropStationaryRunHasBackgroundContinuity(samples, 1, 14, cropW) {
		t.Fatal("clustered background observations aligned with both motion anchors were rejected")
	}
	samples[14].point.X = 1_050
	if smartCropStationaryRunHasBackgroundContinuity(samples, 1, 14, cropW) {
		t.Fatal("divergent motion anchors were treated as one stationary subject")
	}
}

func TestReelStationarySubjectTailFollowsClusteredHandoff(t *testing.T) {
	samples := make([]smartCropV2Sample, 11)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 504}
	}
	samples[0].point.X = 584
	samples[0].motionTracked = true
	candidates := []int{564, 568, 576, 632, 640, 684, 680, 676}
	if corrected := correctSmartCropStationarySubjectRun(samples, 1, len(samples), candidates, 8, 1280, 404); corrected != 10 {
		t.Fatalf("corrected=%d want 10", corrected)
	}
	for i := 1; i < len(samples); i++ {
		if samples[i].point.X != 640 || !samples[i].temporalTrack || samples[i].motionTracked {
			t.Fatalf("tail sample %d was not stabilized: %+v", i, samples[i])
		}
	}
	if samples[0].point.X != 584 || !samples[0].motionTracked {
		t.Fatalf("motion anchor changed: %+v", samples[0])
	}
}

func TestReelStationarySubjectTailRejectsUnsafeEvidence(t *testing.T) {
	fixture := func() []smartCropV2Sample {
		samples := make([]smartCropV2Sample, 7)
		for i := range samples {
			samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 500}
		}
		samples[0].point.X = 580
		samples[0].motionTracked = true
		return samples
	}

	noAnchor := fixture()
	noAnchor[0].motionTracked = false
	if got := correctSmartCropStationarySubjectRun(noAnchor, 1, len(noAnchor), []int{620, 624, 628, 632}, 4, 1280, 404); got != 0 {
		t.Fatalf("unanchored run changed %d samples", got)
	}

	dispersed := fixture()
	if got := correctSmartCropStationarySubjectRun(dispersed, 1, len(dispersed), []int{300, 420, 620, 760}, 4, 1280, 404); got != 0 {
		t.Fatalf("dispersed candidates changed %d samples", got)
	}

	traversal := fixture()
	for i := 1; i < len(traversal); i++ {
		traversal[i].point.X += i * 30
	}
	if got := correctSmartCropStationarySubjectRun(traversal, 1, len(traversal), []int{620, 624, 628, 632}, 4, 1280, 404); got != 0 {
		t.Fatalf("moving run changed %d samples", got)
	}

	distant := fixture()
	if got := correctSmartCropStationarySubjectRun(distant, 1, len(distant), []int{800, 804, 808, 812}, 4, 1280, 404); got != 0 {
		t.Fatalf("distant candidates changed %d samples", got)
	}
}

func TestReelStationaryRunsRecoverFromAdjacentMotionContinuity(t *testing.T) {
	samples := make([]smartCropV2Sample, 12)
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 40}
		samples[i].img = temporalTestFrame(320, 180, color.RGBA{R: 70, G: 80, B: 90, A: 255}, 100)
	}
	samples[8].point.X = 1_080
	samples[8].motionTracked = true
	for i := 9; i < len(samples); i++ {
		samples[i].point.X = 1_100
		samples[i].motionTracked = true
	}

	corrected := correctSmartCropStationaryRuns(samples, 3840, 1214)
	if corrected != 8 {
		t.Fatalf("corrected %d points, want 8", corrected)
	}
	for i := 0; i < 8; i++ {
		if samples[i].point.X != 1_080 || !samples[i].temporalTrack || samples[i].motionTracked {
			t.Fatalf("leading stationary point %d not recovered: %+v", i, samples[i])
		}
	}
	for i := 8; i < len(samples); i++ {
		if !samples[i].motionTracked {
			t.Fatalf("motion anchor %d changed: %+v", i, samples[i])
		}
	}
}

func TestDenseRunWideUncertifiedExtentIsBounded(t *testing.T) {
	xs := []int{1_092, 1_080, 660, 660, 660, 624}
	samples := make([]smartCropV2Sample, len(xs))
	for i, x := range xs {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: x}
	}
	correctSmartCropDenseRun(samples, 1_104, 3_840, 1_214)
	for i, sample := range samples {
		if sample.point.X < 800 || sample.point.X > 1_104 || !sample.temporalTrack {
			t.Fatalf("uncertified wide extent point %d escaped safe bound: %+v", i, sample)
		}
	}
}

func TestDenseRunReversalReleasesPreviousExtent(t *testing.T) {
	xs := []int{512, 448, 396, 448, 648, 636}
	samples := make([]smartCropV2Sample, len(xs))
	for i, x := range xs {
		samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: x}
	}
	correctSmartCropDenseRun(samples, 524, 1280, 404)
	if samples[2].point.X > 460 {
		t.Fatalf("leftward extent was not entered: %+v", samples)
	}
	if samples[len(samples)-1].point.X < 600 {
		t.Fatalf("opposite extent did not release prior state: %+v", samples)
	}
}

func TestDenseRunPreservesSparseClusteredExtent(t *testing.T) {
	xs := []int{464, 464, 304, 464, 464, 272, 382, 300, 300}
	times := []int64{0, 1_000, 2_000, 3_000, 4_000, 8_000, 9_000, 10_000, 11_000}
	samples := make([]smartCropV2Sample, len(xs))
	for i := range samples {
		samples[i].point = cropPathPoint{AtMs: times[i], X: xs[i]}
	}
	correctSmartCropDenseRun(samples, 464, 1280, 404)
	if samples[len(samples)-1].point.X >= 400 {
		t.Fatalf("sparse reclining extent was discarded: %+v", samples)
	}
	for i := range samples {
		if !samples[i].temporalTrack {
			t.Fatalf("sample %d was not stabilized: %+v", i, samples[i])
		}
	}
}

func TestTightSmartCropExtentClusterIgnoresOutlier(t *testing.T) {
	got := tightSmartCropExtentCluster([]int{304, 272, 382, 300, 300}, 67)
	if len(got) != 4 || got[0] != 272 || got[len(got)-1] != 304 {
		t.Fatalf("cluster=%v want [272 300 300 304]", got)
	}
}

func TestReelStationaryContinuityRejectsOpposingOrDistantAnchors(t *testing.T) {
	makeSamples := func() []smartCropV2Sample {
		samples := make([]smartCropV2Sample, 7)
		for i := range samples {
			samples[i].point = cropPathPoint{AtMs: int64(i) * smartCropTrackingIntervalMs, X: 700}
		}
		samples[0].point.X = 100
		samples[0].motionTracked = true
		samples[6].point.X = 1_500
		samples[6].motionTracked = true
		return samples
	}

	distant := makeSamples()
	if corrected := correctSmartCropStationaryRunFromMotionContinuity(distant, 1, 6, 3840, 1214); corrected != 0 {
		t.Fatalf("distant traversal anchors changed %d points: %+v", corrected, distant)
	}

	opposing := makeSamples()
	opposing[0].point.X = 900
	opposing[6].point.X = 1_000
	for i, x := range []int{100, 1_800, 100, 1_800, 100} {
		opposing[i+1].point.X = x
	}
	if corrected := correctSmartCropStationaryRunFromMotionContinuity(opposing, 1, 6, 3840, 1214); corrected != 0 {
		t.Fatalf("opposing outliers changed %d points: %+v", corrected, opposing)
	}
}

func TestReelStationaryRunsDoNotCrossSceneCut(t *testing.T) {
	left := makeTemporalTestSamples([]int{92, 98, 104}, 740)
	right := makeTemporalTestSamples([]int{220, 226, 232}, 740)
	samples := append(left, right...)
	for i := range samples {
		samples[i].point.AtMs = int64(i) * smartCropTrackingIntervalMs
	}
	samples[len(left)].point.Cut = true
	if corrected := correctSmartCropStationaryRuns(samples, 1280, 404); corrected != len(samples) {
		t.Fatalf("corrected %d points, want %d", corrected, len(samples))
	}
	leftX, rightX := samples[0].point.X, samples[len(left)].point.X
	if leftX >= rightX || rightX-leftX < 300 {
		t.Fatalf("scene cut was flattened: left=%d right=%d samples=%+v", leftX, rightX, samples)
	}
	for i := 1; i < len(left); i++ {
		if samples[i].point.X != leftX {
			t.Fatalf("left scene point %d differs: %+v", i, samples[i])
		}
	}
	for i := len(left) + 1; i < len(samples); i++ {
		if samples[i].point.X != rightX {
			t.Fatalf("right scene point %d differs: %+v", i, samples[i])
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

func BenchmarkCorrectSmartCropStationaryRuns(b *testing.B) {
	left := makeTemporalTestSamples([]int{92, 98, 104, 110, 116}, 740)
	moving := makeTemporalTestSamples([]int{128, 176, 224}, 300)
	right := makeTemporalTestSamples([]int{140, 134, 128, 122, 116}, 740)
	template := append(append(left, moving...), right...)
	for i := range template {
		template[i].point.AtMs = int64(i) * smartCropTrackingIntervalMs
	}
	for i, x := range []int{300, 500, 700} {
		idx := len(left) + i
		template[idx].point.X = x
		template[idx].motionTracked = true
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		samples := append([]smartCropV2Sample(nil), template...)
		correctSmartCropStationaryRuns(samples, 1280, 404)
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

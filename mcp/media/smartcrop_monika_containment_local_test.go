package main

import (
	"context"
	"os"
	"sort"
	"testing"
)

const (
	monika10080SHA256 = "8be35154f1ce9e5cafdd760a53a9f9f83c6af87811f58daa90308ed2839c2d5e"
	monika10410SHA256 = "eb1738beee99443e85cf5bea9593f2f6dcccbd741005f57c804fccec95419ff0"
)

// TestMonikaHandContainmentLocalRegression covers the production frames and
// reels where the head edge guard mistook a hand beside a red cushion for a
// reclining face. It is opt-in because the exact source videos are too large
// for the repository. Run with MONIKA_10080_VIDEO and MONIKA_10410_VIDEO.
func TestMonikaHandContainmentLocalRegression(t *testing.T) {
	t.Run("resist", func(t *testing.T) {
		video := os.Getenv("MONIKA_10080_VIDEO")
		if video == "" {
			t.Skip("set MONIKA_10080_VIDEO to run the real-media regression")
		}
		assertFileSHA256(t, video, monika10080SHA256)
		const duration = int64(526_866)
		if x := monikaContainmentStillX(t, video, duration, 379_000); x < 650 || x > 850 {
			t.Fatalf("379000ms crop follows hand instead of face: x=%d", x)
		}
		path := monikaContainmentReelPath(t, video, duration, 355_705, 390_755)
		for _, point := range path {
			if point.X > 900 {
				t.Fatalf("resist reel leaves subject for hand at %+v; path=%v", point, path)
			}
		}
	})

	t.Run("thinking-hypno", func(t *testing.T) {
		video := os.Getenv("MONIKA_10410_VIDEO")
		if video == "" {
			t.Skip("set MONIKA_10410_VIDEO to run the real-media regression")
		}
		assertFileSHA256(t, video, monika10410SHA256)
		const duration = int64(316_466)
		for _, tc := range []struct {
			focus    int64
			min, max int
		}{{163_755, 700, 950}, {213_815, 600, 900}} {
			if x := monikaContainmentStillX(t, video, duration, tc.focus); x < tc.min || x > tc.max {
				t.Fatalf("%dms crop follows hand instead of face: x=%d want [%d,%d]", tc.focus, x, tc.min, tc.max)
			}
		}
		path := monikaContainmentReelPath(t, video, duration, 174_250, 218_855)
		for _, point := range path {
			if point.AtMs >= 210_000 && point.X > 900 {
				t.Fatalf("thinking reel leaves subject for hand at %+v; path=%v", point, path)
			}
		}
	})
}

func monikaContainmentStillX(t *testing.T, video string, duration, focus int64) int {
	t.Helper()
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		monikaContainmentNearestPositions(focus, duration, 9), 1920, 1080, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	const cropW = 606
	tracking := false
	if smartCropStillNeedsTracking(samples, focus, cropW) {
		samples, err = analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropStillTrackingPositions(focus, duration), 1920, 1080, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, 1920, cropW)
		refineSmartCropHeadSamples(samples, 1920, cropW)
		tracking = true
	}
	x, _, err := resolveSmartCropStillBase(samples, focus)
	if err != nil {
		t.Fatal(err)
	}
	if !tracking {
		scene := smartCropSceneSamples(samples, focus)
		result, ok := temporalSubjectConsensus(scene, 1920, cropW)
		if !ok {
			result, ok = staticWarmSubjectConsensus(scene, 1920, cropW)
		}
		if ok {
			if corrected, changed := applySmartCropTemporalOverride(x, result, cropW, 1920); changed {
				x = corrected
			}
		}
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil {
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, 1920, cropW); changed {
			x = corrected
		}
	}
	return clampInt(roundEven(x), 0, 1920-cropW)
}

func monikaContainmentReelPath(t *testing.T, video string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	positions := monikaContainmentRangePositions(start, end, duration)
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		positions, 1920, 1080, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if smartCropReelNeedsTracking(samples, 1920, 606) {
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropAdaptiveTrackingPositions(samples, target, duration, 1920, 606),
			1920, 1080, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		samples = mergeSmartCropSamples(samples, extra)
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, 1920, 606)
		refineSmartCropHeadSamples(samples, 1920, 606)
	}
	correctSmartCropReelTemporalOutliers(samples, 1920, 606)
	refineSmartCropHeadSamples(samples, 1920, 606)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	return stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), 606, 1920)
}

func monikaContainmentNearestPositions(focus, duration int64, limit int) []int64 {
	positions := make([]int64, 0)
	for position := int64(1_000); position < duration; position += 5_000 {
		positions = append(positions, position)
	}
	sort.Slice(positions, func(i, j int) bool {
		di, dj := absInt64(positions[i]-focus), absInt64(positions[j]-focus)
		if di == dj {
			return positions[i] < positions[j]
		}
		return di < dj
	})
	if len(positions) > limit {
		positions = positions[:limit]
	}
	sort.Slice(positions, func(i, j int) bool { return positions[i] < positions[j] })
	return positions
}

func monikaContainmentRangePositions(start, end, duration int64) []int64 {
	positions := make([]int64, 0)
	for position := int64(1_000); position < duration; position += 5_000 {
		if position >= start-5_000 && position <= end+5_000 {
			positions = append(positions, position)
		}
	}
	return positions
}

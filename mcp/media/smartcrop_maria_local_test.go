package main

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"path/filepath"
	"testing"

	"github.com/nfnt/resize"
)

const maria10526SHA256 = "e23f3e64cae5244bdb0235b7d13394488e81986ae853483222f4cbcd868a078c"

// TestMaria10526LocalRegression replays the exact production source that
// exposed a high-contrast static background winning over a dark-clothed
// person. The fixture is opt-in because the source video is too large for the
// repository.
func TestMaria10526LocalRegression(t *testing.T) {
	video := os.Getenv("MARIA_10526_VIDEO")
	if video == "" {
		t.Skip("set MARIA_10526_VIDEO to run the real-media regression")
	}
	assertFileSHA256(t, video, maria10526SHA256)
	const duration = int64(289_166)

	t.Run("standalone-images", func(t *testing.T) {
		for _, tc := range []struct {
			file     string
			min, max int
		}{
			{"10587.png", 800, 1_150},
			{"10591.png", 800, 1_150},
			{"10595.png", 1_000, 1_450},
			{"10599.png", 0, 600},
			{"10603.png", 500, 1_000},
		} {
			x := mariaStandaloneImageX(t, filepath.Join(filepath.Dir(video), tc.file))
			if x < tc.min || x > tc.max {
				t.Errorf("%s crop x=%d want [%d,%d]", tc.file, x, tc.min, tc.max)
			}
		}
	})

	for _, tc := range []struct {
		focus    int64
		min, max int
	}{
		{74_200, 800, 1_150},
		{108_000, 800, 1_150},
		{166_000, 1_000, 1_450},
		{205_000, 0, 600},
		{249_000, 500, 1_000},
	} {
		t.Run(fmt.Sprintf("still-%d", tc.focus), func(t *testing.T) {
			x := mariaStillX(t, video, duration, tc.focus)
			if x < tc.min || x > tc.max {
				t.Fatalf("crop x=%d want [%d,%d]", x, tc.min, tc.max)
			}
		})
	}

	t.Run("reel-first-zombie-stationary-recovery", func(t *testing.T) {
		const start, end = int64(154_985), int64(190_944)
		path := mariaReelPath(t, video, duration, start, end)
		for _, point := range path {
			if point.Cut {
				t.Fatalf("single shot was misclassified as a scene cut at %+v; path=%v", point, path)
			}
		}
		for _, want := range []struct {
			at       int64
			min, max int
		}{
			{159_000, 300, 750},
			{160_000, 300, 800},
			{178_000, 250, 750},
			{179_000, 250, 750},
			{185_000, 300, 800},
			{188_000, 300, 850},
			{190_000, 300, 850},
		} {
			x, ok := mariaInterpolatedPathX(path, want.at)
			if !ok || x < want.min || x > want.max {
				t.Fatalf("stationary recovery at %dms x=%d found=%v want [%d,%d]; path=%v", want.at, x, ok, want.min, want.max, path)
			}
		}
		if outputDir := os.Getenv("MARIA_10526_OUTPUT_DIR"); outputDir != "" {
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(1214, 2158, 0, start, path) + ",scale=1080:1920,setsar=1"
			runFFmpeg(t, "-ss", secondsString(start), "-i", video,
				"-t", secondsString(end-start), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
				filepath.Join(outputDir, "maria-first-zombie-stationary-recovery.mp4"))
		}
	})

	for _, tc := range []struct {
		name       string
		start, end int64
		min, max   int
	}{
		{"countdown", 47_845, 75_104, 800, 1_150},
		{"final-sleep", 241_775, 265_875, 800, 1_150},
	} {
		t.Run("reel-"+tc.name, func(t *testing.T) {
			path := mariaReelPath(t, video, duration, tc.start, tc.end)
			for _, point := range path {
				if point.X < tc.min || point.X > tc.max {
					t.Fatalf("subject leaves crop at %+v; path=%v", point, path)
				}
			}
		})
	}

	t.Run("reel-zombie-tracking", func(t *testing.T) {
		// Exact range selected for production render 954 / output 10664.
		const start, end = int64(191_990), int64(229_720)
		path := mariaReelPath(t, video, duration, start, end)
		if outputDir := os.Getenv("MARIA_10526_OUTPUT_DIR"); outputDir != "" {
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(1214, 2158, 0, start, path) + ",scale=1080:1920,setsar=1"
			runFFmpeg(t, "-ss", secondsString(start), "-i", video,
				"-t", secondsString(end-start), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
				filepath.Join(outputDir, "maria-zombie-tracking.mp4"))
		}
		for _, point := range path {
			if point.Cut {
				t.Fatalf("foreground traversal was misclassified as a scene cut at %+v; path=%v", point, path)
			}
		}
		for _, want := range []struct {
			at       int64
			min, max int
		}{
			{192_000, 300, 700},
			{194_000, 500, 1_000},
			{196_000, 1_700, 2_300},
			{200_000, 0, 400},
			{207_000, 1_900, 2_600},
			{211_000, 0, 400},
			{218_000, 1_800, 2_400},
			{220_000, 1_200, 1_900},
			{225_000, 0, 400},
			{228_000, 300, 1_100},
			{229_000, 300, 1_100},
		} {
			x, ok := mariaInterpolatedPathX(path, want.at)
			if !ok || x < want.min || x > want.max {
				t.Fatalf("tracking at %dms x=%d found=%v want [%d,%d]; path=%v", want.at, x, ok, want.min, want.max, path)
			}
		}
	})
}

func mariaStandaloneImageX(t *testing.T, path string) int {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	img, _, err := image.Decode(f)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	thumb := resize.Resize(320, 180, img, resize.Lanczos3)
	window, err := analyzeSmartCropV2Frame(3840, 2160, 9, 16, thumb)
	if err != nil {
		t.Fatal(err)
	}
	return window.X
}

func mariaInterpolatedPathX(path []cropPathPoint, at int64) (int, bool) {
	if len(path) == 0 || at < path[0].AtMs || at > path[len(path)-1].AtMs {
		return 0, false
	}
	for i := 1; i < len(path); i++ {
		if at <= path[i].AtMs {
			return interpolateSmartCropStillX(path[i-1], path[i], at), true
		}
	}
	return path[len(path)-1].X, true
}

func mariaStillX(t *testing.T, video string, duration, focus int64) int {
	t.Helper()
	const srcW, srcH, cropW = 3840, 2160, 1214
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		monikaContainmentNearestPositions(focus, duration, 9), srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	contextSamples := smartCropSceneSamples(samples, focus)
	contextResult, contextOK := bestSmartCropTemporalConsensus(contextSamples, srcW, cropW)
	tracking := false
	if smartCropStillNeedsTracking(samples, focus, cropW) {
		samples, err = analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropStillTrackingPositions(focus, duration), srcW, srcH, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, srcW, cropW)
		refineSmartCropHeadSamples(samples, srcW, cropW)
		fillSmartCropMotionGaps(samples, srcW, cropW)
		tracking = true
	}
	x, _, err := resolveSmartCropStillBase(samples, focus)
	if err != nil {
		t.Fatal(err)
	}
	if !tracking {
		if contextOK {
			if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
				x = corrected
			}
		}
	} else if contextOK && smartCropTemporalResultConfident(contextResult) &&
		(contextResult.StaticAnchored || !smartCropStillHasMotionEvidence(samples, focus)) {
		if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
			x = corrected
		}
	} else if contextOK && smartCropTemporalResultConfident(contextResult) {
		x = clampInt(roundEven(clampInt(x, contextResult.X-cropW/2, contextResult.X+cropW/2)), 0, srcW-cropW)
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil {
		bounds := sample.img.Bounds()
		thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(bounds.Dx())/float64(srcW))), 1, bounds.Dx())
		currentStart := clampInt(int(math.Round(float64(x)*float64(bounds.Dx())/float64(srcW))), 0, bounds.Dx()-thumbCropW)
		strongX, _, strong := strongSmartCropSilhouetteX(sample.img, srcW, cropW, thumbCropW, currentStart)
		if strong && contextOK && smartCropTemporalResultConfident(contextResult) && absInt(strongX-contextResult.X) <= cropW/2 {
			x = strongX
		} else if corrected, changed := silhouetteAwareNarrowSmartCropX(sample.img, x, srcW, cropW, thumbCropW); changed {
			x = corrected
		}
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
			x = corrected
		}
	}
	return clampInt(roundEven(x), 0, srcW-cropW)
}

func mariaReelPath(t *testing.T, video string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	const srcW, srcH, cropW = 3840, 2160, 1214
	positions := monikaContainmentRangePositions(start, end, duration)
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		positions, srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if smartCropReelNeedsTracking(samples, srcW, cropW) {
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropAdaptiveTrackingPositions(samples, target, duration, srcW, cropW),
			srcW, srcH, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		samples = mergeSmartCropSamples(samples, extra)
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, srcW, cropW)
		correctSmartCropIsolatedMotionBoundaryScenes(samples, srcW, cropW)
		fillSmartCropMotionGaps(samples, srcW, cropW)
		correctSmartCropStationaryRuns(samples, srcW, cropW)
		refineSmartCropHeadSamples(samples, srcW, cropW)
	}
	correctSmartCropReelTemporalOutliers(samples, srcW, cropW)
	refineSmartCropHeadSamples(samples, srcW, cropW)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	return stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), cropW, srcW)
}

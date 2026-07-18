package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

const monikaDrunk11788SHA256 = "b31988f5b289ceedbb5acd1ebe10636d2f90ee5dde87fa89d6317890b528e7a7"

// TestMonikaDrunk11788LocalRegression covers the production-identical source
// whose reclining and crossing shots exposed crop paths that followed the
// torso or the empty room instead of keeping the visible head in frame. It is
// opt-in because the 79 MB source is too large for the repository.
func TestMonikaDrunk11788LocalRegression(t *testing.T) {
	video := os.Getenv("MONIKA_DRUNK_11788_VIDEO")
	if video == "" {
		t.Skip("set MONIKA_DRUNK_11788_VIDEO to run the real-media regression")
	}
	assertFileSHA256(t, video, monikaDrunk11788SHA256)
	const duration = int64(527_606)

	t.Run("reclining-still", func(t *testing.T) {
		x := monikaDrunkStillX(t, video, duration, 199_645)
		t.Logf("crop x=%d", x)
		if x < 240 || x > 360 {
			t.Errorf("199645ms reclining crop is off subject: x=%d want [240,360]", x)
		}
		if outputDir := os.Getenv("MONIKA_DRUNK_11788_OUTPUT_DIR"); outputDir != "" {
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			runFFmpeg(t, "-ss", secondsString(199_645), "-i", video, "-frames:v", "1",
				"-vf", fmt.Sprintf("crop=404:718:%d:0,scale=1080:1920,setsar=1", x), "-y",
				filepath.Join(outputDir, "reclining-still.png"))
		}
	})

	fixtures := []struct {
		name       string
		start, end int64
		checks     []struct {
			at, min, max int64
		}
	}{
		{
			name: "reclining-reel", start: 162_495, end: 187_410,
			checks: []struct{ at, min, max int64 }{
				{174_495, 200, 360}, {180_495, 200, 360}, {187_000, 200, 360},
			},
		},
		{
			name: "crossing-reel", start: 383_420, end: 404_905,
			checks: []struct{ at, min, max int64 }{
				{386_420, 430, 560}, {387_920, 430, 560},
				{394_420, 400, 540}, {396_920, 520, 680},
			},
		},
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			path := monikaDrunkReelPath(t, video, duration, fixture.start, fixture.end)
			t.Logf("crop path: %v", path)
			for _, check := range fixture.checks {
				x, ok := mariaInterpolatedPathX(path, check.at)
				if !ok {
					t.Errorf("no crop path at %dms: %v", check.at, path)
					continue
				}
				if int64(x) < check.min || int64(x) > check.max {
					t.Errorf("subject lost at %dms: crop x=%d want [%d,%d]; path=%v",
						check.at, x, check.min, check.max, path)
				}
			}
			if outputDir := os.Getenv("MONIKA_DRUNK_11788_OUTPUT_DIR"); outputDir != "" {
				if err := os.MkdirAll(outputDir, 0o755); err != nil {
					t.Fatal(err)
				}
				filter := "setpts=PTS-STARTPTS," + cropFilterForPath(404, 718, 0, fixture.start, path) +
					",scale=1080:1920,setsar=1"
				runFFmpeg(t, "-ss", secondsString(fixture.start), "-i", video,
					"-t", secondsString(fixture.end-fixture.start), "-vf", filter, "-an",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
					filepath.Join(outputDir, fixture.name+".mp4"))
			}
		})
	}
}

func monikaDrunkStillX(t *testing.T, video string, duration, focus int64) int {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
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
		if corrected, changed := silhouetteAwareNarrowSmartCropX(sample.img, x, srcW, cropW, 101); changed {
			x = corrected
		}
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
			x = corrected
		}
		if corrected, changed := recliningSubjectAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
			x = corrected
		}
	}
	return clampInt(roundEven(x), 0, srcW-cropW)
}

func monikaDrunkReelPath(t *testing.T, video string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
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

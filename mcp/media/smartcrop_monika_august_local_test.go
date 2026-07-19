package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// TestMonikaAugustLocalRegression covers the production-identical sources
// from /monika/august_2024. The source files remain external because they are
// 76-98 MB each; hashes keep the opt-in regression deterministic.
func TestMonikaAugustLocalRegression(t *testing.T) {
	root := os.Getenv("MONIKA_AUGUST_FIXTURE_DIR")
	if root == "" {
		t.Skip("set MONIKA_AUGUST_FIXTURE_DIR to run the real-media regression")
	}
	outputDir := os.Getenv("MONIKA_AUGUST_OUTPUT_DIR")
	storyboardRoot := os.Getenv("MONIKA_AUGUST_STORYBOARD_DIR")
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	type stillFixture struct {
		name, file, sourceID, sha256 string
		duration, at                 int64
		minX, maxX                   int
	}
	stills := []stillFixture{
		{"drunk-199645", "drunk.mp4", "11788", monikaDrunk11788SHA256, 527_606, 199_645, 240, 360},
		{"drooling-392000", "drooling.mp4", "11981", "7360564fcb882d525a0148174c9da0db0a2416b4ef2a1bda8c961b242e60665e", 567_385, 392_000, 180, 320},
		{"drooling-426000", "drooling.mp4", "11981", "7360564fcb882d525a0148174c9da0db0a2416b4ef2a1bda8c961b242e60665e", 567_385, 426_000, 280, 420},
		{"chicken-292745", "chicken.mp4", "12147", "3ab7b6ecba8e2b2b4f2e702b2ead496fba7ec4f838efe3db7cf503d2f6402e4d", 662_960, 292_745, 480, 650},
		{"chicken-460000", "chicken.mp4", "12147", "3ab7b6ecba8e2b2b4f2e702b2ead496fba7ec4f838efe3db7cf503d2f6402e4d", 662_960, 460_000, 440, 600},
		{"voice-531000", "voice.mp4", "12319", "0c5320483be5e0fc7aea9d283edc830a7bf930c76498e2ffb26b2af0c88b4411", 741_160, 531_000, 220, 360},
	}
	verified := make(map[string]bool)
	for _, fixture := range stills {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			video := filepath.Join(root, fixture.file)
			if !verified[video] {
				assertFileSHA256(t, video, fixture.sha256)
				verified[video] = true
			}
			x := monikaDrunkStillX(t, video, fixture.duration, fixture.at)
			if storyboardRoot != "" && fixture.sourceID != "11788" {
				x = monikaAugustProductionStillX(t, video, filepath.Join(storyboardRoot, fixture.sourceID), fixture.duration, fixture.at)
			}
			t.Logf("crop x=%d", x)
			if x < fixture.minX || x > fixture.maxX {
				t.Errorf("crop x=%d want [%d,%d]", x, fixture.minX, fixture.maxX)
			}
			if outputDir != "" {
				runFFmpeg(t, "-ss", secondsString(fixture.at), "-i", video, "-frames:v", "1",
					"-vf", fmt.Sprintf("crop=404:718:%d:0,scale=1080:1920,setsar=1", x), "-y",
					filepath.Join(outputDir, fixture.name+".png"))
			}
		})
	}

	reels := []struct {
		name, file, sourceID, sha256 string
		duration, start, end         int64
		startMin, startMax           int
		endMin, endMax               int
	}{
		{"chicken-281560-315760", "chicken.mp4", "12147", "3ab7b6ecba8e2b2b4f2e702b2ead496fba7ec4f838efe3db7cf503d2f6402e4d", 662_960, 281_560, 315_760, 440, 550, 570, 700},
		{"voice-383625-410330", "voice.mp4", "12319", "0c5320483be5e0fc7aea9d283edc830a7bf930c76498e2ffb26b2af0c88b4411", 741_160, 383_625, 410_330, 370, 470, 300, 400},
	}
	for _, fixture := range reels {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			video := filepath.Join(root, fixture.file)
			if !verified[video] {
				assertFileSHA256(t, video, fixture.sha256)
				verified[video] = true
			}
			path := monikaDrunkReelPath(t, video, fixture.duration, fixture.start, fixture.end)
			if storyboardRoot != "" {
				path = monikaAugustProductionReelPath(t, video, filepath.Join(storyboardRoot, fixture.sourceID), fixture.duration, fixture.start, fixture.end)
			}
			t.Logf("crop path: %v", path)
			if len(path) < 2 {
				t.Fatalf("crop path has %d points, want at least 2", len(path))
			}
			if x := path[0].X; x < fixture.startMin || x > fixture.startMax {
				t.Errorf("start crop x=%d want [%d,%d]", x, fixture.startMin, fixture.startMax)
			}
			if x := path[len(path)-1].X; x < fixture.endMin || x > fixture.endMax {
				t.Errorf("end crop x=%d want [%d,%d]", x, fixture.endMin, fixture.endMax)
			}
			if outputDir != "" {
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

func monikaAugustProductionStillX(t *testing.T, video, storyboardDir string, duration, focus int64) int {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
	samples := monikaAugustStoryboardSamples(t, storyboardDir)
	sort.Slice(samples, func(i, j int) bool {
		di := absInt64(samples[i].point.AtMs - focus)
		dj := absInt64(samples[j].point.AtMs - focus)
		if di == dj {
			return samples[i].point.AtMs < samples[j].point.AtMs
		}
		return di < dj
	})
	if len(samples) > smartCropTemporalMaxSamples {
		samples = samples[:smartCropTemporalMaxSamples]
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].point.AtMs < samples[j].point.AtMs })
	contextSamples := smartCropSceneSamples(samples, focus)
	contextResult, contextOK := bestSmartCropTemporalConsensus(contextSamples, srcW, cropW)
	tracking := false
	if smartCropStillNeedsTracking(samples, focus, cropW) {
		var err error
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
	if tracking && contextOK && smartCropTemporalResultConfident(contextResult) {
		if corrected, changed := stabilizeSmartCropStillTrackingX(x, samples, contextResult, cropW, srcW); changed {
			x = corrected
		}
	} else if contextOK {
		if corrected, changed := applySmartCropWeakTemporalStabilizer(x, contextResult, contextSamples, cropW, srcW); changed {
			x = corrected
		}
	}
	if !tracking && contextOK {
		if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
			x = corrected
		}
	} else if tracking && contextOK && smartCropTemporalResultConfident(contextResult) &&
		(contextResult.StaticAnchored || !smartCropStillHasMotionEvidence(samples, focus)) {
		if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
			x = corrected
		}
	} else if tracking && contextOK && smartCropTemporalResultConfident(contextResult) {
		x = clampInt(roundEven(clampInt(x, contextResult.X-cropW/2, contextResult.X+cropW/2)), 0, srcW-cropW)
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil {
		bounds := sample.img.Bounds()
		thumbCropW := clampInt(int(float64(cropW)*float64(bounds.Dx())/float64(srcW)+0.5), 1, bounds.Dx())
		currentStart := clampInt(int(float64(x)*float64(bounds.Dx())/float64(srcW)+0.5), 0, bounds.Dx()-thumbCropW)
		strongX, _, strong := strongSmartCropSilhouetteX(sample.img, srcW, cropW, thumbCropW, currentStart)
		contextConfident := contextOK && smartCropTemporalResultConfident(contextResult)
		if strong && contextConfident && absInt(strongX-contextResult.X) <= cropW/2 {
			x = strongX
		} else if corrected, changed := silhouetteAwareNarrowSmartCropX(sample.img, x, srcW, cropW, thumbCropW); changed &&
			(!contextConfident || absInt(corrected-contextResult.X) <= cropW/2) {
			x = corrected
		}
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed &&
			(!contextConfident || absInt(corrected-contextResult.X) <= cropW/2) {
			x = corrected
		}
		if corrected, changed := recliningSubjectAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed &&
			(!contextConfident || absInt(corrected-contextResult.X) <= cropW/2) {
			x = corrected
		}
		if contextConfident && contextResult.SubjectAnchored &&
			contextResult.MeanActivity >= smartCropTemporalMinMeanActivity &&
			contextResult.ActiveFraction >= smartCropTemporalMinActiveFraction {
			if corrected, changed := tallSubjectExtentAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
				x = corrected
			}
		}
	}
	return clampInt(roundEven(x), 0, srcW-cropW)
}

func monikaAugustProductionReelPath(t *testing.T, video, storyboardDir string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
	all := monikaAugustStoryboardSamples(t, storyboardDir)
	selected := make([]smartCropV2Sample, 0, len(all))
	var before, after *smartCropV2Sample
	for i := range all {
		sample := all[i]
		switch {
		case sample.point.AtMs < start:
			copy := sample
			before = &copy
		case sample.point.AtMs > end:
			if after == nil {
				copy := sample
				after = &copy
			}
		default:
			selected = append(selected, sample)
		}
	}
	if before != nil {
		selected = append([]smartCropV2Sample{*before}, selected...)
	}
	if after != nil {
		selected = append(selected, *after)
	}
	samples := selected
	markSmartCropSceneCuts(samples)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if smartCropReelNeedsTracking(samples, srcW, cropW) {
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropAdaptiveTrackingPositions(samples, target, duration, srcW, cropW), srcW, srcH, 9, 16)
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
	correctSmartCropStationarySubjectTails(samples, srcW, cropW)
	refineSmartCropHeadSamples(samples, srcW, cropW)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	return stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), cropW, srcW)
}

func monikaAugustStoryboardSamples(t *testing.T, dir string) []smartCropV2Sample {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	samples := make([]smartCropV2Sample, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jpg") {
			continue
		}
		position, err := strconv.ParseInt(strings.TrimSuffix(entry.Name(), ".jpg"), 10, 64)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		win, err := analyzeSmartCropV2Frame(1280, 720, 9, 16, img)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, smartCropV2Sample{point: cropPathPoint{AtMs: position, X: win.X}, img: img})
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i].point.AtMs < samples[j].point.AtMs })
	return samples
}

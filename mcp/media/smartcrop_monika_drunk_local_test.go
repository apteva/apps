package main

import (
	"context"
	"fmt"
	"image"
	"math"
	"os"
	"os/exec"
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

	t.Run("production-storyboard-reclining-reel", func(t *testing.T) {
		path := monikaDrunkProductionCadenceReelPath(t, video, duration, 162_495, 187_410)
		t.Logf("production cadence crop path: %v", path)
		for _, at := range []int64{174_495, 180_495, 187_000} {
			x, ok := mariaInterpolatedPathX(path, at)
			if !ok {
				t.Fatalf("no crop path at %dms: %v", at, path)
			}
			if x > 320 {
				t.Errorf("reclining face leaves frame at %dms: crop x=%d want <=320; path=%v", at, x, path)
			}
		}
		if outputDir := os.Getenv("MONIKA_DRUNK_11788_OUTPUT_DIR"); outputDir != "" {
			if err := os.MkdirAll(outputDir, 0o755); err != nil {
				t.Fatal(err)
			}
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(404, 718, 0, 162_495, path) +
				",scale=1080:1920,setsar=1"
			runFFmpeg(t, "-ss", secondsString(162_495), "-i", video,
				"-t", secondsString(187_410-162_495), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
				filepath.Join(outputDir, "production-storyboard-reclining-reel.mp4"))
		}
	})
}

// monikaDrunkProductionCadenceReelPath mirrors resolveSmartCropReelV2's
// storyboard-first execution path for the production five-second keyframes.
// The older exact-source regression skipped this branch and therefore missed
// a crop that stayed on the upright torso after the subject reclined.
func monikaDrunkProductionCadenceReelPath(t *testing.T, video string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
	positions := []int64{161_000, 166_000, 171_000, 176_000, 181_000, 186_000, 191_000}
	samples := monikaDrunkStoryboardSamples(t, video, positions, srcW, srcH)
	markSmartCropSceneCuts(samples)
	for _, sample := range samples {
		t.Logf("storyboard at=%d x=%d cut=%v", sample.point.AtMs, sample.point.X, sample.point.Cut)
	}
	target := smartCropTarget{StartMs: start, EndMs: end}
	if smartCropReelNeedsTracking(samples, srcW, cropW) || smartCropFaceTrackNeedsSourceSamples(samples, cropW) {
		trackingPositions := smartCropAdaptiveTrackingPositions(samples, target, duration, srcW, cropW)
		extra := monikaDrunkRemoteScriptSamples(t, video,
			trackingPositions,
			srcW, srcH)
		samples = mergeSmartCropSamples(samples, extra)
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, srcW, cropW)
		correctSmartCropIsolatedMotionBoundaryScenes(samples, srcW, cropW)
		fillSmartCropMotionGaps(samples, srcW, cropW)
		correctSmartCropStationaryRuns(samples, srcW, cropW)
		refineSmartCropHeadSamples(samples, srcW, cropW)
	}
	correctSmartCropUnanchoredEdgeFurnitureScenes(samples, srcW, cropW)
	correctSmartCropReelTemporalOutliers(samples, srcW, cropW)
	refineSmartCropHeadSamples(samples, srcW, cropW)
	correctSmartCropFaceTracks(samples, srcW, cropW)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	path = stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), cropW, srcW)
	return constrainSmartCropPathToFaceTracks(path, samples, srcW, cropW)
}

func monikaDrunkRemoteScriptSamples(t *testing.T, video string, positions []int64, srcW, srcH int) []smartCropV2Sample {
	t.Helper()
	if fixtureDir := os.Getenv("MONIKA_DRUNK_11788_REMOTE_SAMPLES_DIR"); fixtureDir != "" {
		samples := make([]smartCropV2Sample, 0, len(positions))
		for _, position := range uniqueSortedSmartCropPositions(append([]int64(nil), positions...)) {
			frame := filepath.Join(fixtureDir, fmt.Sprintf("%d.jpg", position))
			f, err := os.Open(frame)
			if os.IsNotExist(err) {
				frame = filepath.Join(fixtureDir, fmt.Sprintf("%d.png", position))
				f, err = os.Open(frame)
				if os.IsNotExist(err) {
					continue
				}
			}
			if err != nil {
				t.Fatal(err)
			}
			img, _, err := image.Decode(f)
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
			win, err := analyzeSmartCropV2Frame(srcW, srcH, 9, 16, img)
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("remote fixture at=%d x=%d", position, win.X)
			samples = append(samples, smartCropV2Sample{
				point: cropPathPoint{AtMs: position, X: win.X},
				img:   img,
			})
		}
		return samples
	}
	script, err := buildRemoteSmartCropSampleScript("ffmpeg", video, positions, 1280)
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.CommandContext(t.Context(), "bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("remote sample script emulation: %v: %s", err, out)
	}
	t.Logf("remote sample payload bytes=%d", len(out))
	samples, err := parseRemoteSmartCropSamples(string(out), positions, srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	return samples
}

func monikaDrunkStoryboardSamples(t *testing.T, video string, positions []int64, srcW, srcH int) []smartCropV2Sample {
	t.Helper()
	dir := t.TempDir()
	fixtureDir := os.Getenv("MONIKA_DRUNK_11788_STORYBOARD_DIR")
	samples := make([]smartCropV2Sample, 0, len(positions))
	for i, position := range positions {
		frame := filepath.Join(dir, fmt.Sprintf("%02d.jpg", i))
		if fixtureDir != "" {
			frame = filepath.Join(fixtureDir, fmt.Sprintf("%d.jpg", position))
		} else {
			runFFmpeg(t, "-ss", secondsString(position), "-i", video,
				"-vf", "thumbnail=30,scale=320:-2", "-frames:v", "1", "-q:v", "3", "-y", frame)
		}
		f, err := os.Open(frame)
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(f)
		f.Close()
		if err != nil {
			t.Fatal(err)
		}
		win, err := analyzeSmartCropV2Frame(srcW, srcH, 9, 16, img)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: position, X: win.X},
			img:   img,
		})
	}
	return samples
}

func monikaDrunkStillX(t *testing.T, video string, duration, focus int64) int {
	t.Helper()
	return localSmartCropStillX(t, video, duration, focus, 1280, 720)
}

func localSmartCropStillX(t *testing.T, video string, duration, focus int64, srcW, srcH int) int {
	t.Helper()
	cropW, _ := cropDimsForRatio(srcW, srcH, 9, 16)
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		monikaContainmentNearestPositions(focus, duration, 9), srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	promoteSmartCropDetailedFaces(samples, cropW, true)
	for _, sample := range samples {
		face := "none"
		if sample.face != nil {
			face = fmt.Sprintf("x=%d q=%.1f", sample.face.CenterX, sample.face.Quality)
		}
		t.Logf("storyboard at=%d crop=%d face=%s", sample.point.AtMs, sample.point.X, face)
	}
	contextSamples := smartCropSceneSamples(samples, focus)
	contextResult, contextOK := bestSmartCropTemporalConsensus(contextSamples, srcW, cropW)
	tracking := false
	if smartCropStillNeedsTracking(samples, focus, cropW) || smartCropFaceTrackNeedsSourceSamples(contextSamples, cropW) {
		samples, err = analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropStillTrackingPositions(focus, duration), srcW, srcH, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		promoteSmartCropDetailedFaces(samples, cropW, true)
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, srcW, cropW)
		refineSmartCropHeadSamples(samples, srcW, cropW)
		tracking = true
		for _, sample := range samples {
			face := "none"
			if sample.face != nil {
				face = fmt.Sprintf("x=%d q=%.1f", sample.face.CenterX, sample.face.Quality)
			}
			t.Logf("tracking at=%d crop=%d motion=%v face=%s", sample.point.AtMs, sample.point.X, sample.motionTracked, face)
		}
	}
	x, _, err := resolveSmartCropStillBase(samples, focus)
	if err != nil {
		t.Fatal(err)
	}
	trackingConsensusApplied := false
	if tracking && contextOK && smartCropTemporalResultConfident(contextResult) {
		if corrected, changed := stabilizeSmartCropStillTrackingX(x, samples, contextResult, cropW, srcW); changed {
			x = corrected
			trackingConsensusApplied = true
		}
	}
	if !tracking {
		if contextOK {
			if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
				x = corrected
			}
		}
	} else if !trackingConsensusApplied && contextOK && smartCropTemporalResultConfident(contextResult) &&
		(contextResult.StaticAnchored || !smartCropStillHasMotionEvidence(samples, focus)) {
		if corrected, changed := applySmartCropTemporalOverride(x, contextResult, cropW, srcW); changed {
			x = corrected
		}
	} else if contextOK && smartCropTemporalResultConfident(contextResult) {
		x = clampInt(roundEven(clampInt(x, contextResult.X-cropW/2, contextResult.X+cropW/2)), 0, srcW-cropW)
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil {
		thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(sample.img.Bounds().Dx())/float64(srcW))), 1, sample.img.Bounds().Dx())
		if corrected, changed := silhouetteAwareNarrowSmartCropX(sample.img, x, srcW, cropW, thumbCropW); changed {
			x = corrected
		}
		if corrected, changed := headAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
			x = corrected
		}
		if corrected, changed := recliningSubjectAwareNarrowSmartCropX(sample.img, x, srcW, cropW); changed {
			x = corrected
		}
	}
	if tracking {
		if handoffX, changed := stabilizeSmartCropStillMotionHandoff(x, samples, contextSamples, focus, cropW, srcW); changed {
			x = handoffX
		}
	}
	faceTrackX, faceTrackOK := smartCropFaceTrackXAt(contextSamples, focus, cropW, srcW)
	if faceTrackOK &&
		(!tracking || !smartCropStillHasMotionEvidence(samples, focus)) &&
		absInt(x-faceTrackX) > maxInt(20, cropW/12) {
		x = faceTrackX
	}
	if furnitureX, ok := smartCropUnanchoredEdgeFurnitureFallbackX(contextSamples, srcW, cropW); ok {
		x = furnitureX
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil && sample.face == nil {
		backgroundImages := localSmartCropBackgroundImages(t, video, duration, srcW, srcH)
		if backgroundX, _, ok := backgroundAwareNarrowSmartCropX(sample.img, backgroundImages, x, srcW, cropW); ok {
			motionHandoff := tracking && smartCropStillHasMotionEvidence(samples, focus)
			if !faceTrackOK || motionHandoff || absInt(backgroundX-faceTrackX) < absInt(x-faceTrackX) {
				x = backgroundX
			}
		}
	}
	if sample := nearestSmartCropSample(samples, focus); sample != nil && sample.face != nil {
		x = containSmartCropFaceX(x, *sample.face, srcW, cropW)
	}
	return clampInt(roundEven(x), 0, srcW-cropW)
}

func monikaDrunkReelPath(t *testing.T, video string, duration, start, end int64) []cropPathPoint {
	t.Helper()
	return localSmartCropReelPath(t, video, duration, start, end, 1280, 720)
}

func localSmartCropReelPath(t *testing.T, video string, duration, start, end int64, srcW, srcH int) []cropPathPoint {
	t.Helper()
	cropW, _ := cropDimsForRatio(srcW, srcH, 9, 16)
	positions := monikaContainmentRangePositions(start, end, duration)
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		positions, srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	backgroundImages := localSmartCropBackgroundImages(t, video, duration, srcW, srcH)
	correctSmartCropBackgroundSamples(samples, backgroundImages, srcW, cropW)
	correctSmartCropUnanchoredEdgeFurnitureScenes(samples, srcW, cropW)
	target := smartCropTarget{StartMs: start, EndMs: end}
	postTrackingNeeded := smartCropReelNeedsTracking(samples, srcW, cropW)
	postFaceTrackingNeeded := smartCropFaceTrackNeedsSourceSamples(samples, cropW)
	lateTrackingNeeded := smartCropLateRefinementNeedsSourceSamples(samples, srcW, cropW)
	if postTrackingNeeded || postFaceTrackingNeeded || lateTrackingNeeded {
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropAdaptiveTrackingPositions(samples, target, duration, srcW, cropW),
			srcW, srcH, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		samples = mergeSmartCropSamples(samples, extra)
		markSmartCropSceneCuts(samples)
		correctSmartCropBackgroundSamples(samples, backgroundImages, srcW, cropW)
		refineSmartCropMotionSamples(samples, srcW, cropW)
		correctSmartCropIsolatedMotionBoundaryScenes(samples, srcW, cropW)
		fillSmartCropMotionGaps(samples, srcW, cropW)
		correctSmartCropStationaryRuns(samples, srcW, cropW)
		refineSmartCropHeadSamples(samples, srcW, cropW)
	}
	correctSmartCropReelTemporalOutliers(samples, srcW, cropW)
	correctSmartCropStationarySubjectTails(samples, srcW, cropW)
	refineSmartCropHeadSamples(samples, srcW, cropW)
	correctSmartCropHeadTracks(samples, srcW, cropW)
	promoteSmartCropDetailedFaces(samples, cropW, false)
	filterSmartCropWeakFaceAnchors(samples, cropW)
	filterSmartCropWeakFaceDirectionClusters(samples, srcW, cropW)
	filterSmartCropIsolatedFaceExcursions(samples, cropW)
	correctSmartCropWeakFaceExcursions(samples, srcW, cropW)
	correctSmartCropFaceTracks(samples, srcW, cropW)
	correctSmartCropBackgroundEdgeDeparturesFromFaces(samples, srcW, cropW)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	path = stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), cropW, srcW)
	return constrainSmartCropPathToFaceTracks(path, samples, srcW, cropW)
}

func localSmartCropBackgroundImages(t *testing.T, video string, duration int64, srcW, srcH int) []image.Image {
	t.Helper()
	positions := make([]int64, 0, 12)
	for i := 0; i < 12; i++ {
		positions = append(positions, 1_000+int64(i)*(duration-2_000)/11)
	}
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video, positions, srcW, srcH, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	images := make([]image.Image, 0, len(samples))
	for _, sample := range samples {
		images = append(images, sample.img)
	}
	return images
}

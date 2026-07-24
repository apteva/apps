package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"testing"
)

const (
	aleksandraPart1ClipSHA256 = "db36fc1250dee733b0729fec7e150e0b80dca02793f6819923aafc9dd2947516"
	aleksandraPart2ClipSHA256 = "bf5d01d443f9100927c7cf589e3ebe7d12628189dc16d6294fc7a751f22308d7"
)

// TestAleksandraLocalRegression covers the exact production pixels and
// storyboard cadence that exposed two close-up profile/head-drop failures.
// Compact clips and the selected production storyboard frames remain external
// because the original sources are 353-430 MB and private.
func TestAleksandraLocalRegression(t *testing.T) {
	root := os.Getenv("ALEKSANDRA_FIXTURE_DIR")
	if root == "" {
		t.Skip("set ALEKSANDRA_FIXTURE_DIR to run the real-media regression")
	}
	outputDir := os.Getenv("ALEKSANDRA_OUTPUT_DIR")
	if outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	fixtures := []struct {
		name       string
		sourceID   string
		video      string
		sha256     string
		offset     int64
		duration   int64
		start, end int64
		storyboard []int64
		checks     []struct {
			at, minX, maxX int64
		}
	}{
		{
			name:     "part-1-sleep-trigger-profile",
			sourceID: "24715",
			video:    "part_1_947000_1005000.mp4",
			sha256:   aleksandraPart1ClipSHA256,
			offset:   947_000,
			duration: 1_307_866,
			start:    955_175,
			end:      997_300,
			storyboard: []int64{
				945_452, 956_434, 967_416, 978_398, 989_380, 1_000_362,
			},
			checks: []struct{ at, minX, maxX int64 }{
				{955_175, 760, 840},
				{965_175, 760, 840},
				{975_175, 760, 840},
				{985_175, 760, 840},
				{995_175, 760, 840},
			},
		},
		{
			name:     "part-2-countdown-head-drop",
			sourceID: "24940",
			video:    "part_2_294000_349000.mp4",
			sha256:   aleksandraPart2ClipSHA256,
			offset:   294_000,
			duration: 1_074_233,
			start:    301_875,
			end:      341_550,
			storyboard: []int64{
				298_627, 307_646, 316_665, 325_684, 334_703, 343_722,
			},
			checks: []struct{ at, minX, maxX int64 }{
				{301_875, 650, 800},
				{311_875, 540, 760},
				{321_875, 700, 820},
				{331_875, 700, 820},
				{341_000, 700, 820},
			},
		},
	}

	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			video := filepath.Join(root, "sources", fixture.video)
			assertFileSHA256(t, video, fixture.sha256)
			path := aleksandraProductionReelPath(t, video, fixture.offset, fixture.duration,
				fixture.start, fixture.end,
				filepath.Join(root, "storyboards", fixture.sourceID),
				filepath.Join(root, "backgrounds", fixture.sourceID),
				fixture.storyboard)
			t.Logf("crop path: %v", path)
			for _, check := range fixture.checks {
				x, ok := mariaInterpolatedPathX(path, check.at)
				if !ok {
					t.Errorf("no crop path at %dms: %v", check.at, path)
					continue
				}
				if int64(x) < check.minX || int64(x) > check.maxX {
					t.Errorf("subject lost at %dms: crop x=%d want [%d,%d]; path=%v",
						check.at, x, check.minX, check.maxX, path)
				}
			}
			if outputDir != "" {
				filter := "setpts=PTS-STARTPTS," + cropFilterForPath(404, 718, 0, fixture.start, path) +
					",scale=1080:1920,setsar=1"
				runFFmpeg(t, "-ss", secondsString(fixture.start-fixture.offset), "-i", video,
					"-t", secondsString(fixture.end-fixture.start), "-vf", filter, "-an",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
					filepath.Join(outputDir, fixture.name+".mp4"))
			}
		})
	}
}

func aleksandraProductionReelPath(
	t *testing.T,
	video string,
	offset, duration, start, end int64,
	storyboardDir, backgroundDir string,
	storyboardPositions []int64,
) []cropPathPoint {
	t.Helper()
	const srcW, srcH, cropW = 1280, 720, 404
	samples := aleksandraFixtureSamples(t, storyboardDir, storyboardPositions, srcW, srcH)
	backgroundImages := aleksandraFixtureImages(t, backgroundDir)
	markSmartCropSceneCuts(samples)
	trackingNeeded := smartCropReelNeedsTracking(samples, srcW, cropW) ||
		smartCropFaceTrackNeedsSourceSamples(samples, cropW)
	correctSmartCropBackgroundSamples(samples, backgroundImages, srcW, cropW)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if trackingNeeded || smartCropReelNeedsTracking(samples, srcW, cropW) ||
		smartCropFaceTrackNeedsSourceSamples(samples, cropW) ||
		smartCropLateRefinementNeedsSourceSamples(samples, srcW, cropW) {
		positions := smartCropAdaptiveTrackingPositions(samples, target, duration, srcW, cropW)
		relative := make([]int64, len(positions))
		for i, position := range positions {
			relative[i] = position - offset
		}
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			relative, srcW, srcH, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		for i := range extra {
			extra[i].point.AtMs += offset
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
	correctSmartCropFaceTracks(samples, srcW, cropW)
	for _, sample := range samples {
		face := "none"
		if sample.face != nil {
			face = fmt.Sprintf("center=%d q=%.1f", sample.face.CenterX, sample.face.Quality)
		}
		t.Logf("sample at=%d x=%d cut=%v motion=%v head=%v background=%v face=%s",
			sample.point.AtMs, sample.point.X, sample.point.Cut,
			sample.motionTracked, sample.headTracked, sample.backgroundTracked, face)
	}
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	path = stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), cropW, srcW)
	return constrainSmartCropPathToFaceTracks(path, samples, srcW, cropW)
}

func aleksandraFixtureSamples(t *testing.T, dir string, positions []int64, srcW, srcH int) []smartCropV2Sample {
	t.Helper()
	samples := make([]smartCropV2Sample, 0, len(positions))
	for _, position := range positions {
		img := decodeAleksandraFixtureImage(t, filepath.Join(dir, fmt.Sprintf("%d.jpg", position)))
		win, face, err := analyzeSmartCropV2FrameDetailed(srcW, srcH, 9, 16, img)
		if err != nil {
			t.Fatal(err)
		}
		samples = append(samples, smartCropV2Sample{
			point: cropPathPoint{AtMs: position, X: win.X},
			img:   img,
			face:  face,
		})
	}
	return samples
}

func aleksandraFixtureImages(t *testing.T, dir string) []image.Image {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	images := make([]image.Image, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		images = append(images, decodeAleksandraFixtureImage(t, filepath.Join(dir, entry.Name())))
	}
	return images
}

func decodeAleksandraFixtureImage(t *testing.T, path string) image.Image {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	img, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

package main

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
)

const monika9215SHA256 = "e3b1aff290bb6160bf4d0479feb1a5115d963b2c69a16b78e99333b2139021eb"

// TestMonika9215LocalRegression is an opt-in real-media regression for the
// production source that exposed scene-wide consensus flattening real subject
// traversal. CI remains self-contained; operators can retain the exact source
// locally and run:
//
//	MONIKA_9215_VIDEO=/path/to/zombie.mp4 go test -run TestMonika9215LocalRegression -v
//
// MONIKA_9215_OUTPUT_DIR additionally renders two stills and two reel previews
// using the computed crop path for visual review.
func TestMonika9215LocalRegression(t *testing.T) {
	video := os.Getenv("MONIKA_9215_VIDEO")
	if video == "" {
		t.Skip("set MONIKA_9215_VIDEO to run the real-media regression")
	}
	assertFileSHA256(t, video, monika9215SHA256)

	stillXs := make(map[int64]int)
	for _, focus := range []int64{160000, 195000} {
		samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropStillTrackingPositions(focus, 298200), 1920, 1080, 606, 1076)
		if err != nil {
			t.Fatal(err)
		}
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, 1920, 606)
		x, _, err := resolveSmartCropStillBase(samples, focus)
		if err != nil {
			t.Fatal(err)
		}
		stillXs[focus] = x
	}
	if stillXs[160000] >= 800 {
		t.Fatalf("160s crop did not follow subject left: x=%d", stillXs[160000])
	}
	if stillXs[195000] < 1200 {
		t.Fatalf("195s crop did not follow subject right: x=%d", stillXs[195000])
	}

	type reelCase struct {
		name                string
		start, end          int64
		storyboardPositions []int64
		wantMinBelow        int
		wantMaxAbove        int
	}
	reels := []reelCase{
		{"first-zombie-response", 151145, 175599, []int64{151000, 156000, 161000, 166000, 171000, 176000}, 650, 1100},
		{"zombie-loop-release", 184215, 218745, []int64{181000, 186000, 191000, 196000, 201000, 206000, 211000, 216000, 221000}, 900, 1250},
	}
	paths := make(map[string][]cropPathPoint)
	for _, reel := range reels {
		path := monika9215TrackedPath(t, video, reel.start, reel.end, reel.storyboardPositions)
		minX, maxX := path[0].X, path[0].X
		for _, point := range path[1:] {
			minX = minInt(minX, point.X)
			maxX = maxInt(maxX, point.X)
		}
		if minX >= reel.wantMinBelow || maxX <= reel.wantMaxAbove {
			t.Fatalf("%s path did not retain traversal: min=%d max=%d path=%v", reel.name, minX, maxX, path)
		}
		paths[reel.name] = path
	}
	stableName := "sleep-countdown-stable"
	stableStart, stableEnd := int64(40274), int64(72725)
	stablePath := monika9215StablePath(t, video, stableStart, stableEnd,
		[]int64{36_000, 41_000, 46_000, 51_000, 56_000, 61_000, 66_000, 71_000, 76_000})
	paths[stableName] = stablePath

	if outputDir := os.Getenv("MONIKA_9215_OUTPUT_DIR"); outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for focus, x := range stillXs {
			out := filepath.Join(outputDir, fmt.Sprintf("monika-%d-smart-tracking.png", focus))
			runFFmpeg(t, "-ss", secondsString(focus), "-i", video, "-frames:v", "1",
				"-vf", fmt.Sprintf("crop=606:1076:%d:2", x), "-y", out)
		}
		for _, reel := range reels {
			out := filepath.Join(outputDir, reel.name+"-smart-tracking.mp4")
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1076, 2, reel.start, paths[reel.name]) + ",scale=606:1076,setsar=1"
			runFFmpeg(t, "-ss", secondsString(reel.start), "-i", video,
				"-t", secondsString(reel.end-reel.start), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y", out)
		}
		out := filepath.Join(outputDir, stableName+"-smart-tracking.mp4")
		filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1076, 2, stableStart, stablePath) + ",scale=606:1076,setsar=1"
		runFFmpeg(t, "-ss", secondsString(stableStart), "-i", video,
			"-t", secondsString(stableEnd-stableStart), "-vf", filter, "-an",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y", out)
	}
}

// TestMonika9215ProductionSelectionsLocalRegression exercises the exact
// timestamps and ranges selected by the production agent on 2026-07-17. Keep
// this separate from the historical traversal fixture above so a future crop
// change cannot silently fix one sequence while regressing another.
func TestMonika9215ProductionSelectionsLocalRegression(t *testing.T) {
	video := os.Getenv("MONIKA_9215_VIDEO")
	if video == "" {
		t.Skip("set MONIKA_9215_VIDEO to run the real-media regression")
	}
	assertFileSHA256(t, video, monika9215SHA256)

	stillPositions := []int64{65_690, 95_065, 158_825, 193_170, 241_084}
	stillRanges := map[int64][2]int{
		65_690: {680, 850}, 95_065: {650, 800}, 158_825: {650, 820},
		193_170: {1_050, 1_320}, 241_084: {450, 650},
	}
	stillXs := make(map[int64]int, len(stillPositions))
	for _, focus := range stillPositions {
		x := monika9215ProductionStillX(t, video, focus)
		stillXs[focus] = x
		t.Logf("production still focus=%d crop_x=%d", focus, x)
		want := stillRanges[focus]
		if x < want[0] || x > want[1] {
			t.Fatalf("production still focus=%d crop_x=%d outside [%d,%d]", focus, x, want[0], want[1])
		}
	}

	reels := []struct {
		name       string
		start, end int64
	}{
		{"first-zombie-trigger-response", 151_145, 183_495},
		{"extended-zombie-loop-and-release", 184_215, 224_110},
		{"final-deepening-and-wake", 237_564, 272_435},
	}
	paths := make(map[string][]cropPathPoint, len(reels))
	for _, reel := range reels {
		positions := monika9215StoryboardPositions(reel.start, reel.end)
		path := monika9215TrackedPath(t, video, reel.start, reel.end, positions)
		if len(path) < 2 {
			t.Fatalf("%s: empty crop path", reel.name)
		}
		paths[reel.name] = path
		t.Logf("production reel %s path=%v", reel.name, path)
	}
	if x := monika9215PathXAt(paths["final-deepening-and-wake"], 241_084); x > 700 {
		t.Fatalf("final reel did not protect reclining face at 241084ms: x=%d path=%v", x, paths["final-deepening-and-wake"])
	}

	if outputDir := os.Getenv("MONIKA_9215_OUTPUT_DIR"); outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, focus := range stillPositions {
			out := filepath.Join(outputDir, fmt.Sprintf("production-still-%d.png", focus))
			runFFmpeg(t, "-ss", secondsString(focus), "-i", video, "-frames:v", "1",
				"-vf", fmt.Sprintf("crop=606:1076:%d:2", stillXs[focus]), "-y", out)
		}
		for _, reel := range reels {
			out := filepath.Join(outputDir, reel.name+".mp4")
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1076, 2, reel.start, paths[reel.name]) + ",scale=606:1076,setsar=1"
			runFFmpeg(t, "-ss", secondsString(reel.start), "-i", video,
				"-t", secondsString(reel.end-reel.start), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y", out)
		}
	}
}

// TestMonika9215LatestProductionSelectionsLocalRegression covers the second
// production extraction performed after Media 0.13.68 was installed. These
// are the exact agent-selected frames and reel ranges, not hand-picked proxy
// timestamps.
func TestMonika9215LatestProductionSelectionsLocalRegression(t *testing.T) {
	video := os.Getenv("MONIKA_9215_VIDEO")
	if video == "" {
		t.Skip("set MONIKA_9215_VIDEO to run the real-media regression")
	}
	assertFileSHA256(t, video, monika9215SHA256)

	stillPositions := []int64{67_690, 160_000, 177_575, 194_209, 237_564}
	stillRanges := map[int64][2]int{
		67_690: {680, 820}, 160_000: {300, 620}, 177_575: {740, 900},
		194_209: {1_240, 1_314}, 237_564: {700, 880},
	}
	stillXs := make(map[int64]int, len(stillPositions))
	for _, focus := range stillPositions {
		x := monika9215ProductionStillX(t, video, focus)
		stillXs[focus] = x
		want := stillRanges[focus]
		if x < want[0] || x > want[1] {
			t.Fatalf("latest production still focus=%d crop_x=%d outside [%d,%d]", focus, x, want[0], want[1])
		}
		t.Logf("latest production still focus=%d crop_x=%d", focus, x)
	}

	reels := []struct {
		name       string
		start, end int64
	}{
		{"relaxation-countdown-into-sleep", 40_274, 76_965},
		{"first-trigger-response-and-confusion", 151_145, 182_055},
		{"sustained-zombie-trigger-and-release", 184_215, 218_745},
	}
	paths := make(map[string][]cropPathPoint, len(reels))
	for _, reel := range reels {
		path := monika9215ProductionReelPath(t, video, reel.start, reel.end,
			monika9215StoryboardPositions(reel.start, reel.end))
		if len(path) < 2 {
			t.Fatalf("%s: empty crop path", reel.name)
		}
		paths[reel.name] = path
		t.Logf("latest production reel %s path=%v", reel.name, path)
	}
	if x := monika9215PathXAt(paths["first-trigger-response-and-confusion"], 161_145); x > 700 {
		t.Fatalf("first trigger reel did not follow subject left at 161145ms: x=%d", x)
	}
	lastPath := paths["sustained-zombie-trigger-and-release"]
	for at, want := range map[int64][2]int{189_215: {600, 900}, 204_215: {700, 950}} {
		x := monika9215PathXAt(lastPath, at)
		if x < want[0] || x > want[1] {
			t.Fatalf("sustained trigger reel at %dms crop_x=%d outside [%d,%d]", at, x, want[0], want[1])
		}
	}

	if outputDir := os.Getenv("MONIKA_9215_OUTPUT_DIR"); outputDir != "" {
		if err := os.MkdirAll(outputDir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, focus := range stillPositions {
			out := filepath.Join(outputDir, fmt.Sprintf("latest-production-still-%d.png", focus))
			runFFmpeg(t, "-ss", secondsString(focus), "-i", video, "-frames:v", "1",
				"-vf", fmt.Sprintf("crop=606:1076:%d:2", stillXs[focus]), "-y", out)
		}
		for _, reel := range reels {
			out := filepath.Join(outputDir, "latest-"+reel.name+".mp4")
			filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1076, 2, reel.start, paths[reel.name]) + ",scale=606:1076,setsar=1"
			runFFmpeg(t, "-ss", secondsString(reel.start), "-i", video,
				"-t", secondsString(reel.end-reel.start), "-vf", filter, "-an",
				"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y", out)
		}
	}
}

func monika9215ProductionStillX(t *testing.T, video string, focus int64) int {
	t.Helper()
	positions := monika9215NearestStoryboardPositions(focus, 9)
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		positions, 1920, 1080, 9, 16)
	if err != nil {
		t.Fatal(err)
	}
	cropW, _ := cropDimsForRatio(1920, 1080, 9, 16)
	tracking := false
	if smartCropStillNeedsTracking(samples, focus, cropW) {
		samples, err = analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			smartCropStillTrackingPositions(focus, 298_200), 1920, 1080, 9, 16)
		if err != nil {
			t.Fatal(err)
		}
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, 1920, cropW)
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

func monika9215PathXAt(path []cropPathPoint, atMs int64) int {
	if len(path) == 0 {
		return 0
	}
	for i := 0; i+1 < len(path); i++ {
		if atMs <= path[i+1].AtMs {
			return interpolateSmartCropStillX(path[i], path[i+1], atMs)
		}
	}
	return path[len(path)-1].X
}

func monika9215NearestStoryboardPositions(focus int64, limit int) []int64 {
	positions := make([]int64, 0, 60)
	for position := int64(1_000); position <= 296_000; position += 5_000 {
		positions = append(positions, position)
	}
	sort.Slice(positions, func(i, j int) bool {
		di := absInt64(positions[i] - focus)
		dj := absInt64(positions[j] - focus)
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

func monika9215StoryboardPositions(start, end int64) []int64 {
	positions := make([]int64, 0)
	for position := int64(1_000); position <= 296_000; position += 5_000 {
		if position >= start-5_000 && position <= end+5_000 {
			positions = append(positions, position)
		}
	}
	return positions
}

func monika9215TrackedPath(t *testing.T, video string, start, end int64, storyboardPositions []int64) []cropPathPoint {
	t.Helper()
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		storyboardPositions, 1920, 1080, 606, 1076)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if !smartCropReelNeedsTracking(samples, 1920, 606) {
		t.Fatal("fixture traversal did not activate adaptive tracking")
	}
	positions := smartCropAdaptiveTrackingPositions(samples, target, 298200, 1920, 606)
	extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		positions, 1920, 1080, 606, 1076)
	if err != nil {
		t.Fatal(err)
	}
	samples = mergeSmartCropSamples(samples, extra)
	markSmartCropSceneCuts(samples)
	refineSmartCropMotionSamples(samples, 1920, 606)
	correctSmartCropReelTemporalOutliers(samples, 1920, 606)
	refineSmartCropHeadSamples(samples, 1920, 606)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	return stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), 606, 1920)
}

func monika9215ProductionReelPath(t *testing.T, video string, start, end int64, storyboardPositions []int64) []cropPathPoint {
	t.Helper()
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		storyboardPositions, 1920, 1080, 606, 1076)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	target := smartCropTarget{StartMs: start, EndMs: end}
	if smartCropReelNeedsTracking(samples, 1920, 606) {
		positions := smartCropAdaptiveTrackingPositions(samples, target, 298_200, 1920, 606)
		extra, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
			positions, 1920, 1080, 606, 1076)
		if err != nil {
			t.Fatal(err)
		}
		samples = mergeSmartCropSamples(samples, extra)
		markSmartCropSceneCuts(samples)
		refineSmartCropMotionSamples(samples, 1920, 606)
	}
	correctSmartCropReelTemporalOutliers(samples, 1920, 606)
	refineSmartCropHeadSamples(samples, 1920, 606)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	path = stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), 606, 1920)
	if x, ok := staticSmartCropPathX(path, 606); ok {
		return []cropPathPoint{{AtMs: start, X: x}, {AtMs: end, X: x}}
	}
	return path
}

func monika9215StablePath(t *testing.T, video string, start, end int64, storyboardPositions []int64) []cropPathPoint {
	t.Helper()
	samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
		storyboardPositions, 1920, 1080, 606, 1076)
	if err != nil {
		t.Fatal(err)
	}
	markSmartCropSceneCuts(samples)
	if smartCropReelNeedsTracking(samples, 1920, 606) {
		t.Fatal("stable fixture unexpectedly activated adaptive source tracking")
	}
	correctSmartCropReelTemporalOutliers(samples, 1920, 606)
	path := make([]cropPathPoint, len(samples))
	for i := range samples {
		path[i] = samples[i].point
	}
	path = stabilizeSmartCropPath(anchorSmartCropPath(path, start, end), 606, 1920)
	x, ok := staticSmartCropPathX(path, 606)
	if !ok {
		t.Fatalf("stable fixture produced a moving crop: %v", path)
	}
	return []cropPathPoint{{AtMs: start, X: x}, {AtMs: end, X: x}}
}

func assertFileSHA256(t *testing.T, path, want string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprintf("%x", h.Sum(nil)); got != want {
		t.Fatalf("fixture SHA-256=%s want=%s", got, want)
	}
}

func secondsString(ms int64) string {
	return fmt.Sprintf("%.3f", float64(ms)/1000.0)
}

func runFFmpeg(t *testing.T, args ...string) {
	t.Helper()
	cmd := exec.Command("ffmpeg", append([]string{"-hide_banner", "-loglevel", "error"}, args...)...)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v: %s", err, output)
	}
}

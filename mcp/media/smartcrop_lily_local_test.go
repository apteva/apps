package main

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type lilySmartCropSource struct {
	file     string
	sha256   string
	duration int64
	width    int
	height   int
}

var lilySmartCropSources = map[int]lilySmartCropSource{
	50948: {"50948-clothes_surprise.mp4", "ea5c3062598f18142714ddcdfc133ff6b682fe58e1f3571af8c8a15b029cbabf", 489_167, 3840, 2160},
	51702: {"51702-touching_tights.mp4", "95d947c7a78c2b7d25ad6b3e1cf229f860631298bfdbc92a1c87e7c1873a1194", 326_167, 3840, 2160},
	51817: {"51817-zombie_new.mp4", "b9b883cebb49b8ef320afb00eafcbf6495eab05e4fcfce1407827c3201adc892", 312_024, 3840, 2160},
	69497: {"69497-eye_roll.mp4", "6593f91749e90648dc3fbc1602628e6a61fae4b02b1b481c0ff509257da4a4ea", 436_753, 3840, 2160},
	70446: {"70446-kinky.mp4", "1880d472eee28f92bdc5f7f7e5a28ee1493157d9d5b3103c7b76732c24a4800d", 1_383_482, 3840, 2160},
}

// TestLilySourceRegression covers the production-identical sources that
// exposed static furniture selection, unbalanced motion windows, false large
// face votes and small profile-face clipping. The media remains private and
// external; exact hashes prevent a replacement file from weakening the gate.
func TestLilySourceRegression(t *testing.T) {
	root := os.Getenv("LILY_SOURCE_DIR")
	if root == "" {
		t.Skip("set LILY_SOURCE_DIR to run the production-identical Lily regression")
	}
	output := os.Getenv("LILY_OUTPUT_DIR")
	for _, source := range lilySmartCropSources {
		assertFileSHA256(t, filepath.Join(root, source.file), source.sha256)
	}
	video := func(id int) (string, lilySmartCropSource) {
		source := lilySmartCropSources[id]
		return filepath.Join(root, source.file), source
	}

	stills := []struct {
		id, source int
		at         int64
		minX, maxX int
	}{
		{51777, 51702, 69_000, 1000, 1700},
		{51781, 51702, 80_000, 1000, 1700},
		{69645, 69497, 381_300, 0, 1000},
	}
	for _, fixture := range stills {
		fixture := fixture
		t.Run(fmt.Sprintf("still-%d", fixture.id), func(t *testing.T) {
			path, source := video(fixture.source)
			x := localSmartCropStillX(t, path, source.duration, fixture.at, source.width, source.height)
			t.Logf("crop x=%d", x)
			if x < fixture.minX || x > fixture.maxX {
				t.Fatalf("crop x=%d want [%d,%d]", x, fixture.minX, fixture.maxX)
			}
			if output != "" {
				if err := os.MkdirAll(output, 0o755); err != nil {
					t.Fatal(err)
				}
				cropW, cropH := cropDimsForRatio(source.width, source.height, 9, 16)
				runFFmpeg(t, "-ss", secondsString(fixture.at), "-i", path, "-frames:v", "1",
					"-vf", fmt.Sprintf("crop=%d:%d:%d:0,scale=540:960", cropW, cropH, x),
					"-y", filepath.Join(output, fmt.Sprintf("%d-new.png", fixture.id)))
			}
		})
	}

	reels := []struct {
		id, source int
		start, end int64
		minX, maxX int
	}{
		{51069, 50948, 54_915, 86_329, 700, 1900},
		{51800, 51702, 72_645, 103_335, 1000, 1700},
		{51809, 51702, 132_035, 154_630, 1000, 1700},
		{69658, 69497, 198_330, 231_810, 0, 1100},
		{70751, 70446, 387_720, 420_060, 0, 300},
	}
	for _, fixture := range reels {
		fixture := fixture
		t.Run(fmt.Sprintf("reel-%d", fixture.id), func(t *testing.T) {
			path, source := video(fixture.source)
			cropPath := localSmartCropReelPath(t, path, source.duration, fixture.start, fixture.end,
				source.width, source.height)
			t.Logf("crop path: %v", cropPath)
			for _, point := range cropPath {
				if point.AtMs < fixture.start || point.AtMs > fixture.end {
					continue
				}
				if point.X < fixture.minX || point.X > fixture.maxX {
					t.Fatalf("crop leaves subject at %dms: x=%d want [%d,%d]; path=%v",
						point.AtMs, point.X, fixture.minX, fixture.maxX, cropPath)
				}
			}
			renderLilyRegressionReel(t, output, fixture.id, path, source, fixture.start, fixture.end, cropPath)
		})
	}

	t.Run("reel-51912", func(t *testing.T) {
		const start, end = int64(198_730), int64(233_480)
		path, source := video(51817)
		cropPath := localSmartCropReelPath(t, path, source.duration, start, end, source.width, source.height)
		t.Logf("crop path: %v", cropPath)
		checks := []struct {
			at         int64
			minX, maxX int
		}{
			{200_000, 700, 1400},
			{213_000, 500, 1100},
			{215_000, 900, 1500},
			{218_000, 1400, 2200},
			{220_000, 1700, 2400},
			{224_000, 900, 1600},
			{228_000, 1400, 2100},
			{232_000, 1100, 1800},
		}
		for _, check := range checks {
			x, ok := mariaInterpolatedPathX(cropPath, check.at)
			if !ok || x < check.minX || x > check.maxX {
				t.Fatalf("crop x=%d ok=%v at %dms want [%d,%d]; path=%v",
					x, ok, check.at, check.minX, check.maxX, cropPath)
			}
		}
		renderLilyRegressionReel(t, output, 51912, path, source, start, end, cropPath)
	})
}

func renderLilyRegressionReel(
	t *testing.T,
	output string,
	id int,
	path string,
	source lilySmartCropSource,
	start, end int64,
	cropPath []cropPathPoint,
) {
	t.Helper()
	if output == "" {
		return
	}
	if err := os.MkdirAll(output, 0o755); err != nil {
		t.Fatal(err)
	}
	cropW, cropH := cropDimsForRatio(source.width, source.height, 9, 16)
	filter := "setpts=PTS-STARTPTS," + cropFilterForPath(cropW, cropH, 0, start, cropPath) +
		",scale=360:640,setsar=1"
	runFFmpeg(t, "-ss", secondsString(start), "-i", path, "-t", secondsString(end-start),
		"-vf", filter, "-an", "-c:v", "libx264", "-preset", "ultrafast", "-crf", "25",
		"-movflags", "+faststart", "-y", filepath.Join(output, fmt.Sprintf("%d-new.mp4", id)))
}

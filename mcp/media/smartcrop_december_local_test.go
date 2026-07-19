package main

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/nfnt/resize"
)

var monikaDecemberSources = map[string]struct {
	file     string
	sha256   string
	duration int64
}{
	"playing":     {"12695-playing_instruments.mp4", "e421dea5ed7d7c054f744c027eef775676dcef3a66ebed04ea77f58b0d3a5544", 487_700},
	"hypnoteased": {"12846-hypnoteased.mp4", "b2f3dcb21b9fc772edea6c0771c369d8fe29b2757cc9b196f8a98673acb3b3fa", 860_685},
	"resist":      {"13018-resist.mp4", "e3b0d499ee3a507c86ffe4e72c097359c6506d24f8fcb7ed0c24c27776b32c1c", 725_033},
	"puppy":       {"13193-puppy.mp4", "6b6f8da4091e527ff6a7277f11250edb933fd6f8ad18c32459c06386719d2fad", 1_011_507},
	"mindless":    {"13368-mindless.mp4", "008f281eab0dcbdb7a0481f1566cd56fa70ebb4606bc7916f5488469371847f8", 1_174_550},
	"slow-freeze": {"13541-slow_freeze.mp4", "c700e409c2e0332b48a100dc43c6727d9a1f7671248817197dac5f2eabf2797c", 1_410_107},
}

// TestMonikaDecemberFaceAudit is an opt-in diagnostic over the private,
// production-identical landscape frames. The release regression below uses a
// checked manifest; this audit intentionally logs every detector decision when
// tuning that manifest without copying private media into git.
func TestMonikaDecemberFaceAudit(t *testing.T) {
	root := os.Getenv("MONIKA_DECEMBER_FIXTURE_DIR")
	if root == "" {
		t.Skip("set MONIKA_DECEMBER_FIXTURE_DIR to run the private December audit")
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(strings.ToLower(entry.Name()), ".png") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := os.Open(filepath.Join(root, name))
		if err != nil {
			t.Fatal(err)
		}
		img, _, err := image.Decode(file)
		file.Close()
		if err != nil {
			t.Fatal(err)
		}
		srcW, srcH := img.Bounds().Dx(), img.Bounds().Dy()
		img = resize.Resize(320, 0, img, resize.Lanczos3)
		window, face, err := analyzeSmartCropV2FrameDetailed(srcW, srcH, 9, 16, img)
		if err != nil {
			t.Fatal(err)
		}
		if face == nil {
			t.Logf("%s crop_x=%d face=none", name, window.X)
			continue
		}
		t.Logf("%s crop_x=%d face_center=%d face=[%d,%d] scale=%d quality=%.2f",
			name, window.X, face.CenterX, face.MinX, face.MaxX, face.Scale, face.Quality)
	}
}

// TestMonikaDecemberSourceRegression executes the same source-frame cadence as
// production against all December failure archetypes. Sources stay private and
// external; exact hashes prevent a similarly named replacement from weakening
// the release gate.
func TestMonikaDecemberSourceRegression(t *testing.T) {
	root := os.Getenv("MONIKA_DECEMBER_SOURCE_DIR")
	if root == "" {
		t.Skip("set MONIKA_DECEMBER_SOURCE_DIR to run the production-identical December regression")
	}
	verified := make(map[string]bool)
	video := func(key string) (string, int64) {
		fixture := monikaDecemberSources[key]
		path := filepath.Join(root, fixture.file)
		if !verified[path] {
			assertFileSHA256(t, path, fixture.sha256)
			verified[path] = true
		}
		return path, fixture.duration
	}

	stills := []struct {
		name, source string
		at           int64
		minX, maxX   int
	}{
		{"playing-reclining-left", "playing", 221_800, 180, 400},
		{"hypnoteased-reclining-left", "hypnoteased", 510_000, 0, 320},
		{"resist-reclining-right", "resist", 436_680, 1100, 1314},
		{"mindless-raised-hand", "mindless", 472_445, 1080, 1314},
		{"mindless-upright", "mindless", 604_985, 650, 900},
		{"slow-freeze-torso", "slow-freeze", 747_000, 200, 450},
		{"slow-freeze-final", "slow-freeze", 920_000, 100, 450},
	}
	for _, fixture := range stills {
		fixture := fixture
		t.Run("still-"+fixture.name, func(t *testing.T) {
			path, duration := video(fixture.source)
			x := localSmartCropStillX(t, path, duration, fixture.at, 1920, 1080)
			t.Logf("crop x=%d", x)
			if x < fixture.minX || x > fixture.maxX {
				t.Fatalf("crop x=%d want [%d,%d]", x, fixture.minX, fixture.maxX)
			}
		})
	}

	reels := []struct {
		name, source string
		start, end   int64
		minX, maxX   int
	}{
		{"playing-guitar", "playing", 339_944, 369_080, 150, 850},
		{"hypnoteased-awake", "hypnoteased", 304_115, 337_090, 350, 750},
		{"hypnoteased-drop", "hypnoteased", 352_065, 381_815, 350, 750},
		{"hypnoteased-countdown", "hypnoteased", 419_950, 453_520, 350, 800},
		{"resist-first", "resist", 429_975, 461_085, 450, 1314},
		{"resist-second", "resist", 500_199, 542_065, 250, 900},
		{"puppy-roll", "puppy", 424_970, 458_230, 500, 1050},
		{"mindless-flop", "mindless", 805_610, 842_750, 500, 850},
		{"slow-freeze-torso", "slow-freeze", 714_630, 752_945, 150, 650},
	}
	for _, fixture := range reels {
		fixture := fixture
		t.Run("reel-"+fixture.name, func(t *testing.T) {
			path, duration := video(fixture.source)
			cropPath := localSmartCropReelPath(t, path, duration, fixture.start, fixture.end, 1920, 1080)
			t.Logf("crop path: %v", cropPath)
			if len(cropPath) < 2 {
				t.Fatalf("crop path has %d points", len(cropPath))
			}
			for _, point := range cropPath {
				if point.X < fixture.minX || point.X > fixture.maxX {
					t.Fatalf("crop leaves subject at %dms: x=%d want [%d,%d]; path=%v",
						point.AtMs, point.X, fixture.minX, fixture.maxX, cropPath)
				}
				if fixture.name == "resist-first" && point.AtMs >= 438_000 && point.AtMs <= 458_000 && point.X < 950 {
					t.Fatalf("reclined face leaves the right crop at %dms: x=%d; path=%v",
						point.AtMs, point.X, cropPath)
				}
				if fixture.name == "playing-guitar" && point.AtMs >= 365_000 && point.X > 500 {
					t.Fatalf("reclined face snaps back to the room at %dms: x=%d; path=%v",
						point.AtMs, point.X, cropPath)
				}
			}
			if outputDir := os.Getenv("MONIKA_DECEMBER_OUTPUT_DIR"); outputDir != "" {
				if err := os.MkdirAll(outputDir, 0o755); err != nil {
					t.Fatal(err)
				}
				filter := "setpts=PTS-STARTPTS," + cropFilterForPath(606, 1078, 0, fixture.start, cropPath) +
					",scale=1080:1920,setsar=1"
				runFFmpeg(t, "-ss", secondsString(fixture.start), "-i", path,
					"-t", secondsString(fixture.end-fixture.start), "-vf", filter, "-an",
					"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-movflags", "+faststart", "-y",
					filepath.Join(outputDir, fmt.Sprintf("%s.mp4", fixture.name)))
			}
		})
	}
}

func TestMonikaDecemberBackgroundAudit(t *testing.T) {
	root := os.Getenv("MONIKA_DECEMBER_SOURCE_DIR")
	if root == "" {
		t.Skip("set MONIKA_DECEMBER_SOURCE_DIR to run the background-model audit")
	}
	fixtures := []struct {
		name, source string
		at           int64
	}{
		{"playing", "playing", 221_800},
		{"hypnoteased", "hypnoteased", 510_000},
		{"resist", "resist", 436_680},
		{"mindless-raised", "mindless", 472_445},
		{"slow-torso", "slow-freeze", 747_000},
		{"slow-final", "slow-freeze", 920_000},
		{"reel-countdown-tail", "hypnoteased", 450_000},
		{"reel-mindless-flop", "mindless", 820_000},
		{"reel-slow-freeze", "slow-freeze", 730_000},
	}
	references := make(map[string][]smartCropV2Sample)
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			source := monikaDecemberSources[fixture.source]
			video := filepath.Join(root, source.file)
			refs := references[fixture.source]
			if refs == nil {
				positions := make([]int64, 0, 12)
				for i := 0; i < 12; i++ {
					positions = append(positions, 1_000+int64(i)*(source.duration-2_000)/11)
				}
				var err error
				refs, err = analyzeSmartCropV2Input(context.Background(), "ffmpeg", video, positions, 1920, 1080, 9, 16)
				if err != nil {
					t.Fatal(err)
				}
				references[fixture.source] = refs
			}
			current, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
				[]int64{fixture.at - 500, fixture.at, fixture.at + 500}, 1920, 1080, 9, 16)
			if err != nil || len(current) < 2 {
				t.Fatalf("current frame: samples=%d err=%v", len(current), err)
			}
			currentSample := nearestSmartCropSample(current, fixture.at)
			images := make([]image.Image, 0, len(refs))
			for _, ref := range refs {
				images = append(images, ref.img)
			}
			x, result, ok := backgroundAwareNarrowSmartCropX(currentSample.img, images, currentSample.point.X, 1920, 606)
			t.Logf("base=%d background=%d changed=%v result=%+v", currentSample.point.X, x, ok, result)
			if fixture.name == "mindless-raised" {
				components := warmSubjectComponents(normalizedSmartCropRGB(currentSample.img, 320, 180), 320, 180, 101, 0, 319)
				for i, component := range components {
					if i >= 12 {
						break
					}
					t.Logf("warm component %d: %+v", i, component)
				}
				for _, turns := range []int{1, 3} {
					rotated := rotateSmartCropAudit(currentSample.img, turns)
					t.Logf("rotated %d faces: %+v", turns, detectSmartCropFaces(rotated))
				}
			}
		})
	}
}

func TestMonikaDecemberTemporalAudit(t *testing.T) {
	root := os.Getenv("MONIKA_DECEMBER_SOURCE_DIR")
	if root == "" {
		t.Skip("set MONIKA_DECEMBER_SOURCE_DIR to run the temporal-model audit")
	}
	fixtures := []struct {
		name, source string
		start, end   int64
	}{
		{"resist-first", "resist", 429_975, 461_085},
		{"mindless-flop", "mindless", 805_610, 842_750},
		{"slow-freeze", "slow-freeze", 714_630, 752_945},
	}
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			source := monikaDecemberSources[fixture.source]
			video := filepath.Join(root, source.file)
			samples, err := analyzeSmartCropV2Input(context.Background(), "ffmpeg", video,
				monikaContainmentRangePositions(fixture.start, fixture.end, source.duration),
				1920, 1080, 9, 16)
			if err != nil {
				t.Fatal(err)
			}
			if result, ok := temporalSubjectConsensus(samples, 1920, 606); ok {
				t.Logf("raw=%+v", result)
			}
			if result, ok := bestSmartCropTemporalConsensus(samples, 1920, 606); ok {
				t.Logf("best=%+v", result)
			}
		})
	}
}

func rotateSmartCropAudit(src image.Image, quarterTurns int) image.Image {
	b := src.Bounds()
	w, h := b.Dx(), b.Dy()
	quarterTurns = ((quarterTurns % 4) + 4) % 4
	if quarterTurns == 0 {
		return src
	}
	var dst *image.RGBA
	if quarterTurns == 2 {
		dst = image.NewRGBA(image.Rect(0, 0, w, h))
	} else {
		dst = image.NewRGBA(image.Rect(0, 0, h, w))
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			switch quarterTurns {
			case 1:
				dst.Set(h-1-y, x, src.At(b.Min.X+x, b.Min.Y+y))
			case 2:
				dst.Set(w-1-x, h-1-y, src.At(b.Min.X+x, b.Min.Y+y))
			case 3:
				dst.Set(y, w-1-x, src.At(b.Min.X+x, b.Min.Y+y))
			}
		}
	}
	return dst
}

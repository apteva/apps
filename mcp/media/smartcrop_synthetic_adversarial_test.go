package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"math"
	"math/rand"
	"os"
	"reflect"
	"sync"
	"testing"
)

// These tests deliberately use generated pixels and fixed random seeds. They
// exercise broad invariants without publishing customer footage, depending on
// codec behavior, or turning CI into a probabilistic visual benchmark.

func TestSmartCropSyntheticFaceContainmentSweep(t *testing.T) {
	rng := rand.New(rand.NewSource(0x5c0ffee))
	for caseIndex := 0; caseIndex < 5_000; caseIndex++ {
		srcW := []int{640, 1280, 1920, 3840}[rng.Intn(4)]
		cropW := 120 + rng.Intn(srcW-120)
		center := rng.Intn(srcW)
		scale := 16 + rng.Intn(maxInt(17, minInt(360, srcW/2))-16)
		half := scale / 2
		face := smartCropFace{
			CenterX: center,
			MinX:    maxInt(0, center-half),
			MaxX:    minInt(srcW, center+half),
			Scale:   scale,
			Quality: 20,
		}
		start := rng.Intn(srcW+cropW) - cropW
		x := containSmartCropFaceX(start, face, srcW, cropW)
		if x < 0 || x > srcW-cropW {
			t.Fatalf("case %d escaped bounds: src=%d crop=%d start=%d face=%+v x=%d", caseIndex, srcW, cropW, start, face, x)
		}
		if center < x || center > x+cropW {
			t.Fatalf("case %d clipped face center: src=%d crop=%d start=%d face=%+v x=%d", caseIndex, srcW, cropW, start, face, x)
		}
		if again := containSmartCropFaceX(x, face, srcW, cropW); again != x {
			t.Fatalf("case %d containment was not idempotent: first=%d second=%d face=%+v", caseIndex, x, again, face)
		}

		margin := maxInt(face.Scale/2, int(math.Round(float64(cropW)*smartCropFaceSafeMargin)))
		wantedMin, wantedMax := face.MinX-margin, face.MaxX+margin
		feasibleLow := maxInt(0, wantedMax-cropW)
		feasibleHigh := minInt(srcW-cropW, wantedMin)
		firstFeasibleEven := feasibleLow
		if firstFeasibleEven%2 != 0 {
			firstFeasibleEven++
		}
		if wantedMax-wantedMin <= cropW && wantedMin >= 0 && wantedMax <= srcW && firstFeasibleEven <= feasibleHigh && (wantedMin < x || wantedMax > x+cropW) {
			t.Fatalf("case %d lost feasible safe margin: wanted=[%d,%d] crop=[%d,%d]", caseIndex, wantedMin, wantedMax, x, x+cropW)
		}
	}
}

func TestSmartCropSyntheticLongVideoFaceDropouts(t *testing.T) {
	const srcW, cropW = 1920, 606
	const samplesPerScene = 61
	samples := make([]smartCropV2Sample, 0, samplesPerScene*10)
	centers := make([]int, 0, samplesPerScene*10)
	for scene := 0; scene < 10; scene++ {
		for step := 0; step < samplesPerScene; step++ {
			at := int64(scene*samplesPerScene+step) * 1000
			phase := float64(step) / float64(samplesPerScene-1)
			center := 230 + int(math.Round(1460*phase))
			if scene%2 == 1 {
				center = srcW - center
			}
			desiredX := clampInt(roundEven(center-cropW/2), 0, srcW-cropW)
			sample := smartCropV2Sample{point: cropPathPoint{AtMs: at, X: (scene % 2) * (srcW - cropW)}}
			if step == 0 {
				sample.point.Cut = scene > 0
			}
			// Regularly miss seven consecutive detections, including around the
			// middle and near both frame edges. The bounding anchors remain inside
			// the production 12-second interpolation limit.
			if step%10 == 0 {
				sample.point.X = desiredX
				sample.face = syntheticSmartCropFace(center, 150)
			}
			samples = append(samples, sample)
			centers = append(centers, center)
		}
	}
	if corrected := correctSmartCropFaceTracks(samples, srcW, cropW); corrected < 400 {
		t.Fatalf("long-video dropout recovery was unexpectedly weak: corrected=%d", corrected)
	}
	for i, sample := range samples {
		if centers[i] < sample.point.X || centers[i] > sample.point.X+cropW {
			t.Fatalf("sample %d at %dms lost moving person: center=%d crop=[%d,%d]", i, sample.point.AtMs, centers[i], sample.point.X, sample.point.X+cropW)
		}
		if sample.point.X < 0 || sample.point.X > srcW-cropW {
			t.Fatalf("sample %d escaped bounds: %+v", i, sample)
		}
	}
}

func TestSmartCropSyntheticUprightToHorizontalMatrix(t *testing.T) {
	const srcW, cropW = 1920, 606
	for _, direction := range []int{-1, 1} {
		name := "falls-left"
		if direction > 0 {
			name = "falls-right"
		}
		t.Run(name, func(t *testing.T) {
			samples := make([]smartCropV2Sample, 31)
			uprightCenter := 900
			if direction < 0 {
				uprightCenter = 1100
			}
			horizontalCenter := 550
			horizontalBaseX := 400
			falseCenter := 800
			if direction > 0 {
				horizontalCenter = 1370
				horizontalBaseX = 914
				falseCenter = 1120
			}
			for i := range samples {
				center := uprightCenter + direction*i*40
				baseX := clampInt(center-cropW/2, 0, srcW-cropW)
				pose := syntheticStanding
				if i >= 10 && i < 28 {
					baseX = horizontalBaseX
					pose = syntheticReclined
				}
				if i >= 28 {
					center = 960
					baseX = center - cropW/2
				}
				samples[i] = smartCropV2Sample{
					point: cropPathPoint{AtMs: int64(i) * 1000, X: roundEven(baseX)},
					img:   syntheticAdversarialSmartCropFrame(320, 180, 160, pose, 900+i, false),
				}
				if i <= 5 || i >= 28 {
					strongCenter := center
					if i == 4 {
						strongCenter = 960 - direction*50
					}
					if i == 5 {
						strongCenter = 960 + direction*50
					}
					samples[i].face = syntheticSmartCropFace(strongCenter, 170)
					samples[i].face.Quality = 100
				}
			}
			// Two real sideways anchors compete with more frequent torso-shaped
			// cascade votes. Direction + repetition must select the head cluster.
			for _, i := range []int{10, 17} {
				samples[i].face = syntheticSmartCropFace(horizontalCenter, 360)
				samples[i].face.Quality = 6
			}
			for _, i := range []int{11, 13, 15} {
				samples[i].face = syntheticSmartCropFace(falseCenter, 180)
				samples[i].face.Quality = 7
			}

			filterSmartCropWeakFaceAnchors(samples, cropW)
			if filtered := filterSmartCropWeakFaceDirectionClusters(samples, srcW, cropW); filtered < 3 {
				t.Fatalf("torso cluster survived: filtered=%d", filtered)
			}
			correctSmartCropFaceTracks(samples, srcW, cropW)
			path := make([]cropPathPoint, len(samples))
			for i := range samples {
				path[i] = samples[i].point
			}
			path = stabilizeSmartCropPath(path, cropW, srcW)
			path = constrainSmartCropPathToFaceTracks(path, samples, srcW, cropW)
			for _, at := range []int64{12_000, 18_000, 24_000} {
				x := syntheticSmartCropPathXAt(path, at)
				faceMin, faceMax := horizontalCenter-180, horizontalCenter+180
				if faceMin < x || faceMax > x+cropW {
					t.Fatalf("at=%d horizontal face clipped: face=[%d,%d] crop=[%d,%d] path=%v",
						at, faceMin, faceMax, x, x+cropW, path)
				}
			}
			// Do not start panning back to the upright pose several seconds early.
			x25 := syntheticSmartCropPathXAt(path, 25_000)
			if direction < 0 && x25 > 360 {
				t.Fatalf("left horizontal hold released early: x=%d path=%v", x25, path)
			}
			if direction > 0 && x25 < 960 {
				t.Fatalf("right horizontal hold released early: x=%d path=%v", x25, path)
			}
		})
	}
}

func TestSmartCropSyntheticPostSmoothingFaceConstraints(t *testing.T) {
	const srcW, cropW = 1920, 606
	for seed := int64(0); seed < 250; seed++ {
		rng := rand.New(rand.NewSource(seed + 0xface))
		center := 180 + rng.Intn(srcW-360)
		face := syntheticSmartCropFace(center, 80+rng.Intn(180))
		face.Quality = 25
		desired := containSmartCropFaceX(rng.Intn(srcW-cropW+1), *face, srcW, cropW)
		samples := []smartCropV2Sample{
			{point: cropPathPoint{AtMs: 0, X: desired}},
			{point: cropPathPoint{AtMs: 1000, X: desired}, face: face},
			{point: cropPathPoint{AtMs: 2000, X: desired}},
		}
		// Deliberately model a smoothed path pulled away from the face anchor.
		path := []cropPathPoint{{AtMs: 0, X: rng.Intn(srcW - cropW + 1)}, {AtMs: 2000, X: rng.Intn(srcW - cropW + 1)}}
		path = constrainSmartCropPathToFaceTracks(path, samples, srcW, cropW)
		x := syntheticSmartCropPathXAt(path, 1000)
		if face.CenterX < x || face.CenterX > x+cropW {
			t.Fatalf("seed=%d face center clipped after constraint: face=%+v x=%d path=%v", seed, face, x, path)
		}
	}
}

func syntheticSmartCropPathXAt(path []cropPathPoint, atMs int64) int {
	if len(path) == 0 {
		return 0
	}
	if atMs <= path[0].AtMs {
		return path[0].X
	}
	for i := 1; i < len(path); i++ {
		if atMs <= path[i].AtMs {
			return interpolateSmartCropStillX(path[i-1], path[i], atMs)
		}
	}
	return path[len(path)-1].X
}

func TestSmartCropSyntheticPathStress(t *testing.T) {
	const srcW, cropW = 1920, 606
	for seed := int64(0); seed < 200; seed++ {
		rng := rand.New(rand.NewSource(seed))
		count := 8 + rng.Intn(80)
		path := make([]cropPathPoint, count)
		cutTimes := make(map[int64]bool)
		for i := range path {
			path[i] = cropPathPoint{AtMs: int64(i) * 500, X: rng.Intn(srcW+1000) - 500}
			if i > 0 && i < count-1 && rng.Intn(13) == 0 {
				path[i].Cut = true
				cutTimes[path[i].AtMs] = true
			}
		}
		got := stabilizeSmartCropPath(path, cropW, srcW)
		if len(got) < 2 || len(got) > count {
			t.Fatalf("seed %d invalid output length %d from %d", seed, len(got), count)
		}
		seenCuts := make(map[int64]bool)
		for i, point := range got {
			if point.X < 0 || point.X > srcW-cropW || point.X%2 != 0 {
				t.Fatalf("seed %d point %d invalid: %+v", seed, i, point)
			}
			if i > 0 && point.AtMs <= got[i-1].AtMs {
				t.Fatalf("seed %d timestamps no longer ordered: %v", seed, got)
			}
			if point.Cut {
				seenCuts[point.AtMs] = true
			}
		}
		if !reflect.DeepEqual(seenCuts, cutTimes) {
			t.Fatalf("seed %d lost a scene boundary: want=%v got=%v", seed, cutTimes, seenCuts)
		}
	}
}

func TestSmartCropSyntheticBackgroundSubjectSweep(t *testing.T) {
	const srcW, cropW = 1920, 606
	poses := []syntheticSmartCropPose{syntheticStanding, syntheticCrouched, syntheticReclined, syntheticOccluded}
	positions := []int{18, 48, 96, 160, 224, 272, 305}
	for _, pose := range poses {
		t.Run(pose.String(), func(t *testing.T) {
			for _, subjectX := range positions {
				references := make([]image.Image, 8)
				for i := range references {
					references[i] = syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, i, false)
				}
				current := syntheticAdversarialSmartCropFrame(320, 180, subjectX, pose, 100+subjectX, false)
				currentX := 0
				if subjectX < 160 {
					currentX = srcW - cropW
				}
				x, result, ok := backgroundAwareNarrowSmartCropX(current, references, currentX, srcW, cropW)
				if !ok {
					t.Fatalf("position %d was not recovered: current=%d scene_score=%.3f result=%+v", subjectX, currentX, sceneCutScore(current, references[0]), result)
				}
				center := int(math.Round(float64(subjectX) * srcW / 320.0))
				if center < x || center > x+cropW {
					t.Fatalf("position %d still clipped: source center=%d crop=[%d,%d] result=%+v", subjectX, center, x, x+cropW, result)
				}
			}
		})
	}
}

func TestSmartCropSyntheticBackgroundRejectsAmbiguousLowContrastEdge(t *testing.T) {
	const srcW, cropW = 1920, 606
	references := make([]image.Image, 8)
	for i := range references {
		references[i] = syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, i, false)
	}
	current := syntheticAdversarialSmartCropFrame(320, 180, 18, syntheticLowContrast, 118, false)
	// At this contrast the block differs from the couch by roughly the same
	// amount as codec shimmer. With no face or motion evidence, refusing a
	// room-wide jump is safer than fabricating a confident subject. Real faces
	// at this position are covered by the photographic CPU-model fixture.
	if x, result, ok := backgroundAwareNarrowSmartCropX(current, references, srcW-cropW, srcW, cropW); ok || x != srcW-cropW {
		t.Fatalf("ambiguous low-contrast edge caused a speculative jump: x=%d ok=%v result=%+v", x, ok, result)
	}
}

func TestSmartCropSyntheticBackgroundRejectsGlobalChanges(t *testing.T) {
	const srcW, cropW = 1920, 606
	references := make([]image.Image, 8)
	for i := range references {
		references[i] = syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, i, false)
	}
	cases := map[string]image.Image{
		"empty-room-with-compression-noise": syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, 99, false),
		"camera-pan":                        syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, 99, true),
		"global-exposure-change":            proceduralSmartCropFrameWithBase(320, 180, -1, color.RGBA{R: 180, G: 184, B: 182, A: 255}),
	}
	for name, current := range cases {
		t.Run(name, func(t *testing.T) {
			if x, result, ok := backgroundAwareNarrowSmartCropX(current, references, 600, srcW, cropW); ok || x != 600 {
				t.Fatalf("global/background change invented a person: x=%d ok=%v result=%+v", x, ok, result)
			}
		})
	}
}

func TestSmartCropSyntheticEndToEndSubjectSweep(t *testing.T) {
	const srcW, srcH = 1920, 1080
	for _, pose := range []syntheticSmartCropPose{syntheticStanding, syntheticCrouched, syntheticOccluded} {
		// Geometric figures intentionally are not faces. Keep this end-to-end
		// saliency gate to the region where the subject is visible to generic
		// analysis; the generated photographic fixture below covers edge faces.
		for _, subjectX := range []int{112, 160, 208} {
			img := syntheticAdversarialSmartCropFrame(320, 180, subjectX, pose, subjectX, false)
			window, _, err := analyzeSmartCropV2FrameDetailed(srcW, srcH, 9, 16, img)
			if err != nil {
				t.Fatalf("pose=%s x=%d: %v", pose, subjectX, err)
			}
			if window.X < 0 || window.X+window.W > srcW || window.Y < 0 || window.Y+window.H > srcH {
				t.Fatalf("pose=%s x=%d produced invalid window: %+v", pose, subjectX, window)
			}
			center := int(math.Round(float64(subjectX) * srcW / 320.0))
			if center < window.X || center > window.X+window.W {
				subjectX2, subjectOK := subjectAwareNarrowSmartCropX(img, window.X, window.X, srcW, srcH, window.W, window.H, 100)
				silhouetteX, silhouetteOK := silhouetteAwareNarrowSmartCropX(img, window.X, srcW, window.W, 100)
				headX, headOK := headAwareNarrowSmartCropX(img, window.X, srcW, window.W)
				recliningX, recliningOK := recliningSubjectAwareNarrowSmartCropX(img, window.X, srcW, window.W)
				t.Fatalf("pose=%s x=%d lost subject center=%d window=%+v guards=subject(%d,%v) silhouette(%d,%v) head(%d,%v) reclining(%d,%v)", pose, subjectX, center, window, subjectX2, subjectOK, silhouetteX, silhouetteOK, headX, headOK, recliningX, recliningOK)
			}
		}
	}
}

func TestSmartCropSyntheticPhotographicFaceFixture(t *testing.T) {
	file, err := os.Open("testdata/smartcrop/synthetic_seated_person.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	base, err := jpeg.Decode(file)
	if err != nil {
		t.Fatal(err)
	}

	faces := detectSmartCropFaces(base)
	if len(faces) == 0 {
		t.Fatal("embedded CPU model did not detect the generated frontal face")
	}
	if faces[0].CenterX < 250 || faces[0].CenterX > 390 {
		t.Fatalf("strongest detection was not the generated center face: %+v", faces)
	}

	for name, shifted := range map[string]image.Image{
		"face-near-left-edge":  shiftSyntheticSmartCropImage(base, -220),
		"face-near-right-edge": shiftSyntheticSmartCropImage(base, 220),
	} {
		t.Run(name, func(t *testing.T) {
			window, face, err := analyzeSmartCropV2FrameDetailed(1920, 1080, 9, 16, shifted)
			if err != nil {
				t.Fatal(err)
			}
			if face == nil {
				t.Fatalf("edge face was not detected; window=%+v", window)
			}
			if face.CenterX < window.X || face.CenterX > window.X+window.W {
				t.Fatalf("detected edge face was clipped: face=%+v window=%+v", face, window)
			}
		})
	}

	for _, turns := range []int{1, 3} {
		rotated := rotateSyntheticSmartCropImage(base, turns)
		if got := detectSmartCropFaces(rotated); len(got) == 0 {
			t.Fatalf("sideways generated face was missed at quarter-turn %d", turns)
		}
	}

	for _, delta := range []int{-35, -20, 20, 35} {
		adjusted := adjustSyntheticSmartCropExposure(base, delta)
		if got := detectSmartCropFaces(adjusted); len(got) == 0 {
			t.Fatalf("generated face was missed after exposure delta %d", delta)
		}
	}

	reclinedFile, err := os.Open("testdata/smartcrop/synthetic_reclined_person.jpg")
	if err != nil {
		t.Fatal(err)
	}
	reclined, err := jpeg.Decode(reclinedFile)
	reclinedFile.Close()
	if err != nil {
		t.Fatal(err)
	}
	for name, frame := range map[string]image.Image{
		"reclined-left":  reclined,
		"reclined-right": shiftSyntheticSmartCropImage(reclined, 340),
	} {
		t.Run(name, func(t *testing.T) {
			window, face, err := analyzeSmartCropV2FrameDetailed(1920, 1080, 9, 16, frame)
			if err != nil {
				t.Fatal(err)
			}
			if face == nil {
				t.Fatalf("generated reclining face was not detected; window=%+v raw_faces=%+v", window, detectSmartCropFaces(frame))
			}
			if face.Quality < smartCropFaceMinQuality {
				t.Fatalf("weak reclining cluster crossed the final threshold: face=%+v", face)
			}
			if face.CenterX < window.X || face.CenterX > window.X+window.W {
				t.Fatalf("generated reclining face was clipped: face=%+v window=%+v", face, window)
			}
		})
	}
}

func TestSmartCropSyntheticDetectorInputAndConcurrency(t *testing.T) {
	inputs := []image.Image{
		nil,
		image.NewRGBA(image.Rect(0, 0, 1, 1)),
		image.NewGray(image.Rect(0, 0, 23, 240)),
		image.NewNRGBA(image.Rect(0, 0, 320, 180)),
		image.NewRGBA(image.Rect(17, 23, 337, 203)),
		syntheticAdversarialSmartCropFrame(321, 181, 160, syntheticStanding, 42, false),
	}
	for i, input := range inputs {
		first := detectSmartCropFaces(input)
		second := detectSmartCropFaces(input)
		if !reflect.DeepEqual(first, second) {
			t.Fatalf("input %d detector was nondeterministic: first=%+v second=%+v", i, first, second)
		}
	}

	input := syntheticAdversarialSmartCropFrame(320, 180, 160, syntheticStanding, 77, false)
	want := detectSmartCropFaces(input)
	var wg sync.WaitGroup
	errs := make(chan []smartCropFace, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := detectSmartCropFaces(input); !reflect.DeepEqual(got, want) {
				errs <- got
			}
		}()
	}
	wg.Wait()
	close(errs)
	for got := range errs {
		t.Fatalf("concurrent detector result changed: want=%+v got=%+v", want, got)
	}
}

func TestSmartCropSyntheticDetectorRejectsPatternedRooms(t *testing.T) {
	// Clustering weak votes must recover real profile faces without converting
	// repeated room texture into a confident face. Sweep many deterministic
	// checker/noise phases because a single blank image is not a useful guard.
	for seed := 0; seed < 80; seed++ {
		frame := syntheticAdversarialSmartCropFrame(320, 180, -1, syntheticStanding, seed, seed%7 == 0)
		if faces := detectSmartCropFaces(frame); len(faces) != 0 {
			t.Fatalf("seed %d invented faces in an empty patterned room: %+v", seed, faces)
		}
	}
}

func syntheticSmartCropFace(center, scale int) *smartCropFace {
	return &smartCropFace{
		CenterX: center,
		MinX:    center - scale/2,
		MaxX:    center + scale/2,
		Scale:   scale,
		Quality: 20,
	}
}

type syntheticSmartCropPose int

const (
	syntheticStanding syntheticSmartCropPose = iota
	syntheticCrouched
	syntheticReclined
	syntheticOccluded
	syntheticLowContrast
)

func (p syntheticSmartCropPose) String() string {
	return [...]string{"standing", "crouched", "reclined", "occluded", "low-contrast"}[p]
}

func syntheticAdversarialSmartCropFrame(w, h, subjectX int, pose syntheticSmartCropPose, seed int, cameraPan bool) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			xPattern := x
			if cameraPan {
				xPattern += 43
			}
			noise := ((x*17 + y*31 + seed*13) % 5) - 2
			checker := ((xPattern/20 + y/16) % 2) * 9
			base := 72 + checker + noise
			img.SetRGBA(x, y, color.RGBA{R: uint8(base + (xPattern/53)%4), G: uint8(base + 4), B: uint8(base + 2), A: 255})
		}
	}
	// Fixed warm furniture and a high-contrast picture are intentional hard
	// negatives. They exist in every reference and must not become the subject.
	fillSyntheticSmartCropRect(img, 18, h*2/3, w-18, h-8, color.RGBA{R: 122, G: 79, B: 64, A: 255})
	fillSyntheticSmartCropRect(img, w/2-26, 18, w/2+26, 58, color.RGBA{R: 205, G: 185, B: 120, A: 255})
	if subjectX < 0 {
		return img
	}
	body := color.RGBA{R: 176, G: 39, B: 58, A: 255}
	skin := color.RGBA{R: 221, G: 157, B: 123, A: 255}
	if pose == syntheticLowContrast {
		body = color.RGBA{R: 112, G: 91, B: 88, A: 255}
		skin = color.RGBA{R: 142, G: 112, B: 98, A: 255}
	}
	switch pose {
	case syntheticCrouched:
		fillSyntheticSmartCropRect(img, subjectX-30, h/2, subjectX+30, h-8, body)
		fillSyntheticSmartCropRect(img, subjectX-17, h/2-22, subjectX+17, h/2+10, skin)
	case syntheticReclined:
		fillSyntheticSmartCropRect(img, subjectX-58, h*3/5, subjectX+54, h-17, body)
		fillSyntheticSmartCropRect(img, subjectX-67, h*3/5-12, subjectX-35, h*3/5+22, skin)
	case syntheticOccluded:
		fillSyntheticSmartCropRect(img, subjectX-25, h/4, subjectX+25, h-8, body)
		fillSyntheticSmartCropRect(img, subjectX-17, h/6, subjectX+17, h/3, skin)
		fillSyntheticSmartCropRect(img, subjectX-6, h/5, subjectX+34, h*4/5, color.RGBA{R: 78, G: 82, B: 80, A: 255})
	default:
		fillSyntheticSmartCropRect(img, subjectX-25, h/4, subjectX+25, h-8, body)
		fillSyntheticSmartCropRect(img, subjectX-17, h/7, subjectX+17, h/3, skin)
	}
	return img
}

func fillSyntheticSmartCropRect(img *image.RGBA, minX, minY, maxX, maxY int, c color.RGBA) {
	b := img.Bounds()
	minX, minY = maxInt(b.Min.X, minX), maxInt(b.Min.Y, minY)
	maxX, maxY = minInt(b.Max.X, maxX), minInt(b.Max.Y, maxY)
	for y := minY; y < maxY; y++ {
		for x := minX; x < maxX; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func shiftSyntheticSmartCropImage(src image.Image, dx int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	draw.Draw(dst, dst.Bounds(), &image.Uniform{C: color.RGBA{R: 118, G: 118, B: 114, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: b.Min.X - dx, Y: b.Min.Y}, draw.Src)
	return dst
}

func rotateSyntheticSmartCropImage(src image.Image, quarterTurns int) image.Image {
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

func adjustSyntheticSmartCropExposure(src image.Image, delta int) image.Image {
	b := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, b.Dx(), b.Dy()))
	for y := 0; y < b.Dy(); y++ {
		for x := 0; x < b.Dx(); x++ {
			r, g, bl, a := src.At(b.Min.X+x, b.Min.Y+y).RGBA()
			dst.SetRGBA(x, y, color.RGBA{
				R: uint8(clampInt(int(r>>8)+delta, 0, 255)),
				G: uint8(clampInt(int(g>>8)+delta, 0, 255)),
				B: uint8(clampInt(int(bl>>8)+delta, 0, 255)),
				A: uint8(a >> 8),
			})
		}
	}
	return dst
}

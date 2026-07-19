package main

import (
	_ "embed"
	"fmt"
	"image"
	"math"
	"sort"
	"sync"

	pigo "github.com/esimov/pigo/core"
)

// The 234 KiB PICO cascade is MIT licensed (see
// smartcrop_model/LICENSE.pigo) and compiled into the sidecar. Keeping the
// detector in-process makes smart crop deterministic and avoids adding an
// OpenCV, Python, network, or GPU dependency to either Media or the remote
// FFmpeg host.
//
//go:embed smartcrop_model/facefinder
var smartCropFaceCascade []byte

const (
	smartCropFaceMinQuality     = float32(5.0)
	smartCropFaceMinClusterVote = float32(1.0)
	smartCropFaceSafeMargin     = 0.16
)

type smartCropFace struct {
	CenterX int
	MinX    int
	MaxX    int
	Scale   int
	Quality float32
}

var smartCropFaces = struct {
	once       sync.Once
	classifier *pigo.Pigo
	err        error
	mu         sync.Mutex
}{}

func loadSmartCropFaceClassifier() (*pigo.Pigo, error) {
	smartCropFaces.once.Do(func() {
		defer func() {
			if recovered := recover(); recovered != nil {
				smartCropFaces.err = fmt.Errorf("unpack embedded face cascade: %v", recovered)
			}
		}()
		smartCropFaces.classifier, smartCropFaces.err = pigo.NewPigo().Unpack(smartCropFaceCascade)
	})
	return smartCropFaces.classifier, smartCropFaces.err
}

// detectSmartCropFaces runs a small CPU-only classifier at the resolution
// already used by smart crop (normally 320 px wide). Physical quarter-turn
// passes are important for people reclining on a couch. They always run: an
// upright false positive in furniture must not suppress the real sideways
// face, which is precisely the failure mode this detector is meant to cover.
func detectSmartCropFaces(img image.Image) []smartCropFace {
	if img == nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w < 24 || h < 24 {
		return nil
	}
	classifier, err := loadSmartCropFaceClassifier()
	if err != nil || classifier == nil {
		return nil
	}
	pixels := make([]uint8, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			r, g, b, _ := img.At(bounds.Min.X+x, bounds.Min.Y+y).RGBA()
			pixels[y*w+x] = uint8((299*(r>>8) + 587*(g>>8) + 114*(b>>8)) / 1000)
		}
	}
	minSize := maxInt(16, minInt(w, h)/12)
	maxSize := minInt(w, h) * 4 / 5
	params := pigo.CascadeParams{
		MinSize:     minSize,
		MaxSize:     maxSize,
		ShiftFactor: 0.1,
		ScaleFactor: 1.1,
		ImageParams: pigo.ImageParams{Pixels: pixels, Rows: h, Cols: w, Dim: w},
	}

	// Serialize inference because the classifier owns reusable tree storage.
	// Smart crop parallelizes frame downloads; inference itself is short at
	// 320x180 and deterministic serialization avoids a detector data race.
	smartCropFaces.mu.Lock()
	run := func(cascade pigo.CascadeParams) []pigo.Detection {
		// Cluster the weak overlapping cascade votes before applying the final
		// quality threshold. A profile or reclining face can be composed of
		// several individually weak votes whose cluster is strong. Filtering the
		// raw votes first silently removed those real faces, while filtering the
		// combined result below retains the same public acceptance threshold.
		raw := classifier.RunCascade(cascade, 0)
		votes := raw[:0]
		for _, detection := range raw {
			if detection.Q >= smartCropFaceMinClusterVote {
				votes = append(votes, detection)
			}
		}
		return classifier.ClusterDetections(votes, 0.18)
	}
	raw := run(params)
	type rotatedDetection struct {
		detection pigo.Detection
		turn      int
	}
	var rotated []rotatedDetection
	clockwise := make([]uint8, len(pixels))
	counterClockwise := make([]uint8, len(pixels))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			value := pixels[y*w+x]
			clockwise[x*h+(h-1-y)] = value
			counterClockwise[(w-1-x)*h+y] = value
		}
	}
	rotatedParams := params
	rotatedParams.ImageParams = pigo.ImageParams{Pixels: clockwise, Rows: w, Cols: h, Dim: h}
	for _, detection := range run(rotatedParams) {
		rotated = append(rotated, rotatedDetection{detection: detection, turn: 1})
	}
	rotatedParams.ImageParams.Pixels = counterClockwise
	for _, detection := range run(rotatedParams) {
		rotated = append(rotated, rotatedDetection{detection: detection, turn: 3})
	}
	smartCropFaces.mu.Unlock()

	faces := make([]smartCropFace, 0, len(raw))
	for _, detection := range raw {
		if detection.Q < smartCropFaceMinQuality || detection.Scale < minSize {
			continue
		}
		half := detection.Scale / 2
		faces = append(faces, smartCropFace{
			CenterX: clampInt(detection.Col, 0, w-1),
			MinX:    clampInt(detection.Col-half, 0, w-1),
			MaxX:    clampInt(detection.Col+half, 0, w-1),
			Scale:   detection.Scale,
			Quality: detection.Q,
		})
	}
	for _, candidate := range rotated {
		detection := candidate.detection
		if detection.Q < smartCropFaceMinQuality || detection.Scale < minSize {
			continue
		}
		centerX := detection.Row
		if candidate.turn == 3 {
			centerX = w - 1 - detection.Row
		}
		half := detection.Scale / 2
		faces = append(faces, smartCropFace{
			CenterX: clampInt(centerX, 0, w-1),
			MinX:    clampInt(centerX-half, 0, w-1),
			MaxX:    clampInt(centerX+half, 0, w-1),
			Scale:   detection.Scale,
			Quality: detection.Q,
		})
	}
	sort.Slice(faces, func(i, j int) bool {
		left := float64(faces[i].Quality) * math.Sqrt(float64(faces[i].Scale))
		right := float64(faces[j].Quality) * math.Sqrt(float64(faces[j].Scale))
		return left > right
	})
	return faces
}

// faceAwareNarrowSmartCropX keeps the strongest detected face inside a safe
// portrait margin. The generic body/saliency crop remains authoritative when
// that face is already contained.
func faceAwareNarrowSmartCropX(img image.Image, currentX, srcW, cropW int) (int, smartCropFace, bool) {
	if img == nil || srcW <= cropW || cropW <= 0 {
		return currentX, smartCropFace{}, false
	}
	bounds := img.Bounds()
	tW := bounds.Dx()
	if tW <= 0 {
		return currentX, smartCropFace{}, false
	}
	faces := detectSmartCropFaces(img)
	if len(faces) == 0 {
		return currentX, smartCropFace{}, false
	}
	primary := faces[0]
	toSource := func(v int) int {
		return clampInt(int(math.Round(float64(v)*float64(srcW)/float64(tW))), 0, srcW)
	}
	face := smartCropFace{
		CenterX: toSource(primary.CenterX),
		MinX:    toSource(primary.MinX),
		MaxX:    toSource(primary.MaxX),
		Scale:   toSource(primary.Scale),
		Quality: primary.Quality,
	}
	x := containSmartCropFaceX(currentX, face, srcW, cropW)
	return x, face, true
}

func containSmartCropFaceX(currentX int, face smartCropFace, srcW, cropW int) int {
	margin := maxInt(face.Scale/2, int(math.Round(float64(cropW)*smartCropFaceSafeMargin)))
	minWanted := face.MinX - margin
	maxWanted := face.MaxX + margin
	if maxWanted-minWanted > cropW {
		minWanted = face.CenterX - cropW/2
		maxWanted = minWanted + cropW
	}
	// If the face is already comfortably contained, avoid changing a credible
	// body/saliency crop. Otherwise make the smallest move that restores the
	// safe margin; this is much less jittery than centering every detection.
	x := currentX
	if minWanted < x {
		x = minWanted
	}
	if maxWanted > x+cropW {
		x = maxWanted - cropW
	}
	// Clamp before enforcing chroma-safe even coordinates. If the minimally
	// moved coordinate is odd, choose the adjacent even coordinate that loses
	// the least requested face margin. Always rounding downward can clip the
	// right margin; clamping after rounding can reintroduce an odd coordinate
	// when srcW-cropW itself is odd.
	maxX := srcW - cropW
	x = clampInt(x, 0, maxX)
	if x%2 != 0 {
		down, up := x-1, x+1
		violation := func(candidate int) int {
			if candidate < 0 || candidate > maxX {
				return math.MaxInt
			}
			return maxInt(0, candidate-minWanted) + maxInt(0, maxWanted-(candidate+cropW))
		}
		if violation(up) < violation(down) {
			x = up
		} else {
			x = down
		}
	}
	return x
}

func smartCropFaceTrackNeedsSourceSamples(samples []smartCropV2Sample, cropW int) bool {
	if len(samples) < 2 || cropW <= 0 {
		return false
	}
	anchors := 0
	missing := 0
	minX, maxX := 0, 0
	for i := range samples {
		if samples[i].face == nil {
			missing++
			continue
		}
		if anchors == 0 {
			minX, maxX = samples[i].point.X, samples[i].point.X
		} else {
			minX = minInt(minX, samples[i].point.X)
			maxX = maxInt(maxX, samples[i].point.X)
		}
		anchors++
	}
	if anchors >= 2 && (missing > 0 || maxX-minX > cropW/6) {
		return true
	}
	if anchors == 1 && missing >= 2 {
		for i := range samples {
			if samples[i].face != nil {
				continue
			}
			if absInt(samples[i].point.X-minX) > cropW/2 {
				return true
			}
		}
	}
	return false
}

// correctSmartCropFaceTracks treats ML detections as anchors, not a complete
// track. Misses inside a scene are interpolated; the latest edge anchor is
// carried for at most one bounded storyboard gap. This keeps profiles,
// occlusions and motion blur stable without freezing a person who genuinely
// crosses the frame.
func correctSmartCropFaceTracks(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) == 0 || srcW <= cropW || cropW <= 0 {
		return 0
	}
	corrected := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		anchors := make([]int, 0, sceneEnd-sceneStart)
		for i := sceneStart; i < sceneEnd; i++ {
			if samples[i].face != nil {
				x := containSmartCropFaceX(samples[i].point.X, *samples[i].face, srcW, cropW)
				if x != samples[i].point.X {
					samples[i].point.X = x
					corrected++
				}
				anchors = append(anchors, i)
			}
		}
		for a := 0; a+1 < len(anchors); a++ {
			left, right := anchors[a], anchors[a+1]
			if samples[right].point.AtMs-samples[left].point.AtMs > smartCropV2MaxGapMs {
				continue
			}
			for i := left + 1; i < right; i++ {
				if samples[i].face != nil {
					continue
				}
				x := interpolateSmartCropStillX(samples[left].point, samples[right].point, samples[i].point.AtMs)
				x = clampInt(roundEven(x), 0, srcW-cropW)
				if absInt(samples[i].point.X-x) > maxInt(20, cropW/10) {
					samples[i].point.X = x
					samples[i].faceTracked = true
					corrected++
				}
			}
		}
		if len(anchors) >= 2 {
			first, second := anchors[0], anchors[1]
			for i := first - 1; i >= sceneStart; i-- {
				if samples[first].point.AtMs-samples[i].point.AtMs > 2500 {
					break
				}
				x := samples[first].point.X
				if samples[second].point.AtMs > samples[first].point.AtMs {
					delta := samples[second].point.X - samples[first].point.X
					dt := samples[second].point.AtMs - samples[first].point.AtMs
					x -= int(float64(delta) * float64(samples[first].point.AtMs-samples[i].point.AtMs) / float64(dt))
				}
				x = clampInt(roundEven(x), 0, srcW-cropW)
				if absInt(samples[i].point.X-x) > maxInt(20, cropW/10) {
					samples[i].point.X = x
					samples[i].faceTracked = true
					corrected++
				}
			}
			last, previous := anchors[len(anchors)-1], anchors[len(anchors)-2]
			stableTail := absInt(samples[last].point.X-samples[previous].point.X) <= cropW/12
			// A face commonly disappears when it turns into profile or becomes
			// horizontal. Hold the last human anchor for one bounded storyboard
			// gap instead of snapping to room saliency. A settled track may keep
			// its small velocity; after a large move we hold position rather than
			// extrapolating blindly. Any motion-certified sample stops the carry.
			for i := last + 1; i < sceneEnd; i++ {
				if samples[i].point.AtMs-samples[last].point.AtMs > smartCropV2MaxGapMs || samples[i].motionTracked {
					break
				}
				x := samples[last].point.X
				if stableTail && samples[last].point.AtMs > samples[previous].point.AtMs {
					delta := samples[last].point.X - samples[previous].point.X
					dt := samples[last].point.AtMs - samples[previous].point.AtMs
					x += int(float64(delta) * float64(samples[i].point.AtMs-samples[last].point.AtMs) / float64(dt))
				}
				x = clampInt(roundEven(x), 0, srcW-cropW)
				if stableTail {
					x = clampInt(x, samples[last].point.X-cropW/4, samples[last].point.X+cropW/4)
					x = clampInt(roundEven(x), 0, srcW-cropW)
				}
				if absInt(samples[i].point.X-x) > maxInt(20, cropW/10) {
					samples[i].point.X = x
					samples[i].faceTracked = true
					corrected++
				}
			}
		}
		sceneStart = sceneEnd
	}
	return corrected
}

func smartCropFaceTrackXAt(samples []smartCropV2Sample, focusMs int64, cropW, srcW int) (int, bool) {
	anchors := make([]smartCropV2Sample, 0, len(samples))
	for _, sample := range samples {
		if sample.face != nil {
			anchors = append(anchors, sample)
		}
	}
	if len(anchors) == 0 {
		return 0, false
	}
	var before, after *smartCropV2Sample
	for i := range anchors {
		anchor := &anchors[i]
		if anchor.point.AtMs <= focusMs && (before == nil || anchor.point.AtMs > before.point.AtMs) {
			before = anchor
		}
		if anchor.point.AtMs >= focusMs && (after == nil || anchor.point.AtMs < after.point.AtMs) {
			after = anchor
		}
	}
	if before != nil && after != nil && after.point.AtMs-before.point.AtMs <= smartCropV2MaxGapMs {
		return clampInt(roundEven(interpolateSmartCropStillX(before.point, after.point, focusMs)), 0, srcW-cropW), true
	}
	nearest := anchors[0]
	for _, anchor := range anchors[1:] {
		if absInt64(anchor.point.AtMs-focusMs) < absInt64(nearest.point.AtMs-focusMs) {
			nearest = anchor
		}
	}
	if len(anchors) >= 2 && absInt64(nearest.point.AtMs-focusMs) <= smartCropV2MaxGapMs {
		return clampInt(roundEven(nearest.point.X), 0, srcW-cropW), true
	}
	if nearest.face.Quality >= 20 && absInt64(nearest.point.AtMs-focusMs) <= 2500 {
		return clampInt(roundEven(nearest.point.X), 0, srcW-cropW), true
	}
	return 0, false
}

// stabilizeSmartCropStillMotionHandoff covers the instant immediately after a
// fast traverse, when the person has stopped but saliency snaps back to the
// room before the next face detection. A recent face anchor establishes the
// direction, and the motion-certified half-second frame supplies the handoff.
// The extrapolation is capped to one quarter of the crop width.
func stabilizeSmartCropStillMotionHandoff(baseX int, tracked, context []smartCropV2Sample, focusMs int64, cropW, srcW int) (int, bool) {
	if len(tracked) < 2 || cropW <= 0 || srcW <= cropW {
		return baseX, false
	}
	var motion *smartCropV2Sample
	for i := range tracked {
		sample := &tracked[i]
		if !sample.motionTracked || absInt64(sample.point.AtMs-focusMs) > 750 {
			continue
		}
		if motion == nil || absInt64(sample.point.AtMs-focusMs) < absInt64(motion.point.AtMs-focusMs) {
			motion = sample
		}
	}
	if motion == nil || absInt(baseX-motion.point.X) <= cropW/6 {
		return baseX, false
	}
	var anchor *smartCropV2Sample
	for i := range context {
		sample := &context[i]
		if sample.face == nil || sample.point.AtMs >= motion.point.AtMs ||
			motion.point.AtMs-sample.point.AtMs > smartCropV2MaxGapMs {
			continue
		}
		if anchor == nil || sample.point.AtMs > anchor.point.AtMs {
			anchor = sample
		}
	}
	if anchor == nil {
		return baseX, false
	}
	delta := motion.point.X - anchor.point.X
	if absInt(delta) <= cropW/4 {
		return baseX, false
	}
	extra := minInt(cropW/4, absInt(delta)/2)
	x := motion.point.X
	if delta > 0 {
		x += extra
	} else {
		x -= extra
	}
	x = clampInt(roundEven(x), 0, srcW-cropW)
	return x, x != baseX
}

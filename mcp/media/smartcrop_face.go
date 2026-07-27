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
	CenterY int
	MinX    int
	MinY    int
	MaxX    int
	MaxY    int
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
			CenterY: clampInt(detection.Row, 0, h-1),
			MinX:    clampInt(detection.Col-half, 0, w-1),
			MinY:    clampInt(detection.Row-half, 0, h-1),
			MaxX:    clampInt(detection.Col+half, 0, w-1),
			MaxY:    clampInt(detection.Row+half, 0, h-1),
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
		centerY := h - 1 - detection.Col
		if candidate.turn == 3 {
			centerX = w - 1 - detection.Row
			centerY = detection.Col
		}
		half := detection.Scale / 2
		faces = append(faces, smartCropFace{
			CenterX: clampInt(centerX, 0, w-1),
			CenterY: clampInt(centerY, 0, h-1),
			MinX:    clampInt(centerX-half, 0, w-1),
			MinY:    clampInt(centerY-half, 0, h-1),
			MaxX:    clampInt(centerX+half, 0, w-1),
			MaxY:    clampInt(centerY+half, 0, h-1),
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
// portrait margin. A strong detection is centered: for a single still that is
// both more natural and protects the nearby torso/body extent. Weak profile
// and reclining detections retain the minimal-movement behavior because their
// main purpose is edge protection without introducing reel jitter.
func faceAwareNarrowSmartCropX(img image.Image, currentX, srcW, srcH, cropW int) (int, smartCropFace, bool) {
	if img == nil || srcW <= cropW || srcH <= 0 || cropW <= 0 {
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
	toSourceY := func(v int) int {
		return clampInt(int(math.Round(float64(v)*float64(srcH)/float64(bounds.Dy()))), 0, srcH)
	}
	face := smartCropFace{
		CenterX: toSource(primary.CenterX),
		CenterY: toSourceY(primary.CenterY),
		MinX:    toSource(primary.MinX),
		MinY:    toSourceY(primary.MinY),
		MaxX:    toSource(primary.MaxX),
		MaxY:    toSourceY(primary.MaxY),
		Scale:   toSource(primary.Scale),
		Quality: primary.Quality,
	}
	if !smartCropFaceCandidateSupported(face, currentX, cropW, smartCropWeakFaceHasPixelSupport(img, primary)) {
		return currentX, smartCropFace{}, false
	}
	if face.Quality >= 20 {
		x := clampInt(roundEven(face.CenterX-cropW/2), 0, srcW-cropW)
		return x, face, true
	}
	x := containSmartCropFaceX(currentX, face, srcW, cropW)
	return x, face, true
}

// smartCropFaceCandidateSupported prevents a weak, isolated cascade vote from
// overriding the subject window already established by saliency, warm-subject,
// silhouette, and head passes. Low-confidence PICO clusters are valuable for
// profile and reclining faces, but on a large still image they can also fire
// on a television edge, furniture seam, knee, or patterned wall.
//
// A strong detection remains authoritative anywhere. A medium detection may
// protect a face whose bounds overlap the crop. A threshold-level detection
// must either put its center inside the established subject window or overlap
// that window with independent pixel support; otherwise the preceding non-ML
// evidence wins. Reel processing applies the same principle across multiple
// frames in filterSmartCropWeakFaceAnchors.
func smartCropFaceCandidateSupported(face smartCropFace, currentX, cropW int, weakPixelSupport bool) bool {
	if cropW <= 0 {
		return false
	}
	if face.Quality >= 20 {
		return true
	}
	cropEnd := currentX + cropW
	if face.CenterX >= currentX && face.CenterX <= cropEnd {
		return true
	}
	if face.MaxX < currentX || face.MinX > cropEnd {
		return false
	}
	return face.Quality >= 12 || weakPixelSupport
}

// smartCropWeakFaceHasPixelSupport gives a threshold-level external face vote
// one independent check before it can move the crop. Genuine profile and
// reclining fixtures contain a compact, connected skin/chroma region inside
// the PICO box; the room patterns that create weak false votes do not. This is
// deliberately only a rescue path for a face box that already overlaps the
// established crop, so monochrome or dark-complexion subjects inside the crop
// continue to be accepted without this colour cue.
func smartCropWeakFaceHasPixelSupport(img image.Image, face smartCropFace) bool {
	if img == nil || face.Scale <= 0 {
		return false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return false
	}
	minX := clampInt(face.MinX, 0, b.Dx()-1)
	maxX := clampInt(face.MaxX, minX, b.Dx()-1)
	minY := clampInt(face.MinY, 0, b.Dy()-1)
	maxY := clampInt(face.MaxY, minY, b.Dy()-1)
	stride := maxInt(1, maxInt(maxX-minX+1, maxY-minY+1)/64)
	strict, total := 0, 0
	for y := minY; y <= maxY; y += stride {
		for x := minX; x <= maxX; x += stride {
			r, g, blue := rgb8(img.At(b.Min.X+x, b.Min.Y+y))
			if strictWarmSubjectPixel(r, g, blue) {
				strict++
			}
			total++
		}
	}
	return total > 0 && float64(strict)/float64(total) >= 0.45
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

// filterSmartCropWeakFaceAnchors rejects low-confidence rotated-cascade hits
// that disagree with the subject path established by generic saliency,
// foreground, and motion analysis. These weak hits are useful for profile and
// reclining faces, but knees and a bent torso can produce similar votes. A
// reel path has already incorporated foreground/motion evidence at this stage,
// so a weak face center must lie inside that subject window. Strong detections
// remain authoritative regardless of position.
func filterSmartCropWeakFaceAnchors(samples []smartCropV2Sample, cropW int) int {
	if len(samples) == 0 || cropW <= 0 {
		return 0
	}
	filtered := 0
	for i := range samples {
		face := samples[i].face
		if face == nil || face.Quality >= 20 {
			continue
		}
		if face.CenterX < samples[i].point.X ||
			face.CenterX > samples[i].point.X+cropW {
			samples[i].face = nil
			filtered++
		}
	}
	return filtered
}

// filterSmartCropWeakFaceDirectionClusters resolves the ambiguous interval
// after a strong upright face turns sideways. The rotated cascade may then
// alternate between the real head and a knee/torso. When the last two strong
// anchors establish a direction, retain a repeated weak cluster at that outer
// extent and discard weak candidates that reverse back toward the torso.
func filterSmartCropWeakFaceDirectionClusters(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 4 || srcW <= cropW || cropW <= 0 {
		return 0
	}
	filtered := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		strong := make([]int, 0, sceneEnd-sceneStart)
		for i := sceneStart; i < sceneEnd; i++ {
			if samples[i].face != nil && samples[i].face.Quality >= 20 {
				strong = append(strong, i)
			}
		}
		for s := 1; s < len(strong); s++ {
			previous, anchor := strong[s-1], strong[s]
			delta := samples[anchor].face.CenterX - samples[previous].face.CenterX
			if absInt(delta) < cropW/10 {
				continue
			}
			end := sceneEnd
			if s+1 < len(strong) {
				end = strong[s+1]
			}
			type weakCandidate struct {
				index int
				x     int
			}
			weak := make([]weakCandidate, 0, end-anchor-1)
			for i := anchor + 1; i < end; i++ {
				if samples[i].face == nil || samples[i].face.Quality >= 20 {
					continue
				}
				x := containSmartCropFaceX(samples[i].point.X, *samples[i].face, srcW, cropW)
				weak = append(weak, weakCandidate{index: i, x: x})
			}
			if len(weak) < 2 {
				continue
			}
			radius := maxInt(24, cropW/8)
			bestX, bestCount, bestSet := 0, 0, false
			for _, candidate := range weak {
				count := 0
				for _, other := range weak {
					if absInt(other.x-candidate.x) <= radius {
						count++
					}
				}
				if count < 2 {
					continue
				}
				outer := !bestSet || (delta < 0 && candidate.x < bestX) || (delta > 0 && candidate.x > bestX)
				if outer || (candidate.x == bestX && count > bestCount) {
					bestX, bestCount, bestSet = candidate.x, count, true
				}
			}
			if !bestSet {
				continue
			}
			for _, candidate := range weak {
				if absInt(candidate.x-bestX) <= radius {
					continue
				}
				samples[candidate.index].face = nil
				filtered++
			}
		}
		sceneStart = sceneEnd
	}
	return filtered
}

// correctSmartCropWeakFaceExcursions rejects a short, isolated pair of weak
// rotated-cascade hits when dense tracking says the subject stayed put. This
// is the characteristic failure produced by a bent limb or patterned cushion:
// two nearby "faces" briefly pull the crop away, while stable samples on both
// sides agree on the actual subject position.
//
// The rule deliberately requires two stable samples on each side, no motion,
// temporal, or head tracking, low detector confidence, and a return to the
// same baseline. A real traversal, pose hand-off, or scene cut therefore
// remains untouched.
func correctSmartCropWeakFaceExcursions(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 6 || srcW <= cropW || cropW <= 0 {
		return 0
	}
	corrected := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		for first := sceneStart + 2; first+3 < sceneEnd; first++ {
			if samples[first].face == nil || samples[first].face.Quality >= 12 ||
				samples[first+1].face == nil || samples[first+1].face.Quality >= 12 {
				continue
			}
			if first+2 < sceneEnd && samples[first+2].face != nil {
				continue
			}
			before := []int{samples[first-2].point.X, samples[first-1].point.X}
			after := []int{samples[first+2].point.X, samples[first+3].point.X}
			if samples[first-2].face != nil || samples[first-1].face != nil ||
				samples[first+2].face != nil || samples[first+3].face != nil {
				continue
			}
			tracked := false
			for i := first - 2; i <= first+3; i++ {
				if samples[i].motionTracked || samples[i].headTracked || samples[i].temporalTrack {
					tracked = true
					break
				}
			}
			if tracked ||
				samples[first+1].point.AtMs-samples[first].point.AtMs > 6_000 ||
				absInt(before[0]-before[1]) > cropW/12 ||
				absInt(after[0]-after[1]) > cropW/12 {
				continue
			}
			beforeX := roundEven((before[0] + before[1]) / 2)
			afterX := roundEven((after[0] + after[1]) / 2)
			firstX := containSmartCropFaceX(samples[first].point.X, *samples[first].face, srcW, cropW)
			secondX := containSmartCropFaceX(samples[first+1].point.X, *samples[first+1].face, srcW, cropW)
			if absInt(beforeX-afterX) > cropW/10 ||
				absInt(firstX-beforeX) <= cropW/3 ||
				absInt(secondX-beforeX) <= cropW/3 ||
				(firstX < beforeX) != (secondX < beforeX) ||
				absInt(firstX-secondX) > cropW/8 {
				continue
			}
			left := cropPathPoint{AtMs: samples[first-1].point.AtMs, X: beforeX}
			right := cropPathPoint{AtMs: samples[first+2].point.AtMs, X: afterX}
			for i := first; i <= first+1; i++ {
				samples[i].point.X = roundEven(interpolateSmartCropStillX(left, right, samples[i].point.AtMs))
				samples[i].face = nil
				samples[i].detailedFace = nil
				corrected++
			}
			first++
		}
		sceneStart = sceneEnd
	}
	return corrected
}

// promoteSmartCropDetailedFaces introduces the higher-resolution detector only
// after generic temporal/motion analysis has established the subject window.
// Keeping these weak rotated detections out of earlier consensus prevents a
// knee-shaped false positive from changing which tracking branch executes.
func promoteSmartCropDetailedFaces(samples []smartCropV2Sample, cropW int, allowEdgeHalo bool) int {
	if len(samples) == 0 || cropW <= 0 {
		return 0
	}
	promoted := 0
	for i := range samples {
		if samples[i].detailedFace == nil {
			continue
		}
		candidate := samples[i].detailedFace
		halo := 0
		if allowEdgeHalo {
			halo = cropW / 3
		}
		if candidate.CenterX < samples[i].point.X-halo ||
			candidate.CenterX > samples[i].point.X+cropW+halo {
			continue
		}
		if samples[i].face == nil || candidate.Quality > samples[i].face.Quality {
			copy := *candidate
			samples[i].face = &copy
			promoted++
		}
	}
	return promoted
}

// correctSmartCropHeadTracks joins repeated conservative head/reclining edge
// guards across a stationary detector gap. Two anchors are required and their
// positions must agree; this is intentionally narrower than generic motion
// tracking and cannot create a new cross-frame traversal.
func correctSmartCropHeadTracks(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 2 || srcW <= cropW || cropW <= 0 {
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
			if samples[i].headTracked {
				anchors = append(anchors, i)
			}
		}
		for a := 0; a+1 < len(anchors); a++ {
			left, right := anchors[a], anchors[a+1]
			if samples[right].point.AtMs-samples[left].point.AtMs > smartCropV2MaxGapMs ||
				absInt(samples[left].headTrackX-samples[right].headTrackX) > cropW/4 {
				continue
			}
			leftPoint := cropPathPoint{AtMs: samples[left].point.AtMs, X: samples[left].headTrackX}
			rightPoint := cropPathPoint{AtMs: samples[right].point.AtMs, X: samples[right].headTrackX}
			for i := left; i <= right; i++ {
				x := interpolateSmartCropStillX(leftPoint, rightPoint, samples[i].point.AtMs)
				x = clampInt(roundEven(x), 0, srcW-cropW)
				if samples[i].point.X != x {
					samples[i].point.X = x
					corrected++
				}
				samples[i].headTracked = true
				samples[i].headTrackX = x
			}
		}
		if len(anchors) >= 2 {
			first, second := anchors[0], anchors[1]
			if absInt(samples[first].headTrackX-samples[second].headTrackX) <= cropW/4 {
				for i := first - 1; i >= sceneStart && samples[first].point.AtMs-samples[i].point.AtMs <= 2500; i-- {
					x := clampInt(roundEven(samples[first].headTrackX), 0, srcW-cropW)
					if samples[i].point.X != x {
						samples[i].point.X = x
						corrected++
					}
					samples[i].headTracked, samples[i].headTrackX = true, x
				}
			}
			last, previous := anchors[len(anchors)-1], anchors[len(anchors)-2]
			if absInt(samples[last].headTrackX-samples[previous].headTrackX) <= cropW/4 {
				for i := last + 1; i < sceneEnd && samples[i].point.AtMs-samples[last].point.AtMs <= smartCropV2MaxGapMs; i++ {
					x := clampInt(roundEven(samples[last].headTrackX), 0, srcW-cropW)
					if samples[i].point.X != x {
						samples[i].point.X = x
						corrected++
					}
					samples[i].headTracked, samples[i].headTrackX = true, x
				}
			}
		}
		sceneStart = sceneEnd
	}
	return corrected
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
	corrected := correctSmartCropSingleFaceAlternatingFallbacks(samples, srcW, cropW)
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
			latePoseRelease := samples[left].face != nil && samples[left].face.Quality < 20 &&
				samples[right].face != nil && samples[right].face.Quality >= 20 &&
				samples[right].point.AtMs-samples[left].point.AtMs > 2500 &&
				absInt(samples[right].point.X-samples[left].point.X) > cropW/3
			earlyPoseEntry := samples[left].face != nil && samples[left].face.Quality >= 20 &&
				samples[right].face != nil && samples[right].face.Quality < 20 &&
				samples[right].point.AtMs-samples[left].point.AtMs > 2500 &&
				absInt(samples[right].point.X-samples[left].point.X) > cropW/3
			handoffAt := samples[right].point.AtMs - 2500
			for i := left + 1; i < right; i++ {
				if samples[i].face != nil {
					continue
				}
				x := 0
				if latePoseRelease && samples[i].point.AtMs <= handoffAt {
					x = samples[left].point.X
				} else if latePoseRelease {
					x = interpolateSmartCropStillX(
						cropPathPoint{AtMs: handoffAt, X: samples[left].point.X},
						samples[right].point, samples[i].point.AtMs)
				} else if earlyPoseEntry {
					span := float64(samples[right].point.AtMs - samples[left].point.AtMs)
					progress := float64(samples[i].point.AtMs-samples[left].point.AtMs) / span
					eased := 1 - (1-progress)*(1-progress)
					x = samples[left].point.X + int(math.Round(eased*float64(samples[right].point.X-samples[left].point.X)))
				} else {
					x = interpolateSmartCropStillX(samples[left].point, samples[right].point, samples[i].point.AtMs)
				}
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
			leadLimit := int64(2500)
			if samples[first].face.Quality >= 20 &&
				samples[second].face.Quality >= 20 &&
				absInt(samples[first].point.X-samples[second].point.X) <= cropW/12 {
				leadLimit = 5_000
			}
			for i := first - 1; i >= sceneStart; i-- {
				if samples[first].point.AtMs-samples[i].point.AtMs > leadLimit ||
					samples[i].motionTracked {
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
			strongStableTail := stableTail &&
				samples[last].face.Quality >= 20 &&
				samples[previous].face.Quality >= 20
			carryLimit := smartCropV2MaxGapMs
			maxTailDrift := cropW / 4
			if strongStableTail {
				// Several strong upright anchors can disappear together when a
				// subject slowly turns into profile. Allow the final dense
				// tracking interval to outlive the nominal storyboard gap, but
				// hold it much closer to the last confirmed face.
				carryLimit = 15_000
				maxTailDrift = cropW / 8
			}
			// A face commonly disappears when it turns into profile or becomes
			// horizontal. Hold the last human anchor for one bounded storyboard
			// gap instead of snapping to room saliency. A settled track may keep
			// its small velocity; after a large move we hold position rather than
			// extrapolating blindly. Any motion-certified sample stops the carry.
			for i := last + 1; i < sceneEnd; i++ {
				if samples[i].point.AtMs-samples[last].point.AtMs > carryLimit || samples[i].motionTracked {
					break
				}
				anchorDelta := samples[last].point.X - samples[previous].point.X
				releaseDelta := samples[i].point.X - samples[last].point.X
				weakDirectionalRelease := samples[last].face.Quality < 20 &&
					samples[previous].face.Quality < 20 &&
					absInt(anchorDelta) >= maxInt(12, cropW/40) &&
					absInt(releaseDelta) > cropW/3 &&
					(anchorDelta < 0) == (releaseDelta < 0)
				if weakDirectionalRelease {
					// Two weak profile hits followed by a much larger move in
					// the same direction are a hand-off, not a detector gap.
					// Keeping the weak anchor here strands the crop on the old
					// pose while the person reclines across the frame.
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
					x = clampInt(x, samples[last].point.X-maxTailDrift, samples[last].point.X+maxTailDrift)
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

// correctSmartCropSingleFaceAlternatingFallbacks handles a close profile or
// sideways head that the CPU cascade detects only once. In that failure mode,
// generic saliency alternates every few frames between the real head and one
// extremely stable empty-room position. A single face anchor alone is not
// normally enough to freeze a reel, so this correction additionally requires:
//
//   - at least three repeated non-face samples beside that anchor;
//   - a tight, clearly separated fallback cluster;
//   - repeated alternation between both clusters; and
//   - no motion-certified sample anywhere in the scene.
//
// A genuine traversal normally produces motion evidence or one directional
// cluster transition and is therefore left untouched.
func correctSmartCropSingleFaceAlternatingFallbacks(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 8 || srcW <= cropW || cropW <= 0 {
		return 0
	}
	corrected := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		anchor := -1
		motion := false
		for i := sceneStart; i < sceneEnd; i++ {
			if samples[i].motionTracked {
				motion = true
			}
			if samples[i].face == nil {
				continue
			}
			if anchor >= 0 {
				anchor = -2
				break
			}
			anchor = i
		}
		if motion || anchor < 0 || samples[anchor].face.Quality < 8 {
			sceneStart = sceneEnd
			continue
		}

		anchorX := containSmartCropFaceX(samples[anchor].point.X, *samples[anchor].face, srcW, cropW)
		radius := maxInt(48, cropW/3)
		support := make([]int, 0, sceneEnd-sceneStart)
		fallback := make([]int, 0, sceneEnd-sceneStart)
		firstSupportAt, lastSupportAt := int64(0), int64(0)
		firstSupportSet := false
		switches := 0
		previousSupport := false
		havePrevious := false
		for i := sceneStart; i < sceneEnd; i++ {
			isSupport := absInt(samples[i].point.X-anchorX) <= radius
			if isSupport {
				support = append(support, samples[i].point.X)
				if !firstSupportSet {
					firstSupportAt = samples[i].point.AtMs
					firstSupportSet = true
				}
				lastSupportAt = samples[i].point.AtMs
			} else {
				fallback = append(fallback, samples[i].point.X)
			}
			if havePrevious && isSupport != previousSupport {
				switches++
			}
			previousSupport, havePrevious = isSupport, true
		}
		if len(support) < 4 || len(fallback) < 4 || switches < 4 ||
			lastSupportAt-firstSupportAt < 5_000 {
			sceneStart = sceneEnd
			continue
		}
		sort.Ints(support)
		sort.Ints(fallback)
		supportLo, supportHi := support[0], support[len(support)-1]
		fallbackLo, fallbackHi := fallback[0], fallback[len(fallback)-1]
		supportX := support[len(support)/2]
		fallbackX := fallback[len(fallback)/2]
		if supportHi-supportLo > cropW/3 ||
			fallbackHi-fallbackLo > maxInt(32, cropW/8) ||
			absInt(supportX-fallbackX) < cropW/3 {
			sceneStart = sceneEnd
			continue
		}
		x := containSmartCropFaceX(supportX, *samples[anchor].face, srcW, cropW)
		x = clampInt(roundEven(x), 0, srcW-cropW)
		for i := sceneStart; i < sceneEnd; i++ {
			if samples[i].point.X != x {
				samples[i].point.X = x
				corrected++
			}
			if samples[i].face == nil {
				samples[i].faceTracked = true
			}
		}
		sceneStart = sceneEnd
	}
	return corrected
}

// constrainSmartCropPathToFaceTracks reapplies only the safety constraints
// that temporal smoothing is not allowed to violate. Smoothing remains free to
// remove detector jitter, but it cannot pull a confirmed face back across an
// edge or move an interpolated dropout track more than a small dead zone.
func constrainSmartCropPathToFaceTracks(path []cropPathPoint, samples []smartCropV2Sample, srcW, cropW int) []cropPathPoint {
	if len(path) < 2 || len(samples) == 0 || srcW <= cropW || cropW <= 0 {
		return path
	}
	byTime := make(map[int64]int, len(path))
	for i := range path {
		byTime[path[i].AtMs] = i
	}
	pathXAt := func(atMs int64) int {
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
	for _, sample := range samples {
		if sample.face == nil && !sample.faceTracked && !sample.headTracked {
			continue
		}
		current := pathXAt(sample.point.AtMs)
		desired := current
		if sample.face != nil {
			desired = containSmartCropFaceX(current, *sample.face, srcW, cropW)
		} else {
			maxDrift := maxInt(16, cropW/20)
			desired = clampInt(current, sample.point.X-maxDrift, sample.point.X+maxDrift)
			desired = clampInt(roundEven(desired), 0, srcW-cropW)
		}
		if desired == current {
			continue
		}
		if i, ok := byTime[sample.point.AtMs]; ok {
			path[i].X = desired
			continue
		}
		path = append(path, cropPathPoint{AtMs: sample.point.AtMs, X: desired, Cut: sample.point.Cut})
	}
	sort.SliceStable(path, func(i, j int) bool { return path[i].AtMs < path[j].AtMs })
	return path
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

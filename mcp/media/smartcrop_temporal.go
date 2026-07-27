package main

import (
	"image"
	"math"
	"sort"
)

const (
	smartCropTemporalMaxSamples              = 9
	smartCropTemporalMinSamples              = 3
	smartCropSceneCutThreshold               = 0.28
	smartCropTemporalMinConcentration        = 0.60
	smartCropTemporalMinMeanActivity         = 0.50
	smartCropTemporalMinActiveFraction       = 0.015
	smartCropTemporalDenseMinConcentration   = 0.50
	smartCropTemporalDenseMinMeanActivity    = 2.0
	smartCropTemporalDenseMinActiveFraction  = 0.04
	smartCropTemporalStaticMinConcentration  = 0.98
	smartCropTemporalStaticMinMeanActivity   = 0.25
	smartCropTemporalStaticMinActiveFraction = 0.008
	smartCropTemporalStaticMinAnchorScore    = 300.0
	smartCropTemporalStaticMaxAnchorScore    = 800.0
)

type smartCropTemporalResult struct {
	X               int
	Samples         int
	Concentration   float64
	MeanActivity    float64
	ActiveFraction  float64
	SubjectAnchored bool
	StaticAnchored  bool
	AnchorCoverage  int
	AnchorScore     float64
	AnchorX         int
	AnchorAligned   bool
}

// smartCropSceneSamples keeps temporal evidence inside the scene containing
// the requested timestamp. A median built across an edit would describe two
// unrelated compositions and can manufacture a false foreground signal.
func smartCropSceneSamples(samples []smartCropV2Sample, focusMs int64) []smartCropV2Sample {
	if len(samples) == 0 {
		return nil
	}
	nearest := 0
	for i := 1; i < len(samples); i++ {
		if absInt64(samples[i].point.AtMs-focusMs) < absInt64(samples[nearest].point.AtMs-focusMs) {
			nearest = i
		}
	}
	left, right := nearest, nearest
	for left > 0 && sceneCutScore(samples[left-1].img, samples[left].img) < smartCropSceneCutThreshold {
		left--
	}
	for right+1 < len(samples) && sceneCutScore(samples[right].img, samples[right+1].img) < smartCropSceneCutThreshold {
		right++
	}
	return samples[left : right+1]
}

// temporalSubjectConsensus finds motion that remains concentrated in one
// portrait-width region. Per-pixel temporal medians remove static backgrounds;
// the remaining activity is projected into columns and scored by crop window.
// The confidence fields are deliberately exposed so the override policy stays
// independently testable and conservative.
func temporalSubjectConsensus(samples []smartCropV2Sample, srcW, cropW int) (smartCropTemporalResult, bool) {
	if len(samples) < smartCropTemporalMinSamples || srcW <= 0 || cropW <= 0 || cropW >= srcW {
		return smartCropTemporalResult{}, false
	}
	// Temporal correction addresses narrow social reframing. Wider crops have
	// enough context for the normal saliency path and should not be moved by a
	// foreground heuristic; this mirrors the guard on the still-image subject
	// detector and prevents changes to landscape/square outputs.
	if float64(srcW)/float64(cropW) < 1.8 {
		return smartCropTemporalResult{}, false
	}
	if len(samples) > smartCropTemporalMaxSamples {
		samples = evenlyCapSmartCropSamples(samples, smartCropTemporalMaxSamples)
	}
	if samples[0].img == nil {
		return smartCropTemporalResult{}, false
	}
	firstBounds := samples[0].img.Bounds()
	if firstBounds.Dx() <= 0 || firstBounds.Dy() <= 0 {
		return smartCropTemporalResult{}, false
	}
	w := minInt(320, firstBounds.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(firstBounds.Dy())/float64(firstBounds.Dx()))))
	pixels := make([][]uint8, len(samples))
	for i, sample := range samples {
		if sample.img == nil {
			return smartCropTemporalResult{}, false
		}
		pixels[i] = normalizedSmartCropRGB(sample.img, w, h)
	}

	columns := make([]float64, w)
	var activeTotal float64
	var activeCount int
	observations := len(samples) * w * h
	topRows := int(math.Round(float64(h) * 0.07))
	bottomRows := int(math.Round(float64(h) * 0.03))
	for y := 0; y < h; y++ {
		yWeight := 1.0
		if y < topRows {
			yWeight = 0.3
		} else if y >= h-bottomRows {
			yWeight = 0.6
		}
		for x := 0; x < w; x++ {
			pixel := (y*w + x) * 3
			var rs, gs, bs [smartCropTemporalMaxSamples]uint8
			for i := range pixels {
				rs[i] = pixels[i][pixel]
				gs[i] = pixels[i][pixel+1]
				bs[i] = pixels[i][pixel+2]
			}
			medianR := smartCropMedian(rs, len(pixels))
			medianG := smartCropMedian(gs, len(pixels))
			medianB := smartCropMedian(bs, len(pixels))
			for i := range pixels {
				diff := (math.Abs(float64(pixels[i][pixel])-medianR) +
					math.Abs(float64(pixels[i][pixel+1])-medianG) +
					math.Abs(float64(pixels[i][pixel+2])-medianB)) / 3.0
				active := math.Max(diff-8.0, 0)
				activeTotal += active
				if active > 4.0 {
					activeCount++
				}
				columns[x] += math.Sqrt(active) * yWeight
			}
		}
	}

	// A nine-column box filter matches the real storyboard audit and makes
	// isolated compression noise irrelevant. Missing edge columns are zeros.
	smoothed := make([]float64, w)
	for x := 0; x < w; x++ {
		for sx := maxInt(0, x-4); sx <= minInt(w-1, x+4); sx++ {
			smoothed[x] += columns[sx] / 9.0
		}
	}
	var total float64
	for _, score := range smoothed {
		total += score
	}
	normalizedCropW := clampInt(int(math.Round(float64(w)*float64(cropW)/float64(srcW))), 1, w)
	bestStart := 0
	var bestScore float64
	scores := make([]float64, w-normalizedCropW+1)
	for start := 0; start+normalizedCropW <= w; start++ {
		var score float64
		for x := start; x < start+normalizedCropW; x++ {
			score += smoothed[x]
		}
		if score > bestScore {
			bestScore = score
			bestStart = start
		}
		scores[start] = score
	}
	if total <= 0 || observations <= 0 {
		return smartCropTemporalResult{}, false
	}

	// Coverage alone has a broad plateau whenever a subject is narrower than
	// the requested crop. Picking the first maximum puts that subject against
	// the right edge. Among windows retaining at least 99% of the best coverage,
	// prefer the one whose activity is balanced around the crop centre.
	bestBalance := math.Inf(1)
	for start, score := range scores {
		if score < bestScore*0.99 || score <= 0 {
			continue
		}
		center := float64(start) + float64(normalizedCropW-1)/2.0
		var distance float64
		for x := start; x < start+normalizedCropW; x++ {
			distance += smoothed[x] * math.Abs(float64(x)-center)
		}
		balance := distance / score
		if balance < bestBalance {
			bestBalance = balance
			bestStart = start
		}
	}

	anchorCenter, anchorCoverage, anchorScore, anchored := persistentWarmSubjectCenter(
		pixels, w, h, bestStart, normalizedCropW,
	)
	anchorStart := clampInt(int(math.Round(anchorCenter-float64(normalizedCropW)/2.0)), 0, w-normalizedCropW)
	anchorAligned := absInt(anchorStart-bestStart) <= normalizedCropW*3/5
	if anchored {
		// A recurring skin-colored component certifies the temporal region, but
		// does not position it. A face, hand, or arm can be the strongest warm
		// component and centering that fragment would move the torso off-center.
		anchored = anchorAligned
	}
	result := smartCropTemporalResult{
		X:               clampInt(roundEven(int(math.Round(float64(bestStart)*float64(srcW)/float64(w)))), 0, srcW-cropW),
		Samples:         len(samples),
		Concentration:   bestScore / total,
		MeanActivity:    activeTotal / float64(observations),
		ActiveFraction:  float64(activeCount) / float64(observations),
		SubjectAnchored: anchored,
		AnchorCoverage:  anchorCoverage,
		AnchorScore:     anchorScore,
		AnchorX:         clampInt(roundEven(int(math.Round(float64(anchorStart)*float64(srcW)/float64(w)))), 0, srcW-cropW),
		AnchorAligned:   anchorAligned,
	}
	return result, true
}

func applySmartCropTemporalOverride(baseX int, result smartCropTemporalResult, cropW, srcW int) (int, bool) {
	if !smartCropTemporalResultConfident(result) || absInt(baseX-result.X) <= cropW/8 {
		return baseX, false
	}
	if result.StaticAnchored && !result.AnchorAligned {
		// Static colour evidence is intentionally only an edge-recovery signal.
		// The candidate person's centre must already be inside the saliency crop
		// and stranded in its outer fifth. Otherwise a warm lamp or chair across
		// the room could pull a perfectly usable frozen crop to the wrong side.
		subjectCenter := result.X + cropW/2
		edgeBand := maxInt(16, cropW/5)
		if subjectCenter < baseX || subjectCenter > baseX+cropW ||
			(subjectCenter-baseX > edgeBand && baseX+cropW-subjectCenter > edgeBand) {
			return baseX, false
		}
	}
	return clampInt(roundEven(result.X), 0, srcW-cropW), true
}

func smartCropTemporalResultConfident(result smartCropTemporalResult) bool {
	if result.Samples < smartCropTemporalMinSamples {
		return false
	}
	// A recurring warm component is only a refinement of concentrated temporal
	// evidence. Without this gate, repeated room features (for example a lamp
	// or chair) can look person-like across an otherwise unfocused scene.
	if result.SubjectAnchored &&
		result.AnchorCoverage >= (result.Samples+1)/2 &&
		result.Concentration >= smartCropTemporalMinConcentration {
		return true
	}
	// Completely motionless footage has no temporal activity to concentrate.
	// In that case, accept a crop only when a strict person-like warm component
	// recurs across at least two thirds of the sampled frames.
	if result.StaticAnchored &&
		result.AnchorCoverage >= maxInt(3, (2*result.Samples+2)/3) &&
		result.AnchorScore >= smartCropTemporalStaticMinAnchorScore &&
		result.AnchorScore < smartCropTemporalStaticMaxAnchorScore {
		return true
	}
	// Strong, dense motion can extend beyond a portrait window when a person
	// raises an arm or casts a moving shadow. Accept a slightly lower spatial
	// concentration only when both activity measures are decisively high. This
	// keeps broad exposure changes and weak compression motion rejected.
	if result.Concentration >= smartCropTemporalDenseMinConcentration &&
		result.MeanActivity >= smartCropTemporalDenseMinMeanActivity &&
		result.ActiveFraction >= smartCropTemporalDenseMinActiveFraction {
		return true
	}
	// A nearly motionless person may only produce subtle compression-level
	// activity. Accept that signal only when it is extremely concentrated and
	// backed by a recurring warm human component. The anchor requirement keeps
	// isolated codec noise from moving a crop.
	if result.Concentration >= smartCropTemporalStaticMinConcentration &&
		result.MeanActivity >= smartCropTemporalStaticMinMeanActivity &&
		result.ActiveFraction >= smartCropTemporalStaticMinActiveFraction &&
		result.AnchorCoverage >= (result.Samples+1)/2 &&
		result.AnchorScore >= smartCropTemporalStaticMinAnchorScore {
		return true
	}
	// Some production clips have a nearly motionless, dark-clothed person:
	// only the face/legs contribute warm evidence and codec activity is below
	// the normal static-motion floor. An extremely concentrated region with a
	// modest recurring human component is still safe at this lower floor.
	if result.Concentration >= 0.98 &&
		result.MeanActivity >= 0.10 &&
		result.MeanActivity < 0.20 &&
		result.ActiveFraction >= 0.006 &&
		result.ActiveFraction < smartCropTemporalStaticMinActiveFraction &&
		result.AnchorCoverage >= maxInt(3, (2*result.Samples+2)/3) &&
		result.AnchorScore >= smartCropTemporalStaticMinAnchorScore &&
		result.AnchorScore < smartCropTemporalStaticMaxAnchorScore {
		return true
	}
	return result.Concentration >= smartCropTemporalMinConcentration &&
		result.MeanActivity >= smartCropTemporalMinMeanActivity &&
		result.ActiveFraction >= smartCropTemporalMinActiveFraction
}

type warmSubjectComponent struct {
	centerX float64
	score   float64
	minX    int
	maxX    int
	minY    int
	maxY    int
	area    int
}

// persistentWarmSubjectCenter finds a person-like warm component that recurs
// near the temporal activity region. It is intentionally stricter than the
// legacy single-frame warm-pixel guard: broad walls/blinds, edge-touching
// strips, tiny fragments, and low-area furniture highlights are rejected.
func persistentWarmSubjectCenter(
	frames [][]uint8,
	w, h, activityStart, cropW int,
) (center float64, coverage int, normalizedScore float64, ok bool) {
	if len(frames) < smartCropTemporalMinSamples || w <= 0 || h <= 0 || cropW <= 0 {
		return 0, 0, 0, false
	}
	components := make([][]warmSubjectComponent, len(frames))
	margin := cropW / 4
	regionMin := maxInt(0, activityStart-margin)
	regionMax := minInt(w-1, activityStart+cropW+margin)
	for i, frame := range frames {
		components[i] = warmSubjectComponents(frame, w, h, cropW, regionMin, regionMax)
	}

	radius := maxInt(6, w/16)
	requiredCoverage := (len(frames) + 1) / 2
	bestTotal := 0.0
	bestCoverage := 0
	bestCenter := 0.0
	for _, frameComponents := range components {
		for _, seed := range frameComponents {
			total := 0.0
			weightedX := 0.0
			matched := 0
			for _, candidates := range components {
				best := warmSubjectComponent{}
				for _, candidate := range candidates {
					if math.Abs(candidate.centerX-seed.centerX) <= float64(radius) && candidate.score > best.score {
						best = candidate
					}
				}
				if best.score > 0 {
					matched++
					total += best.score
					weightedX += best.centerX * best.score
				}
			}
			if matched < requiredCoverage || total <= 0 {
				continue
			}
			if total > bestTotal || (total == bestTotal && matched > bestCoverage) {
				bestTotal = total
				bestCoverage = matched
				bestCenter = weightedX / total
			}
		}
	}
	if bestCoverage < requiredCoverage || bestTotal <= 0 {
		return 0, bestCoverage, 0, false
	}
	// Scores are measured on the normalized working frame. Scale the minimum
	// so unusually small derivations use the same evidence requirement.
	normalizedScore = bestTotal / float64(len(frames)) * (320.0 * 180.0 / float64(w*h))
	if normalizedScore < 800.0 {
		return bestCenter, bestCoverage, normalizedScore, false
	}
	return bestCenter, bestCoverage, normalizedScore, true
}

func staticWarmSubjectConsensus(samples []smartCropV2Sample, srcW, cropW int) (smartCropTemporalResult, bool) {
	if len(samples) < smartCropTemporalMinSamples || srcW <= cropW || cropW <= 0 ||
		float64(srcW)/float64(cropW) < 1.8 {
		return smartCropTemporalResult{}, false
	}
	if len(samples) > smartCropTemporalMaxSamples {
		samples = evenlyCapSmartCropSamples(samples, smartCropTemporalMaxSamples)
	}
	if samples[0].img == nil {
		return smartCropTemporalResult{}, false
	}
	bounds := samples[0].img.Bounds()
	if bounds.Dx() <= 0 || bounds.Dy() <= 0 {
		return smartCropTemporalResult{}, false
	}
	w := minInt(320, bounds.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(bounds.Dy())/float64(bounds.Dx()))))
	pixels := make([][]uint8, len(samples))
	for i, sample := range samples {
		if sample.img == nil {
			return smartCropTemporalResult{}, false
		}
		pixels[i] = normalizedSmartCropRGB(sample.img, w, h)
	}
	center, coverage, score, _ := persistentWarmSubjectCenter(pixels, w, h, 0, w)
	requiredCoverage := maxInt(3, (2*len(samples)+2)/3)
	if coverage < requiredCoverage || score < smartCropTemporalStaticMinAnchorScore ||
		score >= smartCropTemporalStaticMaxAnchorScore {
		return smartCropTemporalResult{}, false
	}
	normalizedCropW := clampInt(int(math.Round(float64(w)*float64(cropW)/float64(srcW))), 1, w)
	start := clampInt(int(math.Round(center-float64(normalizedCropW)/2)), 0, w-normalizedCropW)
	return smartCropTemporalResult{
		X:              clampInt(roundEven(int(math.Round(float64(start)*float64(srcW)/float64(w)))), 0, srcW-cropW),
		Samples:        len(samples),
		StaticAnchored: true,
		AnchorCoverage: coverage,
		AnchorScore:    score,
	}, true
}

// bestSmartCropTemporalConsensus distinguishes "a temporal score could be
// calculated" from "that score is safe to use". A recurring static human
// anchor is preferred when unanchored activity points elsewhere and the
// evidence path is not a genuine tracked traversal. This covers people who
// pause, wear dark clothes, or move too little for background subtraction.
func bestSmartCropTemporalConsensus(samples []smartCropV2Sample, srcW, cropW int) (smartCropTemporalResult, bool) {
	temporal, temporalOK := temporalSubjectConsensus(samples, srcW, cropW)
	static, staticOK := staticWarmSubjectConsensus(samples, srcW, cropW)
	staticConfident := staticOK && smartCropTemporalResultConfident(static)
	if temporalOK && !smartCropTemporalResultConfident(temporal) && temporal.AnchorAligned &&
		temporal.AnchorCoverage >= maxInt(3, (2*temporal.Samples+2)/3) &&
		temporal.AnchorScore >= smartCropTemporalStaticMinAnchorScore &&
		temporal.AnchorScore < smartCropTemporalStaticMaxAnchorScore {
		anchored := temporal
		anchored.X = temporal.AnchorX
		anchored.StaticAnchored = true
		return anchored, true
	}
	if staticConfident {
		if !temporalOK || !smartCropTemporalResultConfident(temporal) {
			return static, true
		}
		if !temporal.SubjectAnchored &&
			absInt(temporal.X-static.X) > cropW/4 &&
			!smartCropSceneHasSustainedTraversal(samples, cropW) {
			return static, true
		}
	}
	if temporalOK {
		return temporal, true
	}
	if staticOK {
		return static, true
	}
	return smartCropTemporalResult{}, false
}

// smartCropUnanchoredEdgeFurnitureFallbackX rejects a repeated frame-edge
// crop whose only persistent "subject" is a broad, low warm object such as a
// dining table or couch. This is deliberately a scene-level decision:
//
//   - at least three frames and two thirds of the scene must agree;
//   - there may be no face, motion, head, foreground or temporal track;
//   - the crop must be parked at the same physical frame edge; and
//   - a person-sized portrait window must be dominated by a wide lower-frame
//     component rather than an upright subject.
//
// A single still cannot satisfy the gate. A real edge subject with any direct
// human/motion evidence keeps its crop. The safe fallback is geometric centre,
// which is preferable to returning a portrait containing only furniture.
func smartCropUnanchoredEdgeFurnitureFallbackX(samples []smartCropV2Sample, srcW, cropW int) (int, bool) {
	if len(samples) < 3 || srcW <= cropW || cropW <= 0 {
		return 0, false
	}
	maxX := srcW - cropW
	edgeLimit := maxInt(24, cropW/12)
	left, right := 0, 0
	for i := range samples {
		sample := &samples[i]
		if sample.face != nil || sample.detailedFace != nil || sample.faceTracked ||
			sample.headTracked || sample.backgroundTracked || sample.motionTracked ||
			sample.temporalTrack || sample.point.Cut {
			return 0, false
		}
		switch {
		case sample.point.X <= edgeLimit &&
			smartCropEdgeWindowDominatedByLowFurniture(sample.img, sample.point.X, srcW, cropW):
			left++
		case sample.point.X >= maxX-edgeLimit &&
			smartCropEdgeWindowDominatedByLowFurniture(sample.img, sample.point.X, srcW, cropW):
			right++
		}
	}
	required := maxInt(3, (2*len(samples)+2)/3)
	if maxInt(left, right) < required {
		return 0, false
	}
	return clampInt(roundEven(maxX/2), 0, maxX), true
}

func smartCropEdgeWindowDominatedByLowFurniture(img image.Image, srcX, srcW, cropW int) bool {
	if img == nil || srcW <= cropW || cropW <= 0 {
		return false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return false
	}
	w := minInt(320, b.Dx())
	h := maxInt(1, int(math.Round(float64(w)*float64(b.Dy())/float64(b.Dx()))))
	thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(w)/float64(srcW))), 1, w)
	start := clampInt(int(math.Round(float64(srcX)*float64(w)/float64(srcW))), 0, w-thumbCropW)
	end := start + thumbCropW - 1
	components := warmSubjectComponents(normalizedSmartCropRGB(img, w, h), w, h, thumbCropW, start, end)
	for _, component := range components {
		componentW := component.maxX - component.minX + 1
		componentH := component.maxY - component.minY + 1
		if component.centerX < float64(start) || component.centerX > float64(end) ||
			componentW < thumbCropW/2 || componentH < h/6 ||
			component.minY < h/2 || component.maxY < h*4/5 ||
			component.score < 600.0*float64(w*h)/(320.0*180.0) {
			continue
		}
		return true
	}
	return false
}

func correctSmartCropUnanchoredEdgeFurnitureScenes(samples []smartCropV2Sample, srcW, cropW int) int {
	corrected := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		if x, ok := smartCropUnanchoredEdgeFurnitureFallbackX(samples[sceneStart:sceneEnd], srcW, cropW); ok {
			for i := sceneStart; i < sceneEnd; i++ {
				if samples[i].point.X != x {
					samples[i].point.X = x
					corrected++
				}
				samples[i].temporalTrack = true
			}
		}
		sceneStart = sceneEnd
	}
	return corrected
}

func warmSubjectComponents(frame []uint8, w, h, cropW, regionMin, regionMax int) []warmSubjectComponent {
	if len(frame) != w*h*3 {
		return nil
	}
	mask := make([]bool, w*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) * 3
			mask[y*w+x] = strictWarmSubjectPixel(int(frame[idx]), int(frame[idx+1]), int(frame[idx+2]))
		}
	}
	visited := make([]bool, len(mask))
	queue := make([]int, 0, len(mask)/8)
	components := make([]warmSubjectComponent, 0)
	maxComponentW := maxInt(18, cropW*3/4)
	for start, active := range mask {
		if !active || visited[start] {
			continue
		}
		queue = queue[:0]
		queue = append(queue, start)
		visited[start] = true
		minX, maxX := start%w, start%w
		minY, maxY := start/w, start/w
		area := 0
		sumX := 0
		for head := 0; head < len(queue); head++ {
			pos := queue[head]
			x, y := pos%w, pos/w
			area++
			sumX += x
			minX, maxX = minInt(minX, x), maxInt(maxX, x)
			minY, maxY = minInt(minY, y), maxInt(maxY, y)
			for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
				for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
					next := ny*w + nx
					if mask[next] && !visited[next] {
						visited[next] = true
						queue = append(queue, next)
					}
				}
			}
		}
		componentW := maxX - minX + 1
		componentH := maxY - minY + 1
		centerX := float64(sumX) / float64(maxInt(1, area))
		if area < 20 || componentW > maxComponentW || componentH < 20 || componentH > h*8/10 ||
			minX <= 1 || maxX >= w-2 || minY > h*9/10 || maxY < h*15/100 ||
			centerX < float64(regionMin) || centerX > float64(regionMax) {
			continue
		}
		heightFactor := math.Min(float64(componentH)/60.0, 1.0)
		components = append(components, warmSubjectComponent{
			centerX: centerX,
			score:   float64(area) * (1.0 + heightFactor),
			minX:    minX,
			maxX:    maxX,
			minY:    minY,
			maxY:    maxY,
			area:    area,
		})
	}
	return components
}

func strictWarmSubjectPixel(r, g, b int) bool {
	if warmSubjectPixelWeight(r, g, b) == 0 {
		return false
	}
	cb := 128.0 - 0.168736*float64(r) - 0.331264*float64(g) + 0.5*float64(b)
	return cb >= 104.0
}

func normalizedSmartCropRGB(img image.Image, w, h int) []uint8 {
	b := img.Bounds()
	out := make([]uint8, w*h*3)
	if rgba, ok := img.(*image.RGBA); ok && b.Dx() == w && b.Dy() == h {
		for y := 0; y < h; y++ {
			src := rgba.PixOffset(b.Min.X, b.Min.Y+y)
			for x := 0; x < w; x++ {
				dst := (y*w + x) * 3
				out[dst] = rgba.Pix[src+x*4]
				out[dst+1] = rgba.Pix[src+x*4+1]
				out[dst+2] = rgba.Pix[src+x*4+2]
			}
		}
		return out
	}
	for y := 0; y < h; y++ {
		sy := b.Min.Y + (2*y+1)*b.Dy()/(2*h)
		if sy >= b.Max.Y {
			sy = b.Max.Y - 1
		}
		for x := 0; x < w; x++ {
			sx := b.Min.X + (2*x+1)*b.Dx()/(2*w)
			if sx >= b.Max.X {
				sx = b.Max.X - 1
			}
			r, g, bl, _ := img.At(sx, sy).RGBA()
			idx := (y*w + x) * 3
			out[idx] = uint8(r >> 8)
			out[idx+1] = uint8(g >> 8)
			out[idx+2] = uint8(bl >> 8)
		}
	}
	return out
}

func smartCropMedian(values [smartCropTemporalMaxSamples]uint8, n int) float64 {
	for i := 1; i < n; i++ {
		v := values[i]
		j := i - 1
		for ; j >= 0 && values[j] > v; j-- {
			values[j+1] = values[j]
		}
		values[j+1] = v
	}
	if n%2 == 1 {
		return float64(values[n/2])
	}
	return (float64(values[n/2-1]) + float64(values[n/2])) / 2.0
}

func evenlyCapSmartCropSamples(samples []smartCropV2Sample, limit int) []smartCropV2Sample {
	if len(samples) <= limit {
		return samples
	}
	out := make([]smartCropV2Sample, 0, limit)
	for i := 0; i < limit; i++ {
		idx := int(math.Round(float64(i) * float64(len(samples)-1) / float64(limit-1)))
		out = append(out, samples[idx])
	}
	return out
}

func correctSmartCropReelTemporalOutliers(samples []smartCropV2Sample, srcW, cropW int) int {
	corrected := 0
	for start := 0; start < len(samples); {
		end := start + 1
		for end < len(samples) && !samples[end].point.Cut {
			end++
		}
		scene := samples[start:end]
		if smartCropSceneHasBackgroundSubjectState(scene, cropW) {
			// A distributed fixed-camera model has independently located a
			// repeated foreground state. Scene-wide temporal activity is broader
			// and can center the movement (torso/arm) instead of the final person,
			// flattening a valid upright-to-reclined transition. Preserve the
			// background-anchored state; face and motion anchors still run later.
			start = end
			continue
		}
		if result, ok := temporalSubjectConsensus(scene, srcW, cropW); ok {
			if n := correctSmartCropAbruptStaticClusters(scene, result, srcW, cropW); n > 0 {
				corrected += n
				start = end
				continue
			}
		}
		// Once adaptive sampling shows the crop occupying substantially
		// different regions for a meaningful fraction of the scene, this is a
		// tracked traversal rather than isolated saliency drift. A scene-wide
		// consensus would average away the timeline and park the crop between
		// the subject's old and new positions.
		if smartCropSceneHasSustainedTraversal(samples[start:end], cropW) {
			start = end
			continue
		}
		result, ok := bestSmartCropTemporalConsensus(samples[start:end], srcW, cropW)
		if ok {
			if !smartCropTemporalResultConfident(result) {
				start = end
				continue
			}
			if result.StaticAnchored && !result.AnchorAligned &&
				!smartCropStaticCandidateTouchesSceneSubject(result, samples[start:end], cropW) {
				// A repeated warm lamp, cushion, or chair may satisfy the strict
				// static-person score while sitting across the room. Static colour
				// evidence is only allowed to rescue a subject already stranded at
				// the edge of most saliency crops; it cannot replace a stable crop
				// with a disconnected candidate. This mirrors the still-image gate.
				start = end
				continue
			}
			deadZone := cropW / 8
			left, right, aligned := 0, 0, 0
			for i := start; i < end; i++ {
				switch {
				case samples[i].point.X < result.X-deadZone:
					left++
				case samples[i].point.X > result.X+deadZone:
					right++
				default:
					aligned++
				}
			}
			// A static-background failure pushes most saliency samples away
			// from the subject in the same direction. Opposing outliers mean
			// the subject is genuinely traversing the frame; keep the tracked
			// path rather than flattening it to one scene-wide consensus.
			// Four agreeing frames are the minimum for modifying a reel path.
			// Three-frame scenes are too short to distinguish a static saliency
			// miss from deliberate subject movement reliably.
			legacyDirection := smartCropTemporalLegacyCorrectionDirection(result, left, right, aligned, end-start)
			direction := legacyDirection
			if direction == 0 {
				direction = smartCropTemporalAnchoredCorrectionDirection(result, left, right, aligned, end-start)
			}
			// Static-anchor corrections and the new anchored-minority recovery
			// are authoritative. Legacy majority corrections still allow the
			// released face/head edge guard to refine reclining people.
			if direction != 0 && (result.StaticAnchored || legacyDirection == 0) {
				for i := start; i < end; i++ {
					samples[i].temporalTrack = true
				}
			}
			for i := start; direction != 0 && i < end; i++ {
				delta := samples[i].point.X - result.X
				if (direction < 0 && delta < -deadZone) || (direction > 0 && delta > deadZone) {
					samples[i].point.X = clampInt(roundEven(result.X), 0, srcW-cropW)
					corrected++
				}
			}
		}
		start = end
	}
	return corrected
}

func smartCropSceneHasBackgroundSubjectState(samples []smartCropV2Sample, cropW int) bool {
	if len(samples) < 4 || cropW <= 0 {
		return false
	}
	xs := make([]int, 0, len(samples))
	for _, sample := range samples {
		if sample.backgroundTracked {
			xs = append(xs, sample.point.X)
		}
	}
	if len(xs) < 4 {
		return false
	}
	sort.Ints(xs)
	if xs[len(xs)-1]-xs[0] > cropW/3 {
		return false
	}
	backgroundX := xs[len(xs)/2]
	for _, sample := range samples {
		if sample.face != nil {
			faceX := sample.face.CenterX - cropW/2
			if absInt(backgroundX-faceX) > cropW/3 {
				return true
			}
		}
		if sample.motionTracked && !sample.backgroundTracked &&
			absInt(backgroundX-sample.point.X) > cropW/3 {
			return true
		}
	}
	return false
}

func smartCropStaticCandidateTouchesSceneSubject(result smartCropTemporalResult, samples []smartCropV2Sample, cropW int) bool {
	if len(samples) == 0 || cropW <= 0 {
		return false
	}
	center := result.X + cropW/2
	edgeBand := maxInt(16, cropW/5)
	touches := 0
	for _, sample := range samples {
		left, right := sample.point.X, sample.point.X+cropW
		if center < left || center > right {
			continue
		}
		if center-left <= edgeBand || right-center <= edgeBand {
			touches++
		}
	}
	return touches >= maxInt(3, (len(samples)+1)/2)
}

// correctSmartCropIsolatedMotionBoundaryScenes rejects a single-frame motion
// ghost when the surrounding frames provide stronger containment evidence.
// A narrow crop can clamp to a frame edge for a mostly still person, while one
// transient shadow produces a confident motion window on the opposite side of
// that person. Propagating that lone motion point then follows the empty room.
//
// This recovery is deliberately narrow: the scene must contain exactly one
// motion-certified sample, most non-motion samples must be clamped to the same
// edge, and a tight interior crop cluster must bracket the motion sample in
// time and lie geometrically between the edge and the motion window. Genuine
// traversal, multiple moving subjects, and unbracketed transitions are left
// untouched.
func correctSmartCropIsolatedMotionBoundaryScenes(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 8 || srcW <= cropW || cropW <= 0 || float64(srcW)/float64(cropW) < 1.8 {
		return 0
	}
	corrected := 0
	for sceneStart := 0; sceneStart < len(samples); {
		sceneEnd := sceneStart + 1
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		corrected += correctSmartCropIsolatedMotionBoundaryScene(samples[sceneStart:sceneEnd], srcW, cropW)
		sceneStart = sceneEnd
	}
	return corrected
}

func correctSmartCropIsolatedMotionBoundaryScene(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 8 {
		return 0
	}
	motionIndex := -1
	for i, sample := range samples {
		if !sample.motionTracked {
			continue
		}
		if motionIndex >= 0 {
			return 0
		}
		motionIndex = i
	}
	if motionIndex < 0 {
		return 0
	}

	maxX := srcW - cropW
	edgeBand := maxInt(48, cropW/8)
	leftEdge, rightEdge := 0, 0
	for i, sample := range samples {
		if i == motionIndex {
			continue
		}
		if sample.point.X <= edgeBand {
			leftEdge++
		}
		if sample.point.X >= maxX-edgeBand {
			rightEdge++
		}
	}
	edgeX, edgeCount := 0, leftEdge
	leftBoundary := true
	if rightEdge > leftEdge {
		edgeX, edgeCount, leftBoundary = maxX, rightEdge, false
	}
	if edgeCount < maxInt(5, len(samples)/2) {
		return 0
	}

	// Find the largest tight interior cluster without allowing the dominant
	// boundary fallback or the isolated motion answer to vote for itself.
	radius := maxInt(72, cropW/8)
	clusters := make([]smartCropXCluster, 0, 3)
	clusterMembers := make([][]int, 0, 3)
	for i, sample := range samples {
		if i == motionIndex || sample.point.X <= edgeBand || sample.point.X >= maxX-edgeBand {
			continue
		}
		best, distance := -1, math.MaxFloat64
		for j, cluster := range clusters {
			d := math.Abs(float64(sample.point.X) - cluster.center)
			if d <= float64(radius) && d < distance {
				best, distance = j, d
			}
		}
		if best < 0 {
			clusters = append(clusters, smartCropXCluster{center: float64(sample.point.X), count: 1})
			clusterMembers = append(clusterMembers, []int{i})
			continue
		}
		cluster := &clusters[best]
		cluster.center = (cluster.center*float64(cluster.count) + float64(sample.point.X)) / float64(cluster.count+1)
		cluster.count++
		clusterMembers[best] = append(clusterMembers[best], i)
	}
	best := -1
	for i, cluster := range clusters {
		if best < 0 || cluster.count > clusters[best].count {
			best = i
		}
	}
	if best < 0 || clusters[best].count < 4 {
		return 0
	}
	interiorCount := 0
	for _, cluster := range clusters {
		interiorCount += cluster.count
	}
	if clusters[best].count*3 < interiorCount*2 {
		return 0
	}

	before, after := 0, 0
	for _, index := range clusterMembers[best] {
		if index < motionIndex {
			before++
		} else if index > motionIndex {
			after++
		}
	}
	if before < 2 || after < 2 {
		return 0
	}

	candidateX := clampInt(roundEven(int(math.Round(clusters[best].center))), 0, maxX)
	motionX := samples[motionIndex].point.X
	if leftBoundary {
		if candidateX <= edgeBand || motionX <= candidateX {
			return 0
		}
	} else if candidateX >= maxX-edgeBand || motionX >= candidateX {
		return 0
	}
	if absInt(candidateX-edgeX) < cropW/3 || absInt(candidateX-edgeX) > cropW ||
		absInt(motionX-candidateX) < cropW/3 || absInt(motionX-candidateX) > cropW {
		return 0
	}

	corrected := 0
	for i := range samples {
		if samples[i].point.X != candidateX {
			samples[i].point.X = candidateX
			corrected++
		}
		samples[i].temporalTrack = true
	}
	return corrected
}

type smartCropXCluster struct {
	center float64
	count  int
}

// correctSmartCropAbruptStaticClusters repairs a second low-motion failure
// shape: a scene has no motion-certified points and generic saliency abruptly
// switches between two long-lived static crops. Broad foreground activity can
// still locate the person approximately, even when it is too diffuse to be an
// authoritative crop by itself (for example, a head dropping over dark
// clothes). We use that activity only to choose an already-observed stable
// crop cluster; gradual/multi-step traversal and ambiguous choices are left
// untouched.
func correctSmartCropAbruptStaticClusters(
	samples []smartCropV2Sample,
	result smartCropTemporalResult,
	srcW, cropW int,
) int {
	if len(samples) < 6 || srcW <= cropW || cropW <= 0 ||
		result.MeanActivity < smartCropTemporalDenseMinMeanActivity ||
		result.ActiveFraction < 0.10 || result.Concentration < 0.30 {
		return 0
	}
	for _, sample := range samples {
		if sample.motionTracked {
			return 0
		}
	}

	clusterRadius := maxInt(96, cropW/4)
	clusters := make([]smartCropXCluster, 0, 3)
	assignments := make([]int, len(samples))
	for i, sample := range samples {
		best, bestDistance := -1, math.MaxFloat64
		for j, cluster := range clusters {
			distance := math.Abs(float64(sample.point.X) - cluster.center)
			if distance <= float64(clusterRadius) && distance < bestDistance {
				best, bestDistance = j, distance
			}
		}
		if best < 0 {
			clusters = append(clusters, smartCropXCluster{center: float64(sample.point.X), count: 1})
			assignments[i] = len(clusters) - 1
			continue
		}
		cluster := &clusters[best]
		cluster.center = (cluster.center*float64(cluster.count) + float64(sample.point.X)) / float64(cluster.count+1)
		cluster.count++
		assignments[i] = best
	}
	if len(clusters) != 2 || clusters[0].count < 3 || clusters[1].count < 3 {
		return 0
	}

	candidate := 0
	firstDistance := math.Abs(clusters[0].center - float64(result.X))
	secondDistance := math.Abs(clusters[1].center - float64(result.X))
	if secondDistance < firstDistance {
		candidate = 1
		firstDistance, secondDistance = secondDistance, firstDistance
	}
	if firstDistance > float64(cropW)/2 || secondDistance-firstDistance < float64(cropW)/6 {
		return 0
	}
	transitions := 0
	for i := 1; i < len(assignments); i++ {
		if assignments[i] != assignments[i-1] {
			transitions++
		}
	}
	if transitions > 2 {
		return 0
	}

	x := clampInt(roundEven(int(math.Round(clusters[candidate].center))), 0, srcW-cropW)
	corrected := 0
	for i := range samples {
		if samples[i].point.X != x {
			samples[i].point.X = x
			corrected++
		}
		samples[i].temporalTrack = true
	}
	return corrected
}

// correctSmartCropStationaryRuns repairs low-confidence saliency anchors that
// occur while a tracked subject pauses inside an otherwise moving scene.
// Scene-wide temporal correction deliberately stays disabled for sustained
// traversal, because flattening the whole scene would erase real movement.
// That old all-or-nothing rule left stationary stretches unprotected: a
// bright room feature could win for several seconds after the person stopped.
//
// Motion-certified points remain authoritative. Only runs of at least three
// consecutive non-motion samples are considered, scene cuts split runs, and a
// run changes only when its own frames establish a confident temporal or
// recurring-person consensus. Marking corrected samples as temporalTrack also
// prevents the later single-frame head guard from undoing the stable result.
func correctSmartCropStationaryRuns(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < smartCropTemporalMinSamples || srcW <= cropW || cropW <= 0 {
		return 0
	}
	corrected := 0
	for i := 0; i < len(samples); {
		if samples[i].motionTracked {
			i++
			continue
		}
		end := i + 1
		for end < len(samples) && !samples[end].point.Cut && !samples[end].motionTracked {
			end++
		}
		if end-i < smartCropTemporalMinSamples {
			i = end
			continue
		}
		// A locally dense foreground means this nominally "stationary" run is
		// actually a posture change or a traversal that the pairwise motion
		// detector did not certify at every frame. Resolve that run from its own
		// frames before considering adjacent anchors. Otherwise a person who
		// lies down or reverses direction can be held at the previous position
		// for the rest of the reel.
		result, resultOK := bestSmartCropTemporalConsensus(samples[i:end], srcW, cropW)
		if resultOK && result.Concentration >= smartCropTemporalDenseMinConcentration &&
			result.MeanActivity >= smartCropTemporalDenseMinMeanActivity &&
			result.ActiveFraction >= smartCropTemporalDenseMinActiveFraction {
			corrected += correctSmartCropDenseRun(samples[i:end], result.X, srcW, cropW)
			i = end
			continue
		}
		// A run bracketed by trusted motion positions is an identity-continuity
		// problem before it is a fresh saliency problem. This also rejects a
		// locally "confident" background change when both adjacent subject
		// anchors remain on the other side of the frame.
		if n := correctSmartCropStationaryRunFromMotionContinuity(samples, i, end, srcW, cropW); n > 0 {
			corrected += n
			i = end
			continue
		}
		if !resultOK || !smartCropTemporalResultConfident(result) {
			i = end
			continue
		}
		// Even when the foreground activity falls just below the dense-motion
		// threshold, preserve a repeatedly observed one-sided person extent.
		// Encoding and decoder revisions can move that activity score slightly;
		// the clustered position evidence below is the more stable signal.
		if end-i >= 4 {
			corrected += correctSmartCropDenseRun(samples[i:end], result.X, srcW, cropW)
			i = end
			continue
		}
		x := clampInt(roundEven(result.X), 0, srcW-cropW)
		for j := i; j < end; j++ {
			if samples[j].point.X != x {
				samples[j].point.X = x
				corrected++
			}
			samples[j].temporalTrack = true
		}
		i = end
	}
	return corrected
}

// correctSmartCropStationarySubjectTails handles the final hand-off from
// motion tracking to a nearly motionless pose. Pairwise motion can correctly
// follow a person into a crouch or recline and then disappear once the pose is
// held. Generic saliency may leave the crop at a nearby but no longer
// containing position. A rolling multi-frame foreground consensus still sees
// the subject for several frames during that hand-off.
//
// This is intentionally a tail/bridge correction rather than another global
// tracker: a run must touch a motion-certified sample, remain spatially
// stationary itself, and provide at least four tightly clustered rolling
// subject candidates. Static-room anchors are excluded. The resulting move is
// bounded to half a crop from the trusted motion position, so a couch, window,
// or codec noise cannot make the crop cross the room.
func correctSmartCropStationarySubjectTails(samples []smartCropV2Sample, srcW, cropW int) int {
	if len(samples) < 6 || srcW <= cropW || cropW <= 0 {
		return 0
	}
	corrected := 0
	for start := 0; start < len(samples); {
		if samples[start].motionTracked {
			start++
			continue
		}
		end := start + 1
		for end < len(samples) && !samples[end].point.Cut && !samples[end].motionTracked {
			end++
		}
		if end-start < 5 {
			start = end
			continue
		}

		prev := start - 1
		next := end
		prevOK := prev >= 0 && !samples[start].point.Cut && samples[prev].motionTracked
		nextOK := next < len(samples) && !samples[next].point.Cut && samples[next].motionTracked
		if !prevOK && !nextOK {
			start = end
			continue
		}

		sceneStart := start
		for sceneStart > 0 && !samples[sceneStart].point.Cut {
			sceneStart--
		}
		sceneEnd := end
		for sceneEnd < len(samples) && !samples[sceneEnd].point.Cut {
			sceneEnd++
		}
		// Inspect only a bounded hand-off band next to each trusted motion
		// anchor. This keeps the cost independent of a long stationary pose and
		// avoids letting late codec noise outvote the actual transition.
		indices := make([]int, 0, 12)
		seen := make(map[int]bool, 12)
		appendIndex := func(i int) {
			if i >= start && i < end && !seen[i] {
				seen[i] = true
				indices = append(indices, i)
			}
		}
		if prevOK {
			for i, limit := start, minInt(end, start+12); i < limit; i += 2 {
				appendIndex(i)
			}
		}
		if nextOK {
			for i, limit := end-1, maxInt(start, end-12); i >= limit; i -= 2 {
				appendIndex(i)
			}
		}
		candidates := make([]int, 0, len(indices))
		for _, i := range indices {
			left, right := maxInt(sceneStart, i-4), minInt(sceneEnd, i+5)
			if right-left < 5 {
				continue
			}
			result, ok := bestSmartCropTemporalConsensus(samples[left:right], srcW, cropW)
			if !ok || result.StaticAnchored || result.Concentration < 0.70 {
				continue
			}
			strong := smartCropTemporalResultConfident(result) &&
				(result.SubjectAnchored || (result.MeanActivity >= smartCropTemporalDenseMinMeanActivity &&
					result.ActiveFraction >= smartCropTemporalDenseMinActiveFraction))
			weakHandoff := result.MeanActivity >= 0.0005 && result.ActiveFraction >= 0.00002
			if !strong && !weakHandoff {
				continue
			}
			candidates = append(candidates, clampInt(roundEven(result.X), 0, srcW-cropW))
		}
		corrected += correctSmartCropStationarySubjectRun(samples, start, end, candidates, len(indices), srcW, cropW)
		start = end
	}
	return corrected
}

func correctSmartCropStationarySubjectRun(samples []smartCropV2Sample, start, end int, candidates []int, observations, srcW, cropW int) int {
	if start < 0 || end > len(samples) || end-start < 5 || srcW <= cropW || cropW <= 0 ||
		observations < 4 || len(candidates) < 4 || len(candidates)*2 < observations {
		return 0
	}
	prev := start - 1
	next := end
	prevOK := prev >= 0 && !samples[start].point.Cut && samples[prev].motionTracked
	nextOK := next < len(samples) && !samples[next].point.Cut && samples[next].motionTracked
	if !prevOK && !nextOK {
		return 0
	}
	minX, maxX := samples[start].point.X, samples[start].point.X
	for i := start + 1; i < end; i++ {
		minX = minInt(minX, samples[i].point.X)
		maxX = maxInt(maxX, samples[i].point.X)
	}
	if maxX-minX > cropW/4 {
		return 0
	}

	candidates = append([]int(nil), candidates...)
	sort.Ints(candidates)
	lo := candidates[(len(candidates)-1)/5]
	hi := candidates[(len(candidates)-1)*4/5]
	if hi-lo > cropW/3 {
		return 0
	}
	candidateX := roundEven(candidates[len(candidates)/2])
	anchorX := candidateX
	if prevOK && nextOK {
		if absInt(samples[prev].point.X-samples[next].point.X) > cropW/2 {
			return 0
		}
		anchorX = roundEven((samples[prev].point.X + samples[next].point.X) / 2)
	} else if prevOK {
		anchorX = samples[prev].point.X
	} else {
		anchorX = samples[next].point.X
	}
	if absInt(candidateX-anchorX) > cropW/2 {
		return 0
	}

	candidateX = clampInt(candidateX, anchorX-cropW/2, anchorX+cropW/2)
	candidateX = clampInt(roundEven(candidateX), 0, srcW-cropW)
	corrected := 0
	for i := start; i < end; i++ {
		if samples[i].point.X != candidateX {
			samples[i].point.X = candidateX
			corrected++
		}
		samples[i].temporalTrack = true
	}
	return corrected
}

// correctSmartCropDenseRun uses the local temporal consensus as the stable
// baseline, but keeps a repeated one-sided extent as a new state. This matters
// when a person lies down or reverses direction: the inexpensive per-frame
// guard may recognize the outer head only intermittently, while flattening the
// entire run would erase the transition. Three clustered observations are
// required to enter a state; aligned generic frames do not immediately clear
// it, which prevents a visible one-second crop oscillation.
func correctSmartCropDenseRun(samples []smartCropV2Sample, consensusX, srcW, cropW int) int {
	consensusX = clampInt(roundEven(consensusX), 0, srcW-cropW)
	if len(samples) < 4 || cropW <= 0 || srcW <= cropW {
		return 0
	}
	threshold := maxInt(48, cropW/6)
	clusterRadius := maxInt(32, cropW/6)
	// Reclining faces are intermittently recognized when a hand, hair, or a
	// patterned cushion merges with the warm-subject component. Keep the
	// existing requirement for three tightly clustered positions, but allow
	// those observations to be sparse across one capped-storyboard interval.
	// Scene cuts already split callers' runs, and the later reclining-extent
	// certification bounds an unrelated repeated background candidate.
	const maxEvidenceGapMs = smartCropV2MaxGapMs
	type extentEvent struct {
		index int
		x     int
		dir   int
	}
	events := make([]extentEvent, 0, 2)
	lastEventEnd := -1
	for i := 0; i+2 < len(samples); i++ {
		if i <= lastEventEnd {
			continue
		}
		for j := i + 2; j < len(samples) && samples[j].point.AtMs-samples[i].point.AtMs <= maxEvidenceGapMs; j++ {
			left, right := make([]int, 0, 3), make([]int, 0, 3)
			for k := i; k <= j; k++ {
				delta := samples[k].point.X - consensusX
				if delta <= -threshold {
					left = append(left, samples[k].point.X)
				} else if delta >= threshold {
					right = append(right, samples[k].point.X)
				}
			}
			left = tightSmartCropExtentCluster(left, clusterRadius)
			right = tightSmartCropExtentCluster(right, clusterRadius)
			values, dir := left, -1
			if len(right) > len(left) {
				values, dir = right, 1
			}
			if len(values) < 3 {
				continue
			}
			events = append(events, extentEvent{
				index: maxInt(0, i-1),
				x:     clampInt(roundEven(values[len(values)/2]), 0, srcW-cropW),
				dir:   dir,
			})
			lastEventEnd = j
			break
		}
	}
	// Once an extent state is established, two tightly clustered observations
	// on the opposite side are enough to release it. Requiring three again
	// would hold the old crop one or two seconds into a clear reversal.
	if len(events) > 0 {
		const maxReversalGapMs = 4 * smartCropTrackingIntervalMs
		last := events[len(events)-1]
		for i := last.index + 1; i+1 < len(samples); i++ {
			a, b := samples[i], samples[i+1]
			if b.point.AtMs-a.point.AtMs > maxReversalGapMs || absInt(b.point.X-a.point.X) > clusterRadius {
				continue
			}
			da, db := a.point.X-consensusX, b.point.X-consensusX
			dir := 0
			if da <= -threshold && db <= -threshold {
				dir = -1
			} else if da >= threshold && db >= threshold {
				dir = 1
			}
			if dir == 0 || dir == last.dir {
				continue
			}
			events = append(events, extentEvent{
				index: maxInt(last.index+1, i-1),
				x:     clampInt(roundEven((a.point.X+b.point.X)/2), 0, srcW-cropW),
				dir:   dir,
			})
			break
		}
	}
	for i := range events {
		if absInt(events[i].x-consensusX) <= cropW/4 {
			continue
		}
		certifiedRecliningExtent := false
		for _, sample := range samples {
			candidate, changed := recliningSubjectAwareNarrowSmartCropX(sample.img, consensusX, srcW, cropW)
			if !changed || (candidate-consensusX)*events[i].dir <= 0 {
				continue
			}
			if absInt(candidate-events[i].x) <= cropW/3 {
				certifiedRecliningExtent = true
				break
			}
		}
		if !certifiedRecliningExtent {
			events[i].x = clampInt(events[i].x, consensusX-cropW/4, consensusX+cropW/4)
			events[i].x = clampInt(roundEven(events[i].x), 0, srcW-cropW)
		}
	}

	currentX, currentDir, eventIndex := consensusX, 0, 0
	corrected := 0
	for i := range samples {
		for eventIndex < len(events) && events[eventIndex].index <= i {
			if events[eventIndex].dir != currentDir || absInt(events[eventIndex].x-currentX) >= threshold {
				currentX, currentDir = events[eventIndex].x, events[eventIndex].dir
			}
			eventIndex++
		}
		if samples[i].point.X != currentX {
			samples[i].point.X = currentX
			corrected++
		}
		samples[i].temporalTrack = true
	}
	return corrected
}

// tightSmartCropExtentCluster returns the densest subset whose complete
// horizontal range fits inside radius. A single threshold-sensitive saliency
// result must not discard three otherwise consistent observations of the
// same head/body extent.
func tightSmartCropExtentCluster(values []int, radius int) []int {
	if len(values) == 0 || radius < 0 {
		return nil
	}
	values = append([]int(nil), values...)
	sort.Ints(values)
	bestStart, bestEnd := 0, 0
	for start, end := 0, 0; end < len(values); end++ {
		for values[end]-values[start] > radius {
			start++
		}
		if end-start > bestEnd-bestStart {
			bestStart, bestEnd = start, end
		}
	}
	return values[bestStart : bestEnd+1]
}

// correctSmartCropStationaryRunFromMotionContinuity handles the case where a
// person becomes too still for temporal foreground scoring immediately before
// or after a motion-certified stretch. Generic saliency can then jump to a
// static room feature and remain there for many seconds. The adjacent tracked
// position is a stronger identity/continuity signal, but it is only allowed to
// repair a run when at least two thirds of the run disagrees in one direction.
// Opposing outliers and distant anchors are preserved as real traversal.
func correctSmartCropStationaryRunFromMotionContinuity(
	samples []smartCropV2Sample,
	start, end, srcW, cropW int,
) int {
	if end-start < smartCropTemporalMinSamples || start < 0 || end > len(samples) ||
		srcW <= cropW || cropW <= 0 {
		return 0
	}
	prev := start - 1
	next := end
	prevOK := prev >= 0 && !samples[start].point.Cut && samples[prev].motionTracked
	nextOK := next < len(samples) && !samples[next].point.Cut && samples[next].motionTracked
	if !prevOK && !nextOK {
		return 0
	}
	if prevOK && nextOK && absInt(samples[prev].point.X-samples[next].point.X) > cropW/2 {
		return 0
	}

	predictedX := func(atMs int64) int {
		switch {
		case prevOK && nextOK:
			return interpolateSmartCropStillX(samples[prev].point, samples[next].point, atMs)
		case prevOK:
			return samples[prev].point.X
		default:
			return samples[next].point.X
		}
	}
	deadZone := maxInt(48, cropW/4)
	left, right, aligned := 0, 0, 0
	for j := start; j < end; j++ {
		delta := samples[j].point.X - predictedX(samples[j].point.AtMs)
		switch {
		case delta < -deadZone:
			left++
		case delta > deadZone:
			right++
		default:
			aligned++
		}
	}
	required := maxInt(3, (2*(end-start)+2)/3)
	bracketedMinority := prevOK && nextOK && aligned >= 2 &&
		((left >= 2 && right == 0) || (right >= 2 && left == 0))
	if !bracketedMinority && !((left >= required && right == 0) || (right >= required && left == 0)) {
		return 0
	}

	corrected := 0
	for j := start; j < end; j++ {
		x := clampInt(roundEven(predictedX(samples[j].point.AtMs)), 0, srcW-cropW)
		if absInt(samples[j].point.X-x) > deadZone {
			samples[j].point.X = x
			corrected++
		}
		samples[j].temporalTrack = true
	}
	return corrected
}

func smartCropSceneHasSustainedTraversal(samples []smartCropV2Sample, cropW int) bool {
	if len(samples) < 5 || cropW <= 0 {
		return false
	}
	xs := make([]int, 0, len(samples))
	for _, sample := range samples {
		if sample.motionTracked {
			xs = append(xs, sample.point.X)
		}
	}
	if len(xs) < 3 {
		xs = xs[:0]
		for _, sample := range samples {
			xs = append(xs, sample.point.X)
		}
	}
	ordered := append([]int(nil), xs...)
	sort.Ints(xs)
	// Robust 20th-to-80th percentile range ignores one isolated drift point,
	// while accepting movement that persists across several adjacent frames.
	lo := xs[(len(xs)-1)/5]
	hi := xs[(len(xs)-1)*4/5]
	spread := hi - lo
	if spread <= maxInt(96, cropW/3) {
		return false
	}
	// Alternating between a static background and one subject crop is saliency
	// flicker, not traversal. Genuine following is directionally coherent for
	// several samples even when the person later turns around.
	stepThreshold := maxInt(48, cropW/6)
	lastDirection, significant, reversals := 0, 0, 0
	for i := 1; i < len(ordered); i++ {
		delta := ordered[i] - ordered[i-1]
		if absInt(delta) <= stepThreshold {
			continue
		}
		direction := 1
		if delta < 0 {
			direction = -1
		}
		if lastDirection != 0 && direction != lastDirection {
			reversals++
		}
		lastDirection = direction
		significant++
	}
	if significant >= 4 && reversals*3 >= significant &&
		absInt(ordered[len(ordered)-1]-ordered[0]) < spread/2 {
		return false
	}
	return true
}

func smartCropTemporalCorrectionDirection(result smartCropTemporalResult, left, right, aligned, samples int) int {
	if direction := smartCropTemporalLegacyCorrectionDirection(result, left, right, aligned, samples); direction != 0 {
		return direction
	}
	return smartCropTemporalAnchoredCorrectionDirection(result, left, right, aligned, samples)
}

func smartCropTemporalLegacyCorrectionDirection(result smartCropTemporalResult, left, right, aligned, samples int) int {
	required := maxInt(4, (2*samples+2)/3)
	if left >= required && right == 0 {
		return -1
	}
	if right >= required && left == 0 {
		return 1
	}

	// A short run of bad saliency frames can occur at one edge of an
	// otherwise static shot (for example, two bad frames followed by five good
	// ones). Correct that minority only when a highly concentrated temporal
	// region is backed by the same modest, person-like warm component across
	// the scene. Real traversal loses that recurring anchor or produces
	// outliers on both sides, so its path remains tracked.
	stableRequired := maxInt(3, (2*samples+2)/3)
	stableMinority := result.Concentration >= 0.90 &&
		result.AnchorCoverage >= stableRequired &&
		result.AnchorScore >= smartCropTemporalStaticMinAnchorScore &&
		result.AnchorScore < smartCropTemporalStaticMaxAnchorScore &&
		aligned >= stableRequired
	if stableMinority && left > 0 && right == 0 {
		return -1
	}
	if stableMinority && right > 0 && left == 0 {
		return 1
	}
	return 0
}

func smartCropTemporalAnchoredCorrectionDirection(result smartCropTemporalResult, left, right, aligned, samples int) int {
	anchoredMinority := result.SubjectAnchored &&
		result.Concentration >= 0.85 &&
		result.AnchorCoverage >= maxInt(3, (2*result.Samples+2)/3) &&
		result.AnchorScore < 1500.0 &&
		aligned >= maxInt(3, samples/3)
	if anchoredMinority && left > 0 && right == 0 {
		return -1
	}
	if anchoredMinority && right > 0 && left == 0 {
		return 1
	}
	return 0
}

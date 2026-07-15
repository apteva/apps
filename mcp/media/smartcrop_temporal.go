package main

import (
	"image"
	"math"
)

const (
	smartCropTemporalMaxSamples        = 9
	smartCropTemporalMinSamples        = 3
	smartCropSceneCutThreshold         = 0.28
	smartCropTemporalMinConcentration  = 0.60
	smartCropTemporalMinMeanActivity   = 0.50
	smartCropTemporalMinActiveFraction = 0.015
)

type smartCropTemporalResult struct {
	X               int
	Samples         int
	Concentration   float64
	MeanActivity    float64
	ActiveFraction  float64
	SubjectAnchored bool
	AnchorCoverage  int
	AnchorScore     float64
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
	if anchored {
		anchorStart := clampInt(int(math.Round(anchorCenter-float64(normalizedCropW)/2.0)), 0, w-normalizedCropW)
		// A recurring skin-colored component certifies the temporal region, but
		// does not position it. A face, hand, or arm can be the strongest warm
		// component and centering that fragment would move the torso off-center.
		anchored = absInt(anchorStart-bestStart) <= normalizedCropW*3/5
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
	}
	return result, true
}

func applySmartCropTemporalOverride(baseX int, result smartCropTemporalResult, cropW, srcW int) (int, bool) {
	if !smartCropTemporalResultConfident(result) || absInt(baseX-result.X) <= cropW/8 {
		return baseX, false
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
	return result.Concentration >= smartCropTemporalMinConcentration &&
		result.MeanActivity >= smartCropTemporalMinMeanActivity &&
		result.ActiveFraction >= smartCropTemporalMinActiveFraction
}

type warmSubjectComponent struct {
	centerX float64
	score   float64
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
		return 0, bestCoverage, normalizedScore, false
	}
	return bestCenter, bestCoverage, normalizedScore, true
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
		if result, ok := temporalSubjectConsensus(samples[start:end], srcW, cropW); ok {
			if !smartCropTemporalResultConfident(result) {
				start = end
				continue
			}
			deadZone := cropW / 8
			left, right := 0, 0
			for i := start; i < end; i++ {
				switch {
				case samples[i].point.X < result.X-deadZone:
					left++
				case samples[i].point.X > result.X+deadZone:
					right++
				}
			}
			// A static-background failure pushes most saliency samples away
			// from the subject in the same direction. Opposing outliers mean
			// the subject is genuinely traversing the frame; keep the tracked
			// path rather than flattening it to one scene-wide consensus.
			// Four agreeing frames are the minimum for modifying a reel path.
			// Three-frame scenes are too short to distinguish a static saliency
			// miss from deliberate subject movement reliably.
			required := maxInt(4, (2*(end-start)+2)/3)
			direction := 0
			if left >= required && right == 0 {
				direction = -1
			} else if right >= required && left == 0 {
				direction = 1
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

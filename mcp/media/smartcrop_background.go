package main

import (
	"context"
	"image"
	"math"
	"sort"
	"sync"
)

const (
	smartCropBackgroundMaxSceneDifference = 0.20
	smartCropBackgroundPixelDifference    = 14
	smartCropBackgroundMinActivity        = 0.65
)

type smartCropBackgroundResult struct {
	X             int
	References    int
	Concentration float64
	Improvement   float64
	RowCoverage   float64
}

// backgroundAwareNarrowSmartCropX builds a tiny per-pixel background model
// from distributed storyboard frames. A pixel is background when its current
// colour matches a meaningful minority of the references; foreground when it
// differs from nearly all of them. This nearest-consensus form is deliberate:
// a conventional median can retain a ghost of a frequently seated person and
// then identify the newly revealed couch as foreground when they recline.
// Camera moves, edits, exposure changes, and empty rooms produce diffuse
// activity and fail the concentration gates below.
func backgroundAwareNarrowSmartCropX(current image.Image, references []image.Image, currentX, srcW, cropW int) (int, smartCropBackgroundResult, bool) {
	if current == nil || srcW <= cropW || cropW <= 0 {
		return currentX, smartCropBackgroundResult{}, false
	}
	b := current.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return currentX, smartCropBackgroundResult{}, false
	}
	thumbCropW := clampInt(int(math.Round(float64(cropW)*float64(w)/float64(srcW))), 1, w)
	if thumbCropW >= w {
		return currentX, smartCropBackgroundResult{}, false
	}
	usable := make([]image.Image, 0, len(references))
	for _, reference := range references {
		if reference == nil || reference.Bounds().Dx() != w || reference.Bounds().Dy() != h ||
			sceneCutScore(current, reference) > smartCropBackgroundMaxSceneDifference {
			continue
		}
		usable = append(usable, reference)
	}
	if len(usable) < 4 {
		return currentX, smartCropBackgroundResult{}, false
	}
	if len(usable) > 12 {
		usable = usable[:12]
	}
	currentRGB := normalizedSmartCropRGB(current, w, h)
	refRGB := make([][]uint8, len(usable))
	for i, reference := range usable {
		refRGB[i] = normalizedSmartCropRGB(reference, w, h)
	}
	cols := make([]float64, w)
	active := make([]bool, w*h)
	values := make([]int, len(refRGB))
	for y := h / 10; y < h; y++ {
		for x := 0; x < w; x++ {
			idx := (y*w + x) * 3
			for i := range refRGB {
				values[i] = (absInt(int(currentRGB[idx])-int(refRGB[i][idx])) +
					absInt(int(currentRGB[idx+1])-int(refRGB[i][idx+1])) +
					absInt(int(currentRGB[idx+2])-int(refRGB[i][idx+2]))) / 3
			}
			sort.Ints(values)
			// Ignore one or two coincidental matches (compression, a hand, or
			// clothing revisiting the same pixel), while allowing a stable room
			// pixel to match only a minority after gradual exposure changes.
			diff := values[len(values)/3]
			if diff <= smartCropBackgroundPixelDifference {
				continue
			}
			weight := float64(minInt(diff-smartCropBackgroundPixelDifference, 80))
			// The couch and floor often contain compression shimmer. Requiring a
			// little local structure favors an actual foreground boundary.
			gray := gray8(int(currentRGB[idx]), int(currentRGB[idx+1]), int(currentRGB[idx+2]))
			grad := 0
			if x > 0 {
				left := idx - 3
				grad += absInt(gray - gray8(int(currentRGB[left]), int(currentRGB[left+1]), int(currentRGB[left+2])))
			}
			if y > 0 {
				up := idx - w*3
				grad += absInt(gray - gray8(int(currentRGB[up]), int(currentRGB[up+1]), int(currentRGB[up+2])))
			}
			weight *= 0.5 + math.Min(1.5, float64(grad)/32.0)
			cols[x] += weight
			active[y*w+x] = true
		}
	}
	cols = smoothColumns(cols, 7)
	bestStart, bestScore, currentScore, total, ok := bestColumnWindow(cols, thumbCropW, currentX, srcW)
	if !ok || total < float64(w*h)*smartCropBackgroundMinActivity {
		return currentX, smartCropBackgroundResult{}, false
	}
	coveredRows := 0
	for y := h / 10; y < h; y++ {
		count := 0
		for x := bestStart; x < bestStart+thumbCropW; x++ {
			if active[y*w+x] {
				count++
			}
		}
		if count >= maxInt(3, thumbCropW/18) {
			coveredRows++
		}
	}
	rowCoverage := float64(coveredRows) / float64(h-h/10)
	concentration := bestScore / math.Max(total, 1)
	improvement := bestScore / math.Max(currentScore, 1)
	result := smartCropBackgroundResult{
		References: len(usable), Concentration: concentration,
		Improvement: improvement, RowCoverage: rowCoverage,
	}
	if concentration < 0.43 || improvement < 1.12 || rowCoverage < 0.24 {
		return currentX, result, false
	}
	x := clampInt(roundEven(int(math.Round(float64(bestStart)*float64(srcW)/float64(w)))), 0, srcW-cropW)
	result.X = x
	if absInt(x-currentX) > cropW/2 && (concentration < 0.68 || improvement < 1.25) {
		// A room-wide jump needs substantially stronger evidence than a local
		// centering adjustment. This rejects the contaminated-background case
		// where a person already forms a stable saliency cluster on one side,
		// while a weak couch ghost proposes the middle of the room.
		return currentX, result, false
	}
	if absInt(x-currentX) <= maxInt(20, cropW/12) {
		return currentX, result, false
	}
	return x, result, true
}

func selectSmartCropBackgroundDerivations(derivs []DerivationRow, excludeStartMs, excludeEndMs int64, limit int) []DerivationRow {
	if limit <= 0 {
		return nil
	}
	keyframes := make([]DerivationRow, 0, len(derivs))
	for _, derivation := range derivs {
		if derivation.Kind != "keyframe" || derivation.Status != "ok" || derivation.StorageFileID == "" ||
			(derivation.PositionMs >= excludeStartMs && derivation.PositionMs <= excludeEndMs) {
			continue
		}
		keyframes = append(keyframes, derivation)
	}
	sort.Slice(keyframes, func(i, j int) bool { return keyframes[i].PositionMs < keyframes[j].PositionMs })
	if len(keyframes) <= limit {
		return keyframes
	}
	out := make([]DerivationRow, 0, limit)
	for i := 0; i < limit; i++ {
		index := int(math.Round(float64(i) * float64(len(keyframes)-1) / float64(limit-1)))
		out = append(out, keyframes[index])
	}
	return out
}

func downloadSmartCropBackgroundImages(ctx context.Context, sc *storageClient, projectID string, derivs []DerivationRow) []image.Image {
	images := make([]image.Image, len(derivs))
	sem := make(chan struct{}, smartCropV2MaxParallelDownloads)
	var wg sync.WaitGroup
	for i, derivation := range derivs {
		i, derivation := i, derivation
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				return
			}
			defer func() { <-sem }()
			img, err := downloadAndDecodeImage(ctx, sc, projectID, derivation.StorageFileID)
			if err == nil {
				images[i] = img
			}
		}()
	}
	wg.Wait()
	out := make([]image.Image, 0, len(images))
	for _, img := range images {
		if img != nil {
			out = append(out, img)
		}
	}
	return out
}

func correctSmartCropBackgroundSamples(samples []smartCropV2Sample, references []image.Image, srcW, cropW int) int {
	corrected := 0
	for i := range samples {
		x, _, ok := backgroundAwareNarrowSmartCropX(samples[i].img, references, samples[i].point.X, srcW, cropW)
		if !ok {
			continue
		}
		if samples[i].face != nil && absInt(x-samples[i].point.X) > cropW/6 {
			// A direct ML anchor beats a background model that may still contain
			// a ghost of the person at another position.
			continue
		}
		samples[i].point.X = x
		samples[i].backgroundTracked = true
		corrected++
	}
	return corrected
}

package main

import (
	"context"
	"image"
	"math"
)

// Only a concentrated, fixed-camera foreground can support a whole-reel zoom.
// Use the same bounded samples and background references as horizontal tracking.
func smartCropReelCompositionCandidate(samples []smartCropV2Sample, refs []image.Image, srcW, srcH, cropW, cropH int) bool {
	if cropH != srcH || cropW >= cropH || srcW <= cropW*2 || len(samples) < 2 {
		return false
	}
	for _, s := range samples {
		if s.point.Cut {
			return false
		}
	}
	// A cheap candidacy check; composition below verifies every sample again.
	count := minInt(4, len(samples))
	n := 0
	for i := 0; i < count; i++ {
		s := samples[i*(len(samples)-1)/(count-1)]
		_, r, _ := backgroundAwareNarrowSmartCropX(s.img, refs, s.point.X, srcW, cropW)
		if r.References >= 4 && r.Concentration >= 0.85 && r.RowCoverage >= 0.24 {
			n++
		}
	}
	return n*4 >= count*3
}

// A single window contains every sampled extent, upper pose and padded head.
// The source bottom is retained because dark shoes may have no skin evidence.
// No per-frame zoom/pan is introduced; ambiguity, edits and wide movement keep
// the released tracking path. Coverage is sampled, never a visual approval.
func composeSmartCropReel(ctx context.Context, current cropWindow, samples []smartCropV2Sample, refs []image.Image, srcW, srcH int, target smartCropTarget) (result cropWindow, changed bool) {
	if !smartCropReelCompositionCandidate(samples, refs, srcW, srcH, current.W, current.H) {
		return current, false
	}
	defer func() {
		if !changed {
			recordSmartCropFallback(ctx, "stable_composition_envelope_unverified")
		}
	}()
	left, right, top := srcW, 0, srcH
	heads := 0
	var used []smartCropV2Sample
	var extents []*smartCropSubjectExtent
	for _, s := range samples {
		if s.point.AtMs < target.StartMs || s.point.AtMs > target.EndMs {
			continue
		}
		if len(used) > 0 && s.point.AtMs-used[len(used)-1].point.AtMs > 2500 {
			return current, false
		}
		_, r, _ := backgroundAwareNarrowSmartCropX(s.img, refs, s.point.X, srcW, current.W)
		if r.References < 4 || r.Concentration < 0.85 || r.RowCoverage < 0.24 {
			return current, false
		}
		// Weak face detections on furniture/lower limbs cannot certify composition.
		if s.face != nil && s.face.Quality < 20 {
			s.face = nil
		}
		s.sceneForeground = true
		s.scenePoseGroup = true
		s.motionTracked = true // independently supported by the concentrated foreground
		e, ok := supportedSmartCropSubjectExtent(s, refs, srcW, srcH, current.W)
		if !ok || e.Bounds.W <= 0 || e.Bounds.H <= 0 || e.Bounds.Y < srcH/8 || e.Bounds.W > e.Bounds.H || e.Evidence == "reclining_foreground_head" {
			return current, false
		}
		b := e.Bounds
		margin := maxInt(12, current.W/50)
		left = minInt(left, b.X-margin)
		right = maxInt(right, b.X+b.W+margin)
		top = minInt(top, b.Y-margin)
		if u := e.UpperPose; u != nil {
			left = minInt(left, u.X-margin)
			right = maxInt(right, u.X+u.W+margin)
		}
		if h := e.Head; h != nil {
			heads++
			pad := h.Scale / 2
			left = minInt(left, h.MinX-pad)
			right = maxInt(right, h.MaxX+pad)
			top = minInt(top, h.MinY-pad)
		}
		// Also retain any geometry already committed to the coverage audit.
		if a := cropAudit(ctx); a != nil {
			for _, e := range a.Extents {
				if e.AtMs == s.point.AtMs {
					left = minInt(left, e.Bounds.X-margin)
					right = maxInt(right, e.Bounds.X+e.Bounds.W+margin)
					top = minInt(top, e.Bounds.Y-margin)
					if h := e.Head; h != nil {
						left = minInt(left, h.Bounds.X)
						right = maxInt(right, h.Bounds.X+h.Bounds.W)
						top = minInt(top, h.Bounds.Y)
					}
				}
			}
		}
		used = append(used, s)
		extents = append(extents, e)
	}
	if len(used) < 4 || heads*2 < len(used) || used[0].point.AtMs-target.StartMs > 250 || target.EndMs-used[len(used)-1].point.AtMs > 250 {
		return current, false
	}
	h := maxInt(srcH*2/3, srcH-top)
	h = maxInt(h, int(math.Ceil(float64(right-left)*float64(current.H)/float64(current.W))))
	h = (h + 1) &^ 1
	w := (int(math.Ceil(float64(h)*float64(current.W)/float64(current.H))) + 1) &^ 1
	if h >= current.H-current.H/20 || w > current.W || left < 0 || right > srcW {
		return current, false
	}
	x := roundEven((left + right - w) / 2)
	win := cropWindow{X: x, Y: srcH - h, W: w, H: h}
	if x < 0 || x > left || x+w < right || win.Y > top {
		return current, false
	}
	for i, s := range used {
		recordSmartCropExtent(ctx, s, extents[i], w)
	}
	return win, true
}

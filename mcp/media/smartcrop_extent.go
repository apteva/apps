package main

import (
	"image"
	"math"
	"sort"
)

type smartCropSubjectExtent struct {
	Bounds           cropWindow     `json:"bounds"`
	Head             *smartCropFace `json:"head,omitempty"`
	Evidence         string         `json:"evidence"`
	UpperPose        *cropWindow    `json:"upper_pose,omitempty"`
	Pose             *cropWindow    `json:"pose,omitempty"`
	ForegroundBounds *cropWindow    `json:"foreground_bounds,omitempty"`
}

// supportedSmartCropSubjectExtent measures skin/head/limb geometry inside a
// connected foreground, using the existing distributed background references.
// Furniture and static warm decor cannot become head anchors from colour alone.
// A supported face or motion track chooses the component. Reclining poses may
// instead use a compact upper skin region in a horizontal foreground component.
func supportedSmartCropSubjectExtent(sample smartCropV2Sample, refs []image.Image, srcW, srcH, cropW int) (*smartCropSubjectExtent, bool) {
	if sample.img == nil || srcW <= cropW || cropW <= 0 {
		return nil, false
	}
	b := sample.img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w < 24 || h < 24 || w > 960 {
		return nil, false
	}
	pixels := normalizedSmartCropRGB(sample.img, w, h)
	var backgrounds [][]uint8
	for _, ref := range refs {
		if ref == nil || ref.Bounds().Dx() != w || ref.Bounds().Dy() != h || sceneCutScore(sample.img, ref) > smartCropBackgroundMaxSceneDifference {
			continue
		}
		backgrounds = append(backgrounds, normalizedSmartCropRGB(ref, w, h))
		if len(backgrounds) == 12 {
			break
		}
	}
	if len(backgrounds) < 4 {
		return nil, false
	}
	active := make([]bool, w*h)
	warm := make([]bool, w*h)
	values := make([]int, len(backgrounds))
	count := 0
	for pos := range active {
		idx := pos * 3
		r, g, blue := int(pixels[idx]), int(pixels[idx+1]), int(pixels[idx+2])
		warm[pos] = strictWarmSubjectPixel(r, g, blue)
		for i, ref := range backgrounds {
			values[i] = (absInt(r-int(ref[idx])) + absInt(g-int(ref[idx+1])) + absInt(blue-int(ref[idx+2]))) / 3
		}
		sort.Ints(values)
		active[pos] = values[len(values)/3] > smartCropBackgroundPixelDifference
		if active[pos] {
			count++
		}
	}
	// A moving camera or global exposure change provides no separable foreground.
	if count < w*h/100 || count > w*h*35/100 {
		return nil, false
	}
	visited := make([]bool, w*h)
	queue := make([]int, 0, count)
	type region struct {
		minX, maxX, minY, maxY int
		positions              []int
		overlap                int
	}
	var regions []region
	for start, v := range active {
		if !v || visited[start] {
			continue
		}
		queue = queue[:0]
		queue = append(queue, start)
		visited[start] = true
		r := region{minX: start % w, maxX: start % w, minY: start / w, maxY: start / w}
		for head := 0; head < len(queue); head++ {
			pos := queue[head]
			x, y := pos%w, pos/w
			r.minX = minInt(r.minX, x)
			r.maxX = maxInt(r.maxX, x)
			r.minY = minInt(r.minY, y)
			r.maxY = maxInt(r.maxY, y)
			if sample.face != nil && x*srcW/w >= sample.face.MinX && x*srcW/w <= sample.face.MaxX && y*srcH/h >= sample.face.MinY && y*srcH/h <= sample.face.MaxY {
				r.overlap++
			}
			for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
				for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
					n := ny*w + nx
					if active[n] && !visited[n] {
						visited[n] = true
						queue = append(queue, n)
					}
				}
			}
		}
		if len(queue) < w*h/100 || r.minX <= 1 || r.maxX >= w-2 || r.maxY-r.minY < h/5 {
			continue
		}
		center := (r.minX + r.maxX) * srcW / (2 * w)
		if center < sample.point.X-cropW/2 || center > sample.point.X+cropW*3/2 {
			continue
		}
		r.positions = append([]int(nil), queue...)
		regions = append(regions, r)
	}
	sort.SliceStable(regions, func(i, j int) bool {
		if regions[i].overlap != regions[j].overlap {
			return regions[i].overlap > regions[j].overlap
		}
		return len(regions[i].positions) > len(regions[j].positions)
	})
	// A verified video foreground may split at clothing that matches the room.
	// Join only two overlapping envelopes inside one portrait-width column;
	// side-by-side subjects and disconnected furniture remain ambiguous.
	if sample.scenePoseGroup && sample.face == nil && len(regions) == 2 {
		a, b := regions[0], regions[1]
		overlapX := minInt(a.maxX, b.maxX) - maxInt(a.minX, b.minX) + 1
		overlapY := minInt(a.maxY, b.maxY) - maxInt(a.minY, b.minY) + 1
		width := maxInt(a.maxX, b.maxX) - minInt(a.minX, b.minX) + 1
		if overlapX*2 >= minInt(a.maxX-a.minX+1, b.maxX-b.minX+1) && overlapY > 0 && width*srcW/w <= cropW*9/10 {
			a.minX = minInt(a.minX, b.minX)
			a.maxX = maxInt(a.maxX, b.maxX)
			a.minY = minInt(a.minY, b.minY)
			a.maxY = maxInt(a.maxY, b.maxY)
			a.positions = append(a.positions, b.positions...)
			regions = []region{a}
		}
	}
	// Without a face identity, similarly substantial foreground components
	// remain ambiguous; colour alone cannot select between two subjects.
	if sample.face == nil && len(regions) > 1 && len(regions[1].positions)*10 >= len(regions[0].positions)*6 {
		return nil, false
	}
	for _, r := range regions {
		skin := make([]bool, w*h)
		skinCount := 0
		left, right, top, bottom := w, -1, h, -1
		for _, pos := range r.positions {
			if warm[pos] {
				// Only the upper third can support an inferred head. Saturated red
				// fabric and lower feet/legs cannot compete with this geometry.
				skin[pos] = pos/w <= r.minY+(r.maxY-r.minY)/3 && int(pixels[pos*3])*10 < int(pixels[pos*3+1])*16 && pixels[pos*3+1] > 80
				skinCount++
				left = minInt(left, pos%w)
				right = maxInt(right, pos%w)
				top = minInt(top, pos/w)
				bottom = maxInt(bottom, pos/w)
			}
		}
		if skinCount < maxInt(30, w*h/500) {
			continue
		}
		reclining := r.maxX-r.minX >= r.maxY-r.minY && r.minY >= h*40/100 && r.maxY >= h*80/100 && r.maxY-r.minY >= h/3
		head := sample.face
		evidence := "face_foreground"
		if head != nil && r.overlap < 3 {
			continue
		}
		if head == nil && (reclining || sample.sceneForeground) {
			clear(visited)
			var best *smartCropFace
			bestY := h
			for start, v := range skin {
				if !v || visited[start] {
					continue
				}
				queue = queue[:0]
				queue = append(queue, start)
				visited[start] = true
				minX, maxX, minY, maxY := start%w, start%w, start/w, start/w
				for k := 0; k < len(queue); k++ {
					pos := queue[k]
					x, y := pos%w, pos/w
					minX = minInt(minX, x)
					maxX = maxInt(maxX, x)
					minY = minInt(minY, y)
					maxY = maxInt(maxY, y)
					for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
						for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
							n := ny*w + nx
							if skin[n] && !visited[n] {
								visited[n] = true
								queue = append(queue, n)
							}
						}
					}
				}
				rw, rh := maxX-minX+1, maxY-minY+1
				if len(queue) < 20 || len(queue)*4 < rw*rh || rw < maxInt(5, w/40) || rh < maxInt(5, h/25) || rw > w*18/100 || rh > h*30/100 || rw > rh*3 || rh > rw*3 || minY > r.minY+(r.maxY-r.minY)/2 || maxY > r.minY+(r.maxY-r.minY)*3/4 || minY >= bestY {
					continue
				}
				// The head must be near an end of the horizontal body, not a warm
				// fragment in the middle of a cushion/torso.
				if reclining && minX > r.minX+(r.maxX-r.minX)/3 && maxX < r.maxX-(r.maxX-r.minX)/3 {
					continue
				}
				// A raised peripheral hand can resemble an inferred head. Upright
				// heads need central body support; reclining heads use the end test.
				if sample.sceneStill && !reclining && ((minX+maxX)/2 < r.minX+(r.maxX-r.minX)/4 || (minX+maxX)/2 > r.maxX-(r.maxX-r.minX)/4) {
					continue
				}
				bestY = minY
				scale := maxInt(rw*srcW/w, rh*srcH/h)
				best = &smartCropFace{MinX: minX * srcW / w, MaxX: (maxX + 1) * srcW / w, MinY: minY * srcH / h, MaxY: (maxY + 1) * srcH / h, CenterX: (minX + maxX + 1) * srcW / (2 * w), CenterY: (minY + maxY + 1) * srcH / (2 * h), Scale: scale}
			}
			head = best
			evidence = "reclining_foreground_head"
			if !reclining {
				evidence = "upright_scene_foreground_head"
			}
		}
		if head == nil {
			if !sample.motionTracked && !sample.headTracked && !sample.temporalTrack {
				if reclining {
					return &smartCropSubjectExtent{Evidence: "reclining_head_unresolved"}, false
				}
				continue
			}
			evidence = "motion_foreground"
		}
		var upper *cropWindow
		if sample.sceneForeground && head != nil && !reclining {
			limit := (head.MinY + head.Scale*5/2) * h / srcH
			neutral := make([]bool, w*h)
			for _, pos := range r.positions {
				idx := pos * 3
				red, green, blue := int(pixels[idx]), int(pixels[idx+1]), int(pixels[idx+2])
				spread := maxInt(red, maxInt(green, blue)) - minInt(red, minInt(green, blue))
				neutral[pos] = pos/w <= limit && spread < 50
			}
			clear(visited)
			bestArea := 0
			center := head.CenterX * w / srcW
			for start, on := range neutral {
				if !on || visited[start] {
					continue
				}
				queue = queue[:0]
				queue = append(queue, start)
				visited[start] = true
				l, rr, upperTop, upperBottom := start%w, start%w, start/w, start/w
				for k := 0; k < len(queue); k++ {
					pos := queue[k]
					xx, yy := pos%w, pos/w
					l = minInt(l, xx)
					rr = maxInt(rr, xx)
					upperTop = minInt(upperTop, yy)
					upperBottom = maxInt(upperBottom, yy)
					for ny := maxInt(0, yy-1); ny <= minInt(h-1, yy+1); ny++ {
						for nx := maxInt(0, xx-1); nx <= minInt(w-1, xx+1); nx++ {
							np := ny*w + nx
							if neutral[np] && !visited[np] {
								visited[np] = true
								queue = append(queue, np)
							}
						}
					}
				}
				if len(queue) > bestArea && len(queue) >= w*h/500 && center >= l && center <= rr {
					bestArea = len(queue)
					upper = &cropWindow{X: l * srcW / w, W: (rr - l + 1) * srcW / w, Y: upperTop * srcH / h, H: (upperBottom - upperTop + 1) * srcH / h}
				}
			}
		}
		var pose *cropWindow
		if sample.sceneStill && head != nil && !reclining {
			pose = connectedSmartCropUprightPose(pixels, r.positions, head, w, h, srcW, srcH)
		}
		return &smartCropSubjectExtent{Pose: pose, Bounds: cropWindow{X: left * srcW / w, Y: top * srcH / h, W: int(math.Ceil(float64(right-left+1) * float64(srcW) / float64(w))), H: int(math.Ceil(float64(bottom-top+1) * float64(srcH) / float64(h)))}, Head: head, Evidence: evidence, UpperPose: upper}, true
	}
	return nil, false
}

func containSmartCropSubjectExtentX(currentX int, extent *smartCropSubjectExtent, srcW, cropW int) int {
	if extent == nil {
		return currentX
	}
	margin := maxInt(12, cropW/16)
	bounds := extent.Bounds
	x := currentX
	if bounds.W+2*margin <= cropW {
		x = clampInt(x, bounds.X+bounds.W+margin-cropW, bounds.X-margin)
	}
	if extent.Head != nil {
		x = containSmartCropFaceX(x, *extent.Head, srcW, cropW)
	}
	return roundEven(clampInt(x, 0, srcW-cropW))
}

// A neutral-colour foreground connected to the head can separate clothing,
// hands and legs from warm scene changes. Require actual head overlap and a
// continuous full-body component reaching the lower frame, not just an upper
// pose envelope. Missing, disconnected or coloured limbs stay conservative.
func connectedSmartCropUprightPose(pixels []uint8, positions []int, head *smartCropFace, w, h, srcW, srcH int) *cropWindow {
	mask := make([]bool, w*h)
	for _, pos := range positions {
		i := pos * 3
		r, g, b := int(pixels[i]), int(pixels[i+1]), int(pixels[i+2])
		mask[pos] = maxInt(r, maxInt(g, b))-minInt(r, minInt(g, b)) < 50
	}
	visited := make([]bool, w*h)
	queue := make([]int, 0, len(positions))
	var best *cropWindow
	var bestPositions []int
	bestCount := 0
	for start, on := range mask {
		if !on || visited[start] {
			continue
		}
		queue = queue[:0]
		queue = append(queue, start)
		visited[start] = true
		l, rr, t, bottom, overlap := start%w, start%w, start/w, start/w, 0
		for k := 0; k < len(queue); k++ {
			pos := queue[k]
			x, y := pos%w, pos/w
			l = minInt(l, x)
			rr = maxInt(rr, x)
			t = minInt(t, y)
			bottom = maxInt(bottom, y)
			if x*srcW/w >= head.MinX && x*srcW/w <= head.MaxX && y*srcH/h >= head.MinY && y*srcH/h <= head.MaxY {
				overlap++
			}
			for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
				for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
					n := ny*w + nx
					if mask[n] && !visited[n] {
						visited[n] = true
						queue = append(queue, n)
					}
				}
			}
		}
		if overlap < 3 || len(queue) < w*h/100 || len(queue) <= bestCount || t*srcH/h > head.MinY || bottom < h*4/5 || bottom-t < h*7/10 {
			continue
		}
		bestCount = len(queue)
		bestPositions = append(bestPositions[:0], queue...)
		best = &cropWindow{X: l * srcW / w, Y: t * srcH / h, W: (rr - l + 1) * srcW / w, H: (bottom - t + 1) * srcH / h}
	}
	if best != nil && smartCropPoseHasUnresolvedColour(pixels, positions, bestPositions, *best, maxInt(4, head.Scale*w/srcW/4), w, h, srcW, srcH) {
		return nil
	}
	return best
}

// Compact coloured foreground touching the pose can be a sleeve, hand or leg.
// Do not let a neutral-colour body estimate silently discard those fragments.
// Broad or frame-edge scene changes remain raw foreground evidence. A few
// isolated JPEG bridges are insufficient evidence of a limb connection.
func smartCropPoseHasUnresolvedColour(pixels []uint8, positions, posePositions []int, pose cropWindow, minimumContact, w, h, srcW, srcH int) bool {
	mask := make([]bool, w*h)
	for _, pos := range positions {
		i := pos * 3
		r, g, b := int(pixels[i]), int(pixels[i+1]), int(pixels[i+2])
		mask[pos] = maxInt(r, maxInt(g, b))-minInt(r, minInt(g, b)) >= 50
	}
	selected := make([]bool, w*h)
	for _, pos := range posePositions {
		selected[pos] = true
	}
	visited := make([]bool, w*h)
	q := make([]int, 0, len(positions))
	pl, pr := pose.X*w/srcW, (pose.X+pose.W)*w/srcW
	for start, on := range mask {
		if !on || visited[start] {
			continue
		}
		q = q[:0]
		q = append(q, start)
		visited[start] = true
		l, r, top, bottom := start%w, start%w, start/w, start/w
		touches := 0
		for k := 0; k < len(q); k++ {
			pos := q[k]
			x, y := pos%w, pos/w
			touchingPixel := false
			l = minInt(l, x)
			r = maxInt(r, x)
			top = minInt(top, y)
			bottom = maxInt(bottom, y)
			for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
				for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
					n := ny*w + nx
					if selected[n] {
						touchingPixel = true
					}
					if mask[n] && !visited[n] {
						visited[n] = true
						q = append(q, n)
					}
				}
			}
			if touchingPixel {
				touches++
			}
		}
		if touches >= minimumContact && len(q) >= 20 && top > 1 && bottom < h-2 && bottom-top < h/2 && (l < pl || r >= pr) && r >= pl-2 && l <= pr+2 {
			return true
		}
	}
	return false
}

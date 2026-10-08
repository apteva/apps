package main

import (
	"image"
	"math"
	"sort"
)

type smartCropSubjectExtent struct {
	Bounds    cropWindow     `json:"bounds"`
	Head      *smartCropFace `json:"head,omitempty"`
	Evidence  string         `json:"evidence"`
	UpperPose *cropWindow    `json:"upper_pose,omitempty"`
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
				l, rr := start%w, start%w
				for k := 0; k < len(queue); k++ {
					pos := queue[k]
					xx, yy := pos%w, pos/w
					l = minInt(l, xx)
					rr = maxInt(rr, xx)
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
					upper = &cropWindow{X: l * srcW / w, W: (rr - l + 1) * srcW / w}
				}
			}
		}
		return &smartCropSubjectExtent{Bounds: cropWindow{X: left * srcW / w, Y: top * srcH / h, W: int(math.Ceil(float64(right-left+1) * float64(srcW) / float64(w))), H: int(math.Ceil(float64(bottom-top+1) * float64(srcH) / float64(h)))}, Head: head, Evidence: evidence, UpperPose: upper}, true
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

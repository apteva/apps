package main

import (
	"image"
	"math"
)

// composeSmartCropPortrait refines standalone images with an upright subject
// below a large, plain, bright background. Unlike a video track, a still can
// discard headroom. Require a connected torso, warm head evidence, and clear
// space above it; saliency alone must never authorize a tighter crop.
// The bounded thumbnail mask also works when a profile/closed-eye pose evades
// the face cascade. No source download or model dependency is needed.
func composeSmartCropPortrait(img image.Image, current cropWindow, srcW, srcH int) (cropWindow, bool) {
	if img == nil || current.W >= current.H || srcW <= current.W || current.H != srcH || current.W*2 >= srcW {
		return current, false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || srcW <= 0 || srcH <= 0 || current.W <= 0 {
		return current, false
	}
	w := minInt(320, b.Dx())
	h := int(math.Round(float64(w) * float64(b.Dy()) / float64(b.Dx())))
	if w < 32 || h < 32 {
		return current, false
	}
	pixels := normalizedSmartCropRGB(img, w, h)
	active := make([]bool, w*h)
	warm := make([]bool, w*h)
	for i := range active {
		r, g, blue := int(pixels[i*3]), int(pixels[i*3+1]), int(pixels[i*3+2])
		spread := maxInt(r, maxInt(g, blue)) - minInt(r, minInt(g, blue))
		warm[i] = warmSubjectPixelWeight(r, g, blue) > 0 && float64(spread) > float64(maxInt(r, maxInt(g, blue)))*0.20
		active[i] = gray8(r, g, blue) < 155 || warm[i]
	}
	visited := make([]bool, w*h)
	queue := make([]int, 0, w*h/8)
	limit := h * 4 / 5
	thumbCropW := current.W * w / srcW
	type subject struct{ minX, maxX, minY, maxY, coreMin, coreMax, area, seed int }
	var candidates []subject
	for start := 0; start < w*limit; start++ {
		if !active[start] || visited[start] {
			continue
		}
		queue = queue[:0]
		queue = append(queue, start)
		visited[start] = true
		c := subject{minX: start % w, maxX: start % w, minY: start / w, maxY: start / w, seed: start}
		for head := 0; head < len(queue); head++ {
			pos := queue[head]
			x, y := pos%w, pos/w
			c.minX = minInt(c.minX, x)
			c.maxX = maxInt(c.maxX, x)
			c.minY = minInt(c.minY, y)
			c.maxY = maxInt(c.maxY, y)
			for ny := maxInt(0, y-1); ny <= minInt(limit-1, y+1); ny++ {
				for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
					next := ny*w + nx
					if active[next] && !visited[next] {
						visited[next] = true
						queue = append(queue, next)
					}
				}
			}
		}
		c.area = len(queue)
		height := c.maxY - c.minY + 1
		if c.minY < h/5 || c.minY > h*3/5 || c.maxY < h*3/4 || height < h*3/10 || c.area < w*h/50 {
			continue
		}
		cols := make([]int, w)
		for _, pos := range queue {
			if pos/w >= c.minY+height/2 {
				cols[pos%w]++
			}
		}
		c.coreMin = w
		c.coreMax = -1
		for x, n := range cols {
			if n >= height*3/10 {
				c.coreMin = minInt(c.coreMin, x)
				c.coreMax = maxInt(c.coreMax, x)
			}
		}
		coreW := c.coreMax - c.coreMin + 1
		if coreW < thumbCropW/5 || coreW > thumbCropW*4/5 || c.coreMin <= 1 || c.coreMax >= w-2 {
			continue
		}
		// A head-sized warm region must sit above the sustained torso columns.
		headWarm := 0
		for _, pos := range queue {
			x, y := pos%w, pos/w
			if y <= c.minY+height/4 && x >= c.coreMin-thumbCropW/6 && x <= c.coreMax+thumbCropW/6 && warm[pos] {
				headWarm++
			}
		}
		if headWarm < maxInt(20, coreW*height/40) {
			continue
		}
		empty, bright, total := 0, 0, 0
		for y := 0; y < maxInt(1, c.minY-h/25); y++ {
			for x := c.coreMin; x <= c.coreMax; x++ {
				pos := y*w + x
				total++
				if !active[pos] {
					empty++
				}
				bright += gray8(int(pixels[pos*3]), int(pixels[pos*3+1]), int(pixels[pos*3+2]))
			}
		}
		if total == 0 || empty*100 < total*95 || bright < total*175 {
			continue
		}
		candidates = append(candidates, c)
	}
	// Multiple plausible people/objects are ambiguous; keep the existing crop.
	if len(candidates) != 1 {
		return current, false
	}
	c := candidates[0]
	// Expand only this component to learn whether the lower body or a gesture
	// would be cut by zooming. A hand touching furniture conservatively disables
	// the tighter crop rather than declaring the furniture part of the torso.
	clear(visited)
	queue = queue[:0]
	queue = append(queue, c.seed)
	visited[c.seed] = true
	minX, maxX, minY, maxY := c.minX, c.maxX, c.minY, c.maxY
	for head := 0; head < len(queue); head++ {
		pos := queue[head]
		x, y := pos%w, pos/w
		minX = minInt(minX, x)
		maxX = maxInt(maxX, x)
		minY = minInt(minY, y)
		maxY = maxInt(maxY, y)
		for ny := maxInt(0, y-1); ny <= minInt(h-1, y+1); ny++ {
			for nx := maxInt(0, x-1); nx <= minInt(w-1, x+1); nx++ {
				next := ny*w + nx
				if active[next] && !visited[next] {
					visited[next] = true
					queue = append(queue, next)
				}
			}
		}
	}
	toX := func(x int) int { return x * srcW / w }
	toY := func(y int) int { return y * srcH / h }
	margin := maxInt(12, current.W/16)
	subjectLeft, subjectRight := toX(minX), toX(maxX+1)
	top, bottom := toY(minY), toY(maxY+1)
	cw, ch := current.W, current.H
	// Cap zoom at 1.5x and retain the entire connected extent plus breathing
	// room. Wide gestures retain the full-height window.
	neededH := maxInt(srcH*2/3, bottom-top+2*margin)
	neededH = maxInt(neededH, ((subjectRight-subjectLeft+2*margin)*current.H+current.W-1)/current.W)
	if neededH < ch {
		ch = roundEven(neededH + 1)
		cw = roundEven(int(math.Ceil(float64(ch)*float64(current.W)/float64(current.H))) + 1)
		if cw > current.W || ch > current.H {
			cw, ch = current.W, current.H
		}
	}
	coreLeft, coreRight := toX(c.coreMin), toX(c.coreMax+1)
	x := (coreLeft + coreRight - cw) / 2
	if subjectRight-subjectLeft+2*margin <= cw {
		x = clampInt(x, subjectRight+margin-cw, subjectLeft-margin)
	} else if subjectRight-subjectLeft <= current.W*3/2 {
		// Maximize gesture coverage while keeping the upper subject and torso safe.
		left := toX(c.minX)
		x = clampInt((subjectLeft+subjectRight-cw)/2, coreRight+margin-cw, minInt(subjectLeft, minInt(coreLeft, left))-margin)
	}
	y := 0
	if ch < srcH {
		y = clampInt(top-margin, 0, srcH-ch)
	}
	result := cropWindow{W: cw, H: ch, X: roundEven(clampInt(x, 0, srcW-cw)), Y: roundEven(clampInt(y, 0, srcH-ch))}
	return result, result != current
}

// A foreground shape must not displace an independently supported face.
func smartCropPortraitPreservesFace(win cropWindow, face *smartCropFace) bool {
	if face == nil {
		return true
	}
	margin := face.Scale / 2
	return win.X <= face.MinX-margin && win.X+win.W >= face.MaxX+margin && win.Y <= face.MinY-margin && win.Y+win.H >= face.MaxY+margin
}

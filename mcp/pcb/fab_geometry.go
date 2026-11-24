package main

import (
	"math"
	"strings"
)

// contourSeg is one piece of a closed fabrication contour: a straight line or
// a circular arc to End. Arc centres are absolute; CW selects G02 over G03.
type contourSeg struct {
	End    Point
	Arc    bool
	Center Point
	CW     bool
}

// contour is a closed path that starts and ends at Start. It carries true
// arcs so Gerber writers can emit G02/G03 instead of polyline facets, while
// DRC and rendering use polygon() as a deterministic approximation.
type contour struct {
	Start Point
	Segs  []contourSeg
}

const defaultRoundrectRatio = 0.25

func lineContour(points []Point) contour {
	if len(points) == 0 {
		return contour{}
	}
	out := contour{Start: points[0]}
	for _, point := range points[1:] {
		out.Segs = append(out.Segs, contourSeg{End: point})
	}
	out.Segs = append(out.Segs, contourSeg{End: points[0]})
	return out
}

// circleContour is one full-circle G03 arc (start == end under G75).
func circleContour(center Point, radius int64) contour {
	start := Point{XNM: center.XNM + radius, YNM: center.YNM}
	return contour{Start: start, Segs: []contourSeg{{End: start, Arc: true, Center: center}}}
}

// roundedRectContour is a counter-clockwise rectangle centred on the origin
// with corner radius r (0 = sharp, min(w,h)/2 = obround/stadium).
func roundedRectContour(w, h, r int64) contour {
	hw, hh := w/2, h/2
	if r < 0 {
		r = 0
	}
	if limit := min64(hw, hh); r > limit {
		r = limit
	}
	if r == 0 {
		return lineContour([]Point{{XNM: -hw, YNM: -hh}, {XNM: hw, YNM: -hh}, {XNM: hw, YNM: hh}, {XNM: -hw, YNM: hh}})
	}
	out := contour{Start: Point{XNM: -hw + r, YNM: -hh}}
	add := func(seg contourSeg) {
		last := out.Start
		if n := len(out.Segs); n > 0 {
			last = out.Segs[n-1].End
		}
		if !seg.Arc && last == seg.End {
			return
		}
		out.Segs = append(out.Segs, seg)
	}
	add(contourSeg{End: Point{XNM: hw - r, YNM: -hh}})
	add(contourSeg{End: Point{XNM: hw, YNM: -hh + r}, Arc: true, Center: Point{XNM: hw - r, YNM: -hh + r}})
	add(contourSeg{End: Point{XNM: hw, YNM: hh - r}})
	add(contourSeg{End: Point{XNM: hw - r, YNM: hh}, Arc: true, Center: Point{XNM: hw - r, YNM: hh - r}})
	add(contourSeg{End: Point{XNM: -hw + r, YNM: hh}})
	add(contourSeg{End: Point{XNM: -hw, YNM: hh - r}, Arc: true, Center: Point{XNM: -hw + r, YNM: hh - r}})
	add(contourSeg{End: Point{XNM: -hw, YNM: -hh + r}})
	add(contourSeg{End: Point{XNM: -hw + r, YNM: -hh}, Arc: true, Center: Point{XNM: -hw + r, YNM: -hh + r}})
	return out
}

// transform rotates (radians, counter-clockwise) and then translates. Pure
// rotation preserves arc direction, so CW flags carry over unchanged.
func (c contour) transform(angle float64, dx, dy int64) contour {
	cos, sin := math.Cos(angle), math.Sin(angle)
	move := func(p Point) Point {
		fx, fy := float64(p.XNM), float64(p.YNM)
		return Point{XNM: dx + int64(math.Round(fx*cos-fy*sin)), YNM: dy + int64(math.Round(fx*sin+fy*cos))}
	}
	if angle == 0 {
		move = func(p Point) Point { return Point{XNM: p.XNM + dx, YNM: p.YNM + dy} }
	}
	out := contour{Start: move(c.Start), Segs: make([]contourSeg, len(c.Segs))}
	for i, seg := range c.Segs {
		out.Segs[i] = contourSeg{End: move(seg.End), Arc: seg.Arc, Center: move(seg.Center), CW: seg.CW}
	}
	return out
}

// polygon flattens the contour. Arcs are sampled at most every 11.25 degrees
// (eight segments per quarter circle); the closing point is not repeated.
func (c contour) polygon() []Point {
	out := []Point{c.Start}
	current := c.Start
	for _, seg := range c.Segs {
		if seg.Arc {
			a0 := math.Atan2(float64(current.YNM-seg.Center.YNM), float64(current.XNM-seg.Center.XNM))
			a1 := math.Atan2(float64(seg.End.YNM-seg.Center.YNM), float64(seg.End.XNM-seg.Center.XNM))
			radius := math.Hypot(float64(current.XNM-seg.Center.XNM), float64(current.YNM-seg.Center.YNM))
			sweep := a1 - a0
			if seg.CW {
				for sweep >= 0 {
					sweep -= 2 * math.Pi
				}
			} else {
				for sweep <= 0 {
					sweep += 2 * math.Pi
				}
			}
			steps := int(math.Ceil(math.Abs(sweep) / (math.Pi / 16)))
			if steps < 1 {
				steps = 1
			}
			for i := 1; i < steps; i++ {
				a := a0 + sweep*float64(i)/float64(steps)
				out = append(out, Point{XNM: seg.Center.XNM + int64(math.Round(radius*math.Cos(a))), YNM: seg.Center.YNM + int64(math.Round(radius*math.Sin(a)))})
			}
		}
		out = append(out, seg.End)
		current = seg.End
	}
	if len(out) > 1 && out[len(out)-1] == out[0] {
		out = out[:len(out)-1]
	}
	return out
}

func padShapeName(pad Pad) string {
	shape := strings.ToLower(strings.TrimSpace(pad.Shape))
	if shape == "" {
		return "rect"
	}
	return shape
}

// padCornerRadius is the true corner radius of a pad shape before any mask or
// paste expansion: KiCad's roundrect convention (ratio of the short side,
// default 0.25, capped at 0.5), half the short side for oval, 0 for rect.
func padCornerRadius(pad Pad) int64 {
	short := min64(pad.WidthNM, pad.HeightNM)
	switch padShapeName(pad) {
	case "oval":
		return short / 2
	case "roundrect":
		ratio := pad.RoundrectRatio
		if ratio <= 0 {
			ratio = defaultRoundrectRatio
		}
		if ratio > 0.5 {
			ratio = 0.5
		}
		return int64(math.Round(float64(short) * ratio))
	}
	return 0
}

// padLocalContour is the exact pad outline in the component frame (pad
// offset applied, component rotation not yet applied), grown by expansion on
// every side. Negative expansion shrinks it (paste reductions).
func padLocalContour(pad Pad, expansion int64) contour {
	w, h := pad.WidthNM+2*expansion, pad.HeightNM+2*expansion
	if padShapeName(pad) == "circle" {
		return circleContour(Point{XNM: pad.XNM, YNM: pad.YNM}, min64(pad.WidthNM, pad.HeightNM)/2+expansion)
	}
	radius := padCornerRadius(pad)
	if radius > 0 || expansion < 0 {
		radius += expansion
	}
	return roundedRectContour(w, h, radius).transform(0, pad.XNM, pad.YNM)
}

func componentAngle(component Component) float64 {
	return float64(component.Position.RotationUdeg) / 1e6 * math.Pi / 180
}

// placedPadContour is the exact board-frame outline of a pad.
func placedPadContour(component Component, pad Pad, expansion int64) contour {
	return padLocalContour(pad, expansion).transform(componentAngle(component), component.Position.XNM, component.Position.YNM)
}

// boardEdgeContour is the board profile with true corner arcs. An explicit
// outline is authoritative; otherwise the width × height rectangle is rounded
// by CornerRadiusNM.
func boardEdgeContour(board Board) contour {
	if len(board.Outline) >= 3 {
		return lineContour(board.Outline)
	}
	if board.CornerRadiusNM <= 0 {
		return lineContour([]Point{{XNM: 0, YNM: 0}, {XNM: board.WidthNM, YNM: 0}, {XNM: board.WidthNM, YNM: board.HeightNM}, {XNM: 0, YNM: board.HeightNM}})
	}
	return roundedRectContour(board.WidthNM, board.HeightNM, board.CornerRadiusNM).transform(0, board.WidthNM/2, board.HeightNM/2)
}

// cutoutEdgeContour is the Edge_Cuts profile of a non-plated board cutout.
func cutoutEdgeContour(cutout BoardCutout) contour {
	switch cutout.Kind {
	case "polygon":
		return lineContour(cutout.Polygon)
	case "slot":
		angle := float64(cutout.RotationUdeg) / 1e6 * math.Pi / 180
		return roundedRectContour(cutout.WidthNM, cutout.HeightNM, min64(cutout.WidthNM, cutout.HeightNM)/2).transform(angle, cutout.XNM, cutout.YNM)
	default:
		return circleContour(Point{XNM: cutout.XNM, YNM: cutout.YNM}, cutout.DiameterNM/2)
	}
}

// boardEdgeContours lists everything drawn on Edge_Cuts for a native board.
// Plated cutouts are drilled/routed by the PTH program instead.
func boardEdgeContours(board Board) []contour {
	out := []contour{boardEdgeContour(board)}
	for _, cutout := range board.Cutouts {
		if cutout.Plated && (cutout.Kind == "slot" || cutout.Kind == "circle") {
			continue
		}
		out = append(out, cutoutEdgeContour(cutout))
	}
	return out
}

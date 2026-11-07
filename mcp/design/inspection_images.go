package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"strconv"
)

type inspectionMesh struct {
	Vertices   []float64        `json:"vertices"`
	Triangles  []uint32         `json:"triangles"`
	ID         string           `json:"id"`
	InstanceID string           `json:"instance_id"`
	Color      string           `json:"color"`
	Parts      []inspectionMesh `json:"parts"`
}

// Render portable geometry evidence with a depth buffer. Uses the exact same
// tessellated geometry as the viewer; it is an inspection image, not a drawing.
func inspectionPNGs(body []byte, release *ReleaseEvidence) (map[string][]byte, error) {
	var mesh inspectionMesh
	if err := json.Unmarshal(body, &mesh); err != nil {
		return nil, err
	}
	parts := mesh.Parts
	if len(parts) == 0 {
		parts = []inspectionMesh{mesh}
	}
	views := []InspectionView{{Name: "Front", Direction: "front"}, {Name: "Top", Direction: "top"}, {Name: "Side", Direction: "side"}}
	if release != nil && len(release.Views) > 0 {
		views = release.Views
	}
	out := map[string][]byte{}
	const width, height = 1000, 800
	for index, view := range views {
		x, y, z, sign := 0, 2, 1, 1.0
		switch view.Direction {
		case "front":
		case "top":
			x, y, z, sign = 0, 1, 2, -1
		case "side":
			x, y, z, sign = 1, 2, 0, -1
		default:
			return nil, fmt.Errorf("inspection direction %s is unsupported; use front/top/side", view.Direction)
		}
		selected := func(p inspectionMesh) bool {
			if len(view.Instances) == 0 {
				return true
			}
			for _, id := range view.Instances {
				if id == p.ID || id == p.InstanceID {
					return true
				}
			}
			return false
		}
		min, max := [2]float64{math.Inf(1), math.Inf(1)}, [2]float64{math.Inf(-1), math.Inf(-1)}
		for _, p := range parts {
			if !selected(p) {
				continue
			}
			for i := 0; i+2 < len(p.Vertices); i += 3 {
				for k, a := range []int{x, y} {
					min[k] = math.Min(min[k], p.Vertices[i+a])
					max[k] = math.Max(max[k], p.Vertices[i+a])
				}
			}
		}
		if !finiteImage(min[0]) || max[0] <= min[0] || max[1] <= min[1] {
			continue
		}
		scale := math.Min((width-40)/(max[0]-min[0]), (height-40)/(max[1]-min[1]))
		cx, cy := (min[0]+max[0])/2, (min[1]+max[1])/2
		img := image.NewRGBA(image.Rect(0, 0, width, height))
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 11, 16, 26, 255
		}
		depth := make([]float64, width*height)
		for i := range depth {
			depth[i] = math.Inf(1)
		}
		clipAxis := -1
		switch view.SectionAxis {
		case "x", "0":
			clipAxis = 0
		case "y", "1":
			clipAxis = 1
		case "z", "2":
			clipAxis = 2
		case "":
		default:
			return nil, fmt.Errorf("invalid inspection section axis")
		}
		for _, part := range parts {
			if !selected(part) {
				continue
			}
			rgb := color.RGBA{100, 116, 139, 255}
			if len(part.Color) == 7 && part.Color[0] == '#' {
				if n, err := strconv.ParseUint(part.Color[1:], 16, 24); err == nil {
					rgb = color.RGBA{uint8(n >> 16), uint8(n >> 8), uint8(n), 255}
				}
			}
			for t := 0; t+2 < len(part.Triangles); t += 3 {
				polygon := make([][3]float64, 0, 4)
				for c := 0; c < 3; c++ {
					i := int(part.Triangles[t+c]) * 3
					if i+2 >= len(part.Vertices) {
						return nil, fmt.Errorf("invalid mesh index in inspection image")
					}
					polygon = append(polygon, [3]float64{part.Vertices[i], part.Vertices[i+1], part.Vertices[i+2]})
				}
				if clipAxis >= 0 {
					clipped := [][3]float64{}
					for i, a := range polygon {
						b := polygon[(i+1)%len(polygon)]
						ain, bin := a[clipAxis] >= view.SectionMM, b[clipAxis] >= view.SectionMM
						if ain {
							clipped = append(clipped, a)
						}
						if ain != bin {
							ratio := (view.SectionMM - a[clipAxis]) / (b[clipAxis] - a[clipAxis])
							p := a
							for k := 0; k < 3; k++ {
								p[k] = a[k] + ratio*(b[k]-a[k])
							}
							clipped = append(clipped, p)
						}
					}
					polygon = clipped
				}
				if len(polygon) < 3 {
					continue
				}
				for n := 1; n+1 < len(polygon); n++ {
					points := [3][3]float64{polygon[0], polygon[n], polygon[n+1]}
					screen := [3][3]float64{}
					for k, p := range points {
						screen[k] = [3]float64{width/2 + (p[x]-cx)*scale, height/2 - (p[y]-cy)*scale, p[z] * sign}
					}
					edge1, edge2 := [3]float64{}, [3]float64{}
					for k := 0; k < 3; k++ {
						edge1[k] = points[1][k] - points[0][k]
						edge2[k] = points[2][k] - points[0][k]
					}
					normal := [3]float64{edge1[1]*edge2[2] - edge1[2]*edge2[1], edge1[2]*edge2[0] - edge1[0]*edge2[2], edge1[0]*edge2[1] - edge1[1]*edge2[0]}
					length := math.Sqrt(normal[0]*normal[0] + normal[1]*normal[1] + normal[2]*normal[2])
					shade := .65
					if length > 0 {
						shade = .45 + .55*math.Abs((normal[0]*.3+normal[1]*.4+normal[2]*.866)/length)
					}
					c := color.RGBA{uint8(float64(rgb.R) * shade), uint8(float64(rgb.G) * shade), uint8(float64(rgb.B) * shade), 255}
					rasterTriangle(img, depth, screen, c)
				}
			}
		}
		var buffer bytes.Buffer
		if err := png.Encode(&buffer, img); err != nil {
			return nil, err
		}
		out[fmt.Sprintf("%02d-%s.png", index, view.Direction)] = buffer.Bytes()
	}
	return out, nil
}
func finiteImage(v float64) bool { return !math.IsNaN(v) && !math.IsInf(v, 0) }
func rasterTriangle(img *image.RGBA, depth []float64, p [3][3]float64, c color.RGBA) {
	edge := func(a, b [3]float64, x, y float64) float64 { return (x-a[0])*(b[1]-a[1]) - (y-a[1])*(b[0]-a[0]) }
	area := edge(p[0], p[1], p[2][0], p[2][1])
	if math.Abs(area) < 1e-10 {
		return
	}
	xmin, xmax := math.Max(0, math.Floor(math.Min(p[0][0], math.Min(p[1][0], p[2][0])))), math.Min(float64(img.Bounds().Dx()-1), math.Ceil(math.Max(p[0][0], math.Max(p[1][0], p[2][0]))))
	ymin, ymax := math.Max(0, math.Floor(math.Min(p[0][1], math.Min(p[1][1], p[2][1])))), math.Min(float64(img.Bounds().Dy()-1), math.Ceil(math.Max(p[0][1], math.Max(p[1][1], p[2][1]))))
	for y := int(ymin); y <= int(ymax); y++ {
		for x := int(xmin); x <= int(xmax); x++ {
			a := edge(p[1], p[2], float64(x)+.5, float64(y)+.5) / area
			b := edge(p[2], p[0], float64(x)+.5, float64(y)+.5) / area
			cc := 1 - a - b
			if a < 0 || b < 0 || cc < 0 {
				continue
			}
			z := a*p[0][2] + b*p[1][2] + cc*p[2][2]
			i := y*img.Bounds().Dx() + x
			if z < depth[i] {
				depth[i] = z
				img.SetRGBA(x, y, c)
			}
		}
	}
}

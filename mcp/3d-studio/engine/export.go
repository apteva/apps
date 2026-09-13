package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
)

// GLB exports flat-shaded geometry with one material and centered pivot per node.
func GLB(d Document) ([]byte, error) {
	triangles, err := Triangles(d)
	if err != nil {
		return nil, err
	}
	if len(triangles) == 0 {
		return nil, errors.New("nothing to export")
	}
	var bin bytes.Buffer
	views := []any{}
	accessors := []any{}
	meshes := []any{}
	nodes := []any{}
	materials := []any{}
	sceneNodes := []int{}
	for _, node := range d.Nodes {
		tris := []Triangle{}
		for _, t := range triangles {
			if t.NodeID == node.ID {
				tris = append(tris, t)
			}
		}
		if len(tris) == 0 {
			continue
		}
		low, high := tris[0].A, tris[0].A
		for _, t := range tris {
			for _, p := range []Vec{t.A, t.B, t.C} {
				for k, x := range p {
					low[k] = math.Min(low[k], x)
					high[k] = math.Max(high[k], x)
				}
			}
		}
		center := low.Add(high).Mul(.5)
		offset := bin.Len()
		for _, t := range tris {
			for _, p := range []Vec{t.A, t.B, t.C} {
				p = p.Sub(center)
				for _, x := range p {
					_ = binary.Write(&bin, binary.LittleEndian, float32(x))
				}
			}
		}
		pi := len(accessors)
		vi := len(views)
		views = append(views, map[string]any{"buffer": 0, "byteOffset": offset, "byteLength": bin.Len() - offset, "target": 34962})
		accessors = append(accessors, map[string]any{"bufferView": vi, "componentType": 5126, "count": len(tris) * 3, "type": "VEC3", "min": floatBounds(low.Sub(center)), "max": floatBounds(high.Sub(center))})
		offset = bin.Len()
		for _, t := range tris {
			for i := 0; i < 3; i++ {
				for _, x := range t.Normal {
					_ = binary.Write(&bin, binary.LittleEndian, float32(x))
				}
			}
		}
		ni := len(accessors)
		vi = len(views)
		views = append(views, map[string]any{"buffer": 0, "byteOffset": offset, "byteLength": bin.Len() - offset, "target": 34962})
		accessors = append(accessors, map[string]any{"bufferView": vi, "componentType": 5126, "count": len(tris) * 3, "type": "VEC3"})
		mi := len(materials)
		materials = append(materials, map[string]any{"name": node.Name, "pbrMetallicRoughness": map[string]any{"baseColorFactor": [4]float64{node.Color[0], node.Color[1], node.Color[2], 1}, "metallicFactor": 0, "roughnessFactor": .75}, "doubleSided": true})
		meshes = append(meshes, map[string]any{"name": node.Name, "primitives": []any{map[string]any{"attributes": map[string]int{"POSITION": pi, "NORMAL": ni}, "material": mi, "mode": 4}}})
		sceneNodes = append(sceneNodes, len(nodes))
		nodes = append(nodes, map[string]any{"name": node.ID, "mesh": len(meshes) - 1, "translation": center})
	}
	root := map[string]any{"asset": map[string]any{"version": "2.0", "generator": "Apteva 3D Studio " + Version}, "scene": 0, "scenes": []any{map[string]any{"nodes": sceneNodes}}, "nodes": nodes, "meshes": meshes, "materials": materials, "buffers": []any{map[string]any{"byteLength": bin.Len()}}, "bufferViews": views, "accessors": accessors}
	metadata, err := json.Marshal(root)
	if err != nil {
		return nil, err
	}
	for len(metadata)%4 != 0 {
		metadata = append(metadata, ' ')
	}
	var out bytes.Buffer
	for _, x := range []uint32{0x46546c67, 2, uint32(12 + 8 + len(metadata) + 8 + bin.Len()), uint32(len(metadata)), 0x4e4f534a} {
		_ = binary.Write(&out, binary.LittleEndian, x)
	}
	out.Write(metadata)
	_ = binary.Write(&out, binary.LittleEndian, uint32(bin.Len()))
	_ = binary.Write(&out, binary.LittleEndian, uint32(0x004e4942))
	out.Write(bin.Bytes())
	return out.Bytes(), nil
}

// RenderPNG provides bounded, native Go visual feedback for unattended agents.
// It is an orthographic z-buffer rasterizer, not a browser/runtime dependency.
func RenderPNG(d Document, view string, selection *Selection) ([]byte, error) {
	triangles, err := Triangles(d)
	if err != nil {
		return nil, err
	}
	if len(triangles) == 0 {
		return nil, errors.New("nothing to render")
	}
	report, _ := Validate(d)
	yaw, pitch := .7, .4
	switch view {
	case "", "perspective":
	case "front":
		yaw = 0
		pitch = 0
	case "side":
		yaw = math.Pi / 2
		pitch = 0
	case "top":
		yaw = 0
		pitch = math.Pi / 2
	default:
		return nil, fmt.Errorf("unknown view %q", view)
	}
	const size = 640
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			img.SetRGBA(x, y, color.RGBA{19, 24, 34, 255})
		}
	}
	depth := make([]float64, size*size)
	for i := range depth {
		depth[i] = math.Inf(-1)
	}
	center := report.Bounds[0].Add(report.Bounds[1]).Mul(.5)
	extent := report.Bounds[1].Sub(report.Bounds[0]).Len()
	scale := float64(size) * .8 / math.Max(extent, .01)
	project := func(p Vec) Vec {
		p = p.Sub(center)
		x, z := p[0]*math.Cos(yaw)+p[2]*math.Sin(yaw), -p[0]*math.Sin(yaw)+p[2]*math.Cos(yaw)
		y, z2 := p[1]*math.Cos(pitch)-z*math.Sin(pitch), p[1]*math.Sin(pitch)+z*math.Cos(pitch)
		return Vec{size/2 + x*scale, size/2 - y*scale, z2}
	}
	selected := map[string]bool{}
	if selection != nil {
		selected = idSet(selection.IDs)
	}
	light := Vec{-.4, .8, 1}.Unit()
	rasterPixels := 0
	for _, t := range triangles {
		a, b, c := project(t.A), project(t.B), project(t.C)
		area := (b[1]-c[1])*(a[0]-c[0]) + (c[0]-b[0])*(a[1]-c[1])
		if math.Abs(area) < 1e-9 {
			continue
		}
		xmin := int(math.Max(0, math.Floor(math.Min(a[0], math.Min(b[0], c[0])))))
		xmax := int(math.Min(size-1, math.Ceil(math.Max(a[0], math.Max(b[0], c[0])))))
		ymin := int(math.Max(0, math.Floor(math.Min(a[1], math.Min(b[1], c[1])))))
		ymax := int(math.Min(size-1, math.Ceil(math.Max(a[1], math.Max(b[1], c[1])))))
		rasterPixels += (xmax - xmin + 1) * (ymax - ymin + 1)
		if rasterPixels > 50000000 {
			return nil, errors.New("render exceeds pixel budget; simplify overlapping geometry")
		}
		tint := t.Color
		if selection != nil && selection.NodeID == t.NodeID && selection.Kind == "face" && selected[t.FaceID] {
			tint = Vec{1, .65, .15}
		}
		shade := .45 + .55*math.Abs(t.Normal.Dot(light))
		rgb := color.RGBA{uint8(math.Pow(tint[0]*shade, 1/2.2) * 255), uint8(math.Pow(tint[1]*shade, 1/2.2) * 255), uint8(math.Pow(tint[2]*shade, 1/2.2) * 255), 255}
		for y := ymin; y <= ymax; y++ {
			for x := xmin; x <= xmax; x++ {
				px, py := float64(x)+.5, float64(y)+.5
				u := ((b[1]-c[1])*(px-c[0]) + (c[0]-b[0])*(py-c[1])) / area
				v := ((c[1]-a[1])*(px-c[0]) + (a[0]-c[0])*(py-c[1])) / area
				w := 1 - u - v
				if u < 0 || v < 0 || w < 0 {
					continue
				}
				z := u*a[2] + v*b[2] + w*c[2]
				i := y*size + x
				if z > depth[i] {
					depth[i] = z
					img.SetRGBA(x, y, rgb)
				}
			}
		}
	}
	if selection != nil && selection.Kind == "vertex" {
		n, e := d.Node(selection.NodeID)
		if e == nil {
			for _, v := range n.Mesh.Vertices {
				if !selected[v.ID] {
					continue
				}
				p := project(v.Position)
				x, y := int(p[0]), int(p[1])
				if x < 0 || x >= size || y < 0 || y >= size || p[2] < depth[y*size+x]-.01 {
					continue
				}
				for dy := -3; dy <= 3; dy++ {
					for dx := -3; dx <= 3; dx++ {
						img.SetRGBA(x+dx, y+dy, color.RGBA{255, 195, 60, 255})
					}
				}
			}
		}
	}
	var out bytes.Buffer
	err = png.Encode(&out, img)
	return out.Bytes(), err
}

func floatBounds(v Vec) Vec {
	for i, x := range v {
		v[i] = float64(float32(x))
	}
	return v
}

package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
)

type meshDocument struct {
	Triangles []uint32   `json:"triangles"`
	Vertices  []float64  `json:"vertices"`
	Normals   []float64  `json:"normals"`
	Parts     []meshPart `json:"parts,omitempty"`
}

// meshPart is one visual of an assembly preview mesh (runner mesh-json).
type meshPart struct {
	ID         string    `json:"id"`
	PartID     string    `json:"part_id"`
	Name       string    `json:"name"`
	Color      string    `json:"color"`
	MaterialID string    `json:"material_id,omitempty"`
	Visible    *bool     `json:"visible,omitempty"`
	Triangles  []uint32  `json:"triangles"`
	Vertices   []float64 `json:"vertices"`
}

// hexRGBA parses #rrggbb into linear-ish 0..1 floats; ok=false for anything else.
func hexRGBA(value string) ([4]float64, bool) {
	if len(value) != 7 || value[0] != '#' {
		return [4]float64{}, false
	}
	parsed, err := strconv.ParseUint(value[1:], 16, 32)
	if err != nil {
		return [4]float64{}, false
	}
	srgb := func(channel uint64) float64 {
		c := float64(channel) / 255
		if c <= 0.04045 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return [4]float64{srgb(parsed >> 16 & 0xff), srgb(parsed >> 8 & 0xff), srgb(parsed & 0xff), 1}, true
}

func parseMesh(body []byte) (*meshDocument, error) {
	var mesh meshDocument
	if err := json.Unmarshal(body, &mesh); err != nil {
		return nil, fmt.Errorf("parse mesh: %w", err)
	}
	if len(mesh.Vertices) == 0 || len(mesh.Vertices)%3 != 0 {
		return nil, errors.New("mesh vertices are empty or malformed")
	}
	if len(mesh.Triangles) == 0 || len(mesh.Triangles)%3 != 0 {
		return nil, errors.New("mesh triangles are empty or malformed")
	}
	vertexCount := uint32(len(mesh.Vertices) / 3)
	for _, index := range mesh.Triangles {
		if index >= vertexCount {
			return nil, errors.New("mesh triangle index out of range")
		}
	}
	return &mesh, nil
}

// meshTo3MF writes one mesh object. name and color (#rrggbb) are optional;
// a colour is written as a core-spec base material so slicers show it.
func meshTo3MF(body []byte, extra ...string) ([]byte, error) {
	name, color, material := "Apteva Design", "", "Material"
	if len(extra) > 0 && extra[0] != "" {
		name = extra[0]
	}
	if len(extra) > 1 {
		color = extra[1]
	}
	if len(extra) > 2 && extra[2] != "" {
		material = extra[2]
	}
	mesh, err := parseMesh(body)
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	write := func(name, content string) error {
		entry, err := archive.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = entry.Write([]byte(content))
		return err
	}
	if err := write("[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types">
  <Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/>
  <Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/>
</Types>`); err != nil {
		return nil, err
	}
	if err := write("_rels/.rels", `<?xml version="1.0" encoding="UTF-8"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships">
  <Relationship Target="/3D/3dmodel.model" Id="rel0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/>
</Relationships>`); err != nil {
		return nil, err
	}
	model, err := archive.CreateHeader(&zip.FileHeader{Name: "3D/3dmodel.model", Method: zip.Deflate})
	if err != nil {
		return nil, err
	}
	_, _ = model.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?>
<model unit="millimeter" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02">
  <metadata name="Title">` + xmlEscape(name) + `</metadata>
  <resources>`))
	if _, ok := hexRGBA(color); ok {
		fmt.Fprintf(model, `<basematerials id="2"><base name="%s" displaycolor="%sFF"/></basematerials><object id="1" type="model" name="%s" pid="2" pindex="0"><mesh><vertices>`, xmlEscape(material), strings.ToUpper(color), xmlEscape(name))
	} else {
		fmt.Fprintf(model, `<object id="1" type="model" name="%s"><mesh><vertices>`, xmlEscape(name))
	}
	for index := 0; index < len(mesh.Vertices); index += 3 {
		fmt.Fprintf(model, `<vertex x="%s" y="%s" z="%s"/>`,
			floatString(mesh.Vertices[index]), floatString(mesh.Vertices[index+1]), floatString(mesh.Vertices[index+2]))
	}
	_, _ = model.Write([]byte(`</vertices><triangles>`))
	for index := 0; index < len(mesh.Triangles); index += 3 {
		fmt.Fprintf(model, `<triangle v1="%d" v2="%d" v3="%d"/>`,
			mesh.Triangles[index], mesh.Triangles[index+1], mesh.Triangles[index+2])
	}
	_, _ = model.Write([]byte(`</triangles></mesh></object></resources><build><item objectid="1"/></build></model>`))
	if err := archive.Close(); err != nil {
		return nil, err
	}
	return output.Bytes(), nil
}

func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func floatString(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

type glbPrimitive struct {
	name      string
	color     [4]float64
	material  string
	vertices  []float64
	triangles []uint32
}

// meshToGLB writes a binary glTF. When the mesh carries per-part visuals,
// each visible part becomes its own node with a PBR material in the part's
// colour; otherwise one grey mesh is written.
func meshToGLB(body []byte) ([]byte, error) {
	mesh, err := parseMesh(body)
	if err != nil {
		return nil, err
	}
	primitives := []glbPrimitive{}
	for _, part := range mesh.Parts {
		if part.Visible != nil && !*part.Visible {
			continue
		}
		if len(part.Vertices) == 0 || len(part.Vertices)%3 != 0 || len(part.Triangles) == 0 || len(part.Triangles)%3 != 0 {
			continue
		}
		count := uint32(len(part.Vertices) / 3)
		valid := true
		for _, index := range part.Triangles {
			if index >= count {
				valid = false
				break
			}
		}
		if !valid {
			return nil, fmt.Errorf("mesh part %s has a triangle index out of range", part.ID)
		}
		color, ok := hexRGBA(part.Color)
		if !ok {
			color, _ = hexRGBA("#64748b")
		}
		name := part.Name
		if name == "" {
			name = part.ID
		}
		primitives = append(primitives, glbPrimitive{name: name, color: color, material: part.MaterialID, vertices: part.Vertices, triangles: part.Triangles})
	}
	if len(primitives) == 0 {
		grey, _ := hexRGBA("#64748b")
		primitives = append(primitives, glbPrimitive{name: "Design", color: grey, vertices: mesh.Vertices, triangles: mesh.Triangles})
	}
	var bin []byte
	bufferViews, accessors, meshes, nodes, materials := []any{}, []any{}, []any{}, []any{}, []any{}
	materialIndex := map[string]int{}
	for _, primitive := range primitives {
		min := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
		max := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
		positionOffset := len(bin)
		positions := make([]byte, len(primitive.vertices)*4)
		for index, value := range primitive.vertices {
			binary.LittleEndian.PutUint32(positions[index*4:], math.Float32bits(float32(value)))
			axis := index % 3
			min[axis], max[axis] = math.Min(min[axis], value), math.Max(max[axis], value)
		}
		bin = append(bin, positions...)
		bin = append(bin, make([]byte, align4(len(bin))-len(bin))...)
		indexOffset := len(bin)
		indices := make([]byte, len(primitive.triangles)*4)
		for index, value := range primitive.triangles {
			binary.LittleEndian.PutUint32(indices[index*4:], value)
		}
		bin = append(bin, indices...)
		bin = append(bin, make([]byte, align4(len(bin))-len(bin))...)
		bufferViews = append(bufferViews,
			map[string]any{"buffer": 0, "byteOffset": positionOffset, "byteLength": len(positions), "target": 34962},
			map[string]any{"buffer": 0, "byteOffset": indexOffset, "byteLength": len(indices), "target": 34963},
		)
		accessors = append(accessors,
			map[string]any{"bufferView": len(bufferViews) - 2, "componentType": 5126, "count": len(primitive.vertices) / 3, "type": "VEC3", "min": min, "max": max},
			map[string]any{"bufferView": len(bufferViews) - 1, "componentType": 5125, "count": len(primitive.triangles), "type": "SCALAR"},
		)
		key := fmt.Sprintf("%s|%v", primitive.material, primitive.color)
		material, seen := materialIndex[key]
		if !seen {
			name := primitive.material
			if name == "" {
				name = primitive.name
			}
			material = len(materials)
			materialIndex[key] = material
			materials = append(materials, map[string]any{"name": name, "pbrMetallicRoughness": map[string]any{
				"baseColorFactor": primitive.color, "metallicFactor": 0, "roughnessFactor": 0.6,
			}})
		}
		meshes = append(meshes, map[string]any{"name": primitive.name, "primitives": []any{map[string]any{
			"attributes": map[string]int{"POSITION": len(accessors) - 2}, "indices": len(accessors) - 1, "material": material,
		}}})
		nodes = append(nodes, map[string]any{"name": primitive.name, "mesh": len(meshes) - 1})
	}
	sceneNodes := make([]int, len(nodes))
	for index := range nodes {
		sceneNodes[index] = index
	}
	document := map[string]any{
		"asset":       map[string]any{"version": "2.0", "generator": "Apteva Design Studio"},
		"scene":       0,
		"scenes":      []any{map[string]any{"nodes": sceneNodes}},
		"nodes":       nodes,
		"meshes":      meshes,
		"materials":   materials,
		"buffers":     []any{map[string]any{"byteLength": len(bin)}},
		"bufferViews": bufferViews,
		"accessors":   accessors,
	}
	jsonBody, _ := json.Marshal(document)
	jsonLength := align4(len(jsonBody))
	binLength := align4(len(bin))
	total := 12 + 8 + jsonLength + 8 + binLength
	output := make([]byte, total)
	copy(output[:4], []byte("glTF"))
	binary.LittleEndian.PutUint32(output[4:8], 2)
	binary.LittleEndian.PutUint32(output[8:12], uint32(total))
	offset := 12
	binary.LittleEndian.PutUint32(output[offset:offset+4], uint32(jsonLength))
	copy(output[offset+4:offset+8], []byte("JSON"))
	for index := offset + 8; index < offset+8+jsonLength; index++ {
		output[index] = ' '
	}
	copy(output[offset+8:], jsonBody)
	offset += 8 + jsonLength
	binary.LittleEndian.PutUint32(output[offset:offset+4], uint32(binLength))
	copy(output[offset+4:offset+8], []byte("BIN\x00"))
	copy(output[offset+8:], bin)
	return output, nil
}

func align4(value int) int { return (value + 3) &^ 3 }

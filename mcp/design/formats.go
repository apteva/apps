package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
)

type meshDocument struct {
	Triangles []uint32  `json:"triangles"`
	Vertices  []float64 `json:"vertices"`
	Normals   []float64 `json:"normals"`
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

func meshTo3MF(body []byte) ([]byte, error) {
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
  <metadata name="Title">Apteva Design</metadata>
  <resources><object id="1" type="model"><mesh><vertices>`))
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

func floatString(value float64) string {
	return strconv.FormatFloat(value, 'g', -1, 64)
}

func meshToGLB(body []byte) ([]byte, error) {
	mesh, err := parseMesh(body)
	if err != nil {
		return nil, err
	}
	positionBytes := make([]byte, len(mesh.Vertices)*4)
	min := [3]float64{math.Inf(1), math.Inf(1), math.Inf(1)}
	max := [3]float64{math.Inf(-1), math.Inf(-1), math.Inf(-1)}
	for index, value := range mesh.Vertices {
		binary.LittleEndian.PutUint32(positionBytes[index*4:], math.Float32bits(float32(value)))
		axis := index % 3
		if value < min[axis] {
			min[axis] = value
		}
		if value > max[axis] {
			max[axis] = value
		}
	}
	indexOffset := align4(len(positionBytes))
	bin := make([]byte, indexOffset+len(mesh.Triangles)*4)
	copy(bin, positionBytes)
	for index, value := range mesh.Triangles {
		binary.LittleEndian.PutUint32(bin[indexOffset+index*4:], value)
	}
	document := map[string]any{
		"asset":  map[string]any{"version": "2.0", "generator": "Apteva Design Studio"},
		"scene":  0,
		"scenes": []any{map[string]any{"nodes": []int{0}}},
		"nodes":  []any{map[string]any{"mesh": 0}},
		"meshes": []any{map[string]any{"primitives": []any{map[string]any{
			"attributes": map[string]int{"POSITION": 0}, "indices": 1,
		}}}},
		"buffers": []any{map[string]any{"byteLength": len(bin)}},
		"bufferViews": []any{
			map[string]any{"buffer": 0, "byteOffset": 0, "byteLength": len(positionBytes), "target": 34962},
			map[string]any{"buffer": 0, "byteOffset": indexOffset, "byteLength": len(mesh.Triangles) * 4, "target": 34963},
		},
		"accessors": []any{
			map[string]any{"bufferView": 0, "componentType": 5126, "count": len(mesh.Vertices) / 3, "type": "VEC3", "min": min, "max": max},
			map[string]any{"bufferView": 1, "componentType": 5125, "count": len(mesh.Triangles), "type": "SCALAR"},
		},
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

package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"testing"
)

var triangleMesh = []byte(`{"vertices":[0,0,0,10,0,0,0,10,0],"triangles":[0,1,2]}`)

func TestMeshTo3MF(t *testing.T) {
	body, err := meshTo3MF(triangleMesh)
	if err != nil {
		t.Fatal(err)
	}
	archive, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatal(err)
	}
	found := map[string]bool{}
	for _, file := range archive.File {
		found[file.Name] = true
	}
	for _, name := range []string{"[Content_Types].xml", "_rels/.rels", "3D/3dmodel.model"} {
		if !found[name] {
			t.Errorf("3MF missing %s", name)
		}
	}
}

func TestMeshToGLB(t *testing.T) {
	body, err := meshToGLB(triangleMesh)
	if err != nil {
		t.Fatal(err)
	}
	if string(body[:4]) != "glTF" || binary.LittleEndian.Uint32(body[4:8]) != 2 {
		t.Fatalf("invalid GLB header: %x", body[:12])
	}
	if int(binary.LittleEndian.Uint32(body[8:12])) != len(body) {
		t.Fatalf("GLB header length mismatch")
	}
}

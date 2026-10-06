package engine

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"image/png"
	"math"
	"testing"
)

func box(t *testing.T) Document {
	t.Helper()
	r, err := Evaluate(EditRequest{Document: Empty(), Commands: []Command{{Op: "primitive.add", NodeID: "body", Shape: "box"}}})
	if err != nil {
		t.Fatal(err)
	}
	return r.Document
}
func roof(t *testing.T, d Document) Selection {
	t.Helper()
	s, err := Select(d, Query{NodeID: "body", Kind: "face", Normal: ptr(Vec{0, 1, 0})})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.IDs) != 1 {
		t.Fatalf("expected single upward face: %+v", s)
	}
	return s
}
func TestExtrudeRegionAndSelectionChain(t *testing.T) {
	d := box(t)
	before, _ := json.Marshal(d)
	s := roof(t, d)
	r, err := Evaluate(EditRequest{Document: d, Selections: map[string]Selection{"roof": s}, Commands: []Command{{Op: "extrude", NodeID: "body", Selection: "roof", Direction: ptr(Vec{0, 1, 0}), Distance: .5, ResultSelection: "cap"}, {Op: "transform", NodeID: "body", Selection: "cap", Scale: ptr(Vec{.6, 1, .6})}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.BoundaryEdges != 0 || r.Report.Vertices != 12 || r.Report.Faces != 10 || r.Report.Bounds[1][1] != 1 {
		t.Fatalf("unexpected extrusion: %+v", r.Report)
	}
	if _, ok := r.Selections["roof"]; ok {
		t.Fatal("old face selection should be invalidated")
	}
	if len(r.Selections["cap"].IDs) != 1 {
		t.Fatal("missing cap selection")
	}
	after, _ := json.Marshal(d)
	if !bytes.Equal(before, after) {
		t.Fatal("input mutated")
	}
}
func TestAdjacentExtrusionDoesNotCreateInternalWalls(t *testing.T) {
	d := box(t)
	s, err := Select(d, Query{NodeID: "body", Kind: "face", IDs: []string{d.Nodes[0].Mesh.Faces[3].ID, d.Nodes[0].Mesh.Faces[5].ID}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := Evaluate(EditRequest{Document: d, Selections: map[string]Selection{"region": s}, Commands: []Command{{Op: "extrude", NodeID: "body", Selection: "region", Direction: ptr(Vec{1, 1, 0}), Distance: .4}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.BoundaryEdges != 0 || r.Report.Faces != 12 {
		t.Fatalf("internal wall or missing boundary: %+v", r.Report)
	}
}
func TestAtomicFailureAndStaleSelection(t *testing.T) {
	d := box(t)
	before, _ := json.Marshal(d)
	_, err := Evaluate(EditRequest{Document: d, Commands: []Command{{Op: "transform", NodeID: "body", Translation: ptr(Vec{1, 0, 0})}, {Op: "vertices.patch", NodeID: "body", Positions: []Patch{{"missing", Vec{}}}}}})
	if err == nil {
		t.Fatal("missing vertex accepted")
	}
	after, _ := json.Marshal(d)
	if !bytes.Equal(before, after) {
		t.Fatal("failed batch mutated input")
	}
	_, err = Evaluate(EditRequest{Document: d, Selections: map[string]Selection{"bad": {NodeID: "body", Kind: "face", IDs: []string{"missing"}}}, Commands: []Command{{Op: "transform", NodeID: "body", Selection: "bad"}}})
	if err == nil {
		t.Fatal("stale selection accepted")
	}
}
func TestProportionalConnectedOnly(t *testing.T) {
	d := box(t)
	m := &d.Nodes[0].Mesh
	a, b, c := m.vertex(Vec{.5, .51, .5}), m.vertex(Vec{.6, .51, .5}), m.vertex(Vec{.5, .61, .5})
	m.face([]string{a, b, c})
	s := Selection{NodeID: "body", Kind: "vertex", IDs: []string{m.Vertices[6].ID}}
	r, err := Evaluate(EditRequest{Document: d, Selections: map[string]Selection{"point": s}, Commands: []Command{{Op: "transform", NodeID: "body", Selection: "point", Translation: ptr(Vec{0, .1, 0}), Radius: 2, ConnectedOnly: true}}})
	if err != nil {
		t.Fatal(err)
	}
	p := r.Document.Nodes[0].Mesh.Positions()
	if p[a] != m.Positions()[a] {
		t.Fatal("disconnected component moved")
	}
	if p[m.Vertices[7].ID][1] <= .5 {
		t.Fatal("connected neighbor did not move")
	}
}
func TestInsetMirrorAndCarExport(t *testing.T) {
	d, err := ExampleCar()
	if err != nil {
		t.Fatal(err)
	}
	report, err := Validate(d)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Nodes) != 10 || report.Triangles > 2000 || report.BoundaryEdges != 0 {
		t.Fatalf("car does not meet target: %+v nodes=%d", report, len(d.Nodes))
	}
	r, err := Evaluate(EditRequest{Document: box(t), Commands: []Command{{Op: "mesh.mirror", NodeID: "body", TargetID: "other", Axis: "x", Offset: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	if r.Report.BoundaryEdges != 0 {
		t.Fatal("mirror broke winding")
	}
	glb, err := GLB(d)
	if err != nil {
		t.Fatal(err)
	}
	if binary.LittleEndian.Uint32(glb[:4]) != 0x46546c67 || int(binary.LittleEndian.Uint32(glb[8:12])) != len(glb) {
		t.Fatal("invalid GLB header")
	}
	length := int(binary.LittleEndian.Uint32(glb[12:16]))
	var root struct {
		Nodes     []map[string]any `json:"nodes"`
		Accessors []struct {
			Count int `json:"count"`
		} `json:"accessors"`
	}
	if err = json.Unmarshal(glb[20:20+length], &root); err != nil {
		t.Fatal(err)
	}
	if len(root.Nodes) != 10 {
		t.Fatal("export lost nodes")
	}
	triangles := 0
	for i, a := range root.Accessors {
		if i%2 == 0 {
			triangles += a.Count / 3
		}
	}
	if triangles != report.Triangles {
		t.Fatal("export changed triangle count")
	}
	for _, view := range []string{"perspective", "front", "side", "top"} {
		data, err := RenderPNG(d, view, nil)
		if err != nil {
			t.Fatal(err)
		}
		img, err := png.Decode(bytes.NewReader(data))
		if err != nil || img.Bounds().Dx() != 640 {
			t.Fatal("invalid PNG")
		}
		nonBackground := 0
		for y := 0; y < 640; y++ {
			for x := 0; x < 640; x++ {
				r, g, b, _ := img.At(x, y).RGBA()
				if r != 19*257 || g != 24*257 || b != 34*257 {
					nonBackground++
				}
			}
		}
		if nonBackground < 1000 {
			t.Fatalf("blank %s render", view)
		}
	}
}
func TestConcaveTriangulationAndInvalidGeometry(t *testing.T) {
	m := Mesh{}
	ids := []string{}
	for _, p := range []Vec{{0, 0, 0}, {2, 0, 0}, {2, 1, 0}, {1, 1, 0}, {1, 2, 0}, {0, 2, 0}} {
		ids = append(ids, m.vertex(p))
	}
	m.face(ids)
	d := Empty()
	d.Nodes = append(d.Nodes, Node{ID: "concave", Mesh: m})
	triangles, err := Triangles(d)
	if err != nil {
		t.Fatal(err)
	}
	area := 0.0
	for _, tri := range triangles {
		area += tri.B.Sub(tri.A).Cross(tri.C.Sub(tri.A)).Len() / 2
	}
	if math.Abs(area-3) > 1e-9 {
		t.Fatalf("concave area %f", area)
	}
	d.Nodes[0].Mesh.Vertices[0].Position[0] = math.NaN()
	if _, err = Validate(d); err == nil {
		t.Fatal("NaN accepted")
	}
}

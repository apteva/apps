package engine

import (
	"encoding/binary"
	"encoding/json"
	"testing"
)

func TestGameExamples(t *testing.T) {
	for name, build := range map[string]func() (Document, error){"warrior": ExampleWarrior, "landscape": ExampleLandscape, "sword": ExampleSword} {
		t.Run(name, func(t *testing.T) {
			d, err := build()
			if err != nil {
				t.Fatal(err)
			}
			// Consistent winding alone also accepts inward-facing components.
			tris, err := Triangles(d)
			if err != nil {
				t.Fatal(err)
			}
			volumes := map[string]float64{}
			for _, tri := range tris {
				volumes[tri.NodeID] += tri.A.Dot(tri.B.Cross(tri.C)) / 6
			}
			for id, volume := range volumes {
				if volume <= 0 {
					t.Fatalf("part %s is inward-facing or degenerate: %g", id, volume)
				}
			}
			report, err := Validate(d)
			if err != nil {
				t.Fatal(err)
			}
			if report.BoundaryEdges != 0 || report.NonManifoldEdges != 0 {
				t.Fatalf("not closed manifold: %+v", report)
			}
			if len(d.Nodes) < 15 || report.Triangles < 200 {
				t.Fatalf("example lacks detail: %+v", report)
			}
			again, err := build()
			if err != nil {
				t.Fatal(err)
			}
			a, _ := json.Marshal(d)
			b, _ := json.Marshal(again)
			if string(a) != string(b) {
				t.Fatal("example is not deterministic")
			}
			// Selecting and editing a face must work on the actual generated topology.
			n := d.Nodes[0]
			sel := Selection{NodeID: n.ID, Kind: "face", IDs: []string{n.Mesh.Faces[0].ID}}
			edited, err := Evaluate(EditRequest{Document: d, Selections: map[string]Selection{"part": sel}, Commands: []Command{{Op: "transform", NodeID: n.ID, Selection: "part", Translation: ptr(Vec{0, .005, 0})}}})
			if err != nil {
				t.Fatal(err)
			}
			if len(edited.Document.Nodes) != len(d.Nodes) {
				t.Fatal("edit lost parts")
			}
			glb, err := GLB(d)
			if err != nil {
				t.Fatal(err)
			}
			if binary.LittleEndian.Uint32(glb[:4]) != 0x46546c67 {
				t.Fatal("bad GLB")
			}
			if _, err := RenderPNG(d, "perspective", nil); err != nil {
				t.Fatal(err)
			}
			t.Logf("%d parts, %d vertices, %d triangles", len(d.Nodes), report.Vertices, report.Triangles)
		})
	}
}

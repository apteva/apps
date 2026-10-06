// Package engine is the portable, dependency-free 3D Studio mesh kernel.
// Source meshes retain polygon faces and stable IDs; triangulation is derived.
package engine

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
)

const Version = "0.1.0"
const MaxVertices = 20000
const MaxFaces = 20000

type Vec [3]float64

func (a Vec) Add(b Vec) Vec     { return Vec{a[0] + b[0], a[1] + b[1], a[2] + b[2]} }
func (a Vec) Sub(b Vec) Vec     { return a.Add(b.Mul(-1)) }
func (a Vec) Mul(s float64) Vec { return Vec{a[0] * s, a[1] * s, a[2] * s} }
func (a Vec) Dot(b Vec) float64 { return a[0]*b[0] + a[1]*b[1] + a[2]*b[2] }
func (a Vec) Cross(b Vec) Vec {
	return Vec{a[1]*b[2] - a[2]*b[1], a[2]*b[0] - a[0]*b[2], a[0]*b[1] - a[1]*b[0]}
}
func (a Vec) Len() float64 { return math.Sqrt(a.Dot(a)) }
func (a Vec) Unit() Vec {
	if l := a.Len(); l > 1e-12 {
		return a.Mul(1 / l)
	}
	return Vec{}
}
func finite(v Vec) bool {
	for _, x := range v {
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > 1e6 {
			return false
		}
	}
	return true
}

type Vertex struct {
	ID       string `json:"id"`
	Position Vec    `json:"position"`
}
type Face struct {
	ID       string   `json:"id"`
	Vertices []string `json:"vertices"`
}
type Mesh struct {
	Vertices []Vertex `json:"vertices"`
	Faces    []Face   `json:"faces"`
	NextID   int      `json:"next_id"`
}
type Node struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Color Vec    `json:"color"`
	Mesh  Mesh   `json:"mesh"`
}
type Document struct {
	Schema string `json:"schema"`
	Units  string `json:"units"`
	Nodes  []Node `json:"nodes"`
}
type Selection struct {
	NodeID string   `json:"node_id"`
	Kind   string   `json:"kind"`
	IDs    []string `json:"ids"`
}
type Query struct {
	NodeID string   `json:"node_id"`
	Kind   string   `json:"kind"`
	IDs    []string `json:"ids,omitempty"`
	Min    *Vec     `json:"min,omitempty"`
	Max    *Vec     `json:"max,omitempty"`
	Normal *Vec     `json:"normal,omitempty"`
	MinDot *float64 `json:"min_dot,omitempty"`
}
type Report struct {
	Vertices         int      `json:"vertices"`
	Faces            int      `json:"faces"`
	Triangles        int      `json:"triangles"`
	Bounds           [2]Vec   `json:"bounds"`
	BoundaryEdges    int      `json:"boundary_edges"`
	NonManifoldEdges int      `json:"non_manifold_edges"`
	Warnings         []string `json:"warnings"`
}
type Triangle struct {
	A, B, C        Vec
	Normal         Vec
	Color          Vec
	NodeID, FaceID string
}

func Empty() Document { return Document{"apteva-3d/v1", "m", []Node{}} }
func Clone(d Document) Document {
	b, _ := json.Marshal(d)
	var c Document
	_ = json.Unmarshal(b, &c)
	return c
}
func (d *Document) Node(id string) (*Node, error) {
	for i := range d.Nodes {
		if d.Nodes[i].ID == id {
			return &d.Nodes[i], nil
		}
	}
	return nil, fmt.Errorf("unknown node %q", id)
}
func (m *Mesh) id(prefix string) string { m.NextID++; return fmt.Sprintf("%s%d", prefix, m.NextID) }
func (m *Mesh) vertex(p Vec) string {
	id := m.id("v")
	m.Vertices = append(m.Vertices, Vertex{id, p})
	return id
}
func (m *Mesh) face(v []string) string {
	id := m.id("f")
	m.Faces = append(m.Faces, Face{id, v})
	return id
}
func (m Mesh) Positions() map[string]Vec {
	out := map[string]Vec{}
	for _, v := range m.Vertices {
		out[v.ID] = v.Position
	}
	return out
}
func Normal(f Face, p map[string]Vec) Vec {
	n := Vec{}
	for i, id := range f.Vertices {
		a, b := p[id], p[f.Vertices[(i+1)%len(f.Vertices)]]
		n = n.Add(a.Cross(b))
	}
	return n.Unit()
}
func Center(f Face, p map[string]Vec) Vec {
	c := Vec{}
	for _, id := range f.Vertices {
		c = c.Add(p[id])
	}
	return c.Mul(1 / float64(len(f.Vertices)))
}
func edge(a, b string) string {
	if a > b {
		a, b = b, a
	}
	return a + ":" + b
}

// Triangulate uses projected ear clipping, including concave polygon faces.
func Triangulate(f Face, p map[string]Vec) ([][3]string, error) {
	n := Normal(f, p)
	if n.Len() < 0.5 {
		return nil, fmt.Errorf("face %s has zero area", f.ID)
	}
	axis := 0
	for i := 1; i < 3; i++ {
		if math.Abs(n[i]) > math.Abs(n[axis]) {
			axis = i
		}
	}
	u, v := (axis+1)%3, (axis+2)%3
	cross := func(a, b, c string) float64 {
		x, y, z := p[a], p[b], p[c]
		return (y[u]-x[u])*(z[v]-x[v]) - (y[v]-x[v])*(z[u]-x[u])
	}
	ids := append([]string(nil), f.Vertices...)
	sign := 1.0
	if n[axis] < 0 {
		sign = -1
	}
	out := [][3]string{}
	for len(ids) > 3 {
		found := false
		for i, b := range ids {
			a, c := ids[(i+len(ids)-1)%len(ids)], ids[(i+1)%len(ids)]
			if sign*cross(a, b, c) <= 1e-12 {
				continue
			}
			inside := false
			for _, q := range ids {
				if q == a || q == b || q == c {
					continue
				}
				if sign*cross(a, b, q) >= -1e-12 && sign*cross(b, c, q) >= -1e-12 && sign*cross(c, a, q) >= -1e-12 {
					inside = true
					break
				}
			}
			if !inside {
				out = append(out, [3]string{a, b, c})
				ids = append(ids[:i], ids[i+1:]...)
				found = true
				break
			}
		}
		if !found {
			return nil, fmt.Errorf("face %s cannot be triangulated; simplify its boundary", f.ID)
		}
	}
	out = append(out, [3]string{ids[0], ids[1], ids[2]})
	return out, nil
}

func Validate(d Document) (Report, error) {
	r := Report{Warnings: []string{}}
	if d.Schema != "apteva-3d/v1" || d.Units != "m" {
		return r, errors.New("expected apteva-3d/v1 in meters")
	}
	if len(d.Nodes) > 256 {
		return r, errors.New("maximum 256 nodes")
	}
	nodeIDs := map[string]bool{}
	initialized := false
	for _, n := range d.Nodes {
		if n.ID == "" || len(n.ID) > 80 || nodeIDs[n.ID] {
			return r, errors.New("node IDs must be unique and nonempty (max 80 characters)")
		}
		nodeIDs[n.ID] = true
		if !finite(n.Color) {
			return r, errors.New("invalid color")
		}
		for _, c := range n.Color {
			if c < 0 || c > 1 {
				return r, errors.New("colors must be between 0 and 1")
			}
		}
		r.Vertices += len(n.Mesh.Vertices)
		r.Faces += len(n.Mesh.Faces)
		if r.Vertices > MaxVertices || r.Faces > MaxFaces {
			return r, errors.New("mesh budget exceeded: 20000 vertices/faces per document")
		}
		p := map[string]Vec{}
		seen := map[string]bool{}
		edges := map[string]int{}
		orientation := map[string]int{}
		for _, v := range n.Mesh.Vertices {
			if v.ID == "" || seen[v.ID] || !finite(v.Position) {
				return r, errors.New("invalid or duplicate vertex")
			}
			seen[v.ID] = true
			p[v.ID] = v.Position
			if !initialized {
				r.Bounds = [2]Vec{v.Position, v.Position}
				initialized = true
			}
			for k, x := range v.Position {
				r.Bounds[0][k] = math.Min(r.Bounds[0][k], x)
				r.Bounds[1][k] = math.Max(r.Bounds[1][k], x)
			}
		}
		for _, f := range n.Mesh.Faces {
			if f.ID == "" || seen[f.ID] || len(f.Vertices) < 3 || len(f.Vertices) > 128 {
				return r, errors.New("invalid face: expected unique ID and 3–128 vertices")
			}
			seen[f.ID] = true
			local := map[string]bool{}
			for i, id := range f.Vertices {
				if _, ok := p[id]; !ok || local[id] {
					return r, errors.New("face references missing or repeated vertex")
				}
				local[id] = true
				b := f.Vertices[(i+1)%len(f.Vertices)]
				key := edge(id, b)
				edges[key]++
				if id < b {
					orientation[key]++
				} else {
					orientation[key]--
				}
			}
			tris, err := Triangulate(f, p)
			if err != nil {
				return r, err
			}
			r.Triangles += len(tris)
		}
		for e, count := range edges {
			if count == 1 {
				r.BoundaryEdges++
			}
			if count > 2 {
				r.NonManifoldEdges++
			}
			if count == 2 && orientation[e] != 0 {
				return r, fmt.Errorf("inconsistent winding at edge %s", e)
			}
		}
	}
	if r.NonManifoldEdges > 0 {
		return r, errors.New("non-manifold edges are unsupported")
	}
	if r.BoundaryEdges > 0 {
		r.Warnings = append(r.Warnings, "Mesh has open boundaries; this may be intentional for game assets.")
	}
	return r, nil
}
func Triangles(d Document) ([]Triangle, error) {
	if _, err := Validate(d); err != nil {
		return nil, err
	}
	out := []Triangle{}
	for _, n := range d.Nodes {
		p := n.Mesh.Positions()
		for _, f := range n.Mesh.Faces {
			ts, _ := Triangulate(f, p)
			for _, t := range ts {
				a, b, c := p[t[0]], p[t[1]], p[t[2]]
				out = append(out, Triangle{a, b, c, b.Sub(a).Cross(c.Sub(a)).Unit(), n.Color, n.ID, f.ID})
			}
		}
	}
	return out, nil
}
func Select(d Document, q Query) (Selection, error) {
	s := Selection{q.NodeID, q.Kind, []string{}}
	n, err := d.Node(q.NodeID)
	if err != nil {
		return s, err
	}
	if q.Kind != "face" && q.Kind != "vertex" {
		return s, errors.New("kind must be face or vertex")
	}
	if q.Min != nil && !finite(*q.Min) || q.Max != nil && !finite(*q.Max) {
		return s, errors.New("invalid bounds")
	}
	if q.Min != nil && q.Max != nil {
		for i := 0; i < 3; i++ {
			if q.Min[i] > q.Max[i] {
				return s, errors.New("min exceeds max")
			}
		}
	}
	if q.Normal != nil && (q.Kind != "face" || !finite(*q.Normal) || q.Normal.Len() < 1e-12) {
		return s, errors.New("normal requires a nonzero face direction")
	}
	if q.MinDot != nil && (*q.MinDot < -1 || *q.MinDot > 1 || math.IsNaN(*q.MinDot)) {
		return s, errors.New("min_dot must be -1 to 1")
	}
	wanted := map[string]bool{}
	for _, id := range q.IDs {
		wanted[id] = true
	}
	found := map[string]bool{}
	p := n.Mesh.Positions()
	matches := func(id string, v Vec, normal Vec) {
		found[id] = true
		if len(wanted) > 0 && !wanted[id] {
			return
		}
		for k := 0; k < 3; k++ {
			if q.Min != nil && v[k] < q.Min[k] || q.Max != nil && v[k] > q.Max[k] {
				return
			}
		}
		if q.Normal != nil {
			threshold := 0.8
			if q.MinDot != nil {
				threshold = *q.MinDot
			}
			if normal.Dot(q.Normal.Unit()) < threshold {
				return
			}
		}
		s.IDs = append(s.IDs, id)
	}
	if q.Kind == "face" {
		for _, f := range n.Mesh.Faces {
			matches(f.ID, Center(f, p), Normal(f, p))
		}
	} else {
		for _, v := range n.Mesh.Vertices {
			matches(v.ID, v.Position, Vec{})
		}
	}
	for id := range wanted {
		if !found[id] {
			return s, fmt.Errorf("unknown %s %s", q.Kind, id)
		}
	}
	if len(s.IDs) == 0 {
		return s, errors.New("selection is empty")
	}
	return s, nil
}
func vertices(m Mesh, s Selection) (map[string]bool, error) {
	ids := map[string]bool{}
	for _, id := range s.IDs {
		ids[id] = true
	}
	if len(ids) == 0 {
		return nil, errors.New("selection is empty")
	}
	out := map[string]bool{}
	if s.Kind == "vertex" {
		for _, v := range m.Vertices {
			if ids[v.ID] {
				out[v.ID] = true
				delete(ids, v.ID)
			}
		}
	} else if s.Kind == "face" {
		for _, f := range m.Faces {
			if ids[f.ID] {
				for _, id := range f.Vertices {
					out[id] = true
				}
				delete(ids, f.ID)
			}
		}
	} else {
		return nil, errors.New("invalid selection kind")
	}
	if len(ids) > 0 {
		return nil, errors.New("selection contains stale element IDs")
	}
	return out, nil
}
func Grow(d Document, s Selection, steps int) (Selection, error) {
	n, err := d.Node(s.NodeID)
	if err != nil {
		return s, err
	}
	if steps < 1 || steps > 16 {
		return s, errors.New("steps must be 1–16")
	}
	if _, err = vertices(n.Mesh, s); err != nil {
		return s, err
	}
	for step := 0; step < steps; step++ {
		selected, _ := vertices(n.Mesh, s)
		ids := map[string]bool{}
		for _, id := range s.IDs {
			ids[id] = true
		}
		for _, f := range n.Mesh.Faces {
			hit := false
			for _, id := range f.Vertices {
				if selected[id] {
					hit = true
				}
			}
			if hit {
				if s.Kind == "face" {
					ids[f.ID] = true
				} else {
					for _, id := range f.Vertices {
						ids[id] = true
					}
				}
			}
		}
		s.IDs = nil
		for id := range ids {
			s.IDs = append(s.IDs, id)
		}
		sort.Strings(s.IDs)
	}
	return s, nil
}

// UnmarshalJSON rejects truncated/extra coordinates rather than silently accepting
// the fixed-array defaults used by encoding/json.
func (v *Vec) UnmarshalJSON(raw []byte) error {
	var parts []json.RawMessage
	if err := json.Unmarshal(raw, &parts); err != nil {
		return err
	}
	if len(parts) != 3 {
		return errors.New("vector must have exactly three numeric coordinates")
	}
	for i, p := range parts {
		if string(p) == "null" {
			return errors.New("null coordinate")
		}
		if err := json.Unmarshal(p, &v[i]); err != nil {
			return err
		}
	}
	if !finite(*v) {
		return errors.New("coordinates must be finite and within +/-1000000")
	}
	return nil
}

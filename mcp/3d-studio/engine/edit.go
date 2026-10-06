package engine

import (
	"errors"
	"fmt"
	"math"
	"sort"
)

type Patch struct {
	VertexID string `json:"vertex_id"`
	Position Vec    `json:"position"`
}
type Command struct {
	Op              string  `json:"op"`
	NodeID          string  `json:"node_id"`
	Name            string  `json:"name,omitempty"`
	Shape           string  `json:"shape,omitempty"`
	Size            *Vec    `json:"size,omitempty"`
	Color           *Vec    `json:"color,omitempty"`
	Segments        int     `json:"segments,omitempty"`
	Selection       string  `json:"selection,omitempty"`
	ResultSelection string  `json:"result_selection,omitempty"`
	Translation     *Vec    `json:"translation,omitempty"`
	Scale           *Vec    `json:"scale,omitempty"`
	Rotation        *Vec    `json:"rotation,omitempty"` // Euler degrees, applied X then Y then Z.
	Pivot           *Vec    `json:"pivot,omitempty"`    // Defaults to selection centroid.
	Radius          float64 `json:"radius,omitempty"`
	Falloff         string  `json:"falloff,omitempty"`
	ConnectedOnly   bool    `json:"connected_only,omitempty"`
	Direction       *Vec    `json:"direction,omitempty"`
	Distance        float64 `json:"distance,omitempty"`
	Amount          float64 `json:"amount,omitempty"` // Inset fraction, 0 < amount < 1.
	Axis            string  `json:"axis,omitempty"`
	Offset          float64 `json:"offset,omitempty"`
	TargetID        string  `json:"target_id,omitempty"`
	Tolerance       float64 `json:"tolerance,omitempty"`
	Positions       []Patch `json:"positions,omitempty"`
}
type EditRequest struct {
	Document   Document             `json:"document"`
	Commands   []Command            `json:"commands"`
	Selections map[string]Selection `json:"selections,omitempty"`
}
type EditResult struct {
	Document   Document             `json:"document"`
	Selections map[string]Selection `json:"selections"`
	Report     Report               `json:"report"`
	Changes    []string             `json:"changes"`
}

// Evaluate never mutates input. All commands succeed together, or nothing changes.
func Evaluate(req EditRequest) (EditResult, error) {
	r := EditResult{Document: Clone(req.Document), Selections: map[string]Selection{}, Changes: []string{}}
	if _, err := Validate(req.Document); err != nil {
		return r, err
	}
	if len(req.Commands) < 1 || len(req.Commands) > 64 {
		return r, errors.New("expected 1–64 commands")
	}
	for k, v := range req.Selections {
		if len(k) > 80 {
			return EditResult{}, errors.New("selection alias exceeds 80 characters")
		}
		v.IDs = append([]string(nil), v.IDs...)
		r.Selections[k] = v
	}
	for i, c := range req.Commands {
		if len(c.ResultSelection) > 80 {
			return EditResult{}, errors.New("result_selection exceeds 80 characters")
		}
		if err := apply(&r, c); err != nil {
			return EditResult{}, fmt.Errorf("command %d (%s): %w", i, c.Op, err)
		}
		report, err := Validate(r.Document)
		if err != nil {
			return EditResult{}, fmt.Errorf("command %d (%s): %w", i, c.Op, err)
		}
		r.Report = report
		r.Changes = append(r.Changes, fmt.Sprintf("%s on %s", c.Op, c.NodeID))
		// Invalidate stale selections after topology edits; never silently retarget IDs.
		for key, s := range r.Selections {
			n, err := r.Document.Node(s.NodeID)
			if err != nil {
				delete(r.Selections, key)
				continue
			}
			if _, err = vertices(n.Mesh, s); err != nil {
				delete(r.Selections, key)
			}
		}
	}
	return r, nil
}
func apply(r *EditResult, c Command) error {
	for _, v := range []*Vec{c.Size, c.Color, c.Translation, c.Scale, c.Rotation, c.Pivot, c.Direction} {
		if v != nil && !finite(*v) {
			return errors.New("non-finite or out-of-range vector")
		}
	}
	for _, x := range []float64{c.Radius, c.Distance, c.Amount, c.Offset, c.Tolerance} {
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Abs(x) > 1e6 {
			return errors.New("invalid numeric parameter")
		}
	}
	if c.Op == "primitive.add" {
		return addPrimitive(&r.Document, c)
	}
	n, err := r.Document.Node(c.NodeID)
	if err != nil {
		return err
	}
	switch c.Op {
	case "node.color":
		if c.Color == nil {
			return errors.New("color required")
		}
		n.Color = *c.Color
		return nil
	case "node.rename":
		if c.Name == "" || len(c.Name) > 160 {
			return errors.New("name required (max 160)")
		}
		n.Name = c.Name
		return nil
	case "node.delete":
		for i := range r.Document.Nodes {
			if r.Document.Nodes[i].ID == c.NodeID {
				r.Document.Nodes = append(r.Document.Nodes[:i], r.Document.Nodes[i+1:]...)
				break
			}
		}
		return nil
	case "node.duplicate", "mesh.mirror":
		if c.TargetID == "" {
			return errors.New("target_id required")
		}
		for _, node := range r.Document.Nodes {
			if node.ID == c.TargetID {
				return errors.New("target node already exists")
			}
		}
		clone := Clone(Document{Nodes: []Node{*n}}).Nodes[0]
		clone.ID = c.TargetID
		clone.Name = c.TargetID
		if c.Op == "mesh.mirror" {
			axis, err := axisIndex(c.Axis)
			if err != nil {
				return err
			}
			for i := range clone.Mesh.Vertices {
				clone.Mesh.Vertices[i].Position[axis] = 2*c.Offset - clone.Mesh.Vertices[i].Position[axis]
			}
			for i := range clone.Mesh.Faces {
				reverse(clone.Mesh.Faces[i].Vertices)
			}
		}
		if c.Translation != nil {
			for i := range clone.Mesh.Vertices {
				clone.Mesh.Vertices[i].Position = clone.Mesh.Vertices[i].Position.Add(*c.Translation)
			}
		}
		r.Document.Nodes = append(r.Document.Nodes, clone)
		return nil
	case "vertices.patch":
		if len(c.Positions) == 0 || len(c.Positions) > MaxVertices {
			return errors.New("positions required")
		}
		seen := map[string]Vec{}
		for _, v := range c.Positions {
			if !finite(v.Position) {
				return errors.New("invalid position")
			}
			if _, ok := seen[v.VertexID]; ok {
				return errors.New("duplicate patch vertex")
			}
			seen[v.VertexID] = v.Position
		}
		for i := range n.Mesh.Vertices {
			v := &n.Mesh.Vertices[i]
			if p, ok := seen[v.ID]; ok {
				v.Position = p
				delete(seen, v.ID)
			}
		}
		if len(seen) > 0 {
			return errors.New("unknown patch vertex")
		}
		return nil
	}
	s := Selection{NodeID: n.ID, Kind: "vertex"}
	if c.Selection == "" {
		for _, v := range n.Mesh.Vertices {
			s.IDs = append(s.IDs, v.ID)
		}
	} else {
		var ok bool
		s, ok = r.Selections[c.Selection]
		if !ok || s.NodeID != n.ID {
			return errors.New("unknown or wrong-node selection")
		}
	}
	selected, err := vertices(n.Mesh, s)
	if err != nil {
		return err
	}
	switch c.Op {
	case "transform":
		return transform(&n.Mesh, selected, c)
	case "flatten":
		axis, err := axisIndex(c.Axis)
		if err != nil {
			return err
		}
		for i := range n.Mesh.Vertices {
			if selected[n.Mesh.Vertices[i].ID] {
				n.Mesh.Vertices[i].Position[axis] = c.Offset
			}
		}
		return nil
	case "extrude":
		if c.Direction == nil || c.Direction.Len() < 1e-9 || math.Abs(c.Distance) < 1e-9 {
			return errors.New("extrude requires nonzero direction and distance")
		}
		if s.Kind != "face" {
			return errors.New("extrude requires a face selection")
		}
		out, err := extrude(&n.Mesh, s, c.Direction.Unit().Mul(c.Distance))
		if err != nil {
			return err
		}
		out.NodeID = n.ID
		if c.ResultSelection != "" {
			r.Selections[c.ResultSelection] = out
		}
		return nil
	case "inset":
		if s.Kind != "face" || c.Amount <= 0 || c.Amount >= 1 {
			return errors.New("inset requires faces and amount between 0 and 1 (centroid fraction)")
		}
		out := inset(&n.Mesh, s, c.Amount)
		out.NodeID = n.ID
		if c.ResultSelection != "" {
			r.Selections[c.ResultSelection] = out
		}
		return nil
	case "faces.delete":
		if s.Kind != "face" {
			return errors.New("face selection required")
		}
		ids := idSet(s.IDs)
		fs := []Face{}
		for _, f := range n.Mesh.Faces {
			if !ids[f.ID] {
				fs = append(fs, f)
			}
		}
		n.Mesh.Faces = fs
		prune(&n.Mesh)
		return nil
	case "vertices.weld":
		if c.Tolerance <= 0 || c.Tolerance > 0.1 {
			return errors.New("tolerance must be >0 and <=0.1 meters")
		}
		if len(selected) > 2000 {
			return errors.New("weld is limited to 2000 selected vertices")
		}
		return weld(&n.Mesh, selected, c.Tolerance)
	default:
		return fmt.Errorf("unsupported operation %q", c.Op)
	}
}
func idSet(ids []string) map[string]bool {
	out := map[string]bool{}
	for _, id := range ids {
		out[id] = true
	}
	return out
}
func axisIndex(s string) (int, error) {
	switch s {
	case "x":
		return 0, nil
	case "y":
		return 1, nil
	case "z":
		return 2, nil
	}
	return 0, errors.New("axis must be x, y, or z")
}
func reverse(ids []string) {
	for i, j := 0, len(ids)-1; i < j; i, j = i+1, j-1 {
		ids[i], ids[j] = ids[j], ids[i]
	}
}
func prune(m *Mesh) {
	used := map[string]bool{}
	for _, f := range m.Faces {
		for _, id := range f.Vertices {
			used[id] = true
		}
	}
	vs := []Vertex{}
	for _, v := range m.Vertices {
		if used[v.ID] {
			vs = append(vs, v)
		}
	}
	m.Vertices = vs
}
func addPrimitive(d *Document, c Command) error {
	if c.NodeID == "" {
		return errors.New("node_id required")
	}
	for _, n := range d.Nodes {
		if n.ID == c.NodeID {
			return errors.New("node already exists")
		}
	}
	size := Vec{1, 1, 1}
	if c.Size != nil {
		size = *c.Size
	}
	for _, s := range size {
		if s <= 0 {
			return errors.New("size components must be positive")
		}
	}
	n := Node{ID: c.NodeID, Name: c.NodeID, Color: Vec{0.38, 0.65, 0.92}}
	if c.Name != "" {
		n.Name = c.Name
	}
	if c.Color != nil {
		n.Color = *c.Color
	}
	m := &n.Mesh
	switch c.Shape {
	case "box":
		ids := []string{}
		for _, v := range []Vec{{-1, -1, -1}, {1, -1, -1}, {1, 1, -1}, {-1, 1, -1}, {-1, -1, 1}, {1, -1, 1}, {1, 1, 1}, {-1, 1, 1}} {
			for k := 0; k < 3; k++ {
				v[k] *= size[k] / 2
			}
			ids = append(ids, m.vertex(v))
		}
		for _, f := range [][]int{{0, 3, 2, 1}, {4, 5, 6, 7}, {0, 1, 5, 4}, {3, 7, 6, 2}, {0, 4, 7, 3}, {1, 2, 6, 5}} {
			v := []string{}
			for _, i := range f {
				v = append(v, ids[i])
			}
			m.face(v)
		}
	case "cylinder":
		count := c.Segments
		if count == 0 {
			count = 12
		}
		if count < 3 || count > 64 {
			return errors.New("segments must be 3–64")
		}
		bottom, top := []string{}, []string{}
		for i := 0; i < count; i++ {
			angle := float64(i) * 2 * math.Pi / float64(count)
			x, z := math.Cos(angle)*size[0]/2, math.Sin(angle)*size[2]/2
			bottom = append(bottom, m.vertex(Vec{x, -size[1] / 2, z}))
			top = append(top, m.vertex(Vec{x, size[1] / 2, z}))
		}
		m.face(append([]string(nil), bottom...))
		cap := append([]string(nil), top...)
		reverse(cap)
		m.face(cap)
		for i := 0; i < count; i++ {
			j := (i + 1) % count
			m.face([]string{bottom[i], top[i], top[j], bottom[j]})
		}
	default:
		return errors.New("shape must be box or cylinder")
	}
	if c.Translation != nil {
		for i := range m.Vertices {
			m.Vertices[i].Position = m.Vertices[i].Position.Add(*c.Translation)
		}
	}
	d.Nodes = append(d.Nodes, n)
	return nil
}
func transform(m *Mesh, selected map[string]bool, c Command) error {
	if c.Radius < 0 || c.Radius > 100 {
		return errors.New("radius must be 0–100 meters")
	}
	if c.Radius > 0 && len(selected)*len(m.Vertices) > 4000000 {
		return errors.New("proportional selection exceeds interaction budget")
	}
	if c.Falloff != "" && c.Falloff != "linear" && c.Falloff != "smooth" {
		return errors.New("falloff must be linear or smooth")
	}
	scale := Vec{1, 1, 1}
	if c.Scale != nil {
		scale = *c.Scale
		for _, s := range scale {
			if s <= 0 {
				return errors.New("scale must be positive; use mirror for reflection")
			}
		}
	}
	pivot := Vec{}
	points := []Vec{}
	for _, v := range m.Vertices {
		if selected[v.ID] {
			pivot = pivot.Add(v.Position)
			points = append(points, v.Position)
		}
	}
	pivot = pivot.Mul(1 / float64(len(points)))
	if c.Pivot != nil {
		pivot = *c.Pivot
	}
	allowed := map[string]bool{}
	if c.ConnectedOnly {
		for id := range selected {
			allowed[id] = true
		}
		adj := map[string][]string{}
		for _, f := range m.Faces {
			for i, a := range f.Vertices {
				b := f.Vertices[(i+1)%len(f.Vertices)]
				adj[a] = append(adj[a], b)
				adj[b] = append(adj[b], a)
			}
		}
		queue := []string{}
		for id := range allowed {
			queue = append(queue, id)
		}
		for i := 0; i < len(queue); i++ {
			for _, id := range adj[queue[i]] {
				if !allowed[id] {
					allowed[id] = true
					queue = append(queue, id)
				}
			}
		}
	}
	for i := range m.Vertices {
		v := &m.Vertices[i]
		weight := 1.0
		if !selected[v.ID] {
			if c.Radius == 0 || c.ConnectedOnly && !allowed[v.ID] {
				continue
			}
			distance := math.Inf(1)
			for _, p := range points {
				distance = math.Min(distance, v.Position.Sub(p).Len())
			}
			weight = math.Max(0, 1-distance/c.Radius)
			if c.Falloff != "linear" {
				weight = weight * weight * (3 - 2*weight)
			}
		}
		p := v.Position.Sub(pivot)
		for k := 0; k < 3; k++ {
			p[k] *= scale[k]
		}
		if c.Rotation != nil {
			for axis, deg := range *c.Rotation {
				a := deg * math.Pi / 180
				u, w := (axis+1)%3, (axis+2)%3
				p[u], p[w] = p[u]*math.Cos(a)-p[w]*math.Sin(a), p[u]*math.Sin(a)+p[w]*math.Cos(a)
			}
		}
		p = p.Add(pivot)
		if c.Translation != nil {
			p = p.Add(*c.Translation)
		}
		v.Position = v.Position.Add(p.Sub(v.Position).Mul(weight))
	}
	return nil
}
func extrude(m *Mesh, s Selection, delta Vec) (Selection, error) {
	ids := idSet(s.IDs)
	used, _ := vertices(*m, s)
	p := m.Positions()
	mapping := map[string]string{}
	keys := []string{}
	for id := range used {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		mapping[id] = m.vertex(p[id].Add(delta))
	}
	counts := map[string]int{}
	type boundary struct{ a, b string }
	edges := []boundary{}
	caps := [][]string{}
	remaining := []Face{}
	for _, f := range m.Faces {
		if !ids[f.ID] {
			remaining = append(remaining, f)
			continue
		}
		cap := []string{}
		for i, a := range f.Vertices {
			b := f.Vertices[(i+1)%len(f.Vertices)]
			counts[edge(a, b)]++
			edges = append(edges, boundary{a, b})
			cap = append(cap, mapping[a])
		}
		caps = append(caps, cap)
	}
	boundaries := 0
	for _, e := range edges {
		if counts[edge(e.a, e.b)] == 1 {
			boundaries++
		}
	}
	if boundaries == 0 {
		return Selection{}, errors.New("extrude requires a region with a boundary")
	}
	m.Faces = remaining
	out := Selection{Kind: "face", IDs: []string{}}
	for _, cap := range caps {
		out.IDs = append(out.IDs, m.face(cap))
	}
	for _, e := range edges {
		if counts[edge(e.a, e.b)] == 1 {
			m.face([]string{e.a, e.b, mapping[e.b], mapping[e.a]})
		}
	}
	prune(m)
	return out, nil
}
func inset(m *Mesh, s Selection, amount float64) Selection {
	ids := idSet(s.IDs)
	p := m.Positions()
	original := append([]Face(nil), m.Faces...)
	m.Faces = nil
	out := Selection{Kind: "face", IDs: []string{}}
	for _, f := range original {
		if !ids[f.ID] {
			m.Faces = append(m.Faces, f)
			continue
		}
		center := Center(f, p)
		inner := []string{}
		for _, id := range f.Vertices {
			inner = append(inner, m.vertex(p[id].Add(center.Sub(p[id]).Mul(amount))))
		}
		out.IDs = append(out.IDs, m.face(inner))
		for i, a := range f.Vertices {
			j := (i + 1) % len(f.Vertices)
			m.face([]string{a, f.Vertices[j], inner[j], inner[i]})
		}
	}
	return out
}
func weld(m *Mesh, selected map[string]bool, tolerance float64) error {
	remap := map[string]string{}
	for i, a := range m.Vertices {
		if !selected[a.ID] {
			continue
		}
		if _, ok := remap[a.ID]; ok {
			continue
		}
		for _, b := range m.Vertices[i+1:] {
			if selected[b.ID] && a.Position.Sub(b.Position).Len() <= tolerance {
				if _, ok := remap[b.ID]; !ok {
					remap[b.ID] = a.ID
				}
			}
		}
	}
	faces := []Face{}
	for _, f := range m.Faces {
		ids := []string{}
		for _, id := range f.Vertices {
			if dst, ok := remap[id]; ok {
				id = dst
			}
			if len(ids) == 0 || ids[len(ids)-1] != id {
				ids = append(ids, id)
			}
		}
		if len(ids) > 1 && ids[0] == ids[len(ids)-1] {
			ids = ids[:len(ids)-1]
		}
		if len(ids) >= 3 {
			f.Vertices = ids
			faces = append(faces, f)
		}
	}
	m.Faces = faces
	prune(m)
	return nil
}

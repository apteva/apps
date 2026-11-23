package main

import (
	"bytes"
	"fmt"
	"html"
	"math"
	"strings"
)

// Mechanical 0.9 exports an AP242 faceted-BREP assembly, engine-independent
// reference meshes, and an exact 2D mechanical profile. The envelope JSON
// remains authoritative for holes, cutouts, tolerances, openings, and keepouts.
type solidPoint struct{ X, Y, Z float64 }
type solidTriangle struct{ A, B, C solidPoint }

func mechanicalAssemblyTriangles(def *Definition) []solidTriangle {
	triangles := prismTriangles(boardOutline(def.Board), 0, float64(def.Board.ThicknessNM)/1e6)
	if def.Mechanical == nil {
		return triangles
	}
	for _, envelope := range def.Mechanical.ComponentEnvelopes {
		triangles = append(triangles, orientedBoxTriangles(envelope)...)
	}
	return triangles
}

func prismTriangles(outline []Point, z0, z1 float64) []solidTriangle {
	if len(outline) < 3 {
		return nil
	}
	point := func(p Point, z float64) solidPoint { return solidPoint{float64(p.XNM) / 1e6, float64(p.YNM) / 1e6, z} }
	out := make([]solidTriangle, 0, len(outline)*4)
	for i := 1; i+1 < len(outline); i++ {
		out = append(out, solidTriangle{point(outline[0], z1), point(outline[i], z1), point(outline[i+1], z1)})
		out = append(out, solidTriangle{point(outline[0], z0), point(outline[i+1], z0), point(outline[i], z0)})
	}
	for i := range outline {
		j := (i + 1) % len(outline)
		a, b, c, d := point(outline[i], z0), point(outline[j], z0), point(outline[j], z1), point(outline[i], z1)
		out = append(out, solidTriangle{a, b, c}, solidTriangle{a, c, d})
	}
	return out
}

func orientedBoxTriangles(envelope ComponentEnvelope) []solidTriangle {
	corners := orientedRectangleCorners(envelope)
	z0, z1 := float64(envelope.ZMinNM)/1e6, float64(envelope.ZMaxNM)/1e6
	p := func(i int, z float64) solidPoint { return solidPoint{corners[i].x / 1e6, corners[i].y / 1e6, z} }
	faces := [][4]solidPoint{{p(0, z0), p(1, z0), p(2, z0), p(3, z0)}, {p(0, z1), p(3, z1), p(2, z1), p(1, z1)}, {p(0, z0), p(0, z1), p(1, z1), p(1, z0)}, {p(1, z0), p(1, z1), p(2, z1), p(2, z0)}, {p(2, z0), p(2, z1), p(3, z1), p(3, z0)}, {p(3, z0), p(3, z1), p(0, z1), p(0, z0)}}
	out := make([]solidTriangle, 0, 12)
	for _, face := range faces {
		out = append(out, solidTriangle{face[0], face[1], face[2]}, solidTriangle{face[0], face[2], face[3]})
	}
	return out
}

func triangleNormal(t solidTriangle) solidPoint {
	u := solidPoint{t.B.X - t.A.X, t.B.Y - t.A.Y, t.B.Z - t.A.Z}
	v := solidPoint{t.C.X - t.A.X, t.C.Y - t.A.Y, t.C.Z - t.A.Z}
	n := solidPoint{u.Y*v.Z - u.Z*v.Y, u.Z*v.X - u.X*v.Z, u.X*v.Y - u.Y*v.X}
	length := math.Sqrt(n.X*n.X + n.Y*n.Y + n.Z*n.Z)
	if length > 0 {
		n.X, n.Y, n.Z = n.X/length, n.Y/length, n.Z/length
	}
	return n
}

func renderMechanicalSTL(def *Definition) []byte {
	var b strings.Builder
	b.WriteString("solid apteva_pcb_mechanical_assembly\n")
	for _, triangle := range mechanicalAssemblyTriangles(def) {
		n := triangleNormal(triangle)
		fmt.Fprintf(&b, "  facet normal %.7g %.7g %.7g\n    outer loop\n", n.X, n.Y, n.Z)
		for _, p := range []solidPoint{triangle.A, triangle.B, triangle.C} {
			fmt.Fprintf(&b, "      vertex %.7g %.7g %.7g\n", p.X, p.Y, p.Z)
		}
		b.WriteString("    endloop\n  endfacet\n")
	}
	b.WriteString("endsolid apteva_pcb_mechanical_assembly\n")
	return []byte(b.String())
}

func renderMechanical3MF(def *Definition) ([]byte, error) {
	triangles := mechanicalAssemblyTriangles(def)
	var vertices, faces strings.Builder
	index := 0
	for _, triangle := range triangles {
		for _, p := range []solidPoint{triangle.A, triangle.B, triangle.C} {
			fmt.Fprintf(&vertices, `<vertex x="%.7g" y="%.7g" z="%.7g"/>`, p.X, p.Y, p.Z)
		}
		fmt.Fprintf(&faces, `<triangle v1="%d" v2="%d" v3="%d"/>`, index, index+1, index+2)
		index += 3
	}
	model := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><model unit="millimeter" xml:lang="en-US" xmlns="http://schemas.microsoft.com/3dmanufacturing/core/2015/02"><metadata name="Title">PCB Studio mechanical assembly</metadata><metadata name="Description">Reference solid; authoritative holes, cutouts and tolerances are in apteva-mechanical-envelope/v1.</metadata><resources><object id="1" type="model"><mesh><vertices>%s</vertices><triangles>%s</triangles></mesh></object></resources><build><item objectid="1"/></build></model>`, vertices.String(), faces.String())
	return deterministicZip(map[string][]byte{
		"[Content_Types].xml": []byte(`<?xml version="1.0" encoding="UTF-8"?><Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="model" ContentType="application/vnd.ms-package.3dmanufacturing-3dmodel+xml"/></Types>`),
		"_rels/.rels":         []byte(`<?xml version="1.0" encoding="UTF-8"?><Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Target="/3D/3dmodel.model" Id="rel0" Type="http://schemas.microsoft.com/3dmanufacturing/2013/01/3dmodel"/></Relationships>`),
		"3D/3dmodel.model":    []byte(model),
	})
}

func renderMechanicalDXF(def *Definition) []byte {
	var b bytes.Buffer
	b.WriteString("0\nSECTION\n2\nHEADER\n0\nENDSEC\n0\nSECTION\n2\nENTITIES\n")
	polyline := func(layer string, points []Point) {
		fmt.Fprintf(&b, "0\nLWPOLYLINE\n8\n%s\n90\n%d\n70\n1\n", layer, len(points))
		for _, p := range points {
			fmt.Fprintf(&b, "10\n%.6f\n20\n%.6f\n", float64(p.XNM)/1e6, float64(p.YNM)/1e6)
		}
	}
	polyline("BOARD_OUTLINE", boardOutline(def.Board))
	for _, cutout := range def.Board.Cutouts {
		switch cutout.Kind {
		case "circle":
			fmt.Fprintf(&b, "0\nCIRCLE\n8\nBOARD_CUTOUTS\n10\n%.6f\n20\n%.6f\n40\n%.6f\n", float64(cutout.XNM)/1e6, float64(cutout.YNM)/1e6, float64(cutout.DiameterNM)/2e6)
		case "polygon":
			polyline("BOARD_CUTOUTS", cutout.Polygon)
		case "slot":
			// A closed rotated rectangle preserves the exact slot envelope;
			// radius and tolerance remain in the JSON handoff.
			e := ComponentEnvelope{XNM: cutout.XNM, YNM: cutout.YNM, WidthNM: cutout.WidthNM, DepthNM: cutout.HeightNM, RotationUdeg: cutout.RotationUdeg}
			corners := orientedRectangleCorners(e)
			points := make([]Point, 4)
			for i, p := range corners {
				points[i] = Point{XNM: int64(math.Round(p.x)), YNM: int64(math.Round(p.y))}
			}
			polyline("BOARD_CUTOUTS", points)
		}
	}
	if def.Mechanical != nil {
		for _, hole := range def.Mechanical.MountingHoles {
			fmt.Fprintf(&b, "0\nCIRCLE\n8\nMOUNTING_HOLES\n10\n%.6f\n20\n%.6f\n40\n%.6f\n", float64(hole.XNM)/1e6, float64(hole.YNM)/1e6, float64(hole.DiameterNM)/2e6)
		}
		for _, opening := range def.Mechanical.PanelOpenings {
			fmt.Fprintf(&b, "999\nPANEL_OPENING %s %s %.6fx%.6f tol=%.6f\n", html.EscapeString(opening.ID), opening.Face, float64(opening.WidthNM)/1e6, float64(opening.HeightNM)/1e6, float64(opening.ToleranceNM)/1e6)
		}
	}
	b.WriteString("0\nENDSEC\n0\nEOF\n")
	return b.Bytes()
}

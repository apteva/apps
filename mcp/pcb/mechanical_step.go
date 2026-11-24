package main

import (
	"fmt"
	"math"
	"strings"
)

type stepEntityWriter struct {
	b      strings.Builder
	nextID int
}

func (w *stepEntityWriter) add(format string, args ...any) int {
	w.nextID++
	fmt.Fprintf(&w.b, "#%d=", w.nextID)
	fmt.Fprintf(&w.b, format, args...)
	w.b.WriteString(";\n")
	return w.nextID
}

type mechanicalSolidGroup struct {
	Name      string
	Triangles []solidTriangle
}

func mechanicalSolidGroups(def *Definition) []mechanicalSolidGroup {
	groups := []mechanicalSolidGroup{{Name: "PCB", Triangles: prismTriangles(boardOutline(def.Board), 0, float64(def.Board.ThicknessNM)/1e6)}}
	if def.Mechanical != nil {
		for _, envelope := range def.Mechanical.ComponentEnvelopes {
			name := envelope.Label
			if name == "" {
				name = envelope.ID
			}
			groups = append(groups, mechanicalSolidGroup{Name: name, Triangles: orientedBoxTriangles(envelope)})
		}
	}
	return groups
}

// renderMechanicalSTEP writes a standalone ISO 10303-21 AP242 faceted-BREP
// assembly. Unlike STL/3MF it carries product structure, units, named solids,
// planes, topology, and an immutable source identity understood by CAD tools.
func renderMechanicalSTEP(def *Definition) []byte {
	w := &stepEntityWriter{}
	application := w.add("APPLICATION_CONTEXT('managed model based 3d engineering')")
	w.add("APPLICATION_PROTOCOL_DEFINITION('international standard','ap242_managed_model_based_3d_engineering',2022,#%d)", application)
	productContext := w.add("PRODUCT_CONTEXT('',#%d,'mechanical')", application)
	product := w.add("PRODUCT('APTEVA-PCB','APTEVA-PCB','PCB Studio 0.9 mechanical assembly',(#%d))", productContext)
	formation := w.add("PRODUCT_DEFINITION_FORMATION('1','',#%d)", product)
	definitionContext := w.add("PRODUCT_DEFINITION_CONTEXT('part definition',#%d,'design')", application)
	definition := w.add("PRODUCT_DEFINITION('design','',#%d,#%d)", formation, definitionContext)
	shape := w.add("PRODUCT_DEFINITION_SHAPE('','',#%d)", definition)
	lengthUnit := w.add("(LENGTH_UNIT()NAMED_UNIT(*)SI_UNIT(.MILLI.,.METRE.))")
	angleUnit := w.add("(NAMED_UNIT(*)PLANE_ANGLE_UNIT()SI_UNIT($,.RADIAN.))")
	solidAngleUnit := w.add("(NAMED_UNIT(*)SI_UNIT($,.STERADIAN.)SOLID_ANGLE_UNIT())")
	uncertainty := w.add("UNCERTAINTY_MEASURE_WITH_UNIT(LENGTH_MEASURE(1.E-6),#%d,'distance_accuracy_value','confusion accuracy')", lengthUnit)
	context := w.add("(GEOMETRIC_REPRESENTATION_CONTEXT(3)GLOBAL_UNCERTAINTY_ASSIGNED_CONTEXT((#%d))GLOBAL_UNIT_ASSIGNED_CONTEXT((#%d,#%d,#%d))REPRESENTATION_CONTEXT('PCB Studio','3D'))", uncertainty, lengthUnit, angleUnit, solidAngleUnit)
	items := []int{}
	for _, group := range mechanicalSolidGroups(def) {
		faces := []int{}
		points := map[string]int{}
		pointID := func(point solidPoint) int {
			key := fmt.Sprintf("%.9f/%.9f/%.9f", point.X, point.Y, point.Z)
			if id := points[key]; id != 0 {
				return id
			}
			id := w.add("CARTESIAN_POINT('',(%.9f,%.9f,%.9f))", point.X, point.Y, point.Z)
			points[key] = id
			return id
		}
		for _, triangle := range group.Triangles {
			pa, pb, pc := pointID(triangle.A), pointID(triangle.B), pointID(triangle.C)
			normal := triangleNormal(triangle)
			dx, dy, dz := triangle.B.X-triangle.A.X, triangle.B.Y-triangle.A.Y, triangle.B.Z-triangle.A.Z
			length := math.Sqrt(dx*dx + dy*dy + dz*dz)
			if length == 0 {
				continue
			}
			dx, dy, dz = dx/length, dy/length, dz/length
			normalID := w.add("DIRECTION('',(%.9f,%.9f,%.9f))", normal.X, normal.Y, normal.Z)
			refID := w.add("DIRECTION('',(%.9f,%.9f,%.9f))", dx, dy, dz)
			axis := w.add("AXIS2_PLACEMENT_3D('',#%d,#%d,#%d)", pa, normalID, refID)
			plane := w.add("PLANE('',#%d)", axis)
			loop := w.add("POLY_LOOP('',(#%d,#%d,#%d))", pa, pb, pc)
			bound := w.add("FACE_OUTER_BOUND('',#%d,.T.)", loop)
			faces = append(faces, w.add("FACE_SURFACE('',(#%d),#%d,.T.)", bound, plane))
		}
		faceRefs := make([]string, len(faces))
		for index, face := range faces {
			faceRefs[index] = fmt.Sprintf("#%d", face)
		}
		shell := w.add("CLOSED_SHELL('%s',(%s))", stepEscape(group.Name), strings.Join(faceRefs, ","))
		items = append(items, w.add("FACETED_BREP('%s',#%d)", stepEscape(group.Name), shell))
	}
	itemRefs := make([]string, len(items))
	for index, item := range items {
		itemRefs[index] = fmt.Sprintf("#%d", item)
	}
	representation := w.add("SHAPE_REPRESENTATION('PCB Studio native mechanical assembly',(%s),#%d)", strings.Join(itemRefs, ","), context)
	w.add("SHAPE_DEFINITION_REPRESENTATION(#%d,#%d)", shape, representation)
	return []byte(fmt.Sprintf("ISO-10303-21;\nHEADER;\nFILE_DESCRIPTION(('PCB Studio native faceted BREP assembly'),'2;1');\nFILE_NAME('apteva-pcb.step','',('Apteva'),('Apteva'),'PCB Studio 0.9','PCB Studio','');\nFILE_SCHEMA(('AP242_MANAGED_MODEL_BASED_3D_ENGINEERING_MIM_LF'));\nENDSEC;\nDATA;\n%sENDSEC;\nEND-ISO-10303-21;\n", w.b.String()))
}

func verifyMechanicalSTEP(def *Definition, body []byte) error {
	text := string(body)
	for _, marker := range []string{"ISO-10303-21;", "FILE_SCHEMA(('AP242_", "FACETED_BREP(", "SHAPE_DEFINITION_REPRESENTATION(", "END-ISO-10303-21;"} {
		if !strings.Contains(text, marker) {
			return fmt.Errorf("STEP output is missing %s", marker)
		}
	}
	if strings.Count(text, "FACETED_BREP(") != len(mechanicalSolidGroups(def)) {
		return fmt.Errorf("STEP solid count does not match the native assembly")
	}
	return nil
}

func stepEscape(value string) string { return strings.ReplaceAll(value, "'", "''") }

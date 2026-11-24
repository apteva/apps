package main

func designExamples() map[string]any {
	return map[string]any{
		"schema": designSchema,
		"units":  "mm",
		"expressions": map[string]any{
			"syntax":    "Numbers or strings over parameters: + - * / %, parentheses, comparisons (< <= > >= == !=), and/or/not (&& || !), cond ? a : b, true/false/pi. Booleans are 1/0.",
			"functions": []string{"min", "max", "abs", "round(x[,step])", "floor", "ceil", "sqrt", "pow", "clamp(x,lo,hi)", "hypot", "sin/cos/tan (degrees)", "asin/acos/atan/atan2 (degrees)"},
			"examples":  []string{"width/2", "(diameter+clearance)/2", "max(wall*2, 3)", "clamp(width-2*margin, 10, 80)", "has_vent and width > 40 ? 6 : 0"},
		},
		"parameter_types": map[string]any{
			"length/number/angle": "Number with optional min, max, and options (allowed values).",
			"integer":             "Whole number, with optional min/max/options; use for counts.",
			"boolean":             "true/false (stored as 1/0); drive optional features with an operation's when field.",
		},
		"conditional_operations": map[string]any{
			"field":     "when: boolean, number, or expression; 0/false skips the operation.",
			"semantics": "Skipped operations drop out of fuse, cut tools, intersect, and compound inputs. Referring to a skipped operation any other way (cut base, transform input, part or definition output) is an error.",
			"example":   map[string]any{"id": "vent", "type": "cylinder", "radius": 3, "height": 10, "when": "has_vent"},
		},
		"component_operation": map[string]any{
			"shape":     map[string]any{"id": "body", "type": "component", "source": map[string]any{"design_id": 12, "revision_id": 34, "source_sha256": "...", "part_id": "optional"}, "parameters": map[string]any{"width": "size", "depth": 30}, "transform": map[string]any{"translate": []any{0, 0, 0}}},
			"semantics": "Brings another design's pinned geometry in as an ordinary shape you can cut, fuse, or pattern. parameters override the source revision's values: numbers are checked against the source's ranges; strings are expressions over this design's parameters. Linked parts (part.source) accept the same parameters overrides.",
		},
		"operations": []string{
			"box", "cylinder", "sphere", "extrude_rectangle", "extrude_circle", "extrude_polygon",
			"revolve_profile", "sweep_circle", "fuse", "cut", "intersect", "compound", "translate", "rotate", "scale", "mirror",
			"linear_pattern", "circular_pattern", "fillet", "chamfer", "component",
			"shell", "hole", "extrude_profile", "loft",
		},
		"edge_and_face_selectors": map[string]any{
			"keys":      []string{"side: top|bottom|+x|-x|+y|-y", "direction: X|Y|Z|[x,y,z] (edges)", "curve: line|circle (edges)", "surface: plane|cylinder|cone (faces)", "parallel_to: XY|XZ|YZ", "in_plane: {plane, offset}", "in_box: [[x,y,z],[x,y,z]]", "near: {point, distance}", "contains_point", "length (edges)", "not: selector", "any: [selectors]"},
			"semantics": "An object matches when all its keys match; a list matches any entry. Selecting nothing is an error.",
			"examples": []any{
				map[string]any{"id": "soft", "type": "fillet", "input": "body", "radius": 2, "edges": map[string]any{"side": "top"}},
				map[string]any{"id": "corners", "type": "fillet", "input": "body", "radius": 4, "edges": map[string]any{"direction": "Z"}},
				map[string]any{"id": "hollow", "type": "shell", "input": "soft", "thickness": 2, "open_faces": map[string]any{"side": "bottom"}},
			},
		},
		"profiles": map[string]any{
			"kinds":   []string{"rectangle/rounded_rectangle {width,height,radius}", "slot {length (end to end), width}", "circle {radius|diameter}", "regular_polygon {radius,sides}", "polygon {points}", "path {start, segments:[{line},{arc,via},{arc,sagitta},{tangent_arc}]}"},
			"common":  "center [x,y], angle (degrees), holes [profiles]",
			"extrude": map[string]any{"id": "usb", "type": "extrude_profile", "plane": "YZ", "origin": []any{68, 0, 0}, "profile": map[string]any{"kind": "slot", "length": 9.5, "width": 3.5, "center": []any{20, 7}}, "height": 4},
			"loft":    map[string]any{"id": "nozzle", "type": "loft", "sections": []any{map[string]any{"profile": map[string]any{"kind": "rectangle", "width": 10, "height": 10}, "offset": 0}, map[string]any{"profile": map[string]any{"kind": "circle", "radius": 3}, "offset": 10}}},
		},
		"holes": map[string]any{
			"shape":   map[string]any{"id": "mount", "type": "hole", "input": "plate", "positions": []any{[]any{5, 5, 3}, []any{25, 5, 3}}, "direction": []any{0, 0, -1}, "thread": map[string]any{"size": "M3", "mode": "clearance"}, "counterbore": map[string]any{"diameter": 6, "depth": 3}},
			"options": "diameter or thread {size M2-M8, mode clearance|tap|insert}; depth (omit = through); counterbore {diameter, depth} or countersink {diameter, angle=90}. position is on the surface; direction points into the material.",
		},
		"formats": []string{"mesh-json", "glb", "step", "stl", "3mf", "manufacturing-package.zip"},
		"mates": map[string]any{
			"interface": map[string]any{"id": "dovetail_out", "kind": "dovetail", "part_id": "base", "position": []any{20, 20, 10}, "direction": []any{0, 0, 1}, "up": []any{1, 0, 0}},
			"instance":  map[string]any{"id": "cap", "part_id": "cap", "mate": map[string]any{"interface": "dovetail_in", "to_instance": "base_1", "to_interface": "dovetail_out", "gap": 0.2, "angle": 0}},
			"joint":     map[string]any{"id": "lid_hinge", "type": "revolute", "parent_instance": "base_1", "child_instance": "lid_1", "origin": []any{0, 40, 10}, "axis": []any{-1, 0, 0}, "limits": []any{0, 110}, "state": "lid_open"},
			"check":     map[string]any{"id": "lid_swing", "type": "joint_sweep", "joint": "lid_hinge", "steps": 12, "min": 0.5},
		},
		"linked_components": map[string]any{
			"source_contract": map[string]any{"design_id": 12, "revision_id": 34, "source_sha256": "64 lowercase hex characters", "part_id": "optional_named_part"},
			"create_tool":     "assembly_create", "refresh_tool": "assembly_sources_refresh",
			"semantics":       "Revision and hash pins are immutable; refresh creates a new assembly revision.",
			"dependents_tool": "component_dependents", "refresh_all_tool": "component_sources_refresh_all",
		},
		"pcb_enclosures": map[string]any{
			"source_schema": pcbMechanicalEnvelopeSchema,
			"binding_role":  "pcb_source",
			"create_tool":   "design_enclosure_from_pcb",
			"refresh_tool":  "design_enclosure_refresh_from_pcb",
		},
		"featured": map[string]any{
			"open_rover": map[string]any{
				"name":        "Apteva Open Rover",
				"description": "Printable open-hardware rover with a named-parts assembly, materials, joints, interfaces, BOM, print profiles, and release documentation.",
				"definition":  openRoverExample(),
			},
		},
		"examples": []map[string]any{
			{
				"name": "Parametric mounting plate",
				"definition": map[string]any{
					"schema": designSchema, "units": "mm",
					"parameters": map[string]any{
						"width":     map[string]any{"type": "number", "default": 80, "min": 20, "max": 300},
						"depth":     map[string]any{"type": "number", "default": 50, "min": 20, "max": 300},
						"thickness": map[string]any{"type": "number", "default": 4, "min": 1, "max": 20},
						"hole":      map[string]any{"type": "number", "default": 4.2, "min": 1, "max": 20},
						"margin":    map[string]any{"type": "number", "default": 7, "min": 2, "max": 40},
					},
					"operations": []map[string]any{
						{"id": "plate", "type": "box", "size": []any{"width", "depth", "thickness"}},
						{"id": "h1", "type": "cylinder", "radius": "hole/2", "height": "thickness+2", "origin": []any{"margin", "margin", -1}},
						{"id": "h2", "type": "cylinder", "radius": "hole/2", "height": "thickness+2", "origin": []any{"width-margin", "margin", -1}},
						{"id": "h3", "type": "cylinder", "radius": "hole/2", "height": "thickness+2", "origin": []any{"width-margin", "depth-margin", -1}},
						{"id": "h4", "type": "cylinder", "radius": "hole/2", "height": "thickness+2", "origin": []any{"margin", "depth-margin", -1}},
						{"id": "drilled", "type": "cut", "inputs": []string{"plate", "h1", "h2", "h3", "h4"}},
						{"id": "finished", "type": "fillet", "input": "drilled", "radius": 1.5},
					},
					"output": "finished",
					"checks": []map[string]any{
						{"type": "bounding_box", "max": []any{300, 300, 20}},
						{"type": "body_count", "equals": 1},
					},
				},
			},
			{
				"name": "Printable spacer",
				"definition": map[string]any{
					"schema": designSchema, "units": "mm",
					"parameters": map[string]any{
						"outer":  map[string]any{"type": "number", "default": 18, "min": 4},
						"inner":  map[string]any{"type": "number", "default": 8.4, "min": 1},
						"height": map[string]any{"type": "number", "default": 12, "min": 1},
					},
					"operations": []map[string]any{
						{"id": "outer", "type": "cylinder", "radius": "outer/2", "height": "height"},
						{"id": "bore", "type": "cylinder", "radius": "inner/2", "height": "height+2", "origin": []any{0, 0, -1}},
						{"id": "spacer", "type": "cut", "inputs": []string{"outer", "bore"}},
					},
					"output": "spacer",
					"checks": []map[string]any{{"type": "body_count", "equals": 1}},
				},
			},
			{
				"name": "Extruded 2D profile",
				"definition": map[string]any{
					"schema": designSchema, "units": "mm",
					"operations": []map[string]any{
						{"id": "profile", "type": "extrude_polygon", "points": []any{[]any{0, 0}, []any{60, 0}, []any{60, 20}, []any{35, 20}, []any{35, 40}, []any{0, 40}}, "height": 6},
						{"id": "softened", "type": "chamfer", "input": "profile", "distance": 1},
					},
					"output": "softened",
				},
			},
		},
	}
}

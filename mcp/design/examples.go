package main

func designExamples() map[string]any {
	return map[string]any{
		"schema": designSchema,
		"units":  "mm",
		"expressions": map[string]any{
			"syntax":   "Numbers or arithmetic strings using parameters, +, -, *, /, unary signs, and parentheses.",
			"examples": []string{"width/2", "height-thickness*2", "(diameter+clearance)/2"},
		},
		"operations": []string{
			"box", "cylinder", "sphere", "extrude_rectangle", "extrude_circle", "extrude_polygon",
			"fuse", "cut", "intersect", "compound", "translate", "rotate", "scale", "fillet", "chamfer",
		},
		"formats": []string{"mesh-json", "glb", "step", "stl", "3mf", "manufacturing-package.zip"},
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

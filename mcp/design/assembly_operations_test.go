package main

import (
	"context"
	"encoding/json"
	"os/exec"
	"testing"
	"time"
)

func TestExtendedOperationsBuild(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun unavailable")
	}
	definition := map[string]any{
		"schema": designSchema, "units": "mm",
		"operations": []map[string]any{
			{"id": "profile", "type": "revolve_profile", "points": [][]any{{4, 0}, {8, 0}, {8, 4}, {4, 4}}, "plane": "XZ", "axis": []any{0, 0, 1}},
			{"id": "mirrored", "type": "mirror", "input": "profile", "plane": "YZ", "origin": []any{20, 0, 0}},
			{"id": "seed", "type": "box", "size": []any{2, 2, 2}, "origin": []any{30, 0, 0}},
			{"id": "linear", "type": "linear_pattern", "input": "seed", "count": 3, "step": []any{0, 5, 0}},
			{"id": "radial_seed", "type": "box", "size": []any{2, 2, 3}, "origin": []any{40, 0, 0}},
			{"id": "radial", "type": "circular_pattern", "input": "radial_seed", "count": 5, "center": []any{35, 0, 0}, "direction": []any{0, 0, 1}},
			{"id": "swept", "type": "sweep_circle", "path": [][]any{{0, 0}, {10, 0}, {10, 10}}, "plane": "XY", "origin": []any{50, 0, 0}, "radius": 1.5},
			{"id": "all", "type": "compound", "inputs": []string{"profile", "mirrored", "linear", "radial", "swept"}},
		},
		"output": "all",
	}
	raw, _ := json.Marshal(definition)
	canonical, parsed, err := normalizeDefinition(raw, 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters(nil, parsed)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(t.TempDir(), bun, 45*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	result, err := engine.Build(context.Background(), "Extended operations", canonical, parameters, []string{"mesh-json"})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Report.Valid || result.Report.TriangleCount < 20 {
		t.Fatalf("unexpected extended-operation report: %#v", result.Report)
	}
}

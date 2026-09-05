package main

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
)

func evaluateChecks(definition *DesignDefinition, report GeometryReport) []CheckResult {
	results := []CheckResult{{
		Type: "geometry_valid", Status: status(report.Valid), Message: ternary(report.Valid, "Geometry is a valid non-empty solid", "Geometry is empty or invalid"),
		Expected: true, Actual: report.Valid,
	}}
	for _, check := range definition.Checks {
		kind, _ := check["type"].(string)
		result := CheckResult{Type: kind, Status: "warning", Message: "Unsupported check type"}
		switch kind {
		case "bounding_box":
			result = checkBounds(check, report.Bounds.Size)
		case "volume":
			result = checkRange(kind, check, report.VolumeMM3)
		case "surface_area":
			result = checkRange(kind, check, report.SurfaceAreaMM2)
		case "body_count":
			result = checkEqualInt(kind, check, report.BodyCount)
		case "triangle_count":
			result = checkRange(kind, check, float64(report.TriangleCount))
		case "face_count":
			result = checkRange(kind, check, float64(report.FaceCount))
		case "mass":
			result = checkRange(kind, check, report.MassG)
		case "ground_clearance":
			result = checkGroundClearance(check, report)
		case "part_clearance":
			result = checkPartClearance(check, report)
		case "assembly_collision":
			result = checkAssemblyCollision(check, report)
		case "manufacturing_rules":
			result = checkManufacturingRules(definition, report)
		case "open_hardware_complete":
			result = checkOpenHardwareComplete(definition)
		}
		results = append(results, result)
	}
	return results
}

func partReport(report GeometryReport, value any) (PartGeometryReport, bool) {
	id, _ := value.(string)
	for _, part := range report.Parts {
		if part.InstanceID == id || part.PartID == id {
			return part, true
		}
	}
	return PartGeometryReport{}, false
}

func checkGroundClearance(check map[string]any, report GeometryReport) CheckResult {
	part, ok := partReport(report, check["part"])
	if !ok {
		return CheckResult{Type: "ground_clearance", Status: "fail", Message: "Ground-clearance target part was not found"}
	}
	ground, _ := numberValue(check["ground_z"])
	actual := part.Bounds.Min[2] - ground
	return checkRange("ground_clearance", check, actual)
}

func checkPartClearance(check map[string]any, report GeometryReport) CheckResult {
	a, okA := partReport(report, check["a"])
	b, okB := partReport(report, check["b"])
	if !okA || !okB {
		return CheckResult{Type: "part_clearance", Status: "fail", Message: "Part-clearance target was not found"}
	}
	actual := aabbClearance(a.Bounds, b.Bounds)
	return checkRange("part_clearance", check, actual)
}

func aabbClearance(a, b Bounds) float64 {
	gaps := [3]float64{}
	overlaps := [3]float64{}
	separated := false
	for axis := range 3 {
		gaps[axis] = math.Max(math.Max(a.Min[axis]-b.Max[axis], b.Min[axis]-a.Max[axis]), 0)
		if gaps[axis] > 0 {
			separated = true
		}
		overlaps[axis] = math.Min(a.Max[axis], b.Max[axis]) - math.Max(a.Min[axis], b.Min[axis])
	}
	if separated {
		return math.Sqrt(gaps[0]*gaps[0] + gaps[1]*gaps[1] + gaps[2]*gaps[2])
	}
	return -math.Min(overlaps[0], math.Min(overlaps[1], overlaps[2]))
}

func checkAssemblyCollision(check map[string]any, report GeometryReport) CheckResult {
	ignored := map[string]bool{}
	if pairs, ok := check["ignore_pairs"].([]any); ok {
		for _, raw := range pairs {
			pair, ok := raw.([]any)
			if !ok || len(pair) != 2 {
				continue
			}
			a, _ := pair[0].(string)
			b, _ := pair[1].(string)
			ignored[a+"\x00"+b], ignored[b+"\x00"+a] = true, true
		}
	}
	collisions := []string{}
	for left := 0; left < len(report.Parts); left++ {
		for right := left + 1; right < len(report.Parts); right++ {
			a, b := report.Parts[left], report.Parts[right]
			if ignored[a.InstanceID+"\x00"+b.InstanceID] || ignored[a.PartID+"\x00"+b.PartID] {
				continue
			}
			if aabbClearance(a.Bounds, b.Bounds) < -0.01 {
				collisions = append(collisions, a.InstanceID+"/"+b.InstanceID)
			}
		}
	}
	passed := len(collisions) == 0
	return CheckResult{Type: "assembly_collision", Status: status(passed), Message: ternary(passed, "No unexpected assembly bounding-box collisions", fmt.Sprintf("Potential collisions: %v", collisions)), Expected: 0, Actual: len(collisions)}
}

func checkManufacturingRules(definition *DesignDefinition, report GeometryReport) CheckResult {
	profiles := map[string]PrintProfile{}
	for _, profile := range definition.PrintProfiles {
		profiles[profile.ID] = profile
	}
	reports := map[string]PartGeometryReport{}
	for _, part := range report.Parts {
		if _, exists := reports[part.PartID]; !exists {
			reports[part.PartID] = part
		}
	}
	issues := []string{}
	for _, part := range definition.Parts {
		manufacturing := part.Manufacturing
		if manufacturing == nil || manufacturing.Classification != "printed" {
			continue
		}
		profile, ok := profiles[manufacturing.PrintProfileID]
		if !ok {
			issues = append(issues, part.ID+": missing print profile")
			continue
		}
		if manufacturing.MinWallMM <= 0 || manufacturing.MinWallMM+1e-9 < profile.NozzleMM*2 {
			issues = append(issues, part.ID+": wall below two nozzle widths")
		}
		if manufacturing.MinFeatureMM <= 0 || manufacturing.MinFeatureMM+1e-9 < profile.NozzleMM {
			issues = append(issues, part.ID+": feature below nozzle width")
		}
		if profile.MaxOverhangDeg > 0 && manufacturing.MaxOverhangDeg > profile.MaxOverhangDeg {
			issues = append(issues, part.ID+": overhang exceeds profile")
		}
		if geometry, exists := reports[part.ID]; exists && len(profile.BedSizeMM) == 3 {
			dims := []float64{geometry.Bounds.Size[0], geometry.Bounds.Size[1], geometry.Bounds.Size[2]}
			sort.Float64s(dims)
			bed := append([]float64(nil), profile.BedSizeMM...)
			sort.Float64s(bed)
			for axis := range 3 {
				if dims[axis] > bed[axis]+0.01 {
					issues = append(issues, part.ID+": does not fit configured print volume")
					break
				}
			}
		}
	}
	passed := len(issues) == 0
	return CheckResult{Type: "manufacturing_rules", Status: status(passed), Message: ternary(passed, "Printed parts satisfy declared FDM rules", strings.Join(issues, "; ")), Expected: 0, Actual: len(issues)}
}

func checkOpenHardwareComplete(definition *DesignDefinition) CheckResult {
	metadata := definition.OpenHardware
	missing := []string{}
	if metadata == nil {
		missing = append(missing, "open_hardware")
	} else {
		if metadata.License == "" {
			missing = append(missing, "license")
		}
		if metadata.Repository == "" {
			missing = append(missing, "repository")
		}
		if metadata.Readme == "" {
			missing = append(missing, "readme")
		}
		if metadata.AssemblyGuide == "" {
			missing = append(missing, "assembly_guide")
		}
	}
	if len(definition.BOM) == 0 {
		missing = append(missing, "bom")
	}
	passed := len(missing) == 0
	return CheckResult{Type: "open_hardware_complete", Status: status(passed), Message: ternary(passed, "Open-hardware release metadata is complete", "Missing open-hardware fields: "+strings.Join(missing, ", ")), Expected: 0, Actual: len(missing)}
}

func checkBounds(check map[string]any, actual [3]float64) CheckResult {
	result := CheckResult{Type: "bounding_box", Status: "pass", Message: "Bounding box is within limits", Actual: actual}
	if expected, ok := numberTriple(check["max"]); ok {
		result.Expected = map[string]any{"max": expected}
		for axis := range 3 {
			if actual[axis] > expected[axis]+0.01 {
				result.Status = "fail"
				result.Message = fmt.Sprintf("Bounding box axis %d exceeds maximum", axis)
			}
		}
	}
	if expected, ok := numberTriple(check["min"]); ok {
		if result.Expected == nil {
			result.Expected = map[string]any{"min": expected}
		} else {
			result.Expected.(map[string]any)["min"] = expected
		}
		for axis := range 3 {
			if actual[axis]+0.01 < expected[axis] {
				result.Status = "fail"
				result.Message = fmt.Sprintf("Bounding box axis %d is below minimum", axis)
			}
		}
	}
	return result
}

func checkRange(kind string, check map[string]any, actual float64) CheckResult {
	result := CheckResult{Type: kind, Status: "pass", Message: kind + " is within limits", Actual: actual}
	expected := map[string]any{}
	if min, ok := numberValue(check["min"]); ok {
		expected["min"] = min
		if actual < min {
			result.Status = "fail"
			result.Message = fmt.Sprintf("%s is below minimum", kind)
		}
	}
	if max, ok := numberValue(check["max"]); ok {
		expected["max"] = max
		if actual > max {
			result.Status = "fail"
			result.Message = fmt.Sprintf("%s exceeds maximum", kind)
		}
	}
	result.Expected = expected
	return result
}

func checkEqualInt(kind string, check map[string]any, actual int) CheckResult {
	expected, ok := numberValue(check["equals"])
	if !ok || math.Trunc(expected) != expected {
		return CheckResult{Type: kind, Status: "warning", Message: "equals must be an integer", Actual: actual}
	}
	passed := actual == int(expected)
	return CheckResult{Type: kind, Status: status(passed), Message: ternary(passed, kind+" matches", kind+" does not match"), Expected: int(expected), Actual: actual}
}

func numberValue(value any) (float64, bool) {
	switch number := value.(type) {
	case float64:
		return number, true
	case json.Number:
		parsed, err := number.Float64()
		return parsed, err == nil
	case int:
		return float64(number), true
	}
	return 0, false
}

func numberTriple(value any) ([3]float64, bool) {
	var out [3]float64
	items, ok := value.([]any)
	if !ok || len(items) != 3 {
		return out, false
	}
	for index, item := range items {
		number, ok := numberValue(item)
		if !ok {
			return out, false
		}
		out[index] = number
	}
	return out, true
}

func status(ok bool) string { return ternary(ok, "pass", "fail") }

func ternary[T any](condition bool, yes, no T) T {
	if condition {
		return yes
	}
	return no
}

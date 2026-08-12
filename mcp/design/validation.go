package main

import (
	"encoding/json"
	"fmt"
	"math"
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
		}
		results = append(results, result)
	}
	return results
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

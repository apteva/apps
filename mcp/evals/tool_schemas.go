package main

func strictObject(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{
		"type":                 "object",
		"properties":           properties,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items}
}

func nonEmptyArraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items, "minItems": 1}
}

var targetInputSchema = strictObject(map[string]any{
	"agent_id": map[string]any{"type": "integer", "minimum": 1},
	"provider": map[string]any{"type": "string"},
	"model":    map[string]any{"type": "string"},
}, "agent_id")

var assertionInputSchema = strictObject(map[string]any{
	"name":        map[string]any{"type": "string"},
	"type":        map[string]any{"type": "string", "description": "Use a type listed by eval_catalog.assertion_types. output_equals is evaluated natively by Evals against the final assistant message."},
	"app":         map[string]any{"type": "string"},
	"mcp":         map[string]any{"type": "string"},
	"tool":        map[string]any{"type": "string"},
	"input":       map[string]any{"type": "object", "additionalProperties": true},
	"path":        map[string]any{"type": "string"},
	"equals":      map[string]any{},
	"method":      map[string]any{"type": "string"},
	"host":        map[string]any{"type": "string"},
	"min_calls":   map[string]any{"type": "integer", "minimum": 0},
	"agent_alias": map[string]any{"type": "string"},
	"event_type":  map[string]any{"type": "string"},
	"fixture":     map[string]any{"type": "string"},
}, "name", "type")

var voiceCaseInputSchema = strictObject(map[string]any{
	"caller_name":             map[string]any{"type": "string"},
	"caller_persona":          map[string]any{"type": "string"},
	"caller_goal":             map[string]any{"type": "string"},
	"caller_behavior":         map[string]any{"type": "string"},
	"provider":                map[string]any{"type": "string"},
	"voice":                   map[string]any{"type": "string"},
	"caller_provider":         map[string]any{"type": "string"},
	"caller_voice":            map[string]any{"type": "string"},
	"greeting":                map[string]any{"type": "string"},
	"max_first_response_ms":   map[string]any{"type": "integer", "minimum": 0},
	"max_average_response_ms": map[string]any{"type": "integer", "minimum": 0},
	"transport":               map[string]any{"type": "string", "enum": []string{"direct", "carrier"}},
	"protocol_fixture":        map[string]any{"type": "string"},
	"audio_conditions": strictObject(map[string]any{
		"preset":    map[string]any{"type": "string", "enum": []string{"clean", "office", "cafe", "street", "train_station", "poor_phone"}},
		"intensity": map[string]any{"type": "string", "enum": []string{"light", "moderate", "heavy"}},
		"codec":     map[string]any{"type": "string", "enum": []string{"none", "g711_mulaw"}},
		"seed":      map[string]any{"type": "integer", "minimum": 0},
	}),
})

var evalSuiteCreateSchema = strictObject(map[string]any{
	"id":                 map[string]any{"type": "string"},
	"name":               map[string]any{"type": "string"},
	"description":        map[string]any{"type": "string"},
	"environment_id":     map[string]any{"type": "string"},
	"judge_model":        map[string]any{"type": "string", "description": "Qualified gateway_model from eval_catalog.models[].gateway_model. Bare model IDs are accepted only when unambiguous."},
	"continuous_targets": arraySchema(targetInputSchema),
	"schedule_minutes":   map[string]any{"type": "integer", "minimum": 0},
	"required_pass_rate": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
}, "name")

var evalCaseCreateSchema = strictObject(map[string]any{
	"id":              map[string]any{"type": "string"},
	"suite_id":        map[string]any{"type": "string"},
	"name":            map[string]any{"type": "string"},
	"prompt":          map[string]any{"type": "string"},
	"mode":            map[string]any{"type": "string", "enum": []string{"text", "voice"}},
	"voice":           voiceCaseInputSchema,
	"goals":           arraySchema(map[string]any{"type": "string"}),
	"assertions":      arraySchema(assertionInputSchema),
	"environment_id":  map[string]any{"type": "string"},
	"weight":          map[string]any{"type": "number", "minimum": 0},
	"timeout_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 1800},
	"max_turns":       map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
}, "suite_id", "name", "prompt")

var evalExperimentCreateSchema = strictObject(map[string]any{
	"suite_id":        map[string]any{"type": "string"},
	"name":            map[string]any{"type": "string"},
	"targets":         nonEmptyArraySchema(targetInputSchema),
	"repetitions":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
	"baseline_target": map[string]any{"type": "integer", "minimum": 0},
	"judge_model":     map[string]any{"type": "string", "description": "Qualified gateway_model from eval_catalog.models[].gateway_model. Bare model IDs are accepted only when unambiguous."},
}, "suite_id", "targets")

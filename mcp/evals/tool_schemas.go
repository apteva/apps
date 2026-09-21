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

var draftAgentInputSchema = strictObject(map[string]any{
	"name":      map[string]any{"type": "string", "minLength": 1},
	"directive": map[string]any{"type": "string", "minLength": 1},
	"mode":      map[string]any{"type": "string", "enum": []string{"autonomous", "cautious", "learn"}},
	"config":    map[string]any{"type": "string", "description": "JSON object encoded as a string; defaults to {}."},
}, "name", "directive")

var targetInputSchema = strictObject(map[string]any{
	"agent_id": map[string]any{"type": "integer", "minimum": 1},
	"draft":    draftAgentInputSchema,
	"provider": map[string]any{"type": "string"},
	"model":    map[string]any{"type": "string"},
})

func init() {
	targetInputSchema["oneOf"] = []any{
		map[string]any{"required": []string{"agent_id"}, "not": map[string]any{"required": []string{"draft"}}},
		map[string]any{"required": []string{"draft"}, "not": map[string]any{"required": []string{"agent_id"}}},
	}
}

var assertionEvidenceSchema = strictObject(map[string]any{
	"name":          map[string]any{"type": "string"},
	"type":          map[string]any{"type": "string", "description": "Use a type listed by eval_catalog.assertion_types. output_equals is evaluated natively by Evals against the final assistant message."},
	"app":           map[string]any{"type": "string"},
	"mcp":           map[string]any{"type": "string"},
	"tool":          map[string]any{"type": "string"},
	"input":         map[string]any{"type": "object", "additionalProperties": true},
	"path":          map[string]any{"type": "string"},
	"equals":        map[string]any{},
	"method":        map[string]any{"type": "string"},
	"host":          map[string]any{"type": "string"},
	"min_calls":     map[string]any{"type": "integer", "minimum": 0},
	"agent_alias":   map[string]any{"type": "string"},
	"event_type":    map[string]any{"type": "string"},
	"fixture":       map[string]any{"type": "string"},
	"weight":        map[string]any{"type": "number", "minimum": 0},
	"critical":      map[string]any{"type": "boolean"},
	"category":      map[string]any{"type": "string"},
	"disqualifying": map[string]any{"type": "boolean"},
}, "type")

var assertionInputSchema = strictObject(map[string]any{
	"name":            map[string]any{"type": "string"},
	"type":            map[string]any{"type": "string", "description": "Use a type listed by eval_catalog.assertion_types. output_equals is evaluated natively by Evals."},
	"app":             map[string]any{"type": "string"},
	"mcp":             map[string]any{"type": "string"},
	"tool":            map[string]any{"type": "string"},
	"input":           map[string]any{"type": "object", "additionalProperties": true},
	"path":            map[string]any{"type": "string"},
	"equals":          map[string]any{},
	"method":          map[string]any{"type": "string"},
	"host":            map[string]any{"type": "string"},
	"min_calls":       map[string]any{"type": "integer", "minimum": 0},
	"agent_alias":     map[string]any{"type": "string"},
	"event_type":      map[string]any{"type": "string"},
	"fixture":         map[string]any{"type": "string"},
	"weight":          map[string]any{"type": "number", "minimum": 0},
	"critical":        map[string]any{"type": "boolean"},
	"category":        map[string]any{"type": "string"},
	"disqualifying":   map[string]any{"type": "boolean"},
	"evidence_any_of": nonEmptyArraySchema(assertionEvidenceSchema),
}, "name")

var goalInputSchema = map[string]any{"oneOf": []any{
	map[string]any{"type": "string", "minLength": 1},
	strictObject(map[string]any{
		"text":     map[string]any{"type": "string", "minLength": 1},
		"weight":   map[string]any{"type": "number", "minimum": 0},
		"critical": map[string]any{"type": "boolean"},
		"category": map[string]any{"type": "string"},
	}, "text"),
}}

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
	"id":             map[string]any{"type": "string"},
	"suite_id":       map[string]any{"type": "string"},
	"name":           map[string]any{"type": "string"},
	"prompt":         map[string]any{"type": "string"},
	"mode":           map[string]any{"type": "string", "enum": []string{"text", "voice"}},
	"voice":          voiceCaseInputSchema,
	"goals":          arraySchema(goalInputSchema),
	"assertions":     arraySchema(assertionInputSchema),
	"environment_id": map[string]any{"type": "string"},
	"environment": map[string]any{
		"type":                 "object",
		"description":          "Self-contained ephemeral Environments spec. Use apps with stable app names; Evals resolves current project install ids for every run.",
		"additionalProperties": true,
		"properties": map[string]any{
			"version":          map[string]any{"type": "integer", "enum": []int{1}},
			"apps":             arraySchema(map[string]any{"type": "string", "minLength": 1}),
			"network_mode":     map[string]any{"type": "string"},
			"integration_mode": map[string]any{"type": "string"},
			"ttl_seconds":      map[string]any{"type": "integer", "minimum": 1},
			"seeds":            arraySchema(map[string]any{"type": "object", "additionalProperties": true}),
		},
	},
	"weight":          map[string]any{"type": "number", "minimum": 0},
	"timeout_seconds": map[string]any{"type": "integer", "minimum": 0, "maximum": 1800},
	"max_turns":       map[string]any{"type": "integer", "minimum": 0, "maximum": 100},
	"rating_profile":  map[string]any{"type": "string", "enum": []string{"agentic-quality-v2"}},
}, "suite_id", "name", "prompt")

var evalExperimentCreateSchema = strictObject(map[string]any{
	"suite_id":        map[string]any{"type": "string"},
	"name":            map[string]any{"type": "string"},
	"targets":         nonEmptyArraySchema(targetInputSchema),
	"repetitions":     map[string]any{"type": "integer", "minimum": 1, "maximum": 20},
	"baseline_target": map[string]any{"type": "integer", "minimum": 0},
	"judge_model":     map[string]any{"type": "string", "description": "Qualified gateway_model from eval_catalog.models[].gateway_model. Bare model IDs are accepted only when unambiguous."},
}, "suite_id", "targets")

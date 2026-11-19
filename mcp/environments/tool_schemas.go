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

func stringMapSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "string"}}
}

func anyObjectSchema() map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true}
}

var environmentSpecSchema = strictObject(map[string]any{
	"targets": map[string]any{"type": "object", "additionalProperties": strictObject(map[string]any{
		"app": map[string]any{"type": "string"}, "path": map[string]any{"type": "string"},
	}, "app", "path")},
	"version":     map[string]any{"type": "integer", "minimum": 1},
	"ttl_seconds": map[string]any{"type": "integer", "minimum": 60, "maximum": 86400},
	"clock": strictObject(map[string]any{
		"mode":         map[string]any{"type": "string", "enum": []string{"real", "manual"}},
		"initial_time": map[string]any{"type": "string", "format": "date-time"},
	}, "mode"),
	"app_install_ids":     arraySchema(map[string]any{"type": "integer"}),
	"connection_ids":      arraySchema(map[string]any{"type": "integer"}),
	"mcp_server_ids":      arraySchema(map[string]any{"type": "integer"}),
	"network_mode":        map[string]any{"type": "string", "enum": []string{"block", "passthrough", "record", "replay"}},
	"integration_mode":    map[string]any{"type": "string", "enum": []string{"mock", "real"}},
	"allow_host_suffixes": arraySchema(map[string]any{"type": "string"}),
	"http_mocks": arraySchema(strictObject(map[string]any{
		"host":         map[string]any{"type": "string"},
		"path":         map[string]any{"type": "string"},
		"method":       map[string]any{"type": "string"},
		"status":       map[string]any{"type": "integer"},
		"headers":      stringMapSchema(),
		"body":         map[string]any{},
		"available_at": map[string]any{"type": "string", "format": "date-time"},
		"expires_at":   map[string]any{"type": "string", "format": "date-time"},
	})),
	"integration_fixtures": arraySchema(strictObject(map[string]any{
		"app":          map[string]any{"type": "string"},
		"tool":         map[string]any{"type": "string"},
		"status":       map[string]any{"type": "integer"},
		"data":         map[string]any{},
		"available_at": map[string]any{"type": "string", "format": "date-time"},
		"expires_at":   map[string]any{"type": "string", "format": "date-time"},
	})),
	"integration_bindings": arraySchema(strictObject(map[string]any{
		"app":              map[string]any{"type": "string"},
		"role":             map[string]any{"type": "string"},
		"slug":             map[string]any{"type": "string"},
		"app_name":         map[string]any{"type": "string"},
		"name":             map[string]any{"type": "string"},
		"auth_type":        map[string]any{"type": "string"},
		"credentials":      stringMapSchema(),
		"expose_to_agents": map[string]any{"type": "boolean"},
		"simulate":         map[string]any{"type": "boolean", "description": "Run the integration's catalog simulation instead of a dead fake connection. Drive it with environment_integration_trigger and read it with environment_integration_state."},
		"seed": map[string]any{
			"type":                 "object",
			"description":          "Pre-load simulation collections, for example {\"phone_numbers\": [{\"phone_number\": \"+15550100001\"}]}. Requires simulate.",
			"additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "object"}},
		},
	})),
	"connection_bindings": arraySchema(strictObject(map[string]any{
		"app":           map[string]any{"type": "string"},
		"role":          map[string]any{"type": "string"},
		"connection_id": map[string]any{"type": "integer"},
	}, "app", "role", "connection_id")),
	"subscriptions": arraySchema(strictObject(map[string]any{
		"id":                 map[string]any{"type": "string"},
		"source":             map[string]any{"type": "string"},
		"app":                map[string]any{"type": "string"},
		"topic":              map[string]any{"type": "string"},
		"target_agent_alias": map[string]any{"type": "string"},
		"thread_id":          map[string]any{"type": "string"},
		"name":               map[string]any{"type": "string"},
		"description":        map[string]any{"type": "string"},
		"enabled":            map[string]any{"type": "boolean"},
	})),
	"seeds": arraySchema(strictObject(map[string]any{
		"app":  map[string]any{"type": "string"},
		"tool": map[string]any{"type": "string"},
		"input": map[string]any{
			"type":                 "object",
			"additionalProperties": true,
			"description":          `Tool arguments. Anywhere in this object, {"$ref": "<index>.<path>"} is replaced by the value at that dotted path in the result of an earlier seed, so seed 1 can use an id created by seed 0. An empty path references the whole result.`,
		},
	}, "app", "tool")),
	"agents": arraySchema(strictObject(map[string]any{
		"source_agent_id": map[string]any{"type": "integer"},
		"draft": strictObject(map[string]any{
			"name":      map[string]any{"type": "string"},
			"directive": map[string]any{"type": "string"},
			"mode":      map[string]any{"type": "string"},
			"config":    map[string]any{"type": "string"},
		}),
		"directive":    map[string]any{"type": "string"},
		"alias":        map[string]any{"type": "string"},
		"start_paused": map[string]any{"type": "boolean"},
		"provider":     map[string]any{"type": "string"},
		"model":        map[string]any{"type": "string"},
	})),
	"snapshot_id": map[string]any{"type": "string"},
	"web_fixtures": arraySchema(strictObject(map[string]any{
		"id":       map[string]any{"type": "string"},
		"pack":     map[string]any{"type": "string"},
		"version":  map[string]any{"type": "string"},
		"scenario": map[string]any{"type": "string"},
		"strict":   map[string]any{"type": "boolean"},
		"seed":     anyObjectSchema(),
	}, "id", "pack")),
	"protocol_fixtures": arraySchema(strictObject(map[string]any{
		"id":         map[string]any{"type": "string"},
		"pack":       map[string]any{"type": "string"},
		"version":    map[string]any{"type": "string"},
		"target_app": map[string]any{"type": "string"},
		"config":     anyObjectSchema(),
	}, "id", "pack")),
	"voice_fixtures": arraySchema(strictObject(map[string]any{
		"id":                 map[string]any{"type": "string"},
		"name":               map[string]any{"type": "string"},
		"caller_name":        map[string]any{"type": "string"},
		"caller_persona":     map[string]any{"type": "string"},
		"caller_goal":        map[string]any{"type": "string"},
		"caller_behavior":    map[string]any{"type": "string"},
		"provider":           map[string]any{"type": "string"},
		"voice":              map[string]any{"type": "string"},
		"caller_provider":    map[string]any{"type": "string"},
		"caller_voice":       map[string]any{"type": "string"},
		"timeout_seconds":    map[string]any{"type": "integer"},
		"greeting":           map[string]any{"type": "string"},
		"target_agent":       map[string]any{"type": "string"},
		"target_directive":   map[string]any{"type": "string"},
		"disconnect_on_done": map[string]any{"type": "boolean"},
		"transport":          map[string]any{"type": "string", "enum": []string{"direct", "carrier"}},
		"protocol_fixture":   map[string]any{"type": "string"},
		"audio_conditions": strictObject(map[string]any{
			"preset":    map[string]any{"type": "string", "enum": []string{"clean", "office", "cafe", "street", "train_station", "poor_phone"}},
			"intensity": map[string]any{"type": "string", "enum": []string{"light", "moderate", "heavy"}},
			"codec":     map[string]any{"type": "string", "enum": []string{"none", "g711_mulaw"}},
			"seed":      map[string]any{"type": "integer", "minimum": 0},
		}),
	}, "caller_goal")),
})

// Exactly one of spec (inline) or environment_id (pinned stored definition) is
// required. The rule is enforced by the handler and stated in the property
// descriptions rather than a top-level oneOf, which some tool clients reject.
var environmentRunCreateSchema = strictObject(map[string]any{
	"request_id": map[string]any{"type": "string", "description": "Optional unique run_ identifier, reserved before startup."},
	"kind":       map[string]any{"type": "string"},
	"spec":       withDescription(environmentSpecSchema, "Inline EnvironmentSpec. Mutually exclusive with environment_id."),
	"environment_id": map[string]any{"type": "string", "maxLength": maxSummaryIDBytes,
		"description": "Create the run from this stored definition instead of an inline spec. Requires spec_version and spec_digest; the run fails if either no longer matches the definition."},
	"spec_version": map[string]any{"type": "integer", "minimum": 1,
		"description": "Pinned spec_version, as returned by environment_get. Only with environment_id."},
	"spec_digest": map[string]any{"type": "string", "pattern": "^[0-9a-f]{64}$",
		"description": "Pinned canonical spec_digest (lowercase hex SHA-256), as returned by environment_get. Only with environment_id."},
	"seed_vars": map[string]any{
		"type":                 "object",
		"maxProperties":        maxSeedVars,
		"propertyNames":        map[string]any{"pattern": "^[a-z][a-z0-9_]{0,63}$"},
		"additionalProperties": map[string]any{"type": "string", "maxLength": maxSeedVarValueBytes, "pattern": "^[^\\r\\n]*$"},
		"description":          "Only with environment_id. In seeds[].input, a string that is exactly {{name}} becomes the value and {{name}} inside longer strings is replaced textually; unknown placeholders are left unchanged. Values are strings of at most 4096 bytes without CR or LF.",
	},
})

func withDescription(schema map[string]any, description string) map[string]any {
	out := make(map[string]any, len(schema)+1)
	for key, value := range schema {
		out[key] = value
	}
	out["description"] = description
	return out
}

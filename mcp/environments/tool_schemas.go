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
	"version":             map[string]any{"type": "integer", "minimum": 1},
	"ttl_seconds":         map[string]any{"type": "integer", "minimum": 60, "maximum": 86400},
	"app_install_ids":     arraySchema(map[string]any{"type": "integer"}),
	"connection_ids":      arraySchema(map[string]any{"type": "integer"}),
	"mcp_server_ids":      arraySchema(map[string]any{"type": "integer"}),
	"network_mode":        map[string]any{"type": "string", "enum": []string{"block", "passthrough", "record", "replay"}},
	"integration_mode":    map[string]any{"type": "string", "enum": []string{"mock", "real"}},
	"allow_host_suffixes": arraySchema(map[string]any{"type": "string"}),
	"http_mocks": arraySchema(strictObject(map[string]any{
		"host":    map[string]any{"type": "string"},
		"path":    map[string]any{"type": "string"},
		"method":  map[string]any{"type": "string"},
		"status":  map[string]any{"type": "integer"},
		"headers": stringMapSchema(),
		"body":    map[string]any{},
	})),
	"integration_fixtures": arraySchema(strictObject(map[string]any{
		"app":    map[string]any{"type": "string"},
		"tool":   map[string]any{"type": "string"},
		"status": map[string]any{"type": "integer"},
		"data":   map[string]any{},
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
		"app":   map[string]any{"type": "string"},
		"tool":  map[string]any{"type": "string"},
		"input": anyObjectSchema(),
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

var environmentRunCreateSchema = strictObject(map[string]any{
	"kind": map[string]any{"type": "string"},
	"spec": environmentSpecSchema,
}, "spec")

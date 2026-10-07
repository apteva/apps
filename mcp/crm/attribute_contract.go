package main

import (
	"encoding/json"
	"fmt"
	"math"
)

func attributeValueInputSchema() map[string]any {
	return map[string]any{
		"description": "Value matching the project-owned attribute definition. Prefer native JSON types. Only number/bool definitions accept legacy JSON scalar strings (e.g. \"88\"/\"false\"). Text, select, URL and date strings remain unchanged. Null clears only optional attributes.",
		"anyOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "number"},
			map[string]any{"type": "boolean"},
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
			map[string]any{"type": "null"},
		},
		"examples": []any{float64(88), false, "00123", []any{"vip"}, nil},
	}
}

// Core's legacy MCP adapter stringifies scalar arguments. Resolve the actual
// project's definition before recovering a number or boolean, never infer a
// type from the string itself. Keep this compatibility rule at the MCP boundary:
// HTTP, contact patches and dbSetAttribute continue to require native JSON types.
func normalizeMCPAttributeValue(db sqlQueryExecer, pid, key string, value any) (any, error) {
	text, isString := value.(string)
	if !isString {
		return value, nil
	}
	def, err := resolveAttributeDefinition(db, pid, key)
	if err != nil {
		return nil, err
	}
	if def.Type != "number" && def.Type != "bool" {
		return value, nil
	}
	var decoded any
	if err := json.Unmarshal([]byte(text), &decoded); err == nil {
		if number, ok := decoded.(float64); def.Type == "number" && ok && !math.IsInf(number, 0) && !math.IsNaN(number) {
			return number, nil
		}
		if boolean, ok := decoded.(bool); def.Type == "bool" && ok {
			return boolean, nil
		}
	}
	if def.Type == "number" {
		return nil, fmt.Errorf("attribute %q expects a finite JSON number (example: value: 88); legacy adapters may pass its JSON scalar as text; write rejected, do not advance workflow readiness", key)
	}
	return nil, fmt.Errorf("attribute %q expects a JSON boolean (example: value: false); legacy adapters may pass exactly true or false as text; write rejected, do not advance workflow readiness", key)
}

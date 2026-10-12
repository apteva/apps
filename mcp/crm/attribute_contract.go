package main

import (
	"encoding/json"
	"fmt"
	"math"
)

func attributeValueInputSchema() map[string]any {
	schema := strictAttributeValueInputSchema()
	schema["description"] = "Value matching the project-owned attribute definition. Prefer native JSON types. Only contacts_set_attribute accepts legacy JSON scalar strings for number/bool definitions (e.g. \"88\"/\"false\"). Text, select, URL and date strings remain unchanged. Null clears only optional attributes. Discover keys/types/allowed values with contacts_attribute_definitions_list."
	return schema
}

func strictAttributeValueInputSchema() map[string]any {
	return map[string]any{
		"description": "Match the project definition from contacts_attribute_definitions_list: text/url/date/select use strings; number uses a finite JSON number; bool uses a JSON boolean; multi_select uses an array of allowed strings. Update patches do NOT convert numeric/boolean strings. Explicit null clears only optional attributes; value must be present.",
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

func contactPatchInputSchema() map[string]any {
	properties := map[string]any{}
	for _, key := range []string{"first_name", "last_name", "display_name", "pronouns", "company", "job_title"} {
		properties[key] = map[string]any{"type": []string{"string", "null"}, "description": "String or explicit null to clear the field."}
	}
	properties["owner_user_id"] = map[string]any{"type": []string{"integer", "null"}}
	properties["status"] = map[string]any{"type": "string", "enum": []string{"active", "archived", "spam"}}
	for _, key := range []string{"first_contact_at", "last_contact_at"} {
		properties[key] = map[string]any{"type": []string{"string", "null"}, "format": "date-time", "description": "RFC3339 timestamp or explicit null."}
	}
	properties["expected_updated_at"] = map[string]any{"type": "string", "description": "Optional optimistic concurrency token from contacts_get; stale patches fail without overwriting newer data."}
	channel := schemaObject(map[string]any{
		"kind": map[string]any{"type": "string"}, "value": map[string]any{"type": "string"},
		"label": map[string]any{"type": "string"}, "is_primary": map[string]any{"type": "boolean"},
	}, []string{"kind", "value"})
	channel["additionalProperties"] = false
	properties["channels"] = map[string]any{"type": "array", "maxItems": 100, "items": channel, "description": "Replaces the entire channel list, including primary markers. Omit to keep it unchanged; [] removes all channels. primary_email/primary_phone are read-only mirrors."}
	attribute := schemaObject(map[string]any{"key": map[string]any{"type": "string", "minLength": 1}, "value": strictAttributeValueInputSchema(), "source": map[string]any{"type": "string", "description": "Optional per-attribute provenance overriding the outer source."}}, []string{"key", "value"})
	attribute["additionalProperties"] = false
	properties["attributes"] = map[string]any{"type": "array", "items": attribute, "description": "Array of {key,value,source?}, NOT a key/value dictionary. Updates only listed attributes; omitted keys and [] leave existing values unchanged. Explicit value:null clears an optional attribute. Definitions must already exist; discover with contacts_attribute_definitions_list.", "examples": []any{[]any{map[string]any{"key": "lead_score", "value": 88}, map[string]any{"key": "do_not_contact", "value": false}}}}
	schema := schemaObject(properties, nil)
	schema["additionalProperties"] = false
	return schema
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

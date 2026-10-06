package main

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestApprovalToolsDescribeRequiredActionFields(t *testing.T) {
	app := &App{}
	for _, tool := range app.MCPTools() {
		if tool.Name != "request_approval" && tool.Name != "inbox_post" {
			continue
		}
		t.Run(tool.Name, func(t *testing.T) {
			encoded, err := json.Marshal(tool.InputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(encoded, &schema); err != nil {
				t.Fatal(err)
			}
			actions := schema["properties"].(map[string]any)["actions"].(map[string]any)
			items := actions["items"].(map[string]any)
			if !reflect.DeepEqual(items["required"], []any{"id", "label"}) {
				t.Fatalf("required fields: %v", items["required"])
			}
			props := items["properties"].(map[string]any)
			if props["id"] == nil || props["label"] == nil || props["style"] == nil {
				t.Fatal("action schema is incomplete")
			}
			if actions["minItems"] != float64(1) || actions["maxItems"] != float64(8) {
				t.Fatal("schema bounds differ from handler")
			}
		})
	}
}

func TestApprovalActionsRepeatListRegression(t *testing.T) {
	// The actual failing model call used value rather than id. Do not silently
	// reinterpret arbitrary identifiers: the schema must teach the correct field.
	_, err := approvalActionsArg(map[string]any{"actions": []any{map[string]any{"value": "approve", "label": "Delete RepeatList"}}})
	if err == nil {
		t.Fatal("missing id accepted")
	}
	actions, err := approvalActionsArg(map[string]any{"actions": []any{
		map[string]any{"id": "delete_repeatlist", "label": "Delete RepeatList"},
		map[string]any{"id": "keep_repeatlist", "label": "Keep RepeatList"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if len(actions) != 2 || actions[0].ID != "delete_repeatlist" || actions[1].ID != "keep_repeatlist" {
		t.Fatal(actions)
	}
	defaults, err := approvalActionsArg(map[string]any{})
	if err != nil || defaults[0].Style != "primary" || defaults[1].Style != "secondary" {
		t.Fatalf("default styles: %+v %v", defaults, err)
	}
}

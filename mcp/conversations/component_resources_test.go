package main

import (
	"encoding/json"
	"testing"
)

func TestComponentResourceBoundary(t *testing.T) {
	reference := map[string]any{"app_install_id": 19, "resource_type": "computer.browser_session", "resource_id": "br_test"}
	input := map[string]any{"app": "computer", "name": "browser-view", "props": map[string]any{"session_id": "br_test"}, "resource": reference}
	cards, err := componentsArg(map[string]any{"components": []any{input}}, "components")
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(cards)
	var restored []Component
	if err := json.Unmarshal(raw, &restored); err != nil || restored[0].Resource == nil || restored[0].Resource.ResourceID != "br_test" {
		t.Fatalf("resource lost in durable JSON: %s %v", raw, err)
	}
	for _, invalid := range []map[string]any{
		{"app_install_id": 0, "resource_type": "computer.browser_session", "resource_id": "br_test"},
		{"app_install_id": 19, "resource_type": "browser", "resource_id": "br_test"},
		{"app_install_id": 19, "resource_type": "computer.browser_session", "resource_id": ""},
	} {
		input["resource"] = invalid
		if _, err := componentsArg(map[string]any{"components": []any{input}}, "components"); err == nil {
			t.Fatalf("accepted invalid reference %+v", invalid)
		}
	}
}

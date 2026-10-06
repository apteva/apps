package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func toolContract(t *testing.T, name string) (string, map[string]any) {
	t.Helper()
	for _, tool := range (&App{}).MCPTools() {
		if tool.Name == name {
			return tool.Description, tool.InputSchema
		}
	}
	t.Fatalf("tool %q not found", name)
	return "", nil
}

func schemaProperties(t *testing.T, schema map[string]any) map[string]any {
	t.Helper()
	properties, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema properties=%#v (%T)", schema["properties"], schema["properties"])
	}
	return properties
}

func TestSegmentToolContractDocumentsEveryPredicate(t *testing.T) {
	createDescription, createSchema := toolContract(t, "segments_create")
	updateDescription, updateSchema := toolContract(t, "segments_update")

	createDefinition, ok := schemaProperties(t, createSchema)["definition"].(map[string]any)
	if !ok {
		t.Fatalf("segments_create definition schema missing: %#v", createSchema)
	}
	patch, ok := schemaProperties(t, updateSchema)["patch"].(map[string]any)
	if !ok {
		t.Fatalf("segments_update patch schema missing: %#v", updateSchema)
	}
	updateDefinition, ok := schemaProperties(t, patch)["definition"].(map[string]any)
	if !ok {
		t.Fatalf("segments_update definition schema missing: %#v", patch)
	}
	if !reflect.DeepEqual(createDefinition, updateDefinition) {
		t.Fatal("segments_create and segments_update expose different definition contracts")
	}

	contractJSON, err := json.Marshal(createDefinition)
	if err != nil {
		t.Fatal(err)
	}
	contractText := createDescription + "\n" + updateDescription + "\n" + string(contractJSON)
	for _, predicate := range supportedSegmentPredicates {
		if !strings.Contains(contractText, predicate) {
			t.Errorf("MCP contract does not document predicate %q", predicate)
		}
	}
	for _, required := range []string{
		`{"predicate":"tag_in","tags":["vip"]}`,
		`{"field":"company","op":"eq","value":"Acme"}`,
		"first_name",
		"is_not_null",
	} {
		if !strings.Contains(contractText, required) {
			t.Errorf("MCP contract missing copyable guidance %q", required)
		}
	}
}

func TestDocumentedSegmentDefinitionExamplesCompile(t *testing.T) {
	_, createSchema := toolContract(t, "segments_create")
	definition := schemaProperties(t, createSchema)["definition"].(map[string]any)
	examples, ok := definition["examples"].([]any)
	if !ok || len(examples) == 0 {
		t.Fatalf("definition examples=%#v", definition["examples"])
	}
	for i, example := range examples {
		raw, err := json.Marshal(example)
		if err != nil {
			t.Fatalf("example %d marshal: %v", i+1, err)
		}
		if _, err := compileSegmentDefinition("test-proj", nil, raw); err != nil {
			t.Errorf("example %d does not compile: %s: %v", i+1, raw, err)
		}
	}
}

func TestSegmentsCreateAcceptsDocumentedTagInExample(t *testing.T) {
	ctx := newTestCtx(t)
	out, err := (&App{}).toolSegmentsCreate(ctx, map[string]any{
		"name": "VIP contacts",
		"kind": "dynamic",
		"definition": []any{
			map[string]any{"predicate": "tag_in", "tags": []any{"vip"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	segment, ok := out.(map[string]any)["segment"].(*Segment)
	if !ok || segment == nil || segment.Name != "VIP contacts" {
		t.Fatalf("unexpected create response: %#v", out)
	}
}

func TestUnknownSegmentPredicateListsSupportedPredicates(t *testing.T) {
	_, err := compileSegmentDefinition("test-proj", nil, json.RawMessage(`[{"predicate":"guessed_shape"}]`))
	if err == nil {
		t.Fatal("unknown predicate accepted")
	}
	message := err.Error()
	if !strings.Contains(message, `unknown predicate "guessed_shape"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, predicate := range supportedSegmentPredicates {
		if !strings.Contains(message, predicate) {
			t.Errorf("validation error missing supported predicate %q: %v", predicate, err)
		}
	}
}

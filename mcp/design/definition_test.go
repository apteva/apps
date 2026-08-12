package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testDefinition() []byte {
	return []byte(`{
		"schema":"apteva-design/v1","units":"mm",
		"parameters":{"width":{"type":"number","default":40,"min":10,"max":100}},
		"operations":[
			{"id":"body","type":"box","size":["width",20,5]},
			{"id":"hole","type":"cylinder","radius":3,"height":7,"origin":[10,10,-1]},
			{"id":"result","type":"cut","inputs":["body","hole"]}
		],
		"output":"result",
		"checks":[{"type":"bounding_box","max":[100,100,10]}]
	}`)
}

func TestNormalizeDefinitionAndParameters(t *testing.T) {
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	if !json.Valid(canonical) || definition.Output != "result" {
		t.Fatalf("unexpected normalized definition: %s", canonical)
	}
	parameters, err := normalizeParameters([]byte(`{"width":55}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	if string(parameters) != `{"width":55}` {
		t.Fatalf("unexpected normalized parameters: %s", parameters)
	}
	if _, err := normalizeParameters([]byte(`{"width":101}`), definition); err == nil || !strings.Contains(err.Error(), "max") {
		t.Fatalf("expected maximum error, got %v", err)
	}
}

func TestNormalizeDefinitionRejectsUnsafeOrBrokenGraphs(t *testing.T) {
	cases := []string{
		`{"schema":"other","units":"mm","operations":[{"id":"a","type":"box"}],"output":"a"}`,
		`{"schema":"apteva-design/v1","units":"inch","operations":[{"id":"a","type":"box"}],"output":"a"}`,
		`{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"a","type":"javascript"}],"output":"a"}`,
		`{"schema":"apteva-design/v1","units":"mm","operations":[{"id":"a","type":"box"}],"output":"missing"}`,
	}
	for _, input := range cases {
		if _, _, err := normalizeDefinition([]byte(input), 256); err == nil {
			t.Fatalf("expected rejection for %s", input)
		}
	}
}

func TestExamplesAreCanonical(t *testing.T) {
	examples := designExamples()["examples"].([]map[string]any)
	for _, example := range examples {
		body, _ := json.Marshal(example["definition"])
		if _, _, err := normalizeDefinition(body, 256); err != nil {
			t.Fatalf("example %q invalid: %v", example["name"], err)
		}
	}
}

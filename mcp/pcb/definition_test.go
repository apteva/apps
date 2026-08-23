package main

import (
	"encoding/json"
	"strings"
	"testing"
)

func testDefinition() Definition { return pcbExamples()["example"].(Definition) }
func testDefinitionJSON(t *testing.T) []byte {
	t.Helper()
	body, err := json.Marshal(testDefinition())
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func TestDefinitionCanonicalizationIsStable(t *testing.T) {
	a, def, hashA, err := normalizeDefinition(testDefinitionJSON(t), "")
	if err != nil {
		t.Fatal(err)
	}
	b, _, hashB, err := normalizeDefinition(a, "")
	if err != nil {
		t.Fatal(err)
	}
	if string(a) != string(b) || hashA != hashB {
		t.Fatalf("canonical source is not stable\n%s\n%s", a, b)
	}
	if def.Schema != pcbSchema || def.Board.WidthNM != 40_000_000 {
		t.Fatalf("unexpected normalized definition: %#v", def)
	}
}
func TestDefinitionDefaultsAndStrictFields(t *testing.T) {
	body := []byte(`{"schema":"apteva-pcb/v1","board":{"width_nm":10000000,"height_nm":8000000,"layers":[]},"rules":{"min_clearance_nm":0,"min_trace_width_nm":0,"min_edge_clearance_nm":0},"components":null,"nets":null,"traces":null,"vias":null}`)
	_, def, _, err := normalizeDefinition(body, "Blank")
	if err != nil {
		t.Fatal(err)
	}
	if len(def.Board.Layers) != 2 || def.Rules.MinClearanceNM != defaultClearance || def.Components == nil {
		t.Fatalf("defaults not applied: %#v", def)
	}
	bad := strings.Replace(string(body), `"schema":`, `"surprise":true,"schema":`, 1)
	if _, _, _, err := normalizeDefinition([]byte(bad), ""); err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

package main

import (
	"encoding/json"
	"testing"

	rules "github.com/apteva/apps/mcp/strategies/internal/ruleengine"
)

// targetDefinition is a strategy definition that holds every symbol at weight
// (a number or an expression map) on each closed bar.
func targetDefinition(symbols []string, timeframe string, weight any) map[string]any {
	if w, ok := weight.(float64); ok {
		weight = map[string]any{"value": w}
	}
	return map[string]any{"program": map[string]any{
		"version": rules.Version, "symbols": symbols, "timeframe": timeframe, "timezone": "UTC",
		"rules": []any{map[string]any{"id": "hold", "on": "bar.close", "repeat": "always", "actions": []any{
			map[string]any{"kind": "target", "weight": weight, "construct": map[string]any{"normalize": "none"}},
		}}},
	}}
}

func mustTargetDefinition(symbols []string, timeframe string, weight any) *StrategyDefinition {
	def, _, err := validateStrategyDefinition(targetDefinition(symbols, timeframe, weight))
	if err != nil {
		panic(err)
	}
	return def
}

// mustJSONMap round-trips v through JSON, as an MCP client would send it.
func mustJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

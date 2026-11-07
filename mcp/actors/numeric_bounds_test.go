package main

import (
	"testing"
)

func TestInclusiveNumericBounds(t *testing.T) {
	for _, tc := range []struct {
		value any
		valid bool
	}{
		{17, false}, {18, true}, {25, true}, {39, true}, {40, false},
		{"18", true}, {"39", true}, {nil, false}, {"unknown", false},
		{"NaN", false}, {"+Inf", false}, {"-Inf", false},
	} {
		e := &actorExecution{lastValues: map[string]any{"value": tc.value}}
		err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{
			"value": {GreaterThanOrEqual: "18", LessThanOrEqual: "39"},
		}})
		if (err == nil) != tc.valid {
			t.Fatalf("%v: %v", tc.value, err)
		}
	}
	for _, bound := range []any{"NaN", "+Inf", "unknown"} {
		e := &actorExecution{lastValues: map[string]any{"value": 25}}
		if e.assertValues(actorStep{Assertions: map[string]actorAssertion{"value": {GreaterThanOrEqual: bound}}}) == nil {
			t.Fatal("invalid bound accepted")
		}
	}
	def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "assert_values", Assertions: map[string]actorAssertion{"value": {GreaterThanOrEqual: "{{minimum}}", LessThanOrEqual: "{{maximum}}"}}}}}
	if err := validateActorDefinition(def); err != nil {
		t.Fatal(err)
	}
}

package main

import "testing"

func TestEvalConditionTruthy(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{"x": true, "y": false, "z": "foo"}}
	cases := []struct {
		expr string
		want bool
	}{
		{"input.x", true},
		{"input.y", false},
		{"input.z", true},
		{"input.missing", false},
	}
	for _, c := range cases {
		got, err := EvalCondition(c.expr, ctx)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalConditionComparisons(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{"n": float64(5), "name": "alice"}}
	cases := []struct {
		expr string
		want bool
	}{
		{"input.n == 5", true},
		{"input.n != 5", false},
		{"input.n > 4", true},
		{"input.n > 5", false},
		{"input.n < 6", true},
		{"input.n >= 5", true},
		{"input.n <= 5", true},
		{"input.name == 'alice'", true},
		{"input.name != 'bob'", true},
	}
	for _, c := range cases {
		got, err := EvalCondition(c.expr, ctx)
		if err != nil {
			t.Errorf("%s: %v", c.expr, err)
		}
		if got != c.want {
			t.Errorf("%s: got %v, want %v", c.expr, got, c.want)
		}
	}
}

func TestEvalConditionBadOp(t *testing.T) {
	_, err := EvalCondition("input.x === 5", TemplateContext{})
	if err == nil {
		t.Error("expected error for === operator")
	}
}

func TestEvalConditionTokensWithQuotedSpace(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{"k": "invoice paid"}}
	ok, err := EvalCondition("input.k == 'invoice paid'", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("expected true")
	}
}

func TestEvalConditionLogicalOperators(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{
		"appel_id":        nil,
		"appel_source_id": float64(42),
		"status":          "ready",
		"enabled":         true,
	}}
	cases := []struct {
		expr string
		want bool
	}{
		{"input.appel_id or input.appel_source_id", true},
		{"input.appel_id and input.enabled", false},
		{"input.status == 'ready' and input.enabled", true},
		{"input.status == 'pending' or input.appel_source_id", true},
		{"true or input.status > 2", true}, // short-circuit avoids the invalid numeric comparison
		{"false and input.status > 2", false},
		{"true or false and false", true}, // and binds more tightly than or
	}
	for _, tc := range cases {
		got, err := EvalCondition(tc.expr, ctx)
		if err != nil {
			t.Errorf("%q: %v", tc.expr, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%q: got %v, want %v", tc.expr, got, tc.want)
		}
	}
}

func TestValidateTriggerCondition(t *testing.T) {
	valid := []string{
		"input.data.table == 'ventes'",
		`input.data.status == "ready"`,
		"input.data.count >= 2",
		"input.data.enabled",
		"input.data.table == 'ventes' and input.data.row.status == 'ready'",
		"input.data.appel_id or input.data.appel_source_id",
		"true",
	}
	for _, expr := range valid {
		if err := ValidateTriggerCondition(expr); err != nil {
			t.Errorf("%q: unexpected validation error: %v", expr, err)
		}
	}

	invalid := []string{
		"ventes",
		"'always truthy'",
		"input.data.table = 'ventes'",
		"input.data.table === 'ventes'",
		"input.data.table == ventes",
		"input.data.table == 'unterminated",
		"input.data.table == 'ventes' && input.data.ready",
	}
	for _, expr := range invalid {
		if err := ValidateTriggerCondition(expr); err == nil {
			t.Errorf("%q: expected validation error", expr)
		}
		matched, err := EvalTriggerCondition(expr, TemplateContext{
			Input: map[string]any{"data": map[string]any{"table": "ventes"}},
		})
		if err == nil || matched {
			t.Errorf("%q: matched=%v err=%v, want fail closed", expr, matched, err)
		}
	}
}

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

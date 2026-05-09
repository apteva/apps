package main

import (
	"reflect"
	"testing"
)

func TestRenderExactMatchPreservesType(t *testing.T) {
	ctx := TemplateContext{
		Input: map[string]any{"obj": map[string]any{"k": "v"}, "num": float64(42)},
	}
	// Whole-string substitution returns the raw value.
	out, err := Render("{{ input.obj }}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(out, map[string]any{"k": "v"}) {
		t.Errorf("object substitution lost type: %#v", out)
	}
	out, _ = Render("{{ input.num }}", ctx)
	if out != float64(42) {
		t.Errorf("number substitution: %#v", out)
	}
}

func TestRenderInterpolationStringifies(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{"name": "world", "n": float64(7)}}
	out, _ := Render("hello {{ input.name }} #{{ input.n }}", ctx)
	if out != "hello world #7" {
		t.Errorf("got %q", out)
	}
}

func TestRenderRecursesIntoMapsAndSlices(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{"x": "ok"}}
	v, _ := Render(map[string]any{
		"a": "{{ input.x }}",
		"b": []any{"{{ input.x }}", "raw"},
	}, ctx)
	want := map[string]any{
		"a": "ok",
		"b": []any{"ok", "raw"},
	}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("got %#v", v)
	}
}

func TestRenderMissPathReturnsNil(t *testing.T) {
	ctx := TemplateContext{Input: map[string]any{}}
	out, err := Render("{{ input.missing }}", ctx)
	if err != nil {
		t.Fatal(err)
	}
	if out != nil {
		t.Errorf("miss should return nil, got %#v", out)
	}
}

func TestRenderStepsLookup(t *testing.T) {
	ctx := TemplateContext{
		Steps: map[string]any{
			"lookup": map[string]any{"contact": map[string]any{"id": float64(123)}},
		},
	}
	out, _ := Render("{{ steps.lookup.contact.id }}", ctx)
	if out != float64(123) {
		t.Errorf("nested lookup: %#v", out)
	}
}

// Critical safety property: an attacker-supplied input value can't
// inject template syntax into a downstream step. The renderer
// substitutes whole *values*, never re-evaluates the substituted
// content.
func TestRenderNoSecondaryEvaluation(t *testing.T) {
	ctx := TemplateContext{
		Input: map[string]any{"evil": "{{ env.SECRET }}"},
		Env:   map[string]string{"SECRET": "do-not-leak"},
	}
	out, _ := Render("{{ input.evil }}", ctx)
	if out != "{{ env.SECRET }}" {
		t.Errorf("templating got re-evaluated: %#v", out)
	}
}

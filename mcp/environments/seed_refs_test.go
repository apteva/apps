package main

import (
	"reflect"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// seedPlatform records the input each seed is called with and replays a canned
// result for it, so a test can assert what seed N+1 actually received.
type seedPlatform struct {
	*auditPlatform
	inputs  []map[string]any
	results []any
	callErr error
}

func (p *seedPlatform) CallRuntimeAppResult(_, _, _ string, input map[string]any, out any) error {
	p.inputs = append(p.inputs, input)
	if p.callErr != nil {
		return p.callErr
	}
	if len(p.inputs) <= len(p.results) {
		if ptr, ok := out.(*any); ok {
			*ptr = p.results[len(p.inputs)-1]
		}
	}
	return nil
}

func seedService(t *testing.T, results ...any) (*service, *seedPlatform) {
	t.Helper()
	db := testStore(t)
	p := &seedPlatform{auditPlatform: &auditPlatform{live: map[string]sdk.RuntimeSummary{}}, results: results}
	ctx := sdk.NewAppCtxForTest(nil, db.db, nil, p, nil).WithProject("seeds")
	return &service{db: db, ctx: ctx}, p
}

func TestStartResolvesSeedReferences(t *testing.T) {
	s, p := seedService(t,
		map[string]any{"contact": map[string]any{"id": float64(42), "name": "Ada"}},
		map[string]any{"id": float64(7)},
	)
	spec := EnvironmentSpec{Version: 1, Seeds: []SeedStep{
		{App: "crm", Tool: "contact_create", Input: map[string]any{"name": "Ada"}},
		{App: "crm", Tool: "deal_create", Input: map[string]any{"contact_id": map[string]any{"$ref": "0.contact.id"}}},
		{App: "crm", Tool: "activity_log", Input: map[string]any{
			"contact_id": map[string]any{"$ref": "0.contact.id"},
			"deal_id":    map[string]any{"$ref": "1.id"},
			"kind":       "note",
			"meta":       map[string]any{"tags": []any{"seeded", map[string]any{"$ref": "0.contact.name"}}},
		}},
	}}
	if _, err := s.start("", "interactive", spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	if len(p.inputs) != 3 {
		t.Fatalf("got %d seed calls, want 3", len(p.inputs))
	}
	if got := p.inputs[1]["contact_id"]; got != float64(42) {
		t.Fatalf("seed 1 contact_id = %#v, want 42", got)
	}
	want := map[string]any{
		"contact_id": float64(42),
		"deal_id":    float64(7),
		"kind":       "note",
		"meta":       map[string]any{"tags": []any{"seeded", "Ada"}},
	}
	if !reflect.DeepEqual(p.inputs[2], want) {
		t.Fatalf("seed 2 input = %#v, want %#v", p.inputs[2], want)
	}
}

func TestStartLeavesSeedInputUnchangedWhenResolving(t *testing.T) {
	s, p := seedService(t, map[string]any{"id": float64(3)})
	input := map[string]any{"contact_id": map[string]any{"$ref": "0.id"}}
	spec := EnvironmentSpec{Version: 1, Seeds: []SeedStep{
		{App: "crm", Tool: "contact_create", Input: map[string]any{"name": "Ada"}},
		{App: "crm", Tool: "activity_log", Input: input},
	}}
	if _, err := s.start("", "interactive", spec); err != nil {
		t.Fatalf("start: %v", err)
	}
	ref, ok := input["contact_id"].(map[string]any)
	if !ok || ref["$ref"] != "0.id" {
		t.Fatalf("spec input was mutated: %#v", input["contact_id"])
	}
	if got := p.inputs[1]["contact_id"]; got != float64(3) {
		t.Fatalf("resolved contact_id = %#v, want 3", got)
	}
}

// A path that misses can only be caught once the earlier seed has actually run,
// so it fails the run where the reference sits rather than at validation.
func TestStartFailsOnUnresolvableSeedReference(t *testing.T) {
	s, p := seedService(t, map[string]any{"id": float64(1)})
	spec := EnvironmentSpec{Version: 1, Seeds: []SeedStep{
		{App: "crm", Tool: "contact_create", Input: map[string]any{"name": "Ada"}},
		{App: "crm", Tool: "activity_log", Input: map[string]any{"contact_id": map[string]any{"$ref": "0.contact.id"}}},
	}}
	run, err := s.start("", "interactive", spec)
	if err == nil {
		t.Fatal("want error, got nil")
	}
	if !strings.HasPrefix(err.Error(), "seed 1 crm.activity_log:") {
		t.Fatalf("error = %q, want the failing seed named", err)
	}
	if !strings.Contains(err.Error(), `seed 0 result has no "contact.id"`) {
		t.Fatalf("error = %q, want the unresolved path reported", err)
	}
	if len(p.inputs) != 1 {
		t.Fatalf("got %d seed calls, want the run to stop before seed 1", len(p.inputs))
	}
	if p.destroyed != 1 {
		t.Fatalf("runtime destroyed %d times, want 1", p.destroyed)
	}
	if run == nil || run.Status != "failed" {
		t.Fatalf("run = %#v, want status failed", run)
	}
}

func TestResolveSeedRefs(t *testing.T) {
	results := []any{
		map[string]any{"contact": map[string]any{"id": float64(42), "note": nil}},
		[]any{"not an object"},
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  map[string]any
	}{
		{"no refs", map[string]any{"a": "b", "n": float64(1)}, map[string]any{"a": "b", "n": float64(1)}},
		{"whole result", map[string]any{"r": map[string]any{"$ref": "0"}}, map[string]any{"r": results[0]}},
		{"null value resolves", map[string]any{"n": map[string]any{"$ref": "0.contact.note"}}, map[string]any{"n": nil}},
		{"trimmed ref", map[string]any{"id": map[string]any{"$ref": " 0.contact.id "}}, map[string]any{"id": float64(42)}},
		{"nested in list", map[string]any{"l": []any{map[string]any{"$ref": "0.contact.id"}}}, map[string]any{"l": []any{float64(42)}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveSeedRefs(tc.input, results)
			if err != nil {
				t.Fatalf("resolveSeedRefs: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %#v, want %#v", got, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		input map[string]any
		want  string
	}{
		{"extra keys", map[string]any{"x": map[string]any{"$ref": "0.contact.id", "else": 1}}, "must not carry other keys"},
		{"non-string ref", map[string]any{"x": map[string]any{"$ref": float64(0)}}, "must be a string"},
		{"negative index", map[string]any{"x": map[string]any{"$ref": "-1.id"}}, "must not be negative"},
		{"unknown index", map[string]any{"x": map[string]any{"$ref": "5.id"}}, "seed 5 has not run yet"},
		{"not an index", map[string]any{"x": map[string]any{"$ref": "contact.id"}}, "must start with a seed index"},
		{"path through a list", map[string]any{"x": map[string]any{"$ref": "1.0"}}, `seed 1 result has no "0"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := resolveSeedRefs(tc.input, results); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestValidateSpecRejectsForwardSeedReference(t *testing.T) {
	spec := EnvironmentSpec{Version: 1, Seeds: []SeedStep{
		{App: "crm", Tool: "contact_create", Input: map[string]any{"deal_id": map[string]any{"$ref": "1.id"}}},
		{App: "crm", Tool: "deal_create"},
	}}
	err := validateSpec(spec)
	if err == nil || !strings.Contains(err.Error(), "may only reference earlier seeds") {
		t.Fatalf("error = %v, want a forward-reference rejection", err)
	}
	// A seed referencing itself is the same mistake.
	spec.Seeds[0].Input = map[string]any{"deal_id": map[string]any{"$ref": "0.id"}}
	if err := validateSpec(spec); err == nil || !strings.Contains(err.Error(), "may only reference earlier seeds") {
		t.Fatalf("error = %v, want a self-reference rejection", err)
	}
	// A malformed reference never resolves, so it fails validation too.
	spec.Seeds[0].Input = map[string]any{"deal_id": map[string]any{"$ref": "contact.id"}}
	if err := validateSpec(spec); err == nil || !strings.Contains(err.Error(), "must start with a seed index") {
		t.Fatalf("error = %v, want a malformed-reference rejection", err)
	}
	spec.Seeds[0].Input = map[string]any{"name": "Ada"}
	spec.Seeds[1].Input = map[string]any{"contact_id": map[string]any{"$ref": "0.id"}}
	if err := validateSpec(spec); err != nil {
		t.Fatalf("backward reference rejected: %v", err)
	}
}

func TestJSONPathLookupSeparatesMissFromNull(t *testing.T) {
	v := map[string]any{"a": map[string]any{"b": nil}}
	if got, ok := jsonPathLookup(v, "a.b"); got != nil || !ok {
		t.Fatalf("a.b = %#v, %v; want nil, true", got, ok)
	}
	if got, ok := jsonPathLookup(v, "a.c"); got != nil || ok {
		t.Fatalf("a.c = %#v, %v; want nil, false", got, ok)
	}
	if got, ok := jsonPathLookup(v, ""); !reflect.DeepEqual(got, v) || !ok {
		t.Fatalf("empty path = %#v, %v; want the whole value, true", got, ok)
	}
	if got := jsonPath(v, "a.c"); got != nil {
		t.Fatalf("jsonPath kept its nil-on-miss behaviour? got %#v", got)
	}
}

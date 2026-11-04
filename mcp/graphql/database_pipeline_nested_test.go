package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const (
	nestedOn      = `"on":[{"localField":"local_id","foreignField":"call_id"}]`
	nestedAllowed = `{"exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"in","value":"$scope.allowed"}}}`
	nestedExcept  = `{"not_exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"not_in","value":"$scope.allowed"}}}`
)

// viaCalls wraps a filter on sales in one more relationship, from reports to calls.
func viaCalls(where string) string {
	return `{"exists":{"collection":"calls","on":[{"localField":"call_ref","foreignField":"local_id"}],"where":` + where + `}}`
}

// nestedChain nests exists clauses, each a level deeper, around a leaf.
func nestedChain(levels int, leaf string) string {
	out := leaf
	for i := levels; i >= 1; i-- {
		out = fmt.Sprintf(`{"exists":{"collection":"level%d","on":[{"localField":"id","foreignField":"parent"}],"where":%s}}`, i, out)
	}
	return out
}

func TestNestedRelationsValidation(t *testing.T) {
	const leaf = `{"field":"offer","op":"in","value":"$scope.allowed"}`
	const trusted = `{"op":"gt","args":[{"op":"length","args":["$scope.allowed"]},{"const":0}]}`
	for name, where := range map[string]string{
		"exists of exists":          viaCalls(nestedAllowed),
		"exists of not_exists":      viaCalls(nestedExcept),
		"not_exists of exists":      `{"not_exists":{"collection":"calls","on":[{"localField":"call_ref","foreignField":"local_id"}],"where":` + nestedAllowed + `}}`,
		"both in a group":           viaCalls(`{"and":[` + nestedAllowed + `,` + nestedExcept + `]}`),
		"in an or":                  viaCalls(`{"or":[` + nestedAllowed + `,` + nestedExcept + `]}`),
		"four levels":               nestedChain(4, leaf),
		"beside own filters":        `{"and":[` + authorisedFilter + `,` + viaCalls(nestedAllowed) + `]}`,
		"trusted selection inside":  viaCalls(`{"and":[{"access_when":` + trusted + `,"filter":` + nestedAllowed + `}]}`),
		"trusted selection deeper":  viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"and":[` + leaf + `,{"access_when":` + trusted + `,"filter":{"field":"sale_centre","op":"in","value":"$scope.allowed"}}]}}}`),
		"selection around a nest":   `{"and":[{"access_when":` + trusted + `,"filter":` + viaCalls(nestedAllowed) + `}]}`,
		"client value under exists": viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"eq","value":"$param.status"}}}`),
		"eight across branches":     `{"and":[` + strings.Repeat(viaCalls(nestedAllowed)+`,`, 3) + viaCalls(nestedExcept) + `]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDatabasePipeline(conditionalPlan(t, where)); err != nil {
				t.Fatal(err)
			}
		})
	}

	nine := `{"and":[` + strings.Repeat(viaCalls(nestedAllowed)+`,`, 4) + nestedAllowed + `]}`
	for name, c := range map[string]struct{ where, want string }{
		"five levels":                 {nestedChain(5, leaf), "at most 4"},
		"five levels in a group":      {`{"and":[` + authorisedFilter + `,` + nestedChain(5, leaf) + `]}`, "at most 4"},
		"nine across the tree":        {nine, "at most 8"},
		"client when nested":          {viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"and":[` + selectedCentres + `]}}}`), "client when"},
		"search nested":               {viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"fields":["a"],"op":"like_contains_ascii","value":"$param.status"}}}`), "like_contains_ascii"},
		"client value in not_exists":  {viaCalls(`{"not_exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"eq","value":"$param.status"}}}`), "client input"},
		"client value under negation": {`{"not_exists":{"collection":"calls","on":[{"localField":"call_ref","foreignField":"local_id"}],"where":{"exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"eq","value":"$param.status"}}}}}`, "client input"},
		"unknown key nested":          {viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"limit":1}}`), "limit"},
		"placeholder collection":      {viaCalls(`{"exists":{"collection":"$param.status",` + nestedOn + `}}`), "literal collection"},
		"placeholder field":           {viaCalls(`{"exists":{"collection":"sales","on":[{"localField":"$param.status","foreignField":"b"}]}}`), "literal localField"},
		"bad operator nested":         {viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"a","op":"contains","value":"x"}}}`), "unsupported op"},
		"inside a client when":        {`{"and":[{"when":` + selectedWhen + `,"filter":` + viaCalls(nestedAllowed) + `}]}`, "relationships are policy"},
		"selection under or, nested":  {viaCalls(`{"or":[` + nestedAllowed + `,{"exists":{"collection":"sales",` + nestedOn + `,"where":{"and":[{"access_when":` + trusted + `,"filter":` + leaf + `}]}}}]}`), "inside or"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateDatabasePipeline(conditionalPlan(t, c.where))
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}

	// Relationships stay out of the places that were never allowed to hold them.
	plan := conditionalPlan(t, authorisedFilter)
	plan["access"].(map[string]any)["values"].(map[string]any)["allowed"].(map[string]any)["otherwise"].(map[string]any)["where"] = decodeConfig(t, viaCalls(nestedAllowed))
	if _, err := validateDatabasePipeline(plan); err == nil || !strings.Contains(err.Error(), "cannot use exists") {
		t.Errorf("access read: %v", err)
	}
}

func TestNestedRelationsBindAtEveryLevel(t *testing.T) {
	read := func(t *testing.T, where string, args map[string]any, allowed ...any) (string, *dbPipelinePlatform, any, error) {
		t.Helper()
		a, p := pipelineApp(t, conditionalResponder(allowed...))
		plan := conditionalPlan(t, where)
		if args != nil {
			plan["graphql_args"] = args
		}
		value, err := a.callDatabasePipeline(pipelineIdentity("42"), plan)
		if len(p.batches) < 2 {
			return "", p, value, err
		}
		ops := p.batches[1]["operations"].([]any)
		find := ops[0].(map[string]any)["args"].(map[string]any)["where"]
		count := ops[1].(map[string]any)["args"].(map[string]any)["where"]
		encoded, _ := json.Marshal(find)
		if counted, _ := json.Marshal(count); string(counted) != string(encoded) {
			t.Errorf("the total must count the page's filter:\n%s\n%s", encoded, counted)
		}
		return string(encoded), p, value, err
	}

	t.Run("values reach the deepest level", func(t *testing.T) {
		got, _, _, err := read(t, nestedChain(3, `{"field":"offer","op":"in","value":"$scope.allowed"}`), nil, "offer-a", "offer-b")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `"value":["offer-a","offer-b"]`) || strings.Contains(got, "$scope") {
			t.Errorf("unbound: %s", got)
		}
		if strings.Count(got, `"exists"`) != 3 {
			t.Errorf("structure changed: %s", got)
		}
	})
	t.Run("long lists are chunked at depth", func(t *testing.T) {
		long := []any{}
		for i := 0; i < 250; i++ {
			long = append(long, fmt.Sprint("offer-", i))
		}
		got, _, _, err := read(t, viaCalls(nestedAllowed), nil, long...)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(got, `"or":[{"field":"offer","op":"in"`) || strings.Count(got, `"op":"in"`) != 3 {
			t.Errorf("chunks: %s", got)
		}
	})
	t.Run("nothing can match through a chain of exists", func(t *testing.T) {
		_, p, value, err := read(t, viaCalls(nestedAllowed), nil)
		if err != nil {
			t.Fatal(err)
		}
		if len(p.batches) != 1 {
			t.Errorf("an empty scope needs no read: %v", p.batches)
		}
		if total := fmt.Sprint(value.(map[string]any)["total"]); total != "0" {
			t.Errorf("total: %s", total)
		}
	})
	t.Run("a negated chain is not answered empty", func(t *testing.T) {
		// No related record can match the inner clause, so not_exists holds for
		// every record: answering "nothing" here would hide rows.
		for name, where := range map[string]string{
			"not_exists of exists": `{"not_exists":{"collection":"calls","on":[{"localField":"call_ref","foreignField":"local_id"}],"where":` + nestedAllowed + `}}`,
			"exists of not_exists": viaCalls(`{"not_exists":{"collection":"sales",` + nestedOn + `,"where":{"field":"offer","op":"in","value":"$scope.allowed"}}}`),
		} {
			if got, p, _, err := read(t, where, nil); err != nil || len(p.batches) != 2 || !strings.Contains(got, `"is_not_null"`) {
				t.Errorf("%s: %s, %d batches, %v", name, got, len(p.batches), err)
			}
		}
	})
	t.Run("trusted selection resolves at depth", func(t *testing.T) {
		deep := func(condition string) string {
			return viaCalls(`{"exists":{"collection":"sales",` + nestedOn + `,"where":{"and":[{"access_when":` + condition + `,"filter":{"field":"offer","op":"in","value":"$scope.allowed"}}]}}}`)
		}
		listed := `{"op":"gt","args":[{"op":"length","args":["$scope.allowed"]},{"const":0}]}`
		got, _, _, err := read(t, deep(listed), nil, "offer-a")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "access_when") || !strings.Contains(got, `"value":["offer-a"]`) {
			t.Errorf("selected: %s", got)
		}
		got, _, _, err = read(t, deep(`{"op":"eq","args":[{"op":"length","args":["$scope.allowed"]},{"const":0}]}`), nil, "offer-a")
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got, "access_when") || strings.Contains(got, "offer") || strings.Count(got, `"exists"`) != 2 {
			t.Errorf("not selected: %s", got)
		}
	})
}

func TestNestedRelationsPublicationChecks(t *testing.T) {
	const where = `{"and":[` + authorisedFilter + `,{"exists":{"collection":"calls","on":[{"localField":"call_ref","foreignField":"local_id"}],"where":{"and":[
	  {"exists":{"collection":"sales",` + nestedOn + `,"where":{"and":[{"field":"offer","op":"in","value":"$scope.allowed"},
	    {"access_when":{"const":true},"filter":{"field":"sale_centre","op":"in","value":"$scope.allowed"}}]}}},
	  {"not_exists":{"collection":"refunds",` + nestedOn + `}}]}}},
	  {"access_when":{"const":true},"filter":{"field":"own_field","op":"in","value":"$scope.allowed"}}]}`
	plan, err := validateDatabasePipeline(conditionalPlan(t, where))
	if err != nil {
		t.Fatal(err)
	}
	step := plan.Steps[0]
	if got := len(step.relations()); got != 3 {
		t.Errorf("every relationship: %d", got)
	}
	if got := len(step.ownRelations()); got != 1 {
		t.Errorf("relationships on the step's own collection: %d", got)
	}
	if got := relationDepth(step.Args["where"]); got != 2 {
		t.Errorf("depth: %d", got)
	}
	// Subscriptions must re-run when any level changes.
	collections := fmt.Sprint(plan.Collections())
	for _, name := range []string{"calls", "sales", "refunds"} {
		if !strings.Contains(collections, name) {
			t.Errorf("%s isn't watched: %s", name, collections)
		}
	}
	// A related field doesn't belong to the step's collection, so it can't be
	// probed there; it is planned with its relationship.
	if got := fmt.Sprint(step.conditionalFields()); got != "[own_field]" {
		t.Errorf("conditional fields: %s", got)
	}
	// One plan per top-level relationship, on the step's collection, keeping the
	// nested structure and no values.
	recorder := &explainRecorder{}
	a := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(recorder))}
	run := &databasePipelineRun{app: a, plan: plan, project: "p1"}
	if err := run.verifyRelations(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The page and its total are each planned; neither plans a nested clause alone,
	// where it would read its parent's fields from the wrong collection.
	if len(recorder.inputs) != 2 {
		t.Fatalf("plans: %v", recorder.inputs)
	}
	for _, input := range recorder.inputs {
		if input["collection"] != "leads" {
			t.Errorf("collection: %v", input["collection"])
		}
		top, _ := input["where"].(map[string]any)["exists"].(map[string]any)
		if top["collection"] != "calls" {
			t.Errorf("planned alone: %v", input["where"])
		}
		encoded, _ := json.Marshal(input["where"])
		got := string(encoded)
		for _, part := range []string{`"collection":"sales"`, `"collection":"refunds"`, `"not_exists"`, `"field":"sale_centre","op":"is_null"`} {
			if !strings.Contains(got, part) {
				t.Errorf("probe lost %s: %s", part, got)
			}
		}
		if strings.Contains(got, "$scope") || strings.Contains(got, "access_when") || strings.Contains(got, `"value"`) {
			t.Errorf("probe holds values: %s", got)
		}
	}
}

type explainRecorder struct {
	dbPipelinePlatform
	inputs []map[string]any
}

func (p *explainRecorder) CallAppResultContext(_ context.Context, _, tool string, input map[string]any, _ any) error {
	if tool != "db_explain" {
		return fmt.Errorf("unexpected tool %s", tool)
	}
	p.inputs = append(p.inputs, input)
	return nil
}

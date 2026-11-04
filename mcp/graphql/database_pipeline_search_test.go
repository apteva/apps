package main

import (
	"fmt"
	"strings"
	"testing"
)

func searchPlan(params, where string) string {
	return `{"params":{` + params + `},"steps":[{"id":"s","op":"find","args":{"collection":"c","where":` + where + `}}],"result":"$steps.s"}`
}

func TestDatabasePipelineSearchValidation(t *testing.T) {
	q := `"q":{"from":"$args.q"}`
	search := `{"fields":["a","b"],"op":"like_contains_ascii","value":"$param.q"}`
	scoped := `"user":{"from":"$identity.subject"},` + q
	for name, text := range map[string]string{
		"no fields":        searchPlan(q, `{"op":"like_contains_ascii","value":"$param.q"}`),
		"single field key": searchPlan(q, `{"field":"a","op":"like_contains_ascii","value":"$param.q"}`),
		"field and fields": searchPlan(q, `{"field":"a","fields":["a"],"op":"like_contains_ascii","value":"$param.q"}`),
		"other op":         searchPlan(q, `{"fields":["a"],"op":"eq","value":"$param.q"}`),
		"empty fields":     searchPlan(q, `{"fields":[],"op":"like_contains_ascii","value":"$param.q"}`),
		"17 fields":        searchPlan(q, `{"fields":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p","q"],"op":"like_contains_ascii","value":"$param.q"}`),
		"client field":     searchPlan(q, `{"fields":["$param.q"],"op":"like_contains_ascii","value":"$param.q"}`),
		"non-string field": searchPlan(q, `{"fields":[1],"op":"like_contains_ascii","value":"$param.q"}`),
		"unknown key":      searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii","value":"$param.q","mode":"x"}`),
		"missing value":    searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii"}`),
		"number value":     searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii","value":1}`),
		"unknown param":    searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii","value":"$param.nope"}`),
		"non-string param": searchPlan(`"q":{"from":"$args.q","type":"string_list"}`, search),
		"identity value":   searchPlan(`"q":{"from":"$identity.subject"}`, search),
		"parent value":     searchPlan(`"q":{"from":"$parent.q"}`, search),
		"nested argument":  searchPlan(`"q":{"from":"$args.filter.q"}`, search),
		"fixed non-string": searchPlan(`"q":{"value":1,"type":"string"}`, search),
		"long literal":     searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii","value":"`+strings.Repeat("x", 1025)+`"}`),
		"scope value":      `{"params":{` + q + `},"access":{"values":{"v":{"otherwise":{"collection":"r","field":"id"}}}},"steps":[{"id":"s","op":"find","args":{"collection":"c","where":{"fields":["a"],"op":"like_contains_ascii","value":"$scope.v"}}}],"result":"$steps.s"}`,
		// An empty search matches everything, so an or would cancel its siblings.
		"inside or":        searchPlan(scoped, `{"or":[{"field":"u","op":"eq","value":"$param.user"},`+search+`]}`),
		"nested inside or": searchPlan(scoped, `{"or":[{"field":"u","op":"eq","value":"$param.user"},{"and":[{"field":"x","op":"eq","value":"y"},`+search+`]}]}`),
		"access read":      `{"params":{` + q + `},"access":{"values":{"v":{"otherwise":{"collection":"r","field":"id","where":` + search + `}}}},"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":"$steps.s"}`,
		"unknown field op": searchPlan(q, `{"field":"a","op":"contains","value":"$param.q"}`),
		"fuzzy op typo":    searchPlan(q, `{"field":"a","op":"neq","value":"$param.q"}`),
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	for name, text := range map[string]string{
		"client argument": searchPlan(q, search),
		"fixed value":     searchPlan(`"q":{"value":"x"}`, search),
		"literal":         searchPlan(q, `{"fields":["a"],"op":"like_contains_ascii","value":"x"}`),
		"16 fields":       searchPlan(q, `{"fields":["a","b","c","d","e","f","g","h","i","j","k","l","m","n","o","p"],"op":"like_contains_ascii","value":"$param.q"}`),
		"and with scope":  searchPlan(scoped, `{"and":[{"field":"u","op":"eq","value":"$param.user"},{"or":[{"field":"x","op":"eq","value":"y"},{"field":"x","op":"is_null"}]},`+search+`]}`),
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	metric := `{"params":{` + q + `},"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"count","where":` + search + `}]}}],"result":"$steps.s"}`
	plan, err := validateDatabasePipeline(decodeConfig(t, metric))
	if err != nil || fmt.Sprint(plan.searchArguments()) != "[q]" {
		t.Fatalf("metric where search: %v %v", plan, err)
	}
}

// The search reaches Database unchanged, ANDed with the resolved scope, and the
// total uses the same filter in the same batch as the page.
func TestDatabasePipelineSearchBinding(t *testing.T) {
	config := decodeConfig(t, `{"params":{"user":{"from":"$identity.subject"},"q":{"from":"$args.q"}},
	  "access":{"values":{"centres":{"optional":true,"otherwise":{"collection":"profiles","field":"centre","where":{"field":"user","op":"eq","value":"$param.user"}}}}},
	  "steps":[
	    {"id":"rows","op":"find","page":{"size":10},"args":{"collection":"c","where":{"and":[{"field":"centre","op":"in","value":"$scope.centres"},
	      {"fields":["a","b"],"op":"like_contains_ascii","value":"$param.q"}]},"orderBy":[{"field":"id"}]}},
	    {"id":"total","op":"count","count_of":"rows"}],
	  "result":{"rows":"$steps.rows","total":"$steps.total"}}`)
	config["_project_id"], config["graphql_args"] = "p1", map[string]any{"q": "  23 op_%"}
	respond := func(centres []any) func([]any) (any, error) {
		return func(operations []any) (any, error) {
			out := []any{}
			for _, raw := range operations {
				switch raw.(map[string]any)["args"].(map[string]any)["collection"] {
				case "profiles":
					records := []any{}
					for _, centre := range centres {
						records = append(records, map[string]any{"centre": centre})
					}
					out = append(out, map[string]any{"records": records, "hasMore": false})
				default:
					if raw.(map[string]any)["op"] == "count" {
						out = append(out, map[string]any{"count": "1"})
					} else {
						out = append(out, map[string]any{"records": []any{map[string]any{"id": "r1"}}, "hasMore": false})
					}
				}
			}
			return map[string]any{"results": out}, nil
		}
	}
	a, p := pipelineApp(t, respond([]any{"centre-a"}))
	if _, err := a.callDatabasePipeline(pipelineIdentity("u1"), config); err != nil {
		t.Fatal(err)
	}
	steps := p.batches[1]["operations"].([]any)
	if len(p.batches) != 2 || len(steps) != 2 {
		t.Fatalf("page and total must share one batch: %d batches, %d operations", len(p.batches), len(steps))
	}
	page := fmt.Sprint(steps[0].(map[string]any)["args"].(map[string]any)["where"])
	total := fmt.Sprint(steps[1].(map[string]any)["args"].(map[string]any)["where"])
	if page != total {
		t.Fatalf("page and total filters differ:\n%s\n%s", page, total)
	}
	for _, want := range []string{"fields:[a b]", "op:like_contains_ascii", "value:  23 op_%", "field:centre", "centre-a"} {
		if !strings.Contains(page, want) {
			t.Fatalf("filter lost %q: %s", want, page)
		}
	}
	// No authorised scope: the search cannot stand in for it, nothing is read.
	a, p = pipelineApp(t, respond(nil))
	out, err := a.callDatabasePipeline(pipelineIdentity("u1"), config)
	if err != nil || len(p.batches) != 1 {
		t.Fatalf("empty scope with a search: %v %v, %d batches", out, err, len(p.batches))
	}
}

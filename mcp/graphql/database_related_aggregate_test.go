package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func relatedAggregateArgs(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/related-aggregate.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeConfig(t, string(raw))
}
func relatedAggregatePlan(t *testing.T) map[string]any {
	args := relatedAggregateArgs(t)
	args["where"] = map[string]any{"field": "centre", "op": "in", "value": "$scope.centres"}
	args["match"] = decodeConfig(t, `{"and":[{"when":{"op":"neq","args":["$param.q",{"const":""}]},"filter":{"field":"c.text","op":"contains_ascii","value":"$param.q"}}]}`)
	page := copyObject(args)
	delete(page, "computed")
	delete(page, "metrics")
	page["select"] = []any{"id"}
	page["count"] = true
	return map[string]any{"database": "crm", "params": map[string]any{"q": map[string]any{"from": "$args.q", "type": "string"}, "user": map[string]any{"from": "$identity.subject", "type": "string", "required": true}},
		"access": map[string]any{"require_any_permission": []any{"quality:read"}, "values": map[string]any{"centres": map[string]any{"optional": true, "otherwise": map[string]any{"collection": "members", "field": "centre", "where": map[string]any{"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
		"steps":  []any{map[string]any{"id": "page", "op": "find", "args": page, "page": map[string]any{"size": 1}}, map[string]any{"id": "summary", "op": "aggregate", "args": args}},
		"result": map[string]any{"rows": "$steps.page", "total": "$steps.page.total", "summary": "$steps.summary.first"}}
}
func TestRelatedAggregatePolicyAndDependencies(t *testing.T) {
	config := relatedAggregatePlan(t)
	before, _ := json.Marshal(config)
	plan, err := validateDatabasePipeline(config)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Steps[1].databaseOperation() != "aggregate" {
		t.Fatal(plan.Steps[1])
	}
	sameJSON(t, "every data and permission collection", plan.Collections(), `["contexts","members","parents"]`)
	sameJSON(t, "permission rechecks", plan.AccessCollections(), `["members"]`)
	probe, err := plan.Steps[1].joinedProbe()
	if err != nil {
		t.Fatal(err)
	}
	if probe["op"] != "aggregate" {
		t.Fatal(probe)
	}
	if !filterMatchesNothing(probe["args"].(map[string]any)["where"]) {
		t.Fatal("publication probe reads parents")
	}
	for _, change := range []func(map[string]any){
		func(a map[string]any) { a["relation"].(map[string]any)["limit"] = 0 },
		func(a map[string]any) { a["relation"].(map[string]any)["limit"] = 1001 },
		func(a map[string]any) { a["relation"].(map[string]any)["limit"] = "$param.q" },
		func(a map[string]any) { a["relation"].(map[string]any)["sources"] = []any{} },
		func(a map[string]any) {
			a["relation"].(map[string]any)["sources"].([]any)[0].(map[string]any)["filter"] = map[string]any{"field": "c.text", "op": "eq", "value": "$param.q"}
		},
		func(a map[string]any) {
			a["where"] = map[string]any{"or": []any{a["where"], map[string]any{"field": "centre", "op": "eq", "value": "$param.q"}}}
		},
	} {
		changed := decodeConfig(t, string(before))
		a := changed["steps"].([]any)[1].(map[string]any)["args"].(map[string]any)
		change(a)
		if _, err := validateDatabasePipeline(changed); err == nil {
			t.Fatal("accepted invalid related aggregate", a)
		}
	}
	after, _ := json.Marshal(config)
	if string(before) != string(after) {
		t.Fatal("mutated config")
	}
}
func TestRealDatabaseRelatedAggregate(t *testing.T) {
	r := newRealDatabase(t, "graphql-related-aggregate")
	r.collection("parents", "id:integer", "centre:text", "points:number", "maximum:number")
	r.collection("contexts", "id:integer", "parent:integer", "sequence:integer", "allowed:boolean", "text:text")
	r.collection("members", "id:text", "user_id:text", "centre:text")
	for _, idx := range [][2]string{{"contexts", "parent"}, {"parents", "centre"}, {"members", "user_id"}} {
		r.operator("db_index_create", map[string]any{"collection": idx[0], "index": map[string]any{"name": "by_" + idx[1], "fields": []any{map[string]any{"field": idx[1]}}}})
	}
	r.insert("parents", map[string]any{"id": "1", "centre": "allowed", "points": 50, "maximum": 100}, map[string]any{"id": "2", "centre": "allowed", "points": 10, "maximum": 20}, map[string]any{"id": "3", "centre": "foreign", "points": 90, "maximum": 100}, map[string]any{"id": "4", "centre": "allowed", "points": nil, "maximum": 100})
	r.insert("contexts", map[string]any{"id": "1", "parent": "1", "sequence": "0", "allowed": false, "text": "blocked"}, map[string]any{"id": "2", "parent": "1", "sequence": "1", "allowed": true, "text": "match 100% A_B"}, map[string]any{"id": "3", "parent": "1", "sequence": "2", "allowed": true, "text": "match"}, map[string]any{"id": "4", "parent": "1", "sequence": "3", "allowed": true, "text": "outside cap"}, map[string]any{"id": "5", "parent": "2", "sequence": "1", "allowed": true, "text": "match"}, map[string]any{"id": "6", "parent": "3", "sequence": "1", "allowed": true, "text": "match"}, map[string]any{"id": "7", "parent": "4", "sequence": "1", "allowed": true, "text": "unscored"})
	r.insert("members", map[string]any{"id": "a", "user_id": "7", "centre": "allowed"}, map[string]any{"id": "b", "user_id": "8", "centre": "foreign"})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	for _, id := range []string{"7", "8", "9"} {
		r.permissions[id] = []string{"quality:read"}
	}
	r.permissions["10"] = []string{}
	config := relatedAggregatePlan(t)
	fail := r.publish(`scalar JSON
 type Query { quality(q:String=""):JSON }`, map[string]map[string]any{"Query.quality": {"operation": databasePipelineOperation, "config": config}})
	if os.Getenv("GRAPHQL_TEST_RELATED_AGGREGATE") == "refused" {
		if fail == "" || !strings.Contains(fail, "Database") {
			t.Fatal("old Database must refuse publication", fail)
		}
		return
	}
	if fail != "" {
		t.Fatal(fail)
	}
	for _, test := range []struct {
		q        string
		n        int
		sum, avg any
	}{{"match", 2, float64(60), float64(50)}, {"outside cap", 0, nil, nil}, {"blocked", 0, nil, nil}, {"unscored", 1, nil, nil}, {"", 3, float64(60), float64(50)}, {"%", 1, float64(50), float64(50)}, {"_", 1, float64(50), float64(50)}, {"A1B", 0, nil, nil}} {
		out := r.queryAs("7", `query($q:String){quality(q:$q)}`, map[string]any{"q": test.q})
		if len(out.Errors) > 0 {
			t.Fatal(test.q, out.Errors)
		}
		value := out.Data["quality"].(map[string]any)
		summary := value["summary"].(map[string]any)
		if value["total"] != float64(test.n) || summary["parents"] != float64(test.n) || summary["points"] != test.sum || summary["average"] != test.avg {
			t.Fatal(test.q, value)
		}
	}
	foreign := r.queryAs("8", `{quality(q:"match")}`, nil)
	if len(foreign.Errors) > 0 {
		t.Fatal(foreign.Errors)
	}
	summary := foreign.Data["quality"].(map[string]any)["summary"].(map[string]any)
	if summary["parents"] != float64(1) || summary["points"] != float64(90) {
		t.Fatal(foreign)
	}
	empty := r.queryAs("9", `{quality(q:"match")}`, nil)
	if len(empty.Errors) > 0 {
		t.Fatal(empty.Errors)
	}
	if empty.Data["quality"].(map[string]any)["total"] != float64(0) {
		t.Fatal(empty)
	}
	if out := r.queryAs("10", `{quality(q:"match")}`, nil); out.code() != "permission_denied" {
		t.Fatal(out)
	}
}

func TestRelatedAggregatePermissionRecheck(t *testing.T) {
	config := relatedAggregatePlan(t)
	config["_project_id"] = "p"
	config["graphql_args"] = map[string]any{"q": "match"}
	fullBatches := 0
	a, _ := pipelineApp(t, func(ops []any) (any, error) {
		if len(ops) > 1 {
			fullBatches++
			hasPage, hasSummary := false, false
			for _, raw := range ops {
				op := raw.(map[string]any)
				args := op["args"].(map[string]any)
				if args["relation"] != nil {
					hasPage = hasPage || op["op"] == "find"
					hasSummary = hasSummary || op["op"] == "aggregate"
				}
			}
			if !hasPage || !hasSummary {
				t.Fatal("page and summary split", ops)
			}
		}
		results := []any{}
		for _, raw := range ops {
			op := raw.(map[string]any)
			args := op["args"].(map[string]any)
			switch {
			case args["collection"] == "members":
				records := []any{}
				if len(ops) == 1 {
					records = append(records, map[string]any{"centre": "allowed"})
				}
				results = append(results, map[string]any{"records": records, "hasMore": false})
			case op["op"] == "find":
				results = append(results, map[string]any{"records": []any{map[string]any{"id": "1"}}, "total": "1", "hasMore": false})
			default:
				results = append(results, map[string]any{"rows": []any{map[string]any{"parents": "99", "points": 12345, "average": 100}}, "truncated": false})
			}
		}
		return map[string]any{"results": results}, nil
	})
	value, err := a.callDatabasePipeline(pipelineIdentity("7", "quality:read"), config)
	if value != nil || pipelineErrorCode(err) != "access_changing" || fullBatches == 0 {
		t.Fatalf("grant change delivered a result: value=%v error=%v batches=%d", value, err, fullBatches)
	}
}

package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func dependentArgs(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/dependent-aggregate.json")
	if err != nil {
		t.Fatal(err)
	}
	return decodeConfig(t, string(raw))
}
func dependentConfig(t *testing.T, op string) map[string]any {
	args := dependentArgs(t)
	if op == "find" {
		args = map[string]any{"pipeline": args["pipeline"], "count": true, "select": []any{"centre", "period", "callers", "rate_cents"}, "orderBy": []any{map[string]any{"field": "period", "direction": "desc"}}}
		args["pipeline"].(map[string]any)["from"] = "rates"
	}
	return map[string]any{"database": "crm", "steps": []any{map[string]any{"id": "report", "op": op, "args": args}}, "result": "$steps.report"}
}
func TestCanonicalDependentRead(t *testing.T) {
	config := dependentConfig(t, "aggregate")
	before, _ := json.Marshal(config)
	a, p := pipelineApp(t, func(ops []any) (any, error) {
		if len(ops) != 1 {
			t.Fatalf("dependent calculation split into %d operations", len(ops))
		}
		call := ops[0].(map[string]any)
		if call["op"] != "aggregate" {
			t.Fatal(call)
		}
		nodes := call["args"].(map[string]any)["pipeline"].(map[string]any)["nodes"].([]any)
		if len(nodes) != 6 || nodes[0].(map[string]any)["where"].(map[string]any)["value"] != "a" {
			t.Fatal(nodes)
		}
		return map[string]any{"results": []any{map[string]any{"rows": []any{map[string]any{"contracts": "2", "revenue_cents": "6000"}}, "truncated": false}}}, nil
	})
	config["_project_id"] = "p"
	got, err := a.callDatabasePipeline(pipelineIdentity("7"), config)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "exact result", got, `[{"contracts":2,"revenue_cents":6000}]`)
	if len(p.batches) != 1 {
		t.Fatal(p.batches)
	}
	delete(config, "_project_id")
	after, _ := json.Marshal(config)
	if string(before) != string(after) {
		t.Fatal("mutated configuration")
	}
}
func TestCanonicalNamedValidationAndWatch(t *testing.T) {
	for _, op := range []string{"find", "aggregate"} {
		t.Run(op, func(t *testing.T) {
			config := dependentConfig(t, op)
			plan, err := validateDatabasePipeline(config)
			if err != nil {
				t.Fatal(err)
			}
			sameJSON(t, "sources", plan.Collections(), `["facts"]`)
			step := plan.Steps[0]
			probe, err := step.joinedProbe()
			if err != nil {
				t.Fatal(err)
			}
			if probe["op"] != op {
				t.Fatal(probe)
			}
			nodes := probe["args"].(map[string]any)["pipeline"].(map[string]any)["nodes"].([]any)
			if !filterMatchesNothing(nodes[0].(map[string]any)["where"]) {
				t.Fatal("probe scans real facts")
			}
		})
	}
	cases := map[string]func(map[string]any){
		"cycle":             func(p map[string]any) { p["nodes"].([]any)[1].(map[string]any)["from"] = "rates" },
		"unknown":           func(p map[string]any) { p["from"] = "missing" },
		"write":             func(p map[string]any) { p["nodes"].([]any)[0].(map[string]any)["op"] = "insert" },
		"dynamic structure": func(p map[string]any) { p["nodes"].([]any)[0].(map[string]any)["collection"] = "$param.collection" },
		"unknown key":       func(p map[string]any) { p["nodes"].([]any)[0].(map[string]any)["limit"] = 1 },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			config := dependentConfig(t, "aggregate")
			args := config["steps"].([]any)[0].(map[string]any)["args"].(map[string]any)
			change(args["pipeline"].(map[string]any))
			if _, err := validateDatabasePipeline(config); err == nil {
				t.Fatal("accepted invalid graph")
			}
		})
	}
}
func TestCanonicalStageInputs(t *testing.T) {
	config := dependentConfig(t, "aggregate")
	config["params"] = map[string]any{"centre": map[string]any{"from": "$args.centre", "type": "string", "required": true}}
	step := config["steps"].([]any)[0].(map[string]any)
	args := step["args"].(map[string]any)
	node := args["pipeline"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
	node["where"].(map[string]any)["value"] = "$param.centre"
	if _, err := validateDatabasePipeline(config); err == nil || !strings.Contains(err.Error(), "stage_params") {
		t.Fatal(err)
	}
	step["stage_params"] = []any{"centre"}
	if _, err := validateDatabasePipeline(config); err != nil {
		t.Fatal(err)
	}
	// Identity is trusted without opting in client stage values.
	config["params"].(map[string]any)["centre"].(map[string]any)["from"] = "$identity.subject"
	delete(step, "stage_params")
	if _, err := validateDatabasePipeline(config); err != nil {
		t.Fatal(err)
	}
}

func TestRealDatabaseCanonicalDependentRead(t *testing.T) {
	r := newRealDatabase(t, "graphql-canonical-pipeline")
	r.collection("facts", "id:text", "centre:text", "period:text", "caller:text", "kind:text", "contracts:integer", "campaign:text", "adviser:text", "display_date:text")
	raw, err := os.ReadFile("testdata/dependent-records.json")
	if err != nil {
		t.Fatal(err)
	}
	var rows []map[string]any
	if err = json.Unmarshal(raw, &rows); err != nil {
		t.Fatal(err)
	}
	r.insert("facts", rows...)
	r.collection("members", "id:text", "user_id:text", "centre:text")
	r.operator("db_index_create", map[string]any{"collection": "members", "index": map[string]any{"name": "by_user", "fields": []any{map[string]any{"field": "user_id"}}}})
	r.insert("members", map[string]any{"id": "one", "user_id": "7", "centre": "a"}, map[string]any{"id": "two", "user_id": "8", "centre": "b"})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	r.requireAuth()
	r.permissions["7"] = []string{"reports:read"}
	r.permissions["8"] = []string{"reports:read"}
	r.permissions["9"] = []string{"reports:read"}
	r.permissions["10"] = []string{}
	resolvers := map[string]map[string]any{}
	for _, op := range []string{"find", "aggregate"} {
		config := dependentConfig(t, op)
		config["params"] = map[string]any{"user": map[string]any{"from": "$identity.subject", "type": "string", "required": true}}
		config["access"] = map[string]any{"require_any_permission": []any{"reports:read"}, "values": map[string]any{"centres": map[string]any{"optional": true, "otherwise": map[string]any{"collection": "members", "field": "centre", "where": map[string]any{"field": "user_id", "op": "eq", "value": "$param.user"}}}}}
		step := config["steps"].([]any)[0].(map[string]any)
		args := step["args"].(map[string]any)
		source := args["pipeline"].(map[string]any)["nodes"].([]any)[0].(map[string]any)
		source["where"] = map[string]any{"field": "centre", "op": "in", "value": "$scope.centres"}
		if op == "find" {
			config["params"].(map[string]any)["cursor"] = map[string]any{"from": "$args.cursor", "type": "string"}
			step["page"] = map[string]any{"size": 1, "cursor": "$param.cursor"}
			config["result"] = map[string]any{"rows": "$steps.report", "total": "$steps.report.total", "nextCursor": "$steps.report.nextCursor", "hasMore": "$steps.report.hasMore"}
		} else {
			config["result"] = "$steps.report.first"
		}
		resolvers["Query."+op] = map[string]any{"operation": databasePipelineOperation, "config": config}
	}
	if fail := r.publish(`scalar JSON
 type Query { aggregate: JSON find(cursor: String): JSON }`, resolvers); fail != "" {
		t.Fatal(fail)
	}
	realJSON(t, "period rates before sale aggregation", r.queryAs("7", `{ aggregate }`, nil), `{"aggregate":{"contracts":2,"revenue_cents":6000}}`)
	// A client filter-like argument cannot replace a server-owned source policy.
	realJSON(t, "foreign scope", r.queryAs("8", `{ aggregate }`, nil), `{"aggregate":{"contracts":100,"revenue_cents":400000}}`)
	realJSON(t, "empty scope", r.queryAs("9", `{ aggregate }`, nil), `{"aggregate":{"contracts":null,"revenue_cents":null}}`)
	if got := r.queryAs("10", `{ aggregate }`, nil); got.code() != "permission_denied" {
		t.Fatal(got)
	}
	page := r.queryAs("7", `{ find }`, nil)
	if len(page.Errors) > 0 {
		t.Fatal(page.Errors)
	}
	obj := page.Data["find"].(map[string]any)
	sameJSON(t, "first rate", obj["rows"], `[{"centre":"a","period":"sep","callers":"2","rate_cents":"4000"}]`)
	if obj["total"] != float64(2) || obj["hasMore"] != true {
		t.Fatal(obj)
	}
	cursor, _ := obj["nextCursor"].(string)
	if !strings.HasPrefix(cursor, cursorPrefix) {
		t.Fatal(obj)
	}
	next := r.queryAs("7", `query($cursor:String){ find(cursor:$cursor) }`, map[string]any{"cursor": cursor})
	if len(next.Errors) > 0 {
		t.Fatal(next.Errors)
	}
	sameJSON(t, "next rate", next.Data["find"].(map[string]any)["rows"], `[{"centre":"a","period":"oct","callers":"4","rate_cents":"2000"}]`)
	if got := r.queryAs("8", `query($cursor:String){ find(cursor:$cursor) }`, map[string]any{"cursor": cursor}); got.code() != "invalid_cursor" {
		t.Fatal(got)
	}
}

func TestCanonicalWatchAndTopN(t *testing.T) {
	config := dependentConfig(t, "find")
	step := config["steps"].([]any)[0].(map[string]any)
	step["top_n"] = true
	pipeline := step["args"].(map[string]any)["pipeline"].(map[string]any)
	pipeline["nodes"] = append(pipeline["nodes"].([]any), map[string]any{"name": "other", "op": "scan", "collection": "other", "where": map[string]any{"exists": map[string]any{"collection": "grants", "on": []any{map[string]any{"localField": "owner", "foreignField": "id"}}}}})
	plan, err := validateDatabasePipeline(config)
	if err != nil {
		t.Fatal(err)
	}
	sameJSON(t, "all source watches", plan.Collections(), `["facts","grants","other"]`)
	config["watch_collections"] = []any{"extra"}
	spec, ok := databaseWatchFor("p", sourceRecord{Kind: "database", Config: map[string]any{}}, resolverRecord{Operation: databasePipelineOperation, Config: config})
	if !ok {
		t.Fatal("missing watch")
	}
	sameJSON(t, "watch list adds without hiding sources", spec.collections, `["extra","facts","grants","other"]`)
}

func TestRealDatabaseCanonicalFixedRead(t *testing.T) {
	r := newRealDatabase(t, "graphql-canonical-fixed")
	r.collection("items", "id:text", "amount:integer")
	r.insert("items", map[string]any{"id": "one", "amount": "9007199254740993"})
	r.operator("db_access_grant", map[string]any{"app": "graphql", "access": "read"})
	config := decodeConfig(t, `{"pipeline":{"version":1,"nodes":[{"name":"input","op":"scan","collection":"items"}],"from":"input"},"metrics":[{"name":"amount","op":"sum","field":"amount"}]}`)
	fail := r.publish(`scalar JSON
 type Query { fixed: JSON }`, map[string]map[string]any{"Query.fixed": {"operation": "aggregate", "config": config}})
	if os.Getenv("GRAPHQL_TEST_DATABASE_CANONICAL") == "refused" {
		if !strings.Contains(fail, "0.14.0") {
			t.Fatal(fail)
		}
		return
	}
	if fail != "" {
		t.Fatal(fail)
	}
	realJSON(t, "plain canonical resolver keeps exact integers", r.query(`{ fixed }`, nil), `{"fixed":[{"amount":"9007199254740993"}]}`)
	// A new invalid field must fail publication while the existing release serves.
	config["metrics"].([]any)[0].(map[string]any)["field"] = "missing"
	if fail := r.publish(`scalar JSON
 type Query { fixed: JSON }`, map[string]map[string]any{"Query.fixed": {"operation": "aggregate", "config": config}}); fail == "" {
		t.Fatal("published invalid field")
	}
	realJSON(t, "old release still serves", r.query(`{ fixed }`, nil), `{"fixed":[{"amount":"9007199254740993"}]}`)
}

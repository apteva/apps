package main

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

// Verified user -> latest enabled grant -> authorised offers -> grids and
// centres -> records, with the caller's single profile intersected in.
const chainPipeline = `{
  "database": "crm",
  "params": {"user": {"from": "$identity.subject", "required": true}},
  "access": {"values": {
    "grant": {"otherwise": {"collection": "grants", "field": "id",
      "where": {"and": [{"field": "user_id", "op": "eq", "value": "$param.user"}, {"field": "enabled", "op": "eq", "value": true}]},
      "orderBy": [{"field": "granted_at", "direction": "desc"}], "limit": 1}},
    "offers": {"otherwise": {"collection": "grant_offers", "field": "offer_id", "where": {"field": "grant_id", "op": "in", "value": "$scope.grant"}}},
    "grids": {"otherwise": {"collection": "offers", "field": "grid_ids", "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "offerCentres": {"otherwise": {"collection": "offers", "field": "centre_id", "where": {"field": "id", "op": "in", "value": "$scope.offers"}}},
    "home": {"otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}, "single_record": true},
    "centres": {"intersect": ["offerCentres", "home"]},
    "financeGrids": {"require_any_permission": ["finance:read"], "otherwise": {"collection": "finance_grids", "field": "id"}}
  }},
  "steps": [{"id": "rows", "op": "find", "max_rows": 100, "args": {"collection": "records", "where": {"and": [
    {"field": "grid_id", "op": "in", "value": "$scope.grids"},
    {"field": "centre_id", "op": "in", "value": "$scope.centres"}]}}}],
  "result": "$steps.rows"
}`

type chainData map[string][]any

func (data chainData) responder(t *testing.T) func([]any) (any, error) {
	return func(operations []any) (any, error) {
		results := []any{}
		for _, raw := range operations {
			args := raw.(map[string]any)["args"].(map[string]any)
			records := data[fmt.Sprint(args["collection"])]
			if records == nil {
				records = []any{}
			}
			results = append(results, map[string]any{"records": records, "hasMore": false})
		}
		return map[string]any{"results": results}, nil
	}
}

func chainConfig(t *testing.T, drop ...string) map[string]any {
	config := decodeConfig(t, chainPipeline)
	values := config["access"].(map[string]any)["values"].(map[string]any)
	for _, name := range drop {
		delete(values, name)
	}
	config["_project_id"] = "p1"
	return config
}

func TestDatabasePipelineChainedAccess(t *testing.T) {
	data := chainData{
		"grants":        {map[string]any{"id": "g2"}},
		"grant_offers":  {map[string]any{"offer_id": "o1"}, map[string]any{"offer_id": "o2"}},
		"offers":        {map[string]any{"grid_ids": []any{"grid1", "grid2"}, "centre_id": "c1"}, map[string]any{"grid_ids": []any{"grid2", "grid3"}, "centre_id": "c2"}},
		"profiles":      {map[string]any{"centre_id": "c2"}},
		"records":       {map[string]any{"id": "r1"}},
		"finance_grids": {map[string]any{"id": "grid1"}},
	}
	a, p := pipelineApp(t, data.responder(t))
	value, err := a.callDatabasePipeline(pipelineIdentity("u1"), chainConfig(t, "financeGrids"))
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(value) != "[map[id:r1]]" {
		t.Fatalf("root list result: %v", value)
	}
	// Level 0: grant + profile; level 1: offers; level 2: grids + centres
	// (intersect needs no read); then the steps.
	ops := func(i int) []any { return p.batches[i]["operations"].([]any) }
	if len(p.batches) != 4 || len(ops(0)) != 2 || len(ops(1)) != 1 || len(ops(2)) != 2 {
		t.Fatalf("batches: %d", len(p.batches))
	}
	grant := ops(0)[0].(map[string]any)["args"].(map[string]any)
	if grant["collection"] != "grants" || grant["limit"] != float64(1) || fmt.Sprint(grant["orderBy"]) != "[map[direction:desc field:granted_at]]" {
		t.Fatalf("latest grant read: %v", grant)
	}
	if offers := ops(1)[0].(map[string]any)["args"].(map[string]any); fmt.Sprint(offers["where"]) != "map[field:grant_id op:in value:[g2]]" {
		t.Fatalf("offers read: %v", offers["where"])
	}
	where := ops(3)[0].(map[string]any)["args"].(map[string]any)["where"].(map[string]any)["and"].([]any)
	if fmt.Sprint(where[0]) != "map[field:grid_id op:in value:[grid1 grid2 grid3]]" || fmt.Sprint(where[1]) != "map[field:centre_id op:in value:[c2]]" {
		t.Fatalf("records filtered by flattened grids and intersected centres: %v", where)
	}

	refuse := func(label string, ctx context.Context, data chainData, drop ...string) {
		t.Helper()
		a, p := pipelineApp(t, data.responder(t))
		_, err := a.callDatabasePipeline(ctx, chainConfig(t, drop...))
		if pipelineErrorCode(err) != "permission_denied" {
			t.Fatalf("%s: %v", label, err)
		}
		for _, batch := range p.batches {
			for _, op := range batch["operations"].([]any) {
				if op.(map[string]any)["args"].(map[string]any)["collection"] == "records" {
					t.Fatalf("%s read records", label)
				}
			}
		}
	}
	// Two profiles sharing one centre are still two profiles.
	twoProfiles := chainData{}
	for k, v := range data {
		twoProfiles[k] = v
	}
	twoProfiles["profiles"] = []any{map[string]any{"centre_id": "c2"}, map[string]any{"centre_id": "c2"}}
	refuse("two profiles", pipelineIdentity("u1"), twoProfiles, "financeGrids")
	// No enabled grant stops the chain before offers are read.
	noGrant := chainData{"profiles": data["profiles"]}
	refuse("no grant", pipelineIdentity("u1"), noGrant, "financeGrids")
	// The profile centre is not among the offers' centres.
	elsewhere := chainData{}
	for k, v := range data {
		elsewhere[k] = v
	}
	elsewhere["profiles"] = []any{map[string]any{"centre_id": "c9"}}
	refuse("empty intersection", pipelineIdentity("u1"), elsewhere, "financeGrids")
	// A value requiring a permission refuses callers without it.
	refuse("value permission", pipelineIdentity("u1"), data)
	a, _ = pipelineApp(t, data.responder(t))
	if _, err := a.callDatabasePipeline(pipelineIdentity("u1", "finance:read"), chainConfig(t)); err != nil {
		t.Fatalf("permitted caller: %v", err)
	}
}

func TestDatabasePipelineAccessGraphValidation(t *testing.T) {
	read := func(collection, where string) string {
		return `{"otherwise":{"collection":"` + collection + `","field":"id"` + where + `}}`
	}
	dep := func(name string) string {
		return `,"where":{"field":"x","op":"in","value":"$scope.` + name + `"}`
	}
	plan := func(values string) string {
		return `{"access":{"values":{` + values + `}},"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":"$steps.s"}`
	}
	cases := map[string]string{
		"cycle":               plan(`"a":` + read("a", dep("b")) + `,"b":` + read("b", dep("a"))),
		"self":                plan(`"a":` + read("a", dep("a"))),
		"too deep":            plan(`"a":` + read("a", "") + `,"b":` + read("b", dep("a")) + `,"c":` + read("c", dep("b")) + `,"d":` + read("d", dep("c")) + `,"e":` + read("e", dep("d"))),
		"unknown ref":         plan(`"a":` + read("a", dep("zz"))),
		"union one":           plan(`"a":` + read("a", "") + `,"u":{"union":["a"]}`),
		"union and read":      plan(`"a":` + read("a", "") + `,"b":` + read("b", "") + `,"u":{"union":["a","b"],"otherwise":{"collection":"x","field":"id"}}`),
		"union unknown":       plan(`"a":` + read("a", "") + `,"u":{"union":["a","zz"]}`),
		"both set ops":        plan(`"a":` + read("a", "") + `,"b":` + read("b", "") + `,"u":{"union":["a","b"],"intersect":["a","b"]}`),
		"single_record+limit": plan(`"a":{"otherwise":{"collection":"a","field":"id","orderBy":[{"field":"id"}],"limit":1},"single_record":true}`),
		"limit and max":       plan(`"a":{"otherwise":{"collection":"a","field":"id","orderBy":[{"field":"id"}],"limit":1,"max":5}}`),
	}
	for name, text := range cases {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	// a, b, c, d use four read levels; a union over d is computed in memory
	// and needs no fifth.
	if _, err := validateDatabasePipeline(decodeConfig(t, plan(`"a":`+read("a", "")+`,"b":`+read("b", dep("a"))+`,"c":`+read("c", dep("b"))+`,"d":`+read("d", dep("c"))+`,"u":{"union":["a","d"]}`))); err != nil {
		t.Errorf("derived value after four read levels rejected: %v", err)
	}
	if _, err := validateDatabasePipeline(decodeConfig(t, plan(`"a":`+read("a", "")+`,"b":`+read("b", dep("a"))+`,"c":`+read("c", dep("b"))+`,"d":`+read("d", dep("c"))))); err != nil {
		t.Fatalf("four levels rejected: %v", err)
	}
}

// Page through records with a client-chosen size (bounded by max_rows) and
// cursor, plus a total over the same filter in the same snapshot.
func TestDatabasePipelinePaging(t *testing.T) {
	text := `{"params":{
    "centre":{"from":"$identity.claim.centre","required":true},
    "first":{"from":"$args.first","type":"number"},
    "after":{"from":"$args.after"}},
  "steps":[
    {"id":"list","op":"find","max_rows":50,"page":{"size":"$param.first","cursor":"$param.after"},
     "args":{"collection":"records","where":{"field":"centre_id","op":"eq","value":"$param.centre"},"orderBy":[{"field":"id"}]}},
    {"id":"total","op":"count","count_of":"list"}],
  "result":{"rows":"$steps.list","hasMore":"$steps.list.hasMore","nextCursor":"$steps.list.nextCursor","total":"$steps.total"}}`
	run := func(args map[string]any, hasMore bool) (map[string]any, map[string]any, map[string]any, error) {
		a, p := pipelineApp(t, func([]any) (any, error) {
			page := map[string]any{"records": []any{map[string]any{"id": "r3"}}, "hasMore": hasMore}
			if hasMore {
				page["nextCursor"] = "cur-2"
			}
			return map[string]any{"results": []any{page, map[string]any{"count": "57"}}}, nil
		})
		config := decodeConfig(t, text)
		config["_project_id"], config["graphql_args"] = "p1", args
		ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Subject: "u1", Claims: map[string]any{"centre": "c1"}})
		value, err := a.callDatabasePipeline(ctx, config)
		if err != nil {
			return nil, nil, nil, err
		}
		ops := p.batches[0]["operations"].([]any)
		return value.(map[string]any), ops[0].(map[string]any)["args"].(map[string]any), ops[1].(map[string]any)["args"].(map[string]any), nil
	}
	out, list, count, err := run(map[string]any{"first": 10, "after": "cur-1"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if list["limit"] != float64(10) || list["cursor"] != "cur-1" {
		t.Fatalf("page args: %v", list)
	}
	if count["collection"] != "records" || !reflect.DeepEqual(count["where"], list["where"]) || count["limit"] != nil {
		t.Fatalf("count_of args: %v vs %v", count, list)
	}
	if out["hasMore"] != true || out["nextCursor"] != "cur-2" || out["total"] != int64(57) || fmt.Sprint(out["rows"]) != "[map[id:r3]]" {
		t.Fatalf("page: %v", out)
	}
	// A raised size is bounded by max_rows; the last page has no cursor.
	out, list, _, err = run(map[string]any{"first": 5000}, false)
	if err != nil || list["limit"] != float64(50) || list["cursor"] != nil || out["hasMore"] != false || out["nextCursor"] != nil {
		t.Fatalf("bounded last page: %v %v %v", err, list, out)
	}
	if _, _, _, err := run(map[string]any{"first": -1}, false); pipelineErrorCode(err) != "invalid_request" {
		t.Fatalf("negative size: %v", err)
	}

	for name, step := range map[string]string{
		"page on aggregate":  `{"id":"s","op":"aggregate","page":{"size":5},"args":{"collection":"c","metrics":[{"name":"n","op":"count"}]}}`,
		"page with top_n":    `{"id":"s","op":"find","top_n":true,"page":{"size":5},"args":{"collection":"c"}}`,
		"size over max_rows": `{"id":"s","op":"find","max_rows":10,"page":{"size":20},"args":{"collection":"c"}}`,
		"size param string":  `{"id":"s","op":"find","page":{"size":"$param.p"},"args":{"collection":"c"}}`,
		"cursor literal":     `{"id":"s","op":"find","page":{"cursor":"abc"},"args":{"collection":"c"}}`,
		"client limit":       `{"id":"s","op":"find","args":{"collection":"c","limit":5}}`,
		"client cursor":      `{"id":"s","op":"find","args":{"collection":"c","cursor":"$param.p"}}`,
		"count_of with args": `{"id":"s","op":"find","args":{"collection":"c"}},{"id":"t","op":"count","count_of":"s","args":{"collection":"c"}}`,
		"count_of non-find":  `{"id":"s","op":"count","args":{"collection":"c"}},{"id":"t","op":"count","count_of":"s"}`,
	} {
		text := `{"params":{"p":{"from":"$args.p"}},"steps":[` + step + `],"result":{"r":"$steps.s"}}`
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	unpaged := `{"steps":[{"id":"s","op":"find","args":{"collection":"c"}}],"result":{"m":"$steps.s.hasMore"}}`
	if _, err := validateDatabasePipeline(decodeConfig(t, unpaged)); err == nil {
		t.Error("page metadata of an unpaged step was accepted")
	}
}

// Authorised callers get amounts; other permitted callers get the record with
// the amount nulled, decided by verified permissions inside the pipeline.
func TestDatabasePipelineMasksFieldsByPermission(t *testing.T) {
	text := `{"params":{"perms":{"from":"$identity.permissions","type":"string_list"}},
  "steps":[{"id":"rows","op":"find","args":{"collection":"sales","orderBy":[{"field":"id"}]}}],
  "combine":{"view":{"join":["rows"],"on":["id"],"keep":["rows"],"fields":{
    "amount":{"op":"if","condition":{"op":"in","args":[{"const":"finance:read"},"$param.perms"]},"then":"rows.amount","else":{"const":null}}}}},
  "result":"$combine.view"}`
	run := func(permissions ...string) string {
		a, _ := pipelineApp(t, func([]any) (any, error) {
			return map[string]any{"results": []any{map[string]any{"records": []any{
				map[string]any{"id": "s1", "client": "Acme", "amount": 120},
				map[string]any{"id": "s2", "client": "Beta", "amount": 80},
			}}}}, nil
		})
		config := decodeConfig(t, text)
		config["_project_id"] = "p1"
		value, err := a.callDatabasePipeline(pipelineIdentity("u1", permissions...), config)
		if err != nil {
			t.Fatal(err)
		}
		return fmt.Sprint(value)
	}
	if got := run("sales:read", "finance:read"); got != "[map[amount:120 client:Acme id:s1] map[amount:80 client:Beta id:s2]]" {
		t.Fatalf("finance caller: %s", got)
	}
	if got := run("sales:read"); got != "[map[amount:<nil> client:Acme id:s1] map[amount:<nil> client:Beta id:s2]]" {
		t.Fatalf("masked caller: %s", got)
	}
	for name, text := range map[string]string{
		"undeclared param": strings.Replace(text, `"$param.perms"`, `"$param.nope"`, 1),
		"keep unjoined":    strings.Replace(text, `"keep":["rows"]`, `"keep":["other"]`, 1),
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestModuleInOperator(t *testing.T) {
	runtime := moduleRuntime{}
	for _, c := range []struct {
		value, list any
		want        bool
	}{
		{"a", []any{"b", "a"}, true},
		{"c", []any{"b", "a"}, false},
		{3, []any{"3"}, true},
		{"a", nil, false},
		{nil, []any{"a"}, false},
		{"a", []string{"a"}, true},
	} {
		got, err := runtime.eval(map[string]any{"op": "in", "args": []any{map[string]any{"input": "v"}, map[string]any{"input": "l"}}}, map[string]any{"v": c.value, "l": c.list}, 0)
		if err != nil || got != c.want {
			t.Fatalf("in(%v, %v) = %v, %v", c.value, c.list, got, err)
		}
	}
	if _, err := runtime.eval(map[string]any{"op": "in", "args": []any{map[string]any{"const": "a"}, map[string]any{"const": "a"}}}, nil, 0); err == nil {
		t.Fatal("non-list accepted")
	}
	if got := resolveModuleInput("$identity.permissions", nil, nil, &requestIdentity{Permissions: []string{"b", "a"}, Claims: map[string]any{"permissions": []any{"c"}}}); fmt.Sprint(got) != "[a b c]" {
		t.Fatalf("$identity.permissions: %v", got)
	}
}

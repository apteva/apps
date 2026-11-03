package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

// offersPolicy is the CRM catalogue rule:
//
//	allowed = hasRead AND (hasAllCentres OR profiles == 1 OR grant nonempty)
//	offers  = !allowed ? [] : (hasAllCentres OR grant empty) ? catalogue : grant
const offersPolicy = `{
  "params": {
    "user": {"from": "$identity.subject", "required": true},
    "perms": {"from": "$identity.permissions", "type": "string_list"}
  },
  "access": {"values": {
    "profileIds": {"optional": true, "otherwise": {"collection": "profiles", "field": "id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "grantOffers": {"optional": true, "otherwise": {"collection": "grants", "field": "offer_ids",
      "where": {"field": "user_id", "op": "eq", "value": "$param.user"}, "orderBy": [{"field": "id", "direction": "desc"}], "limit": 1}},
    "catalogue": {"optional": true, "otherwise": {"collection": "offers", "field": "local_id"}},
    "offers": {"optional": true, "expr": {"op": "if",
      "condition": {"op": "and", "args": [
        {"op": "in", "args": [{"const": "read"}, "$param.perms"]},
        {"op": "or", "args": [
          {"op": "in", "args": [{"const": "all_centres"}, "$param.perms"]},
          {"op": "eq", "args": [{"op": "length", "args": ["$scope.profileIds"]}, {"const": 1}]},
          {"op": "gt", "args": [{"op": "length", "args": ["$scope.grantOffers"]}, {"const": 0}]}]}]},
      "then": {"op": "if",
        "condition": {"op": "or", "args": [
          {"op": "in", "args": [{"const": "all_centres"}, "$param.perms"]},
          {"op": "eq", "args": [{"op": "length", "args": ["$scope.grantOffers"]}, {"const": 0}]}]},
        "then": "$scope.catalogue", "else": "$scope.grantOffers"},
      "else": {"const": []}}}
  }},
  "steps": [{"id": "rows", "op": "find", "args": {"collection": "offers", "where": {"field": "local_id", "op": "in", "value": "$scope.offers"}, "orderBy": [{"field": "id"}]}}],
  "result": "$steps.rows"
}`

type offersFixture struct {
	profiles []any // profile ids
	grant    []any // latest grant's offer_ids; nil = no grant
	catalog  []string
}

func (f offersFixture) responder(t *testing.T) func([]any) (any, error) {
	return func(operations []any) (any, error) {
		results := []any{}
		for _, raw := range operations {
			args := raw.(map[string]any)["args"].(map[string]any)
			records := []any{}
			switch args["collection"] {
			case "profiles":
				for _, id := range f.profiles {
					records = append(records, map[string]any{"id": id})
				}
			case "grants":
				if f.grant != nil {
					records = append(records, map[string]any{"offer_ids": f.grant})
				}
			case "offers":
				if args["select"] != nil { // the catalogue read
					for _, id := range f.catalog {
						records = append(records, map[string]any{"local_id": id})
					}
					break
				}
				wanted := map[string]bool{}
				where := args["where"].(map[string]any)
				if where["op"] == "eq" {
					wanted[fmt.Sprint(where["value"])] = true
				}
				for _, value := range asAnyList(where["value"]) {
					wanted[fmt.Sprint(value)] = true
				}
				for _, id := range f.catalog {
					if wanted[id] {
						records = append(records, map[string]any{"local_id": id})
					}
				}
			}
			results = append(results, map[string]any{"records": records, "hasMore": false})
		}
		return map[string]any{"results": results}, nil
	}
}

func runOffers(t *testing.T, f offersFixture, permissions ...string) ([]string, *dbPipelinePlatform, error) {
	t.Helper()
	a, p := pipelineApp(t, f.responder(t))
	config := decodeConfig(t, offersPolicy)
	config["_project_id"] = "p1"
	value, err := a.callDatabasePipeline(pipelineIdentity("u1", permissions...), config)
	if err != nil {
		return nil, p, err
	}
	ids := []string{}
	for _, raw := range value.([]any) {
		ids = append(ids, raw.(map[string]any)["local_id"].(string))
	}
	return ids, p, nil
}

func TestDatabasePipelineOffersPolicy(t *testing.T) {
	catalog := []string{"offer-a", "offer-b", "offer-c"}
	for _, c := range []struct {
		name  string
		f     offersFixture
		perms []string
		want  string
	}{
		{"one profile, no grant: catalogue", offersFixture{profiles: []any{"1"}, catalog: catalog}, []string{"read"}, "offer-a,offer-b,offer-c"},
		{"one profile, latest grant empty: catalogue", offersFixture{profiles: []any{"1"}, grant: []any{}, catalog: catalog}, []string{"read"}, "offer-a,offer-b,offer-c"},
		{"grant narrows", offersFixture{profiles: []any{"1"}, grant: []any{"offer-a"}, catalog: catalog}, []string{"read"}, "offer-a"},
		{"unknown grant ids never fall back", offersFixture{profiles: []any{"1"}, grant: []any{"missing"}, catalog: catalog}, []string{"read"}, ""},
		{"duplicates in grant", offersFixture{profiles: []any{"1"}, grant: []any{"offer-a", "offer-a"}, catalog: catalog}, []string{"read"}, "offer-a"},
		{"no profile, no grant: nothing", offersFixture{catalog: catalog}, []string{"read"}, ""},
		{"two profiles: nothing", offersFixture{profiles: []any{"1", "2"}, catalog: catalog}, []string{"read"}, ""},
		{"grant without profile authorises", offersFixture{grant: []any{"offer-b"}, catalog: catalog}, []string{"read"}, "offer-b"},
		{"no read permission", offersFixture{profiles: []any{"1"}, grant: []any{"offer-a"}, catalog: catalog}, []string{"all_centres"}, ""},
		{"all centres: catalogue despite grant", offersFixture{grant: []any{"offer-a"}, catalog: catalog}, []string{"read", "all_centres"}, "offer-a,offer-b,offer-c"},
		{"empty catalogue is a valid empty result", offersFixture{profiles: []any{"1"}}, []string{"read"}, ""},
	} {
		got, p, err := runOffers(t, c.f, c.perms...)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if strings.Join(got, ",") != c.want {
			t.Fatalf("%s: got %v, want %s", c.name, got, c.want)
		}
		// All three reads share one level; the selector runs in memory. An
		// empty selection is answered without a step read.
		access := p.batches[0]["operations"].([]any)
		if len(access) != 3 {
			t.Fatalf("%s: access batch has %d reads", c.name, len(access))
		}
		// Unknown grant ids are a nonempty scope: the read runs and matches nothing.
		emptyScope := c.want == "" && !strings.HasPrefix(c.name, "unknown")
		if want := map[bool]int{true: 1, false: 2}[emptyScope]; len(p.batches) != want {
			t.Fatalf("%s: %d batches, want %d", c.name, len(p.batches), want)
		}
	}
}

func TestDatabasePipelineOptionalKeepsFailures(t *testing.T) {
	plan := func(value string) map[string]any {
		config := decodeConfig(t, `{"params":{"user":{"from":"$identity.subject"}},"access":{"values":{"v":`+value+`}},
		  "steps":[{"id":"s","op":"count","args":{"collection":"c","where":{"field":"x","op":"in","value":"$scope.v"}}}],"result":"$steps.s"}`)
		config["_project_id"] = "p1"
		return config
	}
	read := `{"collection":"r","field":"id","where":{"field":"u","op":"eq","value":"$param.user"}}`
	respond := func(page map[string]any, err error) func([]any) (any, error) {
		return func(operations []any) (any, error) {
			if err != nil {
				return nil, err
			}
			out := []any{}
			for range operations {
				out = append(out, page)
			}
			return map[string]any{"results": out}, nil
		}
	}
	empty := map[string]any{"records": []any{}, "hasMore": false}
	run := func(value string, response func([]any) (any, error), permissions ...string) (any, error, int) {
		a, p := pipelineApp(t, response)
		out, err := a.callDatabasePipeline(pipelineIdentity("u1", permissions...), plan(value))
		return out, err, len(p.batches)
	}
	// Optional empty: count over an empty scope is 0, answered locally.
	if out, err, batches := run(`{"optional":true,"otherwise":`+read+`}`, respond(empty, nil)); err != nil || out != int64(0) || batches != 1 {
		t.Fatalf("optional empty: %v %v %d", out, err, batches)
	}
	// Required empty keeps refusing.
	if _, err, _ := run(`{"otherwise":`+read+`}`, respond(empty, nil)); pipelineErrorCode(err) != "permission_denied" {
		t.Fatalf("required empty: %v", err)
	}
	for name, c := range map[string]struct {
		value    string
		response func([]any) (any, error)
		code     string
	}{
		"truncated":         {`{"optional":true,"otherwise":` + strings.Replace(read, `"id"`, `"id","max":1`, 1) + `}`, respond(map[string]any{"records": []any{map[string]any{"id": "a"}, map[string]any{"id": "b"}}, "hasMore": true}, nil), "source_result_truncated"},
		"single_record":     {`{"optional":true,"single_record":true,"otherwise":` + read + `}`, respond(empty, nil), "permission_denied"},
		"value permission":  {`{"optional":true,"require_any_permission":["x"],"otherwise":` + read + `}`, respond(empty, nil), "permission_denied"},
		"database failure":  {`{"optional":true,"otherwise":` + read + `}`, respond(nil, errors.New("database.db_batch: resource_limit: too much (code=-32000)")), "resource_limit"},
		"invalid response":  {`{"optional":true,"otherwise":` + read + `}`, func([]any) (any, error) { return map[string]any{"results": []any{}}, nil }, "source_error"},
		"expr not a list":   {`{"optional":true,"expr":{"const":"a"}}`, respond(empty, nil), "invalid_result"},
		"expr nested lists": {`{"optional":true,"expr":{"const":[["a"]]}}`, respond(empty, nil), "invalid_result"},
	} {
		if _, err, _ := run(c.value, c.response); pipelineErrorCode(err) != c.code {
			t.Errorf("%s: want %s, got %v", name, c.code, err)
		}
	}
}

func TestDatabasePipelineExprValidation(t *testing.T) {
	plan := func(params, values string) string {
		return `{"params":{` + params + `},"access":{"values":{` + values + `}},"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":"$steps.s"}`
	}
	identity := `"perms":{"from":"$identity.permissions","type":"string_list"},"fixed":{"value":"x"},"client":{"from":"$args.flag"}`
	read := `"r":{"optional":true,"otherwise":{"collection":"r","field":"id"}}`
	for name, text := range map[string]string{
		"client flag":        plan(identity, read+`,"e":{"expr":{"op":"if","condition":"$param.client","then":"$scope.r","else":{"const":[]}}}`),
		"undeclared param":   plan(identity, `"e":{"expr":"$param.nope"}`),
		"unknown scope":      plan(identity, `"e":{"expr":"$scope.nope"}`),
		"self":               plan(identity, `"e":{"expr":"$scope.e"}`),
		"cycle":              plan(identity, `"e":{"expr":"$scope.f"},"f":{"expr":"$scope.e"}`),
		"cycle through read": plan(identity, `"e":{"expr":"$scope.f"},"f":{"otherwise":{"collection":"r","field":"id","where":{"field":"x","op":"in","value":"$scope.e"}}}`),
		"call op":            plan(identity, `"e":{"expr":{"op":"call","module":"m","version":1}}`),
		"date op":            plan(identity, `"e":{"expr":{"op":"date_add","args":[{"const":"2026-01-01"},{"const":1}]}}`),
		"bare literal":       plan(identity, `"e":{"expr":"offer-a"}`),
		"if missing else":    plan(identity, read+`,"e":{"expr":{"op":"if","condition":{"const":true},"then":"$scope.r"}}`),
		"unknown key":        plan(identity, read+`,"e":{"expr":{"op":"length","args":["$scope.r"],"value":1}}`),
		"expr with read":     plan(identity, read+`,"e":{"expr":"$scope.r","otherwise":{"collection":"r","field":"id"}}`),
		"expr with single":   plan(identity, read+`,"e":{"expr":"$scope.r","single":true}`),
		"optional string":    plan(identity, `"e":{"optional":"yes","expr":{"const":[]}}`),
	} {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	good := plan(identity, read+`,"e":{"optional":true,"expr":{"op":"if","condition":{"op":"and","args":[{"op":"in","args":["$param.fixed","$param.perms"]},{"op":"not","args":[{"op":"gt","args":[{"op":"length","args":["$scope.r"]},{"const":3}]}]}]},"then":"$scope.r","else":{"const":[]}}}`)
	parsed, err := validateDatabasePipeline(decodeConfig(t, good))
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed.AccessLevels) != 1 {
		t.Fatalf("a selector over a level-0 read should stay in level 0: %v", parsed.AccessLevels)
	}
}

func TestDatabasePipelineInListsWithinDatabaseLimits(t *testing.T) {
	if got := fmt.Sprint(normalizeInLeaf(map[string]any{"field": "id", "op": "in", "value": []any{}})); got != "map[and:[map[field:id op:is_null] map[field:id op:is_not_null]]]" {
		t.Fatalf("empty in: %s", got)
	}
	values := make([]any, 250)
	for i := range values {
		values[i] = fmt.Sprint(i)
	}
	chunked := normalizeInLeaf(map[string]any{"field": "id", "op": "in", "value": values})["or"].([]any)
	if len(chunked) != 3 || len(chunked[0].(map[string]any)["value"].([]any)) != 100 || len(chunked[2].(map[string]any)["value"].([]any)) != 50 {
		t.Fatalf("chunks: %v", len(chunked))
	}
	if leaf := normalizeInLeaf(map[string]any{"field": "id", "op": "in", "value": values[:100]}); leaf["op"] != "in" {
		t.Fatalf("100 values must stay one leaf: %v", leaf)
	}
	nothing := map[string]any{"field": "x", "op": "in", "value": []any{}}
	for _, c := range []struct {
		filter map[string]any
		empty  bool
	}{
		{normalizeInLeaf(nothing), true},
		{map[string]any{"and": []any{map[string]any{"field": "y", "op": "eq", "value": 1}, normalizeInLeaf(nothing)}}, true},
		{map[string]any{"or": []any{map[string]any{"field": "y", "op": "eq", "value": 1}, normalizeInLeaf(nothing)}}, false},
		{map[string]any{"or": []any{normalizeInLeaf(nothing), normalizeInLeaf(nothing)}}, true},
		{map[string]any{"field": "y", "op": "eq", "value": 1}, false},
	} {
		if filterMatchesNothing(c.filter) != c.empty {
			t.Fatalf("filterMatchesNothing(%v) != %v", c.filter, c.empty)
		}
	}

	// A batch mixes local answers with sent operations; aggregates over an
	// empty scope still go to Database (as the contradiction), keeping its
	// summary-row semantics; or keeps the non-empty branch.
	text := `{"params":{"ids":{"from":"$args.ids","type":"string_list"}},"steps":[
	  {"id":"f","op":"find","args":{"collection":"c","where":{"field":"id","op":"in","value":"$param.ids"}}},
	  {"id":"n","op":"count","args":{"collection":"c","where":{"field":"id","op":"in","value":"$param.ids"}}},
	  {"id":"a","op":"aggregate","args":{"collection":"c","where":{"field":"id","op":"in","value":"$param.ids"},"metrics":[{"name":"n","op":"count"}]}},
	  {"id":"o","op":"count","args":{"collection":"c","where":{"or":[{"field":"id","op":"in","value":"$param.ids"},{"field":"k","op":"eq","value":1}]}}}],
	  "result":{"f":"$steps.f","n":"$steps.n","a":"$steps.a","o":"$steps.o"}}`
	a, p := pipelineApp(t, func(operations []any) (any, error) {
		out := []any{}
		for _, raw := range operations {
			switch raw.(map[string]any)["op"] {
			case "aggregate":
				out = append(out, map[string]any{"rows": []any{map[string]any{"n": "0"}}})
			default:
				out = append(out, map[string]any{"count": "4"})
			}
		}
		return map[string]any{"results": out}, nil
	})
	config := decodeConfig(t, text)
	config["_project_id"], config["graphql_args"] = "p1", map[string]any{"ids": []any{}}
	out, err := a.callDatabasePipeline(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	sent := p.batches[0]["operations"].([]any)
	if len(sent) != 2 || sent[0].(map[string]any)["op"] != "aggregate" || !strings.Contains(fmt.Sprint(sent[0]), "is_not_null") {
		t.Fatalf("sent: %v", sent)
	}
	result := out.(map[string]any)
	if fmt.Sprint(result["f"]) != "[]" || result["n"] != int64(0) || result["o"] != int64(4) || fmt.Sprint(result["a"]) != "[map[n:0]]" {
		t.Fatalf("results: %v", result)
	}
	if got, _ := (moduleRuntime{}).eval(map[string]any{"op": "length", "args": []any{map[string]any{"input": "l"}}}, map[string]any{"l": []string{"a", "b"}}, 0); got != int64(2) {
		t.Fatalf("length of a scope list: %v", got)
	}
}

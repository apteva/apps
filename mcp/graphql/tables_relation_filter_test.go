package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

func relationTestSources() []sourceRecord {
	return []sourceRecord{
		{ID: 1, Name: "prospects", Kind: "tables", Status: "active", Config: map[string]any{"table": "prospects"}},
		{ID: 2, Name: "calls", Kind: "tables", Status: "active", Config: map[string]any{"table": "calls"}},
	}
}

func TestRelationFilterCompoundParentCoalesceAndMultiKey(t *testing.T) {
	filter := map[string]any{"and": []any{
		map[string]any{"or": []any{
			map[string]any{"eq": []any{map[string]any{"column": "prospect_id"}, map[string]any{"parent": "id"}}},
			map[string]any{"eq": []any{map[string]any{"column": "business_id"}, map[string]any{"coalesce": []any{map[string]any{"parent": "business.id"}, map[string]any{"parent": "legacy_id"}}}}},
		}},
		map[string]any{"eq": []any{map[string]any{"column": "centre_id"}, map[string]any{"parent": "centre.id"}}},
	}}
	if err := validateRelationFilter("list", map[string]any{"relation_filter": filter}, relationTestSources()); err != nil {
		t.Fatal(err)
	}
	eval := &relationEval{parent: map[string]any{"id": "physical", "business": map[string]any{"id": nil}, "legacy_id": "business", "centre": map[string]any{"id": "c1"}}, args: map[string]any{}}
	for _, test := range []struct {
		row  map[string]any
		want bool
	}{
		{map[string]any{"prospect_id": "physical", "business_id": "other", "centre_id": "c1"}, true},
		{map[string]any{"prospect_id": "other", "business_id": "business", "centre_id": "c1"}, true},
		{map[string]any{"prospect_id": "physical", "business_id": "business", "centre_id": "c2"}, false},
	} {
		got, err := eval.boolean(filter, relationScope{row: test.row})
		if err != nil || got != test.want {
			t.Fatalf("row=%v got=%v want=%v err=%v", test.row, got, test.want, err)
		}
	}
}

func TestRelationFilterMissingParentFailsClosed(t *testing.T) {
	filter := map[string]any{"eq": []any{map[string]any{"column": "owner_id"}, map[string]any{"parent": "missing.id"}}}
	eval := &relationEval{parent: map[string]any{}, args: map[string]any{}}
	got, err := eval.boolean(filter, relationScope{row: map[string]any{"owner_id": nil}})
	if err != nil || got {
		t.Fatalf("missing parent did not fail closed: got=%v err=%v", got, err)
	}
}

func TestRelationFilterValidationRejectsUnknownExistenceSource(t *testing.T) {
	filter := map[string]any{"exists": map[string]any{"source": "unknown", "correlate": []any{map[string]any{"outer": "id", "inner": "prospect_id"}}}}
	err := validateRelationFilter("search", map[string]any{"relation_filter": filter}, relationTestSources())
	if err == nil || !strings.Contains(err.Error(), "not an active Tables source") {
		t.Fatalf("unknown source accepted: %v", err)
	}
}

type relationTables struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu     sync.Mutex
	inputs []map[string]any
	rows   map[string][]map[string]any
}

func (p *relationTables) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.call(app, tool, input, out)
}

func (p *relationTables) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	return p.call(app, tool, input, out)
}

func (p *relationTables) call(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if app != "tables" || tool != "rows_search" {
		return fmt.Errorf("unexpected call %s.%s", app, tool)
	}
	copyInput := cloneMap(input)
	p.inputs = append(p.inputs, copyInput)
	table := fmt.Sprint(input["table"])
	rows := append([]map[string]any(nil), p.rows[table]...)
	if where, ok := input["where"].([]any); ok {
		filtered := rows[:0]
		for _, row := range rows {
			match := true
			for _, raw := range where {
				predicate := raw.(map[string]any)
				if predicate["op"] == "eq" && !relationEqual(row[fmt.Sprint(predicate["col"])], predicate["value"]) {
					match = false
				}
			}
			if match {
				filtered = append(filtered, row)
			}
		}
		rows = filtered
	}
	offset := 0
	if raw, ok := input["cursor"].(string); ok && raw != "" {
		_, _ = fmt.Sscanf(raw, "native:%d", &offset)
	}
	limit, ok := positiveInt(input["limit"])
	if !ok {
		limit = len(rows)
	}
	start := min(offset, len(rows))
	end := min(start+limit, len(rows))
	pageRows := make([]any, 0, end-start)
	for _, row := range rows[start:end] {
		pageRows = append(pageRows, row)
	}
	result := map[string]any{"rows": pageRows, "has_more": end < len(rows)}
	if end < len(rows) {
		result["next_cursor"] = fmt.Sprintf("native:%d", end)
	}
	encoded, _ := json.Marshal(result)
	return json.Unmarshal(encoded, out)
}

func TestRelationFilterExistsPaginationAndMandatoryFilters(t *testing.T) {
	platform := &relationTables{rows: map[string][]map[string]any{
		"prospects": {
			{"id": "p1", "tenant": "t1", "status": "open"},
			{"id": "p2", "tenant": "t2", "status": "open"},
			{"id": "p3", "tenant": "t1", "status": "closed"},
			{"id": "p4", "tenant": "t1", "status": "open"},
			{"id": "p5", "tenant": "t1", "status": "open"},
		},
		"calls": {
			{"id": "c1", "prospect_id": "p1", "status": "technical", "created_at": "2026-09-19"},
			{"id": "c2", "prospect_id": "p1", "status": "commercial", "created_at": "2026-09-20"},
			{"id": "c3", "prospect_id": "p4", "status": "commercial", "created_at": "2026-09-10"},
			{"id": "c4", "prospect_id": "p5", "status": "commercial", "created_at": "2026-09-21"},
		},
	}}
	filter := map[string]any{"and": []any{
		map[string]any{"neq": []any{map[string]any{"column": "status"}, map[string]any{"const": "closed"}}},
		map[string]any{"exists": map[string]any{
			"source":    "calls",
			"correlate": []any{map[string]any{"outer": "id", "inner": "prospect_id"}},
			"where": map[string]any{"and": []any{
				map[string]any{"eq": []any{map[string]any{"column": "status"}, map[string]any{"const": "commercial"}}},
				map[string]any{"gte": []any{map[string]any{"column": "created_at"}, map[string]any{"argument": "since"}}},
			}},
		}},
	}}
	sources := relationTestSources()
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: sources[0], 2: sources[1]}}
	a, _, base := standardApp(t, `type Query { unused: String }`, platform, bindings)
	ctx := context.WithValue(base, executionBindingsKey{}, bindings)
	config := map[string]any{
		"table": "prospects", "_project_id": "p1", "graphql_args": map[string]any{"since": "2026-09-18", "limit": 1, "where": map[string]any{"status": map[string]any{"eq": "open"}}},
		"where": []any{map[string]any{"col": "tenant", "op": "eq", "value": "t1"}}, "identity_where": []any{map[string]any{"col": "tenant", "op": "eq", "value": "t1"}},
		"relation_filter": filter, "include_total": true,
	}
	value, err := a.callTables(ctx, "search", config)
	if err != nil {
		t.Fatal(err)
	}
	page := value.(map[string]any)
	if page["total"] != 2 || page["has_more"] != true || len(page["rows"].([]any)) != 1 {
		t.Fatalf("unexpected first page: %#v", page)
	}
	firstID := page["rows"].([]any)[0].(map[string]any)["id"]
	if firstID != "p1" {
		t.Fatalf("unexpected first row: %#v", page)
	}
	config["graphql_args"] = map[string]any{"since": "2026-09-18", "limit": 1, "cursor": page["next_cursor"], "where": map[string]any{"status": map[string]any{"eq": "open"}}}
	value, err = a.callTables(ctx, "search", config)
	if err != nil {
		t.Fatal(err)
	}
	second := value.(map[string]any)
	if second["has_more"] != false || second["rows"].([]any)[0].(map[string]any)["id"] != "p5" {
		t.Fatalf("unexpected second page: %#v", second)
	}
	for _, input := range platform.inputs {
		if input["table"] == "prospects" {
			where, _ := input["where"].([]any)
			if len(where) < 3 { // configured + identity + client; no client can replace either mandatory clause
				t.Fatalf("mandatory predicates were lost: %#v", input)
			}
		}
	}
}

func TestRelationFilterNotExistsAndScanCap(t *testing.T) {
	platform := &relationTables{rows: map[string][]map[string]any{
		"prospects": {{"id": "p1"}, {"id": "p2"}},
		"calls":     {{"id": "c1", "prospect_id": "p1"}},
	}}
	sources := relationTestSources()
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: sources[0], 2: sources[1]}}
	a, _, base := standardApp(t, `type Query { unused: String }`, platform, bindings)
	ctx := context.WithValue(base, executionBindingsKey{}, bindings)
	filter := map[string]any{"not_exists": map[string]any{"source": "calls", "correlate": []any{map[string]any{"outer": "id", "inner": "prospect_id"}}}}
	config := map[string]any{"table": "prospects", "_project_id": "p1", "graphql_args": map[string]any{"limit": 10}, "relation_filter": filter}
	value, err := a.callTables(ctx, "list", config)
	if err != nil || !reflect.DeepEqual(value, []any{map[string]any{"id": "p2"}}) {
		t.Fatalf("not_exists result=%#v err=%v", value, err)
	}
	config["relation_filter"] = map[string]any{"or": []any{
		map[string]any{"eq": []any{map[string]any{"column": "id"}, map[string]any{"const": "never"}}},
		map[string]any{"eq": []any{map[string]any{"column": "id"}, map[string]any{"const": "also-never"}}},
	}}
	config["relation_scan_limit"] = 1
	_, err = a.callTables(ctx, "list", config)
	if errorCode(err) != "relation_filter_scan_limit_exceeded" {
		t.Fatalf("scan cap did not fail closed: %v (%s)", err, errorCode(err))
	}
}

func TestRelationFilterPushdownOnlyUsesResolvedANDTerms(t *testing.T) {
	eval := &relationEval{parent: map[string]any{"tenant": "t1"}, args: map[string]any{"since": "2026-01-01"}}
	filter := map[string]any{"and": []any{
		map[string]any{"eq": []any{map[string]any{"column": "tenant"}, map[string]any{"parent": "tenant"}}},
		map[string]any{"gte": []any{map[string]any{"column": "created_at"}, map[string]any{"argument": "since"}}},
		map[string]any{"or": []any{map[string]any{"eq": []any{map[string]any{"column": "a"}, map[string]any{"const": 1}}}, map[string]any{"eq": []any{map[string]any{"column": "b"}, map[string]any{"const": 2}}}}},
	}}
	pushed := eval.pushdown(filter, relationScope{})
	if len(pushed) != 2 {
		t.Fatalf("unsafe OR pushdown or missing safe terms: %#v", pushed)
	}
}

type nativeRelationTables struct {
	sdk.PlatformClient
	sdk.AppContextClient
	mu     sync.Mutex
	calls  []string
	inputs []map[string]any
}

func (p *nativeRelationTables) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.call(app, tool, input, out)
}

func (p *nativeRelationTables) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, out any) error {
	return p.call(app, tool, input, out)
}

func (p *nativeRelationTables) call(app, tool string, input map[string]any, out any) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if app != "tables" {
		return fmt.Errorf("unexpected app %s", app)
	}
	p.calls = append(p.calls, tool)
	p.inputs = append(p.inputs, cloneMap(input))
	var value any
	switch tool {
	case "tables_capabilities":
		value = map[string]any{"filter_ast": map[string]any{"supported": true, "version": "1"}}
	case "rows_search":
		if input["filter_ast"] == nil {
			return fmt.Errorf("native search is missing filter_ast")
		}
		value = map[string]any{"rows": []any{map[string]any{"id": "native"}}, "total": 1, "has_more": false}
	case "tables_batch":
		results := map[string]any{}
		for _, raw := range input["operations"].([]map[string]any) {
			args := raw["args"].(map[string]any)
			if args["filter_ast"] == nil {
				return fmt.Errorf("native batch operation is missing filter_ast")
			}
			results[raw["id"].(string)] = map[string]any{"status": "ok", "result": map[string]any{"rows": []any{map[string]any{"id": raw["id"]}}, "has_more": false}}
		}
		value = map[string]any{"results": results}
	default:
		return fmt.Errorf("unexpected tool %s", tool)
	}
	encoded, _ := json.Marshal(value)
	return json.Unmarshal(encoded, out)
}

func TestNativeRelationFilterCompilationAndSingleCall(t *testing.T) {
	platform := &nativeRelationTables{}
	sources := relationTestSources()
	sources[1].Config["where"] = []any{map[string]any{"col": "visible", "op": "eq", "value": true}}
	bindings := &executionBindings{sources: map[int64]sourceRecord{1: sources[0], 2: sources[1]}}
	a, _, base := standardApp(t, `type Query { unused: String }`, platform, bindings)
	ctx := context.WithValue(base, executionBindingsKey{}, bindings)
	filter := map[string]any{"and": []any{
		map[string]any{"or": []any{
			map[string]any{"eq": []any{map[string]any{"column": "physical_id"}, map[string]any{"parent": "id"}}},
			map[string]any{"eq": []any{map[string]any{"column": "business_id"}, map[string]any{"coalesce": []any{map[string]any{"parent": "missing"}, map[string]any{"parent": "business_id"}}}}},
		}},
		map[string]any{"exists": map[string]any{
			"source": "calls", "correlate": []any{map[string]any{"outer": "id", "inner": "prospect_id"}},
			"where": map[string]any{"gte": []any{map[string]any{"column": "created_at"}, map[string]any{"argument": "since"}}},
		}},
	}}
	config := map[string]any{
		"table": "prospects", "_project_id": "p1", "parent": map[string]any{"id": "p1", "business_id": "b1"},
		"graphql_args": map[string]any{"since": "2026-09-01", "limit": 5}, "relation_filter": filter,
	}
	value, err := a.callTables(ctx, "search", config)
	if err != nil {
		t.Fatal(err)
	}
	if value.(map[string]any)["total"] != float64(1) || fmt.Sprint(platform.calls) != "[tables_capabilities rows_search]" {
		t.Fatalf("value=%#v calls=%v", value, platform.calls)
	}
	native := platform.inputs[1]["filter_ast"].(map[string]any)
	encoded, _ := json.Marshal(native)
	text := string(encoded)
	for _, expected := range []string{`"literal":"p1"`, `"literal":"b1"`, `"table":"calls"`, `"outer":"id"`, `"inner":"prospect_id"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("native AST missing %s: %s", expected, text)
		}
	}
	exists := native["and"].([]any)[1].(map[string]any)["exists"].(map[string]any)
	if len(exists["where"].([]any)) != 1 {
		t.Fatalf("saved source scope was not retained: %#v", exists)
	}
}

func TestNativeRelationFiltersRetainTablesBatching(t *testing.T) {
	platform := &nativeRelationTables{}
	source := sourceRecord{ID: 1, Name: "events", Kind: "tables", Status: "active", Config: map[string]any{"table": "events"}}
	filter := map[string]any{"gte": []any{map[string]any{"column": "created_at"}, map[string]any{"argument": "since"}}}
	bindings := &executionBindings{
		sources:   map[int64]sourceRecord{1: source},
		resolvers: map[string]resolverRecord{"Query.page": {SourceID: 1, Operation: "search", Config: map[string]any{"relation_filter": filter}}},
	}
	_, schema, ctx := standardApp(t, `type Query { page(since:String!): Page! } type Page { rows:[Row!]! has_more:Boolean! } type Row { id:ID! }`, platform, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ a:page(since:"2026-01-01"){rows{id} has_more} b:page(since:"2026-02-01"){rows{id} has_more} }`})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	if fmt.Sprint(platform.calls) != "[tables_capabilities tables_batch]" {
		t.Fatalf("native filters did not retain one Tables batch: calls=%v inputs=%#v", platform.calls, platform.inputs)
	}
}

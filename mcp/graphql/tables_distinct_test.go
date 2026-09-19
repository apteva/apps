package main

import (
	"reflect"
	"testing"

	gql "github.com/graphql-go/graphql"
)

func TestTablesDistinctKeepsFirstOrderedRowPerTuple(t *testing.T) {
	plan, err := tablesDistinct("find", map[string]any{"distinct_by": []any{"offer_id", "sale_type"}, "distinct_defaults": map[string]any{"sale_type": "standard"}, "distinct_scan_limit": 500}, map[string]any{"first": 2})
	if err != nil {
		t.Fatal(err)
	}
	rows := []any{
		map[string]any{"id": 5, "offer_id": "a", "sale_type": "standard"},
		map[string]any{"id": 4, "offer_id": "a", "sale_type": nil},
		map[string]any{"id": 3, "offer_id": "a", "sale_type": "renewal"},
		map[string]any{"id": 2, "offer_id": "b", "sale_type": "standard"},
	}
	value, err := plan.apply(rows)
	if err != nil {
		t.Fatal(err)
	}
	got := value.([]any)
	if plan.scanLimit != 500 || len(got) != 2 || got[0].(map[string]any)["id"] != 5 || got[1].(map[string]any)["id"] != 3 {
		t.Fatalf("ordered distinct: plan=%+v rows=%#v", plan, got)
	}
	if _, err := tablesDistinct("search", map[string]any{"distinct_by": []any{"id"}}, nil); err == nil {
		t.Fatal("accepted distinct_by on an envelope whose total semantics would be ambiguous")
	}
}

func TestProjectionPushdownIncludesDistinctKeys(t *testing.T) {
	p := &standardTables{}
	bindings := &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "tables", Config: map[string]any{"table": "customers", "select": []any{"id", "name"}, "distinct_by": []any{"name"}}},
	}, resolvers: map[string]resolverRecord{
		"Query.customers": {SourceID: 1, Operation: "find"},
	}}
	_, schema, ctx := standardApp(t, `type Query { customers(first: Int = 1): [Customer!]! } type Customer { id: ID! name: String }`, p, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ customers { id } }`})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	args := p.inputs[0]
	if raw, ok := args["operations"].([]map[string]any); ok {
		args = raw[0]["args"].(map[string]any)
	}
	if !reflect.DeepEqual(args["select"], []any{"id", "name"}) || args["limit"] != 1000 {
		t.Fatalf("distinct projection/scan: %#v", args)
	}
}

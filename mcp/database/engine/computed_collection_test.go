package engine

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestComputedCollectionsThroughDatabase(t *testing.T) {
	root := t.TempDir()
	m, err := Open(root)
	checkErr(t, err)
	defer func() { m.Close() }()
	ctx := context.Background()
	execute := func(op string, r Request) (any, error) {
		if r.Database == "" {
			r.Database = "lab"
		}
		return m.Execute(ctx, "p", op, r)
	}
	must := func(op string, r Request) any {
		t.Helper()
		v, err := execute(op, r)
		checkErr(t, err)
		return v
	}
	expectCode := func(op string, r Request, code string) {
		t.Helper()
		_, err := execute(op, r)
		var got *Error
		if !errors.As(err, &got) || got.Code != code {
			t.Fatalf("%s: expected %s, got %v", op, code, err)
		}
	}

	must("database_create", Request{Adapter: "aptevadb"})
	must("collection_create", Request{Collection: "sales", Fields: []Field{
		{Name: "day", Type: "integer"}, {Name: "centre", Type: "text"},
		{Name: "paid", Type: "boolean"}, {Name: "amount", Type: "integer"},
		{Name: "agent", Type: "text", Nullable: true},
	}})
	const day = "9007199254740993"
	must("insert", Request{Collection: "sales", Records: []Record{
		{"id": "a", "day": day, "centre": "east", "paid": true, "amount": "40", "agent": "one"},
		{"id": "b", "day": day, "centre": "east", "paid": true, "amount": "20"},
		{"id": "c", "day": day, "centre": "east", "paid": false, "amount": "90"},
		{"id": "d", "day": "9007199254740992", "centre": "east", "paid": true, "amount": "99"},
	}})
	where := &Filter{Field: "paid", Op: "eq", Value: true}
	rollup := Request{Collection: "totals", ComputedCollection: &ComputedCollectionDefinition{
		Source: "sales", Where: where, GroupBy: []string{"day", "centre"},
		Metrics: []Metric{{Name: "total", Op: "sum", Field: "amount"}, {Name: "sales", Op: "count"}, {Name: "assigned", Op: "count", Field: "agent"}},
	}}
	schema := must("collection_create", rollup).(Collection)
	if !reflect.DeepEqual(schema.PrimaryKey, []string{"day", "centre"}) || schema.ComputedCollection.Source != "sales" {
		t.Fatal(schema)
	}
	if !reflect.DeepEqual(schema, must("collection_create", rollup)) {
		t.Fatal("repeated creation differs")
	}
	projection := Request{Collection: "paid_sales", ComputedCollection: &ComputedCollectionDefinition{Source: "sales", Where: where, Select: []string{"id", "amount"}}}
	must("collection_create", projection)
	must("index_create", Request{Collection: "totals", Index: &Index{Name: "by_centre", Fields: []Order{{Field: "centre"}}}})
	east := Record{"day": day, "centre": "east"}
	west := Record{"day": day, "centre": "west"}
	point := func(collection string, key Record) Operation {
		return Operation{Op: "get", Request: Request{Collection: collection, Key: key}}
	}
	row := func(result any) Record {
		t.Helper()
		v := result.(map[string]any)
		if v["found"] != true {
			t.Fatal(v)
		}
		return v["record"].(Record)
	}
	if got := row(must("get", point("totals", east).Request)); !reflect.DeepEqual(got, Record{"day": day, "centre": "east", "total": "60", "sales": "2", "assigned": "1"}) {
		t.Fatal(got)
	}
	find := Request{Collection: "totals", Query: Query{Where: &Filter{Field: "centre", Op: "eq", Value: "east"}, Limit: 1, RequireIndex: true}}
	page := must("find", find).(map[string]any)
	if got := page["records"].([]Record)[0]["day"]; got != "9007199254740992" {
		t.Fatal(got)
	}
	find.Cursor = page["nextCursor"].(string)
	if got := must("find", find).(map[string]any)["records"].([]Record)[0]["day"]; got != day {
		t.Fatal(got)
	}

	expectCode("update", Request{Collection: "totals", Key: east, Set: Record{"total": "0"}}, "read_only")
	expectCode("collection_drop", Request{Collection: "sales", Confirm: true}, "schema_conflict")
	expectCode("collection_create", Request{Collection: "chain", ComputedCollection: &ComputedCollectionDefinition{Source: "totals"}}, "invalid_argument")
	expectCode("collection_create", Request{Collection: "unique_sales", ComputedCollection: &ComputedCollectionDefinition{
		Source: "sales", GroupBy: []string{"day"}, Metrics: []Metric{{Name: "n", Op: "count_distinct", Field: "agent"}},
	}}, "invalid_argument")
	must("batch", Request{Operations: []Operation{
		{Op: "update", Request: Request{Collection: "sales", Key: Record{"id": "a"}, Set: Record{"centre": "west"}}},
		{Op: "update", Request: Request{Collection: "sales", Key: Record{"id": "b"}, Set: Record{"paid": false}}},
	}})
	batch := must("batch", Request{Operations: []Operation{point("totals", east), point("totals", west), point("paid_sales", Record{"id": "b"})}}).(map[string]any)["results"].([]any)
	if batch[0].(map[string]any)["found"] != false || row(batch[1])["total"] != "40" || batch[2].(map[string]any)["found"] != false {
		t.Fatal(batch)
	}
	agg := must("aggregate", Request{Collection: "totals", Query: Query{Where: &Filter{Field: "day", Op: "eq", Value: day}}, Metrics: []Metric{{Name: "revenue", Op: "sum", Field: "total"}}}).(map[string]any)
	if agg["rows"].([]Record)[0]["revenue"] != "40" {
		t.Fatal(agg)
	}
	checkErr(t, m.Close())
	m, err = Open(root)
	checkErr(t, err)
	if got := row(must("get", point("totals", west).Request)); got["total"] != "40" {
		t.Fatal(got)
	}
	for _, name := range []string{"totals", "paid_sales", "sales"} {
		must("collection_drop", Request{Collection: name, Confirm: true})
	}
	for _, adapter := range []string{"sqlite", "pebble"} {
		must("database_create", Request{Database: adapter, Adapter: adapter})
		other := rollup
		other.Database = adapter
		expectCode("collection_create", other, "unsupported_capability")
	}
}

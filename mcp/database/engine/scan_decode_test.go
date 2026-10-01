package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestScanDecoderValuesAndOwnership(t *testing.T) {
	c, e := newCollection(Request{Collection: "items", Fields: []Field{
		{"text", "text", true}, {"n", "number", true}, {"integer", "integer", true},
		{"flag", "boolean", true}, {"date", "datetime", true}, {"data", "json", true},
	}})
	if e != nil {
		t.Fatal(e)
	}
	d := newScanDecoder(c, nil, nil)
	records := []Record{
		{"id": "a", "text": "a\x00\"\\\n雪😀", "n": math.SmallestNonzeroFloat64, "integer": "9223372036854775807", "flag": true, "date": "2026-09-07T00:00:00.000000Z", "data": map[string]any{"n": json.Number("9007199254740993"), "items": []any{nil, "é", false, json.Number("1.25")}}},
		{"id": "b", "text": "different", "n": -1e100, "integer": "-9223372036854775808", "flag": false, "date": nil, "data": []any{1., "different"}},
		{"id": "c", "text": nil, "n": nil, "integer": nil, "flag": nil, "date": nil, "data": nil},
	}
	retained := []Record{}
	for _, record := range records {
		record["_created_at"] = "2026-09-07T00:00:00.000000Z"
		record["_updated_at"] = "2026-09-07T00:00:00.000000Z"
		record["_version"] = "1"
		row, e := d.decode(marshal(record))
		if e != nil {
			t.Fatal(e)
		}
		if !equalJSON(row, record) {
			t.Fatalf("decoded %s, want %s", marshal(row), marshal(record))
		}
		retained = append(retained, retainSelected(row, Query{}))
	}
	// A parser reset and reused row map must never change already retained data.
	for i := range records {
		if !equalJSON(retained[i], records[i]) {
			t.Fatal("retained row changed", i)
		}
	}
	projected := newScanDecoder(c, []string{"id", "data"}, &Filter{Or: []Filter{{Field: "n", Op: "gt", Value: 0.}, {Field: "text", Op: "is_null"}}})
	row, e := projected.decode(marshal(records[0]))
	if e != nil || len(row) != 4 || !equalJSON(row["data"], records[0]["data"]) {
		t.Fatal(row, e)
	}
	for _, bad := range []string{`{`, `[]`, `null`, `{"id":false}`, `{"id":"a","n":"wrong"}`, `{"id":"a","n":1e9999}`} {
		if _, e := d.decode([]byte(bad)); e == nil {
			t.Fatal("corrupt record accepted", bad)
		}
	}
	// Preserve the existing contract for valid JSON deeper than fastjson's
	// parser limit, including exact numbers inside the selected JSON field.
	deep := []byte(`{"id":"deep","data":` + strings.Repeat("[", 400) + `9007199254740993` + strings.Repeat("]", 400) + `}`)
	full := newScanDecoder(c, []string{"id", "data"}, nil)
	row, e = full.decode(deep)
	var want Record
	if e != nil || decode(deep, &want) != nil || !equalJSON(row, want) {
		t.Fatal("deep JSON compatibility", e)
	}
}

func TestScanIteratorKeysAndPagination(t *testing.T) {
	m := manager(t)
	for _, adapter := range []string{"sqlite", "pebble"} {
		call(t, m, "p", "database_create", Request{Database: adapter, Adapter: adapter})
		call(t, m, "p", "collection_create", Request{Database: adapter, Collection: "items", PrimaryKey: []string{"n", "label"}, Fields: []Field{{"n", "number", false}, {"label", "text", false}, {"rank", "integer", true}, {"data", "json", true}}})
		records := []Record{}
		for i, n := range []float64{-100, -10, -2, -1, 0, 1, 2, 10, 100} {
			for j, label := range []string{"a", "a\x00", "a\"", "\\", "é", "雪"} {
				r := Record{"n": n, "label": label, "rank": fmt.Sprint(100 - i*6 - j), "data": map[string]any{"exact": json.Number("9007199254740993"), "value": n}}
				if j%3 == 0 {
					r["rank"] = nil
				}
				records = append(records, r)
			}
		}
		call(t, m, "p", "insert", Request{Database: adapter, Collection: "items", Records: records})
		call(t, m, "p", "index_create", Request{Database: adapter, Collection: "items", Index: &Index{Name: "by_rank", Fields: []Order{{"rank", "desc"}}}})
	}
	for _, q := range []Query{
		{Limit: 7},
		{OrderBy: []Order{{"n", "desc"}}, Limit: 5},
		{OrderBy: []Order{{"label", "desc"}}, Limit: 8},
		{Where: &Filter{Field: "rank", Op: "gte", Value: "60"}, OrderBy: []Order{{"rank", "desc"}}, Limit: 3, RequireIndex: true},
		{Where: &Filter{Or: []Filter{{Field: "rank", Op: "is_null"}, {Field: "n", Op: "lt", Value: 0.}}}, OrderBy: []Order{{"label", "desc"}, {"n", "desc"}}, Limit: 6},
	} {
		var expected []Record
		for _, adapter := range []string{"sqlite", "pebble"} {
			query := q
			var got []Record
			for page := 0; ; page++ {
				if page > 100 {
					t.Fatal("pagination did not end")
				}
				out := call(t, m, "p", "find", Request{Database: adapter, Collection: "items", Query: query}).(map[string]any)
				for _, row := range out["records"].([]Record) {
					delete(row, "_created_at")
					delete(row, "_updated_at")
					delete(row, "_version")
					got = append(got, row)
				}
				if !out["hasMore"].(bool) {
					break
				}
				query.Cursor = out["nextCursor"].(string)
			}
			if adapter == "sqlite" {
				expected = got
			} else if !equalJSON(got, expected) {
				t.Fatalf("pagination mismatch for %+v\ngot %s\nwant %s", q, marshal(got), marshal(expected))
			}
		}
	}
	for _, groupBy := range [][]string{nil, {"n"}, {"rank"}, {"label", "n"}} {
		for _, where := range []*Filter{nil, {Field: "n", Op: "gt", Value: 1000.}} {
			r := Request{Collection: "items", GroupBy: groupBy, Query: Query{Where: where}, Metrics: []Metric{
				{"rows", "count", ""}, {"ranked", "count", "rank"}, {"documents", "count", "data"},
				{"total", "sum", "rank"}, {"average", "avg", "rank"}, {"lowest", "min", "rank"}, {"highest", "max", "rank"},
				{"numeric_total", "sum", "n"}, {"numeric_average", "avg", "n"}, {"first_label", "min", "label"}, {"last_label", "max", "label"},
			}}
			r.Database = "sqlite"
			want := call(t, m, "p", "aggregate", r)
			r.Database = "pebble"
			got := call(t, m, "p", "aggregate", r)
			if !equalJSON(got, want) {
				t.Fatalf("aggregate mismatch for %v: got %s, want %s", groupBy, marshal(got), marshal(want))
			}
		}
	}
	// Ensure an index referencing a missing record errors instead of returning a
	// neighbouring record from the reusable iterator.
	d, e := m.resolve(context.Background(), "p", "pebble", "", false)
	if e != nil {
		t.Fatal(e)
	}
	e = d.backend.transaction(context.Background(), true, func(tx transaction) error {
		return tx.(*pebbleTx).batch.Delete([]byte(`r/items/[-100,"a"]`), nil)
	})
	if e != nil {
		t.Fatal(e)
	}
	_, e = m.Execute(context.Background(), "p", "find", Request{Database: "pebble", Collection: "items"})
	expectCode(t, e, "storage_error")
}

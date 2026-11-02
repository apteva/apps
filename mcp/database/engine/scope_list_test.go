package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// A one-value in list (how callers pass a single authorised scope) is
// equality: same results as eq on every adapter, in every place a filter is
// accepted, and it binds the leading field of a compound index.
func TestSingleValueInIsEquality(t *testing.T) {
	m := manager(t)
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		call(t, m, "p", "database_create", Request{Database: adapter, Adapter: adapter})
		call(t, m, "p", "collection_create", Request{Database: adapter, Collection: "inbound", Fields: []Field{{"id", "integer", false}, {"scope_id", "text", false}, {"day", "text", false}, {"commercial_id", "text", true}, {"caller", "text", false}, {"answered", "boolean", false}}})
		call(t, m, "p", "index_create", Request{Database: adapter, Collection: "inbound", Index: &Index{Name: "scope_day", Fields: []Order{{"scope_id", "asc"}, {"day", "asc"}}}})
		var records []Record
		for i := 0; i < 600; i++ {
			r := Record{"id": strconv.Itoa(i), "scope_id": fmt.Sprint("s", i%3), "day": fmt.Sprintf("2026-10-%02d", 24+i%4), "caller": fmt.Sprint("c", i%37), "answered": i%5 != 0}
			if i%7 != 0 {
				r["commercial_id"] = fmt.Sprint("a", i%4)
			}
			records = append(records, r)
		}
		call(t, m, "p", "insert", Request{Database: adapter, Collection: "inbound", Records: records})
		scope := func(list bool, v string) Filter {
			if list {
				return Filter{Field: "scope_id", Op: "in", Value: []any{v}}
			}
			return Filter{Field: "scope_id", Op: "eq", Value: v}
		}
		day := []Filter{{Field: "day", Op: "gte", Value: "2026-10-25"}, {Field: "day", Op: "lt", Value: "2026-10-26"}}
		answered := &Filter{Field: "answered", Op: "eq", Value: true}
		requests := func(list bool) []Operation {
			where := &Filter{And: append([]Filter{scope(list, "s1")}, day...)}
			nested := &Filter{And: []Filter{{And: []Filter{scope(list, "s2")}}, {And: day}, {Field: "commercial_id", Op: "is_not_null"}}}
			metricScope := scope(list, "s0")
			base := Request{Database: adapter, Collection: "inbound"}
			grouped, total, distinct, nest, metric, find, count := base, base, base, base, base, base, base
			grouped.Query, grouped.GroupBy, grouped.Metrics = Query{Where: where, RequireIndex: true}, []string{"commercial_id"}, []Metric{{"received", "count", "", nil}, {"answered", "count", "", answered}}
			total.Query, total.Metrics = Query{Where: where, RequireIndex: true}, []Metric{{"received", "count", "", nil}}
			distinct.Query, distinct.Metrics = Query{Where: where}, []Metric{{"callers", "count_distinct", "caller", nil}, {"answered_callers", "count_distinct", "caller", answered}}
			nest.Query, nest.GroupBy, nest.Metrics = Query{Where: nested, RequireIndex: true}, []string{"commercial_id"}, []Metric{{"received", "count", "", nil}}
			metric.Metrics = []Metric{{"s0", "count", "", &metricScope}, {"all", "count", "", nil}}
			find.Query = Query{Where: where, OrderBy: []Order{{"id", "asc"}}, Limit: 1000, RequireIndex: true}
			count.Query = Query{Where: where, RequireIndex: true}
			return []Operation{{"aggregate", grouped}, {"aggregate", total}, {"aggregate", distinct}, {"aggregate", nest}, {"aggregate", metric}, {"find", find}, {"count", count}}
		}
		got := call(t, m, "p", "batch", Request{Database: adapter, Operations: requests(true)})
		want := call(t, m, "p", "batch", Request{Database: adapter, Operations: requests(false)})
		g, w := string(marshal(got)), string(marshal(want))
		if g != w {
			t.Fatalf("%s: in and eq differ\n in %s\n eq %s", adapter, g, w)
		}
		if !strings.Contains(w, `"received"`) || strings.Count(w, `"id"`) == 0 {
			t.Fatalf("%s: empty fixture results %s", adapter, w)
		}
		// several values keep list semantics
		several := Request{Database: adapter, Collection: "inbound", Query: Query{Where: &Filter{Field: "scope_id", Op: "in", Value: []any{"s1", "s2"}}}, Metrics: []Metric{{"n", "count", "", nil}}}
		var out struct{ Rows []map[string]any }
		if e := json.Unmarshal(marshal(call(t, m, "p", "aggregate", several)), &out); e != nil || len(out.Rows) != 1 || fmt.Sprint(out.Rows[0]["n"]) != "400" {
			t.Fatalf("%s: two-value in counted %v (%v)", adapter, out.Rows, e)
		}
	}
}

// AptevaDB builds indexes natively on populated collections without first
// retaining their records, and reports the size cap and uniqueness clearly.
func TestAptevaIndexBuildOnPopulatedCollection(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "n", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "n", Collection: "wide", Fields: []Field{{"id", "integer", false}, {"day", "text", false}, {"dup", "text", false}, {"body", "text", false}}})
	body := strings.Repeat("x", 2000)
	for base := 0; base < 12000; base += MaxRecords {
		var records []Record
		for i := base; i < base+MaxRecords; i++ {
			records = append(records, Record{"id": strconv.Itoa(i), "day": fmt.Sprintf("2026-10-%02d", i%30+1), "dup": fmt.Sprint("d", i%10), "body": body})
		}
		call(t, m, "p", "insert", Request{Database: "n", Collection: "wide", Records: records})
	}
	// 12,000 records of 2 KB exceed both the old 10,000-record cap and the
	// 16 MiB retained-result limit the old pre-scan hit first
	call(t, m, "p", "index_create", Request{Database: "n", Collection: "wide", Index: &Index{Name: "by_day", Fields: []Order{{"day", "asc"}}}})
	n := call(t, m, "p", "count", Request{Database: "n", Collection: "wide", Query: Query{Where: &Filter{Field: "day", Op: "eq", Value: "2026-10-05"}, RequireIndex: true}})
	if !strings.Contains(string(marshal(n)), "400") {
		t.Fatalf("indexed count %s", marshal(n))
	}
	_, e := m.Execute(context.Background(), "p", "index_create", Request{Database: "n", Collection: "wide", Index: &Index{Name: "dup_unique", Fields: []Order{{"dup", "asc"}}, Unique: true}})
	expectCode(t, e, "unique_conflict")
	listed := string(marshal(call(t, m, "p", "indexes_list", Request{Database: "n", Collection: "wide"})))
	if strings.Contains(listed, "dup_unique") || !strings.Contains(listed, "by_day") {
		t.Fatalf("indexes after a failed unique build: %s", listed)
	}
	call(t, m, "p", "insert", Request{Database: "n", Collection: "wide", Records: []Record{{"id": "12000", "day": "2026-10-05", "dup": "d0", "body": "y"}}})
	old := nativeIndexBuildLimit
	nativeIndexBuildLimit = 5000
	defer func() { nativeIndexBuildLimit = old }()
	_, e = m.Execute(context.Background(), "p", "index_create", Request{Database: "n", Collection: "wide", Index: &Index{Name: "by_dup", Fields: []Order{{"dup", "asc"}}}})
	expectCode(t, e, "resource_limit")
	if !strings.Contains(e.Error(), "exceeds 5000 records (collection has 12001)") {
		t.Fatalf("cap message %v", e)
	}
}

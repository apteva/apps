package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"testing"
	"time"
)

func manager(t *testing.T) *Manager {
	t.Helper()
	m, e := Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		if e := m.Close(); e != nil {
			t.Error(e)
		}
	})
	return m
}
func call(t *testing.T, m *Manager, scope, op string, r Request) any {
	t.Helper()
	v, e := m.Execute(context.Background(), scope, op, r)
	if e != nil {
		t.Fatalf("%s: %v", op, e)
	}
	return v
}
func expectCode(t *testing.T, e error, code string) {
	t.Helper()
	var v *Error
	if !errors.As(e, &v) || v.Code != code {
		t.Fatalf("wanted %s, got %v", code, e)
	}
}
func fixture(t *testing.T, m *Manager, adapter string) {
	t.Helper()
	call(t, m, "p", "database_create", Request{Database: "shop", Adapter: adapter})
	call(t, m, "p", "collection_create", Request{Database: "shop", Collection: "orders", Fields: []Field{{"id", "text", false}, {"country", "text", true}, {"email", "text", true}, {"amount", "number", true}, {"units", "integer", true}, {"active", "boolean", false}}})
	call(t, m, "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{
		{"id": "a", "country": "es", "email": "a@example.com", "amount": 10., "units": "2", "active": true},
		{"id": "b", "country": "es", "email": "b@example.com", "amount": 20., "units": "3", "active": true},
		{"id": "c", "country": "fr", "email": nil, "amount": 30., "units": "4", "active": false},
		{"id": "d", "country": nil, "email": nil, "amount": nil, "units": nil, "active": false},
	}})
}
func rows(t *testing.T, v any) []Record { t.Helper(); return v.(map[string]any)["records"].([]Record) }
func TestAdapters(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			t.Run("aggregate", func(t *testing.T) {
				r := Request{Database: "shop", Collection: "orders", GroupBy: []string{"country"}, Metrics: []Metric{{"orders", "count", ""}, {"priced", "count", "amount"}, {"revenue", "sum", "amount"}, {"average", "avg", "amount"}, {"min", "min", "amount"}, {"max", "max", "amount"}, {"units", "sum", "units"}}}
				out := call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
				if len(out) != 3 {
					t.Fatal(out)
				}
				if out[0]["country"] != nil || out[0]["orders"] != "1" || out[0]["priced"] != "0" || out[0]["revenue"] != nil {
					t.Fatal(out[0])
				}
				if out[1]["revenue"] != 30. || out[1]["average"] != 15. || out[1]["units"] != "5" || out[1]["orders"] != "2" {
					t.Fatal(out[1])
				}
				r.GroupBy = nil
				r.Where = &Filter{Field: "country", Op: "eq", Value: "absent"}
				out = call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
				if len(out) != 1 || out[0]["orders"] != "0" || out[0]["revenue"] != nil {
					t.Fatal(out)
				}
				r.GroupBy = []string{"country"}
				r.Where = nil
				r.Limit = 1
				r.OrderBy = []Order{{"revenue", "desc"}}
				out = call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
				if len(out) != 1 || out[0]["country"] != "es" {
					t.Fatal(out)
				}
			})
			t.Run("indexes_and_atomicity", func(t *testing.T) {
				call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "by_email", Fields: []Order{{"email", "asc"}}, Unique: true}})
				call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "by_country_amount", Fields: []Order{{"country", "asc"}, {"amount", "desc"}}}})
				v := call(t, m, "p", "find", Request{Database: "shop", Collection: "orders", Query: Query{Where: &Filter{Field: "email", Op: "eq", Value: "a@example.com"}, RequireIndex: true}})
				if len(rows(t, v)) != 1 {
					t.Fatal(v)
				}
				_, e := m.Execute(context.Background(), "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": "e", "active": false}, {"id": "f", "email": "a@example.com", "active": true}}})
				expectCode(t, e, "unique_conflict")
				v = call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "e"}})
				if v.(map[string]any)["found"].(bool) {
					t.Fatal("failed insert did not roll back")
				}
				call(t, m, "p", "update", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}, Set: Record{"email": "changed@example.com"}, Increment: Record{"units": "1"}, IfVersion: 1})
				v = call(t, m, "p", "find", Request{Database: "shop", Collection: "orders", Query: Query{Where: &Filter{Field: "email", Op: "eq", Value: "a@example.com"}, RequireIndex: true}})
				if len(rows(t, v)) != 0 {
					t.Fatal("stale index", v)
				}
				_, e = m.Execute(context.Background(), "p", "update", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}, Set: Record{"active": false}, IfVersion: 1})
				expectCode(t, e, "version_conflict")
				call(t, m, "p", "upsert", Request{Database: "shop", Collection: "orders", ConflictIndex: "by_email", Records: []Record{{"email": "changed@example.com", "amount": 12.}}})
				v = call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}})
				rec := v.(map[string]any)["record"].(Record)
				if number(rec["amount"]) != 12 || rec["units"] != "3" {
					t.Fatal(rec)
				}
				_, e = m.Execute(context.Background(), "p", "delete", Request{Database: "shop", Collection: "orders", All: true, MaxAffected: 1})
				expectCode(t, e, "resource_limit")
				if call(t, m, "p", "count", Request{Database: "shop", Collection: "orders"}).(map[string]any)["count"] != "4" {
					t.Fatal("partial delete")
				}
			})
			t.Run("pagination_and_filters", func(t *testing.T) {
				r := Request{Database: "shop", Collection: "orders", Query: Query{OrderBy: []Order{{"amount", "desc"}}, Limit: 1, Select: []string{"id"}}}
				ids := []string{}
				for i := 0; i < 6; i++ {
					v := call(t, m, "p", "find", r).(map[string]any)
					for _, rec := range v["records"].([]Record) {
						ids = append(ids, rec["id"].(string))
					}
					if !v["hasMore"].(bool) {
						break
					}
					r.Cursor = v["nextCursor"].(string)
				}
				if strings.Join(ids, ",") != "c,b,a,d" {
					t.Fatal(ids)
				}
				r.Cursor = "tampered"
				_, e := m.Execute(context.Background(), "p", "find", r)
				expectCode(t, e, "invalid_argument")
				r.Query = Query{Where: &Filter{And: []Filter{{Field: "country", Op: "in", Value: []any{"es", "fr"}}, {Or: []Filter{{Field: "amount", Op: "gte", Value: 20.}, {Field: "email", Op: "starts_with", Value: "changed"}}}}}}
				v := call(t, m, "p", "find", r)
				if len(rows(t, v)) != 3 {
					t.Fatal(v)
				}
			})
			t.Run("multiple_collections_and_batch", func(t *testing.T) {
				call(t, m, "p", "collection_create", Request{Database: "shop", Collection: "audit", Fields: []Field{{"note", "text", false}}})
				_, e := m.Execute(context.Background(), "p", "batch", Request{Database: "shop", Operations: []Operation{{"insert", Request{Collection: "audit", Records: []Record{{"note": "must roll back"}}}}, {"insert", Request{Collection: "orders", Records: []Record{{"id": "a", "active": true}}}}}})
				expectCode(t, e, "unique_conflict")
				v := call(t, m, "p", "count", Request{Database: "shop", Collection: "audit"})
				if v.(map[string]any)["count"] != "0" {
					t.Fatal("cross-collection batch did not roll back", v)
				}
				call(t, m, "p", "batch", Request{Database: "shop", Operations: []Operation{{"insert", Request{Collection: "audit", Records: []Record{{"note": "committed"}}}}, {"update", Request{Collection: "orders", Key: Record{"id": "b"}, Set: Record{"active": false}}}}})
			})
			t.Run("failed_unique_build", func(t *testing.T) {
				_, e := m.Execute(context.Background(), "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "bad_unique", Fields: []Order{{"country", "asc"}}, Unique: true}})
				expectCode(t, e, "unique_conflict")
				idx := call(t, m, "p", "indexes_list", Request{Database: "shop", Collection: "orders"}).([]Index)
				for _, i := range idx {
					if i.Name == "bad_unique" {
						t.Fatal("failed index published")
					}
				}
			})
		})
	}
}
func TestDatabaseAndScopeIsolation(t *testing.T) {
	m := manager(t)
	for _, scope := range []string{"p", "q"} {
		for _, name := range []string{"one", "two"} {
			adapter := "sqlite"
			if name == "two" {
				adapter = "pebble"
			}
			call(t, m, scope, "database_create", Request{Database: name, Adapter: adapter})
			call(t, m, scope, "collection_create", Request{Database: name, Collection: "items", Fields: []Field{{"value", "text", false}}})
			call(t, m, scope, "insert", Request{Database: name, Collection: "items", Records: []Record{{"id": "same", "value": scope + name}}})
		}
	}
	for _, scope := range []string{"p", "q"} {
		dbs := call(t, m, scope, "databases_list", Request{}).([]DatabaseInfo)
		if len(dbs) != 2 {
			t.Fatal(dbs)
		}
		for _, name := range []string{"one", "two"} {
			v := call(t, m, scope, "get", Request{Database: name, Collection: "items", Key: Record{"id": "same"}}).(map[string]any)["record"].(Record)
			if v["value"] != scope+name {
				t.Fatal("leaked data", v)
			}
		}
	}
	call(t, m, "p", "database_drop", Request{Database: "one", Confirm: true})
	_, e := m.Execute(context.Background(), "p", "collection_describe", Request{Database: "one", Collection: "items"})
	expectCode(t, e, "not_found")
	call(t, m, "q", "collection_describe", Request{Database: "one", Collection: "items"})
}
func TestRestart(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			root := t.TempDir()
			m, e := Open(root)
			if e != nil {
				t.Fatal(e)
			}
			fixture(t, m, adapter)
			call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "email", Unique: true, Fields: []Order{{"email", "asc"}}}})
			q := Request{Database: "shop", Collection: "orders", Query: Query{Limit: 1}}
			v := call(t, m, "p", "find", q).(map[string]any)
			q.Cursor = v["nextCursor"].(string)
			if e = m.Close(); e != nil {
				t.Fatal(e)
			}
			m, e = Open(root)
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			next := rows(t, call(t, m, "p", "find", q))
			if len(next) != 1 || next[0]["id"] != "b" {
				t.Fatal(next)
			}
			if len(call(t, m, "p", "indexes_list", Request{Database: "shop", Collection: "orders"}).([]Index)) != 1 {
				t.Fatal("index lost")
			}
		})
	}
}
func TestConcurrentUniqueInsert(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "email", Unique: true, Fields: []Order{{"email", "asc"}}}})
			var wg sync.WaitGroup
			var mu sync.Mutex
			success := 0
			for i := 0; i < 12; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					_, e := m.Execute(context.Background(), "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": fmt.Sprint(i), "email": "race@example.com", "active": true}}})
					if e == nil {
						mu.Lock()
						success++
						mu.Unlock()
					} else {
						var x *Error
						if !errors.As(e, &x) || x.Code != "unique_conflict" {
							t.Error(e)
						}
					}
				}(i)
			}
			wg.Wait()
			if success != 1 {
				t.Fatalf("%d writers committed the same unique value", success)
			}
		})
	}
}
func TestIntegerAndCompositeKeys(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			call(t, m, "p", "database_create", Request{Database: "n", Adapter: adapter})
			call(t, m, "p", "collection_create", Request{Database: "n", Collection: "numbers", PrimaryKey: []string{"tenant", "n"}, Fields: []Field{{"tenant", "text", false}, {"n", "integer", false}}})
			for _, n := range []string{"-9223372036854775808", "-10", "0", "10", "9223372036854775807"} {
				call(t, m, "p", "insert", Request{Database: "n", Collection: "numbers", Records: []Record{{"tenant": "t", "n": n}}})
			}
			r := Request{Database: "n", Collection: "numbers", Query: Query{Where: &Filter{And: []Filter{{Field: "tenant", Op: "eq", Value: "t"}, {Field: "n", Op: "gt", Value: "0"}}}, OrderBy: []Order{{"n", "desc"}}, RequireIndex: true}}
			v := rows(t, call(t, m, "p", "find", r))
			if len(v) != 2 || v[0]["n"] != "9223372036854775807" {
				t.Fatal(v)
			}
			_, e := m.Execute(context.Background(), "p", "insert", Request{Database: "n", Collection: "numbers", Records: []Record{{"tenant": "t", "n": float64(math.MaxInt64)}}})
			expectCode(t, e, "invalid_argument")
		})
	}
}
func TestDifferentialQueries(t *testing.T) {
	m := manager(t)
	for _, a := range []string{"sqlite", "pebble"} {
		call(t, m, "p", "database_create", Request{Database: a, Adapter: a})
		call(t, m, "p", "collection_create", Request{Database: a, Collection: "data", Fields: []Field{{"n", "number", true}, {"label", "text", true}}})
		rs := []Record{}
		for i := 0; i < 100; i++ {
			r := Record{"id": fmt.Sprintf("r%03d", i), "n": float64(i%17 - 8), "label": fmt.Sprint(i % 5)}
			if i%13 == 0 {
				r["n"] = nil
			}
			if i%11 == 0 {
				r["label"] = nil
			}
			rs = append(rs, r)
		}
		call(t, m, "p", "insert", Request{Database: a, Collection: "data", Records: rs})
		call(t, m, "p", "index_create", Request{Database: a, Collection: "data", Index: &Index{Name: "compound", Fields: []Order{{"label", "asc"}, {"n", "desc"}}}})
	}
	for _, op := range []string{"eq", "ne", "lt", "lte", "gt", "gte"} {
		for _, direction := range []string{"asc", "desc"} {
			for _, value := range []float64{-9, -1, 0, 1, 9} {
				q := Query{Where: &Filter{And: []Filter{{Field: "label", Op: "eq", Value: "2"}, {Field: "n", Op: op, Value: value}}}, OrderBy: []Order{{"n", direction}}, Select: []string{"id", "n", "label"}, Limit: 100}
				a := call(t, m, "p", "find", Request{Database: "sqlite", Collection: "data", Query: q})
				b := call(t, m, "p", "find", Request{Database: "pebble", Collection: "data", Query: q})
				aj, _ := json.Marshal(a)
				bj, _ := json.Marshal(b)
				if string(aj) != string(bj) {
					t.Fatalf("%s %v %s\nsqlite %s\npebble %s", op, value, direction, aj, bj)
				}
			}
		}
	}
}

func TestAggregateWithIndexAndPrefix(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "email", Fields: []Order{{"email", "desc"}}}})
			r := Request{Database: "shop", Collection: "orders", Query: Query{Where: &Filter{Field: "email", Op: "starts_with", Value: "a@"}, RequireIndex: true, OrderBy: []Order{{"total", "desc"}}}, Metrics: []Metric{{"total", "sum", "amount"}}}
			v := call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
			if len(v) != 1 || v[0]["total"] != 10. {
				t.Fatal(v)
			}
		})
	}
}

func TestPebbleLargeScanAndCursorSeek(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "large", Adapter: "pebble"})
	call(t, m, "p", "collection_create", Request{Database: "large", Collection: "items", Fields: []Field{{"n", "number", false}, {"pad", "text", true}}})
	for batch := 0; batch < 11; batch++ {
		records := []Record{}
		for j := 0; j < 1000; j++ {
			i := batch*1000 + j
			records = append(records, Record{"id": fmt.Sprintf("r%05d", i), "n": float64(i), "pad": strings.Repeat("x", 2048)})
		}
		call(t, m, "p", "insert", Request{Database: "large", Collection: "items", Records: records})
	}
	aggregate := call(t, m, "p", "aggregate", Request{Database: "large", Collection: "items", Metrics: []Metric{{"total", "sum", "n"}}}).(map[string]any)["rows"].([]Record)
	if len(aggregate) != 1 || number(aggregate[0]["total"]) != float64(10999*11000/2) {
		t.Fatal(aggregate)
	}
	// Read more than both previous limits: 10k candidates and 16 MiB input.
	late := rows(t, call(t, m, "p", "find", Request{Database: "large", Collection: "items", Query: Query{Where: &Filter{Field: "n", Op: "eq", Value: 10999.}, Limit: 1}}))
	if len(late) != 1 || late[0]["id"] != "r10999" {
		t.Fatal(late)
	}
	if call(t, m, "p", "count", Request{Database: "large", Collection: "items"}).(map[string]any)["count"] != "11000" {
		t.Fatal("large count mismatch")
	}
	groups := call(t, m, "p", "aggregate", Request{Database: "large", Collection: "items", GroupBy: []string{"n"}, Metrics: []Metric{{"count", "count", ""}}, Query: Query{OrderBy: []Order{{"n", "desc"}}, Limit: 1}}).(map[string]any)
	if !groups["truncated"].(bool) || number(groups["rows"].([]Record)[0]["n"]) != 10999 {
		t.Fatal(groups)
	}
	// A non-indexed sort keeps the top page, not all large matching records.
	query := Query{Select: []string{"id"}, OrderBy: []Order{{"n", "desc"}}, Limit: 3}
	first := call(t, m, "p", "find", Request{Database: "large", Collection: "items", Query: query}).(map[string]any)
	if first["records"].([]Record)[0]["id"] != "r10999" {
		t.Fatal(first)
	}
	query.Cursor = first["nextCursor"].(string)
	second := rows(t, call(t, m, "p", "find", Request{Database: "large", Collection: "items", TimeoutMS: 60000, Query: query}))
	if len(second) != 3 || second[0]["id"] != "r10996" {
		t.Fatal(second)
	}
	d, err := m.resolve(context.Background(), "p", "large", "", false)
	if err != nil {
		t.Fatal(err)
	}
	c := call(t, m, "p", "collection_describe", Request{Database: "large", Collection: "items"}).(Collection)
	q := Query{Limit: 1}
	if err := validateQuery(c, &q); err != nil {
		t.Fatal(err)
	}
	q.Cursor = signCursor(d.key, cursorData{queryHash(c, q), c.Version, time.Now().Add(time.Hour).Unix(), Record{"id": "r09999"}})
	v := rows(t, call(t, m, "p", "find", Request{Database: "large", Collection: "items", Query: q}))
	if len(v) != 1 || v[0]["id"] != "r10000" {
		t.Fatal(v)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.Execute(ctx, "p", "insert", Request{Database: "large", Collection: "items", Records: []Record{{"id": "cancelled", "n": 1.}}}); err == nil {
		t.Fatal("cancelled write committed")
	}
	if call(t, m, "p", "get", Request{Database: "large", Collection: "items", Key: Record{"id": "cancelled"}}).(map[string]any)["found"].(bool) {
		t.Fatal("cancelled record persisted")
	}
}

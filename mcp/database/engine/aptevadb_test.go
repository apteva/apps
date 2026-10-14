package engine

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"path/filepath"
	"strings"
	"testing"
)

// Reopen at every durable boundary between physical DDL and schema publication.
// The intent survives and reconciliation must be idempotent on another reopen.
func TestAptevaSchemaJournalRecovery(t *testing.T) {
	for _, op := range []string{"create", "index_create", "index_drop", "drop"} {
		for _, applied := range []bool{false, true} {
			t.Run(op+map[bool]string{false: "/intent", true: "/physical"}[applied], func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "native")
				opened, e := openAptevaDB(path)
				checkErr(t, e)
				b := opened.(*aptevaBackend)
				c, e := newCollection(Request{Collection: "items", Fields: []Field{{Name: "value", Type: "text"}}})
				checkErr(t, e)
				if op != "create" {
					if op == "index_drop" {
						c.Indexes = []Index{{Name: "by_value", Fields: []Order{{"value", "desc"}}}}
					}
					checkErr(t, b.commitDDL(context.Background(), nativeDDL{After: &c}))
					checkErr(t, b.transaction(context.Background(), true, func(tx transaction) error {
						return tx.insert(c, `["a"]`, Record{"id": "a", "value": "saved", "_created_at": "2026-01-01T00:00:00.000000Z", "_updated_at": "2026-01-01T00:00:00.000000Z", "_version": "1"})
					}))
				}
				intent := nativeDDL{After: &c}
				switch op {
				case "index_create", "index_drop":
					before := cloneCollection(c)
					intent.Before = &before
					c.Version++
					if op == "index_create" {
						c.Indexes = []Index{{Name: "by_value", Fields: []Order{{"value", "desc"}}}}
					} else {
						c.Indexes = []Index{}
					}
				case "drop":
					intent = nativeDDL{Before: &c}
				}
				checkErr(t, b.db.Upsert([]byte(nativeIntentKey), marshal(intent)))
				if applied {
					checkErr(t, b.reconcileSchema(context.Background(), intent.Before, intent.After))
				}
				checkErr(t, b.close())
				for pass := 0; pass < 2; pass++ {
					opened, e = openAptevaDB(path)
					checkErr(t, e)
					b = opened.(*aptevaBackend)
					_, found, e := b.db.Find([]byte(nativeIntentKey), nil)
					checkErr(t, e)
					if found {
						t.Fatal("unpublished journal remains")
					}
					checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
						got, err := tx.collection("items")
						if op == "drop" {
							expectCode(t, err, "not_found")
							return nil
						}
						if err != nil {
							return err
						}
						if !equalJSON(got, c) {
							t.Fatal("recovered wrong schema", got)
						}
						if op != "create" {
							r, err := tx.get(got, `["a"]`)
							if err != nil {
								return err
							}
							if r["value"] != "saved" {
								t.Fatal("DDL lost records")
							}
						}
						return nil
					}))
					checkErr(t, b.close())
				}
			})
		}
	}
}

func TestAptevaOverlayCodecAndWideIndex(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"id", "text", false}, {"wide", "text", false}, {"n", "integer", false}, {"payload", "json", false}}})
	call(t, m, "p", "index_create", Request{Database: "native", Collection: "items", Index: &Index{Name: "by_wide", Fields: []Order{{"wide", "desc"}}}})
	// Exact application maxima include escaping in both the primary and index.
	id := strings.Repeat("a", 2044)            // JSON primary tuple is 2048 bytes.
	wide := strings.Repeat("\x00", 2046) + "a" // scalar tuple is 4096 bytes.
	var payload any
	checkErr(t, decode([]byte(strings.Repeat("[", 400)+`9007199254740993`+strings.Repeat("]", 400)), &payload))
	input := Record{"id": id, "wide": wide, "n": "9007199254740993", "payload": payload}
	call(t, m, "p", "batch", Request{Database: "native", Operations: []Operation{
		{"insert", Request{Collection: "items", Records: []Record{input}}},
		{"update", Request{Collection: "items", Query: Query{Where: &Filter{Field: "n", Op: "eq", Value: "9007199254740993"}}, Increment: Record{"n": "1"}}},
	}})
	got := call(t, m, "p", "get", Request{Database: "native", Collection: "items", Key: Record{"id": id}}).(map[string]any)["record"].(Record)
	if got["n"] != "9007199254740994" || !equalJSON(got["payload"], payload) {
		t.Fatal("typed overlay round trip failed")
	}
	page := rows(t, call(t, m, "p", "find", Request{Database: "native", Collection: "items", Query: Query{Where: &Filter{Field: "wide", Op: "eq", Value: wide}, OrderBy: []Order{{"wide", "desc"}}, RequireIndex: true}}))
	if len(page) != 1 || page[0]["id"] != id {
		t.Fatal("wide index failed")
	}
	// Large opaque JSON values bypass the native nested document depth limits.
	large := map[string]any{"a": strings.Repeat("x", 40000), "b": strings.Repeat("y", 40000), "exact": json.Number("9007199254740993")}
	call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": id}, Set: Record{"payload": large}})
	got = call(t, m, "p", "get", Request{Database: "native", Collection: "items", Key: Record{"id": id}}).(map[string]any)["record"].(Record)
	if !equalJSON(got["payload"], large) {
		t.Fatal("large JSON round trip failed")
	}
	d, e := m.resolve(context.Background(), "p", "native", "", false)
	checkErr(t, e)
	d.backend.(*aptevaBackend).poison(errors.New("uncertain schema publication"))
	_, e = m.Execute(context.Background(), "p", "get", Request{Database: "native", Collection: "items", Key: Record{"id": id}})
	expectCode(t, e, "storage_error")
}

func TestAptevaProjectedSortAndAggregateBound(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"n", "number", false}, {"pad", "text", false}}})
	for batch := 0; batch < 4; batch++ {
		var rs []Record
		for i := 0; i < 10; i++ {
			rs = append(rs, Record{"n": float64(batch*10 + i), "pad": strings.Repeat("x", 512<<10)})
		}
		call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: rs})
	}
	page := rows(t, call(t, m, "p", "find", Request{Database: "native", Collection: "items", Query: Query{Select: []string{"n"}, OrderBy: []Order{{"n", "desc"}}, Limit: 50}}))
	if len(page) != 40 || page[0]["n"] != 39. {
		t.Fatal("projected top page failed")
	}
	_, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "native", Collection: "items", GroupBy: []string{"id", "pad"}, Metrics: []Metric{{"count", "count", ""}}, Query: Query{Limit: 1}})
	expectCode(t, e, "resource_limit")
}

func TestBatchRejectsTemporaryUniqueConflicts(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "by_email", Fields: []Order{{"email", "asc"}}, Unique: true}})
			_, e := m.Execute(context.Background(), "p", "batch", Request{Database: "shop", Operations: []Operation{
				{"update", Request{Collection: "orders", Key: Record{"id": "a"}, Set: Record{"email": "b@example.com"}}},
				{"update", Request{Collection: "orders", Key: Record{"id": "b"}, Set: Record{"email": "a@example.com"}}},
			}})
			expectCode(t, e, "unique_conflict")
			// Releasing an owner first permits later reuse within the same batch.
			call(t, m, "p", "batch", Request{Database: "shop", Operations: []Operation{
				{"update", Request{Collection: "orders", Key: Record{"id": "b"}, Set: Record{"email": nil}}},
				{"update", Request{Collection: "orders", Key: Record{"id": "a"}, Set: Record{"email": "b@example.com"}}},
			}})
			_, e = m.Execute(context.Background(), "p", "batch", Request{Database: "shop", Operations: []Operation{
				{"insert", Request{Collection: "orders", Records: []Record{{"id": "x", "active": true, "email": "temporary@example.com"}}}},
				{"insert", Request{Collection: "orders", Records: []Record{{"id": "y", "active": true, "email": "temporary@example.com"}}}},
				{"delete", Request{Collection: "orders", Key: Record{"id": "x"}}},
			}})
			expectCode(t, e, "unique_conflict")
		})
	}
}

func TestNativeAggregateParity(t *testing.T) {
	var managers []*Manager
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		m := manager(t)
		fixture(t, m, adapter)
		managers = append(managers, m)
	}
	metrics := []Metric{{"count", "count", ""}, {"total", "sum", "amount"}, {"average", "avg", "amount"}, {"low_integer", "min", "units"}, {"high_integer", "max", "units"}, {"first_email", "min", "email"}, {"last_email", "max", "email"}, {"first_active", "min", "active"}}
	for _, groups := range [][]string{nil, {"country"}, {"units", "active"}} {
		for _, where := range []*Filter{nil, {Field: "country", Op: "is_null"}, {Field: "amount", Op: "gt", Value: 1000.}} {
			r := Request{Database: "shop", Collection: "orders", GroupBy: groups, Metrics: metrics, Query: Query{Where: where}}
			want := call(t, managers[0], "p", "aggregate", r)
			for _, m := range managers[1:] {
				got := call(t, m, "p", "aggregate", r)
				if !equalJSON(got, want) {
					t.Fatalf("aggregate mismatch groups=%v where=%+v: got %s want %s", groups, where, marshal(got), marshal(want))
				}
			}
		}
	}
	// Native alpha.4 returns float64 for widened sums. The app must reject them.
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "overflow", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "overflow", Collection: "items", Fields: []Field{{"n", "number", false}, {"i", "integer", false}}})
	call(t, m, "p", "insert", Request{Database: "overflow", Collection: "items", Records: []Record{{"n": math.MaxFloat64, "i": "9223372036854775807"}, {"n": math.MaxFloat64, "i": "9223372036854775807"}}})
	for _, f := range []string{"n", "i"} {
		_, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "overflow", Collection: "items", Metrics: []Metric{{"total", "sum", f}}})
		expectCode(t, e, "resource_limit")
	}
}

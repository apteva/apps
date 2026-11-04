package engine

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestInPrefixRanges(t *testing.T) {
	rng := rand.New(rand.NewSource(21))
	scopes := []any{nil, "", "a", "a b", "A", "b", "c", "é", "z\x00"}
	rows := []Record{}
	for i := 0; i < 1500; i++ {
		r := Record{"id": fmt.Sprintf("r%05d", i), "scope": scopes[rng.Intn(len(scopes))], "region": fmt.Sprintf("%d", rng.Intn(6)), "other": strconv.Itoa(rng.Intn(3))}
		if rng.Intn(8) > 0 {
			r["at"] = strconv.Itoa(rng.Intn(1000) - 200)
		} else {
			r["at"] = nil
		}
		rows = append(rows, r)
	}
	inFilter := func(field string, values ...any) Filter { return Filter{Field: field, Op: "in", Value: values} }
	cmp := func(field, op string, v any) Filter { return Filter{Field: field, Op: op, Value: v} }
	and := func(f ...Filter) *Filter { return &Filter{And: f} }
	num := func(v any) (int, bool) {
		if v == nil {
			return 0, false
		}
		n, e := strconv.Atoi(v.(string))
		return n, e == nil
	}
	between := func(r Record, lo, hi int) bool {
		n, ok := num(r["at"])
		return ok && n >= lo && n < hi
	}
	cases := []struct {
		name string
		f    *Filter
		want func(Record) bool
	}{
		{"in and range", and(inFilter("scope", "a", "b"), cmp("at", "gte", "100"), cmp("at", "lt", "500")), func(r Record) bool {
			return (r["scope"] == "a" || r["scope"] == "b") && between(r, 100, 500)
		}},
		{"duplicate values count once", and(inFilter("scope", "a", "a", "b", "a"), cmp("at", "gte", "0")), func(r Record) bool {
			return (r["scope"] == "a" || r["scope"] == "b") && between(r, 0, 1<<30)
		}},
		{"exact strings", and(inFilter("scope", "a b", "", "A", "é", "z\x00", "missing")), func(r Record) bool {
			return r["scope"] == "a b" || r["scope"] == "" || r["scope"] == "A" || r["scope"] == "é" || r["scope"] == "z\x00"
		}},
		{"boundaries", and(inFilter("scope", "a", "c"), cmp("at", "gt", "-50"), cmp("at", "lte", "50")), func(r Record) bool {
			return (r["scope"] == "a" || r["scope"] == "c") && between(r, -49, 51)
		}},
		{"empty range", and(inFilter("scope", "a", "c"), cmp("at", "gte", "50"), cmp("at", "lt", "50")), func(r Record) bool { return false }},
		{"integer in, descending index", and(inFilter("region", "1", "4"), cmp("at", "lt", "300")), func(r Record) bool {
			return (r["region"] == "1" || r["region"] == "4") && between(r, -1<<30, 300)
		}},
		{"in on the second field", and(cmp("region", "eq", "2"), inFilter("scope", "a", "b"), cmp("at", "gte", "0")), func(r Record) bool {
			return r["region"] == "2" && (r["scope"] == "a" || r["scope"] == "b") && between(r, 0, 1<<30)
		}},
		{"residual field", and(inFilter("scope", "a", "b"), cmp("other", "eq", "1")), func(r Record) bool {
			return (r["scope"] == "a" || r["scope"] == "b") && r["other"] == "1"
		}},
		{"null is not in", and(cmp("scope", "is_not_null", nil), inFilter("scope", "a"), cmp("at", "is_not_null", nil), cmp("at", "gte", "0")), func(r Record) bool {
			return r["scope"] == "a" && between(r, 0, 1<<30)
		}},
		{"or stays whole", &Filter{Or: []Filter{*and(inFilter("scope", "a", "b"), cmp("at", "gte", "0")), cmp("scope", "eq", "c")}}, func(r Record) bool {
			return (r["scope"] == "a" || r["scope"] == "b") && between(r, 0, 1<<30) || r["scope"] == "c"
		}},
	}
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m, e := Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			ctx := context.Background()
			exec := func(op, coll string, r Request) (map[string]any, error) {
				r.Database, r.Collection = "d", coll
				v, e := m.Execute(ctx, "p", op, r)
				out, _ := v.(map[string]any)
				return out, e
			}
			must := func(op, coll string, r Request) map[string]any {
				t.Helper()
				out, e := exec(op, coll, r)
				if e != nil {
					t.Fatal(op, e)
				}
				return out
			}
			if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			must("collection_create", "events", Request{Fields: []Field{{"id", "text", false}, {"scope", "text", true}, {"region", "integer", false}, {"other", "text", false}, {"at", "integer", true}}})
			must("index_create", "events", Request{Index: &Index{Name: "by_scope_at", Fields: []Order{{"scope", "asc"}, {"at", "asc"}}}})
			must("index_create", "events", Request{Index: &Index{Name: "by_region_at", Fields: []Order{{"region", "desc"}, {"at", "desc"}}}})
			must("index_create", "events", Request{Index: &Index{Name: "by_region_scope_at", Fields: []Order{{"region", "asc"}, {"scope", "asc"}, {"at", "asc"}}}})
			for i := 0; i < len(rows); i += 300 {
				must("insert", "events", Request{Records: rows[i:min(i+300, len(rows))]})
			}
			for _, tc := range cases {
				t.Run(tc.name, func(t *testing.T) {
					want := []string{}
					for _, r := range rows {
						if tc.want(r) {
							want = append(want, r["id"].(string))
						}
					}
					sort.Strings(want)
					if c := must("count", "events", Request{Query: Query{Where: tc.f}})["count"]; c != strconv.Itoa(len(want)) {
						t.Fatalf("count %v want %d", c, len(want))
					}
					got := must("aggregate", "events", Request{Query: Query{Where: tc.f}, Metrics: []Metric{{Name: "n", Op: "count"}, {Name: "s", Op: "sum", Field: "region"}}})["rows"].([]Record)
					if got[0]["n"] != strconv.Itoa(len(want)) {
						t.Fatalf("aggregate %v want %d", got[0]["n"], len(want))
					}
					v, e := m.Execute(ctx, "p", "batch", Request{Database: "d", Operations: []Operation{{Op: "count", Request: Request{Collection: "events", Query: Query{Where: tc.f}}}}})
					if e != nil {
						t.Fatal(e)
					}
					if c := v.(map[string]any)["results"].([]any)[0].(map[string]any)["count"]; c != strconv.Itoa(len(want)) {
						t.Fatalf("batch %v want %d", c, len(want))
					}
					// Pages cross the ranges: every record once, in the requested order.
					for _, order := range [][]Order{nil, {{"at", "desc"}}, {{"scope", "asc"}, {"at", "asc"}}} {
						seen := []string{}
						cursor := ""
						for {
							page := must("find", "events", Request{Query: Query{Where: tc.f, OrderBy: order, Limit: 37, Cursor: cursor, Select: []string{"id", "at"}}})
							for _, r := range page["records"].([]Record) {
								seen = append(seen, r["id"].(string))
							}
							if more, _ := page["hasMore"].(bool); !more {
								break
							}
							cursor = page["nextCursor"].(string)
						}
						sort.Strings(seen)
						if strings.Join(seen, ",") != strings.Join(want, ",") {
							t.Fatalf("order %v: %d records, want %d", order, len(seen), len(want))
						}
					}
				})
			}
			// requireIndex accepts a union now that it is one.
			scoped := and(inFilter("scope", "a", "b"), cmp("at", "gte", "0"))
			must("count", "events", Request{Query: Query{Where: scoped, RequireIndex: true}})
			must("find", "events", Request{Query: Query{Where: scoped, RequireIndex: true, Limit: 5}})
			// Diagnostics report what ran, and never change the result.
			plain := must("count", "events", Request{Query: Query{Where: scoped}})["count"]
			diag := must("count", "events", Request{Query: Query{Where: scoped}, Diagnostics: true})
			if diag["count"] != plain {
				t.Fatal(diag, plain)
			}
			report := diag["diagnostics"].(map[string]any)
			if report["executed"] != true || report["executionMs"] == nil || report["queueMs"] == nil || report["snapshotMs"] == nil {
				t.Fatalf("%v", report)
			}
			if adapter == "aptevadb" {
				n, _ := strconv.Atoi(plain.(string))
				if report["planSource"] != "execution" || report["indexOnly"] != true || report["documentFetches"] != int64(0) || report["indexEntriesExamined"] != int64(n) {
					t.Fatalf("%v", report)
				}
				if report["plan"].(map[string]any)["ranges"] != 2 {
					t.Fatalf("%v", report)
				}
				search := and(inFilter("scope", "a", "b"), Filter{Fields: []string{"other"}, Op: opLikeContainsASCII, Value: "1"})
				r := must("count", "events", Request{Query: Query{Where: search}, Diagnostics: true})["diagnostics"].(map[string]any)
				if r["indexOnly"] != false || r["documentFetches"].(int64) == 0 || r["documentFetches"].(int64) >= int64(len(rows)) || r["strategy"] != "native-count" {
					t.Fatalf("search did not narrow: %v", r)
				}
			} else if report["planSource"] != "explain" {
				t.Fatalf("%v", report)
			}
			if _, e := exec("find", "events", Request{Query: Query{Where: scoped}, Diagnostics: true}); e == nil {
				t.Fatal("find accepted diagnostics")
			}
			explained := must("explain", "events", Request{Query: Query{Where: scoped}})
			if explained["executed"] != false {
				t.Fatalf("%v", explained)
			}
			// More values than the range bound still give the exact answer.
			values := []any{}
			for i := 0; i < 100; i++ {
				values = append(values, fmt.Sprintf("v%d", i))
			}
			values = append(values, "a")
			wide := and(Filter{Field: "scope", Op: "in", Value: values[1:]}, cmp("at", "gte", "0"))
			wantWide := 0
			for _, r := range rows {
				if r["scope"] == "a" && between(r, 0, 1<<30) {
					wantWide++
				}
			}
			if c := must("count", "events", Request{Query: Query{Where: wide}})["count"]; c != strconv.Itoa(wantWide) {
				t.Fatalf("wide in: %v want %d", c, wantWide)
			}
			// Relationship filters narrow through the scope and report their work.
			must("collection_create", "links", Request{Fields: []Field{{"id", "text", false}, {"event_id", "text", true}}})
			must("index_create", "links", Request{Index: &Index{Name: "by_event", Fields: []Order{{"event_id", "asc"}}}})
			links := []Record{}
			linked := map[string]bool{}
			for i := 0; i < 300; i++ {
				id := rows[rng.Intn(len(rows))]["id"].(string)
				linked[id] = true
				links = append(links, Record{"id": fmt.Sprintf("l%d", i), "event_id": id})
			}
			must("insert", "links", Request{Records: links})
			rel := and(*scoped, Filter{Exists: &ExistsClause{Collection: "links", On: []ExistsOn{{LocalField: "id", ForeignField: "event_id"}}}})
			wantRel := 0
			for _, r := range rows {
				if (r["scope"] == "a" || r["scope"] == "b") && between(r, 0, 1<<30) && linked[r["id"].(string)] {
					wantRel++
				}
			}
			d2 := must("count", "events", Request{Query: Query{Where: rel}, Diagnostics: true})
			if d2["count"] != strconv.Itoa(wantRel) {
				t.Fatalf("exists: %v want %d", d2["count"], wantRel)
			}
			r2 := d2["diagnostics"].(map[string]any)
			if r2["exists"] == nil {
				t.Fatalf("%v", r2)
			}
			if adapter == "aptevadb" && (r2["strategy"] != "documents" || r2["documentFetches"].(int64) >= int64(len(rows))) {
				t.Fatalf("exists did not narrow: %v", r2)
			}
		})
	}
}

func TestUnionRangesAreBounded(t *testing.T) {
	c := Collection{Name: "events", PrimaryKey: []string{"id"}, Fields: []Field{{"id", "text", false}, {"a", "text", true}, {"b", "text", true}, {"at", "integer", true}}, Indexes: []Index{{Name: "i", Fields: []Order{{"a", "asc"}, {"b", "asc"}, {"at", "asc"}}}}}
	list := func(n int) []any {
		out := make([]any, n)
		for i := range out {
			out[i] = fmt.Sprintf("v%d", i)
		}
		return out
	}
	where := func(a, b int) *Filter {
		return &Filter{And: []Filter{{Field: "a", Op: "in", Value: list(a)}, {Field: "b", Op: "in", Value: list(b)}, {Field: "at", Op: "gte", Value: "1"}}}
	}
	if p := plan(c, Query{Where: where(8, 8)}); p.Ranges != 64 || !p.IndexedLookup {
		t.Fatalf("%+v", p)
	}
	if p := plan(c, Query{Where: where(8, 9)}); p.ranges != nil {
		t.Fatalf("over the bound: %d ranges", p.Ranges)
	}
	big := strings.Repeat("x", 40000)
	huge := []any{}
	for i := 0; i < 40; i++ {
		huge = append(huge, big+strconv.Itoa(i))
	}
	if p := plan(c, Query{Where: &Filter{Field: "a", Op: "in", Value: huge}}); p.ranges != nil {
		t.Fatalf("over the byte bound: %d ranges", p.Ranges)
	}
	// A caller's order keeps an ordered stream available beside the union.
	sortedBy := []Order{{"a", "asc"}, {"b", "asc"}, {"at", "asc"}}
	pk := []Order{{"id", "asc"}}
	if p := plan(c, Query{Where: where(2, 2), OrderBy: pk}); p.ranges != nil || p.Sort || p.union == nil || p.union.Ranges != 4 {
		t.Fatalf("%+v", p)
	}
	if p := plan(c, Query{Where: where(2, 2), OrderBy: pk, RequireIndex: true}); p.ranges == nil {
		t.Fatalf("requireIndex took the unindexed stream: %+v", p)
	}
	if p := plan(c, Query{Where: where(2, 2), OrderBy: sortedBy}); p.ranges != nil && !p.Sort {
		t.Fatalf("a union cannot supply this order: %+v", p)
	}
	// The same value twice is one range.
	if p := plan(c, Query{Where: &Filter{Field: "a", Op: "in", Value: []any{"x", "y", "x"}}}); p.Ranges != 2 {
		t.Fatalf("%+v", p)
	}
	// Exists probes keep the single range of the index they name.
	if p := plan(c, Query{Where: where(2, 2), hint: "i"}); p.ranges != nil {
		t.Fatalf("hinted plan expanded: %+v", p)
	}
}

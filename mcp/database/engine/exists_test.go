package engine

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
)

// Six alternative ways a call can relate to an order.
func orderLinks() []ExistsMatch {
	pair := func(on ExistsOn) ExistsMatch { return ExistsMatch{On: []ExistsOn{on}} }
	return []ExistsMatch{
		pair(ExistsOn{LocalField: "order_id", ForeignField: "id", Comparison: "text"}),
		pair(ExistsOn{LocalField: "order_id", ForeignField: "local_id"}),
		pair(ExistsOn{LocalField: "local_id", ForeignField: "call_id"}),
		pair(ExistsOn{LocalField: "local_id", ForeignField: "source_call_id"}),
		pair(ExistsOn{LocalField: "raw_id", ForeignField: "call_id"}),
		pair(ExistsOn{LocalField: "raw_id", ForeignField: "source_call_id"}),
	}
}

type relatedOrder struct {
	id                  int
	local, call, source *string
	product, region     *string
}

func isRelated(call Record, s relatedOrder) bool {
	eq := func(a any, b *string) bool {
		v, ok := a.(string)
		return ok && b != nil && v == *b
	}
	return eq(call["order_id"], strPtr(strconv.Itoa(s.id))) || eq(call["order_id"], s.local) ||
		eq(call["local_id"], s.call) || eq(call["local_id"], s.source) ||
		eq(call["raw_id"], s.call) || eq(call["raw_id"], s.source)
}

func strPtr(s string) *string { return &s }

func optional(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}

func TestCorrelatedExists(t *testing.T) {
	defer func(after, size int) { existsBuildAfter, existsSetBytes = after, size }(existsBuildAfter, existsSetBytes)
	for _, mode := range []struct {
		name        string
		after, size int
	}{
		{"default", existsBuildAfter, existsSetBytes},
		{"probes only", 1 << 30, existsSetBytes},
		{"sets at once", 1, existsSetBytes},
		{"sets over budget", 1, 64},
	} {
		existsBuildAfter, existsSetBytes = mode.after, mode.size
		var built atomic.Int64
		existsSetsBuilt = func() { built.Add(1) }
		t.Run(mode.name, testCorrelatedExists)
		existsSetsBuilt = nil
		// SQLite compiles to SQL; Pebble and AptevaDB must really take this route.
		if useSets := mode.after == 1 && mode.size > 64; useSets != (built.Load() > 0) {
			if mode.name != "default" {
				t.Fatalf("%s: %d key set builds", mode.name, built.Load())
			}
		}
	}
}

func testCorrelatedExists(t *testing.T) {
	rng := rand.New(rand.NewSource(8))
	key := func(prefix string) *string {
		switch rng.Intn(8) {
		case 0:
			return nil
		case 1:
			return strPtr("") // '' = '' matches in SQL
		}
		return strPtr(fmt.Sprintf("%s%d", prefix, rng.Intn(150)))
	}
	orders := []relatedOrder{}
	for i := 1; i <= 240; i++ {
		s := relatedOrder{id: i, local: key("L"), call: key("C"), source: key("R")}
		if rng.Intn(6) > 0 {
			s.product = strPtr(fmt.Sprintf("product-%d", rng.Intn(4)))
		}
		if rng.Intn(6) > 0 {
			s.region = strPtr(fmt.Sprintf("region-%d", rng.Intn(3)))
		}
		orders = append(orders, s)
	}
	calls := []Record{}
	for i := 0; i < 500; i++ {
		order := []any{nil, "", strconv.Itoa(1 + rng.Intn(300)), "007", " 7", "7 ", "+7", "1e1", "99999999999999999999", fmt.Sprintf("L%d", rng.Intn(150))}[rng.Intn(10)]
		calls = append(calls, Record{"id": fmt.Sprintf("c%04d", i), "region_id": fmt.Sprintf("region-%d", rng.Intn(3)), "n": strconv.Itoa(i),
			"order_id": order, "local_id": optional(key("C")), "raw_id": optional(key("R"))})
	}
	inList := func(v *string, list []string) bool {
		for _, x := range list {
			if v != nil && *v == x {
				return true
			}
		}
		return false
	}
	grant, grantRegions := []string{"product-0", "product-1"}, []string{"region-0"}
	anyValues := func(a []string) []any {
		out := make([]any, len(a))
		for i, x := range a {
			out[i] = x
		}
		return out
	}
	positive := &Filter{And: []Filter{{Field: "product_id", Op: "in", Value: anyValues(grant)}, {Field: "region_id", Op: "in", Value: anyValues(grantRegions)}}}
	outside := &Filter{Field: "product_id", Op: "not_in", Value: anyValues(grant)}
	scope := Filter{Field: "region_id", Op: "in", Value: []any{"region-0", "region-1"}}
	rule := func(call Record, s []relatedOrder) bool {
		pos, neg := false, false
		for _, v := range s {
			if !isRelated(call, v) {
				continue
			}
			pos = pos || inList(v.product, grant) && inList(v.region, grantRegions)
			// NULL NOT IN (...) is not true: a null product does not exclude.
			neg = neg || v.product != nil && !inList(v.product, grant)
		}
		return pos && !neg
	}
	cases := []struct {
		name  string
		where func() *Filter
		want  func(Record) bool
	}{
		{"exists any order", func() *Filter { return &Filter{Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks()}} }, func(c Record) bool {
			for _, s := range orders {
				if isRelated(c, s) {
					return true
				}
			}
			return false
		}},
		{"not exists any order", func() *Filter { return &Filter{NotExists: &ExistsClause{Collection: "orders", AnyOf: orderLinks()}} }, func(c Record) bool {
			for _, s := range orders {
				if isRelated(c, s) {
					return false
				}
			}
			return true
		}},
		{"access rule", func() *Filter {
			return &Filter{And: []Filter{scope,
				{Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: positive}},
				{NotExists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: outside}}}}
		}, func(c Record) bool { return c["region_id"] != "region-2" && rule(c, orders) }},
		{"or with a plain field", func() *Filter {
			return &Filter{Or: []Filter{{Field: "region_id", Op: "eq", Value: "region-2"}, {Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: positive}}}}
		}, func(c Record) bool {
			if c["region_id"] == "region-2" {
				return true
			}
			for _, s := range orders {
				if isRelated(c, s) && inList(s.product, grant) && inList(s.region, grantRegions) {
					return true
				}
			}
			return false
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
			exec := func(op, coll string, r Request) (any, error) {
				r.Database, r.Collection = "d", coll
				return m.Execute(ctx, "p", op, r)
			}
			must := func(op, coll string, r Request) map[string]any {
				t.Helper()
				v, e := exec(op, coll, r)
				if e != nil {
					t.Fatal(op, e)
				}
				out, _ := v.(map[string]any)
				return out
			}
			if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			must("collection_create", "calls", Request{Fields: []Field{{"id", "text", false}, {"region_id", "text", false}, {"n", "integer", false}, {"order_id", "text", true}, {"local_id", "text", true}, {"raw_id", "text", true}}})
			must("collection_create", "orders", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, {"local_id", "text", true}, {"call_id", "text", true}, {"source_call_id", "text", true}, {"product_id", "text", true}, {"region_id", "text", true}}, PrimaryKey: []string{"native_key"}})
			must("index_create", "calls", Request{Index: &Index{Name: "by_region", Fields: []Order{{"region_id", "asc"}, {"n", "desc"}}}})
			// A where on a missing index fails closed, naming the index to add.
			_, missing := exec("count", "calls", Request{Query: Query{Where: &Filter{Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks()}}}})
			var ee *Error
			if !errors.As(missing, &ee) || ee.Code != "query_too_expensive" || !strings.Contains(ee.Message, "orders starting with one of: id") {
				t.Fatalf("missing index: %v", missing)
			}
			for _, f := range []string{"id", "local_id", "call_id", "source_call_id"} {
				must("index_create", "orders", Request{Index: &Index{Name: "by_" + f, Fields: []Order{{f, "asc"}}}})
			}
			for i := 0; i < len(calls); i += 100 {
				must("insert", "calls", Request{Records: calls[i : i+100]})
			}
			rows := []Record{}
			for _, s := range orders {
				rows = append(rows, Record{"native_key": fmt.Sprintf("k%d", s.id), "id": strconv.Itoa(s.id), "local_id": optional(s.local), "call_id": optional(s.call), "source_call_id": optional(s.source), "product_id": optional(s.product), "region_id": optional(s.region)})
			}
			must("insert", "orders", Request{Records: rows})

			for _, tc := range cases {
				var want []string
				byRegion := map[string]int{}
				for i := len(calls) - 1; i >= 0; i-- {
					if tc.want(calls[i]) {
						want = append(want, calls[i]["id"].(string))
						byRegion[calls[i]["region_id"].(string)]++
					}
				}
				if len(want) == 0 || len(want) == len(calls) {
					t.Fatalf("%s: fixture does not discriminate (%d rows)", tc.name, len(want))
				}
				if c := must("count", "calls", Request{Query: Query{Where: tc.where()}})["count"]; c != strconv.Itoa(len(want)) {
					t.Fatalf("%s: count %v want %d", tc.name, c, len(want))
				}
				// Permissions apply before sorting and pagination, across a cursor.
				var got []string
				q := Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 13, Select: []string{"id", "n"}}
				for {
					v := must("find", "calls", Request{Query: q})
					records := v["records"].([]Record)
					if v["nextCursor"] != nil && v["nextCursor"] != "" && len(records) != 13 {
						t.Fatalf("%s: short page %d", tc.name, len(records))
					}
					for _, r := range records {
						got = append(got, r["id"].(string))
					}
					next, _ := v["nextCursor"].(string)
					if next == "" {
						break
					}
					q.Cursor = next
				}
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Fatalf("%s: find\n got %v\nwant %v", tc.name, got, want)
				}
				// Each call counts once, however many orders match it.
				groups := must("aggregate", "calls", Request{Query: Query{Where: tc.where()}, GroupBy: []string{"region_id"}, Metrics: []Metric{{"n", "count", "", nil}, {"total", "sum", "n", nil}}})["rows"].([]Record)
				if len(groups) != len(byRegion) {
					t.Fatalf("%s: groups %v want %v", tc.name, groups, byRegion)
				}
				for _, g := range groups {
					if g["n"] != strconv.Itoa(byRegion[g["region_id"].(string)]) {
						t.Fatalf("%s: group %v want %v", tc.name, g, byRegion)
					}
				}
				batch := must("batch", "", Request{Operations: []Operation{
					{Op: "count", Request: Request{Database: "d", Collection: "calls", Query: Query{Where: tc.where()}}},
					{Op: "find", Request: Request{Database: "d", Collection: "calls", Query: Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 1000}}},
					{Op: "aggregate", Request: Request{Database: "d", Collection: "calls", Query: Query{Where: tc.where()}, Metrics: []Metric{{"n", "count", "", nil}}}},
				}})["results"].([]any)
				if c := batch[0].(map[string]any)["count"]; c != strconv.Itoa(len(want)) {
					t.Fatalf("%s: batch count %v", tc.name, c)
				}
				if n := len(batch[1].(map[string]any)["records"].([]Record)); n != len(want) {
					t.Fatalf("%s: batch find %d", tc.name, n)
				}
				if n := batch[2].(map[string]any)["rows"].([]Record)[0]["n"]; n != strconv.Itoa(len(want)) {
					t.Fatalf("%s: batch aggregate %v", tc.name, n)
				}
			}

			// The scope still selects the index beside the exists nodes.
			scoped := &Filter{And: []Filter{{Field: "region_id", Op: "in", Value: []any{"region-0"}}, {Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: positive}}}}
			plan := must("explain", "calls", Request{Query: Query{Where: scoped, RequireIndex: true}})
			if text := string(marshal(plan)); adapter != "sqlite" && !strings.Contains(text, `"index":"by_region"`) || adapter == "sqlite" && !strings.Contains(text, `"indexedLookup":true`) {
				t.Fatal(text)
			}

			exists := func(c *ExistsClause) *Filter { return &Filter{Exists: c} }
			on := []ExistsOn{{LocalField: "order_id", ForeignField: "local_id"}}
			for name, where := range map[string]*Filter{
				"unknown local":    exists(&ExistsClause{Collection: "orders", On: []ExistsOn{{LocalField: "nope", ForeignField: "local_id"}}}),
				"unknown foreign":  exists(&ExistsClause{Collection: "orders", On: []ExistsOn{{LocalField: "order_id", ForeignField: "nope"}}}),
				"mixed types":      exists(&ExistsClause{Collection: "orders", On: []ExistsOn{{LocalField: "order_id", ForeignField: "id"}}}),
				"bad comparison":   exists(&ExistsClause{Collection: "orders", On: []ExistsOn{{LocalField: "order_id", ForeignField: "id", Comparison: "numeric"}}}),
				"no pairs":         exists(&ExistsClause{Collection: "orders"}),
				"empty on":         exists(&ExistsClause{Collection: "orders", On: []ExistsOn{}}),
				"on and anyOf":     exists(&ExistsClause{Collection: "orders", On: on, AnyOf: []ExistsMatch{{On: on}}}),
				"empty anyOf":      exists(&ExistsClause{Collection: "orders", AnyOf: []ExistsMatch{}}),
				"empty match":      exists(&ExistsClause{Collection: "orders", AnyOf: []ExistsMatch{{}}}),
				"nested bad local": exists(&ExistsClause{Collection: "orders", On: on, Where: exists(&ExistsClause{Collection: "calls", On: on})}),
				"bad where":        exists(&ExistsClause{Collection: "orders", On: on, Where: &Filter{Field: "nope", Op: "eq", Value: "x"}}),
				"with op":          {Op: "eq", Exists: &ExistsClause{Collection: "orders", On: on}},
				"both":             {Exists: &ExistsClause{Collection: "orders", On: on}, NotExists: &ExistsClause{Collection: "orders", On: on}},
				"null in not_in":   {Field: "local_id", Op: "not_in", Value: []any{nil}},
				"empty not_in":     {Field: "local_id", Op: "not_in", Value: []any{}},
			} {
				if _, e := exec("count", "calls", Request{Query: Query{Where: where}}); e == nil {
					t.Fatalf("%s accepted", name)
				}
			}
			if _, e := exec("count", "calls", Request{Query: Query{Where: exists(&ExistsClause{Collection: "missing", On: on})}}); e == nil {
				t.Fatal("missing collection accepted")
			}
			clause := exists(&ExistsClause{Collection: "orders", On: on})
			if _, e := exec("aggregate", "calls", Request{Metrics: []Metric{{"n", "count", "", clause}}}); e == nil {
				t.Fatal("metric exists accepted")
			}
			for _, op := range []string{"update", "delete"} {
				r := Request{Query: Query{Where: clause}, MaxAffected: 1000}
				if op == "update" {
					r.Set = Record{"order_id": "x"}
				}
				if _, e := exec(op, "calls", r); e == nil {
					t.Fatalf("%s with exists accepted", op)
				}
			}
			// not_in: null is not satisfied, and a one-value list equals ne.
			notIn := func(values ...any) string {
				return must("count", "orders", Request{Query: Query{Where: &Filter{Field: "product_id", Op: "not_in", Value: values}}})["count"].(string)
			}
			wantIn := func(list ...string) string {
				n := 0
				for _, s := range orders {
					if s.product != nil && !inList(s.product, list) {
						n++
					}
				}
				return strconv.Itoa(n)
			}
			if got := notIn("product-0", "product-1"); got != wantIn("product-0", "product-1") {
				t.Fatalf("not_in %s want %s", got, wantIn("product-0", "product-1"))
			}
			if got := notIn("product-0"); got != wantIn("product-0") {
				t.Fatalf("not_in one %s want %s", got, wantIn("product-0"))
			}
			if c := must("count", "orders", Request{Query: Query{Where: &Filter{Field: "id", Op: "not_in", Value: []any{"1", "2"}}}})["count"]; c != strconv.Itoa(len(orders)-2) {
				t.Fatalf("integer not_in %v", c)
			}
		})
	}
}

type failingFind struct {
	transaction
	calls int
}

func (f *failingFind) find(Collection, Query, int) ([]Record, error) {
	f.calls++
	return nil, errors.New("probe failed")
}

// A failed probe never reads as "no related record", for either polarity.
func TestExistsProbeFailureFailsClosed(t *testing.T) {
	state := &existsState{}
	tx := &failingFind{}
	plan := &existsPlan{tx: tx, alts: []existsAlternative{{pairs: []existsPair{{on: ExistsOn{LocalField: "a", ForeignField: "b"}, local: Field{Type: "text"}, foreign: Field{Type: "text"}}}}}, state: state}
	for _, f := range []*Filter{{Exists: &ExistsClause{plan: plan}}, {NotExists: &ExistsClause{plan: plan}}} {
		if matchesExists(Record{"a": "x"}, f) {
			t.Fatalf("matched after failure: %+v", f)
		}
	}
	if state.failure() == nil || tx.calls != 2 {
		t.Fatal(state.failure(), tx.calls)
	}
	// Unprepared clauses match nothing either way.
	for _, f := range []*Filter{{Exists: &ExistsClause{}}, {NotExists: &ExistsClause{}}} {
		if matchesExists(Record{"a": "x"}, f) {
			t.Fatal("unprepared clause matched")
		}
		if sql, _ := compileExists(Collection{}, f); sql != "0" {
			t.Fatal(sql)
		}
	}
	// A native path that forgets stripExists is rejected by the engine's parser.
	if _, ok := nativeFilter(Collection{}, &Filter{NotExists: &ExistsClause{}}).(map[string]any)["$exists_needs_evaluation"]; !ok {
		t.Fatal("native filter must not drop exists")
	}
}

// BenchmarkExistsAndNotExists measures the product-restricted rule at the local app's
// size: 125,000 calls and 35,000 orders. A third of the calls have related orders.
func BenchmarkExistsAndNotExists(b *testing.B) {
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	exec := func(op, coll string, r Request) any {
		r.Database, r.Collection = "d", coll
		v, e := m.Execute(ctx, "p", op, r)
		if e != nil {
			b.Fatal(op, e)
		}
		return v
	}
	if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: "aptevadb"}); e != nil {
		b.Fatal(e)
	}
	exec("collection_create", "calls", Request{Fields: []Field{{"id", "text", false}, {"region_id", "text", false}, {"n", "integer", false}, {"order_id", "text", true}, {"local_id", "text", true}, {"raw_id", "text", true}}})
	exec("collection_create", "orders", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, {"local_id", "text", true}, {"call_id", "text", true}, {"source_call_id", "text", true}, {"product_id", "text", true}, {"region_id", "text", true}}, PrimaryKey: []string{"native_key"}})
	exec("index_create", "calls", Request{Index: &Index{Name: "by_region", Fields: []Order{{"region_id", "asc"}, {"n", "desc"}}}})
	for _, f := range []string{"id", "local_id", "call_id", "source_call_id"} {
		exec("index_create", "orders", Request{Index: &Index{Name: "by_" + f, Fields: []Order{{f, "asc"}}}})
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 125000; i += 1000 {
		batch := make([]Record, 1000)
		for j := range batch {
			n := i + j
			r := Record{"id": fmt.Sprintf("c%06d", n), "region_id": fmt.Sprintf("region-%02d", rng.Intn(30)), "n": strconv.Itoa(n), "order_id": nil, "local_id": fmt.Sprintf("C%06d", n), "raw_id": fmt.Sprintf("R%06d", n)}
			if n%3 == 0 {
				r["order_id"] = fmt.Sprintf("L%06d", n)
			}
			batch[j] = r
		}
		exec("insert", "calls", Request{Records: batch})
	}
	for i := 0; i < 35000; i += 1000 {
		batch := make([]Record, 1000)
		for j := range batch {
			n := i + j
			call := n * 3
			batch[j] = Record{"native_key": fmt.Sprintf("k%06d", n), "id": strconv.Itoa(n + 1), "local_id": fmt.Sprintf("L%06d", call), "call_id": fmt.Sprintf("C%06d", call), "source_call_id": fmt.Sprintf("R%06d", call), "product_id": fmt.Sprintf("product-%d", rng.Intn(12)), "region_id": fmt.Sprintf("region-%02d", rng.Intn(30))}
		}
		exec("insert", "orders", Request{Records: batch})
	}
	grant := []any{"product-0", "product-1", "product-2", "product-3", "product-4", "product-5", "product-6", "product-7", "product-8", "product-9", "product-10"}
	rule := func(scope ...Filter) *Filter {
		return &Filter{And: append(scope,
			Filter{Exists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: &Filter{Field: "product_id", Op: "in", Value: grant}}},
			Filter{NotExists: &ExistsClause{Collection: "orders", AnyOf: orderLinks(), Where: &Filter{Field: "product_id", Op: "not_in", Value: grant}}})}
	}
	one := Filter{Field: "region_id", Op: "eq", Value: "region-07"}
	two := Filter{Field: "region_id", Op: "in", Value: []any{"region-07", "region-08"}}
	for _, tc := range []struct {
		name  string
		where func() *Filter
	}{
		{"one_region_no_rule", func() *Filter { return &Filter{And: []Filter{one}} }},
		{"one_region", func() *Filter { return rule(one) }},
		{"two_regions", func() *Filter { return rule(two) }},
		{"all_calls", func() *Filter { return rule() }},
	} {
		b.Run("count/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("count", "calls", Request{Query: Query{Where: tc.where()}})
			}
		})
		b.Run("find50/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("find", "calls", Request{Query: Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 50}})
			}
		})
		b.Run("group/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("aggregate", "calls", Request{Query: Query{Where: tc.where()}, GroupBy: []string{"region_id"}, Metrics: []Metric{{"n", "count", "", nil}}})
			}
		})
	}
}

// Pairs in on all match, like SQL's ON a AND b; anyOf lists alternatives.
func TestExistsCompositeKeys(t *testing.T) {
	defer func(after, size int) { existsBuildAfter, existsSetBytes = after, size }(existsBuildAfter, existsSetBytes)
	values := []any{nil, "", "a", "b", "ab", "c", "bc", "1", "2", "007"}
	locals, foreigns := []Record{}, []Record{}
	n := 0
	for _, x := range values {
		for _, y := range values {
			for _, z := range []any{nil, "1", "2", "7", "007", " 7"} {
				n++
				locals = append(locals, Record{"id": fmt.Sprintf("l%d", n), "x": x, "y": y, "z": z})
			}
		}
	}
	rng := rand.New(rand.NewSource(4))
	for i := 0; i < 90; i++ {
		foreigns = append(foreigns, Record{"id": fmt.Sprintf("f%d", i), "fx": values[rng.Intn(len(values))], "fy": values[rng.Intn(len(values))], "num": strconv.Itoa(1 + rng.Intn(8)), "kind": []any{nil, "keep", "drop"}[rng.Intn(3)]})
	}
	same := func(a, b any) bool { return a != nil && b != nil && a == b }
	composite := func(l, f Record) bool { return same(l["x"], f["fx"]) && same(l["y"], f["fy"]) }
	viaNumber := func(l, f Record) bool { return same(l["z"], f["num"]) }
	onXY := []ExistsOn{{LocalField: "x", ForeignField: "fx"}, {LocalField: "y", ForeignField: "fy"}}
	onZ := []ExistsOn{{LocalField: "z", ForeignField: "num", Comparison: "text"}}
	keep := &Filter{Field: "kind", Op: "eq", Value: "keep"}
	anyRelated := func(l Record, rel func(l, f Record) bool, kept bool) bool {
		for _, f := range foreigns {
			if rel(l, f) && (!kept || f["kind"] == "keep") {
				return true
			}
		}
		return false
	}
	cases := []struct {
		name string
		f    func() *Filter
		want func(Record) bool
	}{
		{"on is and", func() *Filter { return &Filter{Exists: &ExistsClause{Collection: "f", On: onXY}} }, func(l Record) bool { return anyRelated(l, composite, false) }},
		{"not exists composite", func() *Filter { return &Filter{NotExists: &ExistsClause{Collection: "f", On: onXY, Where: keep}} }, func(l Record) bool { return !anyRelated(l, composite, true) }},
		{"composite with text comparison", func() *Filter {
			return &Filter{Exists: &ExistsClause{Collection: "f", On: []ExistsOn{{LocalField: "x", ForeignField: "fx"}, {LocalField: "z", ForeignField: "num", Comparison: "text"}}}}
		}, func(l Record) bool {
			return anyRelated(l, func(l, f Record) bool { return same(l["x"], f["fx"]) && viaNumber(l, f) }, false)
		}},
		{"anyOf mixes composite and single", func() *Filter {
			return &Filter{Exists: &ExistsClause{Collection: "f", AnyOf: []ExistsMatch{{On: onXY}, {On: onZ}}, Where: keep}}
		}, func(l Record) bool {
			return anyRelated(l, composite, true) || anyRelated(l, viaNumber, true)
		}},
	}
	for _, mode := range []struct {
		name        string
		after, size int
	}{{"probes", 1 << 30, existsSetBytes}, {"sets", 1, existsSetBytes}, {"over budget", 1, 64}} {
		existsBuildAfter, existsSetBytes = mode.after, mode.size
		for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
			t.Run(mode.name+"/"+adapter, func(t *testing.T) {
				var built atomic.Int64
				existsSetsBuilt = func() { built.Add(1) }
				defer func() { existsSetsBuilt = nil }()
				if mode.name == "sets" && adapter != "sqlite" {
					defer func() {
						if built.Load() == 0 {
							t.Error("no key sets built")
						}
					}()
				}
				m, e := Open(t.TempDir())
				if e != nil {
					t.Fatal(e)
				}
				defer m.Close()
				ctx := context.Background()
				call := func(op, coll string, r Request) map[string]any {
					t.Helper()
					r.Database, r.Collection = "d", coll
					v, e := m.Execute(ctx, "p", op, r)
					if e != nil {
						t.Fatal(op, e)
					}
					out, _ := v.(map[string]any)
					return out
				}
				if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
					t.Fatal(e)
				}
				call("collection_create", "l", Request{Fields: []Field{{"id", "text", false}, {"x", "text", true}, {"y", "text", true}, {"z", "text", true}}})
				call("collection_create", "f", Request{Fields: []Field{{"id", "text", false}, {"fx", "text", true}, {"fy", "text", true}, {"num", "integer", false}, {"kind", "text", true}}})
				// One indexed field is enough to seek a composite key.
				if _, e := m.Execute(ctx, "p", "count", Request{Database: "d", Collection: "l", Query: Query{Where: &Filter{Exists: &ExistsClause{Collection: "f", On: onXY}}}}); e == nil || !strings.Contains(e.Error(), "fx, fy") {
					t.Fatalf("missing index: %v", e)
				}
				call("index_create", "f", Request{Index: &Index{Name: "by_fx", Fields: []Order{{"fx", "asc"}}}})
				call("index_create", "f", Request{Index: &Index{Name: "by_num", Fields: []Order{{"num", "asc"}}}})
				for i := 0; i < len(locals); i += 200 {
					call("insert", "l", Request{Records: locals[i:min(i+200, len(locals))]})
				}
				call("insert", "f", Request{Records: foreigns})
				for _, tc := range cases {
					want := 0
					for _, l := range locals {
						if tc.want(l) {
							want++
						}
					}
					if want == 0 || want == len(locals) {
						t.Fatalf("%s: fixture does not discriminate: %d", tc.name, want)
					}
					if c := call("count", "l", Request{Query: Query{Where: tc.f()}})["count"]; c != strconv.Itoa(want) {
						t.Fatalf("%s: count %v want %d", tc.name, c, want)
					}
					if n := len(call("find", "l", Request{Query: Query{Where: tc.f(), Limit: 1000}})["records"].([]Record)); n != want {
						t.Fatalf("%s: find %d want %d", tc.name, n, want)
					}
				}
			})
		}
	}
}

func TestTupleKeyIsUnambiguous(t *testing.T) {
	if tupleKey([]any{"ab", "c"}) == tupleKey([]any{"a", "bc"}) {
		t.Fatal("composite keys collide")
	}
	if tupleKey([]any{"x"}) != "x" {
		t.Fatal("single keys stay as they are")
	}
}

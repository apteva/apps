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

// The CRM's six call-to-sale relationships.
func callSaleOn() []ExistsOn {
	return []ExistsOn{
		{LocalField: "vente_id", ForeignField: "id", Comparison: "text"},
		{LocalField: "vente_id", ForeignField: "local_id"},
		{LocalField: "local_id", ForeignField: "appel_id"},
		{LocalField: "local_id", ForeignField: "appel_source_id"},
		{LocalField: "raw_call_id", ForeignField: "appel_id"},
		{LocalField: "raw_call_id", ForeignField: "appel_source_id"},
	}
}

type existsSale struct {
	id                   int
	local, appel, source *string
	offer, centre        *string
}

func relatedSale(call Record, s existsSale) bool {
	eq := func(a any, b *string) bool {
		v, ok := a.(string)
		return ok && b != nil && v == *b
	}
	return eq(call["vente_id"], strPtr(strconv.Itoa(s.id))) || eq(call["vente_id"], s.local) ||
		eq(call["local_id"], s.appel) || eq(call["local_id"], s.source) ||
		eq(call["raw_call_id"], s.appel) || eq(call["raw_call_id"], s.source)
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
	sales := []existsSale{}
	for i := 1; i <= 240; i++ {
		s := existsSale{id: i, local: key("L"), appel: key("C"), source: key("R")}
		if rng.Intn(6) > 0 {
			s.offer = strPtr(fmt.Sprintf("offer-%d", rng.Intn(4)))
		}
		if rng.Intn(6) > 0 {
			s.centre = strPtr(fmt.Sprintf("centre-%d", rng.Intn(3)))
		}
		sales = append(sales, s)
	}
	calls := []Record{}
	for i := 0; i < 500; i++ {
		vente := []any{nil, "", strconv.Itoa(1 + rng.Intn(300)), "007", " 7", "7 ", "+7", "1e1", "99999999999999999999", fmt.Sprintf("L%d", rng.Intn(150))}[rng.Intn(10)]
		calls = append(calls, Record{"id": fmt.Sprintf("c%04d", i), "centre_id": fmt.Sprintf("centre-%d", rng.Intn(3)), "n": strconv.Itoa(i),
			"vente_id": vente, "local_id": optional(key("C")), "raw_call_id": optional(key("R"))})
	}
	inList := func(v *string, list []string) bool {
		for _, x := range list {
			if v != nil && *v == x {
				return true
			}
		}
		return false
	}
	grant, grantCentres := []string{"offer-0", "offer-1"}, []string{"centre-0"}
	anyValues := func(a []string) []any {
		out := make([]any, len(a))
		for i, x := range a {
			out[i] = x
		}
		return out
	}
	positive := &Filter{And: []Filter{{Field: "offre_id", Op: "in", Value: anyValues(grant)}, {Field: "centre_id", Op: "in", Value: anyValues(grantCentres)}}}
	outside := &Filter{Field: "offre_id", Op: "not_in", Value: anyValues(grant)}
	scope := Filter{Field: "centre_id", Op: "in", Value: []any{"centre-0", "centre-1"}}
	policy := func(call Record, s []existsSale) bool {
		pos, neg := false, false
		for _, v := range s {
			if !relatedSale(call, v) {
				continue
			}
			pos = pos || inList(v.offer, grant) && inList(v.centre, grantCentres)
			// NULL NOT IN (...) is not true: a null offer does not exclude.
			neg = neg || v.offer != nil && !inList(v.offer, grant)
		}
		return pos && !neg
	}
	cases := []struct {
		name  string
		where func() *Filter
		want  func(Record) bool
	}{
		{"exists any sale", func() *Filter { return &Filter{Exists: &ExistsClause{Collection: "ventes", On: callSaleOn()}} }, func(c Record) bool {
			for _, s := range sales {
				if relatedSale(c, s) {
					return true
				}
			}
			return false
		}},
		{"not exists any sale", func() *Filter { return &Filter{NotExists: &ExistsClause{Collection: "ventes", On: callSaleOn()}} }, func(c Record) bool {
			for _, s := range sales {
				if relatedSale(c, s) {
					return false
				}
			}
			return true
		}},
		{"offer policy", func() *Filter {
			return &Filter{And: []Filter{scope,
				{Exists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: positive}},
				{NotExists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: outside}}}}
		}, func(c Record) bool { return c["centre_id"] != "centre-2" && policy(c, sales) }},
		{"or with a plain field", func() *Filter {
			return &Filter{Or: []Filter{{Field: "centre_id", Op: "eq", Value: "centre-2"}, {Exists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: positive}}}}
		}, func(c Record) bool {
			if c["centre_id"] == "centre-2" {
				return true
			}
			for _, s := range sales {
				if relatedSale(c, s) && inList(s.offer, grant) && inList(s.centre, grantCentres) {
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
			must("collection_create", "appels", Request{Fields: []Field{{"id", "text", false}, {"centre_id", "text", false}, {"n", "integer", false}, {"vente_id", "text", true}, {"local_id", "text", true}, {"raw_call_id", "text", true}}})
			must("collection_create", "ventes", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, {"local_id", "text", true}, {"appel_id", "text", true}, {"appel_source_id", "text", true}, {"offre_id", "text", true}, {"centre_id", "text", true}}, PrimaryKey: []string{"native_key"}})
			must("index_create", "appels", Request{Index: &Index{Name: "by_centre", Fields: []Order{{"centre_id", "asc"}, {"n", "desc"}}}})
			// A where on a missing index fails closed, naming the index to add.
			_, missing := exec("count", "appels", Request{Query: Query{Where: &Filter{Exists: &ExistsClause{Collection: "ventes", On: callSaleOn()}}}})
			var ee *Error
			if !errors.As(missing, &ee) || ee.Code != "query_too_expensive" || !strings.Contains(ee.Message, "ventes.") {
				t.Fatalf("missing index: %v", missing)
			}
			for _, f := range []string{"id", "local_id", "appel_id", "appel_source_id"} {
				must("index_create", "ventes", Request{Index: &Index{Name: "by_" + f, Fields: []Order{{f, "asc"}}}})
			}
			for i := 0; i < len(calls); i += 100 {
				must("insert", "appels", Request{Records: calls[i : i+100]})
			}
			rows := []Record{}
			for _, s := range sales {
				rows = append(rows, Record{"native_key": fmt.Sprintf("k%d", s.id), "id": strconv.Itoa(s.id), "local_id": optional(s.local), "appel_id": optional(s.appel), "appel_source_id": optional(s.source), "offre_id": optional(s.offer), "centre_id": optional(s.centre)})
			}
			must("insert", "ventes", Request{Records: rows})

			for _, tc := range cases {
				var want []string
				byCentre := map[string]int{}
				for i := len(calls) - 1; i >= 0; i-- {
					if tc.want(calls[i]) {
						want = append(want, calls[i]["id"].(string))
						byCentre[calls[i]["centre_id"].(string)]++
					}
				}
				if len(want) == 0 || len(want) == len(calls) {
					t.Fatalf("%s: fixture does not discriminate (%d rows)", tc.name, len(want))
				}
				if c := must("count", "appels", Request{Query: Query{Where: tc.where()}})["count"]; c != strconv.Itoa(len(want)) {
					t.Fatalf("%s: count %v want %d", tc.name, c, len(want))
				}
				// Permissions apply before sorting and pagination, across a cursor.
				var got []string
				q := Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 13, Select: []string{"id", "n"}}
				for {
					v := must("find", "appels", Request{Query: q})
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
				// Each call counts once, however many sales match it.
				groups := must("aggregate", "appels", Request{Query: Query{Where: tc.where()}, GroupBy: []string{"centre_id"}, Metrics: []Metric{{"n", "count", "", nil}, {"total", "sum", "n", nil}}})["rows"].([]Record)
				if len(groups) != len(byCentre) {
					t.Fatalf("%s: groups %v want %v", tc.name, groups, byCentre)
				}
				for _, g := range groups {
					if g["n"] != strconv.Itoa(byCentre[g["centre_id"].(string)]) {
						t.Fatalf("%s: group %v want %v", tc.name, g, byCentre)
					}
				}
				batch := must("batch", "", Request{Operations: []Operation{
					{Op: "count", Request: Request{Database: "d", Collection: "appels", Query: Query{Where: tc.where()}}},
					{Op: "find", Request: Request{Database: "d", Collection: "appels", Query: Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 1000}}},
					{Op: "aggregate", Request: Request{Database: "d", Collection: "appels", Query: Query{Where: tc.where()}, Metrics: []Metric{{"n", "count", "", nil}}}},
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
			scoped := &Filter{And: []Filter{{Field: "centre_id", Op: "in", Value: []any{"centre-0"}}, {Exists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: positive}}}}
			plan := must("explain", "appels", Request{Query: Query{Where: scoped, RequireIndex: true}})
			if text := string(marshal(plan)); adapter != "sqlite" && !strings.Contains(text, `"index":"by_centre"`) || adapter == "sqlite" && !strings.Contains(text, `"indexedLookup":true`) {
				t.Fatal(text)
			}

			exists := func(c *ExistsClause) *Filter { return &Filter{Exists: c} }
			on := []ExistsOn{{LocalField: "vente_id", ForeignField: "local_id"}}
			for name, where := range map[string]*Filter{
				"unknown local":   exists(&ExistsClause{Collection: "ventes", On: []ExistsOn{{LocalField: "nope", ForeignField: "local_id"}}}),
				"unknown foreign": exists(&ExistsClause{Collection: "ventes", On: []ExistsOn{{LocalField: "vente_id", ForeignField: "nope"}}}),
				"mixed types":     exists(&ExistsClause{Collection: "ventes", On: []ExistsOn{{LocalField: "vente_id", ForeignField: "id"}}}),
				"bad comparison":  exists(&ExistsClause{Collection: "ventes", On: []ExistsOn{{LocalField: "vente_id", ForeignField: "id", Comparison: "numeric"}}}),
				"no pairs":        exists(&ExistsClause{Collection: "ventes"}),
				"nested":          exists(&ExistsClause{Collection: "ventes", On: on, Where: exists(&ExistsClause{Collection: "appels", On: on})}),
				"bad where":       exists(&ExistsClause{Collection: "ventes", On: on, Where: &Filter{Field: "nope", Op: "eq", Value: "x"}}),
				"with op":         {Op: "eq", Exists: &ExistsClause{Collection: "ventes", On: on}},
				"both":            {Exists: &ExistsClause{Collection: "ventes", On: on}, NotExists: &ExistsClause{Collection: "ventes", On: on}},
				"null in not_in":  {Field: "local_id", Op: "not_in", Value: []any{nil}},
				"empty not_in":    {Field: "local_id", Op: "not_in", Value: []any{}},
			} {
				if _, e := exec("count", "appels", Request{Query: Query{Where: where}}); e == nil {
					t.Fatalf("%s accepted", name)
				}
			}
			if _, e := exec("count", "appels", Request{Query: Query{Where: exists(&ExistsClause{Collection: "missing", On: on})}}); e == nil {
				t.Fatal("missing collection accepted")
			}
			clause := exists(&ExistsClause{Collection: "ventes", On: on})
			if _, e := exec("aggregate", "appels", Request{Metrics: []Metric{{"n", "count", "", clause}}}); e == nil {
				t.Fatal("metric exists accepted")
			}
			for _, op := range []string{"update", "delete"} {
				r := Request{Query: Query{Where: clause}, MaxAffected: 1000}
				if op == "update" {
					r.Set = Record{"vente_id": "x"}
				}
				if _, e := exec(op, "appels", r); e == nil {
					t.Fatalf("%s with exists accepted", op)
				}
			}
			// not_in: null is not satisfied, and a one-value list equals ne.
			notIn := func(values ...any) string {
				return must("count", "ventes", Request{Query: Query{Where: &Filter{Field: "offre_id", Op: "not_in", Value: values}}})["count"].(string)
			}
			wantIn := func(list ...string) string {
				n := 0
				for _, s := range sales {
					if s.offer != nil && !inList(s.offer, list) {
						n++
					}
				}
				return strconv.Itoa(n)
			}
			if got := notIn("offer-0", "offer-1"); got != wantIn("offer-0", "offer-1") {
				t.Fatalf("not_in %s want %s", got, wantIn("offer-0", "offer-1"))
			}
			if got := notIn("offer-0"); got != wantIn("offer-0") {
				t.Fatalf("not_in one %s want %s", got, wantIn("offer-0"))
			}
			if c := must("count", "ventes", Request{Query: Query{Where: &Filter{Field: "id", Op: "not_in", Value: []any{"1", "2"}}}})["count"]; c != strconv.Itoa(len(sales)-2) {
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
	plan := &existsPlan{tx: tx, alts: []existsAlternative{{on: ExistsOn{LocalField: "a", ForeignField: "b"}, local: Field{Type: "text"}, foreign: Field{Type: "text"}}}, state: state}
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

// BenchmarkExistsPolicy measures the offer-restricted policy at the local CRM's
// size: 125,000 calls and 35,000 sales. A third of the calls have related sales.
func BenchmarkExistsPolicy(b *testing.B) {
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
	exec("collection_create", "appels", Request{Fields: []Field{{"id", "text", false}, {"centre_id", "text", false}, {"n", "integer", false}, {"vente_id", "text", true}, {"local_id", "text", true}, {"raw_call_id", "text", true}}})
	exec("collection_create", "ventes", Request{Fields: []Field{{"native_key", "text", false}, {"id", "integer", false}, {"local_id", "text", true}, {"appel_id", "text", true}, {"appel_source_id", "text", true}, {"offre_id", "text", true}, {"centre_id", "text", true}}, PrimaryKey: []string{"native_key"}})
	exec("index_create", "appels", Request{Index: &Index{Name: "by_centre", Fields: []Order{{"centre_id", "asc"}, {"n", "desc"}}}})
	for _, f := range []string{"id", "local_id", "appel_id", "appel_source_id"} {
		exec("index_create", "ventes", Request{Index: &Index{Name: "by_" + f, Fields: []Order{{f, "asc"}}}})
	}
	rng := rand.New(rand.NewSource(2))
	for i := 0; i < 125000; i += 1000 {
		batch := make([]Record, 1000)
		for j := range batch {
			n := i + j
			r := Record{"id": fmt.Sprintf("c%06d", n), "centre_id": fmt.Sprintf("centre-%02d", rng.Intn(30)), "n": strconv.Itoa(n), "vente_id": nil, "local_id": fmt.Sprintf("C%06d", n), "raw_call_id": fmt.Sprintf("R%06d", n)}
			if n%3 == 0 {
				r["vente_id"] = fmt.Sprintf("L%06d", n)
			}
			batch[j] = r
		}
		exec("insert", "appels", Request{Records: batch})
	}
	for i := 0; i < 35000; i += 1000 {
		batch := make([]Record, 1000)
		for j := range batch {
			n := i + j
			call := n * 3
			batch[j] = Record{"native_key": fmt.Sprintf("k%06d", n), "id": strconv.Itoa(n + 1), "local_id": fmt.Sprintf("L%06d", call), "appel_id": fmt.Sprintf("C%06d", call), "appel_source_id": fmt.Sprintf("R%06d", call), "offre_id": fmt.Sprintf("offer-%d", rng.Intn(12)), "centre_id": fmt.Sprintf("centre-%02d", rng.Intn(30))}
		}
		exec("insert", "ventes", Request{Records: batch})
	}
	grant := []any{"offer-0", "offer-1", "offer-2", "offer-3", "offer-4", "offer-5", "offer-6", "offer-7", "offer-8", "offer-9", "offer-10"}
	policy := func(scope ...Filter) *Filter {
		return &Filter{And: append(scope,
			Filter{Exists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: &Filter{Field: "offre_id", Op: "in", Value: grant}}},
			Filter{NotExists: &ExistsClause{Collection: "ventes", On: callSaleOn(), Where: &Filter{Field: "offre_id", Op: "not_in", Value: grant}}})}
	}
	one := Filter{Field: "centre_id", Op: "eq", Value: "centre-07"}
	two := Filter{Field: "centre_id", Op: "in", Value: []any{"centre-07", "centre-08"}}
	for _, tc := range []struct {
		name  string
		where func() *Filter
	}{
		{"one_centre_no_policy", func() *Filter { return &Filter{And: []Filter{one}} }},
		{"one_centre", func() *Filter { return policy(one) }},
		{"two_centres", func() *Filter { return policy(two) }},
		{"all_calls", func() *Filter { return policy() }},
	} {
		b.Run("count/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("count", "appels", Request{Query: Query{Where: tc.where()}})
			}
		})
		b.Run("find50/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("find", "appels", Request{Query: Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 50}})
			}
		})
		b.Run("group/"+tc.name, func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				exec("aggregate", "appels", Request{Query: Query{Where: tc.where()}, GroupBy: []string{"centre_id"}, Metrics: []Metric{{"n", "count", "", nil}}})
			}
		})
	}
}

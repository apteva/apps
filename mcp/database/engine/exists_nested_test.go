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

type nestedFixture struct {
	customers, orders, payments, refunds []Record
}

func newNestedFixture() nestedFixture {
	rng := rand.New(rand.NewSource(11))
	var f nestedFixture
	ref := func(prefix string, n int) any {
		switch rng.Intn(10) {
		case 0:
			return nil
		case 1:
			return "" // '' = '' matches in SQL
		}
		return fmt.Sprintf("%s%d", prefix, rng.Intn(n))
	}
	for i := 0; i < 300; i++ {
		f.customers = append(f.customers, Record{"id": fmt.Sprintf("c%d", i), "n": strconv.Itoa(i), "region": fmt.Sprintf("r%d", rng.Intn(3)), "account": ref("a", 60)})
	}
	for i := 0; i < 400; i++ {
		f.orders = append(f.orders, Record{"id": fmt.Sprintf("o%d", i), "number": strconv.Itoa(rng.Intn(500)), "customer_id": ref("c", 300), "account": ref("a", 60), "owner": ref("c", 300), "status": fmt.Sprintf("s%d", rng.Intn(3))})
	}
	for i := 0; i < 500; i++ {
		// The order's number arrives as text here, as an integer on orders.
		f.payments = append(f.payments, Record{"id": fmt.Sprintf("p%d", i), "order_id": ref("o", 400), "order_number": strconv.Itoa(rng.Intn(500)), "state": fmt.Sprintf("x%d", rng.Intn(4))})
	}
	for i := 0; i < 200; i++ {
		f.refunds = append(f.refunds, Record{"id": fmt.Sprintf("f%d", i), "payment_id": ref("p", 500), "kind": fmt.Sprintf("k%d", rng.Intn(2))})
	}
	return f
}

func same(a, b any) bool { return a != nil && b != nil && a == b }

func (f nestedFixture) ordersOf(c Record) []Record {
	out := []Record{}
	for _, o := range f.orders {
		if same(o["customer_id"], c["id"]) {
			out = append(out, o)
		}
	}
	return out
}

func (f nestedFixture) paymentsOf(o Record) []Record {
	out := []Record{}
	for _, p := range f.payments {
		if same(p["order_id"], o["id"]) {
			out = append(out, p)
		}
	}
	return out
}

func (f nestedFixture) refundsOf(p Record) []Record {
	out := []Record{}
	for _, r := range f.refunds {
		if same(r["payment_id"], p["id"]) {
			out = append(out, r)
		}
	}
	return out
}

func anyRecord(rows []Record, test func(Record) bool) bool {
	for _, r := range rows {
		if test(r) {
			return true
		}
	}
	return false
}

// Nested clauses, checked against a brute-force reference through count, paged
// find, grouped aggregate and read batch, on probes and on key sets.
func TestNestedExists(t *testing.T) {
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
		t.Run(mode.name, testNestedExists)
		existsSetsBuilt = nil
		if mode.name == "sets at once" && built.Load() == 0 {
			t.Fatal("no key set was built")
		}
	}
}

func testNestedExists(t *testing.T) {
	fx := newNestedFixture()
	onOrder := []ExistsOn{{LocalField: "id", ForeignField: "customer_id"}}
	onPayment := []ExistsOn{{LocalField: "id", ForeignField: "order_id"}}
	onRefund := []ExistsOn{{LocalField: "id", ForeignField: "payment_id"}}
	stateIs := func(v string) *Filter { return &Filter{Field: "state", Op: "eq", Value: v} }
	payment := func(where *Filter) *Filter {
		return &Filter{Exists: &ExistsClause{Collection: "payments", On: onPayment, Where: where}}
	}
	noPayment := func(where *Filter) *Filter {
		return &Filter{NotExists: &ExistsClause{Collection: "payments", On: onPayment, Where: where}}
	}
	order := func(where *Filter) *Filter {
		return &Filter{Exists: &ExistsClause{Collection: "orders", On: onOrder, Where: where}}
	}
	noOrder := func(where *Filter) *Filter {
		return &Filter{NotExists: &ExistsClause{Collection: "orders", On: onOrder, Where: where}}
	}
	refund := func(where *Filter) *Filter {
		return &Filter{Exists: &ExistsClause{Collection: "refunds", On: onRefund, Where: where}}
	}
	noRefund := func(where *Filter) *Filter {
		return &Filter{NotExists: &ExistsClause{Collection: "refunds", On: onRefund, Where: where}}
	}
	hasState := func(p Record, v string) bool { return p["state"] == v }
	cases := []struct {
		name  string
		where func() *Filter
		want  func(Record) bool
	}{
		{"exists of exists", func() *Filter { return order(payment(stateIs("x0"))) }, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x0") })
			})
		}},
		{"exists of not_exists", func() *Filter { return order(noPayment(stateIs("x0"))) }, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return !anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x0") })
			})
		}},
		{"not_exists of exists", func() *Filter { return noOrder(payment(stateIs("x0"))) }, func(c Record) bool {
			return !anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x0") })
			})
		}},
		{"not_exists of not_exists", func() *Filter { return noOrder(noPayment(nil)) }, func(c Record) bool {
			return !anyRecord(fx.ordersOf(c), func(o Record) bool { return len(fx.paymentsOf(o)) == 0 })
		}},
		{"where beside a nested node", func() *Filter {
			return order(&Filter{And: []Filter{{Field: "status", Op: "eq", Value: "s1"}, *payment(nil), *noPayment(stateIs("x3"))}})
		}, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return o["status"] == "s1" && len(fx.paymentsOf(o)) > 0 && !anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x3") })
			})
		}},
		{"or inside a nested where", func() *Filter {
			return order(&Filter{Or: []Filter{{Field: "status", Op: "eq", Value: "s2"}, *payment(stateIs("x1"))}})
		}, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return o["status"] == "s2" || anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x1") })
			})
		}},
		{"three levels", func() *Filter { return order(payment(noRefund(&Filter{Field: "kind", Op: "eq", Value: "k0"}))) }, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.paymentsOf(o), func(p Record) bool {
					return !anyRecord(fx.refundsOf(p), func(r Record) bool { return r["kind"] == "k0" })
				})
			})
		}},
		{"three levels, not_exists on top", func() *Filter { return noOrder(payment(refund(nil))) }, func(c Record) bool {
			return !anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.paymentsOf(o), func(p Record) bool { return len(fx.refundsOf(p)) > 0 })
			})
		}},
		{"siblings and scope", func() *Filter {
			return &Filter{And: []Filter{{Field: "region", Op: "in", Value: []any{"r0", "r1"}}, *order(payment(stateIs("x0"))), *noOrder(payment(stateIs("x2")))}}
		}, func(c Record) bool {
			return c["region"] != "r2" &&
				anyRecord(fx.ordersOf(c), func(o Record) bool {
					return anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x0") })
				}) &&
				!anyRecord(fx.ordersOf(c), func(o Record) bool {
					return anyRecord(fx.paymentsOf(o), func(p Record) bool { return hasState(p, "x2") })
				})
		}},
		// Local fields come from the immediate parent: account here is the
		// order's, not the customer's of the same name.
		{"same field name on two levels", func() *Filter {
			return order(&Filter{Exists: &ExistsClause{Collection: "customers", On: []ExistsOn{{LocalField: "account", ForeignField: "account"}}, Where: &Filter{Field: "region", Op: "eq", Value: "r1"}}})
		}, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.customers, func(other Record) bool { return same(other["account"], o["account"]) && other["region"] == "r1" })
			})
		}},
		// Back to the first collection: the inner table needs its own alias.
		{"cycle through the same collection", func() *Filter {
			return order(&Filter{NotExists: &ExistsClause{Collection: "customers", On: []ExistsOn{{LocalField: "owner", ForeignField: "id"}}, Where: &Filter{Field: "region", Op: "eq", Value: "r0"}}})
		}, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return !anyRecord(fx.customers, func(other Record) bool { return same(other["id"], o["owner"]) && other["region"] == "r0" })
			})
		}},
		{"anyOf and text comparison at the nested level", func() *Filter {
			return order(&Filter{Exists: &ExistsClause{Collection: "payments", AnyOf: []ExistsMatch{{On: onPayment}, {On: []ExistsOn{{LocalField: "number", ForeignField: "order_number", Comparison: "text"}}}}, Where: stateIs("x2")}})
		}, func(c Record) bool {
			return anyRecord(fx.ordersOf(c), func(o Record) bool {
				return anyRecord(fx.payments, func(p Record) bool {
					return hasState(p, "x2") && (same(p["order_id"], o["id"]) || p["order_number"] == o["number"])
				})
			})
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
			must := func(op, coll string, r Request) map[string]any {
				t.Helper()
				r.Database, r.Collection = "d", coll
				v, e := m.Execute(ctx, "p", op, r)
				if e != nil {
					t.Fatal(op, " ", coll, " ", e)
				}
				out, _ := v.(map[string]any)
				return out
			}
			if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			text := func(name string) Field { return Field{name, "text", true} }
			must("collection_create", "customers", Request{Fields: []Field{{"id", "text", false}, {"n", "integer", false}, text("region"), text("account")}, PrimaryKey: []string{"id"}})
			must("collection_create", "orders", Request{Fields: []Field{{"id", "text", false}, {"number", "integer", false}, text("customer_id"), text("account"), text("owner"), text("status")}, PrimaryKey: []string{"id"}})
			must("collection_create", "payments", Request{Fields: []Field{{"id", "text", false}, text("order_id"), text("order_number"), text("state")}, PrimaryKey: []string{"id"}})
			must("collection_create", "refunds", Request{Fields: []Field{{"id", "text", false}, text("payment_id"), text("kind")}, PrimaryKey: []string{"id"}})
			must("index_create", "customers", Request{Index: &Index{Name: "by_account", Fields: []Order{{"account", "asc"}}}})
			must("index_create", "orders", Request{Index: &Index{Name: "by_customer", Fields: []Order{{"customer_id", "asc"}}}})
			must("index_create", "payments", Request{Index: &Index{Name: "by_order", Fields: []Order{{"order_id", "asc"}}}})
			must("index_create", "payments", Request{Index: &Index{Name: "by_number", Fields: []Order{{"order_number", "asc"}}}})
			must("index_create", "refunds", Request{Index: &Index{Name: "by_payment", Fields: []Order{{"payment_id", "asc"}}}})
			for name, rows := range map[string][]Record{"customers": fx.customers, "orders": fx.orders, "payments": fx.payments, "refunds": fx.refunds} {
				for i := 0; i < len(rows); i += 100 {
					must("insert", name, Request{Records: rows[i:min(i+100, len(rows))]})
				}
			}
			for _, tc := range cases {
				var want []string
				byRegion := map[string]int{}
				for i := len(fx.customers) - 1; i >= 0; i-- {
					if tc.want(fx.customers[i]) {
						want = append(want, fx.customers[i]["id"].(string))
						byRegion[fx.customers[i]["region"].(string)]++
					}
				}
				if len(want) == 0 || len(want) == len(fx.customers) {
					t.Fatalf("%s: fixture does not discriminate (%d rows)", tc.name, len(want))
				}
				if c := must("count", "customers", Request{Query: Query{Where: tc.where()}})["count"]; c != strconv.Itoa(len(want)) {
					t.Fatalf("%s: count %v want %d", tc.name, c, len(want))
				}
				var got []string
				q := Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 13, Select: []string{"id", "n"}}
				for {
					v := must("find", "customers", Request{Query: q})
					for _, r := range v["records"].([]Record) {
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
				groups := must("aggregate", "customers", Request{Query: Query{Where: tc.where()}, GroupBy: []string{"region"}, Metrics: []Metric{{"n", "count", "", nil}}})["rows"].([]Record)
				if len(groups) != len(byRegion) {
					t.Fatalf("%s: groups %v want %v", tc.name, groups, byRegion)
				}
				for _, g := range groups {
					if g["n"] != strconv.Itoa(byRegion[g["region"].(string)]) {
						t.Fatalf("%s: group %v want %v", tc.name, g, byRegion)
					}
				}
				batch := must("batch", "", Request{Operations: []Operation{
					{Op: "count", Request: Request{Database: "d", Collection: "customers", Query: Query{Where: tc.where()}}},
					{Op: "find", Request: Request{Database: "d", Collection: "customers", Query: Query{Where: tc.where(), OrderBy: []Order{{"n", "desc"}}, Limit: 1000}}},
				}})["results"].([]any)
				if c := batch[0].(map[string]any)["count"]; c != strconv.Itoa(len(want)) {
					t.Fatalf("%s: batch count %v", tc.name, c)
				}
				if n := len(batch[1].(map[string]any)["records"].([]Record)); n != len(want) {
					t.Fatalf("%s: batch find %d", tc.name, n)
				}
			}

			reject := func(name, code string, where *Filter) {
				t.Helper()
				_, e := m.Execute(ctx, "p", "count", Request{Database: "d", Collection: "customers", Query: Query{Where: where}})
				var ee *Error
				if e == nil || code != "" && (!errors.As(e, &ee) || ee.Code != code) {
					t.Fatalf("%s: %v", name, e)
				}
			}
			// A local field the parent lacks is rejected, though the grandparent has it.
			reject("grandparent field", "", order(&Filter{Exists: &ExistsClause{Collection: "payments", On: []ExistsOn{{LocalField: "region", ForeignField: "state"}}}}))
			reject("no index at the nested level", "query_too_expensive", order(&Filter{Exists: &ExistsClause{Collection: "payments", On: []ExistsOn{{LocalField: "status", ForeignField: "state"}}}}))
			reject("unknown field in a nested where", "", order(payment(&Filter{Field: "status", Op: "eq", Value: "s0"})))
			// Four levels pass; a fifth does not.
			back := func(where *Filter) *Filter {
				return &Filter{Exists: &ExistsClause{Collection: "payments", On: []ExistsOn{{LocalField: "payment_id", ForeignField: "order_id"}}, Where: where}}
			}
			must("count", "customers", Request{Query: Query{Where: order(payment(refund(back(nil))))}})
			reject("five levels", "", order(payment(refund(back(refund(nil))))))
			// Nodes count across the tree.
			wide := []Filter{}
			for i := 0; i < maxExistsNodes-1; i++ {
				wide = append(wide, *payment(nil))
			}
			must("count", "customers", Request{Query: Query{Where: order(&Filter{And: wide[:maxExistsNodes-1]})}})
			reject("one node over the limit", "", order(&Filter{And: append(wide, *payment(nil))}))

			// Past the budget the operation fails, also under not_exists, where a
			// failed evaluation would otherwise read as a match.
			if adapter != "sqlite" {
				defer func(v int64) { existsNestedWork = v }(existsNestedWork)
				existsNestedWork = 1
				for _, where := range []*Filter{noOrder(payment(nil)), order(noPayment(nil))} {
					for _, op := range []string{"count", "find", "aggregate"} {
						r := Request{Database: "d", Collection: "customers", Query: Query{Where: where}}
						if op == "aggregate" {
							r.Metrics = []Metric{{"n", "count", "", nil}}
						}
						_, e := m.Execute(ctx, "p", op, r)
						var ee *Error
						if !errors.As(e, &ee) || ee.Code != "query_too_expensive" {
							t.Fatalf("%s over budget: %v", op, e)
						}
					}
				}
				existsNestedWork = 16 << 20
			}
			cancelled, cancel := context.WithCancel(ctx)
			cancel()
			if _, e := m.Execute(cancelled, "p", "count", Request{Database: "d", Collection: "customers", Query: Query{Where: noOrder(payment(nil))}}); e == nil {
				t.Fatal("cancelled operation returned a result")
			}
		})
	}
}

// nestedStub evaluates a filter over fixed rows the way an adapter does.
type nestedStub struct {
	transaction
	rows []Record
}

func (s *nestedStub) find(c Collection, q Query, limit int) ([]Record, error) {
	out := []Record{}
	for _, r := range s.rows {
		if matches(c, r, q.Where) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (s *nestedStub) scanRecords(c Collection, _ Query, _ []string, consume func(Record) bool) error {
	for _, r := range s.rows {
		if !consume(r) {
			return nil
		}
	}
	return nil
}

// A failing child under not_exists reads as a match, so the failure must reach
// the shared state, and no key set read past it may be kept.
func TestNestedExistsFailureFailsClosed(t *testing.T) {
	text := Field{Type: "text"}
	pair := func(local, foreign string) existsAlternative {
		return existsAlternative{pairs: []existsPair{{on: ExistsOn{LocalField: local, ForeignField: foreign}, local: text, foreign: text}}, set: foreign}
	}
	state := &existsState{}
	child := &existsPlan{tx: &failingFind{}, alts: []existsAlternative{pair("id", "parent")}, state: state, noSets: true}
	where := &Filter{NotExists: &ExistsClause{plan: child}}
	rows := []Record{{"id": "o1", "owner": "c1"}}
	parent := &existsPlan{tx: &nestedStub{rows: rows}, alts: []existsAlternative{pair("id", "owner")}, where: where, state: state, noSets: true}
	matchesExists(Record{"id": "c1"}, &Filter{Exists: &ExistsClause{plan: parent}})
	if state.failure() == nil {
		t.Fatal("probe: nested failure was lost")
	}

	state = &existsState{}
	child = &existsPlan{tx: &failingFind{}, alts: []existsAlternative{pair("id", "parent")}, state: state, noSets: true}
	where = &Filter{NotExists: &ExistsClause{plan: child}}
	parent = &existsPlan{tx: &nestedStub{rows: rows}, alts: []existsAlternative{pair("id", "owner")}, where: where, state: state, probes: 1000}
	if e := parent.buildSets(); e == nil {
		t.Fatal("key set: nested failure was lost")
	}
	if parent.sets != nil || state.setBytes != 0 {
		t.Fatalf("kept a key set read past a failure: %v, %d bytes", parent.sets, state.setBytes)
	}
}

// BenchmarkNestedExists: records -> related -> restricted, at the local app's
// size: 5,000 records, 125,000 related, 35,000 restricted. One exists of an
// exists, and one not_exists of an exists.
func BenchmarkNestedExists(b *testing.B) {
	m, e := Open(b.TempDir())
	if e != nil {
		b.Fatal(e)
	}
	defer m.Close()
	ctx := context.Background()
	must := func(op, coll string, r Request) {
		r.Database, r.Collection = "d", coll
		if _, e := m.Execute(ctx, "p", op, r); e != nil {
			b.Fatal(op, e)
		}
	}
	if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: "aptevadb"}); e != nil {
		b.Fatal(e)
	}
	must("collection_create", "records", Request{Fields: []Field{{"id", "text", false}, {"scope", "text", false}, {"group_id", "text", false}}, PrimaryKey: []string{"id"}})
	must("collection_create", "related", Request{Fields: []Field{{"id", "text", false}, {"group_id", "text", false}, {"pad", "text", true}}, PrimaryKey: []string{"id"}})
	must("collection_create", "restricted", Request{Fields: []Field{{"id", "text", false}, {"related_id", "text", false}, {"kind", "text", false}}, PrimaryKey: []string{"id"}})
	must("index_create", "records", Request{Index: &Index{Name: "by_scope", Fields: []Order{{"scope", "asc"}}}})
	must("index_create", "related", Request{Index: &Index{Name: "by_group", Fields: []Order{{"group_id", "asc"}}}})
	must("index_create", "restricted", Request{Index: &Index{Name: "by_related", Fields: []Order{{"related_id", "asc"}}}})
	rng := rand.New(rand.NewSource(3))
	fill := func(coll string, n int, row func(i int) Record) {
		rows := []Record{}
		for i := 0; i < n; i++ {
			rows = append(rows, row(i))
			if len(rows) == 1000 || i == n-1 {
				must("insert", coll, Request{Records: rows})
				rows = rows[:0]
			}
		}
	}
	fill("records", 5000, func(i int) Record {
		return Record{"id": fmt.Sprintf("a%d", i), "scope": fmt.Sprintf("s%d", i%32), "group_id": fmt.Sprintf("g%d", rng.Intn(5000))}
	})
	pad := strings.Repeat("x", 400)
	fill("related", 125000, func(i int) Record {
		return Record{"id": fmt.Sprintf("b%d", i), "group_id": fmt.Sprintf("g%d", rng.Intn(5000)), "pad": pad}
	})
	fill("restricted", 35000, func(i int) Record {
		return Record{"id": fmt.Sprintf("c%d", i), "related_id": fmt.Sprintf("b%d", rng.Intn(125000)), "kind": fmt.Sprintf("k%d", rng.Intn(4))}
	})
	for _, tc := range []struct {
		name  string
		where func() *Filter
	}{
		{"exists of exists", func() *Filter {
			return &Filter{Exists: &ExistsClause{Collection: "related", On: []ExistsOn{{LocalField: "group_id", ForeignField: "group_id"}}, Where: &Filter{Exists: &ExistsClause{Collection: "restricted", On: []ExistsOn{{LocalField: "id", ForeignField: "related_id"}}, Where: &Filter{Field: "kind", Op: "eq", Value: "k0"}}}}}
		}},
		{"not_exists of exists", func() *Filter {
			return &Filter{NotExists: &ExistsClause{Collection: "related", On: []ExistsOn{{LocalField: "group_id", ForeignField: "group_id"}}, Where: &Filter{Exists: &ExistsClause{Collection: "restricted", On: []ExistsOn{{LocalField: "id", ForeignField: "related_id"}}, Where: &Filter{Field: "kind", Op: "eq", Value: "k0"}}}}}
		}},
	} {
		scoped := func() *Filter {
			return &Filter{And: []Filter{{Field: "scope", Op: "eq", Value: "s1"}, *tc.where()}}
		}
		for _, run := range []struct {
			name string
			op   string
			q    func() Query
		}{
			{"count/scope", "count", func() Query { return Query{Where: scoped()} }},
			{"find50/scope", "find", func() Query { return Query{Where: scoped(), Limit: 50} }},
			{"count/all", "count", func() Query { return Query{Where: tc.where()} }},
		} {
			b.Run(tc.name+"/"+run.name, func(b *testing.B) {
				for i := 0; i < b.N; i++ {
					if _, e := m.Execute(ctx, "p", run.op, Request{Database: "d", Collection: "records", Query: run.q()}); e != nil {
						b.Fatal(e)
					}
				}
			})
		}
	}
}

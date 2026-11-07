package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

func queryFixture(t *testing.T, rt relT) {
	relCollection(rt, "facts", "id", "id text,centre text,period text,caller text,kind text,contracts integer,campaign text,adviser text,display_date text", "centre")
	body, e := os.ReadFile("../testdata/dependent-records.json")
	if e != nil {
		t.Fatal(e)
	}
	var rows []map[string]any
	if e := decode(body, &rows); e != nil {
		t.Fatal(e)
	}
	relInsert(rt, "facts", rows...)
}

func queryRequest(t *testing.T) Request {
	t.Helper()
	var r Request
	body, e := os.ReadFile("../testdata/dependent-aggregation.json")
	if e != nil {
		t.Fatal(e)
	}
	if e := decode(body, &r); e != nil {
		t.Fatal(e)
	}
	r.Database = "d"
	return r
}
func runQuery(t *testing.T, rt relT, r Request) map[string]any {
	t.Helper()
	v, e := rt.m.Execute(context.Background(), "p", "query", r)
	if e != nil {
		t.Fatal(e)
	}
	var out map[string]any
	if e := decode(marshal(v), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func queryTotal(t *testing.T, rt relT, r Request) any {
	t.Helper()
	return relList(runQuery(t, rt, r)["rows"])[0]["revenue_cents"]
}
func requireQueryError(t *testing.T, rt relT, r Request, code string) {
	t.Helper()
	out, e := rt.m.Execute(context.Background(), "p", "query", r)
	var known *Error
	if !errors.As(e, &known) || known.Code != code || out != nil {
		t.Fatalf("wanted %s with no result, got %v / %v", code, out, e)
	}
}
func TestQueryDependentAggregation(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := queryRequest(t)
		relEqual(t, "period-valued total", queryTotal(t, rt, r), "6000")
		// 1/2 and 1/4 cannot be replaced by the pooled 2/6 rate.
		r.Result.From = "periods"
		r.Result.OrderBy = []Order{{"period", "desc"}}
		rows := relList(runQuery(t, rt, r)["rows"])
		relEqual(t, "deduplicated September callers", rows[0]["callers"], "2")
		relEqual(t, "October callers", rows[1]["callers"], "4")
		r.Result.From = "rates"
		rows = relList(runQuery(t, rt, r)["rows"])
		relEqual(t, "September rate", rows[0]["rate_cents"], "4000")
		relEqual(t, "October rate", rows[1]["rate_cents"], "2000")
		for _, field := range []string{"sale.centre", "sale.campaign", "sale.adviser", "sale.display_date"} {
			r = queryRequest(t)
			r.Nodes[6].GroupBy = []string{field}
			rows = relList(runQuery(t, rt, r)["rows"])
			if field == "sale.centre" {
				relEqual(t, "centre count", len(rows), 1)
				relEqual(t, "centre revenue", rows[0]["revenue_cents"], "6000")
			} else {
				values := map[any]bool{}
				for _, row := range rows {
					values[row["revenue_cents"]] = true
				}
				if len(rows) != 2 || !values["4000"] || !values["2000"] {
					t.Fatalf("%s: %v", field, rows)
				}
			}
		}
		// The display selection applies to sales, after period metrics are calculated.
		r = queryRequest(t)
		r.Nodes[3].Where = &Filter{And: []Filter{{Field: "kind", Op: "eq", Value: "sale"}, {Field: "display_date", Op: "eq", Value: "2026-09-30"}}}
		relEqual(t, "display window preserves period", queryTotal(t, rt, r), "4000")
		// Reusing the same input does not mutate its filters or shared rows.
		before := string(marshal(r))
		runQuery(t, rt, r)
		relEqual(t, "request immutable", string(marshal(r)), before)
	})
}
func TestQueryEmptyAndMissingPeriods(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := queryRequest(t)
		r.Nodes[0].Where.Value = "none"
		row := relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "empty sum", row["revenue_cents"], nil)
		r.Result.From = "periods"
		relEqual(t, "no invented groups", len(relList(runQuery(t, rt, r)["rows"])), 0)
		relInsert(rt, "facts", Record{"id": "empty_period_call", "centre": "a", "period": "nov", "kind": "call", "caller": "x"}, Record{"id": "zero_caller_sale", "centre": "a", "period": "dec", "kind": "sale", "contracts": "1"})
		r = queryRequest(t)
		relEqual(t, "zero denominator takes explicit else", queryTotal(t, rt, r), "8000")
		r.Nodes[6].Metrics = []Metric{{Name: "n", Op: "count"}}
		r.Nodes[0].Where.Value = "none"
		row = relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "empty count", row["n"], "0")
	})
}
func TestQueryValidationAndBudgets(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		cases := map[string]func(*Request){
			"version":              func(r *Request) { r.Version = 0 },
			"unknown dependency":   func(r *Request) { r.Nodes[1].From = "absent" },
			"duplicate name":       func(r *Request) { r.Nodes[1].Name = "facts" },
			"cycle":                func(r *Request) { r.Nodes[3].From = "total" },
			"unknown field":        func(r *Request) { r.Nodes[1].GroupBy = []string{"absent"} },
			"metadata not carried": func(r *Request) { r.Nodes[1].GroupBy = []string{"_version"} },
			"wrong numeric type":   func(r *Request) { r.Nodes[5].Computed[0].Expr.Args[0].Field = "sale.kind" },
			"wrong join type":      func(r *Request) { r.Nodes[4].Joins[0].On[0].Foreign = "contracts" },
			"irrelevant argument":  func(r *Request) { r.Nodes[1].Computed = []RelComputed{{Name: "x"}} },
			"unknown operator":     func(r *Request) { r.Nodes[5].Computed[0].Expr.Op = "eval" },
			"invalid predicate":    func(r *Request) { r.Nodes[3].Predicate = &RelExpr{Field: "kind"} },
			"invalid rounding":     func(r *Request) { r.Nodes[5].Computed[0].Expr = RelExpr{Op: "round", Args: []RelExpr{{Const: "1"}}} },
			"raise limits":         func(r *Request) { r.Limits = &QueryLimits{IntermediateRows: maxQueryRows + 1} },
			"unused invalid node": func(r *Request) {
				r.Nodes = append(r.Nodes, QueryNode{Name: "unused", Op: "scan", Collection: "missing"})
			},
		}
		for name, change := range cases {
			t.Run(name, func(t *testing.T) {
				r := queryRequest(t)
				change(&r)
				code := "invalid_argument"
				if name == "unused invalid node" {
					code = "not_found"
				}
				requireQueryError(t, rt, r, code)
			})
		}
		for name, limits := range map[string]*QueryLimits{"rows": {IntermediateRows: 2}, "memory": {MemoryBytes: 100}, "work": {Work: 2}} {
			t.Run(name, func(t *testing.T) {
				r := queryRequest(t)
				r.Limits = limits
				code := "resource_limit"
				if name == "work" {
					code = "query_too_expensive"
				}
				requireQueryError(t, rt, r, code)
			})
		}
		r := queryRequest(t)
		r.Nodes[0].MaxRows = 2
		requireQueryError(t, rt, r, "resource_limit")
		r = queryRequest(t)
		r.Nodes[2].Computed[0].Expr = RelExpr{Const: "9223372036854775807"}
		requireQueryError(t, rt, r, "resource_limit") // SUM overflows.
		r = queryRequest(t)
		r.Nodes[4].Joins[0].On = []RelOn{{Local: "sale.centre", Foreign: "centre"}}
		requireQueryError(t, rt, r, "cardinality_violation")
		r = queryRequest(t)
		r.Result.From = "rates"
		r.Result.Limit = 1
		requireQueryError(t, rt, r, "resource_limit")
		// Declaration order is not execution order.
		r = queryRequest(t)
		for i, j := 0, len(r.Nodes)-1; i < j; i, j = i+1, j-1 {
			r.Nodes[i], r.Nodes[j] = r.Nodes[j], r.Nodes[i]
		}
		relEqual(t, "forward references", queryTotal(t, rt, r), "6000")
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := rt.m.Execute(ctx, "p", "query", queryRequest(t)); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	})
}
func TestQueryDoesNotTruncateIntermediateGroups(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "items", "id", "id integer")
		for start := 0; start < 1201; start += 1000 {
			rows := []map[string]any{}
			for i := start; i < min(start+1000, 1201); i++ {
				rows = append(rows, Record{"id": fmt.Sprint(i)})
			}
			relInsert(rt, "items", rows...)
		}
		r := Request{Version: 1, Database: "d", Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "items"}, {Name: "groups", Op: "aggregate", From: "source", GroupBy: []string{"id"}, Metrics: []Metric{{Name: "n", Op: "count"}}}, {Name: "total", Op: "aggregate", From: "groups", Metrics: []Metric{{Name: "n", Op: "sum", Field: "n"}}}}, Result: &QueryResult{From: "total"}}
		row := relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "all groups contribute", row["n"], "1201")
		r.Result.From = "groups"
		requireQueryError(t, rt, r, "resource_limit")
	})
}

// A committed insert between the rate scan and the independent sales scan must
// affect neither one. Use backend transactions to permit an actual overlapping
// commit even where the manager currently serializes writes behind read locks.
type queryBarrierTx struct {
	transaction
	afterScan func()
	ctx       context.Context
}

func (t queryBarrierTx) scanRecords(c Collection, q Query, f []string, visit func(Record) bool) error {
	e := t.transaction.(recordScanner).scanRecords(c, q, f, visit)
	if t.afterScan != nil {
		t.afterScan()
	}
	return e
}
func TestQueryConcurrentSnapshot(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := queryRequest(t)
		r.Nodes[3] = QueryNode{Name: "sales", Op: "scan", Collection: "facts", Where: &Filter{And: []Filter{{Field: "centre", Op: "eq", Value: "a"}, {Field: "kind", Op: "eq", Value: "sale"}}}}
		d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ready, done := make(chan struct{}), make(chan error, 1)
		go func() {
			select {
			case <-ready:
			case <-ctx.Done():
				done <- ctx.Err()
				return
			}
			done <- d.backend.transaction(ctx, true, func(tx transaction) error {
				_, e := execute(tx, "insert", Request{Collection: "facts", Records: []Record{{"id": "s3", "centre": "a", "period": "oct", "kind": "sale", "contracts": "1"}}}, nil)
				return e
			})
		}()
		scans := 0
		var out any
		e = d.backend.transaction(ctx, false, func(tx transaction) error {
			wrapper := queryBarrierTx{transaction: d.cachedTransaction(tx, true), ctx: ctx, afterScan: func() {
				scans++
				if scans == 1 {
					close(ready)
					select {
					case e := <-done:
						if e != nil {
							t.Error(e)
						}
					case <-ctx.Done():
						t.Error(ctx.Err())
					}
				}
			}}
			var e error
			out, e = executeQuery(wrapper, r)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		var decoded map[string]any
		decode(marshal(out), &decoded)
		relEqual(t, "one snapshot", relList(decoded["rows"])[0]["revenue_cents"], "6000")
		relEqual(t, "independent collection scans", scans, 2)
		relEqual(t, "fresh snapshot sees insert and new rate", queryTotal(t, rt, r), "12000")
	})
}

func TestNumericExpressions(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "values", "id", "id text,n integer")
		relInsert(rt, "values", Record{"id": "one", "n": "9007199254740993"})
		cases := []struct {
			name, expr, typ string
			want            any
		}{
			{"exact add", `{"op":"add","args":[{"field":"n"},{"const":"1"}]}`, "integer", "9007199254740994"},
			{"subtract", `{"op":"subtract","args":[{"const":"5"},{"const":"8"}]}`, "integer", "-3"},
			{"min cap", `{"op":"min","args":[{"field":"n"},{"const":"100"}]}`, "integer", "100"},
			{"max floor", `{"op":"max","args":[{"const":"-5"},{"const":"0"}]}`, "integer", "0"},
			{"null divide", `{"op":"safe_divide","args":[{"const":"5"},{"const":"0"}]}`, "number", nil},
			{"null operand", `{"op":"add","args":[{}, {"const":"1"}]}`, "integer", nil},
			{"exact comparison", `{"op":"gt","args":[{"field":"n"},{"const":"9007199254740992"}]}`, "boolean", true},
			{"half away", `{"op":"round","args":[{"const":-2.5}],"round":0}`, "integer", "-3"},
			{"half even", `{"op":"round","args":[{"const":-2.5}],"round":0,"roundMode":"half_even"}`, "integer", "-2"},
			{"decimal round", `{"op":"round","args":[{"const":1.005}],"round":2}`, "number", json.Number("1.01")},
			{"floor", `{"op":"round","args":[{"const":-2.1}],"round":0,"roundMode":"floor"}`, "integer", "-3"},
			{"ceil", `{"op":"round","args":[{"const":-2.9}],"round":0,"roundMode":"ceil"}`, "integer", "-2"},
			{"conditional numeric constant", `{"op":"if","args":[{"const":true},{"const":"1"},{"const":"2"}]}`, "number", json.Number("1")},
			{"conditional text", `{"op":"if","args":[{"const":true},{"const":"yes"},{"const":"no"}]}`, "text", "yes"},
			{"short circuit", `{"op":"if","args":[{"const":true},{"const":"1"},{"op":"multiply","args":[{"field":"n"},{"field":"n"}]}]}`, "integer", "1"},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				var expr RelExpr
				decode([]byte(tc.expr), &expr)
				r := Request{Version: 1, Database: "d", Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "values"}, {Name: "computed", Op: "compute", From: "source", Computed: []RelComputed{{Name: "v", Type: tc.typ, Expr: expr}}, Select: []string{"v"}}}, Result: &QueryResult{From: "computed"}}
				row := relList(runQuery(t, rt, r)["rows"])[0]
				if !reflect.DeepEqual(row["v"], tc.want) {
					t.Fatalf("got %v want %v", row["v"], tc.want)
				}
			})
		}
		for name, expr := range map[string]string{
			"integer multiply overflow": `{"op":"multiply","args":[{"field":"n"},{"field":"n"}]}`,
			"integer add overflow":      `{"op":"add","args":[{"const":"9223372036854775807"},{"const":"1"}]}`,
			"fraction requires round":   `{"op":"safe_divide","args":[{"const":"1"},{"const":"2"}]}`,
		} {
			t.Run(name, func(t *testing.T) {
				var x RelExpr
				decode([]byte(expr), &x)
				r := Request{Version: 1, Database: "d", Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "values"}, {Name: "computed", Op: "compute", From: "source", Computed: []RelComputed{{Name: "v", Type: "integer", Expr: x}}}}, Result: &QueryResult{From: "computed"}}
				code := "resource_limit"
				if strings.Contains(name, "round") {
					code = "invalid_argument"
				}
				requireQueryError(t, rt, r, code)
			})
		}
	})
}

func TestQueryJoinSemantics(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "parents", "id", "id text,k text")
		relCollection(rt, "children", "id", "id text,k text,value integer")
		relInsert(rt, "parents", Record{"id": "a", "k": "one"}, Record{"id": "b", "k": "missing"}, Record{"id": "null"})
		relInsert(rt, "children", Record{"id": "c1", "k": "one", "value": "7"}, Record{"id": "c2", "k": "one", "value": "8"}, Record{"id": "null", "value": "9"})
		makePlan := func(kind string) Request {
			return Request{Database: "d", Version: 1, Nodes: []QueryNode{{Name: "parents", Op: "scan", Collection: "parents"}, {Name: "children", Op: "scan", Collection: "children"}, {Name: "joined", Op: "join", From: "parents", As: "p", Joins: []QueryJoin{{Relation: "children", As: "c", Kind: kind, On: []RelOn{{Local: "p.k", Foreign: "k"}}}}}}, Result: &QueryResult{From: "joined", OrderBy: []Order{{Field: "p.id"}}}}
		}
		for kind, want := range map[string]int{"inner": 2, "left": 4, "semi": 1, "anti": 2} {
			r := makePlan(kind)
			rows := relList(runQuery(t, rt, r)["rows"])
			relEqual(t, kind, len(rows), want)
		}
		r := makePlan("left")
		r.Nodes[2].Joins[0].Where = &Filter{Field: "value", Op: "eq", Value: "none"}
		requireQueryError(t, rt, r, "invalid_argument")
		r = makePlan("left")
		r.Nodes[2].Joins[0].Where = &Filter{Field: "value", Op: "eq", Value: "999"}
		rows := relList(runQuery(t, rt, r)["rows"])
		relEqual(t, "left join where preserves parents", len(rows), 3)
		for _, row := range rows {
			relEqual(t, "missing right fields", row["c.value"], nil)
		}
		r = makePlan("inner")
		r.Nodes[2].Where = &Filter{Field: "c.value", Op: "eq", Value: "7"}
		r.Nodes[2].Select = []string{"p.id"}
		rows = relList(runQuery(t, rt, r)["rows"])
		relEqual(t, "filter precedes projection", rows, []map[string]any{{"p.id": "a"}})
	})
}
func TestQueryRoundingOrderAndLegacyNumeric(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "items", "id", "id text,n integer")
		relInsert(rt, "items", Record{"id": "a", "n": "1"}, Record{"id": "b", "n": "1"})
		zero := 0
		divided := RelExpr{Op: "safe_divide", Args: []RelExpr{{Field: "n"}, {Const: "2"}}}
		r := Request{Database: "d", Version: 1, Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "items"}, {Name: "amounts", Op: "compute", From: "source", Computed: []RelComputed{{Name: "cents", Type: "integer", Expr: RelExpr{Op: "round", Args: []RelExpr{divided}, Round: &zero}}}}, {Name: "total", Op: "aggregate", From: "amounts", Metrics: []Metric{{Name: "cents", Op: "sum", Field: "cents"}}}}, Result: &QueryResult{From: "total"}}
		row := relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "round each half cent before sum", row["cents"], "2")
		r.Nodes[1] = QueryNode{Name: "amounts", Op: "aggregate", From: "source", Metrics: []Metric{{Name: "n", Op: "sum", Field: "n"}}}
		r.Nodes[2] = QueryNode{Name: "total", Op: "compute", From: "amounts", Computed: []RelComputed{{Name: "cents", Type: "integer", Expr: RelExpr{Op: "round", Args: []RelExpr{divided}, Round: &zero}}}}
		row = relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "sum then round differs", row["cents"], "1")
		out := relCall(rt, "joined_aggregate", `{"collection":"items","computed":[{"name":"cents","type":"integer","expr":{"op":"multiply","args":[{"field":"root.n"},{"const":"4000"}]}}],"metrics":[{"name":"cents","op":"sum","field":"cents"}]}`)
		relEqual(t, "shared numeric evaluator on existing API", relList(out["rows"])[0]["cents"], "8000")
	})
}
func TestQueryValidatesBeforeScanning(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := queryRequest(t)
		r.Nodes = append(r.Nodes, QueryNode{Name: "invalid", Op: "compute", From: "facts", Computed: []RelComputed{{Name: "x", Expr: RelExpr{Field: "missing"}}}})
		d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		scans := 0
		e = d.backend.transaction(context.Background(), false, func(tx transaction) error {
			_, e := executeQuery(queryBarrierTx{transaction: d.cachedTransaction(tx, true), afterScan: func() { scans++ }}, r)
			return e
		})
		if e == nil || scans != 0 {
			t.Fatalf("validation read data: scans=%d error=%v", scans, e)
		}
	})
}

func TestQueryNestedAggregatesShareActualMemory(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "items", "id", "id text")
		relInsert(rt, "items", Record{"id": "a"})
		r := Request{Database: "d", Version: 1, Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "items"}}, Limits: &QueryLimits{MemoryBytes: 64 << 10}}
		previous := "source"
		for i := range 20 {
			name := fmt.Sprintf("stage_%d", i)
			r.Nodes = append(r.Nodes, QueryNode{Name: name, Op: "aggregate", From: previous, Metrics: []Metric{{Name: "n", Op: "count"}}})
			previous = name
		}
		r.Result = &QueryResult{From: previous}
		row := relList(runQuery(t, rt, r)["rows"])[0]
		relEqual(t, "small nested aggregates fit", row["n"], "1")
		r.Limits.MemoryBytes = 256
		requireQueryError(t, rt, r, "resource_limit")
	})
}

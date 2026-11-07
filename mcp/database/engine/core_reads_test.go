package engine

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func coreExecute(t *testing.T, rt relT, op string, r Request) map[string]any {
	t.Helper()
	r.Database = "d"
	v, e := rt.m.Execute(context.Background(), "p", op, r)
	if e != nil {
		t.Fatal(op, e)
	}
	var out map[string]any
	if e := decode(marshal(v), &out); e != nil {
		t.Fatal(e)
	}
	return out
}
func coreAggregateRequest(t *testing.T) Request {
	t.Helper()
	old := queryRequest(t)
	last := old.Nodes[len(old.Nodes)-1]
	return Request{Database: "d", Pipeline: &ReadPipeline{Version: 1, Nodes: old.Nodes[:len(old.Nodes)-1], From: last.From}, Metrics: last.Metrics, GroupBy: last.GroupBy}
}
func coreFailure(t *testing.T, rt relT, op string, r Request, code string) {
	t.Helper()
	r.Database = "d"
	v, e := rt.m.Execute(context.Background(), "p", op, r)
	var known *Error
	if v != nil || !errors.As(e, &known) || known.Code != code {
		t.Fatalf("%s wanted %s without result, got %v / %v", op, code, v, e)
	}
}
func TestCoreAggregateDependentPipeline(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := coreAggregateRequest(t)
		before := string(marshal(r))
		out := coreExecute(t, rt, "aggregate", r)
		relEqual(t, "full calculation", relList(out["rows"]), []map[string]any{{"contracts": "2", "revenue_cents": "6000"}})
		relEqual(t, "legacy query parity", out, runQuery(t, rt, queryRequest(t)))
		relEqual(t, "immutable request", string(marshal(r)), before)
		for _, field := range []string{"sale.centre", "sale.campaign", "sale.adviser", "sale.display_date"} {
			r.GroupBy = []string{field}
			rows := relList(coreExecute(t, rt, "aggregate", r)["rows"])
			if field == "sale.centre" {
				relEqual(t, "centre sum", rows[0]["revenue_cents"], "6000")
			} else {
				if len(rows) != 2 {
					t.Fatal(field, rows)
				}
				values := map[any]bool{}
				for _, row := range rows {
					values[row["revenue_cents"]] = true
				}
				if !values["4000"] || !values["2000"] {
					t.Fatal(rows)
				}
			}
		}
		r = coreAggregateRequest(t)
		r.Where = &Filter{Field: "sale.display_date", Op: "eq", Value: "2026-09-30"}
		relEqual(t, "display filter after period rate", relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]["revenue_cents"], "4000")
		r.Where = &Filter{Field: "sale.centre", Op: "eq", Value: "none"}
		row := relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		relEqual(t, "empty sum", row["revenue_cents"], nil)
		r.Metrics = []Metric{{Name: "n", Op: "count"}}
		relEqual(t, "empty count", relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]["n"], "0")
		r = coreAggregateRequest(t)
		r.Limits = &QueryLimits{IntermediateRows: 2}
		coreFailure(t, rt, "aggregate", r, "resource_limit")
		r = coreAggregateRequest(t)
		r.Pipeline.Nodes[2].Computed[0].Expr = RelExpr{Const: "9223372036854775807"}
		coreFailure(t, rt, "aggregate", r, "resource_limit")
		r = coreAggregateRequest(t)
		r.Pipeline.Nodes[4].Joins[0].On = r.Pipeline.Nodes[4].Joins[0].On[:1]
		coreFailure(t, rt, "aggregate", r, "cardinality_violation")
	})
}
func coreParity(t *testing.T, rt relT, legacy, canonical, body string) {
	t.Helper()
	old := relCall(rt, legacy, body)
	next := relCall(rt, canonical, body)
	// Cursor expirations may straddle a second; compare authenticated payloads
	// separately, and exercise interchange below.
	a, ao := old["nextCursor"].(string)
	b, bo := next["nextCursor"].(string)
	if ao && bo && a != "" && b != "" {
		delete(old, "nextCursor")
		delete(next, "nextCursor")
	}
	if !reflect.DeepEqual(old, next) {
		t.Fatalf("%s -> %s mismatch:\n%s\n%s", legacy, canonical, marshal(old), marshal(next))
	}
	if a != "" {
		var r Request
		decode([]byte(body), &r)
		r.Cursor = a
		r.Offset = 0
		legacyNext := coreExecute(t, rt, legacy, r)
		canonicalNext := coreExecute(t, rt, canonical, r)
		delete(legacyNext, "nextCursor")
		delete(canonicalNext, "nextCursor")
		relEqual(t, "legacy cursor resumes canonically", canonicalNext, legacyNext)
	}
}
func TestCoreRelationalCompatibility(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		seedInteractions(t, rt)
		for _, extra := range []string{
			`"select":["root.id","sale.amount"],"limit":2`,
			`"distinct":["root","sale"],"group":{"by":["root.interaction"],"orderBy":[{"field":"root.started","agg":"min"}]},"orderBy":[{"field":"root.started"}],"select":["root.id","sale.id"],"limit":1`,
			`"pick":{"by":["root.interaction"],"orderBy":[{"field":"root.started","direction":"desc"}]},"select":["root.id"],"limit":2`,
			`"computed":[{"name":"money","type":"integer","expr":{"coalesce":[{"field":"sale.amount"},{"const":"0"}]}}],"filter":{"field":"money","op":"gte","value":"100"},"select":["root.id","money"],"offset":1,"limit":1`,
		} {
			coreParity(t, rt, "joined_page", "find", `{"collection":"legs",`+interactionJoins+`,`+extra+`}`)
		}
		for _, extra := range []string{
			`"metrics":[{"name":"n","op":"count"},{"name":"sales","op":"count_distinct","field":"sale.id"},{"name":"money","op":"sum","field":"sale.amount"}],"derived":[{"name":"per","op":"divide","numerator":"money","denominator":"sales","round":2}]`,
			`"distinct":["sale"],"metrics":[{"name":"money","op":"sum","field":"sale.amount"}]`,
			`"rollup":{"by":["root.interaction"],"metrics":[{"name":"money","op":"max","field":"sale.amount"}]},"metrics":[{"name":"money","op":"sum","field":"money"}]`,
			`"groupBy":["root.centre"],"metrics":[{"name":"n","op":"count","where":{"field":"sale.amount","op":"is_not_null"}}]`,
		} {
			coreParity(t, rt, "joined_aggregate", "aggregate", `{"collection":"legs",`+interactionJoins+`,`+extra+`}`)
		}
		for _, kind := range []string{"inner", "left", "semi", "anti", "aggregate"} {
			join := fmt.Sprintf(`{"as":"x","kind":%q,"collection":"aliases","on":[{"local":"root.id","foreign":"leg_id"}]`, kind)
			if kind == "aggregate" {
				join += `,"metrics":[{"name":"n","op":"count"}]`
			}
			join += "}"
			coreParity(t, rt, "joined_aggregate", "aggregate", `{"collection":"legs","joins":[`+join+`],"metrics":[{"name":"n","op":"count"}]}`)
		}
	})
	relEach(t, func(t *testing.T, rt relT) {
		seedBranches(t, rt)
		coreParity(t, rt, "joined_aggregate", "aggregate", `{`+branchPlan+`,"groupBy":["campaign"],"orderBy":[{"field":"campaign"}],"metrics":[{"name":"contracts","op":"count_distinct","field":"sale"}]}`)
	})
	relEach(t, func(t *testing.T, rt relT) {
		seedRelated(t, rt)
		for _, suffix := range []string{``, `,"match":{"field":"label","op":"contains_ascii","value":"100%"}`, `,"match":{"field":"phone","op":"eq","value":"330612345678"}`} {
			coreParity(t, rt, "related_read", "find", relatedArgs("", suffix))
		}
	})
}
func TestCorePlainFindCountOffset(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := Request{Collection: "facts", Count: true, Offset: 1, Query: Query{Where: &Filter{Field: "kind", Op: "eq", Value: "sale"}, OrderBy: []Order{{"id", "asc"}}, Select: []string{"id"}, Limit: 1}}
		out := coreExecute(t, rt, "find", r)
		relEqual(t, "total before offset", out["total"], "3")
		relEqual(t, "plain names", relList(out["records"]), []map[string]any{{"id": "s1"}})
		r.Offset = 0
		r.Cursor = out["nextCursor"].(string)
		out = coreExecute(t, rt, "find", r)
		relEqual(t, "cursor after offset", relList(out["records"]), []map[string]any{{"id": "s2"}})
		r = Request{Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}, {Name: "sum", Op: "sum", Field: "contracts"}}, Derived: []RelDerived{{Name: "per", Op: "divide", Numerator: "sum", Denominator: "n"}}}
		row := relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		if row["per"] == nil {
			t.Fatal("native aggregate derived missing", row)
		}
	})
}
func TestCorePipelineFindPages(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		old := queryRequest(t)
		r := Request{Pipeline: &ReadPipeline{Version: 1, Nodes: old.Nodes, From: "rates"}, Count: true, Query: Query{OrderBy: []Order{{"period", "desc"}}, Select: []string{"period", "rate_cents"}, Limit: 1}}
		first := coreExecute(t, rt, "find", r)
		relEqual(t, "count", first["total"], "2")
		relEqual(t, "first", relList(first["records"]), []map[string]any{{"period": "sep", "rate_cents": "4000"}})
		r.Cursor = first["nextCursor"].(string)
		second := coreExecute(t, rt, "find", r)
		relEqual(t, "second", relList(second["records"]), []map[string]any{{"period": "oct", "rate_cents": "2000"}})
		relEqual(t, "end", second["hasMore"], false)
		r.Cursor = ""
		r.Offset = 1
		byOffset := coreExecute(t, rt, "find", r)
		relEqual(t, "offset", byOffset["records"], second["records"])
		r.Offset = 0
		r.Cursor = first["nextCursor"].(string)
		r.Where = &Filter{Field: "period", Op: "eq", Value: "sep"}
		coreFailure(t, rt, "find", r, "invalid_argument")
		r.Where = nil
		r.Cursor = "forged.cursor"
		coreFailure(t, rt, "find", r, "invalid_argument")
		r.Cursor = first["nextCursor"].(string)
		relCall(rt, "index_create", `{"collection":"facts","index":{"name":"new_index","fields":[{"field":"period"}]}}`)
		coreFailure(t, rt, "find", r, "invalid_argument")
		// Identical projected rows and tied ordering values still traverse once each.
		r = Request{Pipeline: &ReadPipeline{Version: 1, From: "source", Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "facts", Where: &Filter{Field: "kind", Op: "eq", Value: "call"}, Select: []string{"kind"}}}}, Count: true, Query: Query{Limit: 2}}
		n := 0
		for page := 0; page < 10; page++ {
			out := coreExecute(t, rt, "find", r)
			n += len(relList(out["records"]))
			if out["hasMore"] != true {
				break
			}
			r.Cursor = out["nextCursor"].(string)
		}
		relEqual(t, "duplicates kept", n, 8)
	})
}
func TestCorePipelineIntermediateLimitAndValidation(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "items", "id", "id integer")
		for start := 0; start < 1201; start += 1000 {
			rows := []map[string]any{}
			for i := start; i < min(start+1000, 1201); i++ {
				rows = append(rows, map[string]any{"id": fmt.Sprint(i)})
			}
			relInsert(rt, "items", rows...)
		}
		pipeline := &ReadPipeline{Version: 1, From: "groups", Nodes: []QueryNode{{Name: "source", Op: "scan", Collection: "items"}, {Name: "groups", Op: "aggregate", From: "source", GroupBy: []string{"id"}, Metrics: []Metric{{Name: "n", Op: "count"}}}}}
		r := Request{Pipeline: pipeline, Metrics: []Metric{{Name: "n", Op: "sum", Field: "n"}}}
		relEqual(t, "all intermediate groups", relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]["n"], "1201")
		r.GroupBy = []string{"id"}
		r.Limit = 2
		out := coreExecute(t, rt, "aggregate", r)
		relEqual(t, "final limit", len(relList(out["rows"])), 2)
		relEqual(t, "explicit truncation", out["truncated"], true)
		find := Request{Pipeline: pipeline, Count: true, Query: Query{Limit: 1000}}
		out = coreExecute(t, rt, "find", find)
		relEqual(t, "page all count", out["total"], "1201")
		relEqual(t, "first page", len(relList(out["records"])), 1000)
		find.Cursor = out["nextCursor"].(string)
		relEqual(t, "remaining", len(relList(coreExecute(t, rt, "find", find)["records"])), 201)
		for _, op := range []string{"find", "aggregate"} {
			bad := Request{Pipeline: pipeline, Metrics: []Metric{{Name: "n", Op: "count"}}, Collection: "items"}
			if op == "find" {
				bad.Metrics = nil
			}
			coreFailure(t, rt, op, bad, "invalid_argument")
			bad.Collection = ""
			bad.Limits = &QueryLimits{Work: 2}
			coreFailure(t, rt, op, bad, "query_too_expensive")
			bad.Limits = &QueryLimits{MemoryBytes: 100}
			coreFailure(t, rt, op, bad, "resource_limit")
		}
	})
}
func TestCorePipelineSnapshot(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		for _, op := range []string{"aggregate", "find"} {
			t.Run(adapter+"/"+op, func(t *testing.T) {
				rt := relOpen(t, adapter)
				queryFixture(t, rt)
				r := coreAggregateRequest(t)
				r.Pipeline.Nodes[3] = QueryNode{Name: "sales", Op: "scan", Collection: "facts", Where: &Filter{And: []Filter{{Field: "centre", Op: "eq", Value: "a"}, {Field: "kind", Op: "eq", Value: "sale"}}}}
				if op == "find" {
					r.Pipeline.Nodes = append(r.Pipeline.Nodes, QueryNode{Name: "total", Op: "aggregate", From: r.Pipeline.From, Metrics: r.Metrics})
					r.Pipeline.From = "total"
					r.Metrics = nil
				}
				d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
				if e != nil {
					t.Fatal(e)
				}
				defer release()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				scans := 0
				var answer any
				e = d.backend.transaction(ctx, false, func(tx transaction) error {
					wrapper := queryBarrierTx{transaction: d.cachedTransaction(tx, true), afterScan: func() {
						scans++
						if scans == 1 {
							e := d.backend.transaction(ctx, true, func(write transaction) error {
								_, e := execute(write, "insert", Request{Collection: "facts", Records: []Record{{"id": "s3", "centre": "a", "period": "oct", "kind": "sale", "contracts": "1"}}}, nil)
								return e
							})
							if e != nil {
								t.Error(e)
							}
						}
					}}
					var e error
					answer, e = execute(wrapper, op, r, d.key)
					return e
				})
				if e != nil {
					t.Fatal(e)
				}
				var out map[string]any
				decode(marshal(answer), &out)
				field := "rows"
				if op == "find" {
					field = "records"
				}
				relEqual(t, "one snapshot", relList(out[field])[0]["revenue_cents"], "6000")
				relEqual(t, "independent scans", scans, 2)
				relEqual(t, "next snapshot", relList(coreExecute(t, rt, op, r)[field])[0]["revenue_cents"], "12000")
			})
		}
	}
}

func TestCoreRejectsAmbiguousForms(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		for name, r := range map[string]Request{
			"missing source":          {Metrics: []Metric{{Name: "n", Op: "count"}}},
			"pipeline and collection": {Pipeline: &ReadPipeline{Version: 1, From: "x"}, Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}}},
			"aggregate page options":  {Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}}, Count: true},
			"query envelope":          {Version: 1, Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}}},
		} {
			t.Run(name, func(t *testing.T) { coreFailure(t, rt, "aggregate", r, "invalid_argument") })
		}
		for _, r := range []Request{{Collection: "facts", Match: &Filter{Field: "id", Op: "eq", Value: "x"}}, {Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}}}, {Collection: "facts", Offset: 1, Query: Query{Cursor: "bad"}}, {Collection: "facts", Limits: &QueryLimits{Work: 1}}, {Collection: "facts", Relation: &RelRelation{As: "items"}, Query: Query{RequireIndex: true}}} {
			coreFailure(t, rt, "find", r, "invalid_argument")
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if _, e := rt.m.Execute(ctx, "p", "aggregate", coreAggregateRequest(t)); !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	})
}

// Assert that ordinary aggregate calls still select the adapter's native method.
type coreNativeTx struct {
	transaction
	aggregates *int
}

func (t coreNativeTx) aggregate(c Collection, r Request, out Collection) ([]Record, error) {
	*t.aggregates++
	return t.transaction.aggregate(c, r, out)
}
func TestCoreNativeAggregatePath(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		calls := 0
		e = d.backend.transaction(context.Background(), false, func(tx transaction) error {
			_, e := execute(coreNativeTx{transaction: d.cachedTransaction(tx, true), aggregates: &calls}, "aggregate", Request{Collection: "facts", Metrics: []Metric{{Name: "n", Op: "count"}}}, nil)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		relEqual(t, "native aggregate invoked", calls, 1)
	})
}

func TestCoreAggregateLookupsAndBuckets(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "events", "id", "id text,owner text,amount integer,at datetime")
		relCollection(rt, "owners", "id", "id text,label text")
		relInsert(rt, "owners", Record{"id": "a", "label": "allowed"}, Record{"id": "b", "label": "other"})
		relInsert(rt, "events", Record{"id": "one", "owner": "a", "amount": "40", "at": "2026-10-01T23:30:00Z"}, Record{"id": "two", "owner": "a", "amount": "20", "at": "2026-10-02T00:30:00Z"}, Record{"id": "foreign", "owner": "b", "amount": "900", "at": "2026-10-02T00:30:00Z"})
		// Ordinary native analytics, relational analytics and pipeline analytics
		// calculate the same scoped local-calendar bucket.
		base := Request{Collection: "events", Lookups: []Lookup{{As: "owner", Collection: "owners", LocalField: "owner"}}, Buckets: []Bucket{{Name: "day", Field: "at", Unit: "day", Timezone: "Europe/Paris"}}, GroupBy: []string{"day"}, Metrics: []Metric{{Name: "money", Op: "sum", Field: "amount", Where: &Filter{Field: "owner.label", Op: "eq", Value: "allowed"}}}}
		// The lookup alias cannot shadow the stored owner field.
		base.Lookups[0].As = "person"
		base.Metrics[0].Where.Field = "person.label"
		want := coreExecute(t, rt, "aggregate", base)
		relEqual(t, "local day", relList(want["rows"]), []map[string]any{{"day": "2026-10-02", "money": "60"}})
		pipeline := base
		pipeline.Collection = ""
		pipeline.Pipeline = &ReadPipeline{Version: 1, From: "events", Nodes: []QueryNode{{Name: "events", Op: "scan", Collection: "events"}}}
		relEqual(t, "pipeline enrichment", coreExecute(t, rt, "aggregate", pipeline), want)
		joined := base
		joined.Buckets = append([]Bucket(nil), base.Buckets...)
		joined.As = "event"
		joined.Lookups = []Lookup{{As: "person", Collection: "owners", LocalField: "event.owner"}}
		joined.Buckets[0].Field = "event.at"
		joined.Metrics = []Metric{{Name: "money", Op: "sum", Field: "event.amount", Where: &Filter{Field: "person.label", Op: "eq", Value: "allowed"}}}
		relEqual(t, "relational enrichment", coreExecute(t, rt, "aggregate", joined), want)
		pipeline.Limits = &QueryLimits{MemoryBytes: 100}
		coreFailure(t, rt, "aggregate", pipeline, "resource_limit")
	})
}

func TestCoreDerivedOverflowAndRounding(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		relCollection(rt, "numbers", "id", "id text,a number,b number,n integer")
		relInsert(rt, "numbers", Record{"id": "one", "a": 1e308, "b": 5e-324, "n": "1"})
		bad := Request{
			Collection: "numbers",
			Metrics:    []Metric{{Name: "a", Op: "max", Field: "a"}, {Name: "b", Op: "min", Field: "b"}},
			Derived:    []RelDerived{{Name: "ratio", Op: "divide", Numerator: "a", Denominator: "b"}},
		}
		coreFailure(t, rt, "aggregate", bad, "resource_limit")
		relInsert(rt, "numbers", Record{"id": "two", "n": "1"})
		zero := 0
		division := RelExpr{Op: "safe_divide", Args: []RelExpr{{Field: "n"}, {Const: "2"}}}
		r := Request{
			Pipeline: &ReadPipeline{
				Version: 1, From: "rounded",
				Nodes: []QueryNode{
					{Name: "source", Op: "scan", Collection: "numbers"},
					{Name: "rounded", Op: "compute", From: "source", Computed: []RelComputed{
						{Name: "cents", Type: "integer", Expr: RelExpr{Op: "round", Args: []RelExpr{division}, Round: &zero}},
					}},
				},
			},
			Metrics: []Metric{{Name: "cents", Op: "sum", Field: "cents"}},
		}
		relEqual(t, "rounding precedes terminal aggregation", relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]["cents"], "2")
	})
}

func TestCoreFinalDerivedSharesPipelineMemory(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		queryFixture(t, rt)
		r := Request{Pipeline: &ReadPipeline{Version: 1, From: "facts", Nodes: []QueryNode{{Name: "facts", Op: "scan", Collection: "facts"}}}, Metrics: []Metric{{Name: "n", Op: "count"}}, Limits: &QueryLimits{MemoryBytes: 8192}}
		for i := 0; i < 100; i++ {
			r.Derived = append(r.Derived, RelDerived{Name: fmt.Sprintf("ratio_%d", i), Op: "divide", Numerator: "n", Denominator: "n"})
		}
		coreFailure(t, rt, "aggregate", r, "resource_limit")
	})
}

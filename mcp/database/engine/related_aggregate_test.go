package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func relatedAggregateFixture(t *testing.T, rt relT) Request {
	relCollection(rt, "parents", "id", "id integer, centre text, points number, maximum number", "centre")
	relCollection(rt, "contexts", "id", "id integer, parent integer, sequence integer, allowed boolean, text text", "parent")
	relCall(rt, "insert", `{"collection":"parents","records":[{"id":"1","centre":"allowed","points":50,"maximum":100},{"id":"2","centre":"allowed","points":10,"maximum":20},{"id":"3","centre":"foreign","points":90,"maximum":100},{"id":"4","centre":"allowed","points":null,"maximum":100}]}`)
	relCall(rt, "insert", `{"collection":"contexts","records":[{"id":"1","parent":"1","sequence":"0","allowed":false,"text":"blocked"},{"id":"2","parent":"1","sequence":"1","allowed":true,"text":"match"},{"id":"3","parent":"1","sequence":"2","allowed":true,"text":"match"},{"id":"4","parent":"1","sequence":"3","allowed":true,"text":"outside cap"},{"id":"5","parent":"2","sequence":"1","allowed":true,"text":"match"},{"id":"6","parent":"3","sequence":"1","allowed":true,"text":"match"},{"id":"7","parent":"4","sequence":"1","allowed":true,"text":"unscored"}]}`)
	var r Request
	body := `{"collection":"parents","as":"p","where":{"field":"centre","op":"eq","value":"allowed"},"relation":{"as":"context","limit":2,"sources":[{"name":"calls","joins":[{"collection":"contexts","as":"c","kind":"inner","on":[{"local":"p.id","foreign":"parent"}]}],"filter":{"field":"c.allowed","op":"eq","value":true},"distinct":["c.id"],"orderBy":[{"field":"c.sequence","direction":"asc"}],"select":["c.text"]}]},"match":{"field":"c.text","op":"contains_ascii","value":"match"},"computed":[{"name":"percentage","type":"number","expr":{"if":{"and":[{"field":"p.maximum","op":"gt","value":0},{"field":"p.points","op":"is_not_null"}]},"then":{"op":"safe_divide","args":[{"op":"multiply","args":[{"field":"p.points"},{"const":100}]},{"field":"p.maximum"}]},"else":{"const":null}}}],"metrics":[{"name":"parents","op":"count"},{"name":"points","op":"sum","field":"points"},{"name":"average","op":"avg","field":"percentage"} ]}`
	if e := json.Unmarshal([]byte(body), &r); e != nil {
		t.Fatal(e)
	}
	return r
}
func TestCoreAggregateRelatedParents(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		before := string(marshal(r))
		out := coreExecute(t, rt, "aggregate", r)

		relEqual(t, "once per parent", relList(out["rows"]), []map[string]any{{"parents": "2", "points": json.Number("60"), "average": json.Number("50")}})
		relEqual(t, "immutable request", string(marshal(r)), before)
		r.GroupBy = []string{"centre"}
		out = coreExecute(t, rt, "aggregate", r)
		relEqual(t, "grouped", relList(out["rows"]), []map[string]any{{"centre": "allowed", "parents": "2", "points": json.Number("60"), "average": json.Number("50")}})
		r.GroupBy = nil
		r.Match = &Filter{Field: "c.text", Op: "contains_ascii", Value: "outside cap"}
		out = coreExecute(t, rt, "aggregate", r)
		rows := relList(out["rows"])
		relEqual(t, "first slots before match", rows[0]["parents"], "0")
		relEqual(t, "empty average", rows[0]["average"], nil)
		r.Match = &Filter{Field: "c.text", Op: "contains_ascii", Value: "blocked"}
		out = coreExecute(t, rt, "aggregate", r)
		relEqual(t, "ineligible entries cannot match", relList(out["rows"])[0]["parents"], "0")
		r.Match = &Filter{Field: "c.text", Op: "contains_ascii", Value: "unscored"}
		out = coreExecute(t, rt, "aggregate", r)
		relEqual(t, "null average", relList(out["rows"])[0]["average"], nil)
		relEqual(t, "unscored parent", relList(out["rows"])[0]["parents"], "1")
		r.Match = nil
		out = coreExecute(t, rt, "aggregate", r)
		relEqual(t, "no match counts all authorised parents", relList(out["rows"])[0]["parents"], "3")
	})
}
func TestCoreAggregateRelatedRejectsPagingAndRootJoins(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		r.Offset = 1
		coreFailure(t, rt, "aggregate", r, "invalid_argument")
		r.Offset = 0
		r.Count = true
		coreFailure(t, rt, "aggregate", r, "invalid_argument")
		r.Count = false
		r.Relation.Limit = 0
		coreFailure(t, rt, "aggregate", r, "invalid_argument")
	})
}

func TestCoreAggregateRelatedFindParityAndLiteralSearch(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		relCall(rt, "update", `{"collection":"contexts","where":{"field":"id","op":"eq","value":"2"},"set":{"text":"100% A_B"}}`)
		for _, needle := range []string{"%", "_", "A_B", "A1B", "1000", "outside cap", "blocked", "unscored", ""} {
			r.Match = &Filter{Field: "c.text", Op: "contains_ascii", Value: needle}
			summary := coreExecute(t, rt, "aggregate", r)
			page := r
			page.Computed = nil
			page.Metrics = nil
			page.Count = true
			page.Select = []string{"id"}
			listed := coreExecute(t, rt, "find", page)
			relEqual(t, "find total for "+needle, relList(summary["rows"])[0]["parents"], listed["total"])
		}
		r.Match = nil
		r.Metrics = append(r.Metrics, Metric{Name: "centres", Op: "count_distinct", Field: "p.centre"}, Metric{Name: "scored", Op: "count", Field: "percentage"})
		row := relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		relEqual(t, "distinct centres", row["centres"], "1")
		relEqual(t, "non-null computed count", row["scored"], "2")
		relInsert(rt, "parents", map[string]any{"id": "5", "centre": "allowed", "points": 20, "maximum": 0})
		row = relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		relEqual(t, "parent without context", row["parents"], "4")
		relEqual(t, "invalid denominator is null", row["average"], json.Number("50"))
		// A later source cannot regain slots consumed by earlier sources.
		r.Relation.Sources = append(r.Relation.Sources, r.Relation.Sources[0])
		r.Relation.Sources[1].Name = "later"
		r.Match = &Filter{Field: "source", Op: "eq", Value: "later"}
		row = relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		relEqual(t, "shared cap across sources", row["parents"], "2")
	})
}

func TestCoreAggregateRelatedOrderingAndValidation(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		r.Match = nil
		r.GroupBy = []string{"id"}
		r.OrderBy = []Order{{Field: "points", Direction: "desc"}}
		r.Limit = 1
		out := coreExecute(t, rt, "aggregate", r)
		relEqual(t, "final limit", len(relList(out["rows"])), 1)
		relEqual(t, "final output order", relList(out["rows"])[0]["id"], "1")
		relEqual(t, "truncated groups", out["truncated"], true)
		for name, change := range map[string]func(*Request){
			"cursor": func(r *Request) { r.Cursor = "page" }, "select": func(r *Request) { r.Select = []string{"id"} }, "join": func(r *Request) { r.Joins = []RelJoin{{As: "x", Collection: "contexts"}} }, "computed shadow": func(r *Request) {
				r.Computed = []RelComputed{{Name: "points", Type: "number", Expr: RelExpr{Const: 1}}}
			},
		} {
			t.Run(name, func(t *testing.T) {
				r := relatedAggregateFixtureRequest(t)
				change(&r)
				coreFailure(t, rt, "aggregate", r, "invalid_argument")
			})
		}
	})
}
func relatedAggregateFixtureRequest(t *testing.T) Request {
	t.Helper()
	raw := `{"collection":"parents","as":"p","where":{"field":"centre","op":"eq","value":"allowed"},"relation":{"as":"context","limit":2,"sources":[{"name":"entries","joins":[{"collection":"contexts","as":"c","on":[{"local":"p.id","foreign":"parent"}]}],"select":["c.text"]}]},"metrics":[{"name":"n","op":"count"}]}`
	var r Request
	if e := decode([]byte(raw), &r); e != nil {
		t.Fatal(e)
	}
	return r
}

type relatedAuditTx struct {
	transaction
	ctx       context.Context
	onRead    func(string)
	projected *testing.T
}

func (t relatedAuditTx) find(c Collection, q Query, n int) ([]Record, error) {
	if t.projected != nil {
		for _, name := range q.Select {
			if name == "body" {
				t.projected.Fatal("selected unused body")
			}
		}
		if len(q.Select) == 0 {
			t.projected.Fatal("unprojected read", c.Name)
		}
	}
	rows, e := t.transaction.find(c, q, n)
	if t.onRead != nil {
		t.onRead(c.Name)
	}
	return rows, e
}
func (t relatedAuditTx) scanRecords(c Collection, q Query, fields []string, visit func(Record) bool) error {
	if t.projected != nil {
		for _, name := range fields {
			if name == "body" {
				t.projected.Fatal("scanned unused body")
			}
		}
		if len(fields) == 0 {
			t.projected.Fatal("unprojected scan", c.Name)
		}
	}
	return t.transaction.(recordScanner).scanRecords(c, q, fields, func(r Record) bool {
		if t.onRead != nil {
			t.onRead(c.Name)
		}
		return visit(r)
	})
}
func TestCoreAggregateRelatedProjectionAndBounds(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		// Add wide bodies to both collections, keeping them irrelevant to the plan.
		relCollection(rt, "wide_parents", "id", "id integer, centre text, points number, maximum number, body text", "centre")
		relCollection(rt, "wide_contexts", "id", "id integer,parent integer,sequence integer,allowed boolean,text text,body text", "parent")
		body := strings.Repeat("x", 512<<10)
		for i := 1; i <= 24; i++ {
			id := fmt.Sprint(i)
			relInsert(rt, "wide_parents", map[string]any{"id": id, "centre": "allowed", "points": 1, "maximum": 2, "body": body})
			relInsert(rt, "wide_contexts", map[string]any{"id": id, "parent": id, "sequence": "1", "allowed": true, "text": "match", "body": body})
		}
		r.Collection = "wide_parents"
		r.Relation.Sources[0].Joins[0].Collection = "wide_contexts"
		withRetained(t, 128<<10)
		d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		var answer any
		e = d.backend.transaction(context.Background(), false, func(tx transaction) error {
			var e error
			answer, e = execute(relatedAuditTx{transaction: d.cachedTransaction(tx, true), projected: t}, "aggregate", r, d.key)
			return e
		})
		if e != nil {
			t.Fatal(e)
		}
		var out map[string]any
		decode(marshal(answer), &out)
		relEqual(t, "wide data projected", relList(out["rows"])[0]["parents"], "24")
		// Needed large values fail the same retained-state budget, with no result.
		r.Relation.Sources[0].Select = append(r.Relation.Sources[0].Select, "c.body")
		coreFailure(t, rt, "aggregate", r, "resource_limit")
		r.Relation.Sources[0].Select = []string{"c.text"}
		r.Computed = append(r.Computed, RelComputed{Name: "large", Expr: RelExpr{Field: "p.body"}})
		coreFailure(t, rt, "aggregate", r, "resource_limit")
	})
}
func TestCoreAggregateRelatedBeyondOneRootBatch(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		r.Match = nil
		rows := []map[string]any{}
		for i := 5; i <= relBatch+17; i++ {
			rows = append(rows, map[string]any{"id": fmt.Sprint(i), "centre": "allowed", "points": 1, "maximum": 2})
			if len(rows) == MaxRecords {
				relInsert(rt, "parents", rows...)
				rows = nil
			}
		}
		if len(rows) > 0 {
			relInsert(rt, "parents", rows...)
		}
		out := coreExecute(t, rt, "aggregate", r)
		relEqual(t, "all scoped roots once", relList(out["rows"])[0]["parents"], fmt.Sprint(relBatch+16))
	})
}
func TestCoreAggregateRelatedCancellationAndSnapshot(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		r.Database = "d"
		d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
		if e != nil {
			t.Fatal(e)
		}
		defer release()
		ctx, cancel := context.WithCancel(context.Background())
		var answer any
		reads := 0
		e = d.backend.transaction(ctx, false, func(tx transaction) error {
			var e error
			answer, e = execute(relatedAuditTx{transaction: d.cachedTransaction(tx, true), ctx: ctx, onRead: func(string) { reads++; cancel() }}, "aggregate", r, d.key)
			return e
		})
		if reads == 0 || !errors.Is(e, context.Canceled) || answer != nil {
			t.Fatalf("cancellation: reads=%d result=%v error=%v", reads, answer, e)
		}
		ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		changed := false
		batch := Request{Database: "d", Operations: []Operation{{Op: "find", Request: Request{Collection: r.Collection, As: r.As, Query: Query{Where: r.Where, Select: []string{"id"}}, Relation: r.Relation, Match: r.Match, Count: true}}, {Op: "aggregate", Request: r}}}
		e = d.backend.transaction(ctx, false, func(tx transaction) error {
			wrapped := relatedAuditTx{transaction: d.cachedTransaction(tx, true), ctx: ctx, onRead: func(name string) {
				if name != "parents" || changed {
					return
				}
				changed = true
				err := d.backend.transaction(ctx, true, func(write transaction) error {
					_, err := execute(write, "insert", Request{Collection: "parents", Records: []Record{{"id": "5", "centre": "allowed", "points": 20, "maximum": 100}}}, nil)
					if err != nil {
						return err
					}
					_, err = execute(write, "insert", Request{Collection: "contexts", Records: []Record{{"id": "8", "parent": "5", "sequence": "1", "allowed": true, "text": "match"}}}, nil)
					return err
				})
				if err != nil {
					t.Error(err)
				}
			}}
			var err error
			answer, err = execute(wrapped, "batch", batch, d.key)
			return err
		})
		if e != nil {
			t.Fatal(e)
		}
		if !changed {
			t.Fatal("writer never committed")
		}
		var out map[string]any
		decode(marshal(answer), &out)
		results := relList(out["results"])
		relEqual(t, "page snapshot", results[0]["total"], "2")
		relEqual(t, "summary snapshot", relList(results[1]["rows"])[0]["parents"], "2")
		relEqual(t, "next snapshot sees writer", relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]["parents"], "3")
	})
}

func TestCoreAggregateRelatedEligibilityAndDistinctPrecedeCap(t *testing.T) {
	relEach(t, func(t *testing.T, rt relT) {
		r := relatedAggregateFixture(t, rt)
		relCall(rt, "update", `{"collection":"contexts","where":{"field":"id","op":"eq","value":"3"},"set":{"text":"second retained"}}`)
		relCollection(rt, "duplicates", "id", "id integer, context_id integer", "context_id")
		relInsert(rt, "duplicates", map[string]any{"id": "1", "context_id": "2"}, map[string]any{"id": "2", "context_id": "2"})
		r.Relation.Sources[0].Joins = append(r.Relation.Sources[0].Joins, RelJoin{As: "d", Collection: "duplicates", Kind: "left", On: []RelOn{{Local: "c.id", Foreign: "context_id"}}})
		r.Match = &Filter{Field: "c.text", Op: "contains_ascii", Value: "second retained"}
		row := relList(coreExecute(t, rt, "aggregate", r)["rows"])[0]
		relEqual(t, "filter and dedup before cap", row["parents"], "1")
		relEqual(t, "parent score once", row["points"], json.Number("50"))
		old := relationalWork
		relationalWork = 2
		defer func() { relationalWork = old }()
		coreFailure(t, rt, "aggregate", r, "query_too_expensive")
	})
}

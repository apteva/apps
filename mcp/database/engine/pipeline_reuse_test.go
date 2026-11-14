package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func pipelineReuseQuery(t *testing.T, centre string) Request {
	t.Helper()
	var r Request
	checkErr(t, decode([]byte(`{"database":"d","diagnostics":true,"operations":[{"op":"find","args":{"pipeline":{"version":1,"nodes":[{"name":"source","op":"scan","collection":"facts","where":{"field":"centre","op":"eq","value":"`+centre+`"}},{"name":"computed","op":"compute","from":"source","computed":[{"name":"label","type":"text","expr":{"concat":[{"field":"caller"},{"const":"!"}],"separator":""}}]}],"from":"computed"},"select":["id","label"],"orderBy":[{"field":"id"}],"limit":2,"count":true}},{"op":"aggregate","args":{"pipeline":{"version":1,"nodes":[{"name":"source","op":"scan","collection":"facts","where":{"field":"centre","op":"eq","value":"`+centre+`"}}],"from":"source"},"metrics":[{"name":"calls","op":"count"},{"name":"callers","op":"count_distinct","field":"caller"}]}}]}`), &r))
	r.Operations = append(r.Operations,
		Operation{Op: "get", Request: Request{Collection: "facts", Key: Record{"id": "a"}}},
		Operation{Op: "find", Request: Request{Collection: "facts", Query: Query{
			Select: []string{"id"}, Where: &Filter{Field: "centre", Op: "eq", Value: centre}, Limit: 10,
		}}},
	)
	return r
}
func TestPipelineReadBatchReuse(t *testing.T) {
	for _, mode := range []string{"off", "coalesce", "cache"} {
		t.Run(mode, func(t *testing.T) {
			m := reuseFixture(t, mode)
			q := pipelineReuseQuery(t, "A")
			var raw []any
			for _, operation := range q.Operations {
				raw = append(raw, reuseCall(t, m, "p", operation.Op, withPipelineDatabase(operation.Request)))
			}
			// Hold native admission so all eight callers join the same cold flight.
			unblock, err := m.acquireNativeQuery(context.Background())
			checkErr(t, err)
			var once sync.Once
			defer once.Do(unblock)
			var channels []<-chan reuseReply
			for i := 0; i < 8; i++ {
				channels = append(channels, asyncReuse(m, context.Background(), q))
			}
			if mode != "off" {
				waitForReuse(t, func() bool { _, n := reuseCounts(m); return n == 8 && len(m.nativeQueryAdmissions) == 2 })
			} else {
				waitForReuse(t, func() bool { return len(m.nativeQueryAdmissions) == 9 })
			}
			once.Do(unblock)
			executed, joined := 0, 0
			var expected []any
			for _, ch := range channels {
				r := <-ch
				checkErr(t, r.err)
				rows := cloneAggregateValue(reuseRows(r.value), false).([]any)
				// Independent executions sign cursors with their own expiry time.
				// Page membership and cursor reuse are checked separately below.
				for _, row := range rows {
					delete(row.(map[string]any), "nextCursor")
				}
				if expected == nil {
					expected = rows
				} else if !reflect.DeepEqual(expected, rows) {
					t.Fatal("different pipeline result", rows, expected)
				}
				switch reusePath(r.value) {
				case "executed", "skipped":
					executed++
				case "inflight":
					joined++
				}
			}
			if mode == "off" && executed != 8 || mode != "off" && (executed != 1 || joined != 7) {
				t.Fatalf("executed=%d joined=%d", executed, joined)
			}
			warm := reuseCall(t, m, "p", "batch", q)
			for i, row := range reuseRows(warm) {
				got, want := cloneAggregateValue(row, false).(map[string]any), cloneAggregateValue(raw[i], false).(map[string]any)
				delete(got, "nextCursor")
				delete(want, "nextCursor")
				if !reflect.DeepEqual(got, want) {
					t.Fatalf("reuse differs from raw operation %d: %v != %v", i, got, want)
				}
			}
			if mode == "cache" && reusePath(warm) != "cache" {
				t.Fatal(warm)
			}
			first := reuseRows(warm)[0].(map[string]any)["records"].([]Record)
			first[0]["label"] = "mutated response"
			if next := reuseCall(t, m, "p", "batch", q); reuseRows(next)[0].(map[string]any)["records"].([]Record)[0]["label"] == "mutated response" {
				t.Fatal("one response modified a retained result")
			}
			// A commit in a scanned dependency invalidates both computed finds and totals.
			reuseCall(t, m, "p", "insert", Request{Database: "d", Collection: "facts", Records: []Record{{"id": "e", "centre": "A", "day": "2026-10-02", "caller": "new"}}})
			after := reuseCall(t, m, "p", "batch", q)
			if reuseRow(after, 1)["calls"] != "4" || (mode != "off" && reusePath(after) != "executed") {
				t.Fatal(after)
			}
			scoped := reuseCall(t, m, "p", "batch", pipelineReuseQuery(t, "B"))
			if reuseRow(scoped, 1)["calls"] != "1" {
				t.Fatal(scoped)
			}
			project := reuseCall(t, m, "q", "batch", q)
			if reuseRow(project, 1)["calls"] != "1" {
				t.Fatal(project)
			}
		})
	}
}
func TestPipelineReuseRejectsErasedInvalidShapes(t *testing.T) {
	q := pipelineReuseQuery(t, "A")
	q.Operations[0].Request.Pipeline.Nodes[0].Where = &Filter{And: []Filter{}}
	if b, reason := reusableAggregateBatch(q, nil); b != nil || reason != "unsupported_query" {
		t.Fatal(reason)
	}
	q = pipelineReuseQuery(t, "A")
	q.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr.Coalesce = []RelExpr{}
	if b, reason := reusableAggregateBatch(q, nil); b != nil || reason != "unsupported_query" {
		t.Fatal(reason)
	}
}

func TestPipelineReuseJoinDependencyAndValidation(t *testing.T) {
	m := reuseFixture(t, "cache")
	reuseCall(t, m, "p", "collection_create", Request{Database: "d", Collection: "weights", Fields: []Field{{Name: "id", Type: "text"}, {Name: "caller", Type: "text"}, {Name: "cents", Type: "integer"}}})
	reuseCall(t, m, "p", "insert", Request{Database: "d", Collection: "weights", Records: []Record{{"id": "w", "caller": "x", "cents": "100"}}})
	var q Request
	checkErr(t, decode([]byte(`{"database":"d","diagnostics":true,"operations":[{"op":"aggregate","args":{"pipeline":{"version":1,"nodes":[{"name":"facts","op":"scan","collection":"facts","where":{"field":"centre","op":"eq","value":"A"}},{"name":"weights","op":"scan","collection":"weights"},{"name":"joined","op":"join","from":"facts","joins":[{"relation":"weights","as":"w","kind":"inner","cardinality":"many_to_one","on":[{"local":"root.caller","foreign":"caller"}]}]}],"from":"joined"},"metrics":[{"name":"cents","op":"sum","field":"w.cents"}]}}]}`), &q))
	for i := 0; i < 2; i++ {
		out := reuseCall(t, m, "p", "batch", q)
		if reuseRow(out, 0)["cents"] != "200" {
			t.Fatal(out)
		}
		if i == 1 && reusePath(out) != "cache" {
			t.Fatal(out)
		}
	}
	reuseCall(t, m, "p", "update", Request{Database: "d", Collection: "weights", Key: Record{"id": "w"}, Set: Record{"cents": "150"}})
	fresh := reuseCall(t, m, "p", "batch", q)
	if reusePath(fresh) != "executed" || reuseRow(fresh, 0)["cents"] != "300" {
		t.Fatal(fresh)
	}
	reuseCall(t, m, "p", "insert", Request{Database: "d", Collection: "weights", Records: []Record{{"id": "duplicate", "caller": "x", "cents": "1"}}})
	for i := 0; i < 2; i++ {
		if _, err := m.Execute(context.Background(), "p", "batch", q); err == nil {
			t.Fatal("duplicate join cardinality was cached or not validated")
		}
	}
}

// Direct calls provide the uncached oracle for the same pipeline specification.
func withPipelineDatabase(r Request) Request {
	r.Database = "d"
	return r
}

func pipelineDiagnostic(t *testing.T, result any) map[string]any {
	t.Helper()
	return result.(map[string]any)["diagnostics"].(map[string]any)
}

func TestPipelineReuseSnapshotAndCancellation(t *testing.T) {
	m := reuseFixture(t, "cache")
	q := pipelineReuseQuery(t, "A")
	unblock, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	var once sync.Once
	defer once.Do(unblock)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	leader := asyncReuse(m, ctx, q)
	waitForReuse(t, func() bool { return len(m.nativeQueryAdmissions) == 2 })
	follower := asyncReuse(m, context.Background(), q)
	waitForReuse(t, func() bool { _, waiters := reuseCounts(m); return waiters == 2 })
	cancel()
	if reply := <-leader; !errors.Is(reply.err, context.Canceled) {
		t.Fatal(reply)
	}
	reuseCall(t, m, "p", "insert", Request{Database: "d", Collection: "facts", Records: []Record{
		{"id": "new", "centre": "A", "day": "2026-10-02", "caller": "new"},
	}})
	postCommit := asyncReuse(m, context.Background(), q)
	waitForReuse(t, func() bool { flights, waiters := reuseCounts(m); return flights == 2 && waiters == 2 })
	once.Do(unblock)
	for _, channel := range []<-chan reuseReply{follower, postCommit} {
		reply := <-channel
		checkErr(t, reply.err)
		if row := reuseRow(reply.value, 1); row["calls"] != "4" || row["callers"] != "3" {
			t.Fatal(row)
		}
	}
	warm := reuseCall(t, m, "p", "batch", q)
	if reusePath(warm) != "cache" {
		t.Fatal(warm)
	}
	m.aggregateReads.mu.Lock()
	entries := len(m.aggregateReads.entries)
	m.aggregateReads.mu.Unlock()
	if entries != 1 {
		t.Fatalf("retained %d entries instead of the actual execution revision", entries)
	}
	ctx, cancel = context.WithCancel(context.Background())
	block, err := m.acquireNativeQuery(context.Background())
	checkErr(t, err)
	defer block()
	abandoned := asyncReuse(m, ctx, pipelineReuseQuery(t, "B"))
	waitForReuse(t, func() bool { return len(m.nativeQueryAdmissions) == 2 })
	cancel()
	if reply := <-abandoned; !errors.Is(reply.err, context.Canceled) {
		t.Fatal(reply)
	}
	waitForReuse(t, func() bool {
		m.aggregateReads.mu.Lock()
		defer m.aggregateReads.mu.Unlock()
		return m.aggregateReads.active == 0 && len(m.aggregateReads.flights) == 0 && len(m.aggregateReads.entries) == 1
	})
}

func TestPipelineReuseKeyAndCursorIsolation(t *testing.T) {
	m := reuseFixture(t, "cache")
	q := pipelineReuseQuery(t, "A")
	first := reuseCall(t, m, "p", "batch", q)
	page := func(result any) map[string]any { return reuseRows(result)[0].(map[string]any) }
	if page(first)["total"] != "3" || page(first)["hasMore"] != true {
		t.Fatal(first)
	}
	cursor := page(first)["nextCursor"].(string)
	q.Operations[0].Request.Cursor = cursor
	next := reuseCall(t, m, "p", "batch", q)
	if reusePath(next) != "executed" || len(page(next)["records"].([]Record)) != 1 || page(next)["hasMore"] != false {
		t.Fatal(next)
	}
	if warm := reuseCall(t, m, "p", "batch", q); reusePath(warm) != "cache" || !reflect.DeepEqual(page(next), page(warm)) {
		t.Fatal(warm)
	}

	base := pipelineReuseQuery(t, "A")
	key, reason := reusableAggregateBatch(base, nil)
	if reason != "" {
		t.Fatal(reason)
	}
	for name, mutate := range map[string]func(*Request){
		"constant":   func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr.Concat[1].Const = "?" },
		"filter":     func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[0].Where.Value = "B" },
		"projection": func(r *Request) { r.Operations[0].Request.Select = []string{"label"} },
		"order":      func(r *Request) { r.Operations[0].Request.OrderBy[0].Direction = "desc" },
		"limit":      func(r *Request) { r.Operations[0].Request.Limit = 1 },
		"offset":     func(r *Request) { r.Operations[0].Request.Offset = 1 },
		"limits":     func(r *Request) { r.Operations[0].Request.Limits = &QueryLimits{Work: 1000} },
		"metric": func(r *Request) {
			r.Operations[1].Request.Metrics[0].Where = &Filter{Field: "caller", Op: "eq", Value: "x"}
		},
		"batch_order": func(r *Request) { r.Operations[0], r.Operations[1] = r.Operations[1], r.Operations[0] },
	} {
		t.Run(name, func(t *testing.T) {
			modified := pipelineReuseQuery(t, "A")
			mutate(&modified)
			data, why := reusableAggregateBatch(modified, nil)
			if data == nil || why != "" || bytes.Equal(key, data) {
				t.Fatalf("pipeline distinction lost in key: %s", why)
			}
			result := reuseCall(t, m, "p", "batch", modified)
			if reusePath(result) != "executed" {
				t.Fatal(result)
			}
		})
	}

	// A different database within one project must not inherit the entry.
	reuseCall(t, m, "p", "database_create", Request{Database: "other", Adapter: "aptevadb"})
	reuseCall(t, m, "p", "collection_create", Request{Database: "other", Collection: "facts", Fields: []Field{
		{Name: "id", Type: "text"}, {Name: "centre", Type: "text"}, {Name: "day", Type: "text"}, {Name: "caller", Type: "text"},
	}})
	isolated := pipelineReuseQuery(t, "A")
	isolated.Database = "other"
	result := reuseCall(t, m, "p", "batch", isolated)
	if reusePath(result) != "executed" || reuseRow(result, 1)["calls"] != "0" {
		t.Fatal(result)
	}

	d, release, err := m.acquireDatabase(context.Background(), "p", "d", "", false)
	checkErr(t, err)
	defer release()
	parsed, err := readCursor(cursor, d.key)
	checkErr(t, err)
	for _, expires := range []time.Time{time.Now().Add(-time.Minute), time.Now().Add(30 * time.Second)} {
		parsed.Expires = expires.Unix()
		signed := signCursor(d.key, parsed)
		query := pipelineReuseQuery(t, "A")
		query.Operations[0].Request.Cursor = signed
		if data, why := reusableAggregateBatch(query, d.key); data != nil || why != "cursor_validation" {
			t.Fatal(why)
		}
		_, err := m.Execute(context.Background(), "p", "batch", query)
		if expires.Before(time.Now()) && err == nil {
			t.Fatal("expired cursor accepted")
		}
		if expires.After(time.Now()) {
			checkErr(t, err)
		}
	}
	parsed.Expires = time.Now().Add(time.Hour).Unix()
	parsed.Hash = "wrong_pipeline"
	query := pipelineReuseQuery(t, "A")
	query.Operations[0].Request.Cursor = signCursor(d.key, parsed)
	if _, err := m.Execute(context.Background(), "p", "batch", query); err == nil {
		t.Fatal("mismatched signed cursor accepted")
	}
}

func TestPipelineReuseInvalidInputsNeverHit(t *testing.T) {
	m := reuseFixture(t, "cache")
	reuseCall(t, m, "p", "batch", pipelineReuseQuery(t, "A"))
	for name, mutate := range map[string]func(*Request){
		"expression": func(r *Request) {
			r.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr = RelExpr{Op: "unsupported"}
		},
		"cycle":          func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[0].From = "computed" },
		"source":         func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[0].Collection = "missing" },
		"cross_database": func(r *Request) { r.Operations[0].Request.Database = "other" },
		"version":        func(r *Request) { r.Operations[0].Request.Pipeline.Version = 2 },
	} {
		t.Run(name, func(t *testing.T) {
			query := pipelineReuseQuery(t, "A")
			mutate(&query)
			for range 2 {
				if _, err := m.Execute(context.Background(), "p", "batch", query); err == nil {
					t.Fatal("invalid pipeline returned cached result")
				}
			}
			if flights, _ := reuseCounts(m); flights != 0 {
				t.Fatal("failed flight retained")
			}
		})
	}
	for name, mutate := range map[string]func(*Request){
		"filter":     func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[0].Where.And = []Filter{} },
		"expression": func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr.Coalesce = []RelExpr{} },
		"projection": func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[0].Select = []string{} },
		"constant": func(r *Request) {
			r.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr.Concat[1].Const = map[string]any{}
		},
		"unsupported_node": func(r *Request) { r.Operations[0].Request.Pipeline.Nodes[1].Op = "future_random" },
		"get_pipeline":     func(r *Request) { r.Operations[0].Op = "get" },
	} {
		t.Run("skipped_"+name, func(t *testing.T) {
			query := pipelineReuseQuery(t, "A")
			mutate(&query)
			if data, why := reusableAggregateBatch(query, nil); data != nil || why != "unsupported_query" {
				t.Fatal(why)
			}
		})
	}
}

func TestPipelineReuseIntermediateAggregateAndOversize(t *testing.T) {
	m := reuseFixture(t, "cache")
	q := pipelineReuseQuery(t, "A")
	p := q.Operations[1].Request.Pipeline
	p.Nodes = append(p.Nodes, QueryNode{Name: "per_caller", Op: "aggregate", From: "source", GroupBy: []string{"caller"}, Metrics: []Metric{{Name: "calls", Op: "count"}}})
	p.From = "per_caller"
	q.Operations[1].Request.Metrics = []Metric{{Name: "callers", Op: "count"}, {Name: "calls", Op: "sum", Field: "calls"}}
	first := reuseCall(t, m, "p", "batch", q)
	if row := reuseRow(first, 1); row["calls"] != "3" || row["callers"] != "2" {
		t.Fatal(row)
	}
	if warm := reuseCall(t, m, "p", "batch", q); reusePath(warm) != "cache" || !reflect.DeepEqual(reuseRows(first), reuseRows(warm)) {
		t.Fatal(warm)
	}

	// Large results stay eligible for coalescing, but never occupy the cache.
	for _, id := range []string{"a", "b", "d"} {
		reuseCall(t, m, "p", "update", Request{Database: "d", Collection: "facts", Key: Record{"id": id},
			Set: Record{"caller": strings.Repeat("x", 190000)},
		})
	}
	q = pipelineReuseQuery(t, "A")
	q.Operations[0].Request.Limit = 10
	for range 2 {
		out := reuseCall(t, m, "p", "batch", q)
		diag := pipelineDiagnostic(t, out)
		if diag["reuse"] != "executed" || diag["cacheStore"] != "result_too_large" || diag["cacheResultBytes"].(int) < aggregateEntryBytes {
			t.Fatal(diag)
		}
	}
}

func TestPipelineReuseKeyPreservesJSONNumbers(t *testing.T) {
	q := pipelineReuseQuery(t, "A")
	q.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Expr = RelExpr{Const: json.Number("9007199254740993")}
	q.Operations[0].Request.Pipeline.Nodes[1].Computed[0].Type = "number"
	data, why := reusableAggregateBatch(q, nil)
	if why != "" || !bytes.Contains(data, []byte("9007199254740993")) {
		t.Fatal(why)
	}
}

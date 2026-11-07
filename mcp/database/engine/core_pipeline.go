package engine

import (
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// pipelineSchemaTx tracks every schema consulted by a plan, including lookup
// and exists dependencies, so continuation cursors cannot outlive schema changes.
type pipelineSchemaTx struct {
	transaction
	schemas map[string]int64
}

func (t pipelineSchemaTx) collection(name string) (Collection, error) {
	c, e := t.transaction.collection(name)
	if e == nil {
		t.schemas[name] = c.Version
	}
	return c, e
}
func (t pipelineSchemaTx) scanRecords(c Collection, q Query, f []string, visit func(Record) bool) error {
	return t.transaction.(recordScanner).scanRecords(c, q, f, visit)
}

func executePipelineRead(tx transaction, op string, r Request, key []byte) (any, error) {
	// Requests and shared pipeline inputs must stay reusable between calls.
	var copy Request
	if e := decode(marshal(r), &copy); e != nil {
		return nil, e
	}
	r = copy
	if r.Pipeline.Version != 1 {
		return nil, Invalid("pipeline requires version: 1")
	}
	tracked := pipelineSchemaTx{transaction: tx, schemas: map[string]int64{}}
	p, e := compileQuery(tracked, Request{Version: r.Pipeline.Version, Nodes: r.Pipeline.Nodes, Result: &QueryResult{From: r.Pipeline.From}, Limits: r.Limits})
	if e != nil {
		return nil, e
	}
	runtime := &queryRuntime{plan: p, cache: map[*queryNodePlan][]Record{}}
	if op == "aggregate" {
		return aggregatePipeline(tracked, r, runtime)
	}
	return findPipeline(r, runtime, tracked.schemas, key)
}
func aggregatePipeline(tx transaction, r Request, runtime *queryRuntime) (any, error) {
	base := runtime.plan.result
	view := base.view
	input := func(yield func(Record) (bool, error)) error { return runtime.stream(base, yield) }
	if len(r.Lookups) > 0 || len(r.Buckets) > 0 {
		a, e := prepareRelationAnalytics(tx, view, r)
		if e != nil {
			return nil, e
		}
		view = a.view
		prior := input
		input = func(yield func(Record) (bool, error)) error { return a.stream(prior, yield, runtime) }
	}
	if e := queryAggregateFields(view, QueryNode{Where: r.Where, GroupBy: r.GroupBy, Metrics: r.Metrics}); e != nil {
		return nil, e
	}
	if hasExists(r.Where) {
		return nil, Invalid("exists belongs in a pipeline scan where")
	}
	final := Request{Query: Query{Where: r.Where, OrderBy: r.OrderBy, Limit: r.Limit}, GroupBy: r.GroupBy, Metrics: r.Metrics}
	out, e := validateAggregate(view, &final)
	if e != nil {
		return nil, e
	}
	if e := validateDerived(out, r.Derived); e != nil {
		return nil, e
	}
	rows, e := aggregateRecordsSharedBudget(runtime.plan.budget.ctx, view, final, out, MaxQueryWorkBytes, func(q Query, _ []string, visit func(Record, []byte) bool) error {
		return input(func(row Record) (bool, error) {
			if !matches(view, row, q.Where) {
				return true, nil
			}
			return visit(row, nil), nil
		})
	}, runtime.retain)
	if e != nil {
		return nil, e
	}
	return finishAggregate(rows, final.Limit, r.Derived, runtime.retain)
}

type pipelinePageItem struct {
	row    Record
	values []any
	tie    string
}

func pipelineItemCompare(fields []Field, order []Order, a, b pipelinePageItem) int {
	for i, f := range fields {
		if cmp := compare(f, a.values[i], b.values[i]); cmp != 0 {
			if order[i].Direction == "desc" {
				return -cmp
			}
			return cmp
		}
	}
	if a.tie < b.tie {
		return -1
	}
	if a.tie > b.tie {
		return 1
	}
	return 0
}
func pipelineCursorHash(r Request, schemas map[string]int64) string {
	// Resource controls and the optional total do not change row membership.
	value := struct {
		Pipeline *ReadPipeline
		Where    *Filter
		Select   []string
		OrderBy  []Order
		Limit    int
		Schemas  map[string]int64
	}{r.Pipeline, r.Where, r.Select, r.OrderBy, r.Limit, schemas}
	hash := sha256.Sum256(marshal(value))
	return base64.RawURLEncoding.EncodeToString(hash[:])
}
func findPipeline(r Request, runtime *queryRuntime, schemas map[string]int64, key []byte) (any, error) {
	view := runtime.plan.result.view
	if hasExists(r.Where) {
		return nil, Invalid("exists belongs in a pipeline scan where")
	}
	if e := queryFilterFields(view, r.Where); e != nil {
		return nil, e
	}
	for _, o := range r.OrderBy {
		if _, e := queryField(view, o.Field); e != nil {
			return nil, e
		}
	}
	for _, name := range r.Select {
		if _, e := queryField(view, name); e != nil {
			return nil, e
		}
	}
	if e := validateQuery(view, &r.Query); e != nil {
		return nil, e
	}
	fields := make([]Field, len(r.OrderBy))
	for i, o := range r.OrderBy {
		fields[i], _ = queryField(view, o.Field)
	}
	hash := pipelineCursorHash(r, schemas)
	var after *pipelinePageItem
	if r.Cursor != "" {
		cursor, e := readCursor(r.Cursor, key)
		if e != nil {
			return nil, e
		}
		if cursor.Hash != hash || cursor.Version != 1 || cursor.Expires < time.Now().Unix() {
			return nil, Invalid("cursor does not match the pipeline or its schemas, or has expired")
		}
		tie, ok := cursor.Values["tie"].(string)
		raw, valuesOK := cursor.Values["order"].([]any)
		if !ok || !valuesOK || len(raw) != len(fields) {
			return nil, Invalid("invalid pipeline cursor")
		}
		after = &pipelinePageItem{tie: tie, values: make([]any, len(raw))}
		for i, value := range raw {
			f := fields[i]
			f.Nullable = true
			normal, e := normalize(f, value)
			if e != nil {
				return nil, Invalid("invalid pipeline cursor ordering value")
			}
			after.values[i] = normal
		}
	}
	items := []pipelinePageItem{}
	seen := map[string]int{}
	e := runtime.stream(runtime.plan.result, func(row Record) (bool, error) {
		if !matches(view, row, r.Where) {
			return true, nil
		}
		digest := sha256.Sum256(marshal(row))
		identity := base64.RawURLEncoding.EncodeToString(digest[:])
		if _, ok := seen[identity]; !ok {
			if e := runtime.retain(96 + len(identity)); e != nil {
				return false, e
			}
		}
		seen[identity]++
		tie := identity + fmt.Sprintf("-%010d", seen[identity])
		output := row
		if len(r.Select) > 0 {
			output = project(row, r.Select)
		} else {
			output = queryProject(view, row)
		}
		item := pipelinePageItem{row: output, tie: tie, values: make([]any, len(fields))}
		for i, o := range r.OrderBy {
			item.values[i] = row[o.Field]
		}
		size := 96 + len(tie) + retainedBytes(output) + retainedBytes(item.values)
		if e := runtime.retain(size); e != nil {
			return false, e
		}
		items = append(items, item)
		return true, nil
	})
	if e != nil {
		return nil, e
	}
	sort.Slice(items, func(a, b int) bool { return pipelineItemCompare(fields, r.OrderBy, items[a], items[b]) < 0 })
	if e := runtime.plan.budget.ctx.Err(); e != nil {
		return nil, e
	}
	start := min(r.Offset, len(items))
	if after != nil {
		start = sort.Search(len(items), func(i int) bool { return pipelineItemCompare(fields, r.OrderBy, items[i], *after) > 0 })
	}
	records := []Record{}
	size := 0
	at := start
	for at < len(items) && len(records) < r.Limit {
		item := items[at]
		bytes := len(marshal(item.row))
		if size+bytes > MaxBytes {
			if len(records) == 0 {
				return nil, fail("resource_limit", "one pipeline record exceeds 4 MiB; select fewer fields")
			}
			break
		}
		records = append(records, item.row)
		size += bytes
		at++
	}
	more := at < len(items)
	result := map[string]any{"records": records, "hasMore": more, "nextCursor": ""}
	if r.Count {
		result["total"] = strconv.Itoa(len(items))
	}
	if more && len(records) > 0 {
		last := items[at-1]
		cursor := signCursor(key, cursorData{Hash: hash, Version: 1, Expires: time.Now().Add(time.Hour).Unix(), Values: Record{"tie": last.tie, "order": last.values}})
		if len(cursor) > 32768 {
			return nil, fail("resource_limit", "pipeline ordering values exceed cursor size limit")
		}
		result["nextCursor"] = cursor
	}
	return result, nil
}

package engine

import (
	"context"
	"errors"
	"sync"

	native "github.com/apteva/db"
)

// An aggregate group owns a single projected scan. Each member still applies
// its own metric filters, ordering, limit and derived expressions.
type batchAggregateGroup struct {
	once       sync.Once
	collection Collection
	input      Query
	members    []batchAggregateMember
	results    map[int]any
}

type batchAggregateMember struct {
	index   int
	request Request
	schema  Collection
}

// Use scan sharing on native read snapshots. Keep the normal executor available
// for unsupported shapes and when independent scans are needed to fit budgets.
func batchAggregateGroups(tx transaction, ops []Operation, workers int) map[int]*batchAggregateGroup {
	native := nativeBatchTx(tx)
	if native == nil || workers < 2 {
		return nil
	}
	type scanKey struct {
		collection   string
		where        string
		requireIndex bool
	}
	groups := map[scanKey]*batchAggregateGroup{}
	for i, op := range ops {
		r := op.Request
		if op.Op != "aggregate" || r.Diagnostics || r.Pipeline != nil || len(r.Derived) != 0 || len(r.Lookups) != 0 || len(r.Buckets) != 0 || hasExists(r.Where) {
			continue
		}
		kind, err := planCoreRead(op.Op, r)
		if err != nil || kind != "aggregate" {
			continue
		}
		c, err := tx.collection(r.Collection)
		if err != nil {
			continue
		}
		// Validation normalizes filters in place. Keep the original operation
		// available for the ordinary executor, including its error ordering.
		if err := decode(marshal(r), &r); err != nil {
			continue
		}
		out, err := validateAggregate(c, &r)
		if err != nil || validateDerived(out, r.Derived) != nil || !shareableAggregate(c, r) {
			continue
		}
		key := scanKey{c.Name, string(marshal(r.Where)), r.RequireIndex}
		group := groups[key]
		if group == nil {
			group = &batchAggregateGroup{collection: c, input: Query{Where: r.Where, RequireIndex: r.RequireIndex}}
			groups[key] = group
		}
		group.members = append(group.members, batchAggregateMember{index: i, request: r, schema: out})
	}
	byIndex := map[int]*batchAggregateGroup{}
	for _, group := range groups {
		if len(group.members) > 1 && len(group.members) <= min(workers, native.backend.options.AggregateWorkers) {
			for _, member := range group.members {
				byIndex[member.index] = group
			}
		}
	}
	return byIndex
}

func nativeBatchTx(tx transaction) *aptevaTx {
	switch t := tx.(type) {
	case *aptevaTx:
		if t.batch == nil {
			return t
		}
	case cachedSchemaTx:
		return nativeBatchTx(t.transaction)
	}
	return nil
}

// Floating sums and averages depend on reduction order. Leave them on their
// established executor. The shared reducer uses the same checked integer sum
// as the native path's exact fallback.
func shareableAggregate(c Collection, r Request) bool {
	if len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
		return false // Keep native index-only counts.
	}
	for _, name := range r.GroupBy {
		f, _ := c.field(name)
		if f.Type == "number" {
			return false
		}
	}
	for _, metric := range r.Metrics {
		f, _ := c.field(metric.Field)
		if metric.Op == "avg" || f.Type == "json" || f.Type == "number" || hasExists(metric.Where) {
			return false
		}
	}
	return true
}

var errSharedScanAborted = errors.New("shared aggregate scan stopped")

// Feed the native reducers in parallel from one reusable chunk. The scan
// copies each borrowed document once. Every reducer acknowledges the chunk
// before the cursor's producer reuses its storage.
func (g *batchAggregateGroup) run(ctx context.Context, source native.Source, workLimit, maxGroups int) map[int]any {
	scanLimit := min(workLimit/8, 512<<10)
	reducerLimit := (workLimit - scanLimit) / len(g.members)
	type entry struct{ keyEnd, docEnd int }
	// Reserve space for both the chunk and the cursor's current document.
	chunk := make([]byte, 0, scanLimit*3/8)
	entries := make([]entry, 0, min(1024, scanLimit/64))
	if cap(entries) == 0 {
		return nil
	}
	type reducer struct {
		input    chan struct{}
		spec     nativeAggregationPlan
		pipeline *native.Pipeline
		values   []map[string]any
		err      error
	}
	reducers := make([]reducer, len(g.members))
	for i, member := range g.members {
		r := &reducers[i]
		r.spec = nativeAggregatePlan(g.collection, member.request)
		r.spec.prefix = nil // the shared cursor applies the common predicate
		pipeline, err := native.ParsePipeline(nativeAggregatePipeline(member.request, r.spec))
		if err != nil {
			return nil
		}
		pipeline.SetLimits(nativeLimits(member.request, reducerLimit, maxGroups))
		r.pipeline = pipeline
		r.input = make(chan struct{})
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ack := make(chan struct{}, len(reducers))
	var workers sync.WaitGroup
	for i := range reducers {
		r := &reducers[i]
		workers.Go(func() {
			r.values, r.err = r.pipeline.Run(runCtx, func(consume func([]byte, []byte) bool) error {
				for range r.input {
					offset := 0
					for _, entry := range entries {
						if !consume(chunk[offset:entry.keyEnd], chunk[entry.keyEnd:entry.docEnd]) {
							cancel()
							return errSharedScanAborted
						}
						offset = entry.docEnd
					}
					ack <- struct{}{}
				}
				return runCtx.Err()
			})
			if r.err != nil {
				cancel()
			}
		})
	}

	flush := func() bool {
		if len(entries) == 0 {
			return runCtx.Err() == nil
		}
		for i := range reducers {
			select {
			case reducers[i].input <- struct{}{}:
			case <-runCtx.Done():
				return false
			}
		}
		for range reducers {
			select {
			case <-ack:
			case <-runCtx.Done():
				return false
			}
		}
		chunk = chunk[:0]
		entries = entries[:0]
		return true
	}
	failed := false
	err := source(func(key, doc []byte) bool {
		size := len(key) + len(doc)
		if runCtx.Err() != nil || size > cap(chunk) ||
			(len(chunk)+size > cap(chunk) || len(entries) == cap(entries)) && !flush() {
			failed = true
			return false
		}
		chunk = append(chunk, key...)
		keyEnd := len(chunk)
		chunk = append(chunk, doc...)
		entries = append(entries, entry{keyEnd, len(chunk)})
		return true
	})
	ok := err == nil && !failed && flush()
	if !ok {
		cancel()
	}
	for i := range reducers {
		close(reducers[i].input)
	}
	workers.Wait()
	if !ok || runCtx.Err() != nil {
		return nil
	}

	results := make(map[int]any, len(g.members))
	for i, member := range g.members {
		r := &reducers[i]
		if r.err != nil {
			return nil
		}
		rows, err := normalizeNativeAggregateRows(ctx, g.collection, member.request, member.schema, r.spec, r.values, reducerLimit)
		if err != nil {
			return nil
		}
		result, err := finishAggregate(rows, member.request.Limit, member.request.Derived)
		if err != nil {
			return nil
		}
		results[member.index] = result
	}
	return results
}

func (t *aptevaTx) batchAggregateSource(c Collection, q Query) native.Source {
	return func(yield func([]byte, []byte) bool) error {
		if q.RequireIndex && !plan(c, q).IndexedLookup {
			return fail("query_too_expensive", "query requires a selective index")
		}
		h, err := t.readCollection(c)
		if err != nil {
			return err
		}
		q, err = t.selectLookupIndex(c, q)
		if err != nil {
			return err
		}
		opts := nativeScanOptions(c, q)
		opts.MaxMemoryBytes = int64(t.backend.options.QueryWorkBytes)
		eachNativeScanFilter(c, q, func(where *Filter) bool {
			more := true
			err = h.Query(t.ctx, marshal(nativeFilter(c, where)), opts, func(key, doc []byte) bool {
				more = yield(key, doc)
				return more
			})
			return err == nil && more
		})
		return err
	}
}

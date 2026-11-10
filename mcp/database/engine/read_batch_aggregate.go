package engine

import (
	"context"
	"errors"
	"sort"
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
	decision   *batchAggregateDecision
}

type batchAggregateMember struct {
	index   int
	request Request
	schema  Collection
}

// Use scan sharing on native read snapshots. Keep the normal executor available
// for unsupported shapes and when independent scans are needed to fit budgets.
func batchAggregateGroups(tx transaction, ops []Operation, workers int, decisions *[]*batchAggregateDecision) map[int]*batchAggregateGroup {
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
		if len(group.members) < 2 {
			continue
		}
		choice := native.chooseBatchAggregateScan(group, workers)
		if decisions != nil {
			choice.Operations = make([]int, 0, len(group.members))
			for _, member := range group.members {
				choice.Operations = append(choice.Operations, member.index)
			}
			group.decision = &choice
			*decisions = append(*decisions, group.decision)
		}
		if choice.Path == "shared" {
			for _, member := range group.members {
				byIndex[member.index] = group
			}
		}
	}
	if decisions != nil {
		sort.Slice(*decisions, func(i, j int) bool {
			return (*decisions)[i].Operations[0] < (*decisions)[j].Operations[0]
		})
	}
	return byIndex
}

type batchAggregateDecision struct {
	Collection      string  `json:"collection"`
	Index           string  `json:"index,omitempty"`
	Operations      []int   `json:"operations"`
	Path            string  `json:"path"`
	Reason          string  `json:"reason"`
	CandidateRows   int64   `json:"candidateRows"`
	ProjectedBytes  int64   `json:"projectedBytes"`
	DistinctStates  int     `json:"distinctStates"`
	SharedCost      float64 `json:"sharedCost"`
	IndependentCost float64 `json:"independentCost"`
}

func (t *aptevaTx) chooseBatchAggregateScan(g *batchAggregateGroup, workers int) batchAggregateDecision {
	d := batchAggregateDecision{Collection: g.collection.Name, Path: "independent", Reason: "worker_capacity"}
	reducers := len(g.members)
	options := t.backend.options
	if reducers > min(workers, options.AggregateWorkers) {
		return d
	}
	c := g.collection
	opts := nativeScanOptions(c, g.input)
	d.Index = opts.Hint
	if g.input.RequireIndex && opts.Hint == "$natural" {
		d.Reason = "required_index_unavailable"
		return d
	}
	d.Reason = "candidate_estimate_unavailable"
	h, err := t.readCollection(c)
	if err != nil {
		return d
	}
	eachNativeScanFilter(c, g.input, func(where *Filter) bool {
		var rows int
		rows, err = h.EstimateQueryCandidates(t.ctx, marshal(nativeFilter(c, where)), opts)
		d.CandidateRows += int64(rows)
		return err == nil
	})
	if err != nil {
		return d
	}

	// Compare scan, projection and exact distinct work in byte-equivalent
	// units. The producer copies raw records, even though reducers decode
	// only their projected fields. Estimate one distinct entry per candidate
	// as a conservative planning allowance. The executor still enforces its
	// memory limit using actual sizes.
	rowWidth := 32
	for _, field := range c.Fields {
		rowWidth += batchAggregateFieldWidth(field) + len(field.Name)
	}
	var totalReducer, peakReducer, peakDistinct int
	for _, member := range g.members {
		fields := map[string]bool{}
		for _, field := range member.request.GroupBy {
			fields[field] = true
		}
		distinct := 0
		for _, metric := range member.request.Metrics {
			fields[metric.Field] = metric.Field != ""
			visitFilterFields(metric.Where, func(field string) { fields[field] = true })
			if metric.Op == "count_distinct" {
				f, _ := c.field(metric.Field)
				distinct += 48 + batchAggregateFieldWidth(f)
				d.DistinctStates++
			}
		}
		projection := 0
		for _, f := range c.Fields {
			if fields[f.Name] {
				projection += batchAggregateFieldWidth(f)
			}
		}
		d.ProjectedBytes += int64(projection) * d.CandidateRows
		work := 16 + projection + distinct
		totalReducer += work
		peakReducer = max(peakReducer, work)
		peakDistinct = max(peakDistinct, distinct)
	}

	rows := float64(max(1, d.CandidateRows))
	// Small inputs rarely fill several native shards. Wide inputs benefit
	// from their parallel scans and avoid repeated chunk rendezvous.
	parallel := 1 + float64(min(workers, options.AggregateWorkers, 4)-1)*min(1, rows/8192)*0.60
	scanBudget := min(reducers*options.QueryWorkBytes/8, 512<<10)
	chunkRows := min(1024, scanBudget*3/8/rowWidth)
	handoffs := max(0, rows/float64(max(1, chunkRows))-1)
	scan := float64(64 + rowWidth)
	d.SharedCost = rows*(scan+float64(rowWidth)/2+float64(peakReducer)) + handoffs*float64(4096*reducers)
	d.IndependentCost = rows * (scan*float64(reducers) + float64(totalReducer)) / parallel
	reducerBudget := options.QueryWorkBytes - scanBudget/reducers
	switch {
	case rows*float64(peakDistinct) > float64(reducerBudget):
		d.Reason = "distinct_state_budget"
	case d.IndependentCost <= d.SharedCost:
		d.Reason = "parallel_scans_cheaper"
	default:
		d.Path, d.Reason = "shared", "repeated_scan_cost"
	}
	return d
}

func batchAggregateFieldWidth(f Field) int {
	switch f.Type {
	case "text", "datetime":
		return 32
	case "json":
		return 128
	case "boolean":
		return 1
	default:
		return 8
	}
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
		if metric.Op == "avg" || metric.Op == "merge_distinct" || f.Type == "json" || f.Type == "number" || hasExists(metric.Where) {
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

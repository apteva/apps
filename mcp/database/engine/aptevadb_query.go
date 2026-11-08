package engine

import (
	"strconv"

	native "github.com/apteva/db"
)

func nativeFilter(c Collection, f *Filter) any {
	if f == nil {
		return map[string]any{}
	}
	if f.And != nil || f.Or != nil {
		children := f.And
		op := "$and"
		if f.Or != nil {
			children = f.Or
			op = "$or"
		}
		out := make([]any, len(children))
		for i := range children {
			out[i] = nativeFilter(c, &children[i])
		}
		return map[string]any{op: out}
	}
	if f.Fields != nil {
		if searchAll(f) {
			return map[string]any{}
		}
		return map[string]any{"$like": map[string]any{"fields": f.Fields, "separator": " ", "pattern": searchPattern(f.Value.(string))}}
	}
	if f.Exists != nil || f.NotExists != nil {
		// Native engines reject this key: a path that forgets stripExists fails
		// instead of dropping a permission filter.
		return map[string]any{"$exists_needs_evaluation": true}
	}
	if f.Op == opContainsASCII {
		if searchAll(f) {
			return map[string]any{}
		}
		return map[string]any{"$like": map[string]any{"fields": []string{f.Field}, "literal": f.Value}}
	}
	if f.Op == "is_null" {
		return map[string]any{f.Field: map[string]any{"$eq": nil}}
	}
	if f.Op == "is_not_null" {
		return map[string]any{f.Field: map[string]any{"$ne": nil}}
	}
	field, _ := c.field(f.Field)
	convert := func(v any) any {
		if v != nil && field.Type == "integer" {
			n, _ := strconv.ParseInt(v.(string), 10, 64)
			return n
		}
		return v
	}
	if f.Op == "not_in" {
		terms := []any{map[string]any{f.Field: map[string]any{"$ne": nil}}}
		for _, v := range f.Value.([]any) {
			terms = append(terms, map[string]any{f.Field: map[string]any{"$ne": convert(v)}})
		}
		return map[string]any{"$and": terms}
	}
	var value any
	if f.Op == "in" {
		values := f.Value.([]any)
		a := make([]any, len(values))
		for i, v := range values {
			a[i] = convert(v)
		}
		value = a
	} else {
		value = convert(f.Value)
	}
	op := "$" + f.Op
	if f.Op == "starts_with" {
		op = "$prefix"
	}
	// Non-null equality already excludes null. Keeping a redundant $ne here
	// prevents the native count planner from proving scalar index coverage.
	if f.Op == "eq" && value != nil {
		return map[string]any{f.Field: map[string]any{op: value}}
	}
	// Portable comparisons exclude null, including ne/lt/lte. Keep that gate
	// separate so it cannot collide with an explicit $ne predicate.
	return map[string]any{"$and": []any{map[string]any{f.Field: map[string]any{"$ne": nil}}, map[string]any{f.Field: map[string]any{op: value}}}}
}
func nativeQueryOptions(c Collection, q Query, limit int) native.QueryOptions {
	p := plan(c, q)
	opts := native.QueryOptions{Limit: limit, MaxMemoryBytes: MaxQueryWorkBytes}
	if p.ranges != nil {
		// The native planner expands the same in prefix, and reads documents by
		// key only while that is cheaper than scanning them. A hint would give it
		// one range over the whole index.
		for _, o := range q.OrderBy {
			opts.Sort = append(opts.Sort, native.IndexField{Field: o.Field, Desc: o.Direction == "desc"})
		}
		return opts
	}
	if !p.Sort && len(q.OrderBy) > 0 {
		// Physical record keys already encode the complete typed primary tuple in
		// ascending order. Native index suffixes therefore supply the app's stable
		// primary tie-breaker without duplicating long keys in the index fields.
		opts.Hint = p.Index
		for _, idx := range indexes(c) {
			if idx.Name == p.Index {
				for _, o := range idx.Fields {
					opts.Sort = append(opts.Sort, native.IndexField{Field: o.Field, Desc: o.Direction == "desc"})
				}
				break
			}
		}
	} else {
		for _, o := range q.OrderBy {
			opts.Sort = append(opts.Sort, native.IndexField{Field: o.Field, Desc: o.Direction == "desc"})
		}
		if p.IndexedLookup {
			opts.Hint = p.Index
			if len(q.OrderBy) == 0 {
				// Supply the complete physical index order to stream matching rows.
				// A filter-only native plan otherwise retains all candidate documents.
				for _, idx := range indexes(c) {
					if idx.Name == p.Index {
						for _, o := range idx.Fields {
							opts.Sort = append(opts.Sort, native.IndexField{Field: o.Field, Desc: o.Direction == "desc"})
						}
						break
					}
				}
			}
		} else if len(q.OrderBy) == 0 {
			opts.Hint = "$natural"
		}
	}
	return opts
}

func nativeQueryFilter(c Collection, q Query) any {
	filter := nativeFilter(c, q.Where)
	p := plan(c, q)
	if q.after == nil || p.Sort {
		return filter
	}
	// The signed cursor's full predicate checks ties. Also bound the leading
	// varying index component so a late page seeks near its position rather
	// than rescanning every preceding page. Preserve nulls in descending order.
	equal := map[string]bool{}
	for _, term := range conjuncts(q.Where) {
		if term.Op == "eq" || term.Op == "is_null" {
			equal[term.Field] = true
		}
	}
	for _, idx := range indexes(c) {
		if idx.Name != p.Index {
			continue
		}
		for _, o := range idx.Fields {
			if equal[o.Field] {
				continue
			}
			value, exists := q.after[o.Field]
			if !exists {
				return filter
			}
			field, _ := c.field(o.Field)
			if o.Direction == "desc" && value != nil && field.Nullable {
				// A native typed range excludes the null partition. It follows
				// non-null values in descending app order, so keep the full scan.
				return filter
			}
			op := "$gte"
			if o.Direction == "desc" {
				op = "$lte"
			}
			if value == nil {
				if o.Direction != "desc" {
					return filter
				}
				op = "$eq"
			} else if field.Type == "integer" {
				value, _ = strconv.ParseInt(value.(string), 10, 64)
			}
			return map[string]any{"$and": []any{filter, map[string]any{o.Field: map[string]any{op: value}}}}
		}
	}
	return filter
}
func (t *aptevaTx) find(c Collection, q Query, limit int) ([]Record, error) {
	var selectionErr error
	q, selectionErr = t.selectLookupIndex(c, q)
	if selectionErr != nil {
		return nil, selectionErr
	}
	if q.RequireIndex && !plan(c, q).IndexedLookup {
		return nil, fail("query_too_expensive", "query requires a selective index")
	}
	evaluate := hasExists(q.Where)
	if len(t.pending) > 0 || plan(c, q).Sort {
		// Project before retaining a bounded top page; do not keep a second
		// native heap of complete documents alongside the adapter's results.
		return t.findPending(c, q, limit)
	}
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	if u := plan(c, q).union; u != nil && t.unionIsCheaper(h, c, *u, q, limit) {
		return t.findPending(c, q, limit)
	}
	opts := nativeQueryOptions(c, q, limit)
	bound := q
	if evaluate {
		// The native limit would count candidates that the exists nodes reject.
		opts.Limit = 0
		bound.Where = stripExists(q.Where)
	}
	workLimit := t.backend.options.QueryWorkBytes
	opts.MaxMemoryBytes = int64(workLimit)
	var stats native.QueryStats
	opts.Stats = &stats
	out := []Record{}
	retained := 0
	var readErr error
	// Decode only retained fields into scratch; retainSelected copies them out.
	var names []string
	if len(q.Select) > 0 {
		names = append(names, q.Select...)
		for _, o := range q.OrderBy {
			names = append(names, o.Field)
		}
	}
	var filterFields *Filter
	if evaluate {
		filterFields = q.Where
	}
	decoder := newNativeScanDecoder(c, names, filterFields)
	e = h.Query(t.ctx, marshal(nativeQueryFilter(c, bound)), opts, func(_, data []byte) bool {
		if readErr = t.ctx.Err(); readErr != nil {
			return false
		}
		var r Record
		r, readErr = decoder.decode(data)
		if readErr != nil {
			return false
		}
		if evaluate && !matches(c, r, q.Where) {
			return true
		}
		r = retainSelected(r, q)
		retained += retainedBytes(r)
		if retained+int(stats.PeakMemoryBytes) > workLimit {
			readErr = fail("resource_limit", "retained query results exceed %d MiB", workLimit>>20)
			return false
		}
		out = append(out, r)
		return len(out) < limit
	})
	if e != nil {
		return nil, nativeError(e)
	}
	if readErr != nil {
		return nil, readErr
	}
	if retained+int(stats.PeakMemoryBytes) > workLimit {
		return nil, fail("resource_limit", "retained query results exceed %d MiB", workLimit>>20)
	}
	return out, t.ctx.Err()
}

// unionIsCheaper compares reading a page through an ordered stream, which visits
// about limit*size/matches documents, with reading every match by key and sorting
// them, which costs several times more per document. Both counts come from index
// entries alone. Any doubt keeps the stream.
func (t *aptevaTx) unionIsCheaper(h *native.Collection, c Collection, u accessPlan, q Query, limit int) bool {
	scope := unionScope(c, u, q.Where)
	if scope == nil {
		return false
	}
	opts := native.CountOptions{MaxMemoryBytes: int64(t.backend.options.QueryWorkBytes)}
	matches, e := h.CountDocuments(t.ctx, marshal(nativeFilter(c, scope)), opts)
	if e != nil {
		return false
	}
	size, e := h.CountDocuments(t.ctx, []byte(`{}`), opts)
	if e != nil {
		return false
	}
	return 4*matches*matches < int64(limit)*size
}

// unionScope keeps the predicates a union's index covers, so counting them reads
// no documents: listed values on its leading fields, then the following range.
func unionScope(c Collection, u accessPlan, where *Filter) *Filter {
	terms := conjuncts(where)
	for _, idx := range indexes(c) {
		if idx.Name != u.Index {
			continue
		}
		scope := []Filter{}
		for _, o := range idx.Fields {
			listed, ranged := []Filter{}, []Filter{}
			for _, p := range terms {
				if p.Field != o.Field {
					continue
				}
				switch p.Op {
				case "eq", "is_null", "in":
					listed = append(listed, p)
				case "gt", "gte", "lt", "lte":
					ranged = append(ranged, p)
				}
			}
			if len(listed) > 0 {
				scope = append(scope, listed...)
				continue
			}
			scope = append(scope, ranged...)
			break
		}
		if len(scope) > 0 {
			return &Filter{And: scope}
		}
	}
	return nil
}

func (t *aptevaTx) findPending(c Collection, q Query, limit int) ([]Record, error) {
	order := q
	if len(order.OrderBy) == 0 {
		for _, p := range c.PrimaryKey {
			order.OrderBy = append(order.OrderBy, Order{p, "asc"})
		}
	}
	top := &topRecords{collection: c, query: order, limit: limit, workLimit: t.backend.options.QueryWorkBytes}
	var fields []string
	if len(q.Select) > 0 {
		fields = append(fields, q.Select...)
		for _, o := range order.OrderBy {
			fields = append(fields, o.Field)
		}
	}
	e := t.scanFields(c, q, fields, func(r Record, _ []byte) bool { return true }, func(r Record, scanBytes int) error {
		// The scan buffer, decoded scratch and a possible projected copy live
		// alongside the retained page, rather than receiving separate budgets.
		top.workLimit = t.backend.options.QueryWorkBytes - scanBytes
		if top.workLimit <= 0 || top.bytes > top.workLimit {
			return fail("resource_limit", "scan and retained page exceed query memory budget")
		}
		if e := top.offer(r); e != nil {
			return e
		}
		if q.diag != nil {
			q.diag.pageHeapBytes = max(q.diag.pageHeapBytes, top.bytes)
			q.diag.scanBytes = max(q.diag.scanBytes, scanBytes)
			q.diag.pagePeakBytes = max(q.diag.pagePeakBytes, top.bytes+scanBytes)
		}
		return nil
	})
	if e != nil {
		return nil, e
	}
	out := make([]Record, 0, len(top.rows))
	for _, r := range top.rows {
		out = append(out, r.row)
	}
	sorted(c, out, order.OrderBy)
	return out, nil
}

// Native query execution is streaming. A write overlay masks old snapshot rows
// and contributes staged records, so filtered writes/upserts see earlier batch
// operations without repeatedly rebuilding an optimistic transaction's batch.
func (t *aptevaTx) scanFields(c Collection, q Query, fields []string, consume func(Record, []byte) bool, offer func(Record, int) error) error {
	if q.RequireIndex && !plan(c, q).IndexedLookup {
		return fail("query_too_expensive", "query requires a selective index")
	}
	h, e := t.readCollection(c)
	if e != nil {
		return e
	}
	// This is an unordered stream: retain the full predicate and internal
	// index selection, but leave paging and result ordering to the consumer.
	stream := Query{Where: q.Where, hint: q.hint, diag: q.diag}
	stream, e = t.selectLookupIndex(c, stream)
	if e != nil {
		return e
	}
	opts := nativeScanOptions(c, stream)
	opts.MaxMemoryBytes = int64(t.backend.options.QueryWorkBytes)
	total := q.diag.nativeStats("documents")
	if q.examined != nil {
		if total == nil {
			total = &native.QueryStats{}
		}
		defer func() { *q.examined += total.DocumentFetches }()
	}
	var stats native.QueryStats
	if total != nil || offer != nil {
		opts.Stats = &stats
	}
	decoder := newNativeScanDecoder(c, fields, q.Where)
	var consumeErr error
	stopped := false
	visit := func(data []byte) bool {
		if consumeErr = t.ctx.Err(); consumeErr != nil {
			return false
		}
		r, e := decoder.decode(data)
		if e != nil {
			consumeErr = e
			return false
		}
		if !matches(c, r, q.Where) {
			return true
		}
		if offer != nil {
			// Reserve decoded scratch and one possible projected copy while the
			// previous heap row is still live. Native statistics cover retained
			// engine buffers; the current borrowed document is accounted separately.
			decodedBytes := retainedBytes(r)
			if q.diag != nil {
				q.diag.pageDecodedBytes = max(q.diag.pageDecodedBytes, decodedBytes)
			}
			scanBytes := len(data) + 2*decodedBytes + int(opts.Stats.PeakMemoryBytes)
			consumeErr = offer(r, scanBytes)
			return consumeErr == nil
		}
		stopped = !consume(r, data)
		return !stopped
	}
	eachNativeScanFilter(c, stream, func(where *Filter) bool {
		e = h.Query(t.ctx, marshal(nativeFilter(c, stripExists(where))), opts, func(key, data []byte) bool {
			if _, changed := t.pending[nativeRowKey{c.Name, string(key)}]; changed {
				return true
			}
			return visit(data)
		})
		if total != nil {
			total.DocumentFetches += stats.DocumentFetches
			total.IndexEntriesExamined += stats.IndexEntriesExamined
			total.Examined += stats.Examined
			total.Returned += stats.Returned
			total.PeakMemoryBytes = max(total.PeakMemoryBytes, stats.PeakMemoryBytes)
			ranges := total.Plan.Ranges + 1
			total.Plan = stats.Plan
			total.Plan.Ranges = ranges
		}
		return e == nil && consumeErr == nil && !stopped
	})
	if e != nil {
		return nativeError(e)
	}
	if consumeErr != nil {
		return consumeErr
	}
	if !stopped {
		for key, data := range t.pending {
			if key.collection == c.Name && data != nil {
				if !visit(data) {
					break
				}
			}
		}
	}
	if consumeErr != nil {
		return consumeErr
	}
	return t.ctx.Err()
}
func (t *aptevaTx) explain(c Collection, q Query) (any, error) {
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	opts := nativeQueryOptions(c, q, 1)
	opts.MaxMemoryBytes = int64(t.backend.options.QueryWorkBytes)
	bound := q
	bound.Where = stripExists(q.Where)
	np, e := h.ExplainQuery(t.ctx, marshal(nativeQueryFilter(c, bound)), opts)
	if e != nil {
		return nil, nativeError(e)
	}
	p := plan(c, q)
	p.Index = np.Index
	p.Sort = np.SortInMemory
	p.IndexedLookup = p.IndexedLookup && np.Index != ""
	return map[string]any{"adapter": "aptevadb", "plan": p, "nativePlan": np, "candidateLimit": nil, "maxRetainedBytes": t.backend.options.QueryWorkBytes, "aggregateWorkers": t.backend.options.AggregateWorkers, "cacheBytes": t.backend.options.CacheBytes, "mappedCacheBytes": t.backend.options.MappedCacheBytes, "managedMemoryBytes": t.backend.options.ManagedMemoryBytes, "aggregateDiskBytes": t.backend.options.AggregateDiskBytes, "aggregateMaxGroups": t.backend.options.AggregateMaxGroups, "aggregateStrategy": "parallel-then-bounded-stream", "syncMode": t.backend.options.SyncMode, "synchronousWrites": true, "executionBound": "deadline"}, nil
}
func (t *aptevaTx) aggregate(c Collection, r Request, outSchema Collection) ([]Record, error) {
	if hasExists(r.Where) {
		// Exists nodes probe from each candidate on this snapshot.
		return t.aggregatePortable(c, r, outSchema)
	}
	if r.analytics != nil {
		// Alpha.11 evaluates lookups, date buckets, distinct counts and
		// filtered metrics natively. Staged writes and buckets used outside
		// groupBy keep the portable accumulator on this snapshot.
		if len(t.pending) == 0 && nativeAnalyticsSupported(r) {
			return t.aggregateNative(c, r, outSchema)
		}
		return t.aggregatePortable(c, r, outSchema)
	}
	if rows, ok, err := t.aggregatePrimaryCount(c, r); ok {
		r.diag.strategy("primary-count")
		return rows, err
	}
	if len(t.pending) == 0 && len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
		var selectionErr error
		r.Query, selectionErr = t.selectLookupIndex(c, r.Query)
		if selectionErr != nil {
			return nil, selectionErr
		}
		p := plan(c, r.Query)
		if nativeCountHasResidual(c, r.Query, p) {
			// Native CountDocuments treats its hint as a covering-index hint
			// and discards it on residual fallback. Stream the selected lookup
			// with the complete filter instead of widening to another index.
			var count int64
			e := t.scanFields(c, r.Query, c.PrimaryKey, func(Record, []byte) bool { count++; return true }, nil)
			if e != nil {
				return nil, e
			}
			return []Record{{r.Metrics[0].Name: strconv.FormatInt(count, 10)}}, t.ctx.Err()
		}
		if r.RequireIndex && !p.IndexedLookup {
			return nil, fail("query_too_expensive", "query requires a selective index")
		}
		h, e := t.readCollection(c)
		if e != nil {
			return nil, e
		}
		opts := native.CountOptions{MaxMemoryBytes: int64(t.backend.options.QueryWorkBytes)}
		if p.IndexedLookup && p.ranges == nil {
			opts.Hint = p.Index
		}
		opts.Stats = r.diag.nativeStats("native-count")
		count, e := h.CountDocuments(t.ctx, marshal(nativeFilter(c, r.Where)), opts)
		if e != nil {
			return nil, nativeError(e)
		}
		return []Record{{r.Metrics[0].Name: strconv.FormatInt(count, 10)}}, t.ctx.Err()
	}
	if r.Where == nil && !r.RequireIndex && len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
		h, e := t.readCollection(c)
		if e != nil {
			return nil, e
		}
		var count int64
		var scanErr error
		e = h.Scan(nil, nil, func(key, _ []byte) bool {
			if scanErr = t.ctx.Err(); scanErr != nil {
				return false
			}
			if _, changed := t.pending[nativeRowKey{c.Name, string(key)}]; !changed {
				count++
			}
			return true
		})
		if e != nil {
			return nil, e
		}
		if scanErr != nil {
			return nil, scanErr
		}
		for key, v := range t.pending {
			if key.collection == c.Name && v != nil {
				count++
			}
		}
		return []Record{{r.Metrics[0].Name: strconv.FormatInt(count, 10)}}, t.ctx.Err()
	}
	if len(t.pending) == 0 {
		return t.aggregateNative(c, r, outSchema)
	}
	return t.aggregatePortable(c, r, outSchema)
}

// unorderedRoots: scan callbacks run outside the database lock, so joins can read
// during them.
func (t *aptevaTx) unorderedRoots() bool { return true }

func (t *aptevaTx) scanRecords(c Collection, q Query, fields []string, consume func(Record) bool) error {
	return t.scanFields(c, q, fields, func(r Record, _ []byte) bool { return consume(r) }, nil)
}
func (t *aptevaTx) aggregatePortable(c Collection, r Request, outSchema Collection) ([]Record, error) {
	return aggregateRecordsWithBudget(t.ctx, c, r, outSchema, t.backend.options.QueryWorkBytes, func(q Query, fields []string, consume func(Record, []byte) bool) error {
		q.diag = r.diag
		return t.scanFields(c, q, fields, consume, nil)
	})
}

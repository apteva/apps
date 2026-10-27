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
	if q.RequireIndex && !plan(c, q).IndexedLookup {
		return nil, fail("query_too_expensive", "query requires a selective index")
	}
	if len(t.pending) > 0 || plan(c, q).Sort {
		// Project before retaining a bounded top page; do not keep a second
		// native heap of complete documents alongside the adapter's results.
		return t.findPending(c, q, limit)
	}
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	opts := nativeQueryOptions(c, q, limit)
	workLimit := t.backend.options.QueryWorkBytes
	opts.MaxMemoryBytes = int64(workLimit)
	var stats native.QueryStats
	opts.Stats = &stats
	out := []Record{}
	retained := 0
	var readErr error
	e = h.Query(t.ctx, marshal(nativeQueryFilter(c, q)), opts, func(_, data []byte) bool {
		var r Record
		r, readErr = decodeNativeRecord(c, data)
		if readErr != nil {
			return false
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
func (t *aptevaTx) findPending(c Collection, q Query, limit int) ([]Record, error) {
	order := q
	if len(order.OrderBy) == 0 {
		for _, p := range c.PrimaryKey {
			order.OrderBy = append(order.OrderBy, Order{p, "asc"})
		}
	}
	top := &topRecords{collection: c, query: order, limit: limit, workLimit: t.backend.options.QueryWorkBytes}
	e := t.scanFields(c, q, nil, func(r Record, _ []byte) bool { return true }, func(r Record) error { return top.offer(r) })
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
func (t *aptevaTx) scanFields(c Collection, q Query, fields []string, consume func(Record, []byte) bool, offer func(Record) error) error {
	if q.RequireIndex && !plan(c, q).IndexedLookup {
		return fail("query_too_expensive", "query requires a selective index")
	}
	h, e := t.readCollection(c)
	if e != nil {
		return e
	}
	opts := nativeQueryOptions(c, Query{Where: q.Where}, 0)
	opts.MaxMemoryBytes = int64(t.backend.options.QueryWorkBytes)
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
			consumeErr = offer(r)
			return consumeErr == nil
		}
		stopped = !consume(r, data)
		return !stopped
	}
	e = h.Query(t.ctx, marshal(nativeFilter(c, q.Where)), opts, func(key, data []byte) bool {
		if _, changed := t.pending[nativeRowKey{c.Name, string(key)}]; changed {
			return true
		}
		return visit(data)
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
	np, e := h.ExplainQuery(t.ctx, marshal(nativeQueryFilter(c, q)), opts)
	if e != nil {
		return nil, nativeError(e)
	}
	p := plan(c, q)
	p.Index = np.Index
	p.Sort = np.SortInMemory
	p.IndexedLookup = p.IndexedLookup && np.Index != ""
	return map[string]any{"adapter": "aptevadb", "plan": p, "nativePlan": np, "candidateLimit": nil, "maxRetainedBytes": t.backend.options.QueryWorkBytes, "aggregateWorkers": t.backend.options.AggregateWorkers, "cacheBytes": t.backend.options.CacheBytes, "syncMode": t.backend.options.SyncMode, "synchronousWrites": true, "executionBound": "deadline"}, nil
}
func (t *aptevaTx) aggregate(c Collection, r Request, outSchema Collection) ([]Record, error) {
	if len(t.pending) == 0 && len(r.GroupBy) == 0 && len(r.Metrics) == 1 && r.Metrics[0].Op == "count" && r.Metrics[0].Field == "" {
		p := plan(c, r.Query)
		if r.RequireIndex && !p.IndexedLookup {
			return nil, fail("query_too_expensive", "query requires a selective index")
		}
		h, e := t.readCollection(c)
		if e != nil {
			return nil, e
		}
		opts := native.CountOptions{MaxMemoryBytes: int64(t.backend.options.QueryWorkBytes)}
		if p.IndexedLookup {
			opts.Hint = p.Index
		}
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

func (t *aptevaTx) aggregatePortable(c Collection, r Request, outSchema Collection) ([]Record, error) {
	return aggregateRecordsWithBudget(t.ctx, c, r, outSchema, t.backend.options.QueryWorkBytes, func(q Query, fields []string, consume func(Record, []byte) bool) error {
		return t.scanFields(c, q, fields, consume, nil)
	})
}

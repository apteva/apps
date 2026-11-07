package engine

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"time"

	native "github.com/apteva/db"
)

// Alpha.4 provides exact wide integer reduction and explicit non-null counts.
// The app still requires int64 sums and checked intermediate accumulation.
// Conservative per-group bounds prove the common case safe in any scan order;
// uncertain cancellation-heavy cases rescan the same snapshot with our checked
// reducer. Pending writes also retain that reducer.
type nativeSumGuard struct{ metric, count, low, high string }
type nativeAggregationPlan struct {
	group     map[string]any
	sumCounts map[string]string
	guards    []nativeSumGuard
	hidden    []string
	// ids names each groupBy entry inside _id. Joined names contain dots,
	// which native paths would read as nesting, so analytics use g0, g1...
	ids    []string
	prefix []any // stages before $group: filters, lookups
	view   Collection
	hours  map[string]bool // groupBy names that are hour buckets
}

func nativeAggregatePlan(c Collection, r Request) nativeAggregationPlan {
	a := r.analytics
	spec := nativeAggregationPlan{group: map[string]any{"_id": nil}, sumCounts: map[string]string{}, view: c, hours: map[string]bool{}}
	where := r.Where
	if a != nil {
		spec.view = a.view
		where = a.pushed
	}
	if where != nil {
		spec.prefix = append(spec.prefix, map[string]any{"$match": nativeFilter(c, where)})
	}
	buckets := map[string]bucketPlan{}
	if a != nil {
		// Joins match the target's primary-key field: many-to-one, and an
		// unmatched or null reference keeps the row with null joined fields.
		for _, l := range a.lookups {
			spec.prefix = append(spec.prefix,
				map[string]any{"$lookup": map[string]any{"from": nativeCollectionName(l.Collection), "localField": l.LocalField, "foreignField": l.target.PrimaryKey[0], "as": l.As}},
				map[string]any{"$unwind": map[string]any{"path": "$" + l.As, "preserveNullAndEmptyArrays": true}})
		}
		if a.residual != nil {
			spec.prefix = append(spec.prefix, map[string]any{"$match": nativeFilter(a.view, a.residual)})
		}
		for _, b := range a.buckets {
			buckets[b.Name] = b
		}
	}
	if len(r.GroupBy) > 0 {
		id := map[string]any{}
		for i, field := range r.GroupBy {
			alias := field
			if a != nil {
				alias = fmt.Sprint("g", i)
			}
			spec.ids = append(spec.ids, alias)
			if b, ok := buckets[field]; ok {
				id[alias] = map[string]any{"$dateTrunc": map[string]any{"date": "$" + b.Field, "unit": b.Unit, "timezone": b.Timezone, "startOfWeek": b.weekStart.String()}}
				spec.hours[field] = b.Unit == "hour"
			} else {
				id[alias] = "$" + field
			}
		}
		spec.group["_id"] = id
	}
	accumulator := func(op, field string, where *Filter) map[string]any {
		var acc map[string]any
		switch {
		case op == "count" && field == "":
			acc = map[string]any{"$count": map[string]any{}}
		case op == "count":
			acc = map[string]any{"$countNonNull": "$" + field}
		case op == "count_distinct":
			acc = map[string]any{"$countDistinct": "$" + field}
		default:
			acc = map[string]any{"$" + op: "$" + field}
		}
		if where != nil {
			acc["where"] = nativeFilter(spec.view, where)
		}
		return acc
	}
	for _, m := range r.Metrics {
		spec.group[m.Name] = accumulator(m.Op, m.Field, m.Where)
	}
	// Guards for a filtered sum must see the same rows as the sum.
	ensure := func(op, field, name string, where *Filter) string {
		f, _ := spec.view.field(field)
		for _, m := range r.Metrics {
			if m.Op == op && (m.Field == field || op == "count" && m.Field == "" && !f.Nullable) && equalJSON(m.Where, where) {
				return m.Name
			}
		}
		spec.group[name] = accumulator(op, field, where)
		spec.hidden = append(spec.hidden, name)
		return name
	}
	for _, m := range r.Metrics {
		if m.Op != "sum" {
			continue
		}
		count := ensure("count", m.Field, "_apteva_count_"+m.Name, m.Where)
		spec.sumCounts[m.Name] = count
		f, _ := spec.view.field(m.Field)
		if f.Type == "integer" {
			spec.guards = append(spec.guards, nativeSumGuard{m.Name, count, ensure("min", m.Field, "_apteva_min_"+m.Name, m.Where), ensure("max", m.Field, "_apteva_max_"+m.Name, m.Where)})
		}
	}
	return spec
}

// nativeAnalyticsSupported reports whether the native pipeline can evaluate
// the extensions. Buckets are native only as group keys.
func nativeAnalyticsSupported(r Request) bool {
	a := r.analytics
	if a == nil || len(a.buckets) == 0 {
		return true
	}
	derived := map[string]bool{}
	for _, b := range a.buckets {
		derived[b.Name] = true
	}
	stored := func(n string) bool { return !derived[n] }
	ok := onlyFields(a.residual, stored)
	for _, m := range r.Metrics {
		ok = ok && !derived[m.Field] && onlyFields(m.Where, stored)
	}
	return ok
}

func nativeSumBoundSafe(count, low, high int64) bool {
	if count <= 0 {
		return true
	}
	n := uint64(count)
	if high > 0 && n > uint64(math.MaxInt64)/uint64(high) {
		return false
	}
	if low < 0 && n > (uint64(1)<<63)/(uint64(-(low+1))+1) {
		return false
	}
	return true
}

func (t *aptevaTx) aggregateNative(c Collection, r Request, outSchema Collection) ([]Record, error) {
	spec := nativeAggregatePlan(c, r)
	// A leading $match lets the native planner pick an index range and shard
	// it, or shard the full scan, instead of one serial Query source.
	pipeline := nativeAggregatePipeline(r, spec)
	p, e := native.ParsePipeline(pipeline)
	if e != nil {
		return nil, nativeError(e)
	}
	workLimit := t.backend.options.QueryWorkBytes
	p.SetLimits(nativeLimits(r, workLimit, t.backend.options.AggregateMaxGroups))
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	if r.RequireIndex && !plan(c, Query{Where: nativeScanFilter(r)}).IndexedLookup {
		return nil, fail("query_too_expensive", "query requires a selective index")
	}
	values, e := h.RunParallel(t.ctx, p, t.backend.options.AggregateWorkers)
	if errors.Is(e, native.ErrLimit) {
		return t.aggregateStream(c, r, outSchema, spec)
	}
	if e != nil {
		return nil, nativeError(e)
	}
	if len(values) == 0 && len(r.GroupBy) == 0 {
		values = []map[string]any{{}}
		for _, m := range r.Metrics {
			if m.Op == "count" || m.Op == "count_distinct" {
				values[0][m.Name] = int64(0)
			} else {
				values[0][m.Name] = nil
			}
		}
	}
	out := make([]Record, 0, len(values))
	retained := 0
	for _, row := range values {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}
		record, err := normalizeNativeAggregateRow(c, r, outSchema, spec, row)
		if errors.Is(err, errNativeSumUnsafe) {
			return t.aggregatePortable(c, r, outSchema)
		}
		if err != nil {
			return nil, err
		}
		retained += retainedBytes(record)
		if retained > workLimit {
			return t.aggregateStream(c, r, outSchema, spec)
		}
		out = append(out, record)
	}
	sorted(outSchema, out, r.OrderBy)
	if len(out) > r.Limit+1 {
		out = out[:r.Limit+1]
	}
	return out, t.ctx.Err()
}

var errNativeSumUnsafe = errors.New("native sum needs checked scan-order evaluation")

// Validate even rows that will be discarded by top-K selection. All counters
// are read before formatting metrics, including counters reused by user aliases.
func normalizeNativeAggregateRow(c Collection, r Request, outSchema Collection, spec nativeAggregationPlan, row map[string]any) (Record, error) {
	for _, guard := range spec.guards {
		count, _ := row[guard.count].(int64)
		if count == 0 {
			continue
		}
		if _, ok := row[guard.metric].(int64); !ok {
			return nil, fail("resource_limit", "aggregate integer overflow")
		}
		low, lowOK := row[guard.low].(int64)
		high, highOK := row[guard.high].(int64)
		if !lowOK || !highOK || !nativeSumBoundSafe(count, low, high) {
			return nil, errNativeSumUnsafe
		}
	}
	id, _ := row["_id"].(map[string]any)
	delete(row, "_id")
	for i, name := range r.GroupBy {
		v := id[spec.ids[i]]
		f, _ := spec.view.field(name)
		if v != nil && f.Type == "integer" {
			v = strconv.FormatInt(v.(int64), 10)
		}
		if s, ok := v.(string); ok && spec.hours[name] {
			t, e := time.Parse(time.RFC3339Nano, s)
			if e != nil {
				return nil, fail("storage_error", "invalid native hour bucket %q", s)
			}
			v = t.UTC().Format("2006-01-02T15:04:05.000000Z")
		}
		row[name] = v
	}
	// Counts may be reused from user metrics. Read them before formatting
	// those metrics as decimal wire strings, independent of metric order.
	for metric, counter := range spec.sumCounts {
		if count, _ := row[counter].(int64); count == 0 {
			row[metric] = nil
		}
	}
	for _, m := range r.Metrics {
		v := row[m.Name]
		if m.Op == "count" || m.Op == "count_distinct" {
			v = strconv.FormatInt(v.(int64), 10)
		} else if v != nil {
			f, _ := outSchema.field(m.Name)
			if f.Type == "integer" {
				v = strconv.FormatInt(v.(int64), 10)
			}
			if f.Type == "number" {
				if n, ok := v.(int64); ok {
					v = float64(n)
				}
				if math.IsInf(number(v), 0) || math.IsNaN(number(v)) {
					return nil, fail("resource_limit", "aggregate numeric overflow")
				}
			}
		}
		row[m.Name] = v
	}
	for _, name := range spec.hidden {
		delete(row, name)
	}
	return Record(row), nil
}

// nativeScanFilter is the part of the filter on stored fields.
func nativeScanFilter(r Request) *Filter {
	if r.analytics != nil {
		return r.analytics.pushed
	}
	return r.Where
}

// nativeLimits bounds one pipeline. Alpha.11 caps a lookup's hash table by
// rows, not bytes, so a join's row cap scales with the work budget (16 MiB
// allows 32,768 target rows). Larger targets exceed it and are joined by
// primary-key reads in the portable accumulator.
func nativeLimits(r Request, workBytes, groups int) native.Limits {
	l := native.Limits{MaxBytes: int64(workBytes), MaxGroups: groups}
	if r.analytics != nil && len(r.analytics.lookups) > 0 {
		l.MaxRows = max(256, workBytes/512)
	}
	return l
}

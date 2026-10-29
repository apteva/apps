package engine

import (
	"errors"
	"math"
	"strconv"

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
}

func nativeAggregatePlan(c Collection, r Request) nativeAggregationPlan {
	spec := nativeAggregationPlan{group: map[string]any{"_id": nil}, sumCounts: map[string]string{}}
	if len(r.GroupBy) > 0 {
		id := map[string]any{}
		for _, field := range r.GroupBy {
			id[field] = "$" + field
		}
		spec.group["_id"] = id
	}
	for _, m := range r.Metrics {
		if m.Op == "count" {
			if m.Field == "" {
				spec.group[m.Name] = map[string]any{"$count": map[string]any{}}
			} else {
				spec.group[m.Name] = map[string]any{"$countNonNull": "$" + m.Field}
			}
		} else {
			spec.group[m.Name] = map[string]any{"$" + m.Op: "$" + m.Field}
		}
	}
	ensure := func(op, field, name string) string {
		f, _ := c.field(field)
		for _, m := range r.Metrics {
			if m.Op == op && (m.Field == field || op == "count" && m.Field == "" && !f.Nullable) {
				return m.Name
			}
		}
		if op == "count" {
			spec.group[name] = map[string]any{"$countNonNull": "$" + field}
		} else {
			spec.group[name] = map[string]any{"$" + op: "$" + field}
		}
		spec.hidden = append(spec.hidden, name)
		return name
	}
	for _, m := range r.Metrics {
		if m.Op != "sum" {
			continue
		}
		count := ensure("count", m.Field, "_apteva_count_"+m.Name)
		spec.sumCounts[m.Name] = count
		f, _ := c.field(m.Field)
		if f.Type == "integer" {
			spec.guards = append(spec.guards, nativeSumGuard{m.Name, count, ensure("min", m.Field, "_apteva_min_"+m.Name), ensure("max", m.Field, "_apteva_max_"+m.Name)})
		}
	}
	return spec
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

	pipeline := nativeAggregatePipeline(r, spec)
	if r.Where != nil {
		// A leading $match lets the native planner pick an index range and
		// shard it, or shard the full scan, instead of one serial Query source.
		match := `[{"$match":` + string(marshal(nativeFilter(c, r.Where))) + `},`
		pipeline = append([]byte(match), pipeline[1:]...)
	}
	p, e := native.ParsePipeline(pipeline)
	if e != nil {
		return nil, nativeError(e)
	}
	workLimit := t.backend.options.QueryWorkBytes
	p.SetLimits(native.Limits{MaxBytes: int64(workLimit), MaxGroups: t.backend.options.AggregateMaxGroups})
	h, e := t.readCollection(c)
	if e != nil {
		return nil, e
	}
	if r.RequireIndex && !plan(c, r.Query).IndexedLookup {
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
			if m.Op == "count" {
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
	for _, name := range r.GroupBy {
		v := id[name]
		f, _ := c.field(name)
		if v != nil && f.Type == "integer" {
			v = strconv.FormatInt(v.(int64), 10)
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
		if m.Op == "count" {
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

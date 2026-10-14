package engine

import (
	"context"
	"math"
	"strconv"
)

// aggregateRecords is the portable bounded accumulator shared by embedded
// adapters. Each scan owns a consistent snapshot and supplies typed records.
func aggregateRecords(ctx context.Context, c Collection, r Request, outSchema Collection, scan func(Query, []string, func(Record, []byte) bool) error) ([]Record, error) {
	return aggregateRecordsWithBudget(ctx, c, r, outSchema, MaxQueryWorkBytes, scan)
}

func aggregateRecordsWithBudget(ctx context.Context, c Collection, r Request, outSchema Collection, workLimit int, scan func(Query, []string, func(Record, []byte) bool) error) ([]Record, error) {
	// Keep typed accumulators while scanning; format portable decimal strings
	// once per output group, instead of allocating them for every input row.
	type metricState struct {
		count   int64
		sum     float64
		integer int64
		value   any
	}
	type groupState struct {
		row     Record
		metrics []metricState
	}
	groups := map[any]*groupState{}
	metricFields := make([]Field, len(r.Metrics))
	for i, m := range r.Metrics {
		if m.Field != "" {
			metricFields[i], _ = c.field(m.Field)
		}
	}
	groupFields := make([]Field, len(r.GroupBy))
	for i, g := range r.GroupBy {
		groupFields[i], _ = c.field(g)
	}
	workBytes := 0
	init := func(key any, row Record) error {
		out := Record{}
		for _, g := range r.GroupBy {
			out[g] = row[g]
		}
		for _, m := range r.Metrics {
			out[m.Name] = nil
		}
		// Include maps, keys, typed accumulator storage and final scalar growth.
		n := 256 + retainedBytes(key) + retainedBytes(out) + len(r.Metrics)*96
		if workBytes+n > workLimit {
			return fail("resource_limit", "aggregate group state exceeds %d MiB", workLimit>>20)
		}
		workBytes += n
		groups[key] = &groupState{row: out, metrics: make([]metricState, len(r.Metrics))}
		return nil
	}
	if len(r.GroupBy) == 0 {
		if e := init(nil, nil); e != nil {
			return nil, e
		}
	}
	var aggErr error
	input := r.Query
	input.OrderBy = nil
	fields := append([]string{}, r.GroupBy...)
	for _, m := range r.Metrics {
		if m.Field != "" {
			fields = append(fields, m.Field)
		}
	}
	vals := make([]any, len(r.GroupBy))
	e := scan(input, fields, func(row Record, _ []byte) bool {
		var key any
		if len(r.GroupBy) > 0 {
			for i, g := range r.GroupBy {
				vals[i] = row[g]
				if vals[i] != nil && groupFields[i].Type == "number" {
					vals[i] = number(vals[i])
				}
			}
			// Scalar keys are already comparable and unambiguous within a query.
			// Only compound groups need a serialized tuple.
			if len(vals) == 1 {
				key = vals[0]
			} else {
				key = string(marshal(vals))
			}
		}
		group := groups[key]
		if group == nil {
			if aggErr = init(key, row); aggErr != nil {
				return false
			}
			group = groups[key]
		}
		for i, m := range r.Metrics {
			v := row[m.Field]
			state := &group.metrics[i]
			if v == nil && !(m.Op == "count" && m.Field == "") {
				continue
			}
			state.count++
			if m.Op == "count" {
				continue
			}
			f := metricFields[i]
			switch m.Op {
			case "min", "max":
				if state.value == nil || (m.Op == "min" && compare(f, v, state.value) < 0) || (m.Op == "max" && compare(f, v, state.value) > 0) {
					workBytes += retainedBytes(v) - retainedBytes(state.value)
					if workBytes > workLimit {
						aggErr = fail("resource_limit", "aggregate group state exceeds %d MiB", workLimit>>20)
						return false
					}
					state.value = v
				}
			case "sum", "avg":
				if f.Type == "integer" && m.Op == "sum" {
					b, _ := strconv.ParseInt(v.(string), 10, 64)
					a := state.integer
					if (b > 0 && a > math.MaxInt64-b) || (b < 0 && a < math.MinInt64-b) {
						aggErr = fail("resource_limit", "aggregate integer overflow")
						return false
					}
					state.integer = a + b
				} else {
					b := number(v)
					if f.Type == "integer" {
						b, _ = strconv.ParseFloat(v.(string), 64)
					}
					sum := state.sum + b
					if math.IsInf(sum, 0) || math.IsNaN(sum) {
						aggErr = fail("resource_limit", "aggregate numeric overflow")
						return false
					}
					state.sum = sum
				}
			}
		}
		return true
	})
	if e != nil {
		return nil, e
	}
	if aggErr != nil {
		return nil, aggErr
	}
	out := []Record{}
	for _, group := range groups {
		if e := ctx.Err(); e != nil {
			return nil, e
		}
		for i, m := range r.Metrics {
			state := group.metrics[i]
			var v any
			if m.Op == "count" {
				v = strconv.FormatInt(state.count, 10)
			} else if state.count > 0 {
				switch m.Op {
				case "sum":
					if metricFields[i].Type == "integer" {
						v = strconv.FormatInt(state.integer, 10)
					} else {
						v = state.sum
					}
				case "avg":
					v = state.sum / float64(state.count)
				case "min", "max":
					v = state.value
				}
			}
			group.row[m.Name] = v
		}
		out = append(out, group.row)
	}
	sorted(outSchema, out, r.OrderBy)
	if len(out) > r.Limit+1 {
		out = out[:r.Limit+1]
	}
	return out, ctx.Err()

}

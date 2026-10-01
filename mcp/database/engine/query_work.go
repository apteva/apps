package engine

import (
	"container/heap"
	"encoding/json"
	"maps"
	"time"
)

// OperationTimeout keeps interactive writes bounded while allowing broad read
// queries to stream large collections. A caller's earlier deadline still wins.
func OperationTimeout(op string, timeoutMS int) (time.Duration, error) {
	query := op == "find" || op == "count" || op == "aggregate"
	if timeoutMS != 0 {
		if !query || timeoutMS < 1 || timeoutMS > 60000 {
			return 0, Invalid("timeoutMs is supported for find/count/aggregate and must be 1–60000")
		}
		return time.Duration(timeoutMS) * time.Millisecond, nil
	}
	if query {
		return 30 * time.Second, nil
	}
	return 2 * time.Second, nil
}

// A conservative accounting estimate for retained decoded values, not bytes
// cumulatively visited. Include map/interface overhead and nested JSON values.
func retainedBytes(v any) int {
	switch x := v.(type) {
	case Record:
		n := 64
		for k, v := range x {
			n += 96 + len(k) + retainedBytes(v)
		}
		return n
	case map[string]any:
		return retainedBytes(Record(x))
	case []any:
		n := 24 + 16*len(x)
		for _, v := range x {
			n += retainedBytes(v)
		}
		return n
	case string:
		return 16 + len(x)
	case json.Number:
		return 16 + len(x)
	default:
		return 16
	}
}

type retainedRow struct {
	row  Record
	size int
}

// The worst retained row is the heap root, so later better rows replace it.
type topRecords struct {
	collection   Collection
	query        Query
	limit, bytes int
	rows         []retainedRow
}

func (h topRecords) Len() int { return len(h.rows) }
func (h topRecords) Less(i, j int) bool {
	return less(h.collection, h.rows[j].row, h.rows[i].row, h.query.OrderBy)
}
func (h topRecords) Swap(i, j int) { h.rows[i], h.rows[j] = h.rows[j], h.rows[i] }
func (h *topRecords) Push(x any)   { h.rows = append(h.rows, x.(retainedRow)) }
func (h *topRecords) Pop() any     { i := len(h.rows) - 1; x := h.rows[i]; h.rows = h.rows[:i]; return x }

func retainSelected(r Record, q Query) Record {
	if len(q.Select) == 0 {
		return maps.Clone(r)
	}
	out := Record{}
	for _, f := range q.Select {
		out[f] = r[f]
	}
	for _, o := range q.OrderBy {
		out[o.Field] = r[o.Field]
	}
	return out
}

func (h *topRecords) accepts(r Record) bool {
	return len(h.rows) < h.limit || less(h.collection, r, h.rows[0].row, h.query.OrderBy)
}

func (h *topRecords) offer(r Record) error {
	if !h.accepts(r) {
		return nil
	}
	r = retainSelected(r, h.query)
	size := retainedBytes(r)
	next := h.bytes + size
	if len(h.rows) == h.limit {
		next -= h.rows[0].size
	}
	if next > MaxQueryWorkBytes {
		return fail("resource_limit", "retained query results exceed 16 MiB")
	}
	h.bytes = next
	if len(h.rows) < h.limit {
		heap.Push(h, retainedRow{r, size})
	} else {
		h.rows[0] = retainedRow{r, size}
		heap.Fix(h, 0)
	}
	return nil
}

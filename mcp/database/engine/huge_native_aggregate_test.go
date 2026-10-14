//go:build aggregatebench

package engine

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	native "github.com/apteva/db"
	"github.com/cockroachdb/pebble/v2"
)

// This reference bypasses Manager.Execute, scoped routing and request/schema
// validation inside the timer. It keeps identical stored data, snapshots,
// result normalization, sorting, limits and JSON response encoding. Pebble has
// no aggregation API: its reference is raw iterators + the shared checked Go
// reducer/codec, not a claim of a built-in Pebble query engine.
func hugeNativeAggregate(t *testing.T, m *Manager, r Request) func(context.Context) (any, error) {
	t.Helper()
	d, e := m.resolve(context.Background(), "p", r.Database, "", false)
	checkErr(t, e)
	var c Collection
	checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error { var e error; c, e = tx.collection(r.Collection); return e }))
	out, e := validateAggregate(c, &r)
	checkErr(t, e)
	finish := func(rows []Record, e error) (any, error) {
		if e != nil {
			return nil, e
		}
		if len(marshal(rows)) > MaxBytes {
			return nil, fail("resource_limit", "aggregate response exceeds 4 MiB")
		}
		more := len(rows) > r.Limit
		if more {
			rows = rows[:r.Limit]
		}
		return map[string]any{"rows": rows, "truncated": more}, nil
	}
	switch b := d.backend.(type) {
	case *sqliteBackend:
		cols := append([]string{}, r.GroupBy...)
		for i := range cols {
			cols[i] = quote(cols[i])
		}
		for _, metric := range r.Metrics {
			field := "*"
			if metric.Field != "" {
				field = quote(metric.Field)
			}
			cols = append(cols, strings.ToUpper(metric.Op)+"("+field+") AS "+quote(metric.Name))
		}
		where, args := compileFilter(c, r.Where)
		query := "SELECT " + strings.Join(cols, ",") + " FROM " + table(c) + " WHERE " + where
		if len(r.GroupBy) > 0 {
			groups := append([]string{}, r.GroupBy...)
			for i := range groups {
				groups[i] = quote(groups[i])
			}
			query += " GROUP BY " + strings.Join(groups, ",")
		}
		query += orderSQL(r.OrderBy) + " LIMIT ?"
		args = append(args, r.Limit+1)
		statement, e := b.readDB.Prepare(query)
		checkErr(t, e)
		t.Cleanup(func() { checkErr(t, statement.Close()) })
		return func(ctx context.Context) (any, error) {
			rows, e := statement.QueryContext(ctx, args...)
			if e != nil {
				return nil, e
			}
			defer rows.Close()
			result := []Record{}
			retained := 0
			for rows.Next() {
				row, _, e := scanRecord(rows, out.Fields)
				if e != nil {
					return nil, e
				}
				for _, v := range row {
					if n, ok := v.(float64); ok && (math.IsNaN(n) || math.IsInf(n, 0)) {
						return nil, fail("resource_limit", "aggregate numeric overflow")
					}
				}
				retained += retainedBytes(row)
				if retained > MaxQueryWorkBytes {
					return nil, fail("resource_limit", "aggregate result memory")
				}
				result = append(result, row)
			}
			return finish(result, rows.Err())
		}
	case *aptevaBackend:
		group := map[string]any{"_id": nil}
		if len(r.GroupBy) > 0 {
			id := map[string]any{}
			for _, field := range r.GroupBy {
				id[field] = "$" + field
			}
			group["_id"] = id
		}
		for _, metric := range r.Metrics {
			if metric.Op == "count" {
				if metric.Field == "" {
					group[metric.Name] = map[string]any{"$count": map[string]any{}}
				} else {
					group[metric.Name] = map[string]any{"$countNonNull": "$" + metric.Field}
				}
			} else {
				group[metric.Name] = map[string]any{"$" + metric.Op: "$" + metric.Field}
			}
		}
		p, e := native.ParsePipeline(marshal([]any{map[string]any{"$group": group}}))
		checkErr(t, e)
		p.SetLimits(native.Limits{MaxBytes: int64(b.options.QueryWorkBytes)})
		filter := marshal(nativeFilter(c, r.Where))
		opts := nativeQueryOptions(c, Query{Where: r.Where}, 0)
		opts.MaxMemoryBytes = int64(b.options.QueryWorkBytes)
		return func(ctx context.Context) (any, error) {
			snapshot, e := b.db.Snapshot()
			if e != nil {
				return nil, nativeError(e)
			}
			defer snapshot.Close()
			h, e := snapshot.Collection(nativeCollectionName(c.Name))
			if e != nil {
				return nil, nativeError(e)
			}
			var values []map[string]any
			if r.Where == nil {
				values, e = h.RunParallel(ctx, p, b.options.AggregateWorkers)
			} else {
				values, e = p.Run(ctx, func(yield func([]byte, []byte) bool) error { return h.Query(ctx, filter, opts, yield) })
			}
			if e != nil {
				return nil, nativeError(e)
			}
			result := make([]Record, 0, len(values))
			retained := 0
			for _, v := range values {
				id, _ := v["_id"].(map[string]any)
				row := Record{}
				for _, f := range out.Fields {
					val := v[f.Name]
					if containsName(r.GroupBy, f.Name) {
						val = id[f.Name]
					}
					if val != nil && f.Type == "integer" {
						n, ok := val.(int64)
						if !ok {
							return nil, fail("resource_limit", "aggregate integer overflow")
						}
						val = strconv.FormatInt(n, 10)
					}
					if val != nil && f.Type == "number" {
						if n, ok := val.(int64); ok {
							val = float64(n)
						}
						if math.IsNaN(number(val)) || math.IsInf(number(val), 0) {
							return nil, fail("resource_limit", "aggregate numeric overflow")
						}
					}
					row[f.Name] = val
				}
				retained += retainedBytes(row)
				if retained > b.options.QueryWorkBytes {
					return nil, fail("resource_limit", "aggregate result memory")
				}
				result = append(result, row)
			}
			sorted(out, result, r.OrderBy)
			if len(result) > r.Limit+1 {
				result = result[:r.Limit+1]
			}
			return finish(result, ctx.Err())
		}
	case *pebbleBackend:
		// The harness supports a broad scan or the exact indexed tenant filter.
		recordPrefix := []byte("r/" + c.Name + "/")
		prefix := recordPrefix
		indexed := false
		if r.Where != nil {
			if r.Where.Field != "tenant" || r.Where.Op != "eq" {
				t.Fatal("unsupported direct Pebble filter")
			}
			var ix Index
			for _, candidate := range c.Indexes {
				if candidate.Name == "by_tenant" {
					ix = candidate
				}
			}
			if ix.Name == "" {
				t.Fatal("missing native tenant index")
			}
			tuple, _, e := indexTuple(c, ix, Record{"tenant": r.Where.Value})
			checkErr(t, e)
			prefix = append(indexPrefix(c, ix, false), tuple...)
			indexed = true
		}
		return func(ctx context.Context) (any, error) {
			snapshot := b.db.NewSnapshot()
			defer snapshot.Close()
			rows, e := aggregateRecords(ctx, c, r, out, func(_ Query, fields []string, consume func(Record, []byte) bool) error {
				it, e := snapshot.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
				if e != nil {
					return e
				}
				defer it.Close()
				decoder := newScanDecoder(c, fields, nil)
				for it.First(); it.Valid(); it.Next() {
					if e := ctx.Err(); e != nil {
						return e
					}
					data := it.Value()

					if indexed {
						data, closer, e := snapshot.Get(append(append([]byte(nil), recordPrefix...), it.Value()...))
						if e != nil {
							return e
						}
						row, e := decoder.decode(data)
						if e != nil {
							closer.Close()
							return e
						}
						keep := consume(row, data)
						if e := closer.Close(); e != nil {
							return e
						}
						if !keep {
							break
						}
						continue
					}

					row, e := decoder.decode(data)
					if e != nil {
						return e
					}
					if !consume(row, data) {
						break
					}
				}
				return it.Error()
			})
			return finish(rows, e)
		}
	default:
		panic(fmt.Sprintf("unknown native backend %T", d.backend))
	}
}

func containsName(names []string, name string) bool {
	for _, n := range names {
		if n == name {
			return true
		}
	}
	return false
}

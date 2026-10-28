package engine

import (
	"errors"

	native "github.com/apteva/db"
)

// Retry only a native working-budget failure, using the transaction's existing
// snapshot. Keep the fast parallel executor for ordinary aggregates that fit.
// Disk leases default to the native database's private aggregate-tmp directory;
// native Open cleans abandoned leases, and each execution cleans its own files.
func (t *aptevaTx) aggregateStream(c Collection, r Request, out Collection, spec nativeAggregationPlan) ([]Record, error) {
	h, err := t.readCollection(c)
	if err != nil {
		return nil, err
	}
	stages := []any{}
	if r.Where != nil {
		stages = append(stages, map[string]any{"$match": nativeFilter(c, r.Where)})
	}
	stages = append(stages, map[string]any{"$group": spec.group})
	p, err := native.ParsePipeline(marshal(stages))
	if err != nil {
		return nil, nativeError(err)
	}
	// Account caller-retained results separately from the native stream. Their
	// fixed reservations together stay within the configured query work budget.
	work := t.backend.options.QueryWorkBytes
	resultBudget := work / 4
	p.SetLimits(native.Limits{MaxBytes: int64(work - resultBudget), MaxGroups: t.backend.options.AggregateMaxGroups})
	top := &topRecords{collection: out, query: r.Query, limit: r.Limit + 1, workLimit: resultBudget}
	var rowErr error
	err = h.RunEach(t.ctx, p, native.AggregateOptions{
		MaxMemoryBytes: int64(work - resultBudget),
		AllowDiskUse:   t.backend.options.AggregateDiskBytes > 0,
		MaxDiskBytes:   t.backend.options.AggregateDiskBytes,
	}, func(value map[string]any) bool {
		if rowErr = t.ctx.Err(); rowErr != nil {
			return false
		}
		var row Record
		row, rowErr = normalizeNativeAggregateRow(c, r, out, spec, value)
		if rowErr == nil {
			rowErr = top.offer(row)
		}
		return rowErr == nil
	})
	// Cleanup errors and native failures must not be hidden by a callback stop.
	if err != nil {
		return nil, nativeError(err)
	}
	if errors.Is(rowErr, errNativeSumUnsafe) {
		return t.aggregatePortable(c, r, out)
	}
	if rowErr != nil {
		return nil, rowErr
	}
	rows := make([]Record, 0, len(top.rows))
	for _, row := range top.rows {
		rows = append(rows, row.row)
	}
	sorted(out, rows, r.OrderBy)
	return rows, t.ctx.Err()
}

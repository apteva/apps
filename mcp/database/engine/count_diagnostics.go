package engine

import (
	"time"

	native "github.com/apteva/db"
)

// countDiagnostics records what one count did. Its methods accept a nil receiver
// so the ordinary path carries no cost beyond a nil check.
type countDiagnostics struct {
	started, queuedAt, snapshotAt, runStart time.Time
	running                                 time.Duration
	route                                   string
	stats                                   *native.QueryStats
	selectionProbes                         int64
	selectionEntries                        int64
	pageHeapBytes, scanBytes, pagePeakBytes int
	pageDecodedBytes                        int
}

func (d *countDiagnostics) queued() {
	if d != nil {
		d.queuedAt = time.Now()
	}
}

func (d *countDiagnostics) snapshotted() {
	if d != nil && d.snapshotAt.IsZero() {
		d.snapshotAt = time.Now()
	}
}

func (d *countDiagnostics) begin() {
	if d != nil {
		d.runStart = time.Now()
	}
}

func (d *countDiagnostics) end() {
	if d != nil {
		d.running = time.Since(d.runStart)
	}
}

func (d *countDiagnostics) strategy(route string) {
	if d != nil {
		d.route = route
	}
}

// nativeStats returns where the engine should record the operation that runs.
func (d *countDiagnostics) nativeStats(route string) *native.QueryStats {
	if d == nil {
		return nil
	}
	d.route = route
	d.stats = &native.QueryStats{}
	return d.stats
}

func milliseconds(v time.Duration) float64 { return float64(v.Microseconds()) / 1000 }

func (d *countDiagnostics) report(tx transaction, c Collection, q Query, exists *existsState) map[string]any {
	out := map[string]any{"executed": true, "executionMs": milliseconds(d.running)}
	// A count inside a read batch shares the batch's queue and snapshot.
	if !d.started.IsZero() {
		out["queueMs"] = milliseconds(d.queuedAt.Sub(d.started))
		out["snapshotMs"] = milliseconds(d.snapshotAt.Sub(d.queuedAt))
	}
	if d.selectionProbes > 0 {
		out["indexSelection"] = map[string]any{"probes": d.selectionProbes, "indexEntriesExamined": d.selectionEntries}
	}
	if d.route != "" {
		out["strategy"] = d.route
	}
	if s := d.stats; s != nil {
		out["planSource"] = "execution"
		out["plan"] = map[string]any{"access": s.Plan.Access, "index": s.Plan.Index, "indexOnly": s.Plan.IndexOnly, "equalityPrefix": s.Plan.EqualityPrefix, "ranges": s.Plan.Ranges}
		out["indexOnly"] = s.Plan.IndexOnly
		out["documentFetches"] = s.DocumentFetches
		out["indexEntriesExamined"] = s.IndexEntriesExamined
		out["examined"] = s.Examined
		out["returned"] = s.Returned
		out["peakMemoryBytes"] = s.PeakMemoryBytes
	} else if plan, e := tx.explain(c, q); e == nil {
		// Without engine statistics the plan is what the adapter would choose.
		out["planSource"] = "explain"
		out["plan"] = plan
	}
	if exists != nil {
		out["exists"] = map[string]any{"probes": exists.probes, "keySetsBuilt": exists.setsBuilt, "relatedRowsRead": exists.rowsRead, "relatedDocumentsExamined": exists.examined, "keySetBytes": exists.setBytes}
	}
	return out
}

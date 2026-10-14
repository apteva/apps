package engine

import "runtime"

// ManagerOptions configures each owned database. Apteva cache is a per-database
// target and query memory is a per-operation accounting budget, not an RSS cap.
type ManagerOptions struct {
	SQLiteDurability string
	Apteva           *AptevaOptions
}

type AptevaOptions struct {
	Realtime         *RealtimeOptions // nil preserves native persisted capture policy
	SyncMode         string           // full (default), barrier or os; writes remain synchronous
	CacheBytes       int64            // zero explicitly selects the engine's uncapped cache
	AggregateWorkers int              // zero selects min(4, GOMAXPROCS)
	QueryWorkBytes   int              // zero selects the shared 16 MiB default
}

func DefaultAptevaOptions() AptevaOptions {
	// Positive cache targets enable a different native WAL/memtable path and
	// are not RSS limits. Keep the existing uncapped profile by default.
	return AptevaOptions{SyncMode: "full", CacheBytes: 0, AggregateWorkers: min(4, runtime.GOMAXPROCS(0)), QueryWorkBytes: MaxQueryWorkBytes}
}

func (o AptevaOptions) normalized() (AptevaOptions, error) {
	if o.SyncMode == "" {
		o.SyncMode = "full"
	}
	if o.SyncMode != "full" && o.SyncMode != "barrier" && o.SyncMode != "os" {
		return o, Invalid("Apteva sync mode must be full, barrier or os")
	}
	if o.CacheBytes < 0 || o.CacheBytes > 1<<30 || (o.CacheBytes > 0 && o.CacheBytes < 1<<20) {
		return o, Invalid("Apteva cache must be 0 (uncapped) or 1–1024 MiB")
	}
	if o.AggregateWorkers == 0 {
		o.AggregateWorkers = DefaultAptevaOptions().AggregateWorkers
	}
	if o.AggregateWorkers < 1 || o.AggregateWorkers > 64 {
		return o, Invalid("Apteva aggregate workers must be 1–64")
	}
	if o.QueryWorkBytes == 0 {
		o.QueryWorkBytes = MaxQueryWorkBytes
	}
	if o.QueryWorkBytes < 1<<20 || o.QueryWorkBytes > 256<<20 {
		return o, Invalid("Apteva query memory must be 1–256 MiB")
	}
	return o, nil
}

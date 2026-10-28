package engine

import "runtime"

// ManagerOptions configures each owned database. Apteva cache is a per-database
// target and query memory is a per-operation accounting budget, not an RSS cap.
type ManagerOptions struct {
	SQLiteDurability           string
	Apteva                     *AptevaOptions
	AptevaCacheBudgetBytes     int64 // zero selects max(256 MiB, one configured cache)
	MaxConcurrentAptevaQueries int   // zero selects two find/count/aggregate operations
}

type AptevaOptions struct {
	MappedCacheBytes   int64            // zero disables automatic cold mapped-page reclamation
	ManagedMemoryBytes int64            // zero disables soft owned-buffer admission
	AggregateDiskBytes int64            // zero disables disk spill; positive bytes bound temporary disk per query
	AggregateMaxGroups int              // zero selects the native default output-group guard
	Realtime           *RealtimeOptions // nil preserves native persisted capture policy
	SyncMode           string           // full (default), barrier or os; writes remain synchronous
	CacheBytes         int64            // zero explicitly selects the engine's uncapped cache
	MemtableBytes      int64            // zero disables write buffering; positive values explicitly opt in
	AggregateWorkers   int              // zero selects min(4, GOMAXPROCS)
	QueryWorkBytes     int              // zero selects the shared 16 MiB default
}

func DefaultAptevaOptions() AptevaOptions {
	// Bound the logical cache without silently enabling a write buffer. Neither
	// target is a process RSS cap; synchronous durability is unchanged.
	return AptevaOptions{SyncMode: "full", CacheBytes: 64 << 20, MappedCacheBytes: 64 << 20, AggregateDiskBytes: 1 << 30, AggregateMaxGroups: 1 << 20, AggregateWorkers: min(4, runtime.GOMAXPROCS(0)), QueryWorkBytes: MaxQueryWorkBytes}
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
	if o.MemtableBytes < 0 || o.MemtableBytes > 256<<20 || o.MemtableBytes > 0 && o.MemtableBytes < 1<<20 {
		return o, Invalid("Apteva memtable must be 0 (disabled) or 1–256 MiB")
	}
	for _, target := range []int64{o.MappedCacheBytes, o.ManagedMemoryBytes, o.AggregateDiskBytes} {
		if target < 0 || target > 8<<30 || target > 0 && target < 1<<20 {
			return o, Invalid("Apteva memory and scratch targets must be 0 (disabled) or 1–8192 MiB")
		}
	}
	if o.AggregateMaxGroups == 0 {
		o.AggregateMaxGroups = 1 << 20
	}
	if o.AggregateMaxGroups < 1 || o.AggregateMaxGroups > 1<<24 {
		return o, Invalid("Apteva aggregate group guard must be 1–16777216")
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

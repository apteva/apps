package main

import (
	"strconv"
	"strings"

	"github.com/apteva/apps/mcp/database/engine"
)

func managerOptions(config map[string]string, getenv func(string) string) (engine.ManagerOptions, error) {
	value := func(name, env string) string {
		if v := strings.TrimSpace(config[name]); v != "" {
			return v
		}
		return strings.TrimSpace(getenv(env))
	}
	options := engine.ManagerOptions{SQLiteDurability: value("durability_profile", "DATABASE_DURABILITY_PROFILE")}
	if options.SQLiteDurability == "" {
		options.SQLiteDurability = "durable"
	}
	native := engine.DefaultAptevaOptions()
	if mode := value("aptevadb_sync_mode", "DATABASE_APTEVADB_SYNC_MODE"); mode != "" {
		if mode != "full" && mode != "barrier" && mode != "os" {
			return options, engine.Invalid("aptevadb_sync_mode must be full, barrier or os")
		}
		native.SyncMode = mode
	}
	for _, item := range []struct {
		name, env string
		min, max  int64
		set       func(int64)
	}{
		{"aptevadb_cache_mib", "DATABASE_APTEVADB_CACHE_MIB", 0, 1024, func(n int64) { native.CacheBytes = n << 20 }},
		{"aptevadb_aggregate_workers", "DATABASE_APTEVADB_AGGREGATE_WORKERS", 1, 64, func(n int64) { native.AggregateWorkers = int(n) }},
		{"aptevadb_query_memory_mib", "DATABASE_APTEVADB_QUERY_MEMORY_MIB", 1, 256, func(n int64) { native.QueryWorkBytes = int(n) << 20 }},
	} {
		v := value(item.name, item.env)
		if v == "" {
			continue
		}
		n, e := strconv.ParseInt(v, 10, 32)
		if e != nil || n < item.min || n > item.max {
			return options, engine.Invalid("%s must be %d–%d", item.name, item.min, item.max)
		}
		item.set(n)
	}
	options.Apteva = &native
	return options, nil
}

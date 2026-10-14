package main

import "testing"

func TestManagerConfigPrecedenceAndValidation(t *testing.T) {
	defaults, err := managerOptions(nil, func(string) string { return "" })
	if err != nil || defaults.Apteva.SyncMode != "full" || defaults.Apteva.CacheBytes != 0 || defaults.Apteva.AggregateWorkers < 1 || defaults.Apteva.AggregateWorkers > 4 || defaults.Apteva.QueryWorkBytes != 16<<20 {
		t.Fatal(defaults, err)
	}
	getenv := func(name string) string {
		return map[string]string{"DATABASE_APTEVADB_SYNC_MODE": "os", "DATABASE_APTEVADB_CACHE_MIB": "128", "DATABASE_APTEVADB_AGGREGATE_WORKERS": "2", "DATABASE_APTEVADB_QUERY_MEMORY_MIB": "32"}[name]
	}
	o, e := managerOptions(map[string]string{"aptevadb_cache_mib": "0", "aptevadb_aggregate_workers": "4"}, getenv)
	if e != nil || o.Apteva.SyncMode != "os" || o.Apteva.CacheBytes != 0 || o.Apteva.AggregateWorkers != 4 || o.Apteva.QueryWorkBytes != 32<<20 {
		t.Fatal(o, e)
	}
	for _, config := range []map[string]string{{"aptevadb_sync_mode": "async"}, {"aptevadb_sync_mode": "typo"}, {"aptevadb_cache_mib": "-1"}, {"aptevadb_cache_mib": "1025"}, {"aptevadb_aggregate_workers": "0"}, {"aptevadb_aggregate_workers": "65"}, {"aptevadb_query_memory_mib": "257"}, {"aptevadb_query_memory_mib": "NaN"}} {
		if _, e := managerOptions(config, func(string) string { return "" }); e == nil {
			t.Fatal("invalid configuration accepted", config)
		}
	}
	for _, mode := range []string{"full", "barrier", "os"} {
		o, e := managerOptions(map[string]string{"aptevadb_sync_mode": mode}, getenv)
		if e != nil || o.Apteva.SyncMode != mode {
			t.Fatal("app sync mode must override environment", o, e)
		}
	}
	if _, e := managerOptions(nil, func(string) string { return "invalid" }); e == nil {
		t.Fatal("invalid environment sync mode accepted")
	}
}

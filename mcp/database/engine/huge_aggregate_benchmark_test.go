//go:build aggregatebench

package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	native "github.com/apteva/db"
)

// One disposable database per process keeps disk/heap peaks independent. Five
// repeated reads share the loaded fixture; this is a warm-cache scale check.
func TestHugeAggregateBenchmark(t *testing.T) {
	adapter := os.Getenv("DB_HUGE_AGG_ADAPTER")
	if adapter != "sqlite" && adapter != "pebble" && adapter != "aptevadb" {
		t.Fatal("DB_HUGE_AGG_ADAPTER required")
	}
	n, e := strconv.Atoi(os.Getenv("DB_HUGE_AGG_ROWS"))
	checkErr(t, e)
	if n < 100000 || n%1000 != 0 {
		t.Fatal("rows must be a multiple of 1000 >= 100000")
	}
	path := os.Getenv("DB_HUGE_AGG_OUTPUT")
	if path == "" {
		t.Fatal("DB_HUGE_AGG_OUTPUT required")
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("use a fresh output file")
	}
	paired := os.Getenv("DB_HUGE_AGG_NATIVE") == "1"
	var results []map[string]any
	var loadSeconds float64
	var databaseBytes int64
	var memoryDiagnostics map[string]any
	options := DefaultAptevaOptions()
	if os.Getenv("DB_HUGE_AGG_CAPTURE") == "1" {
		options.Realtime = &RealtimeOptions{Enabled: true, RetentionBytes: 128 << 20, RetentionSeconds: 3600}
	}
	if mode := os.Getenv("DB_HUGE_AGG_SYNC_MODE"); mode != "" {
		options.SyncMode = mode
	}
	for _, setting := range []struct {
		name string
		set  func(int)
	}{
		{"DB_HUGE_AGG_MAPPED_MIB", func(v int) { options.MappedCacheBytes = int64(v) << 20 }},
		{"DB_HUGE_AGG_DISK_MIB", func(v int) { options.AggregateDiskBytes = int64(v) << 20 }},
		{"DB_HUGE_AGG_MEMTABLE_MIB", func(v int) { options.MemtableBytes = int64(v) << 20 }},
		{"DB_HUGE_AGG_CACHE_MIB", func(v int) { options.CacheBytes = int64(v) << 20 }},
		{"DB_HUGE_AGG_WORKERS", func(v int) { options.AggregateWorkers = v }},
		{"DB_HUGE_AGG_MEMORY_MIB", func(v int) { options.QueryWorkBytes = v << 20 }},
	} {
		if raw := os.Getenv(setting.name); raw != "" {
			v, err := strconv.Atoi(raw)
			checkErr(t, err)
			setting.set(v)
		}
	}
	defer func() {
		b, e := json.MarshalIndent(map[string]any{"adapter": adapter, "rows": n, "go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "passed": !t.Failed(), "load_seconds": loadSeconds, "live_database_bytes": databaseBytes, "peak_rss_mib": hugeRSS(), "aptevadb_options": options, "native_memory": memoryDiagnostics, "paired_native": paired, "watchers": os.Getenv("DB_HUGE_AGG_WATCHERS"), "aptevadb_version": native.Version, "results": results}, "", "  ")
		checkErr(t, e)
		checkErr(t, os.MkdirAll(filepath.Dir(path), 0755))
		checkErr(t, os.WriteFile(path, append(b, '\n'), 0644))
	}()
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &options})
	checkErr(t, e)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	call(t, m, "p", "database_create", Request{Database: "huge", Adapter: adapter})
	call(t, m, "p", "collection_create", Request{Database: "huge", Collection: "items", Fields: []Field{{"n", "number", false}, {"i", "integer", false}, {"bucket", "text", false}, {"tenant", "text", false}, {"optional", "text", true}, {"pad", "text", false}}})
	call(t, m, "p", "index_create", Request{Database: "huge", Collection: "items", Index: &Index{Name: "by_tenant", Fields: []Order{{"tenant", "asc"}}}})
	start, last := time.Now(), time.Now()
	pad := strings.Repeat("x", 128)
	for offset := 0; offset < n; offset += 1000 {
		if offset%10000 == 0 {
			var fs syscall.Statfs_t
			checkErr(t, syscall.Statfs(m.root, &fs))
			if uint64(fs.Bavail)*uint64(fs.Bsize) < 256<<20 {
				t.Fatal("benchmark stopped with less than 256 MiB free disk")
			}
		}
		rs := make([]Record, 1000)
		for j := range rs {
			i := offset + j
			var optional any = "present"
			if i%5 == 0 {
				optional = nil
			}
			rs[j] = Record{"id": fmt.Sprintf("r%09d", i), "n": float64(i), "i": strconv.Itoa(i), "bucket": strconv.Itoa(i % 4), "tenant": fmt.Sprintf("g%04d", i%1000), "optional": optional, "pad": pad}
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, e := m.Execute(ctx, "p", "insert", Request{Database: "huge", Collection: "items", Records: rs})
		cancel()
		checkErr(t, e)
		if time.Since(last) > 15*time.Second {
			t.Logf("%s loaded %d/%d", adapter, offset+1000, n)
			last = time.Now()
		}
	}
	loadSeconds = time.Since(start).Seconds()
	if raw := os.Getenv("DB_HUGE_AGG_WATCHERS"); raw != "" {
		count, err := strconv.Atoi(raw)
		checkErr(t, err)
		for i := 0; i < count; i++ {
			w, err := m.OpenWatch(context.Background(), "p", Request{Database: "huge", Collections: []string{"items"}, WatchCaller: fmt.Sprint(i)})
			checkErr(t, err)
			t.Cleanup(w.Close)
		}
	}

	checkErr(t, filepath.Walk(m.root, func(_ string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if !info.IsDir() {
			databaseBytes += info.Size()
		}
		return nil
	}))
	t.Logf("%s loaded %d in %.2fs; live disk %.1f MiB", adapter, n, loadSeconds, float64(databaseBytes)/(1<<20))
	measureOne := func(mode string, fn func(context.Context) (any, error), name string, r Request, verify func(any), expectedError string) {
		r.Database = "huge"
		r.Collection = "items"
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		start := time.Now()
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		v, e := fn(ctx)
		cancel()
		if e == nil {
			_, e = json.Marshal(v)
		}
		elapsed := time.Since(start).Seconds()
		runtime.ReadMemStats(&after)
		code := ""
		if e != nil {
			var app *Error
			if errors.As(e, &app) {
				code = app.Code
			} else {
				code = e.Error()
			}
		}
		results = append(results, map[string]any{"operation": name, "mode": mode, "seconds": elapsed, "success": e == nil, "error_code": code, "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "mallocs": after.Mallocs - before.Mallocs, "heap_mib": float64(after.HeapAlloc) / (1 << 20), "peak_rss_mib": hugeRSS()})
		t.Logf("%s %s %.4fs error=%s allocated=%.1f MiB heap=%.1f MiB", adapter+"/"+mode, name, elapsed, code, float64(after.TotalAlloc-before.TotalAlloc)/(1<<20), float64(after.HeapAlloc)/(1<<20))
		if expectedError != "" {
			expectCode(t, e, expectedError)
			return
		}
		checkErr(t, e)
		verify(v)
	}

	calls := 0
	measure := func(name string, r Request, verify func(any), expectedError string) {
		r.Database = "huge"
		r.Collection = "items"
		app := func(ctx context.Context) (any, error) { return m.Execute(ctx, "p", "aggregate", r) }
		if !paired {
			measureOne("app_engine", app, name, r, verify, expectedError)
			return
		}
		direct := hugeNativeAggregate(t, m, r)
		nativeExpectedError := expectedError

		if calls%2 == 0 {
			measureOne("app_engine", app, name, r, verify, expectedError)
			measureOne("native_driver", direct, name, r, verify, nativeExpectedError)
		} else {
			measureOne("native_driver", direct, name, r, verify, nativeExpectedError)
			measureOne("app_engine", app, name, r, verify, expectedError)
		}
		calls++
	}
	metrics := []Metric{{"count", "count", ""}, {"sum", "sum", "n"}, {"avg", "avg", "n"}, {"min", "min", "n"}, {"max", "max", "n"}}
	verifyGroups := func(field string, groups int, firstOf func(Record) int) func(any) {
		return func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != groups {
				t.Fatalf("%s groups got %d want %d", field, len(rows), groups)
			}
			for _, row := range rows {
				first := float64(firstOf(row))
				last := float64(n-groups) + first
				avg := (first + last) / 2
				cnt := n / groups
				if row["count"] != strconv.Itoa(cnt) || number(row["sum"]) != avg*float64(cnt) || number(row["avg"]) != avg || number(row["min"]) != first || number(row["max"]) != last {
					t.Fatalf("aggregate mismatch: %v", row)
				}
			}
		}
	}
	bucketFirst := func(row Record) int { v, e := strconv.Atoi(row["bucket"].(string)); checkErr(t, e); return v }
	tenantFirst := func(row Record) int {
		v, e := strconv.Atoi(strings.TrimPrefix(row["tenant"].(string), "g"))
		checkErr(t, e)
		return v
	}
	for repeat := 0; repeat < 5; repeat++ {
		measure("numeric_four_groups", Request{GroupBy: []string{"bucket"}, Metrics: metrics}, verifyGroups("bucket", 4, bucketFirst), "")
		measure("numeric_1000_groups", Request{GroupBy: []string{"tenant"}, Metrics: metrics, Query: Query{Limit: 1000}}, verifyGroups("tenant", 1000, tenantFirst), "")
		measure("integer_four_groups", Request{GroupBy: []string{"bucket"}, Metrics: []Metric{{"count", "count", ""}, {"sum", "sum", "i"}, {"avg", "avg", "i"}, {"min", "min", "i"}, {"max", "max", "i"}}}, func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != 4 {
				t.Fatal(rows)
			}
			for _, row := range rows {
				first := int64(bucketFirst(row))
				last := int64(n-4) + first
				sum := (first + last) * int64(n/4) / 2
				avg := float64(first+last) / 2
				if row["count"] != strconv.Itoa(n/4) || row["sum"] != strconv.FormatInt(sum, 10) || number(row["avg"]) != avg || row["min"] != strconv.FormatInt(first, 10) || row["max"] != strconv.FormatInt(last, 10) {
					t.Fatal(row)
				}
			}
		}, "")
		measure("nullable_count_four_groups", Request{GroupBy: []string{"bucket"}, Metrics: []Metric{{"total", "count", ""}, {"present", "count", "optional"}}}, func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != 4 {
				t.Fatal(rows)
			}
			for _, row := range rows {
				if row["total"] != strconv.Itoa(n/4) || row["present"] != strconv.Itoa(n/5) {
					t.Fatal(row)
				}
			}
		}, "")
		measure("indexed_tenant", Request{GroupBy: []string{"tenant"}, Metrics: metrics, Query: Query{Where: &Filter{Field: "tenant", Op: "eq", Value: "g0007"}, RequireIndex: true}}, func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != 1 {
				t.Fatal(rows)
			}
			row := rows[0]
			first, last := 7., float64(n-1000+7)
			avg := (first + last) / 2
			cnt := n / 1000
			if row["tenant"] != "g0007" || row["count"] != strconv.Itoa(cnt) || number(row["sum"]) != avg*float64(cnt) || number(row["avg"]) != avg || number(row["min"]) != first || number(row["max"]) != last {
				t.Fatal(row)
			}
		}, "")
	}
	// Force ordering by an aggregate metric so LIMIT 1 is not an input-row limit.
	expectedError := "resource_limit"
	if adapter == "sqlite" || adapter == "aptevadb" {
		expectedError = ""
	}
	for repeat := 0; repeat < 3; repeat++ {
		measure("one_group_per_record_top1", Request{GroupBy: []string{"id"}, Metrics: []Metric{{"count", "count", ""}}, Query: Query{OrderBy: []Order{{"count", "desc"}}, Limit: 1}}, func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != 1 || rows[0]["id"] != "r000000000" || rows[0]["count"] != "1" {
				t.Fatal(rows)
			}
		}, expectedError)
	}
	// Uniqueness of input values alone is insufficient for the PK rewrite.
	// This column has one distinct value per row but no uniqueness constraint.
	nonPrimaryError := "resource_limit"
	if adapter == "sqlite" || adapter == "aptevadb" {
		nonPrimaryError = ""
	}
	for repeat := 0; repeat < 3; repeat++ {
		measure("one_group_per_non_primary_value_top1", Request{GroupBy: []string{"i"}, Metrics: []Metric{{"count", "count", ""}}, Query: Query{OrderBy: []Order{{"count", "desc"}}, Limit: 1}}, func(v any) {
			rows := v.(map[string]any)["rows"].([]Record)
			if len(rows) != 1 || rows[0]["i"] != "0" || rows[0]["count"] != "1" {
				t.Fatal(rows)
			}
		}, nonPrimaryError)
	}
	if adapter == "sqlite" || adapter == "aptevadb" {
		for repeat := 0; repeat < 3; repeat++ {
			measure("non_primary_numeric_million_groups_top1", Request{GroupBy: []string{"i"}, Metrics: []Metric{{"count", "count", ""}, {"sum_i", "sum", "i"}, {"min_i", "min", "i"}, {"max_i", "max", "i"}, {"avg_n", "avg", "n"}}, Query: Query{OrderBy: []Order{{"sum_i", "desc"}}, Limit: 1}}, func(v any) {
				rows := v.(map[string]any)["rows"].([]Record)
				last := strconv.Itoa(n - 1)
				if len(rows) != 1 || rows[0]["i"] != last || rows[0]["sum_i"] != last || rows[0]["min_i"] != last || rows[0]["max_i"] != last || rows[0]["count"] != "1" || number(rows[0]["avg_n"]) != float64(n-1) {
					t.Fatal(rows)
				}
			}, "")
		}
	}
	if adapter == "aptevadb" {
		d, e := m.resolve(context.Background(), "p", "huge", "", false)
		checkErr(t, e)
		db := d.backend.(*aptevaBackend).db
		before, e := db.MemoryStats(context.Background())
		checkErr(t, e)
		time.Sleep(3200 * time.Millisecond)
		after, e := db.MemoryStats(context.Background())
		checkErr(t, e)
		memoryDiagnostics = map[string]any{"before_idle": before, "after_idle": after, "idle_seconds": 3.2, "explicit_trim": false}
		row := call(t, m, "p", "get", Request{Database: "huge", Collection: "items", Key: Record{"id": fmt.Sprintf("r%09d", n-1)}}).(map[string]any)["record"].(Record)
		if row["i"] != strconv.Itoa(n-1) {
			t.Fatal("read after idle reclamation", row)
		}
	}
	checkErr(t, m.Close())
}

func hugeRSS() float64 {
	var usage syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &usage) != nil {
		return 0
	}
	bytes := float64(usage.Maxrss)
	if runtime.GOOS != "darwin" {
		bytes *= 1024
	}
	return bytes / (1 << 20)
}

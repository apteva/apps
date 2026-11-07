package engine

import (
	"context"
	"encoding/json"
	"fmt"
	native "github.com/apteva/db"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"testing"
	"time"
)

// Opt-in fresh-fixture scale test. Every timed commit is observed by every
// watcher before advancing; a subscriber failure cannot appear as fast delivery.
func TestRealtimePerformance(t *testing.T) {
	path := os.Getenv("DB_REALTIME_PERF_OUTPUT")
	if path == "" {
		t.Skip("set DB_REALTIME_PERF_OUTPUT")
	}
	n := 100000
	if raw := os.Getenv("DB_REALTIME_PERF_ROWS"); raw != "" {
		var e error
		n, e = strconv.Atoi(raw)
		checkErr(t, e)
	}
	if n < 1000 || n%1000 != 0 {
		t.Fatal("rows must be a multiple of 1000")
	}
	var results []map[string]any
	for run := 1; run <= 3; run++ {
		for _, count := range []int{-1, 0, 1, 10, 32} {
			label := fmt.Sprintf("run%d_watchers%d", run, count)
			t.Run(label, func(t *testing.T) {
				capture := count >= 0
				t.Setenv("NATIVE_COMPARISON_CAPTURE", map[bool]string{true: "1", false: "0"}[capture])
				for _, mode := range []string{"app_engine", "native_driver"} {
					t.Run(mode, func(t *testing.T) {
						f := setupNativeFixture(t, "aptevadb", n)
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
						defer cancel()
						var wg sync.WaitGroup
						starts := make([]chan time.Time, max(0, count))
						delivered := make([]chan time.Time, len(starts))
						failures := make(chan error, max(1, count))
						watchers := make([]*Watch, 0, len(starts))
						cursors := make([]*native.ChangeCursor, 0, len(starts))
						for i := range starts {
							starts[i] = make(chan time.Time, 1)
							delivered[i] = make(chan time.Time, 1)
							var next func() error
							if mode == "app_engine" {
								w, e := f.m.OpenWatch(ctx, "bench", Request{Database: "bench", Collections: []string{"records"}, WatchCaller: fmt.Sprint(i)})
								checkErr(t, e)
								watchers = append(watchers, w)
								next = func() error {
									for {
										v, e := w.Next()
										if e != nil {
											return e
										}
										if v.Cursor == "" {
											return fmt.Errorf("closed cursor")
										}
										if v.Type == "invalidate" {
											return nil
										}
									}
								}
							} else {
								c, e := f.d.backend.(*aptevaBackend).db.Watch(ctx, native.WatchOptions{Collections: []string{nativeCollectionName("records")}, MaxMemoryBytes: 8 << 20})
								checkErr(t, e)
								cursors = append(cursors, c)
								next = func() error {
									for c.Next() {
										if c.Change().EndOfCommit && c.Change().Operation != native.ChangeProgress {
											return nil
										}
									}
									return c.Err()
								}
							}
							wg.Add(1)
							go func(i int, next func() error) {
								defer wg.Done()
								for {
									select {
									case <-ctx.Done():
										return
									case <-starts[i]:
										if e := next(); e != nil {
											select {
											case failures <- e:
											default:
											}
											return
										}
										select {
										case delivered[i] <- time.Now():
										case <-ctx.Done():
											return
										}
									}
								}
							}(i, next)
						}
						defer func() {
							cancel()
							for _, w := range watchers {
								w.Close()
							}
							for _, c := range cursors {
								c.Close()
							}
							wg.Wait()
						}()
						app := func(op string, r Request) any {
							r.Database = "bench"
							r.Collection = "records"
							return call(t, f.m, "bench", op, r)
						}
						get := func(i int) any { return f.nativeGet(t, i*7919%n) }
						if mode == "app_engine" {
							get = func(i int) any { return app("get", Request{Key: Record{"id": benchRow(i * 7919 % n)["id"]}}) }
						}
						read := measureNative(t, "aptevadb", mode, "get", n, run, 1000, get, func(v any, i int) {
							if v.(map[string]any)["record"].(Record)["id"] != benchRow(i * 7919 % n)["id"] {
								t.Fatal("wrong get")
							}
						})
						readAggregate := func(int) any { return f.nativeScan(t, true) }
						if mode == "app_engine" {
							readAggregate = func(int) any {
								v := app("aggregate", Request{Metrics: []Metric{{Name: "total", Op: "sum", Field: "amount"}}})
								return number(v.(map[string]any)["rows"].([]Record)[0]["total"])
							}
						}
						sum := measureNative(t, "aptevadb", mode, "sum", n, run, 10, readAggregate, func(v any, _ int) {
							if v.(float64) != float64(n/1000)*499500 {
								t.Fatal("wrong sum")
							}
						})
						var writes, notifications []comparisonResult
						for _, size := range []int{1, 1000} {
							samples := 40
							if size == 1000 {
								samples = 10
							}
							var notify []float64
							inputs := make([][]Record, samples)
							for i := range inputs {
								for j := 0; j < size; j++ {
									id := n + run*10000000 + size*100000 + len(writes)*1000000 + i*size + j
									if mode == "native_driver" {
										id += 100000000
									}
									inputs[i] = append(inputs[i], benchRow(id))
								}
							}
							write := func(i int) any {
								start := time.Now()
								for _, ch := range starts {
									ch <- start
								}
								var v any
								if mode == "app_engine" {
									v = app("insert", Request{Records: inputs[i]})
								} else {
									v = f.nativeInsert(t, inputs[i])
								}
								_ = marshal(v)
								committed := time.Now()
								// Write timer ends at commit/response; notification waits are outside it.
								v.(map[string]any)["bench_completed"] = committed
								return v
							}
							times := make([]float64, 0, samples)
							for i := 0; i < samples; i++ {
								start := time.Now()
								v := write(i).(map[string]any)
								committed := v["bench_completed"].(time.Time)
								times = append(times, float64(committed.Sub(start).Nanoseconds())/1000)
								keys := v["keys"].([]Record)
								if len(keys) != size {
									t.Fatal("wrong keys")
								}
								for j, key := range keys {
									if key["id"] != inputs[i][j]["id"] {
										t.Fatal("wrong key")
									}
								}
								if v["affected"] != size {
									t.Fatal("wrong affected")
								}
								for _, ch := range delivered {
									select {
									case at := <-ch:
										notify = append(notify, max(0, float64(at.Sub(committed).Nanoseconds())/1000))
									case e := <-failures:
										t.Fatal("watch failed", e)
									case <-ctx.Done():
										t.Fatal("missed commit", ctx.Err())
									}
								}
							}
							writes = append(writes, perfSamples(mode, fmt.Sprintf("insert_%d", size), times))
							if len(notify) > 0 {
								notifications = append(notifications, perfSamples(mode, fmt.Sprintf("commit_return_to_notification_%d", size), notify))
							}
						}
						if got := f.nativeScan(t, false); got != float64(n+10040) {
							t.Fatal("wrong persisted count", got)
						}
						stats := f.d.backend.(*aptevaBackend).db.ChangeFeedStats()
						results = append(results, map[string]any{"run": run, "rows": n, "capture": capture, "watchers": max(0, count), "mode": mode, "get": read, "sum": sum, "writes": writes, "notifications": notifications, "feed_stats": stats, "database_bytes": realtimeDiskBytes(t, f.m.root)})
					})
				}
			})
		}
	}
	checkErr(t, os.MkdirAll(filepath.Dir(path), 0755))
	data, e := json.MarshalIndent(map[string]any{"version": native.Version, "rows": n, "sync_mode": os.Getenv("NATIVE_COMPARISON_APTEVADB_SYNC_MODE"), "go": runtime.Version(), "passed": !t.Failed(), "results": results}, "", "  ")
	checkErr(t, e)
	checkErr(t, os.WriteFile(path, data, 0644))
}
func perfSamples(mode, operation string, times []float64) comparisonResult {
	sorted := append([]float64(nil), times...)
	sortFloats(sorted)
	return comparisonResult{Adapter: "aptevadb", Mode: mode, Operation: operation, Samples: len(times), P50US: sorted[len(sorted)/2], P95US: sorted[min(len(sorted)-1, int(float64(len(sorted))*.95))], Times: times}
}
func sortFloats(values []float64) { sort.Float64s(values) }
func realtimeDiskBytes(t *testing.T, root string) int64 {
	t.Helper()
	var total int64
	checkErr(t, filepath.Walk(root, func(_ string, info os.FileInfo, e error) error {
		if e != nil {
			return e
		}
		if !info.IsDir() {
			total += info.Size()
		}
		return nil
	}))
	return total
}

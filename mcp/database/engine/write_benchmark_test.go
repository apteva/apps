//go:build writebenchmark

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

type countedWrites struct {
	*pebbleBackend
	commits int
}

func (b *countedWrites) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	if write {
		b.commits++
	}
	return b.pebbleBackend.transaction(ctx, write, fn)
}
func (b *countedWrites) transactions(calls []transactionCall) []error {
	return b.combine(calls, func(batch *pebble.Batch) error { b.commits++; return b.commit(batch) })
}

func TestConcurrentWriteBenchmark(t *testing.T) {
	path := os.Getenv("DB_WRITE_BENCHMARK_OUTPUT")
	if path == "" {
		t.Fatal("DB_WRITE_BENCHMARK_OUTPUT must name a new output file")
	}
	var results []map[string]any
	for _, clients := range []int{1, 8, 32} {
		for run := 1; run <= 3; run++ {
			modes := []string{"serial", "grouped"}
			if os.Getenv("DB_WRITE_BENCHMARK_COMPARE") == "1" {
				modes = []string{"sqlite_default", "sqlite_fullfsync", "grouped"}
			}
			if run == 2 {
				for i, j := 0, len(modes)-1; i < j; i, j = i+1, j-1 {
					modes[i], modes[j] = modes[j], modes[i]
				}
			}
			for _, mode := range modes {
				t.Run(fmt.Sprintf("%s_%dclients_%d", mode, clients, run), func(t *testing.T) {
					adapter := "pebble"
					if mode == "sqlite_default" || mode == "sqlite_fullfsync" {
						adapter = "sqlite"
					}
					m := manager(t)
					fixture(t, m, adapter)
					d, err := m.resolve(context.Background(), "p", "shop", "", false)
					if err != nil {
						t.Fatal(err)
					}
					for _, index := range []Index{
						{Name: "by_email", Fields: []Order{{Field: "email"}}, Unique: true},
						{Name: "by_country_units", Fields: []Order{{Field: "country"}, {Field: "units", Direction: "desc"}}},
						{Name: "by_amount", Fields: []Order{{Field: "amount", Direction: "desc"}}},
					} {
						call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &index})
					}
					row := func(i int) Record {
						return Record{"id": fmt.Sprintf("r%09d", i), "email": fmt.Sprintf("r%d@example.test", i), "country": "es", "units": fmt.Sprint(i%5 + 1), "amount": float64(i), "active": true}
					}
					seed := make([]Record, 1000)
					for i := range seed {
						seed[i] = row(i)
					}
					call(t, m, "p", "insert", Request{Database: "shop", Collection: "orders", Records: seed})
					var counter *countedWrites
					fullfsync := -1
					if adapter == "sqlite" {
						db := d.backend.(*sqliteBackend).db
						if mode == "sqlite_fullfsync" {
							if _, err := db.Exec("PRAGMA fullfsync=ON"); err != nil {
								t.Fatal(err)
							}
						}
						if err := db.QueryRow("PRAGMA fullfsync").Scan(&fullfsync); err != nil {
							t.Fatal(err)
						}
						want := 0
						if mode == "sqlite_fullfsync" {
							want = 1
						}
						if fullfsync != want {
							t.Fatalf("fullfsync=%d, want %d", fullfsync, want)
						}
					} else {
						counter = &countedWrites{pebbleBackend: d.backend.(*pebbleBackend)}
						if mode == "serial" {
							d.backend = struct{ backend }{counter}
						} else {
							d.backend = counter
						}
					}
					const each = 40
					all := make([][]float64, clients)
					errors := make(chan error, clients)
					start := make(chan struct{})
					var wg sync.WaitGroup
					for w := 0; w < clients; w++ {
						wg.Add(1)
						go func(w int) {
							defer wg.Done()
							<-start
							for j := 0; j < each; j++ {
								r := Request{Database: "shop", Collection: "orders", Records: []Record{row(1000 + w*each + j)}}
								ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
								t0 := time.Now()
								v, e := m.Execute(ctx, "p", "insert", r)
								if e == nil {
									_, e = json.Marshal(v)
								}
								ms := float64(time.Since(t0).Nanoseconds()) / 1e6
								cancel()
								if e != nil {
									errors <- e
									return
								}
								if v.(map[string]any)["affected"] != 1 {
									errors <- fmt.Errorf("wrong affected count")
									return
								}
								all[w] = append(all[w], ms)
							}
						}(w)
					}
					t0 := time.Now()
					close(start)
					wg.Wait()
					elapsed := time.Since(t0).Seconds()
					close(errors)
					for e := range errors {
						t.Fatal(e)
					}
					var samples []float64
					for _, v := range all {
						samples = append(samples, v...)
					}
					x := append([]float64(nil), samples...)
					sort.Float64s(x)
					v := call(t, m, "p", "count", Request{Database: "shop", Collection: "orders"}).(map[string]any)
					if v["count"] != fmt.Sprint(1004+clients*each) {
						t.Fatalf("count mismatch %v", v)
					}
					commits := len(samples)
					if counter != nil {
						commits = counter.commits
					}
					result := map[string]any{"mode": mode, "adapter": adapter, "sqlite_fullfsync": fullfsync, "clients": clients, "run": run, "requests": len(samples), "samples_ms": samples, "p50_ms": x[len(x)/2-1], "p95_ms": x[(len(x)*95)/100-1], "ops_per_second": float64(len(samples)) / elapsed, "durable_commits": commits}
					results = append(results, result)
					t.Logf("%s %d clients: %.0f ops/s, p50 %.3f ms, p95 %.3f ms, %d durable commits", mode, clients, result["ops_per_second"], result["p50_ms"], result["p95_ms"], commits)
				})
			}
		}
	}
	if t.Failed() {
		return
	}
	b, e := json.MarshalIndent(map[string]any{"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "created_utc": time.Now().UTC(), "results": results}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	f, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if e != nil {
		t.Fatal(e)
	}
	defer f.Close()
	if _, e = f.Write(append(b, '\n')); e != nil {
		t.Fatal(e)
	}
}

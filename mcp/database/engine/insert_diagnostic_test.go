//go:build insertdiagnostic

package engine

// Opt-in local diagnostic; all settings changes apply to temporary test data.
import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/cockroachdb/pebble/v2"
)

type insertProbe struct {
	*pebbleBackend
	prepare, commit []float64
}

func (p *insertProbe) transaction(ctx context.Context, write bool, fn func(transaction) error) error {
	if !write {
		return p.pebbleBackend.transaction(ctx, false, fn)
	}
	start := time.Now()
	batch := p.db.NewIndexedBatch()
	defer batch.Close()
	if err := fn(&pebbleTx{ctx, batch, batch}); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	p.prepare = append(p.prepare, float64(time.Since(start).Nanoseconds())/1e6)
	start = time.Now()
	err := batch.Commit(pebble.Sync)
	p.commit = append(p.commit, float64(time.Since(start).Nanoseconds())/1e6)
	return err
}

func TestInsertDurabilityDiagnostic(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("this experiment compares macOS fullfsync behavior")
	}
	output := os.Getenv("DB_INSERT_DIAGNOSTIC_OUTPUT")
	if output == "" {
		t.Fatal("DB_INSERT_DIAGNOSTIC_OUTPUT must name a new result file")
	}
	var results []map[string]any
	for run := 1; run <= 3; run++ {
		modes := []string{"sqlite_default", "sqlite_fullfsync", "pebble_sync"}
		if run == 2 {
			modes = []string{"pebble_sync", "sqlite_fullfsync", "sqlite_default"}
		}
		for _, mode := range modes {
			t.Run(fmt.Sprintf("%s_%d", mode, run), func(t *testing.T) {
				m, err := Open(t.TempDir())
				if err != nil {
					t.Fatal(err)
				}
				defer m.Close()
				adapter := "sqlite"
				if mode == "pebble_sync" {
					adapter = "pebble"
				}
				call := func(op string, r Request) any {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					r.Database, r.Collection = "probe", "orders"
					v, err := m.Execute(ctx, "diagnostic", op, r)
					if err != nil {
						t.Fatal(err)
					}
					if _, err = json.Marshal(v); err != nil {
						t.Fatal(err)
					}
					return v
				}
				call("database_create", Request{Adapter: adapter})
				call("collection_create", Request{Fields: []Field{{Name: "email", Type: "text"}, {Name: "tenant", Type: "text"}, {Name: "status", Type: "text"}, {Name: "created_at", Type: "datetime"}, {Name: "amount", Type: "number"}, {Name: "units", Type: "integer"}, {Name: "note", Type: "text"}}})
				for _, index := range []Index{
					{Name: "by_email", Fields: []Order{{Field: "email", Direction: "asc"}}, Unique: true},
					{Name: "by_tenant_created", Fields: []Order{{Field: "tenant", Direction: "asc"}, {Field: "created_at", Direction: "desc"}}},
					{Name: "by_status_created", Fields: []Order{{Field: "status", Direction: "asc"}, {Field: "created_at", Direction: "desc"}}},
				} {
					call("index_create", Request{Index: &index})
				}
				row := func(i int) Record {
					id := fmt.Sprintf("r%09d", i)
					return Record{"id": id, "email": id + "@example.test", "tenant": fmt.Sprintf("t%04d", i%1000), "status": []string{"paid", "pending", "cancelled", "draft"}[(i/1000)%4], "created_at": time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339), "amount": float64(i%10000) / 100, "units": fmt.Sprint(i%5 + 1), "note": id + strings.Repeat("x", 112)}
				}
				seed := make([]Record, 1000)
				for i := range seed {
					seed[i] = row(i)
				}
				call("insert", Request{Records: seed})
				d, err := m.resolve(context.Background(), "diagnostic", "probe", "", false)
				if err != nil {
					t.Fatal(err)
				}
				var probe *insertProbe
				fullfsync := -1
				if adapter == "sqlite" {
					db := d.backend.(*sqliteBackend).db
					if mode == "sqlite_fullfsync" {
						if _, err = db.Exec("PRAGMA fullfsync=ON"); err != nil {
							t.Fatal(err)
						}
					}
					if err = db.QueryRow("PRAGMA fullfsync").Scan(&fullfsync); err != nil {
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
					probe = &insertProbe{pebbleBackend: d.backend.(*pebbleBackend)}
					// Keep this diagnostic on the original serial transaction path.
					d.backend = struct{ backend }{probe}
				}
				var elapsed []float64
				for i := 1000; i < 1200; i++ {
					r := Request{Records: []Record{row(i)}}
					start := time.Now()
					v := call("insert", r)
					elapsed = append(elapsed, float64(time.Since(start).Nanoseconds())/1e6)
					if v.(map[string]any)["affected"] != 1 {
						t.Fatal("wrong affected count")
					}
				}
				percentile := func(x []float64, fraction float64) float64 {
					copy := append([]float64(nil), x...)
					sort.Float64s(copy)
					return copy[int(float64(len(copy))*fraction)-1]
				}
				result := map[string]any{"mode": mode, "run": run, "rows_before": 1000, "samples": elapsed, "p50_ms": percentile(elapsed, .5), "p95_ms": percentile(elapsed, .95), "sqlite_fullfsync": fullfsync}
				if probe != nil {
					var total, commits float64
					for i, v := range elapsed {
						total += v
						commits += probe.commit[i]
					}
					result["prepare_ms"], result["commit_ms"] = probe.prepare, probe.commit
					result["prepare_p50_ms"], result["commit_p50_ms"] = percentile(probe.prepare, .5), percentile(probe.commit, .5)
					result["commit_fraction_of_total_time"] = commits / total
				}
				results = append(results, result)
				t.Logf("%s: p50 %.3f ms, p95 %.3f ms", mode, result["p50_ms"], result["p95_ms"])
			})
		}
	}
	if t.Failed() {
		return
	}
	b, err := json.MarshalIndent(map[string]any{"go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "created_utc": time.Now().UTC(), "results": results}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(output, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.Write(append(b, '\n')); err != nil {
		t.Fatal(err)
	}
}

//go:build largescan

package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"testing"
	"time"
)

// An opt-in functional scale check through the public contract, with actual
// default query deadlines. This is not a statistical latency benchmark.
func TestLargeScanBenchmark(t *testing.T) {
	n := 100000
	if value := os.Getenv("DB_LARGE_SCAN_ROWS"); value != "" {
		v, e := strconv.Atoi(value)
		if e != nil || v < 10000 || v%1000 != 0 {
			t.Fatal("rows must be a multiple of 1000 >=10000")
		}
		n = v
	}
	repeats := 1
	if value := os.Getenv("DB_LARGE_SCAN_REPEATS"); value != "" {
		v, e := strconv.Atoi(value)
		if e != nil || v < 1 || v > 10 {
			t.Fatal("repeats must be 1–10")
		}
		repeats = v
	}
	path := os.Getenv("DB_LARGE_SCAN_OUTPUT")
	if path == "" {
		t.Fatal("DB_LARGE_SCAN_OUTPUT required")
	}
	profileDir := os.Getenv("DB_LARGE_SCAN_PROFILE_DIR")
	if profileDir != "" {
		if e := os.MkdirAll(profileDir, 0755); e != nil {
			t.Fatal(e)
		}
	}
	var results []map[string]any
	for _, adapter := range []string{"sqlite", "pebble"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			call(t, m, "p", "database_create", Request{Database: "scale", Adapter: adapter})
			call(t, m, "p", "collection_create", Request{Database: "scale", Collection: "items", Fields: []Field{{"n", "number", false}, {"bucket", "text", false}, {"pad", "text", false}}})
			start := time.Now()
			last := start
			for offset := 0; offset < n; offset += 1000 {
				r := make([]Record, 1000)
				for j := range r {
					i := offset + j
					r[j] = Record{"id": fmt.Sprintf("r%09d", i), "n": float64(i), "bucket": fmt.Sprint(i % 4), "pad": strings.Repeat("x", 128)}
				}
				ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
				_, e := m.Execute(ctx, "p", "insert", Request{Database: "scale", Collection: "items", Records: r})
				cancel()
				if e != nil {
					t.Fatal("load", offset, e)
				}
				if time.Since(last) > 15*time.Second {
					t.Logf("%s loaded %d/%d", adapter, offset+1000, n)
					last = time.Now()
				}
			}
			results = append(results, map[string]any{"adapter": adapter, "rows": n, "operation": "load", "seconds": time.Since(start).Seconds()})
			measure := func(op string, r Request) any {
				r.Database = "scale"
				r.Collection = "items"
				var profile *os.File
				if profileDir != "" {
					var e error
					profile, e = os.Create(filepath.Join(profileDir, fmt.Sprintf("%s-%d-%s.cpu", adapter, len(results), op)))
					if e != nil {
						t.Fatal(e)
					}
					if e = pprof.StartCPUProfile(profile); e != nil {
						profile.Close()
						t.Fatal(e)
					}
				}
				start := time.Now()
				v, e := m.Execute(context.Background(), "p", op, r)
				if e == nil {
					_, e = json.Marshal(v)
				}
				seconds := time.Since(start).Seconds()
				if profile != nil {
					pprof.StopCPUProfile()
					profile.Close()
				}
				result := map[string]any{"adapter": adapter, "rows": n, "operation": op, "request": r, "seconds": seconds, "success": e == nil}
				if e != nil {
					result["error"] = e.Error()
				}
				results = append(results, result)
				t.Logf("%s %s: %.3f seconds; error %v", adapter, op, seconds, e)
				if e != nil {
					t.Fatal(e)
				}
				return v
			}
			for repeat := 0; repeat < repeats; repeat++ {
				if v := measure("count", Request{}).(map[string]any)["count"]; v != fmt.Sprint(n) {
					t.Fatal(v)
				}
				v := measure("find", Request{Query: Query{Where: &Filter{Field: "n", Op: "eq", Value: float64(n - 1)}, Limit: 1}}).(map[string]any)["records"].([]Record)
				if len(v) != 1 || number(v[0]["n"]) != float64(n-1) {
					t.Fatal("late search mismatch")
				}
				v = measure("find", Request{Query: Query{OrderBy: []Order{{"n", "desc"}}, Select: []string{"id", "n"}, Limit: 10}}).(map[string]any)["records"].([]Record)
				if len(v) != 10 {
					t.Fatal("sort count mismatch")
				}
				for i, r := range v {
					if number(r["n"]) != float64(n-1-i) {
						t.Fatal("sort mismatch")
					}
				}
				v = measure("aggregate", Request{GroupBy: []string{"bucket"}, Metrics: []Metric{{"count", "count", ""}, {"sum", "sum", "n"}, {"avg", "avg", "n"}, {"min", "min", "n"}, {"max", "max", "n"}}}).(map[string]any)["rows"].([]Record)
				if len(v) != 4 {
					t.Fatal("aggregate group mismatch")
				}
				for _, r := range v {
					bucket, e := strconv.Atoi(r["bucket"].(string))
					if e != nil {
						t.Fatal(e)
					}
					first, last := float64(bucket), float64(n-4+bucket)
					average := (first + last) / 2
					if r["count"] != fmt.Sprint(n/4) || number(r["sum"]) != average*float64(n/4) || number(r["avg"]) != average || number(r["min"]) != first || number(r["max"]) != last {
						t.Fatal("aggregate mismatch", r)
					}
				}
			}
		})
	}
	b, e := json.MarshalIndent(map[string]any{"created_utc": time.Now().UTC(), "go_version": runtime.Version(), "os": runtime.GOOS, "arch": runtime.GOARCH, "passed": !t.Failed(), "results": results}, "", "  ")
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

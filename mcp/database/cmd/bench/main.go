// Command bench measures the public engine contract against synthetic local
// data. It never opens an existing data directory or changes engine limits.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/apteva/apps/mcp/database/engine"
)

type metric struct {
	Operation                string         `json:"operation"`
	Attempts                 int            `json:"attempts"`
	Successes                int            `json:"successes"`
	Errors                   map[string]int `json:"errors,omitempty"`
	FirstError               string         `json:"first_error,omitempty"`
	ElapsedSeconds           float64        `json:"elapsed_seconds"`
	SuccessfulOpsPerSecond   float64        `json:"successful_ops_per_second"`
	RecordsPerSecond         float64        `json:"records_per_second,omitempty"`
	P50ms                    float64        `json:"p50_ms"`
	P95ms                    float64        `json:"p95_ms"`
	Maxms                    float64        `json:"max_ms"`
	ErrorP50ms               float64        `json:"error_p50_ms,omitempty"`
	AllocatedBytesPerAttempt uint64         `json:"allocated_bytes_per_attempt"`
	MallocsPerAttempt        uint64         `json:"mallocs_per_attempt"`
	HeapAllocMiB             float64        `json:"heap_alloc_mib"`
	PeakRSSMiB               float64        `json:"process_peak_rss_mib"`
	SamplesMs                []float64      `json:"samples_ms,omitempty"`
	ErrorSamplesMs           []float64      `json:"error_samples_ms,omitempty"`
}
type report struct {
	Adapter           string         `json:"adapter"`
	Rows              int            `json:"rows"`
	Run               int            `json:"run"`
	StartedUTC        string         `json:"started_utc"`
	GoVersion         string         `json:"go_version"`
	OS                string         `json:"os"`
	Arch              string         `json:"arch"`
	GOMAXPROCS        int            `json:"gomaxprocs"`
	DeadlineSeconds   int            `json:"deadline_seconds"`
	BatchSize         int            `json:"batch_size"`
	ApproxRecordBytes int            `json:"approx_record_bytes"`
	SecondaryIndexes  int            `json:"secondary_indexes"`
	Metrics           []metric       `json:"metrics"`
	Plans             map[string]any `json:"plans"`
	LiveDiskMiB       float64        `json:"live_disk_mib"`
	ClosedDiskMiB     float64        `json:"closed_disk_mib"`
	PeakRSSMiB        float64        `json:"process_peak_rss_mib"`
	WallSeconds       float64        `json:"wall_seconds"`
	CorrectnessChecks int            `json:"correctness_checks"`
}

var (
	adapter     = flag.String("adapter", "sqlite", "sqlite or pebble")
	n           = flag.Int("rows", 10000, "number of synthetic records (multiple of 1000)")
	repeat      = flag.Int("run", 1, "independent run number")
	samples     = flag.Int("samples", 200, "samples for inexpensive operations")
	slowSamples = flag.Int("slow-samples", 7, "samples for scans and aggregates")
	output      = flag.String("output", "", "required result JSON path, outside the temporary data directory")
	checks      int
)

func die(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(1) }
func must(err error) {
	if err != nil {
		die(err)
	}
}
func logPhase(format string, args ...any) {
	fmt.Fprintf(os.Stderr, time.Now().Format("15:04:05")+" "+format+"\n", args...)
}
func id(i int) string    { return fmt.Sprintf("r%09d", i) }
func email(i int) string { return fmt.Sprintf("contact%09d@example.test", i) }
func stamp(i int) string {
	return time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC).Add(time.Duration(i) * time.Second).Format(time.RFC3339)
}
func row(i int) engine.Record {
	return engine.Record{
		"id": id(i), "email": email(i), "tenant": fmt.Sprintf("t%04d", i%1000),
		"status":     []string{"paid", "pending", "cancelled", "draft"}[(i/1000)%4],
		"created_at": stamp(i), "amount": float64(i%10000) / 100,
		"units": strconv.Itoa(i%5 + 1), "note": id(i) + strings.Repeat("x", 112),
	}
}
func rss() float64 {
	var r syscall.Rusage
	if syscall.Getrusage(syscall.RUSAGE_SELF, &r) != nil {
		return 0
	}
	bytes := float64(r.Maxrss)
	if runtime.GOOS != "darwin" {
		bytes *= 1024
	}
	return bytes / (1 << 20)
}
func disk(root string) float64 {
	var total int64
	must(filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() {
			info, e := d.Info()
			if e != nil {
				return e
			}
			total += info.Size()
		}
		return nil
	}))
	return float64(total) / (1 << 20)
}
func invoke(m *engine.Manager, op string, r engine.Request) (any, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	r.Database = "bench"
	if r.Collection == "" {
		r.Collection = "orders"
	}
	v, e := m.Execute(ctx, "benchmark", op, r)
	if e == nil {
		_, e = json.Marshal(v)
	} // Include result encoding, but not HTTP or input JSON decoding.
	return v, e
}
func errorCode(e error) string {
	var x *engine.Error
	if errors.As(e, &x) {
		return x.Code
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "deadline_exceeded"
	}
	return "other"
}
func percentile(v []float64, p float64) float64 {
	if len(v) == 0 {
		return 0
	}
	x := append([]float64{}, v...)
	sort.Float64s(x)
	i := int(math.Ceil(float64(len(x))*p)) - 1
	if i < 0 {
		i = 0
	}
	return x[i]
}
func stats(name string, start time.Time, before runtime.MemStats, good, bad []float64, errs map[string]int, first string, records int) metric {
	var after runtime.MemStats
	runtime.ReadMemStats(&after)
	elapsed := time.Since(start).Seconds()
	attempts := len(good) + len(bad)
	m := metric{Operation: name, Attempts: attempts, Successes: len(good), Errors: errs, FirstError: first, ElapsedSeconds: elapsed, SuccessfulOpsPerSecond: float64(len(good)) / elapsed, P50ms: percentile(good, .5), P95ms: percentile(good, .95), Maxms: percentile(good, 1), ErrorP50ms: percentile(bad, .5), HeapAllocMiB: float64(after.HeapAlloc) / (1 << 20), PeakRSSMiB: rss(), SamplesMs: good, ErrorSamplesMs: bad}
	if records > 0 {
		m.RecordsPerSecond = float64(records) / elapsed
	}
	if attempts > 0 {
		m.AllocatedBytesPerAttempt = (after.TotalAlloc - before.TotalAlloc) / uint64(attempts)
		m.MallocsPerAttempt = (after.Mallocs - before.Mallocs) / uint64(attempts)
	}
	logPhase("%s: %d/%d succeeded; p50 %.3f ms; p95 %.3f ms; errors %v", name, m.Successes, m.Attempts, m.P50ms, m.P95ms, errs)
	return m
}
func measure(name string, count int, request func(int) (any, error), verify func(int, any), warm bool) metric {
	if warm {
		for i := 0; i < 3; i++ {
			v, e := request(i)
			if e == nil && verify != nil {
				verify(i, v)
			}
		}
	}
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	good, bad := []float64{}, []float64{}
	errs := map[string]int{}
	first := ""
	for i := 0; i < count; i++ {
		t := time.Now()
		v, e := request(i)
		ms := float64(time.Since(t).Nanoseconds()) / 1e6
		if e != nil {
			bad = append(bad, ms)
			errs[errorCode(e)]++
			if first == "" {
				first = e.Error()
			}
		} else {
			good = append(good, ms)
			if verify != nil {
				verify(i, v)
			}
		}
	}
	return stats(name, start, before, good, bad, errs, first, 0)
}
func check(ok bool, format string, args ...any) {
	checks++
	if !ok {
		panic(fmt.Errorf("correctness check failed: "+format, args...))
	}
}
func records(v any) []engine.Record { return v.(map[string]any)["records"].([]engine.Record) }
func requireSuccess(m metric) {
	if m.Successes != m.Attempts {
		panic(fmt.Errorf("required operation %s failed: %s", m.Operation, m.FirstError))
	}
}

func run() error {
	if *output == "" || *n < 10000 || *n%1000 != 0 || *samples < 1 || *samples > 1000 || *slowSamples < 1 || (*adapter != "sqlite" && *adapter != "pebble") {
		return fmt.Errorf("output required; rows must be a positive multiple of 1000; valid adapter and sample counts required")
	}
	root, e := os.MkdirTemp("", "apteva-database-bench-")
	if e != nil {
		return e
	}
	defer os.RemoveAll(root)
	m, e := engine.Open(root)
	if e != nil {
		return e
	}
	defer m.Close()
	start := time.Now()
	r := report{Adapter: *adapter, Rows: *n, Run: *repeat, StartedUTC: time.Now().UTC().Format(time.RFC3339), GoVersion: runtime.Version(), OS: runtime.GOOS, Arch: runtime.GOARCH, GOMAXPROCS: runtime.GOMAXPROCS(0), DeadlineSeconds: 2, BatchSize: 1000, SecondaryIndexes: 3, Plans: map[string]any{}}
	encoded, _ := json.Marshal(row(0))
	r.ApproxRecordBytes = len(encoded)
	_, e = invoke(m, "database_create", engine.Request{Adapter: *adapter})
	if e != nil {
		return e
	}
	_, e = invoke(m, "collection_create", engine.Request{Fields: []engine.Field{{Name: "email", Type: "text"}, {Name: "tenant", Type: "text"}, {Name: "status", Type: "text"}, {Name: "created_at", Type: "datetime"}, {Name: "amount", Type: "number"}, {Name: "units", Type: "integer"}, {Name: "note", Type: "text"}}})
	if e != nil {
		return e
	}
	for _, idx := range []engine.Index{
		{Name: "by_email", Fields: []engine.Order{{Field: "email", Direction: "asc"}}, Unique: true},
		{Name: "by_tenant_created", Fields: []engine.Order{{Field: "tenant", Direction: "asc"}, {Field: "created_at", Direction: "desc"}}},
		{Name: "by_status_created", Fields: []engine.Order{{Field: "status", Direction: "asc"}, {Field: "created_at", Direction: "desc"}}},
	} {
		_, e = invoke(m, "index_create", engine.Request{Index: &idx})
		if e != nil {
			return e
		}
	}
	logPhase("%s run %d: loading %d rows with 3 secondary indexes", *adapter, *repeat, *n)
	var before runtime.MemStats
	runtime.ReadMemStats(&before)
	loading := time.Now()
	durations := []float64{}
	lastLog := time.Now()
	for batch := 0; batch < *n/1000; batch++ {
		rows := make([]engine.Record, 1000)
		for j := range rows {
			rows[j] = row(batch*1000 + j)
		}
		t := time.Now()
		v, e := invoke(m, "insert", engine.Request{Records: rows})
		durations = append(durations, float64(time.Since(t).Nanoseconds())/1e6)
		if e != nil {
			return fmt.Errorf("load batch %d: %w", batch, e)
		}
		check(v.(map[string]any)["affected"] == 1000, "load affected count")
		if time.Since(lastLog) > 15*time.Second {
			logPhase("loaded %d/%d rows", (batch+1)*1000, *n)
			lastLog = time.Now()
		}
	}
	r.Metrics = append(r.Metrics, stats("insert_batch_1000", loading, before, durations, nil, nil, "", *n))
	// Load throughput includes deterministic record construction; per-batch
	// latencies only include Execute + output JSON. Both are stated in report.
	readIndex := func(i int) int { return (i*7919 + *n/2) % *n }
	appendRequired := func(v metric) { requireSuccess(v); r.Metrics = append(r.Metrics, v) }
	appendRequired(measure("get_primary", *samples, func(i int) (any, error) {
		return invoke(m, "get", engine.Request{Key: engine.Record{"id": id(readIndex(i))}})
	}, func(i int, v any) {
		x := v.(map[string]any)
		check(x["found"] == true && x["record"].(engine.Record)["email"] == email(readIndex(i)), "primary lookup mismatch")
	}, true))
	appendRequired(measure("find_unique_email", *samples, func(i int) (any, error) {
		return invoke(m, "find", engine.Request{Query: engine.Query{Where: &engine.Filter{Field: "email", Op: "eq", Value: email(readIndex(i))}, Limit: 1, RequireIndex: true}})
	}, func(i int, v any) {
		rs := records(v)
		check(len(rs) == 1 && rs[0]["id"] == id(readIndex(i)), "unique lookup mismatch")
	}, true))
	compound := engine.Query{Where: &engine.Filter{And: []engine.Filter{{Field: "status", Op: "eq", Value: "paid"}, {Field: "created_at", Op: "gte", Value: stamp(*n / 2)}}}, OrderBy: []engine.Order{{Field: "created_at", Direction: "desc"}}, Limit: 50, RequireIndex: true}
	appendRequired(measure("find_compound_50", *samples, func(int) (any, error) { return invoke(m, "find", engine.Request{Query: compound}) }, func(_ int, v any) {
		rs := records(v)
		check(len(rs) == 50, "compound query returned %d rows", len(rs))
		for j, x := range rs {
			check(x["status"] == "paid" && x["created_at"].(string) >= stamp(*n/2), "compound filter mismatch")
			if j > 0 {
				check(rs[j-1]["created_at"].(string) > x["created_at"].(string), "compound sort mismatch")
			}
		}
	}, true))
	first, e := invoke(m, "find", engine.Request{Query: compound})
	if e != nil {
		return e
	}
	next := compound
	next.Cursor = first.(map[string]any)["nextCursor"].(string)
	check(next.Cursor != "", "missing second-page cursor")
	appendRequired(measure("find_second_page_50", *samples, func(int) (any, error) { return invoke(m, "find", engine.Request{Query: next}) }, func(_ int, v any) {
		rs := records(v)
		check(len(rs) == 50, "second page size")
		check(records(first)[49]["created_at"].(string) > rs[0]["created_at"].(string), "overlapping pages")
	}, true))
	for name, q := range map[string]engine.Query{"compound": compound, "unique": {Where: &engine.Filter{Field: "email", Op: "eq", Value: email(*n / 2)}}} {
		v, e := invoke(m, "explain", engine.Request{Query: q})
		if e != nil {
			return e
		}
		r.Plans[name] = v
	}
	metrics := []engine.Metric{{Name: "count", Op: "count"}, {Name: "sum", Op: "sum", Field: "amount"}, {Name: "avg", Op: "avg", Field: "amount"}}
	verifyAggregate := func(filtered bool) func(int, any) {
		expectedCount := map[string]int{}
		expectedSum := map[string]float64{}
		for i := 0; i < *n; i++ {
			if filtered && i%1000 != 3 {
				continue
			}
			status := []string{"paid", "pending", "cancelled", "draft"}[(i/1000)%4]
			expectedCount[status]++
			expectedSum[status] += float64(i%10000) / 100
		}
		return func(_ int, v any) {
			groups := v.(map[string]any)["rows"].([]engine.Record)
			check(len(groups) == len(expectedCount), "aggregate group count")
			for _, g := range groups {
				s := g["status"].(string)
				count, e := strconv.Atoi(g["count"].(string))
				check(e == nil && count == expectedCount[s], "aggregate count mismatch")
				sum := g["sum"].(float64)
				check(math.Abs(sum-expectedSum[s]) < .001, "aggregate sum mismatch: %v vs %v", sum, expectedSum[s])
				check(math.Abs(g["avg"].(float64)-expectedSum[s]/float64(count)) < .000001, "aggregate average mismatch")
			}
		}
	}
	// Correctness validation is after each timer, but benchmark loop throughput
	// includes that validation. Latencies are the primary cross-operation metric.
	filtered := engine.Request{Query: engine.Query{Where: &engine.Filter{Field: "tenant", Op: "eq", Value: "t0003"}, RequireIndex: true}, GroupBy: []string{"status"}, Metrics: metrics}
	appendRequired(measure("aggregate_one_tenant", *slowSamples, func(int) (any, error) { return invoke(m, "aggregate", filtered) }, verifyAggregate(true), true))
	r.Metrics = append(r.Metrics, measure("aggregate_all", *slowSamples, func(int) (any, error) {
		return invoke(m, "aggregate", engine.Request{GroupBy: []string{"status"}, Metrics: metrics})
	}, verifyAggregate(false), false))
	r.Metrics = append(r.Metrics, measure("count_all", *slowSamples, func(int) (any, error) { return invoke(m, "count", engine.Request{}) }, func(_ int, v any) { check(v.(map[string]any)["count"] == strconv.Itoa(*n), "count mismatch") }, false))
	r.Metrics = append(r.Metrics, measure("find_unindexed_last", *slowSamples, func(int) (any, error) {
		return invoke(m, "find", engine.Request{Query: engine.Query{Where: &engine.Filter{Field: "note", Op: "eq", Value: row(*n - 1)["note"]}, Limit: 1}})
	}, func(_ int, v any) {
		rs := records(v)
		check(len(rs) == 1 && rs[0]["id"] == id(*n-1), "unindexed find mismatch")
	}, false))
	// Eight simultaneous clients, deterministic random primary keys, no writes.
	workers := 8
	each := *samples
	good, bad := []float64{}, []float64{}
	errs := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	runtime.ReadMemStats(&before)
	parallelStart := time.Now()
	for worker := 0; worker < workers; worker++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for i := 0; i < each; i++ {
				k := readIndex(w*each + i)
				t := time.Now()
				v, e := invoke(m, "get", engine.Request{Key: engine.Record{"id": id(k)}})
				ms := float64(time.Since(t).Nanoseconds()) / 1e6
				mu.Lock()
				if e != nil {
					bad = append(bad, ms)
					errs[errorCode(e)]++
				} else {
					good = append(good, ms)
					check(v.(map[string]any)["record"].(engine.Record)["id"] == id(k), "concurrent read mismatch")
				}
				mu.Unlock()
			}
		}(worker)
	}
	wg.Wait()
	appendRequired(stats("get_concurrent_8", parallelStart, before, good, bad, errs, "", 0))
	appendRequired(measure("update_indexed_record", *samples, func(i int) (any, error) {
		return invoke(m, "update", engine.Request{Key: engine.Record{"id": id(readIndex(i))}, Set: engine.Record{"created_at": stamp(*n + 1000 + i)}})
	}, func(_ int, v any) { check(v.(map[string]any)["affected"] == 1, "update count") }, false))
	appendRequired(measure("insert_single", *samples, func(i int) (any, error) {
		return invoke(m, "insert", engine.Request{Records: []engine.Record{row(*n + i)}})
	}, func(_ int, v any) { check(v.(map[string]any)["affected"] == 1, "single insert count") }, false))
	// Restore the original cardinality before measuring late index creation.
	_, e = invoke(m, "delete", engine.Request{Query: engine.Query{Where: &engine.Filter{Field: "id", Op: "gte", Value: id(*n)}}, MaxAffected: *samples})
	if e != nil {
		return e
	}
	for i := 0; i < 3; i++ {
		name := fmt.Sprintf("late_%d", i)
		mt := measure("index_build_existing", 1, func(int) (any, error) {
			return invoke(m, "index_create", engine.Request{Index: &engine.Index{Name: name, Fields: []engine.Order{{Field: "units", Direction: "asc"}}}})
		}, nil, false)
		r.Metrics = append(r.Metrics, mt)
		if mt.Successes > 0 {
			_, e = invoke(m, "index_drop", engine.Request{Name: name, Confirm: true})
			if e != nil {
				return e
			}
		}
	}
	r.LiveDiskMiB = disk(root)
	if e = m.Close(); e != nil {
		return e
	}
	r.ClosedDiskMiB = disk(root)
	r.PeakRSSMiB = rss()
	r.WallSeconds = time.Since(start).Seconds()
	r.CorrectnessChecks = checks
	if e = os.MkdirAll(filepath.Dir(*output), 0755); e != nil {
		return e
	}
	b, e := json.MarshalIndent(r, "", "  ")
	if e != nil {
		return e
	}
	if e = os.WriteFile(*output, append(b, '\n'), 0644); e != nil {
		return e
	}
	logPhase("complete: %.1f seconds, %.1f MiB disk, %.1f MiB peak RSS; %s", r.WallSeconds, r.ClosedDiskMiB, r.PeakRSSMiB, *output)
	return nil
}
func main() {
	flag.Parse()
	if e := run(); e != nil {
		die(e)
	}
}

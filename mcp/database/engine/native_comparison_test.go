package engine

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	native "github.com/apteva/db"
	"github.com/cockroachdb/pebble/v2"
	"github.com/valyala/fastjson"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type comparisonResult struct {
	Adapter    string    `json:"adapter"`
	Mode       string    `json:"mode"`
	Rows       int       `json:"rows"`
	Run        int       `json:"run"`
	Operation  string    `json:"operation"`
	Samples    int       `json:"samples"`
	P50US      float64   `json:"p50_us"`
	P95US      float64   `json:"p95_us"`
	Throughput float64   `json:"throughput_ops_s"`
	Times      []float64 `json:"samples_us"`
}

type nativeFixture struct {
	m         *Manager
	d         *database
	c         Collection
	get       *sql.Stmt
	native    *native.Collection
	nativeSum *native.Pipeline
	bulk      map[int]*sql.Stmt
}

func benchRow(i int) Record {
	return Record{"id": fmt.Sprintf("r%09d", i), "amount": float64(i % 1000), "note": strings.Repeat("x", 128)}
}
func benchFullRow(i int, now string) Record {
	r := benchRow(i)
	r["_created_at"] = now
	r["_updated_at"] = now
	r["_version"] = "1"
	return r
}
func checkErr(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func setupNativeFixture(t *testing.T, adapter string, n int) *nativeFixture {
	t.Helper()
	options := DefaultAptevaOptions()
	options.SyncMode = os.Getenv("NATIVE_COMPARISON_APTEVADB_SYNC_MODE")
	if os.Getenv("NATIVE_COMPARISON_CAPTURE") == "1" {
		options.Realtime = &RealtimeOptions{Enabled: true, RetentionBytes: 128 << 20, RetentionSeconds: 3600}
	}
	options, e := options.normalized()
	checkErr(t, e)
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &options})
	checkErr(t, e)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	call(t, m, "bench", "database_create", Request{Database: "bench", Adapter: adapter})
	call(t, m, "bench", "collection_create", Request{Database: "bench", Collection: "records", Fields: []Field{{Name: "id", Type: "text"}, {Name: "amount", Type: "number"}, {Name: "note", Type: "text"}}, PrimaryKey: []string{"id"}})
	for start := 0; start < n; start += 1000 {
		rs := make([]Record, 0, 1000)
		for i := start; i < start+1000 && i < n; i++ {
			rs = append(rs, benchRow(i))
		}
		call(t, m, "bench", "insert", Request{Database: "bench", Collection: "records", Records: rs})
	}
	d, e := m.resolve(context.Background(), "bench", "bench", "", false)
	checkErr(t, e)
	f := &nativeFixture{m: m, d: d, bulk: map[int]*sql.Stmt{}}
	checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error { var e error; f.c, e = tx.collection("records"); return e }))
	if b, ok := d.backend.(*sqliteBackend); ok {
		// Reserve/check both writer connections. Verify the primitive and
		// synchronous profile actually used by app and direct timed writes.
		full := os.Getenv("NATIVE_COMPARISON_SQLITE_FULLFSYNC") == "1"
		expectedFull := 0
		if full {
			expectedFull = 1
		}
		var conns []*sql.Conn
		for i := 0; i < 2; i++ {
			conn, err := b.db.Conn(context.Background())
			checkErr(t, err)
			conns = append(conns, conn)
			if full {
				_, err = conn.ExecContext(context.Background(), "PRAGMA fullfsync=ON")
				checkErr(t, err)
			}
			var synchronous, fsync int
			checkErr(t, conn.QueryRowContext(context.Background(), "PRAGMA synchronous").Scan(&synchronous))
			checkErr(t, conn.QueryRowContext(context.Background(), "PRAGMA fullfsync").Scan(&fsync))
			if synchronous != 2 || fsync != expectedFull {
				t.Fatalf("SQLite writer%d profile: synchronous=%d fullfsync=%d", i, synchronous, fsync)
			}
			t.Logf("SQLite writer%d verified synchronous=%d fullfsync=%d", i, synchronous, fsync)
		}
		for _, conn := range conns {
			checkErr(t, conn.Close())
		}

		f.get, e = b.readDB.Prepare("SELECT " + sqlColumns(allFields(f.c)) + " FROM c_records WHERE id=?")
		checkErr(t, e)
		t.Cleanup(func() {
			checkErr(t, f.get.Close())
			for _, s := range f.bulk {
				checkErr(t, s.Close())
			}
		})
		for _, size := range []int{1} {
			qs := "(" + strings.TrimSuffix(strings.Repeat("?,", len(allFields(f.c))), ",") + ")"
			f.bulk[size], e = b.db.Prepare("INSERT INTO c_records (" + sqlColumns(allFields(f.c)) + ") VALUES " + strings.TrimSuffix(strings.Repeat(qs+",", size), ","))
			checkErr(t, e)
		}
	}
	if b, ok := d.backend.(*aptevaBackend); ok {
		f.native, e = b.handle(f.c)
		checkErr(t, e)
		f.nativeSum, e = native.ParsePipeline([]byte(`[{"$group":{"_id":null,"total":{"$sum":"$amount"}}}]`))
		checkErr(t, e)
		f.nativeSum.SetLimits(native.Limits{MaxBytes: MaxQueryWorkBytes})
	}
	return f
}
func (f *nativeFixture) nativeGet(t *testing.T, i int) any {
	r := benchRow(i)
	switch b := f.d.backend.(type) {
	case *sqliteBackend:
		row, found, e := scanRecord(f.get.QueryRow(r["id"]), allFields(f.c))
		checkErr(t, e)
		return map[string]any{"found": found, "record": row}
	case *aptevaBackend:
		_ = b
		key, _, e := indexTuple(f.c, primaryIndex(f.c), r)
		checkErr(t, e)
		data, found, e := f.native.Find(key, nil)
		checkErr(t, e)
		var row Record
		if found {
			row, e = decodeNativeRecord(f.c, data)
			checkErr(t, e)
		}
		return map[string]any{"found": found, "record": row}
	case *pebbleBackend:
		pk, e := primaryNormalized(f.c, r)
		checkErr(t, e)
		val, closer, e := b.db.Get([]byte("r/records/" + pk))
		checkErr(t, e)
		var row Record
		e = decode(val, &row)
		checkErr(t, closer.Close())
		checkErr(t, e)
		return map[string]any{"found": true, "record": row}
	}
	panic("unknown backend")
}
func (f *nativeFixture) nativeScan(t *testing.T, sum bool) float64 {
	switch b := f.d.backend.(type) {
	case *sqliteBackend:
		q := "SELECT count(*) FROM c_records"
		if sum {
			q = "SELECT sum(amount) FROM c_records"
		}
		var v float64
		checkErr(t, b.readDB.QueryRow(q).Scan(&v))
		return v
	case *aptevaBackend:
		_ = b
		if sum {
			rows, e := f.native.RunParallel(context.Background(), f.nativeSum, b.options.AggregateWorkers)
			checkErr(t, e)
			return number(rows[0]["total"])
		}
		var v float64
		checkErr(t, f.native.Scan(nil, nil, func(_, _ []byte) bool {
			v++
			return true
		}))
		return v
	case *pebbleBackend:
		snap := b.db.NewSnapshot()
		defer snap.Close()
		prefix := []byte("r/records/")
		it, e := snap.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
		checkErr(t, e)
		defer it.Close()
		var v float64
		var parser fastjson.Parser
		for it.First(); it.Valid(); it.Next() {
			if sum {
				r, err := parser.ParseBytes(it.Value())
				checkErr(t, err)
				v += r.GetFloat64("amount")
			} else {
				v++
			}
		}
		checkErr(t, it.Error())
		return v
	}
	panic("unknown backend")
}
func (f *nativeFixture) nativeInsert(t *testing.T, rs []Record) any {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	switch b := f.d.backend.(type) {
	case *sqliteBackend:
		tx, e := b.db.Begin()
		checkErr(t, e)
		defer tx.Rollback()
		s := tx.Stmt(f.bulk[1])
		for _, r := range rs {
			_, e = s.Exec(r["id"], r["amount"], r["note"], now, now, int64(1))
			checkErr(t, e)
		}
		checkErr(t, s.Close())
		checkErr(t, tx.Commit())
	case *aptevaBackend:
		batch := b.db.NewBatch().With(f.native)
		for _, input := range rs {
			r := Record{}
			for k, v := range input {
				r[k] = v
			}
			r["_created_at"] = now
			r["_updated_at"] = now
			r["_version"] = "1"
			key, _, e := indexTuple(f.c, primaryIndex(f.c), r)
			checkErr(t, e)
			data, e := encodeNativeRecord(f.c, r)
			checkErr(t, e)
			checkErr(t, batch.Insert(key, data))
		}
		checkErr(t, batch.Commit())
	case *pebbleBackend:
		batch := b.db.NewIndexedBatch()
		defer batch.Close()
		for _, input := range rs {
			r := Record{}
			for k, v := range input {
				r[k] = v
			}
			r["_created_at"] = now
			r["_updated_at"] = now
			r["_version"] = "1"
			pk, e := primaryNormalized(f.c, r)
			checkErr(t, e)
			rkey := []byte("r/records/" + pk)
			_, closer, e := batch.Get(rkey)
			if e == nil {
				closer.Close()
				t.Fatal("duplicate record")
			}
			if e != pebble.ErrNotFound {
				checkErr(t, e)
			}
			tuple, _, e := indexTuple(f.c, primaryIndex(f.c), r)
			checkErr(t, e)
			ik := append(indexPrefix(f.c, primaryIndex(f.c), false), tuple...)
			ik = append(ik, tuple...)
			uk := append(indexPrefix(f.c, primaryIndex(f.c), true), tuple...)
			_, closer, e = batch.Get(uk)
			if e == nil {
				closer.Close()
				t.Fatal("duplicate primary index")
			}
			if e != pebble.ErrNotFound {
				checkErr(t, e)
			}
			checkErr(t, batch.Set(ik, []byte(pk), nil))
			checkErr(t, batch.Set(uk, []byte(pk), nil))
			checkErr(t, batch.Set(rkey, marshal(r), nil))
		}
		checkErr(t, batch.Commit(pebble.Sync))
	}
	keys := make([]Record, len(rs))
	for i, r := range rs {
		keys[i] = Record{"id": r["id"]}
	}
	return map[string]any{"affected": len(rs), "keys": keys}
}
func measureNative(t *testing.T, adapter, mode, op string, n, run, samples int, fn func(int) any, validate func(any, int)) comparisonResult {
	times := make([]float64, 0, samples)
	for i := 0; i < samples; i++ {
		start := time.Now()
		v := fn(i)
		_, e := json.Marshal(v)
		checkErr(t, e)
		us := float64(time.Since(start).Nanoseconds()) / 1000
		times = append(times, us)
		validate(v, i)
	}
	sorted := append([]float64(nil), times...)
	sort.Float64s(sorted)
	var total float64
	for _, v := range times {
		total += v
	}
	r := comparisonResult{Adapter: adapter, Mode: mode, Rows: n, Run: run, Operation: op, Samples: samples, P50US: sorted[len(sorted)/2], P95US: sorted[int(float64(len(sorted)-1)*.95)], Throughput: float64(samples) * 1e6 / total, Times: times}
	t.Logf("%s %s %d run%d %s p50 %.1fus p95 %.1fus", adapter, mode, n, run, op, r.P50US, r.P95US)
	return r
}
func TestNativeDriverComparison(t *testing.T) {
	path := os.Getenv("NATIVE_COMPARISON_OUTPUT")
	if path == "" {
		t.Skip("set NATIVE_COMPARISON_OUTPUT")
	}
	var results []comparisonResult
	rowCounts := []int{10000, 100000}
	if raw := os.Getenv("NATIVE_COMPARISON_ROWS"); raw != "" {
		rowCounts = nil
		for _, item := range strings.Split(raw, ",") {
			n, err := strconv.Atoi(item)
			checkErr(t, err)
			if n < 1000 || n%1000 != 0 {
				t.Fatal("native comparison rows must be positive multiples of 1000")
			}
			rowCounts = append(rowCounts, n)
		}
	}
	baseAdapters := []string{"sqlite", "pebble", "aptevadb"}
	if raw := os.Getenv("NATIVE_COMPARISON_ADAPTERS"); raw != "" {
		baseAdapters = nil
		seen := map[string]bool{}
		for _, adapter := range strings.Split(raw, ",") {
			if (adapter != "sqlite" && adapter != "pebble" && adapter != "aptevadb") || seen[adapter] {
				t.Fatal("invalid or duplicate comparison adapter", adapter)
			}
			seen[adapter] = true
			baseAdapters = append(baseAdapters, adapter)
		}
	}
	for _, n := range rowCounts {
		for run := 1; run <= 3; run++ {
			adapters := append([]string(nil), baseAdapters...)
			rotation := (run - 1) % len(adapters)
			adapters = append(adapters[rotation:], adapters[:rotation]...)
			for _, adapter := range adapters {
				modes := []string{"app_engine", "native_driver"}
				if run == 2 {
					modes = []string{"native_driver", "app_engine"}
				}
				for _, mode := range modes {
					t.Run(fmt.Sprintf("%s_%s_%d_run%d", adapter, mode, n, run), func(t *testing.T) {
						f := setupNativeFixture(t, adapter, n)
						exe := func(op string, r Request) any {
							r.Database = "bench"
							r.Collection = "records"
							return call(t, f.m, "bench", op, r)
						}
						get := func(i int) any { return f.nativeGet(t, i*7919%n) }
						if mode == "app_engine" {
							get = func(i int) any { return exe("get", Request{Key: Record{"id": benchRow(i * 7919 % n)["id"]}}) }
						}
						// Warm both paths before read timing.
						for i := 0; i < 100; i++ {
							get(i)
						}
						results = append(results, measureNative(t, adapter, mode, "get", n, run, 1000, get, func(v any, i int) {
							r := v.(map[string]any)["record"].(Record)
							if r["id"] != benchRow(i * 7919 % n)["id"] || number(r["amount"]) != float64((i*7919%n)%1000) {
								t.Fatal("get validation")
							}
						}))
						for _, op := range []string{"count", "sum"} {
							expected := float64(n)
							if op == "sum" {
								expected = float64(n/1000) * 499500
							}
							fn := func(int) any { return f.nativeScan(t, op == "sum") }
							if mode == "app_engine" {
								fn = func(int) any {
									if op == "count" {
										v := exe("count", Request{}).(map[string]any)
										var x float64
										fmt.Sscan(v["count"].(string), &x)
										return x
									}
									v := exe("aggregate", Request{Metrics: []Metric{{Name: "total", Op: "sum", Field: "amount"}}}).(map[string]any)
									return number(v["rows"].([]Record)[0]["total"])
								}
							}
							fn(0)
							results = append(results, measureNative(t, adapter, mode, op, n, run, 15, fn, func(v any, _ int) {
								if v.(float64) != expected {
									t.Fatalf("%s validation %.0f expected %.0f", op, v, expected)
								}
							}))
						}
						for _, size := range []int{1, 1000} {
							samples := 100
							if size == 1000 {
								samples = 20
							}
							inputs := make([][]Record, samples)
							for i := range inputs {
								for j := 0; j < size; j++ {
									inputs[i] = append(inputs[i], benchRow(n+1000000*size+i*size+j))
								}
							}
							fn := func(i int) any { return f.nativeInsert(t, inputs[i]) }
							if mode == "app_engine" {
								fn = func(i int) any { return exe("insert", Request{Records: inputs[i]}) }
							}
							results = append(results, measureNative(t, adapter, mode, fmt.Sprintf("insert_%d", size), n, run, samples, fn, func(value any, i int) {
								result := value.(map[string]any)
								keys := result["keys"].([]Record)
								if result["affected"] != size || len(keys) != size {
									t.Fatal("write response count", result)
								}
								for j, key := range keys {
									if key["id"] != inputs[i][j]["id"] {
										t.Fatal("write response key", key)
									}
								}
							}))
							for i := range inputs {
								for _, j := range []int{0, size - 1} {
									id := inputs[i][j]["id"].(string)
									v := exe("get", Request{Key: Record{"id": id}}).(map[string]any)
									if v["found"] != true || v["record"].(Record)["note"] != strings.Repeat("x", 128) {
										t.Fatal("write verification")
									}
								}
							}
						}
						if got := f.nativeScan(t, false); got != float64(n+100+20000) {
							t.Fatalf("post-write count %v", got)
						}
					})
				}
			}
		}
	}
	options := DefaultAptevaOptions()
	options.SyncMode = os.Getenv("NATIVE_COMPARISON_APTEVADB_SYNC_MODE")
	if os.Getenv("NATIVE_COMPARISON_CAPTURE") == "1" {
		options.Realtime = &RealtimeOptions{Enabled: true, RetentionBytes: 128 << 20, RetentionSeconds: 3600}
	}
	options, e := options.normalized()
	checkErr(t, e)
	nativeDurability := map[string]string{"full": "Sync=true SyncFull (macOS fullfsync)", "os": "Sync=true SyncOS (ordinary fsync)", "barrier": "Sync=true SyncBarrier (macOS barrierfsync)"}[options.SyncMode]
	sqliteDurability := "WAL synchronous=FULL; fullfsync=OFF"
	if os.Getenv("NATIVE_COMPARISON_SQLITE_FULLFSYNC") == "1" {
		sqliteDurability = "WAL synchronous=FULL; fullfsync=ON (diagnostic, both writer connections)"
	}
	b, e := json.MarshalIndent(map[string]any{"version": "0.4.4-dev", "base_commit": "a29c1263e3d4f8ac52ae6afe859f72812cd186c4", "source_label": os.Getenv("NATIVE_COMPARISON_LABEL"), "capture": os.Getenv("NATIVE_COMPARISON_CAPTURE") == "1", "adapters": baseAdapters, "go_version": runtime.Version(), "aptevadb_version": "v" + native.Version, "durability": map[string]string{"sqlite": sqliteDurability, "pebble": "pebble.Sync (macOS fullfsync)", "aptevadb": nativeDurability}, "results": results}, "", "  ")
	checkErr(t, e)
	checkErr(t, os.MkdirAll(filepath.Dir(path), 0755))
	checkErr(t, os.WriteFile(path, b, 0644))
}

func BenchmarkPointRead(b *testing.B) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		b.Run(adapter, func(b *testing.B) {
			m, e := Open(b.TempDir())
			if e != nil {
				b.Fatal(e)
			}
			defer m.Close()
			for _, x := range []struct {
				op string
				r  Request
			}{
				{"database_create", Request{Database: "bench", Adapter: adapter}},
				{"collection_create", Request{Database: "bench", Collection: "records", Fields: []Field{{Name: "id", Type: "text"}, {Name: "amount", Type: "number"}, {Name: "note", Type: "text"}}}},
				{"insert", Request{Database: "bench", Collection: "records", Records: []Record{benchRow(0)}}},
			} {
				if _, e := m.Execute(context.Background(), "bench", x.op, x.r); e != nil {
					b.Fatal(e)
				}
			}
			r := Request{Database: "bench", Collection: "records", Key: Record{"id": benchRow(0)["id"]}}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				v, e := m.Execute(context.Background(), "bench", "get", r)
				if e != nil {
					b.Fatal(e)
				}
				if _, e = json.Marshal(v); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}

// Pair each app read with its direct-driver read on the same warmed fixture.
// This bounds cache and background-load differences between the two timings.
func TestPairedPointDriverComparison(t *testing.T) {
	path := os.Getenv("PAIRED_POINT_COMPARISON_OUTPUT")
	if path == "" {
		t.Skip("set PAIRED_POINT_COMPARISON_OUTPUT")
	}
	var results []comparisonResult
	for run := 1; run <= 3; run++ {
		adapters := []string{"sqlite", "pebble", "aptevadb"}
		rotation := (run - 1) % len(adapters)
		adapters = append(adapters[rotation:], adapters[:rotation]...)
		for _, adapter := range adapters {
			t.Run(fmt.Sprintf("%s_run%d", adapter, run), func(t *testing.T) {
				f := setupNativeFixture(t, adapter, 100000)
				app := func(i int) any {
					return call(t, f.m, "bench", "get", Request{Database: "bench", Collection: "records", Key: Record{"id": benchRow(i * 7919 % 100000)["id"]}})
				}
				direct := func(i int) any { return f.nativeGet(t, i*7919%100000) }
				for i := 0; i < 1000; i++ {
					app(i)
					direct(i)
				}
				all := map[string][]float64{"app_engine": {}, "native_driver": {}}
				for i := 0; i < 2000; i++ {
					order := []string{"app_engine", "native_driver"}
					if i%2 == 1 {
						order[0], order[1] = order[1], order[0]
					}
					var a, b []byte
					for _, mode := range order {
						start := time.Now()
						fn := direct
						if mode == "app_engine" {
							fn = app
						}
						v := fn(i)
						encoded, e := json.Marshal(v)
						elapsed := float64(time.Since(start).Nanoseconds()) / 1000
						checkErr(t, e)
						all[mode] = append(all[mode], elapsed)
						if mode == "app_engine" {
							a = encoded
						} else {
							b = encoded
						}
					}
					if string(a) != string(b) {
						t.Fatal("paired responses differ")
					}
				}
				for _, mode := range []string{"app_engine", "native_driver"} {
					times := all[mode]
					sorted := append([]float64(nil), times...)
					sort.Float64s(sorted)
					var total float64
					for _, us := range times {
						total += us
					}
					r := comparisonResult{Adapter: adapter, Mode: mode, Rows: 100000, Run: run, Operation: "get", Samples: len(times), P50US: sorted[len(sorted)/2], P95US: sorted[int(float64(len(sorted)-1)*.95)], Throughput: float64(len(times)) * 1e6 / total, Times: times}
					results = append(results, r)
					t.Logf("%s %s run%d paired get p50 %.2fus p95 %.2fus", adapter, mode, run, r.P50US, r.P95US)
				}
			})
		}
	}
	b, e := json.MarshalIndent(map[string]any{"rows": 100000, "paired": true, "go_version": runtime.Version(), "results": results}, "", "  ")
	checkErr(t, e)
	checkErr(t, os.WriteFile(path, b, 0644))
}

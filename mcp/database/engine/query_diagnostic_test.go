//go:build aggregatebench

package engine

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	native "github.com/apteva/db"
	"github.com/cockroachdb/pebble/v2"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
)

type diagnosticSample struct {
	Adapter     string    `json:"adapter"`
	Run         int       `json:"run"`
	Operation   string    `json:"operation"`
	Path        string    `json:"path"`
	P50US       float64   `json:"p50_us"`
	P95US       float64   `json:"p95_us"`
	BytesPerOp  uint64    `json:"allocated_bytes_per_op"`
	AllocsPerOp uint64    `json:"allocations_per_op"`
	Times       []float64 `json:"times_us"`
}
type diagnosticFixture struct {
	f          *nativeFixture
	request    Request
	filter     []byte
	opts       native.QueryOptions
	pipeline   *native.Pipeline
	ix         Index
	prefix     []byte
	statements map[string]*sql.Stmt
}

func setupDiagnostic(t *testing.T, adapter string, n int) *diagnosticFixture {
	t.Helper()
	o := DefaultAptevaOptions()
	if value := os.Getenv("DB_QUERY_DIAGNOSTIC_CACHE_BYTES"); value != "" {
		n, err := strconv.ParseInt(value, 10, 64)
		checkErr(t, err)
		o.CacheBytes = n
	}
	o.SyncMode = "os" // read-only measurements; never time or compare loading writes.
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o})
	checkErr(t, e)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	call(t, m, "bench", "database_create", Request{Database: "bench", Adapter: adapter})
	call(t, m, "bench", "collection_create", Request{Database: "bench", Collection: "records", Fields: []Field{{Name: "id", Type: "text"}, {Name: "bucket", Type: "text"}, {Name: "rank", Type: "integer"}, {Name: "amount", Type: "number"}, {Name: "note", Type: "text"}}})
	ix := Index{Name: "by_bucket_rank", Fields: []Order{{Field: "bucket"}, {Field: "rank"}}}
	call(t, m, "bench", "index_create", Request{Database: "bench", Collection: "records", Index: &ix})
	for start := 0; start < n; start += 1000 {
		rs := make([]Record, 1000)
		for j := range rs {
			i := start + j
			rs[j] = benchRow(i)
			rs[j]["bucket"] = fmt.Sprintf("b%02d", i%20)
			rs[j]["rank"] = strconv.Itoa(i)
		}
		call(t, m, "bench", "insert", Request{Database: "bench", Collection: "records", Records: rs})
	}
	d, e := m.resolve(context.Background(), "bench", "bench", "", false)
	checkErr(t, e)
	f := &nativeFixture{m: m, d: d}
	checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error { var err error; f.c, err = tx.collection("records"); return err }))
	r := Request{Database: "bench", Collection: "records", Query: Query{Where: &Filter{Field: "bucket", Op: "eq", Value: "b00"}, OrderBy: []Order{{Field: "rank"}}, Limit: 50}}
	checkErr(t, validateQuery(f.c, &r.Query))
	x := &diagnosticFixture{f: f, request: r, ix: ix, statements: map[string]*sql.Stmt{}}
	prefixIndex := ix
	prefixIndex.Fields = prefixIndex.Fields[:1]
	tuple, _, e := indexTuple(f.c, prefixIndex, Record{"bucket": "b00"})
	checkErr(t, e)
	x.prefix = append(indexPrefix(f.c, ix, false), tuple...)
	x.filter = marshal(nativeFilter(f.c, r.Where))
	x.opts = nativeQueryOptions(f.c, r.Query, 51)
	x.pipeline, e = native.ParsePipeline([]byte(`[{"$group":{"_id":null,"count":{"$count":{}}}}]`))
	checkErr(t, e)
	if b, ok := d.backend.(*aptevaBackend); ok {
		f.native, e = b.handle(f.c)
		checkErr(t, e)
	}
	if b, ok := d.backend.(*sqliteBackend); ok {
		f.get, e = b.readDB.Prepare("SELECT " + sqlColumns(allFields(f.c)) + " FROM c_records WHERE id=?")
		checkErr(t, e)
		t.Cleanup(func() { f.get.Close() })
		for name, query := range map[string]string{
			"page":        "SELECT " + sqlColumns(allFields(f.c)) + " FROM c_records WHERE bucket IS NOT NULL AND bucket=? ORDER BY rank,id LIMIT 51",
			"count":       "SELECT COUNT(*) FROM c_records WHERE bucket IS NOT NULL AND bucket=?",
			"count_fetch": "SELECT COUNT(note) FROM c_records WHERE bucket IS NOT NULL AND bucket=?",
		} {
			x.statements[name], e = b.readDB.Prepare(query)
			checkErr(t, e)
		}
		t.Cleanup(func() {
			for _, s := range x.statements {
				s.Close()
			}
		})
	}
	return x
}
func (x *diagnosticFixture) app(t *testing.T, op string) any {
	r := x.request
	if op == "get" {
		r = Request{Database: "bench", Collection: "records", Key: Record{"id": "r000079180"}}
	}
	if op == "count" {
		r.OrderBy = nil
		r.Limit = 0
	}
	if op == "page" {
		op = "find"
	}
	v, e := x.f.m.Execute(context.Background(), "bench", op, r)
	if e != nil {
		t.Fatal(e)
	}
	return v
}
func (x *diagnosticFixture) directGet(t *testing.T) any {
	var row Record
	var found bool
	var e error
	switch b := x.f.d.backend.(type) {
	case *sqliteBackend:
		_ = b
		row, found, e = scanRecord(x.f.get.QueryRow("r000079180"), allFields(x.f.c))
	case *aptevaBackend:
		_ = b
		key, _, err := indexTuple(x.f.c, primaryIndex(x.f.c), Record{"id": "r000079180"})
		if err != nil {
			t.Fatal(err)
		}
		var data []byte
		data, found, e = x.f.native.Find(key, nil)
		if e == nil && found {
			row, e = decodeNativeRecord(x.f.c, data)
		}
	case *pebbleBackend:
		data, closer, err := b.db.Get([]byte(`r/records/["r000079180"]`))
		if err != nil {
			t.Fatal(err)
		}
		found = true
		e = decode(data, &row)
		closer.Close()
	}
	if e != nil {
		t.Fatal(e)
	}
	return map[string]any{"record": row, "found": found}
}
func (x *diagnosticFixture) directPage(t *testing.T) any {
	var records []Record
	switch b := x.f.d.backend.(type) {
	case *sqliteBackend:
		_ = b
		rows, e := x.statements["page"].Query("b00")
		checkErr(t, e)
		defer rows.Close()
		for rows.Next() {
			r, _, e := scanRecord(rows, allFields(x.f.c))
			if e != nil {
				t.Fatal(e)
			}
			records = append(records, r)
		}
		checkErr(t, rows.Err())
	case *aptevaBackend:
		_ = b
		checkErr(t, x.f.native.Query(context.Background(), x.filter, x.opts, func(_, doc []byte) bool {
			r, e := decodeNativeRecord(x.f.c, doc)
			if e != nil {
				t.Fatal(e)
			}
			records = append(records, r)
			return true
		}))
	case *pebbleBackend:
		snap := b.db.NewSnapshot()
		defer snap.Close()
		it, e := snap.NewIter(&pebble.IterOptions{LowerBound: x.prefix, UpperBound: prefixEnd(x.prefix)})
		checkErr(t, e)
		defer it.Close()
		decoder := newScanDecoder(x.f.c, nil, nil)
		for it.First(); it.Valid() && len(records) < 51; it.Next() {
			data, closer, e := snap.Get(append([]byte("r/records/"), it.Value()...))
			checkErr(t, e)
			r, e := decoder.decode(data)
			if e != nil {
				t.Fatal(e)
			}
			records = append(records, maps.Clone(r))
			closer.Close()
		}
		checkErr(t, it.Error())
	}
	out, e := page(x.f.c, x.request.Query, records, x.f.d.key)
	checkErr(t, e)
	return out
}
func (x *diagnosticFixture) nativeCount(t *testing.T, path string) any {
	b, ok := x.f.d.backend.(*aptevaBackend)
	if ok {
		snapshot, e := b.db.Snapshot()
		checkErr(t, e)
		defer snapshot.Close()
		h, e := snapshot.Collection(nativeCollectionName(x.f.c.Name))
		checkErr(t, e)
		if path == "index_cursor_fetch" {
			cursor, e := h.NewIndexCursor(context.Background(), "by_bucket_rank", native.IndexCursorOptions{Prefix: []any{"b00"}})
			checkErr(t, e)
			defer cursor.Close()
			count := 0
			for cursor.First(); cursor.Valid(); cursor.Next() {
				count++
			}
			checkErr(t, cursor.Err())
			return map[string]any{"count": strconv.Itoa(count)}
		}
		opts := nativeQueryOptions(x.f.c, Query{Where: x.request.Where}, 0)
		filter := x.filter
		if path == "query_simple_filter" {
			filter = []byte(`{"bucket":"b00"}`)
		}
		count := 0
		if path == "pipeline" {
			n, err := h.CountDocuments(context.Background(), filter, native.CountOptions{Hint: "by_bucket_rank", MaxMemoryBytes: MaxQueryWorkBytes})
			checkErr(t, err)
			count = int(n)
		} else if path == "legacy_pipeline" {
			values, e := x.pipeline.Run(context.Background(), func(yield func([]byte, []byte) bool) error { return h.Query(context.Background(), filter, opts, yield) })
			checkErr(t, e)
			count = int(values[0]["count"].(int64))
		} else {
			checkErr(t, h.Query(context.Background(), filter, opts, func(_, doc []byte) bool { count++; return true }))
		}
		return map[string]any{"count": strconv.Itoa(count)}
	}
	if b, ok := x.f.d.backend.(*sqliteBackend); ok {
		_ = b
		name := "count"
		if path == "count_fetch" {
			name = path
		}
		var count int
		checkErr(t, x.statements[name].QueryRow("b00").Scan(&count))
		return map[string]any{"count": strconv.Itoa(count)}
	}
	bpb := x.f.d.backend.(*pebbleBackend)
	snap := bpb.db.NewSnapshot()
	defer snap.Close()
	it, e := snap.NewIter(&pebble.IterOptions{LowerBound: x.prefix, UpperBound: prefixEnd(x.prefix)})
	checkErr(t, e)
	defer it.Close()
	count := 0
	decoder := newScanDecoder(x.f.c, []string{}, x.request.Where)
	if path == "index_iterator_filter" {
		prefix := []byte("r/records/")
		records, err := snap.NewIter(&pebble.IterOptions{LowerBound: prefix, UpperBound: prefixEnd(prefix)})
		checkErr(t, err)
		defer records.Close()
		key := append([]byte(nil), prefix...)
		for it.First(); it.Valid(); it.Next() {
			key = append(key[:len(prefix)], it.Value()...)
			if !records.SeekGE(key) || !bytes.Equal(records.Key(), key) {
				t.Fatal("missing indexed record", records.Error())
			}
			r, err := decoder.decode(records.Value())
			if err != nil {
				t.Fatal(err)
			}
			if matches(x.f.c, r, x.request.Where) {
				count++
			}
		}
		checkErr(t, it.Error())
		checkErr(t, records.Error())
		return map[string]any{"count": strconv.Itoa(count)}
	}
	for it.First(); it.Valid(); it.Next() {
		if path != "index_only" {
			doc, closer, e := snap.Get(append([]byte("r/records/"), it.Value()...))
			if e != nil {
				t.Fatal(e)
			}
			if path == "index_fetch_filter" {
				r, e := decoder.decode(doc)
				if e != nil {
					t.Fatal(e)
				}
				if !matches(x.f.c, r, x.request.Where) {
					closer.Close()
					continue
				}
			}
			closer.Close()
		}
		count++
	}
	checkErr(t, it.Error())
	return map[string]any{"count": strconv.Itoa(count)}
}
func measureDiagnostic(t *testing.T, adapter string, run int, op, path string, samples int, fn func() any) diagnosticSample {
	for i := 0; i < 10; i++ {
		fn()
	}
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	times := make([]float64, samples)
	for i := range times {
		start := time.Now()
		v := fn()
		data, e := json.Marshal(v)
		times[i] = float64(time.Since(start).Nanoseconds()) / 1000
		if e != nil {
			t.Fatal(e)
		}
		if op == "count" {
			if string(data) != `{"count":"5000"}` {
				t.Fatal("wrong count", string(data))
			}
		}
		if op == "page" {
			rows := v.(map[string]any)["records"].([]Record)
			if len(rows) != 50 || rows[0]["id"] != "r000000000" || rows[49]["id"] != "r000000980" {
				t.Fatal("wrong page", len(rows))
			}
		}
		if op == "get" {
			r := v.(map[string]any)["record"].(Record)
			if r["id"] != "r000079180" {
				t.Fatal("wrong get", r)
			}
		}
	}
	runtime.ReadMemStats(&after)
	sorted := append([]float64(nil), times...)
	sort.Float64s(sorted)
	return diagnosticSample{adapter, run, op, path, sorted[len(sorted)/2], sorted[min(len(sorted)-1, int(float64(len(sorted))*.95))], (after.TotalAlloc - before.TotalAlloc) / uint64(samples), (after.Mallocs - before.Mallocs) / uint64(samples), times}
}
func TestIndexedQueryDiagnostic(t *testing.T) {
	output := os.Getenv("DB_QUERY_DIAGNOSTIC_OUTPUT")
	if output == "" {
		t.Skip("set DB_QUERY_DIAGNOSTIC_OUTPUT")
	}
	profile := os.Getenv("DB_QUERY_DIAGNOSTIC_PROFILE")
	var results []diagnosticSample
	plans := map[string]any{}
	repeats := 3
	if profile != "" {
		repeats = 1
	}
	for run := 1; run <= repeats; run++ {
		adapters := []string{"aptevadb", "sqlite", "pebble"}
		if run == 2 {
			adapters = []string{"pebble", "aptevadb", "sqlite"}
		}
		if run == 3 {
			adapters = []string{"sqlite", "pebble", "aptevadb"}
		}
		if profile != "" {
			adapters = []string{"aptevadb"}
		}
		for _, adapter := range adapters {
			if only := os.Getenv("DB_QUERY_DIAGNOSTIC_ADAPTER"); only != "" && adapter != only {
				continue
			}
			t.Run(fmt.Sprintf("%s_run%d", adapter, run), func(t *testing.T) {
				x := setupDiagnostic(t, adapter, 100000)
				if profile != "" {
					checkErr(t, os.MkdirAll(profile, 0755))
					for _, path := range []string{"index_cursor_fetch", "query", "pipeline", "adapter"} {
						file, e := os.Create(filepath.Join(profile, path+".cpu"))
						checkErr(t, e)
						checkErr(t, pprof.StartCPUProfile(file))
						start := time.Now()
						iterations := 0
						for time.Since(start) < 2*time.Second {
							if path == "adapter" {
								x.app(t, "count")
							} else {
								x.nativeCount(t, path)
							}
							iterations++
						}
						pprof.StopCPUProfile()
						file.Close()
						t.Log(path, iterations)
					}
					return
				}
				if adapter == "aptevadb" {
					var stats native.QueryStats
					n, err := x.f.native.CountDocuments(context.Background(), x.filter, native.CountOptions{Stats: &stats, MaxMemoryBytes: MaxQueryWorkBytes})
					checkErr(t, err)
					if n != 5000 || !stats.Plan.IndexOnly || stats.DocumentFetches != 0 {
						t.Fatal(n, stats)
					}
					plans[adapter] = stats
				}
				if adapter == "sqlite" {
					b := x.f.d.backend.(*sqliteBackend)
					queries := map[string]string{"count": "SELECT COUNT(*) FROM c_records WHERE bucket IS NOT NULL AND bucket='b00'", "page": "SELECT " + sqlColumns(allFields(x.f.c)) + " FROM c_records WHERE bucket IS NOT NULL AND bucket='b00' ORDER BY rank,id LIMIT 51", "count_fetch": "SELECT COUNT(note) FROM c_records WHERE bucket='b00'"}
					info := map[string][]string{}
					for name, q := range queries {
						rows, e := b.readDB.Query("EXPLAIN QUERY PLAN " + q)
						checkErr(t, e)
						for rows.Next() {
							var a, b, c int
							var detail string
							checkErr(t, rows.Scan(&a, &b, &c, &detail))
							info[name] = append(info[name], detail)
						}
						rows.Close()
					}
					plans[adapter] = info
				}
				for _, op := range []string{"get", "page", "count"} {
					samples := 1000
					if op == "page" {
						samples = 100
					}
					if op == "count" {
						samples = 60
					}
					paths := []string{"adapter", "direct"}
					if run == 2 {
						paths = []string{"direct", "adapter"}
					}
					for _, path := range paths {
						fn := func() any { return x.app(t, op) }
						if path == "direct" {
							switch op {
							case "get":
								fn = func() any { return x.directGet(t) }
							case "page":
								fn = func() any { return x.directPage(t) }
							case "count":
								fn = func() any {
									path := "pipeline"
									if adapter == "pebble" {
										path = "index_iterator_filter"
									}
									return x.nativeCount(t, path)
								}
							}
						}
						results = append(results, measureDiagnostic(t, adapter, run, op, path, samples, fn))
					}
				}
				extra := []string{}
				switch adapter {
				case "aptevadb":
					extra = []string{"legacy_pipeline", "index_cursor_fetch", "query", "query_simple_filter"}
				case "sqlite":
					extra = []string{"count_fetch"}
				case "pebble":
					extra = []string{"index_only", "index_fetch"}
				}
				for _, path := range extra {
					results = append(results, measureDiagnostic(t, adapter, run, "count", path, 60, func() any { return x.nativeCount(t, path) }))
				}
			})
		}
	}
	data, e := json.MarshalIndent(map[string]any{"passed": !t.Failed(), "rows": 100000, "matches": 5000, "index": "bucket ASC, rank ASC (+ primary-key suffix)", "record_note_bytes": len(strings.Repeat("x", 128)), "capture": false, "version": "Database 0.5.1 / AptevaDB " + native.Version, "go": runtime.Version(), "plans": plans, "results": results}, "", "  ")
	checkErr(t, e)
	checkErr(t, os.WriteFile(output, data, 0644))
}

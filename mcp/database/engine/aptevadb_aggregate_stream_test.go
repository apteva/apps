package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	native "github.com/apteva/db"
)

func TestNativeAggregateSpillParityAndSnapshot(t *testing.T) {
	o := DefaultAptevaOptions()
	o.SyncMode, o.QueryWorkBytes = "os", 1<<20
	m, err := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o})
	checkErr(t, err)
	t.Cleanup(func() { checkErr(t, m.Close()) })
	for _, adapter := range []string{"sqlite", "aptevadb"} {
		call(t, m, "p", "database_create", Request{Database: adapter, Adapter: adapter})
		call(t, m, "p", "collection_create", Request{Database: adapter, Collection: "items", Fields: []Field{{"k", "integer", false}, {"label", "text", false}, {"amount", "integer", false}, {"n", "number", true}, {"flag", "boolean", true}}})
		call(t, m, "p", "index_create", Request{Database: adapter, Collection: "items", Index: &Index{Name: "by_label", Fields: []Order{{"label", "asc"}}}})
		for start := 0; start < 12000; start += 1000 {
			var records []Record
			for i := start; i < start+1000; i++ {
				var n, flag any = float64(i) / 2, i%2 == 0
				if i%5 == 0 {
					n = nil
				}
				if i%3 == 0 {
					flag = nil
				}
				records = append(records, Record{"id": fmt.Sprint(i), "k": strconv.FormatInt(9007199254740992+int64(i), 10), "label": fmt.Sprintf("g%05d", i) + strings.Repeat("x", 90), "amount": strconv.Itoa(i % 7), "n": n, "flag": flag})
			}
			call(t, m, "p", "insert", Request{Database: adapter, Collection: "items", Records: records})
		}
	}
	d, err := m.resolve(context.Background(), "p", "aptevadb", "", false)
	checkErr(t, err)
	b := d.backend.(*aptevaBackend)
	base := Request{Collection: "items", GroupBy: []string{"k", "label"}, Metrics: []Metric{{"rows", "count", ""}, {"present", "count", "n"}, {"total", "sum", "amount"}, {"average", "avg", "n"}, {"low", "min", "k"}, {"flag_max", "max", "flag"}}, Query: Query{Limit: 3}}
	// Prove this fixture cannot pass through the old materializing executor.
	checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
		c, e := tx.collection("items")
		if e != nil {
			return e
		}
		r := base
		_, e = validateAggregate(c, &r)
		if e != nil {
			return e
		}
		p, e := native.ParsePipeline(marshal([]any{map[string]any{"$group": nativeAggregatePlan(c, r).group}}))
		if e != nil {
			return e
		}
		p.SetLimits(native.Limits{MaxBytes: 1 << 20})
		h, e := tx.(*aptevaTx).readCollection(c)
		if e != nil {
			return e
		}
		_, e = h.RunParallel(context.Background(), p, 4)
		if !errors.Is(e, native.ErrLimit) {
			t.Fatalf("control should exceed budget: %v", e)
		}
		return nil
	}))
	for _, group := range [][]string{{"k"}, {"label", "k"}} {
		for _, metrics := range [][]Metric{{{"rows", "count", ""}, {"present", "count", "n"}}, base.Metrics} {
			for _, order := range [][]Order{nil, {{"rows", "desc"}, {"k", "desc"}}} {
				r := base
				r.GroupBy = group
				r.Metrics = metrics
				r.OrderBy = order
				r.Database = "sqlite"
				want := call(t, m, "p", "aggregate", r)
				r.Database = "aptevadb"
				got := call(t, m, "p", "aggregate", r)
				if !equalJSON(got, want) {
					t.Fatalf("%+v: %v != %v", r, got, want)
				}
			}
		}
	}
	// A selective indexed filter is retained when the fallback streams a match.
	r := base
	r.Database = "aptevadb"
	r.Where = &Filter{Field: "label", Op: "starts_with", Value: "g0"}
	r.RequireIndex = true
	wantReq := r
	wantReq.Database = "sqlite"
	want := call(t, m, "p", "aggregate", wantReq)
	if got := call(t, m, "p", "aggregate", r); !equalJSON(got, want) {
		t.Fatal(got, want)
	}
	checkScratch := func() {
		t.Helper()
		checkErr(t, filepath.Walk(filepath.Join(m.root, "databases", d.info.ID), func(path string, info os.FileInfo, e error) error {
			if e != nil {
				return e
			}
			if strings.Contains(filepath.Base(path), ".aptevadb-lease-") || strings.HasPrefix(filepath.Base(path), "aptevadb-aggregate-") {
				return fmt.Errorf("scratch leaked: %s", path)
			}
			return nil
		}))
	}
	for _, cfg := range []struct {
		disk   int64
		groups int
	}{{0, 1 << 20}, {1 << 20, 1 << 20}, {1 << 30, 10}} {
		b.options.AggregateDiskBytes, b.options.AggregateMaxGroups = cfg.disk, cfg.groups
		v, e := m.Execute(context.Background(), "p", "aggregate", baseWithDatabase(base, "aptevadb"))
		expectCode(t, e, "resource_limit")
		if v != nil {
			t.Fatal("partial result", v)
		}
		checkScratch()
	}
	b.options.AggregateDiskBytes, b.options.AggregateMaxGroups = 1<<30, 1<<20
	// Both retry and direct stream keep the pre-existing transaction snapshot.
	checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
		call(t, m, "p", "insert", Request{Database: "aptevadb", Collection: "items", Records: []Record{{"id": "late", "k": "-1", "label": "late", "amount": "7", "n": 1.5, "flag": true}}})
		c, e := tx.collection("items")
		if e != nil {
			return e
		}
		r := base
		out, e := validateAggregate(c, &r)
		if e != nil {
			return e
		}
		rows, e := tx.(*aptevaTx).aggregateStream(c, r, out, nativeAggregatePlan(c, r))
		if e != nil {
			return e
		}
		if rows[0]["k"] != "9007199254740992" {
			t.Fatal("snapshot changed", rows)
		}
		return nil
	}))
	if got := call(t, m, "p", "aggregate", baseWithDatabase(base, "aptevadb")).(map[string]any)["rows"].([]Record)[0]["k"]; got != "-1" {
		t.Fatal(got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	_, err = m.Execute(ctx, "p", "aggregate", baseWithDatabase(base, "aptevadb"))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	checkScratch()
	enabled := true
	call(t, m, "p", "realtime_configure", Request{Database: "aptevadb", Enabled: &enabled})
	w := openTestWatch(t, m, "p", Request{Database: "aptevadb", Collections: []string{"items"}, Initial: &Operation{Op: "aggregate", Request: base}})
	if w.Initial.(map[string]any)["rows"].([]Record)[0]["k"] != "-1" {
		t.Fatal(w.Initial)
	}
	w.Close()
	checkScratch()
	// The omitted high-key group overflows only after aggregation. Streaming
	// must validate it even though the response keeps the lowest keys.
	call(t, m, "p", "insert", Request{Database: "aptevadb", Collection: "items", Records: []Record{
		{"id": "overflow-a", "k": "9223372036854775806", "label": "zz_overflow", "amount": strconv.FormatInt(math.MaxInt64, 10), "n": math.MaxFloat64, "flag": true},
		{"id": "overflow-b", "k": "9223372036854775806", "label": "zz_overflow", "amount": "1", "n": math.MaxFloat64, "flag": true},
	}})
	for _, metric := range []Metric{{"total", "sum", "amount"}, {"total", "sum", "n"}} {
		q := base
		q.Database = "aptevadb"
		q.Metrics = []Metric{metric}
		q.Limit = 1
		v, e := m.Execute(context.Background(), "p", "aggregate", q)
		expectCode(t, e, "resource_limit")
		if v != nil {
			t.Fatal("overflow returned partial response", v)
		}
		checkScratch()
	}
}

func baseWithDatabase(r Request, name string) Request { r.Database = name; return r }

func TestNativeMemoryDiagnosticsAndResourceErrors(t *testing.T) {
	o := DefaultAptevaOptions()
	o.ManagedMemoryBytes = 128 << 20
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o})
	checkErr(t, e)
	defer m.Close()
	call(t, m, "p", "database_create", Request{Database: "n", Adapter: "aptevadb"})
	info := call(t, m, "p", "database_describe", Request{Database: "n"}).(DatabaseInfo)
	if info.Memory == nil || info.Memory.MappedTargetBytes != 64<<20 || info.Memory.ManagedTargetBytes != 128<<20 || info.Memory.ManagedBufferBytes <= 0 {
		t.Fatal(info)
	}
	listed := call(t, m, "p", "databases_list", Request{}).([]DatabaseInfo)
	if listed[0].Memory != nil {
		t.Fatal("diagnostics must not be persisted", listed)
	}
	for _, e := range []error{native.ErrDiskLimit, native.ErrMemoryPressure, fmt.Errorf("wrapped: %w", native.ErrDiskLimit)} {
		expectCode(t, nativeError(e), "resource_limit")
	}
}

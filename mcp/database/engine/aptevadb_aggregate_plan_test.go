package engine

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"testing"

	native "github.com/apteva/db"
)

func TestPrimaryCountAggregateMatchesSQLite(t *testing.T) {
	m := manager(t)
	for _, adapter := range []string{"sqlite", "aptevadb"} {
		call(t, m, "p", "database_create", Request{Database: adapter, Adapter: adapter})
		call(t, m, "p", "collection_create", Request{Database: adapter, Collection: "items", PrimaryKey: []string{"tenant", "n"}, Fields: []Field{{"tenant", "text", false}, {"n", "integer", false}, {"optional", "text", true}}})
		var records []Record
		for i := 0; i < 12; i++ {
			var optional any
			if i%2 == 0 {
				optional = "present"
			}
			records = append(records, Record{"tenant": fmt.Sprint(i % 3), "n": strconv.FormatInt(9007199254740992+int64(i), 10), "optional": optional})
		}
		call(t, m, "p", "insert", Request{Database: adapter, Collection: "items", Records: records})
	}
	for _, group := range [][]string{{"tenant", "n"}, {"n", "tenant"}, {"tenant"}} {
		for _, metrics := range [][]Metric{{{"z_count", "count", ""}, {"a_count", "count", ""}}, {{"present", "count", "optional"}}} {
			for _, where := range []*Filter{nil, {Field: "tenant", Op: "eq", Value: "1"}, {Field: "tenant", Op: "eq", Value: "absent"}} {
				for _, order := range [][]Order{nil, {{metrics[0].Name, "desc"}, {group[0], "desc"}}} {
					for _, limit := range []int{1, 3, 100} {
						r := Request{Collection: "items", GroupBy: group, Metrics: metrics, Query: Query{Where: where, OrderBy: order, Limit: limit}}
						r.Database = "sqlite"
						want := call(t, m, "p", "aggregate", r)
						r.Database = "aptevadb"
						got := call(t, m, "p", "aggregate", r)
						if !equalJSON(got, want) {
							t.Fatalf("%+v: got %v want %v", r, got, want)
						}
					}
				}
			}
		}
	}
	indexed := Request{Database: "aptevadb", Collection: "items", GroupBy: []string{"tenant", "n"}, Metrics: []Metric{{"rows", "count", ""}}, Query: Query{Where: &Filter{Field: "tenant", Op: "eq", Value: "1"}, RequireIndex: true, Limit: 1}}
	if len(call(t, m, "p", "aggregate", indexed).(map[string]any)["rows"].([]Record)) != 1 {
		t.Fatal("indexed primary count")
	}
	indexed.Where = nil
	_, err := m.Execute(context.Background(), "p", "aggregate", indexed)
	expectCode(t, err, "query_too_expensive")
	// The rewrite must retain the write overlay and its snapshot.
	d, e := m.resolve(context.Background(), "p", "aptevadb", "", false)
	checkErr(t, e)
	abort := errors.New("rollback")
	e = d.backend.transaction(context.Background(), true, func(tx transaction) error {
		_, err := execute(tx, "delete", Request{Collection: "items", Key: Record{"tenant": "0", "n": "9007199254740992"}}, d.key)
		if err != nil {
			return err
		}
		v, err := execute(tx, "aggregate", Request{Collection: "items", GroupBy: []string{"tenant", "n"}, Metrics: []Metric{{"count", "count", ""}}, Query: Query{Limit: 1}}, d.key)
		if err != nil {
			return err
		}
		row := v.(map[string]any)["rows"].([]Record)[0]
		if row["n"] != "9007199254740995" || row["count"] != "1" || !v.(map[string]any)["truncated"].(bool) {
			t.Fatal(v)
		}
		return abort
	})
	if !errors.Is(e, abort) {
		t.Fatal(e)
	}
}

func TestNativeCountTopKMatchesSQLite(t *testing.T) {
	m := manager(t)
	fixture(t, m, "sqlite")
	other := manager(t)
	fixture(t, other, "aptevadb")
	for _, group := range [][]string{{"country"}, {"country", "active"}, {"units"}} {
		for _, metric := range []Metric{{"rows", "count", ""}, {"present", "count", "amount"}} {
			for _, order := range [][]Order{{{metric.Name, "desc"}}, {{group[len(group)-1], "desc"}, {metric.Name, "asc"}}} {
				r := Request{Database: "shop", Collection: "orders", GroupBy: group, Metrics: []Metric{metric}, Query: Query{OrderBy: order, Limit: 1}}
				got := call(t, other, "p", "aggregate", r)
				want := call(t, m, "p", "aggregate", r)
				if !equalJSON(got, want) {
					t.Fatalf("%+v: %v != %v", r, got, want)
				}
			}
		}
	}
}

func TestNativeCountTopKAvoidsAllResultMaps(t *testing.T) {
	o := DefaultAptevaOptions()
	o.QueryWorkBytes = 1 << 20
	o.AggregateWorkers = 1
	m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o})
	checkErr(t, e)
	defer m.Close()
	call(t, m, "p", "database_create", Request{Database: "n", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "n", Collection: "items", Fields: []Field{{"label", "text", false}}})
	for start := 0; start < 2000; start += 1000 {
		var records []Record
		for i := start; i < start+1000; i++ {
			records = append(records, Record{"label": fmt.Sprintf("%04d", i) + strings.Repeat("x", 96)})
		}
		call(t, m, "p", "insert", Request{Database: "n", Collection: "items", Records: records})
	}
	r := Request{Database: "n", Collection: "items", GroupBy: []string{"label"}, Metrics: []Metric{{"rows", "count", ""}}, Query: Query{OrderBy: []Order{{"rows", "desc"}, {"label", "desc"}}, Limit: 1}}
	v := call(t, m, "p", "aggregate", r).(map[string]any)
	if !v["truncated"].(bool) || v["rows"].([]Record)[0]["label"] != "1999"+strings.Repeat("x", 96) {
		t.Fatal(v)
	}
	// Group state fits, but materializing every result would exceed the budget.
	d, e := m.resolve(context.Background(), "p", "n", "", false)
	checkErr(t, e)
	checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error {
		c, err := tx.collection("items")
		if err != nil {
			return err
		}
		h, err := tx.(*aptevaTx).readCollection(c)
		if err != nil {
			return err
		}
		spec := nativeAggregatePlan(c, r)
		p, err := native.ParsePipeline(marshal([]any{map[string]any{"$group": spec.group}}))
		if err != nil {
			return err
		}
		p.SetLimits(native.Limits{MaxBytes: 1 << 20})
		_, err = h.RunParallel(context.Background(), p, 1)
		if !errors.Is(err, native.ErrLimit) {
			t.Fatal("control did not reach native result-materialization limit", err)
		}
		return nil
	}))
}

func TestAggregateTopKDoesNotHideOverflow(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "n", Adapter: "aptevadb"})
	call(t, m, "p", "collection_create", Request{Database: "n", Collection: "items", Fields: []Field{{"bucket", "text", false}, {"i", "integer", false}}})
	call(t, m, "p", "insert", Request{Database: "n", Collection: "items", Records: []Record{{"bucket": "a", "i": "1"}, {"bucket": "z", "i": strconv.FormatInt(math.MaxInt64, 10)}, {"bucket": "z", "i": "1"}}})
	_, e := m.Execute(context.Background(), "p", "aggregate", Request{Database: "n", Collection: "items", GroupBy: []string{"bucket"}, Metrics: []Metric{{"total", "sum", "i"}}, Query: Query{OrderBy: []Order{{"bucket", "asc"}}, Limit: 1}})
	expectCode(t, e, "resource_limit")
}

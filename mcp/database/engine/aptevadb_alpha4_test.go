package engine

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"testing"

	native "github.com/apteva/db"
)

func TestAptevaAlpha4NativeReducersAndAppOverflowContract(t *testing.T) {
	for _, workers := range []int{1, 4} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			opts := DefaultAptevaOptions()
			opts.AggregateWorkers = workers
			m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &opts})
			checkErr(t, e)
			t.Cleanup(func() { checkErr(t, m.Close()) })
			call(t, m, "p", "database_create", Request{Database: "native", Adapter: "aptevadb"})
			call(t, m, "p", "collection_create", Request{Database: "native", Collection: "items", Fields: []Field{{"i", "integer", false}, {"optional", "integer", true}, {"b", "boolean", true}}})
			rs := []Record{{"id": "a", "i": "9007199254740993", "optional": "0", "b": false}, {"id": "b", "i": "7", "optional": nil, "b": nil}}
			call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: rs})
			for _, metrics := range [][]Metric{
				{{"rows", "count", ""}, {"total", "sum", "i"}, {"average", "avg", "i"}, {"present", "count", "optional"}, {"false_count", "count", "b"}},
				{{"total", "sum", "i"}, {"rows", "count", ""}, {"present", "count", "optional"}, {"average", "avg", "i"}, {"false_count", "count", "b"}},
			} {
				got := call(t, m, "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: metrics}).(map[string]any)["rows"].([]Record)[0]
				if got["total"] != "9007199254741000" || got["rows"] != "2" || got["present"] != "1" || got["false_count"] != "1" || got["average"] != float64(4503599627370500) {
					t.Fatal(got)
				}
			}
			// The range proof cannot certify this sum, but the checked rescan succeeds.
			call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": "a"}, Set: Record{"i": strconv.FormatInt(math.MaxInt64, 10)}})
			call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": "b"}, Set: Record{"i": "0"}})
			got := call(t, m, "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"total", "sum", "i"}}}).(map[string]any)["rows"].([]Record)[0]
			if got["total"] != strconv.FormatInt(math.MaxInt64, 10) {
				t.Fatal(got)
			}
			// Native wide accumulation accepts cancelling intermediate overflow. Our
			// checked int64 contract must still reject it (a,b,c primary-key order).
			call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": "b"}, Set: Record{"i": "1"}})
			call(t, m, "p", "insert", Request{Database: "native", Collection: "items", Records: []Record{{"id": "c", "i": "-1", "optional": nil, "b": nil}}})
			_, e = m.Execute(context.Background(), "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"total", "sum", "i"}}})
			expectCode(t, e, "resource_limit")
			call(t, m, "p", "update", Request{Database: "native", Collection: "items", Key: Record{"id": "b"}, Set: Record{"i": strconv.FormatInt(math.MaxInt64, 10)}})
			call(t, m, "p", "delete", Request{Database: "native", Collection: "items", Key: Record{"id": "c"}})
			_, e = m.Execute(context.Background(), "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"total", "sum", "i"}}})
			expectCode(t, e, "resource_limit")
			avg := call(t, m, "p", "aggregate", Request{Database: "native", Collection: "items", Metrics: []Metric{{"average", "avg", "i"}}}).(map[string]any)["rows"].([]Record)[0]
			if avg["average"] != float64(math.MaxInt64) {
				t.Fatal(avg)
			}
			// Probe the actual library, independently of the adapter's normalized API.
			d, e := m.resolve(context.Background(), "p", "native", "", false)
			checkErr(t, e)
			b := d.backend.(*aptevaBackend)
			var c Collection
			checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error { var e error; c, e = tx.collection("items"); return e }))
			h, e := b.handle(c)
			checkErr(t, e)
			p, e := native.ParsePipeline([]byte(`[{"$group":{"_id":null,"sum":{"$sum":"$i"},"avg":{"$avg":"$i"},"rows":{"$count":{}},"present":{"$countNonNull":"$optional"},"false_count":{"$countNonNull":"$b"}}}]`))
			checkErr(t, e)
			direct, e := h.RunParallel(context.Background(), p, workers)
			checkErr(t, e)
			if direct[0]["sum"] != 2*float64(math.MaxInt64) || direct[0]["avg"] != float64(math.MaxInt64) || direct[0]["rows"] != int64(2) || direct[0]["present"] != int64(1) || direct[0]["false_count"] != int64(1) {
				t.Fatal(direct)
			}
		})
	}
}

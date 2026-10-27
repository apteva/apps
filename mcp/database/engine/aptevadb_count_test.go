package engine

import (
	"context"
	"errors"
	"fmt"
	"testing"

	native "github.com/apteva/db"
)

func TestAptevaCountDocumentsContract(t *testing.T) {
	for _, cache := range []int64{0, 64 << 20} {
		t.Run(fmt.Sprint(cache), func(t *testing.T) {
			o := DefaultAptevaOptions()
			o.CacheBytes = cache
			m, e := OpenWithConfig(t.TempDir(), ManagerOptions{Apteva: &o})
			checkErr(t, e)
			t.Cleanup(func() { checkErr(t, m.Close()) })
			call(t, m, "p", "database_create", Request{Database: "n", Adapter: "aptevadb"})
			call(t, m, "p", "collection_create", Request{Database: "n", Collection: "items", Fields: []Field{{"bucket", "text", true}, {"rank", "integer", true}, {"flag", "boolean", true}, {"optional", "number", true}}})
			call(t, m, "p", "index_create", Request{Database: "n", Collection: "items", Index: &Index{Name: "bucket_rank", Fields: []Order{{"bucket", "asc"}, {"rank", "desc"}}}})
			call(t, m, "p", "insert", Request{Database: "n", Collection: "items", Records: []Record{
				{"id": "a", "bucket": "x", "rank": "9007199254740993", "flag": false, "optional": 0.},
				{"id": "b", "bucket": "x", "rank": "9007199254740992", "flag": true},
				{"id": "c"}, {"id": "d", "bucket": "", "rank": "0", "flag": false}, {"id": "e"},
			}})
			d, e := m.resolve(context.Background(), "p", "n", "", false)
			checkErr(t, e)
			b := d.backend.(*aptevaBackend)
			for _, checkpoint := range []bool{false, true} {
				if checkpoint {
					checkErr(t, b.db.Checkpoint())
				}
				cases := []struct {
					name    string
					where   *Filter
					want    string
					covered bool
				}{
					{"all", nil, "5", true},
					{"equality", &Filter{Field: "bucket", Op: "eq", Value: "x"}, "2", true},
					{"empty", &Filter{Field: "bucket", Op: "eq", Value: ""}, "1", true},
					{"missing", &Filter{Field: "bucket", Op: "eq", Value: "missing"}, "0", true},
					{"null", &Filter{Field: "bucket", Op: "is_null"}, "2", true},
					{"exact_integer", &Filter{And: []Filter{{Field: "bucket", Op: "eq", Value: "x"}, {Field: "rank", Op: "eq", Value: "9007199254740993"}}}, "1", true},
					{"ne_excludes_null", &Filter{Field: "bucket", Op: "ne", Value: "x"}, "1", false},
					{"range_excludes_null", &Filter{Field: "rank", Op: "gt", Value: "0"}, "2", false},
					{"false", &Filter{Field: "flag", Op: "eq", Value: false}, "2", false},
					{"residual", &Filter{And: []Filter{{Field: "bucket", Op: "eq", Value: "x"}, {Field: "flag", Op: "eq", Value: false}}}, "1", false},
					{"or", &Filter{Or: []Filter{{Field: "bucket", Op: "eq", Value: "x"}, {Field: "bucket", Op: "is_null"}}}, "4", false},
				}
				for _, tc := range cases {
					t.Run(fmt.Sprintf("checkpoint_%v/%s", checkpoint, tc.name), func(t *testing.T) {
						r := Request{Database: "n", Collection: "items", Query: Query{Where: tc.where}}
						got := call(t, m, "p", "count", r).(map[string]any)["count"]
						if got != tc.want {
							t.Fatal(got, tc.want)
						}
						r.Metrics = []Metric{{Name: "total", Op: "count"}}
						summary := call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
						if len(summary) != 1 || summary[0]["total"] != tc.want {
							t.Fatal(summary)
						}
						// Verify the exact adapter translation is eligible for the native covering
						// path, including compound integer precision and null normalization.
						checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
							c, err := tx.collection("items")
							if err != nil {
								return err
							}
							h, err := tx.(*aptevaTx).readCollection(c)
							if err != nil {
								return err
							}
							var stats native.QueryStats
							_, err = h.CountDocuments(context.Background(), marshal(nativeFilter(c, tc.where)), native.CountOptions{Stats: &stats})
							if err == nil && (stats.Plan.IndexOnly != tc.covered || tc.covered && stats.DocumentFetches != 0) {
								t.Fatal(stats)
							}
							return err
						}))
					})
				}
			}
			r := Request{Database: "n", Collection: "items", Metrics: []Metric{{Name: "present", Op: "count", Field: "optional"}}}
			got := call(t, m, "p", "aggregate", r).(map[string]any)["rows"].([]Record)
			if got[0]["present"] != "1" {
				t.Fatal(got)
			}
			r = Request{Database: "n", Collection: "items", Query: Query{Where: &Filter{Field: "bucket", Op: "eq", Value: "x"}, RequireIndex: true}}
			if call(t, m, "p", "count", r).(map[string]any)["count"] != "2" {
				t.Fatal("indexed count")
			}
			r.Where = &Filter{Field: "flag", Op: "eq", Value: false}
			_, e = m.Execute(context.Background(), "p", "count", r)
			expectCode(t, e, "query_too_expensive")
			r.Where = nil
			_, e = m.Execute(context.Background(), "p", "count", r)
			expectCode(t, e, "query_too_expensive")
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			if _, e = m.Execute(ctx, "p", "count", Request{Database: "n", Collection: "items"}); !errors.Is(e, context.Canceled) {
				t.Fatal("cancelled count succeeded")
			}
			// The same read transaction remains on its snapshot while a writer commits.
			checkErr(t, b.transaction(context.Background(), false, func(tx transaction) error {
				call(t, m, "p", "insert", Request{Database: "n", Collection: "items", Records: []Record{{"id": "later", "bucket": "x"}}})
				v, err := execute(tx, "count", Request{Database: "n", Collection: "items"}, d.key)
				if err == nil && v.(map[string]any)["count"] != "5" {
					t.Fatal(v)
				}
				return err
			}))
			// Counts inside a write transaction must include its staged overlay. Abort
			// afterward and check that the staged deletion was never committed.
			abort := errors.New("test rollback")
			e = b.transaction(context.Background(), true, func(tx transaction) error {
				_, err := execute(tx, "delete", Request{Database: "n", Collection: "items", Key: Record{"id": "a"}}, d.key)
				if err != nil {
					return err
				}
				for _, where := range []*Filter{nil, {Field: "bucket", Op: "eq", Value: "x"}} {
					v, err := execute(tx, "count", Request{Database: "n", Collection: "items", Query: Query{Where: where}}, d.key)
					if err != nil {
						return err
					}
					want := "5"
					if where != nil {
						want = "2"
					}
					if v.(map[string]any)["count"] != want {
						t.Fatal(v, want)
					}
				}
				return abort
			})
			if !errors.Is(e, abort) {
				t.Fatal(e)
			}
			if call(t, m, "p", "count", Request{Database: "n", Collection: "items"}).(map[string]any)["count"] != "6" {
				t.Fatal("rollback lost records")
			}
		})
	}
}

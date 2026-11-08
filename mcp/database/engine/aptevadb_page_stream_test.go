package engine

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestLargeScopedPageStreams(t *testing.T) {
	oracle := map[string]string{}
	for _, adapter := range []string{"sqlite", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			rt := relOpen(t, adapter)
			relCollection(rt, "keys", "native_key", "native_key text, id integer, centre text, at text, payload text", "id")
			relCall(rt, "index_create", `{"collection":"keys","index":{"name":"scope_at_id","fields":[{"field":"centre"},{"field":"at"},{"field":"id"}]}}`)
			relCall(rt, "index_create", `{"collection":"keys","index":{"name":"at_id","fields":[{"field":"at"},{"field":"id"}]}}`)
			payload := strings.Repeat("x", 1024)
			// Only the native fixture needs wide unused documents; SQLite
			// supplies the independent row/filter/order oracle.
			if adapter == "sqlite" {
				payload = ""
			}
			for start := 1; start <= 100000; start += 1000 {
				rows := []map[string]any{}
				for i := start; i < start+1000; i++ {
					at := "2026-10-15"
					if i == 1 {
						at = "2026-10-01"
					}
					if i == 40 {
						at = "2026-10-31"
					}
					rows = append(rows, map[string]any{"native_key": fmt.Sprintf("k%06d", i), "id": fmt.Sprint(i), "centre": fmt.Sprint("c", i%40), "at": at, "payload": payload})
				}
				relInsert(rt, "keys", rows...)
			}
			for i, extra := range []struct{ centre, at string }{{"c39", "2026-10-15"}, {"c0", "2026-11-01"}, {"c0", "2026-09-30"}, {"c0", "2026-10-32"}} {
				relInsert(rt, "keys", map[string]any{"native_key": fmt.Sprint("outside", i), "id": fmt.Sprint(100001 + i), "centre": extra.centre, "at": extra.at, "payload": payload})
			}
			centres := []any{}
			for i := 0; i < 32; i++ {
				centres = append(centres, fmt.Sprint("c", i))
			}
			request := func(scope []any, from, to string) Request {
				return Request{Collection: "keys", Query: Query{Where: &Filter{And: []Filter{{Field: "centre", Op: "in", Value: scope}, {Field: "at", Op: "gte", Value: from}, {Field: "at", Op: "lt", Value: to}}}, Select: []string{"id"}, OrderBy: []Order{{Field: "id", Direction: "desc"}}, Limit: 5}}
			}
			base := request(centres, "2026-10-01", "2026-10-32")
			compare := func(name string, r Request) map[string]any {
				t.Helper()
				out := coreExecute(t, rt, "find", r)
				records := out["records"]
				if name == "qualified recording page" {
					normalized := []map[string]any{}
					for _, row := range relList(records) {
						normalized = append(normalized, map[string]any{"id": row["a.id"]})
					}
					records = normalized
				}
				stable := map[string]any{"records": records, "total": out["total"], "hasMore": out["hasMore"]}
				value := string(marshal(stable))
				if adapter == "sqlite" {
					oracle[name] = value
				} else {
					relEqual(t, name, value, oracle[name])
				}
				return out
			}
			r := base
			r.Count = true
			first := compare("counted", r)
			relEqual(t, "exact scoped total", first["total"], "80000")
			relEqual(t, "numeric descending IDs", relStrings(relList(first["records"]), "id"), []string{"100000", "99991", "99990", "99989", "99988"})
			compare("without count", base)
			r = base
			r.Cursor = first["nextCursor"].(string)
			r.Count = true
			compare("cursor", r)
			r = base
			r.Offset = 5
			r.Count = true
			compare("offset", r)
			r = base
			r.As = "a"
			r.Select = []string{"a.id"}
			r.OrderBy = []Order{{Field: "a.id", Direction: "desc"}}
			r.Count = true
			qualified := r
			if adapter == "sqlite" {
				// The plain SQLite page is the independent ordering/scope
				// oracle; its legacy qualified root batching is unrelated
				// to the native adapter's streaming execution.
				oracle["qualified recording page"] = oracle["counted"]
			} else {
				compare("qualified recording page", r)
			}
			for _, tc := range []struct {
				name            string
				scope           []any
				from, to, total string
			}{
				{"one centre", []any{"c0"}, "2026-10-01", "2026-10-32", "2500"},
				{"two centres", []any{"c0", "c1"}, "2026-10-01", "2026-10-32", "5000"},
				{"duplicate IN", append(append([]any{}, centres...), "c0", "c1"), "2026-10-01", "2026-10-32", "80000"},
				{"no results", []any{"missing"}, "2026-10-01", "2026-10-32", "0"},
				{"lower boundary", centres, "2026-10-01", "2026-10-02", "1"},
				{"upper boundary", centres, "2026-10-31", "2026-10-32", "1"},
			} {
				r = request(tc.scope, tc.from, tc.to)
				r.Count = true
				out := compare(tc.name, r)
				relEqual(t, tc.name+" total", out["total"], tc.total)
			}
			batchSecond := qualified
			if adapter == "sqlite" {
				batchSecond = base
				batchSecond.Count = true
			}
			batch := coreExecute(t, rt, "batch", Request{Operations: []Operation{{Op: "find", Request: Request{Collection: "keys", Query: base.Query, Count: true}}, {Op: "find", Request: batchSecond}}})
			results := batch["results"].([]any)
			relEqual(t, "batch plain total", results[0].(map[string]any)["total"], "80000")
			relEqual(t, "batch qualified total", results[1].(map[string]any)["total"], "80000")

			d, release, e := rt.m.acquireDatabase(context.Background(), "p", "d", "", false)
			checkErr(t, e)
			defer release()
			if adapter == "aptevadb" {
				diag := &countDiagnostics{}
				checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error {
					r := base
					r.diag = diag
					_, e := execute(tx, "find", r, d.key)
					return e
				}))
				if diag.stats == nil || diag.stats.Plan.SortInMemory || diag.stats.DocumentFetches != 80000 {
					t.Fatalf("not an actual streaming execution: %+v", diag.stats)
				}
				if diag.pageHeapBytes <= 0 || diag.pageHeapBytes > 32<<10 || diag.pagePeakBytes > 16<<20 || diag.pageDecodedBytes >= 1024 {
					t.Fatalf("unbounded or unprojected page: %+v", diag)
				}
				t.Logf("actual documents=%d native retained=%d heap=%d decoded row=%d combined peak=%d", diag.stats.DocumentFetches, diag.stats.PeakMemoryBytes, diag.pageHeapBytes, diag.pageDecodedBytes, diag.pagePeakBytes)
				// Each component fits this budget separately; their combination must fail
				// atomically instead of receiving two independent memory allowances.
				b := d.backend.(*aptevaBackend)
				old := b.options.QueryWorkBytes
				small := diag.pagePeakBytes - 1
				if small <= diag.pageHeapBytes || small <= diag.scanBytes {
					t.Fatal("invalid combined-budget test", diag)
				}
				b.options.QueryWorkBytes = small
				e = d.backend.transaction(context.Background(), false, func(tx transaction) error {
					out, e := execute(tx, "find", base, d.key)
					if e != nil && out != nil {
						t.Fatal("partial result on memory failure", out)
					}
					return e
				})
				b.options.QueryWorkBytes = old
				expectCode(t, e, "resource_limit")
			}
			// Staged updates share the scan snapshot, mask old committed rows, and do
			// not let a higher ID outside the authorised scope enter the page or total.
			abort := errors.New("rollback fixture overlay")
			e = d.backend.transaction(context.Background(), true, func(tx transaction) error {
				_, e := execute(tx, "delete", Request{Collection: "keys", Key: Record{"native_key": "k100000"}}, d.key)
				if e != nil {
					return e
				}
				_, e = execute(tx, "insert", Request{Collection: "keys", Records: []Record{{"native_key": "staged", "id": "200000", "centre": "c0", "at": "2026-10-15", "payload": payload}, {"native_key": "staged-foreign", "id": "300000", "centre": "c39", "at": "2026-10-15", "payload": payload}}}, d.key)
				if e != nil {
					return e
				}
				r := base
				r.Count = true
				requests := []Request{r}
				if adapter == "aptevadb" {
					requests = append(requests, qualified)
				}
				for _, r := range requests {
					out, e := execute(tx, "find", r, d.key)
					if e != nil {
						return e
					}
					m := map[string]any{}
					checkErr(t, decode(marshal(out), &m))
					relEqual(t, "overlay scoped total", m["total"], "80000")
					if !strings.Contains(string(marshal(m["records"])), `"200000"`) || strings.Contains(string(marshal(m["records"])), `"300000"`) {
						t.Fatal("overlay scope/page", m)
					}
				}
				return abort
			})
			if !errors.Is(e, abort) {
				t.Fatal(e)
			}

			// Equal numeric IDs retain the primary-key tie-breaker across both the
			// first page and its cursor. Different adapters issue their own cursors.
			relInsert(rt, "keys", map[string]any{"native_key": "a-twin", "id": "100000", "centre": "c0", "at": "2026-10-15", "payload": payload})
			r = base
			r.Select = []string{"id", "native_key"}
			r.Limit = 1
			r.Count = true
			twin := compare("tie first", r)
			relEqual(t, "deterministic tie", relList(twin["records"])[0]["native_key"], "a-twin")
			r.Cursor = twin["nextCursor"].(string)
			afterTwin := compare("tie cursor", r)
			relEqual(t, "tie continues", relList(afterTwin["records"])[0]["native_key"], "k100000")
			if adapter == "aptevadb" {
				// A concurrent writer cannot change any later read within this snapshot.
				checkErr(t, d.backend.transaction(context.Background(), false, func(tx transaction) error {
					r := base
					r.Count = true
					out, e := execute(tx, "find", r, d.key)
					if e != nil {
						return e
					}
					before := string(marshal(out))
					relInsert(rt, "keys", map[string]any{"native_key": "concurrent", "id": "400000", "centre": "c0", "at": "2026-10-15", "payload": payload})
					out, e = execute(tx, "find", r, d.key)
					if e != nil {
						return e
					}
					// Tokens legitimately carry different expiry seconds; compare rows/count.
					a, b := map[string]any{}, map[string]any{}
					checkErr(t, decode([]byte(before), &a))
					checkErr(t, decode(marshal(out), &b))
					delete(a, "nextCursor")
					delete(b, "nextCursor")
					relEqual(t, "same snapshot", string(marshal(b)), string(marshal(a)))
					return nil
				}))
				ctx, cancel := context.WithCancel(context.Background())
				e = d.backend.transaction(ctx, false, func(tx transaction) error {
					c, e := tx.collection("keys")
					if e != nil {
						return e
					}
					return tx.(recordScanner).scanRecords(c, base.Query, []string{"id"}, func(Record) bool { cancel(); return false })
				})
				cancel()
				if !errors.Is(e, context.Canceled) {
					t.Fatalf("scan cancellation: %v", e)
				}
			}
		})
	}
}

package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
)

// A where given with a key guards a key mutation: key equality AND the
// guard, checked with ifVersion in the same write transaction. A failed
// guard is indistinguishable from an absent record.
func TestGuardedKeyMutations(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m, e := Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer m.Close()
			ctx := context.Background()
			exec := func(op string, r Request) (any, error) {
				r.Database, r.Collection = "d", "records"
				return m.Execute(ctx, "p", op, r)
			}
			must := func(op string, r Request) map[string]any {
				t.Helper()
				v, e := exec(op, r)
				if e != nil {
					t.Fatal(op, e)
				}
				return v.(map[string]any)
			}
			code := func(e error) string {
				var ee *Error
				if errors.As(e, &ee) {
					return ee.Code + ": " + ee.Message
				}
				return fmt.Sprint(e)
			}
			if _, e := m.Execute(ctx, "p", "database_create", Request{Database: "d", Adapter: adapter}); e != nil {
				t.Fatal(e)
			}
			if _, e := exec("collection_create", Request{Fields: []Field{{"id", "text", false}, {"centre_id", "text", false}, {"notes", "text", true}, {"n", "integer", false}}}); e != nil {
				t.Fatal(e)
			}
			must("insert", Request{Records: []Record{{"id": "owned", "centre_id": "centre_a", "n": "0"}, {"id": "other", "centre_id": "centre_b", "n": "0"}}})
			guard := func(centre string) *Filter { return &Filter{Field: "centre_id", Op: "eq", Value: centre} }
			record := func(id string) Record {
				t.Helper()
				return must("get", Request{Key: Record{"id": id}})["record"].(Record)
			}

			// Key, guard and revision match: one change, one revision.
			v := must("update", Request{Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a"), RequireIndex: true}, IfVersion: 1, Set: Record{"notes": "Edited safely"}, MaxAffected: 1})
			if v["affected"] != 1 {
				t.Fatal(v)
			}
			if r := record("owned"); r["notes"] != "Edited safely" || r["_version"] != "2" {
				t.Fatal(r)
			}
			// Stale revision.
			_, e = exec("update", Request{Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a")}, IfVersion: 1, Set: Record{"notes": "stale"}})
			if c := code(e); c != "version_conflict: record version changed" {
				t.Fatal(c)
			}
			// Failed guard with ifVersion, and a missing record: one neutral failure.
			_, guardErr := exec("update", Request{Key: Record{"id": "other"}, Query: Query{Where: guard("centre_a")}, IfVersion: 1, Set: Record{"notes": "x"}})
			_, missingErr := exec("update", Request{Key: Record{"id": "missing"}, Query: Query{Where: guard("centre_a")}, IfVersion: 1, Set: Record{"notes": "x"}})
			if code(guardErr) != "version_conflict: record is absent" || code(guardErr) != code(missingErr) {
				t.Fatal(code(guardErr), code(missingErr))
			}
			// Failed guard without ifVersion: nothing changes.
			if v := must("update", Request{Key: Record{"id": "other"}, Query: Query{Where: guard("centre_a")}, Set: Record{"notes": "x"}}); v["affected"] != 0 {
				t.Fatal(v)
			}
			if r := record("other"); r["notes"] != nil || r["_version"] != "1" {
				t.Fatal(r)
			}
			if v := must("delete", Request{Key: Record{"id": "other"}, Query: Query{Where: guard("centre_a")}}); v["affected"] != 0 {
				t.Fatal(v)
			}
			// The same rules inside a write batch.
			b := must("batch", Request{Operations: []Operation{
				{Op: "update", Request: Request{Collection: "records", Key: Record{"id": "other"}, Query: Query{Where: guard("centre_a")}, Set: Record{"notes": "x"}}},
				{Op: "update", Request: Request{Collection: "records", Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a")}, IfVersion: 2, Increment: Record{"n": "1"}}},
			}})
			if b["affected"] != 1 {
				t.Fatal(b)
			}
			// Restrictions kept: a complete key, never with all.
			for _, r := range []Request{
				{Key: Record{"id": "owned"}, All: true, Set: Record{"notes": "x"}},
				{Key: Record{"centre_id": "centre_a"}, Query: Query{Where: guard("centre_a")}, Set: Record{"notes": "x"}},
				{Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a")}, Set: Record{"_version": "9"}},
			} {
				if _, e := exec("update", r); code(e)[:16] != "invalid_argument" {
					t.Fatal(r, code(e))
				}
			}
			// Concurrent edits with one revision: exactly one succeeds.
			version := record("owned")["_version"].(string)
			var wins, conflicts int
			var mu sync.Mutex
			var wg sync.WaitGroup
			for i := range 8 {
				wg.Go(func() {
					var rev int64
					fmt.Sscan(version, &rev)
					_, e := exec("update", Request{Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a")}, IfVersion: rev, Set: Record{"notes": fmt.Sprint("edit ", i)}})
					mu.Lock()
					defer mu.Unlock()
					if e == nil {
						wins++
					} else if code(e) == "version_conflict: record version changed" {
						conflicts++
					} else {
						t.Error(e)
					}
				})
			}
			wg.Wait()
			if wins != 1 || conflicts != 7 {
				t.Fatal(wins, conflicts)
			}
			// Delete with key, guard and revision.
			r := record("owned")
			var rev int64
			fmt.Sscan(r["_version"].(string), &rev)
			if v := must("delete", Request{Key: Record{"id": "owned"}, Query: Query{Where: guard("centre_a")}, IfVersion: rev}); v["affected"] != 1 {
				t.Fatal(v)
			}
		})
	}
}

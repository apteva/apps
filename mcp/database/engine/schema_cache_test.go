package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
)

func TestCachedSchemaDropRecreateAndReturnedCopies(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			get := func(id string) Record {
				return call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": id}}).(map[string]any)["record"].(Record)
			}
			get("a")
			c := call(t, m, "p", "collection_describe", Request{Database: "shop", Collection: "orders"}).(Collection)
			c.Fields[0].Name = "changed"
			c.PrimaryKey[0] = "changed"
			if get("a")["id"] != "a" {
				t.Fatal("caller mutated cached schema")
			}
			call(t, m, "p", "index_create", Request{Database: "shop", Collection: "orders", Index: &Index{Name: "unique_country", Unique: true, Fields: []Order{{Field: "email"}}}})
			indexes := call(t, m, "p", "indexes_list", Request{Database: "shop", Collection: "orders"}).([]Index)
			indexes[0].Name = "changed"
			indexes[0].Fields[0].Field = "changed"
			get("a")
			_, e := m.Execute(context.Background(), "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": "duplicate", "email": "a@example.com", "active": true}}})
			expectCode(t, e, "unique_conflict")
			// A failed DDL operation must leave the previous schema and index effective.
			_, e = m.Execute(context.Background(), "p", "collection_drop", Request{Database: "shop", Collection: "orders"})
			expectCode(t, e, "invalid_argument")
			get("a")
			call(t, m, "p", "collection_drop", Request{Database: "shop", Collection: "orders", Confirm: true})
			call(t, m, "p", "collection_create", Request{Database: "shop", Collection: "orders", Fields: []Field{{Name: "label", Type: "text"}}})
			call(t, m, "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{{"id": "a", "label": "new schema"}}})
			if r := get("a"); r["label"] != "new schema" || r["email"] != nil {
				t.Fatal(r)
			}
			call(t, m, "p", "database_drop", Request{Database: "shop", Confirm: true})
			call(t, m, "p", "database_create", Request{Database: "shop", Adapter: adapter})
			call(t, m, "p", "collection_create", Request{Database: "shop", Collection: "orders", Fields: []Field{{Name: "label", Type: "text"}}})
			v := call(t, m, "p", "get", Request{Database: "shop", Collection: "orders", Key: Record{"id": "a"}}).(map[string]any)
			if v["found"] != false {
				t.Fatal("dropped database cache reused")
			}
		})
	}
}
func TestPointReadCancellationLimitsAndIsolation(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			for _, scope := range []string{"p", "q"} {
				call(t, m, scope, "database_create", Request{Database: "bench", Adapter: adapter})
				call(t, m, scope, "collection_create", Request{Database: "bench", Collection: "items", Fields: []Field{{Name: "value", Type: "text"}}})
				call(t, m, scope, "insert", Request{Database: "bench", Collection: "items", Records: []Record{{"id": "same", "value": scope}, {"id": "a<&\u2028", "value": scope}}})
			}
			for _, scope := range []string{"p", "q"} {
				for _, id := range []string{"same", "a<&\u2028"} {
					v := call(t, m, scope, "get", Request{Database: "bench", Collection: "items", Key: Record{"id": id}}).(map[string]any)
					if v["record"].(Record)["value"] != scope {
						t.Fatal("point read escaped scope")
					}
				}
			}
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			_, e := m.Execute(ctx, "p", "get", Request{Database: "bench", Collection: "items", Key: Record{"id": "same"}})
			if !errors.Is(e, context.Canceled) {
				t.Fatal(e)
			}
			_, e = m.Execute(context.Background(), "p", "get", Request{Database: "bench", Collection: "items", Key: Record{"id": strings.Repeat("x", 2049)}})
			expectCode(t, e, "invalid_argument")
			var wg sync.WaitGroup
			for i := 0; i < 8; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for i := 0; i < 40; i++ {
						_, e := m.Execute(context.Background(), "p", "get", Request{Database: "bench", Collection: "items", Key: Record{"id": "same"}})
						if e != nil {
							t.Error(e)
						}
					}
				}()
			}
			wg.Wait()
			if adapter == "pebble" {
				d, e := m.resolve(context.Background(), "p", "bench", "", false)
				if e != nil {
					t.Fatal(e)
				}
				d.mu.Lock()
				d.backend.(*pebbleBackend).writeErr = fail("storage_error", "uncertain durable commit")
				d.mu.Unlock()
				_, e = m.Execute(context.Background(), "p", "get", Request{Database: "bench", Collection: "items", Key: Record{"id": "same"}})
				expectCode(t, e, "storage_error")
			}
		})
	}
}
func TestInsertDoesNotMutateInput(t *testing.T) {
	for _, adapter := range []string{"sqlite", "pebble", "aptevadb"} {
		t.Run(adapter, func(t *testing.T) {
			m := manager(t)
			fixture(t, m, adapter)
			input := Record{"active": true}
			call(t, m, "p", "insert", Request{Database: "shop", Collection: "orders", Records: []Record{input}})
			if len(input) != 1 || input["id"] != nil || input["_version"] != nil {
				t.Fatal("insert changed caller data", input)
			}
		})
	}
}

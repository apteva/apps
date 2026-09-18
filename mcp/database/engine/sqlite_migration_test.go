package engine

import (
	"context"
	"strings"
	"testing"
)

func TestSQLiteV1MigratesOnFirstWrite(t *testing.T) {
	m := manager(t)
	call(t, m, "p", "database_create", Request{Database: "legacy"})
	call(t, m, "p", "collection_create", Request{Database: "legacy", Collection: "items", Fields: []Field{{Name: "id", Type: "text"}, {Name: "name", Type: "text"}, {Name: "payload", Type: "json", Nullable: true}}})
	call(t, m, "p", "index_create", Request{Database: "legacy", Collection: "items", Index: &Index{Name: "by_name", Fields: []Order{{Field: "name"}}}})
	call(t, m, "p", "insert", Request{Database: "legacy", Collection: "items", Records: []Record{{"id": "a", "name": "old", "payload": map[string]any{"n": "1"}}}})
	d, e := m.resolve(context.Background(), "p", "legacy", "", false)
	if e != nil {
		t.Fatal(e)
	}
	db := d.backend.(*sqliteBackend).db
	var c Collection
	// Reconstruct the v1 physical layout with the same typed values. This is
	// equivalent to a database created by v0.1.x before storage_version existed.
	var schema []byte
	if e := db.QueryRow(`SELECT schema_json FROM __collections WHERE name='items'`).Scan(&schema); e != nil {
		t.Fatal(e)
	}
	if e := decode(schema, &c); e != nil {
		t.Fatal(e)
	}
	defs := []string{`"__pk" TEXT PRIMARY KEY NOT NULL`, `"__doc" TEXT NOT NULL`}
	for _, f := range allFields(c) {
		s := quote(f.Name) + " " + sqlType(f)
		if !f.Nullable {
			s += " NOT NULL"
		}
		defs = append(defs, s)
	}
	if _, e := db.Exec(`DROP TABLE "c_items"; CREATE TABLE "c_items" (` + strings.Join(defs, ",") + `);`); e != nil {
		t.Fatal(e)
	}
	r := Record{"id": "a", "name": "old", "payload": map[string]any{"n": "1"}}
	stamp(r, nil, "2026-01-01T00:00:00.000000Z")
	pk, _ := primary(c, r)
	args := []any{pk, string(marshal(r))}
	for _, f := range allFields(c) {
		args = append(args, sqlValue(f, r[f.Name]))
	}
	fields := append([]string{"__pk", "__doc"}, func() []string {
		out := []string{}
		for _, f := range allFields(c) {
			out = append(out, quote(f.Name))
		}
		return out
	}()...)
	qs := strings.TrimSuffix(strings.Repeat("?,", len(fields)), ",")
	if _, e := db.Exec(`INSERT INTO "c_items" (`+strings.Join(fields, ",")+") VALUES ("+qs+")", args...); e != nil {
		t.Fatal(e)
	}
	if _, e := db.Exec(`CREATE UNIQUE INDEX "` + strings.Trim(sqlIndex(c, "primary"), `"`) + `" ON "c_items" ("id"); UPDATE __collections SET storage_version=1 WHERE name='items'`); e != nil {
		t.Fatal(e)
	}
	call(t, m, "p", "update", Request{Database: "legacy", Collection: "items", Key: Record{"id": "a"}, Set: Record{"name": "new"}})
	v := call(t, m, "p", "get", Request{Database: "legacy", Collection: "items", Key: Record{"id": "a"}}).(map[string]any)
	if v["record"].(Record)["name"] != "new" {
		t.Fatal(v)
	}
	var hasDoc int
	if e := db.QueryRow(`SELECT count(*) FROM pragma_table_info('c_items') WHERE name='__doc'`).Scan(&hasDoc); e != nil {
		t.Fatal(e)
	}
	if hasDoc != 0 {
		t.Fatal("v1 document column remains after migration")
	}
	var storage int
	if e := db.QueryRow(`SELECT storage_version FROM __collections WHERE name='items'`).Scan(&storage); e != nil {
		t.Fatal(e)
	}
	if storage != 2 {
		t.Fatalf("storage version=%d", storage)
	}
}

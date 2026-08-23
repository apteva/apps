package main

import (
	"database/sql"
	"errors"
	"os"
	"testing"

	_ "modernc.org/sqlite"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	migration, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewStore(db)
}
func createTestDesign(t *testing.T, store *Store, project string) *Design {
	t.Helper()
	canonical, _, hash, err := normalizeDefinition(testDefinitionJSON(t), "Example")
	if err != nil {
		t.Fatal(err)
	}
	d, err := store.CreateDesign(project, "Example", canonical, nil, hash, "", "")
	if err != nil {
		t.Fatal(err)
	}
	return d
}
func TestStoreRevisionLifecycleAndIsolation(t *testing.T) {
	store := testStore(t)
	d := createTestDesign(t, store, "project-a")
	if d.CurrentRevision == nil || d.CurrentRevision.Number != 1 {
		t.Fatalf("first revision missing: %#v", d)
	}
	if _, err := store.GetDesign("project-b", d.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-project read should fail: %v", err)
	}
	canonical, _, hash, err := normalizeDefinition(testDefinitionJSON(t), "")
	if err != nil {
		t.Fatal(err)
	}
	r, err := store.CreateRevision("project-a", d.ID, d.CurrentRevisionID, canonical, nil, hash, "next", "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Number != 2 {
		t.Fatalf("got revision %d", r.Number)
	}
	if _, err := store.CreateRevision("project-a", d.ID, d.CurrentRevisionID, canonical, nil, hash, "stale", ""); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("expected conflict, got %v", err)
	}
}

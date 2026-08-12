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
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestStoreRevisionLifecycleAndProjectIsolation(t *testing.T) {
	store := testStore(t)
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, err := normalizeParameters([]byte(`{"width":40}`), definition)
	if err != nil {
		t.Fatal(err)
	}
	design, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Plate", Definition: canonical, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	if design.CurrentRevision == nil || design.CurrentRevision.RevisionNumber != 1 {
		t.Fatalf("first revision not attached: %#v", design)
	}
	if _, err := store.GetDesign("project-b", design.ID); !errors.Is(err, errNotFound) {
		t.Fatalf("cross-project read should fail, got %v", err)
	}
	revision, err := store.CreateRevision("project-a", CreateRevisionInput{
		DesignID: design.ID, ExpectedParent: design.CurrentRevisionID,
		Definition: canonical, Parameters: []byte(`{"width":55}`), Note: "wider",
	})
	if err != nil {
		t.Fatal(err)
	}
	if revision.RevisionNumber != 2 {
		t.Fatalf("got revision number %d", revision.RevisionNumber)
	}
	if _, err := store.CreateRevision("project-a", CreateRevisionInput{DesignID: design.ID, ExpectedParent: design.CurrentRevisionID, Definition: canonical, Parameters: parameters}); !errors.Is(err, errRevisionConflict) {
		t.Fatalf("expected optimistic concurrency conflict, got %v", err)
	}
}

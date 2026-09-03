package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
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
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(migrations)
	for _, path := range migrations {
		migration, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(migration)); err != nil {
			t.Fatal(err)
		}
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

func TestSaveArtifactReturnsTheExistingDuplicateInsteadOfLastInsertedRow(t *testing.T) {
	store := testStore(t)
	canonical, definition, err := normalizeDefinition(testDefinition(), 256)
	if err != nil {
		t.Fatal(err)
	}
	parameters, _ := normalizeParameters(nil, definition)
	design, err := store.CreateDesign("project-a", CreateDesignInput{Name: "Artifact design", Definition: canonical, Parameters: parameters})
	if err != nil {
		t.Fatal(err)
	}
	first, err := store.SaveArtifact(Artifact{DesignID: design.ID, RevisionID: design.CurrentRevisionID, Format: "mesh-json", SHA256: "first", Name: "first.mesh.json", ContentType: "application/json", Metadata: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	secondInput := Artifact{DesignID: design.ID, RevisionID: design.CurrentRevisionID, Format: "glb", SHA256: "second", Name: "second.glb", ContentType: "model/gltf-binary", Metadata: json.RawMessage(`{}`)}
	second, err := store.SaveArtifact(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := store.SaveArtifact(secondInput)
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != second.ID || duplicate.ID == first.ID || duplicate.Format != "glb" {
		t.Fatalf("duplicate resolved to wrong artifact: first=%#v second=%#v duplicate=%#v", first, second, duplicate)
	}
}

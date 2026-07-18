package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileIDsAreNotReusedAfterHardDelete(t *testing.T) {
	ctx := newTestCtx(t)
	first := mustUpload(t, ctx, "first.txt", "/", "first")
	if err := dbHardDelete(ctx.AppDB(), "test-proj", first.ID); err != nil {
		t.Fatal(err)
	}
	second := mustUpload(t, ctx, "second.txt", "/", "second")
	if second.ID <= first.ID {
		t.Fatalf("hard-deleted id was reused: first=%d second=%d", first.ID, second.ID)
	}
}

func TestFilesSchemaUsesAutoincrement(t *testing.T) {
	ctx := newTestCtx(t)
	var schema string
	if err := ctx.AppDB().QueryRow(
		`SELECT sql FROM sqlite_master WHERE type='table' AND name='files'`,
	).Scan(&schema); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(schema), "PRIMARY KEY AUTOINCREMENT") {
		t.Fatalf("files schema is not monotonic: %s", schema)
	}
}

func TestMonotonicIDMigrationPreservesRowsAndIndexes(t *testing.T) {
	db := openLegacyStorageDB(t)
	if _, err := db.Exec(`
		INSERT INTO files
		(id, project_id, name, folder, storage_key, content_type, size_bytes,
		 sha256, uploaded_by, source, tags, metadata, visibility)
		VALUES
		(41, 'p', 'a.jpg', '/photos/', 'key-a', 'image/jpeg', 10,
		 'sha-a', 'human:1', 'imported', '["a"]', '{"x":1}', 'signed')`); err != nil {
		t.Fatal(err)
	}
	migration, err := os.ReadFile("migrations/005_files_monotonic_ids.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(migration)); err != nil {
		t.Fatal(err)
	}
	var name, folder, source, tags, metadata, visibility string
	if err := db.QueryRow(`SELECT name, folder, source, tags, metadata, visibility FROM files WHERE id=41`).
		Scan(&name, &folder, &source, &tags, &metadata, &visibility); err != nil {
		t.Fatal(err)
	}
	if name != "a.jpg" || folder != "/photos/" || source != "imported" ||
		tags != `["a"]` || metadata != `{"x":1}` || visibility != "signed" {
		t.Fatalf("migration changed row: %q %q %q %q %q %q", name, folder, source, tags, metadata, visibility)
	}
	if _, err := db.Exec(`DELETE FROM files WHERE id=41`); err != nil {
		t.Fatal(err)
	}
	res, err := db.Exec(`INSERT INTO files (project_id,name,folder,storage_key) VALUES ('p','b.jpg','/','key-b')`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if id <= 41 {
		t.Fatalf("migrated allocator reused id: got %d after 41", id)
	}
	for _, index := range []string{"ix_files_proj", "ix_files_folder", "ix_files_sha", "ix_files_name", "ix_files_updated"} {
		var count int
		if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name=?`, index).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Errorf("index %s missing after migration", index)
		}
	}
}

func openLegacyStorageDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "legacy.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	schema, err := os.ReadFile("migrations/001_init.sql")
	if err != nil {
		t.Fatal(err)
	}
	legacy := strings.Replace(string(schema), "PRIMARY KEY AUTOINCREMENT", "PRIMARY KEY", 1)
	if _, err := db.Exec(legacy); err != nil {
		t.Fatal(err)
	}
	return db
}

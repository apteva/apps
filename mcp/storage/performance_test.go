package main

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Tracks the cost of a root summary without allocating a Go row per file.
func BenchmarkFolderSummary10000Files(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	migrations, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		b.Fatal(err)
	}
	for _, path := range migrations {
		raw, e := os.ReadFile(path)
		if e != nil {
			b.Fatal(e)
		}
		if _, e = db.Exec(string(raw)); e != nil {
			b.Fatal(e)
		}
	}
	tx, err := db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO files(project_id,name,folder,storage_key,size_bytes,sha256,visibility) VALUES('bench','file',?,?,100,'hash','private')`)
	if err != nil {
		b.Fatal(err)
	}
	for i := 0; i < 10000; i++ {
		if _, err = stmt.Exec(fmt.Sprintf("/folder-%03d/sub/", i%100), fmt.Sprintf("key-%d", i)); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.ResetTimer()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		rows, err := dbListChildFolderInfos(db, "bench", "/")
		if err != nil || len(rows) != 100 {
			b.Fatal(len(rows), err)
		}
	}
}

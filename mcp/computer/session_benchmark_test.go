package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

// The image-bearing getter is the v0.7.87 metadata path; compare identical rows.
func BenchmarkSessionMetadataWithStoredScreenshot(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		b.Fatal(err)
	}
	for _, file := range files {
		body, err := os.ReadFile(file)
		if err != nil {
			b.Fatal(err)
		}
		if _, err := db.Exec(string(body)); err != nil {
			b.Fatal(err)
		}
	}
	row := &ComputerSession{ID: "benchmark", Backend: "local", Status: "closed", RecordingStatus: "unsupported", OpenedAt: nowUTC(), UpdatedAt: nowUTC(), FinalScreenshot: make([]byte, 2<<20)}
	if err := dbPutSession(db, row); err != nil {
		b.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		get  func(*sql.DB, string) (*ComputerSession, error)
	}{{"previous_blob_query", dbGetSession}, {"metadata_only", dbGetSessionMetadata}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := tc.get(db, row.ID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

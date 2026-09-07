package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func BenchmarkPublishedExtensionLookup(b *testing.B) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	paths, _ := filepath.Glob("migrations/*.sql")
	for _, path := range paths {
		body, err := os.ReadFile(path)
		if err != nil {
			b.Fatal(err)
		}
		if _, err = db.Exec(string(body)); err != nil {
			b.Fatal(err)
		}
	}
	site, err := dbCreateSite(db, "bench", "main", "Main", "")
	if err != nil {
		b.Fatal(err)
	}
	manifest := testExtensionManifest()
	manifest.Assets["store.css"] = strings.Repeat("a", 128<<10)
	if _, err := dbExtensionUpsert(db, "bench", site.ID, "store", "commerce", manifest, true); err != nil {
		b.Fatal(err)
	}
	if _, err := cachedPublishedExtensions(db, "bench", site.ID); err != nil {
		b.Fatal(err)
	}
	b.Run("uncached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := dbExtensionsList(db, "bench", site.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("cached", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if _, err := cachedPublishedExtensions(db, "bench", site.ID); err != nil {
				b.Fatal(err)
			}
		}
	})
}

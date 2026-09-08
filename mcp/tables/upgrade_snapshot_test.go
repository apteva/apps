package main

import (
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// Optional validation against a consistent SQLite backup. Never opens the
// supplied backup for writing; all migration runs against a temporary copy.
func TestUpgradeDatabaseSnapshotPreservesAllRows(t *testing.T) {
	source := os.Getenv("TABLES_UPGRADE_SNAPSHOT")
	if source == "" {
		t.Skip("set TABLES_UPGRADE_SNAPSHOT to a consistent SQLite backup")
	}
	src, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "snapshot.db")
	dst, err := os.Create(path)
	if err != nil {
		src.Close()
		t.Fatal(err)
	}
	_, copyErr := io.Copy(dst, src)
	src.Close()
	closeErr := dst.Close()
	if copyErr != nil {
		t.Fatal(copyErr)
	}
	if closeErr != nil {
		t.Fatal(closeErr)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	type snapshot struct {
		name, query, hash string
		count             int64
		root              int64
	}
	rows, err := db.Query(`SELECT physical_name FROM tables_meta ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	snapshots := []snapshot{}
	for _, name := range names {
		// Use the original projection, so additive revision columns are allowed.
		r, err := db.Query("SELECT * FROM " + quote(name) + " LIMIT 0")
		if err != nil {
			t.Fatal(err)
		}
		cols, err := r.Columns()
		r.Close()
		if err != nil {
			t.Fatal(err)
		}
		projection := ""
		for i, col := range cols {
			if i > 0 {
				projection += ","
			}
			projection += quote(col)
		}
		q := "SELECT " + projection + " FROM " + quote(name) + " ORDER BY id"
		hash, count := snapshotRowsDigest(t, db, q)
		var root int64
		if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='table' AND name=?`, name).Scan(&root); err != nil {
			t.Fatal(err)
		}
		snapshots = append(snapshots, snapshot{name, q, hash, count, root})
	}
	metadataHash, _ := snapshotRowsDigest(t, db, `SELECT * FROM columns_meta ORDER BY id`)
	var pending int
	if err := db.QueryRow(`SELECT count(*) FROM tables_meta WHERE storage_version<>1`).Scan(&pending); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, nil, nil)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal("snapshot mount", err)
	}
	// Restart must skip committed work without changing existing bytes.
	if err := (&App{}).OnMount(ctx); err != nil {
		t.Fatal("snapshot remount", err)
	}
	var remaining int
	if err := db.QueryRow(`SELECT count(*) FROM tables_meta WHERE storage_version<>1`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatal("unfinished migration", remaining, err)
	}
	var total int64
	for _, s := range snapshots {
		hash, count := snapshotRowsDigest(t, db, s.query)
		if hash != s.hash || count != s.count {
			t.Fatalf("rows changed in %s: before=%d after=%d", s.name, s.count, count)
		}
		var root int64
		if err := db.QueryRow(`SELECT rootpage FROM sqlite_master WHERE type='table' AND name=?`, s.name).Scan(&root); err != nil || root != s.root {
			t.Fatal("table rebuilt", s.name, err)
		}
		total += count
	}
	afterMetadata, _ := snapshotRowsDigest(t, db, `SELECT * FROM columns_meta ORDER BY id`)
	if afterMetadata != metadataHash {
		t.Fatal("column/default metadata rewritten")
	}
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatal("integrity", integrity, err)
	}
	t.Logf("Migrated %d pending tables; %d total tables, %d rows preserved byte-for-byte by original-column SHA256; metadata and table roots unchanged; restart and integrity check passed", pending, len(snapshots), total)
}

func snapshotRowsDigest(t *testing.T, db *sql.DB, q string) (string, int64) {
	t.Helper()
	rows, err := db.Query(q)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		t.Fatal(err)
	}
	h := sha256.New()
	enc := json.NewEncoder(h)
	var count int64
	for rows.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			t.Fatal(err)
		}
		if err := enc.Encode(values); err != nil {
			t.Fatal(err)
		}
		count++
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", h.Sum(nil)), count
}

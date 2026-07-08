package main

import (
	"archive/tar"
	"compress/gzip"
	"database/sql"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestStreamTenantArchiveLocalCopiesSQLiteAndSkipsSidecars(t *testing.T) {
	t.Setenv("FLEET_DATA_ROOT", t.TempDir())
	src := t.TempDir()
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "nested", "state.txt"), []byte("tenant state"), 0o600); err != nil {
		t.Fatal(err)
	}
	dbPath := filepath.Join(src, "apteva.db")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if _, err := db.Exec(`CREATE TABLE stream_check (value TEXT); INSERT INTO stream_check (value) VALUES ('from stream');`); err != nil {
		_ = db.Close()
		t.Fatalf("seed sqlite: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close sqlite: %v", err)
	}
	if err := os.WriteFile(dbPath+"-wal", []byte("wal"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dbPath+"-shm", []byte("shm"), 0o600); err != nil {
		t.Fatal(err)
	}

	pr, pw := io.Pipe()
	errCh := make(chan error, 1)
	go func() {
		errCh <- streamTenantArchiveLocal(pw, src)
		_ = pw.Close()
	}()
	dst := t.TempDir()
	gz, err := gzip.NewReader(pr)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatalf("tar next: %v", err)
		}
		target := filepath.Join(dst, filepath.Clean(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o700); err != nil {
				t.Fatal(err)
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(h.Mode))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := io.Copy(out, tr); err != nil {
				_ = out.Close()
				t.Fatal(err)
			}
			if err := out.Close(); err != nil {
				t.Fatal(err)
			}
		default:
			t.Fatalf("unexpected tar entry %s type=%v", h.Name, h.Typeflag)
		}
	}
	if err := gz.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := <-errCh; err != nil {
		t.Fatalf("stream: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "nested", "state.txt"))
	if err != nil {
		t.Fatalf("read copied file: %v", err)
	}
	if string(got) != "tenant state" {
		t.Fatalf("copied file = %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(dst, "apteva.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("WAL sidecar copied, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(dst, "apteva.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("SHM sidecar copied, stat err=%v", err)
	}
	clonedDB, err := sql.Open("sqlite", filepath.Join(dst, "apteva.db"))
	if err != nil {
		t.Fatalf("open cloned sqlite: %v", err)
	}
	var value string
	if err := clonedDB.QueryRow(`SELECT value FROM stream_check`).Scan(&value); err != nil {
		_ = clonedDB.Close()
		t.Fatalf("query cloned sqlite: %v", err)
	}
	if err := clonedDB.Close(); err != nil {
		t.Fatalf("close cloned sqlite: %v", err)
	}
	if value != "from stream" {
		t.Fatalf("sqlite value = %q", value)
	}
}

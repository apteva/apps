package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
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

func TestTransferURLRequiresHTTPSAndDoesNotRegisterRejectedTransfer(t *testing.T) {
	app := &App{}
	if err := app.initTransferState(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_PUBLIC_URL", "http://controller.example")
	if _, err := app.createTransferURL(nil, t.TempDir(), time.Minute); err == nil {
		t.Fatal("expected insecure public URL to be rejected")
	}
	app.transferMu.Lock()
	count := len(app.transfers)
	app.transferMu.Unlock()
	if count != 0 {
		t.Fatalf("rejected transfer was registered: count=%d", count)
	}
}

func TestSignedTransferCanOnlyBeConsumedOnce(t *testing.T) {
	app := &App{}
	if err := app.initTransferState(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "state.txt"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	rawURL, err := app.createTransferURL(nil, source, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	id := filepath.Base(u.Path)
	requestURL := "/transfers/" + id + "?" + u.RawQuery
	first := httptest.NewRecorder()
	app.httpTransfer(first, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if first.Code != http.StatusOK || first.Body.Len() == 0 {
		t.Fatalf("first transfer status=%d bytes=%d body=%s", first.Code, first.Body.Len(), first.Body.String())
	}
	second := httptest.NewRecorder()
	app.httpTransfer(second, httptest.NewRequest(http.MethodGet, requestURL, nil))
	if second.Code != http.StatusNotFound {
		t.Fatalf("second transfer status=%d want 404", second.Code)
	}
}

func TestStreamLocalTenantToLocalDoesNotUseArchiveBuffer(t *testing.T) {
	t.Setenv("FLEET_DATA_ROOT", t.TempDir())
	src := filepath.Join(t.TempDir(), "source")
	dst := filepath.Join(t.TempDir(), "target")
	if err := os.MkdirAll(filepath.Join(src, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("streamed-state-"), 1<<15)
	if err := os.WriteFile(filepath.Join(src, "nested", "state.bin"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := streamLocalTenantToLocal(src, dst); err != nil {
		t.Fatalf("stream copy: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dst, "nested", "state.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Fatal("streamed payload changed")
	}
}

func TestExtractTenantArchiveStreamRejectsTraversal(t *testing.T) {
	var archive bytes.Buffer
	gz := gzip.NewWriter(&archive)
	tw := tar.NewWriter(gz)
	content := []byte("escape")
	if err := tw.WriteHeader(&tar.Header{Name: "../outside", Mode: 0o600, Size: int64(len(content))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	err := extractTenantArchiveStream(bytes.NewReader(archive.Bytes()), filepath.Join(root, "tenant"))
	if err == nil {
		t.Fatal("expected traversal archive to be rejected")
	}
	if _, statErr := os.Stat(filepath.Join(root, "outside")); !os.IsNotExist(statErr) {
		t.Fatalf("traversal file was created: %v", statErr)
	}
}

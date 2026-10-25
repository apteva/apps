package main

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestFleetBackupKeepsActiveTenantOnline(t *testing.T) {
	app, ctx := newTestApp(t)
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	tenantServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "alive") }))
	defer tenantServer.Close()
	id := seedTenant(t, app, "online", StatusActive)
	tenant, _, _ := app.store.get(id)
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET base_url=? WHERE id=?`, tenantServer.URL, id); err != nil {
		t.Fatal(err)
	}
	os.MkdirAll(tenant.ConfigDir, 0700)
	os.WriteFile(filepath.Join(tenant.ConfigDir, "state"), []byte("state"), 0600)
	// A live WAL database without a .db suffix must be captured through SQLite,
	// including committed pages which have not reached the main file yet.
	db, err := sql.Open("sqlite", "file:"+filepath.Join(tenant.ConfigDir, "state.sqlite")+"?_pragma=journal_mode(WAL)")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE active(n INTEGER); INSERT INTO active VALUES(42)`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	out, err := app.toolFleetTenantSnapshot(ctx, map[string]any{"tenant_id": id, "supports_streaming": true})
	if err != nil {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("preparation copied the tenant before returning")
	}
	url := out.(map[string]any)["archive_url"].(string)
	w := httptest.NewRecorder()
	app.httpTransfer(w, httptest.NewRequest("GET", transferRequestPath(t, url), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	resp, err := http.Get(tenantServer.URL)
	if err != nil {
		t.Fatalf("tenant was stopped: %v", err)
	}
	resp.Body.Close()
	current, _, err := app.store.get(id)
	if err != nil || current.Status != StatusActive {
		t.Fatalf("tenant status changed: %+v %v", current, err)
	}
	_, payload, err := extractFleetTenantArchive(bytes.NewReader(w.Body.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(payload, "state.sqlite-wal")); !os.IsNotExist(err) {
		t.Fatal("live WAL file was archived")
	}
	restored, err := sql.Open("sqlite", filepath.Join(payload, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer restored.Close()
	var n int
	if err = restored.QueryRow(`SELECT n FROM active`).Scan(&n); err != nil || n != 42 {
		t.Fatalf("WAL data missing: %d %v", n, err)
	}
}

func TestFleetSnapshotCancellationAndOverlap(t *testing.T) {
	newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := writeFleetTenantArchiveContext(ctx, &out, t.TempDir(), fleetTenantBackupManifest{}); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancelled snapshot: %v bytes=%d", err, out.Len())
	}
	fleetSnapshotMu.Lock()
	err := writeFleetTenantArchiveContext(context.Background(), &out, t.TempDir(), fleetTenantBackupManifest{})
	fleetSnapshotMu.Unlock()
	if err == nil {
		t.Fatal("overlapping Fleet snapshots admitted")
	}
}

func TestFleetOnlineBackupCancelledAndCleaned(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "source.sqlite")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err = db.Exec(`CREATE TABLE payload(data BLOB); INSERT INTO payload VALUES(zeroblob(16777216))`); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Millisecond)
	defer cancel()
	dst := filepath.Join(dir, "partial.db")
	if err = cloneSQLiteDBContext(ctx, src, dst); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if _, err = os.Stat(dst); !os.IsNotExist(err) {
		t.Fatal("partial SQLite backup retained")
	}
}

func TestFleetBackupExcludesStoredArchives(t *testing.T) {
	newTestApp(t)
	dir := t.TempDir()
	objects := filepath.Join(dir, "objects")
	os.MkdirAll(objects, 0700)
	os.WriteFile(filepath.Join(objects, ".apteva-backup-destination"), nil, 0600)
	os.WriteFile(filepath.Join(objects, "previous.tar.gz"), []byte("old archive"), 0600)
	os.WriteFile(filepath.Join(dir, "state"), []byte("current state"), 0600)
	var out bytes.Buffer
	if err := writeFleetTenantArchiveContext(context.Background(), &out, dir, fleetTenantBackupManifest{FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	_, payload, err := extractFleetTenantArchive(bytes.NewReader(out.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(payload, "objects")); !os.IsNotExist(err) {
		t.Fatal("backups recursively archived")
	}
}

func TestFleetEmptyBackupRestoresEmptyPayload(t *testing.T) {
	newTestApp(t)
	var archive bytes.Buffer
	if err := writeFleetTenantArchive(&archive, t.TempDir(), fleetTenantBackupManifest{FormatVersion: 1}); err != nil {
		t.Fatal(err)
	}
	_, payload, err := extractFleetTenantArchive(bytes.NewReader(archive.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(payload)
	if err != nil || len(entries) != 0 {
		t.Fatalf("empty payload contains extra directories: %v %v", entries, err)
	}
}

func TestOnlineBackupPreservesV2MetadataAndRefreshesPreparedCredentials(t *testing.T) {
	app, ctx := newTestApp(t)
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	id := seedTenant(t, app, "metadata", StatusStopped)
	tenant, _, _ := app.store.get(id)
	os.MkdirAll(tenant.ConfigDir, 0700)
	prepared, err := app.toolFleetTenantSnapshot(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatal(err)
	}
	// A prepared URL must capture the current credential/setup state under its
	// download lease, including changes made since URL preparation.
	metadata, err := app.store.backupMetadata(id)
	if err != nil {
		t.Fatal(err)
	}
	metadata.APIKey, err = app.keys.seal([]byte("new-test-key"))
	if err != nil {
		t.Fatal(err)
	}
	metadata.SetupPassword, err = app.keys.seal([]byte("new-setup-password"))
	if err != nil {
		t.Fatal(err)
	}
	metadata.SetupComplete = false
	metadata.SetupPhase = "registering"
	if err = app.store.restoreMetadata(id, metadata); err != nil {
		t.Fatal(err)
	}
	w := httptest.NewRecorder()
	app.httpTransfer(w, httptest.NewRequest("GET", transferRequestPath(t, prepared.(map[string]any)["archive_url"].(string)), nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	manifest, _, err := extractFleetTenantArchive(bytes.NewReader(w.Body.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if manifest.FormatVersion != 2 || !bytes.Equal(manifest.ManagementKey, metadata.APIKey) || manifest.Metadata == nil || !reflect.DeepEqual(*manifest.Metadata, metadata) {
		t.Fatal("format-v2 control state or current credentials were lost")
	}
}

func TestOnlineBackupPreservesSymlinksAndSkipsStableHostSocket(t *testing.T) {
	newTestApp(t)
	dir, err := os.MkdirTemp("/tmp", "fleet-ipc-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	os.WriteFile(filepath.Join(dir, "state"), []byte("state"), 0600)
	if err := os.Symlink("state", filepath.Join(dir, "current")); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", filepath.Join(dir, "control.sock"))
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	var out bytes.Buffer
	if err = writeFleetTenantArchive(&out, dir, fleetTenantBackupManifest{FormatVersion: 2}); err != nil {
		t.Fatal(err)
	}
	_, payload, err := extractFleetTenantArchive(bytes.NewReader(out.Bytes()), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if link, err := os.Readlink(filepath.Join(payload, "current")); err != nil || link != "state" {
		t.Fatalf("contained symlink lost: %q %v", link, err)
	}
	if _, err = os.Lstat(filepath.Join(payload, "control.sock")); !os.IsNotExist(err) {
		t.Fatal("runtime control socket was archived")
	}
}

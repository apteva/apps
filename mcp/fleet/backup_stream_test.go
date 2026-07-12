package main

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func transferRequestPath(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatal(err)
	}
	return "/transfers/" + filepath.Base(u.Path) + "?" + u.RawQuery
}

func TestFleetSnapshotStreamingReturnsSignedArchive(t *testing.T) {
	app, ctx := newTestApp(t)
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	id := seedTenant(t, app, "acme", StatusStopped)
	tenant, _, _ := app.store.get(id)
	if err := os.MkdirAll(tenant.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tenant.ConfigDir, "state.txt"), []byte("snapshot state"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolFleetTenantSnapshot(ctx, map[string]any{"tenant_id": id, "supports_streaming": true})
	if err != nil {
		t.Fatal(err)
	}
	archiveURL, _ := out.(map[string]any)["archive_url"].(string)
	if archiveURL == "" {
		t.Fatalf("snapshot response = %#v", out)
	}
	rec := httptest.NewRecorder()
	app.httpTransfer(rec, httptest.NewRequest(http.MethodGet, transferRequestPath(t, archiveURL), nil))
	if rec.Code != http.StatusOK || rec.Body.Len() == 0 {
		t.Fatalf("download status=%d body=%s", rec.Code, rec.Body.String())
	}
	stage := t.TempDir()
	manifest, payload, err := extractFleetTenantArchive(bytes.NewReader(rec.Body.Bytes()), stage)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Tenant == nil || manifest.Tenant.ID != id {
		t.Fatalf("manifest tenant = %+v", manifest.Tenant)
	}
	got, err := os.ReadFile(filepath.Join(payload, "state.txt"))
	if err != nil || string(got) != "snapshot state" {
		t.Fatalf("snapshot payload=%q err=%v", got, err)
	}
}

func TestFleetRestoreStreamingValidatesAndReplacesTenant(t *testing.T) {
	app, ctx := newTestApp(t)
	t.Setenv("APTEVA_PUBLIC_URL", "https://controller.example")
	id := seedTenant(t, app, "acme", StatusStopped)
	tenant, _, _ := app.store.get(id)
	if err := os.MkdirAll(tenant.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tenant.ConfigDir, "state.txt"), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "state.txt"), []byte("restored"), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest := fleetTenantBackupManifest{FormatVersion: 1, Provider: "fleet", ScopeKind: "fleet_tenant", GeneratedAt: time.Now().UTC(), Tenant: tenant}
	var archive bytes.Buffer
	if err := writeFleetTenantArchive(&archive, source, manifest); err != nil {
		t.Fatal(err)
	}
	prepared, err := app.toolFleetTenantRestore(ctx, map[string]any{"tenant_id": id, "prepare_stream": true})
	if err != nil {
		t.Fatal(err)
	}
	uploadURL, _ := prepared.(map[string]any)["upload_url"].(string)
	rec := httptest.NewRecorder()
	app.httpTransfer(rec, httptest.NewRequest(http.MethodPost, transferRequestPath(t, uploadURL), bytes.NewReader(archive.Bytes())))
	if rec.Code != http.StatusOK {
		t.Fatalf("restore status=%d body=%s", rec.Code, rec.Body.String())
	}
	got, err := os.ReadFile(filepath.Join(tenant.ConfigDir, "state.txt"))
	if err != nil || string(got) != "restored" {
		t.Fatalf("restored payload=%q err=%v", got, err)
	}
}

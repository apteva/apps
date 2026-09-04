package main

import (
	"bytes"
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTenantOperationSerializesMutations(t *testing.T) {
	app, _ := newTestApp(t)
	done, err := app.beginTenantOperation("tenant-1", "migrate")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.beginTenantOperation("tenant-1", "delete"); err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("concurrent operation error = %v", err)
	}
	done()
	done2, err := app.beginTenantOperation("tenant-1", "delete")
	if err != nil {
		t.Fatalf("operation remained locked: %v", err)
	}
	done2()
}

func TestDurableUpdateLeaseBlocksRespawnAcrossAppInstances(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "leased", StatusActive)
	if err := app.store.setOperationLease(id, "update", "stop_indeterminate", "0.42.0", "0.34.5", "0.34.5"); err != nil {
		t.Fatal(err)
	}

	restarted := &App{store: app.store, operations: map[string]string{}}
	if got := restarted.tenantOperation(id); got != "update (stop_indeterminate)" {
		t.Fatalf("tenantOperation = %q", got)
	}
	if _, err := restarted.beginTenantOperation(id, "hosted auto-respawn"); err == nil {
		t.Fatal("durable update lease did not block auto-respawn")
	}

	// A deliberate tenant_update retry is the recovery path and may take over
	// the durable lease. Other mutation types remain fail-closed.
	done, err := restarted.beginTenantOperation(id, "update")
	if err != nil {
		t.Fatalf("update retry blocked: %v", err)
	}
	done()
	if err := restarted.store.clearOperationLease(id); err != nil {
		t.Fatal(err)
	}
	if got := restarted.tenantOperation(id); got != "" {
		t.Fatalf("operation remained after clearing lease: %q", got)
	}
}

func TestDeleteRejectsUnmanagedDirectoryAndPreservesRow(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusStopped)
	unmanaged := filepath.Join(t.TempDir(), "not-fleet-owned")
	if err := os.MkdirAll(unmanaged, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := app.store.db.Exec(`UPDATE fleet_tenants SET config_dir = ? WHERE id = ?`, unmanaged, id); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDelete(ctx, map[string]any{"tenant_id": id, "confirm": true}); err == nil || !strings.Contains(err.Error(), "unmanaged") {
		t.Fatalf("delete error = %v", err)
	}
	if _, _, err := app.store.get(id); err != nil {
		t.Fatalf("tenant row was removed: %v", err)
	}
	if _, err := os.Stat(unmanaged); err != nil {
		t.Fatalf("unmanaged directory was touched: %v", err)
	}
}

func TestRestoreRejectsBackupForAnotherTenant(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusStopped)
	source := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "state.txt"), []byte("other tenant"), 0o600); err != nil {
		t.Fatal(err)
	}
	var archive bytes.Buffer
	manifest := fleetTenantBackupManifest{
		FormatVersion: 1,
		Provider:      "fleet",
		ScopeKind:     "fleet_tenant",
		GeneratedAt:   time.Now().UTC(),
		Tenant:        &Tenant{ID: "another-tenant", Slug: "other"},
	}
	if err := writeFleetTenantArchive(&archive, source, manifest); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolFleetTenantRestore(ctx, map[string]any{
		"tenant_id":   id,
		"archive_b64": base64.StdEncoding.EncodeToString(archive.Bytes()),
	})
	if err == nil || !strings.Contains(err.Error(), "different tenant") {
		t.Fatalf("restore error = %v", err)
	}
}

func TestValidationRejectsShellAndPathInputs(t *testing.T) {
	for _, slug := range []string{"../acme", "acme;shutdown", "acme space", "-acme"} {
		if _, err := validatedTenantSlug(slug); err == nil {
			t.Errorf("slug %q accepted", slug)
		}
	}
	for _, version := range []string{"latest;curl", "../latest", "$(id)", "v1/latest"} {
		if _, err := validateAptevaVersion(version, false); err == nil {
			t.Errorf("version %q accepted", version)
		}
	}
}

func TestFleetEventRetentionKeepsNewestThousand(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "acme", StatusStopped)
	for i := 0; i < 1005; i++ {
		if err := app.store.recordEvent(id, "test", "test", map[string]any{"n": i}); err != nil {
			t.Fatal(err)
		}
	}
	var count int
	if err := app.store.db.QueryRow(`SELECT COUNT(*) FROM fleet_events WHERE tenant_id = ?`, id).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1000 {
		t.Fatalf("event count=%d want 1000", count)
	}
}

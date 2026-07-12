package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRetainedSourceRoundTripAndDuplicateRefusal(t *testing.T) {
	app, _ := newTestApp(t)
	id := seedTenant(t, app, "retained-roundtrip", StatusActive)
	r := &RetainedSource{
		TenantID:         id,
		SourceInstanceID: 0,
		SourceConfigDir:  filepath.Join(localDataRoot(), "retained-roundtrip"),
		SourceSlug:       "retained-roundtrip",
	}
	if err := app.store.createRetainedSource(r); err != nil {
		t.Fatalf("create retained source: %v", err)
	}
	if err := app.store.createRetainedSource(r); err == nil {
		t.Fatal("duplicate retained source unexpectedly succeeded")
	}
	got, err := app.store.getRetainedSource(id)
	if err != nil {
		t.Fatalf("get retained source: %v", err)
	}
	if got == nil || got.SourceConfigDir != r.SourceConfigDir || got.SourceInstanceID != 0 {
		t.Fatalf("retained source mismatch: %#v", got)
	}
}

func TestMigrateFinalizePreviewsThenDeletesOnlyRetainedSource(t *testing.T) {
	app, ctx := newTestApp(t)
	slug := "retained-finalize"
	id := seedTenant(t, app, slug, StatusActive)
	sourceDir := filepath.Join(localDataRoot(), slug)
	if err := os.MkdirAll(sourceDir, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(sourceDir, "marker")
	if err := os.WriteFile(marker, []byte("keep until confirmed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.store.createRetainedSource(&RetainedSource{
		TenantID:         id,
		SourceInstanceID: 0,
		SourceConfigDir:  sourceDir,
		SourceSlug:       slug,
	}); err != nil {
		t.Fatal(err)
	}
	if err := app.store.setLocation(id, 3, "http://127.0.0.1:7100", "/var/lib/apteva-fleet/"+slug); err != nil {
		t.Fatal(err)
	}

	preview, err := app.toolMigrateFinalize(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatalf("preview: %v", err)
	}
	previewMap := preview.(map[string]any)
	if previewMap["requires_confirmation"] != true {
		t.Fatalf("preview did not require confirmation: %#v", previewMap)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("preview modified retained source: %v", err)
	}

	result, err := app.toolMigrateFinalize(ctx, map[string]any{"tenant_id": id, "confirm": true})
	if err != nil {
		t.Fatalf("finalize: %v", err)
	}
	if result.(map[string]any)["finalized"] != true {
		t.Fatalf("unexpected finalize result: %#v", result)
	}
	if _, err := os.Stat(sourceDir); !os.IsNotExist(err) {
		t.Fatalf("retained source still exists or stat failed: %v", err)
	}
	retained, err := app.store.getRetainedSource(id)
	if err != nil || retained != nil {
		t.Fatalf("retained record remains: retained=%#v err=%v", retained, err)
	}
}

func TestMigrateFinalizeRefusesCurrentLocation(t *testing.T) {
	app, ctx := newTestApp(t)
	slug := "retained-current"
	id := seedTenant(t, app, slug, StatusActive)
	tenant, _, err := app.store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(tenant.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := app.store.createRetainedSource(&RetainedSource{
		TenantID:         id,
		SourceInstanceID: tenant.InstanceID,
		SourceConfigDir:  tenant.ConfigDir,
		SourceSlug:       tenant.Slug,
	}); err != nil {
		t.Fatal(err)
	}

	_, err = app.toolMigrateFinalize(ctx, map[string]any{"tenant_id": id, "confirm": true})
	if err == nil || !strings.Contains(err.Error(), "matches the tenant's current location") {
		t.Fatalf("expected current-location refusal, got %v", err)
	}
	if _, err := os.Stat(tenant.ConfigDir); err != nil {
		t.Fatalf("current tenant directory was modified: %v", err)
	}
}

func TestTenantGetIncludesRetainedSource(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "retained-get", StatusActive)
	if err := app.store.createRetainedSource(&RetainedSource{
		TenantID:         id,
		SourceInstanceID: 2,
		SourceConfigDir:  "/var/lib/apteva-fleet/retained-get",
		SourceSlug:       "retained-get",
	}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolGet(ctx, map[string]any{"tenant_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["retained_source"] == nil {
		t.Fatalf("tenant_get omitted retained source: %#v", out)
	}
}

func TestTenantDeleteRefusesPendingRetainedSource(t *testing.T) {
	app, ctx := newTestApp(t)
	id := seedTenant(t, app, "retained-delete", StatusStopped)
	if err := app.store.createRetainedSource(&RetainedSource{
		TenantID:         id,
		SourceInstanceID: 2,
		SourceConfigDir:  "/var/lib/apteva-fleet/retained-delete",
		SourceSlug:       "retained-delete",
	}); err != nil {
		t.Fatal(err)
	}
	_, err := app.toolDelete(ctx, map[string]any{"tenant_id": id, "confirm": true})
	if err == nil || !strings.Contains(err.Error(), "tenant_migrate_finalize") {
		t.Fatalf("expected retained-source deletion refusal, got %v", err)
	}
	if _, _, getErr := app.store.get(id); getErr != nil {
		t.Fatalf("tenant was deleted despite retained source: %v", getErr)
	}
}

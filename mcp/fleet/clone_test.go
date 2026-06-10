package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
)

func TestToolClone_CopiesLocalTenantWithoutTouchingSource(t *testing.T) {
	t.Setenv("FLEET_DATA_ROOT", t.TempDir())
	app, ctx := newTestApp(t)
	sourceID := seedTenant(t, app, "source", StatusActive)
	source, _, err := app.store.get(sourceID)
	if err != nil {
		t.Fatalf("get source: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(source.ConfigDir, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source.ConfigDir, "nested", "state.txt"), []byte("tenant state"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceDBPath := filepath.Join(source.ConfigDir, "apteva.db")
	sourceDB, err := sql.Open("sqlite", sourceDBPath)
	if err != nil {
		t.Fatalf("open source sqlite: %v", err)
	}
	if _, err := sourceDB.Exec(`CREATE TABLE clone_check (value TEXT); INSERT INTO clone_check (value) VALUES ('from sqlite');`); err != nil {
		_ = sourceDB.Close()
		t.Fatalf("seed source sqlite: %v", err)
	}
	if err := sourceDB.Close(); err != nil {
		t.Fatalf("close source sqlite: %v", err)
	}
	if err := os.WriteFile(sourceDBPath+"-wal", []byte("stale wal should not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sourceDBPath+"-shm", []byte("stale shm should not copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := app.store.setDomain(source.ID, "source.example.com", "example.com|A", source.CreatedAt); err != nil {
		t.Fatalf("set source domain: %v", err)
	}
	before, _, err := app.store.get(sourceID)
	if err != nil {
		t.Fatalf("get source before clone: %v", err)
	}

	out, err := app.toolClone(ctx, map[string]any{
		"source_tenant_id": sourceID,
		"slug":             "source-copy",
		"start":            false,
	})
	if err != nil {
		t.Fatalf("toolClone: %v", err)
	}
	m, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("clone response has type %T", out)
	}
	cloneID, _ := m["tenant_id"].(string)
	if cloneID == "" || cloneID == sourceID {
		t.Fatalf("bad clone id %q", cloneID)
	}

	after, _, err := app.store.get(sourceID)
	if err != nil {
		t.Fatalf("get source after clone: %v", err)
	}
	if after.Slug != before.Slug ||
		after.BaseURL != before.BaseURL ||
		after.ConfigDir != before.ConfigDir ||
		after.Status != before.Status ||
		after.Domain != before.Domain ||
		after.DomainRecordID != before.DomainRecordID ||
		!after.UpdatedAt.Equal(before.UpdatedAt) {
		t.Fatalf("source row changed after clone\nbefore=%+v\nafter=%+v", before, after)
	}
	sourceEvents, err := app.store.recentEvents(sourceID, 10)
	if err != nil {
		t.Fatalf("source events: %v", err)
	}
	if len(sourceEvents) != 0 {
		t.Fatalf("clone should not write source events, got %d", len(sourceEvents))
	}
	raw, err := os.ReadFile(filepath.Join(source.ConfigDir, "nested", "state.txt"))
	if err != nil {
		t.Fatalf("read source file after clone: %v", err)
	}
	if string(raw) != "tenant state" {
		t.Fatalf("source file changed: %q", string(raw))
	}

	clone, cloneKeyEnc, err := app.store.get(cloneID)
	if err != nil {
		t.Fatalf("get clone: %v", err)
	}
	if clone.Slug != "source-copy" {
		t.Fatalf("clone slug = %q", clone.Slug)
	}
	if clone.Status != StatusStopped {
		t.Fatalf("clone status = %q, want stopped", clone.Status)
	}
	if clone.Domain != "" || clone.DomainRecordID != "" || clone.DomainAttachedAt != nil {
		t.Fatalf("clone copied domain fields: %+v", clone)
	}
	if clone.ConfigDir == "" || clone.ConfigDir == source.ConfigDir {
		t.Fatalf("clone config dir = %q, source = %q", clone.ConfigDir, source.ConfigDir)
	}
	copied, err := os.ReadFile(filepath.Join(clone.ConfigDir, "nested", "state.txt"))
	if err != nil {
		t.Fatalf("read cloned file: %v", err)
	}
	if string(copied) != "tenant state" {
		t.Fatalf("cloned file = %q", string(copied))
	}
	if _, err := os.Stat(filepath.Join(clone.ConfigDir, "apteva.db-wal")); !os.IsNotExist(err) {
		t.Fatalf("clone should not copy sqlite WAL sidecar, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(clone.ConfigDir, "apteva.db-shm")); !os.IsNotExist(err) {
		t.Fatalf("clone should not copy sqlite SHM sidecar, stat err=%v", err)
	}
	cloneDB, err := sql.Open("sqlite", filepath.Join(clone.ConfigDir, "apteva.db"))
	if err != nil {
		t.Fatalf("open cloned sqlite: %v", err)
	}
	var dbValue string
	if err := cloneDB.QueryRow(`SELECT value FROM clone_check`).Scan(&dbValue); err != nil {
		_ = cloneDB.Close()
		t.Fatalf("query cloned sqlite: %v", err)
	}
	if err := cloneDB.Close(); err != nil {
		t.Fatalf("close cloned sqlite: %v", err)
	}
	if dbValue != "from sqlite" {
		t.Fatalf("cloned sqlite value = %q", dbValue)
	}
	cloneKey, err := app.keys.open(cloneKeyEnc)
	if err != nil {
		t.Fatalf("open cloned api key: %v", err)
	}
	if string(cloneKey) != "sk-fake" {
		t.Fatalf("clone api key = %q", string(cloneKey))
	}
}

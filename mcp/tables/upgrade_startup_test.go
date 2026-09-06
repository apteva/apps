package main

import (
	"context"
	"testing"
)

func TestLegacyUpgradeTransactionFailureAndResume(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedLegacyTable(t, ctx, false)
	db := ctx.AppDB()
	// Fail after ALTER and identity creation, proving their rollback is atomic.
	if _, err := db.Exec(`CREATE TRIGGER t_41_identity_insert AFTER INSERT ON t_41 BEGIN SELECT 1; END`); err != nil {
		t.Fatal(err)
	}
	if err := app.upgradeAll(ctx); err == nil {
		t.Fatal("expected conflicting trigger to abort migration")
	}
	var version, columns, identities int
	if err := db.QueryRow(`SELECT storage_version FROM tables_meta WHERE id=41`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM pragma_table_info('t_41') WHERE name='_revision'`).Scan(&columns); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM row_identity`).Scan(&identities); err != nil {
		t.Fatal(err)
	}
	if version != 0 || columns != 0 || identities != 0 {
		t.Fatalf("partial migration committed: %d %d %d", version, columns, identities)
	}
	if _, err := db.Exec(`DROP TRIGGER t_41_identity_insert`); err != nil {
		t.Fatal(err)
	}
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal(err)
	}
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal("not resumable", err)
	}
	var raw string
	if err := db.QueryRow(`SELECT at FROM t_41 WHERE id=17`).Scan(&raw); err != nil || raw != "2026-01-01T01:00:00.1+01:00" {
		t.Fatalf("legacy timestamp changed %q %v", raw, err)
	}
	// Simulate old SQL writes, which know nothing about revision counters.
	if _, err := db.Exec(`UPDATE t_41 SET revision='old writer' WHERE id=17`); err != nil {
		t.Fatal(err)
	}
	row := mustCall(t, app, ctx, "rows_get", map[string]any{"table": "legacy", "id": 17})["row"].(map[string]any)
	if row["_revision"] != int64(2) {
		t.Fatal("old write did not advance revision", row)
	}
}
func TestLegacyMigrationCanceledBeforeMutation(t *testing.T) {
	ctx := newTestCtx(t)
	seedLegacyTable(t, ctx, false)
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	activeContexts.Store(ctx, canceled)
	defer activeContexts.Delete(ctx)
	err := upgradeTable(ctx, &Table{ID: 41, PhysicalName: "t_41"})
	if err == nil {
		t.Fatal("canceled migration succeeded")
	}
	var version int
	if err := ctx.AppDB().QueryRow(`SELECT storage_version FROM tables_meta WHERE id=41`).Scan(&version); err != nil || version != 0 {
		t.Fatalf("canceled migration changed metadata %d %v", version, err)
	}
}

func TestLegacyUpgradeResumesAfterCommittedTable(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	seedLegacyTable(t, ctx, false)
	db := ctx.AppDB()
	for _, q := range []string{
		`INSERT INTO tables_meta(id,project_id,scope,name,physical_name,row_count) VALUES(42,'test-proj','project','second','t_42',0)`,
		`CREATE TABLE t_42(id INTEGER PRIMARY KEY,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP)`,
		`CREATE TRIGGER t_42_identity_insert AFTER INSERT ON t_42 BEGIN SELECT 1; END`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.upgradeAll(ctx); err == nil {
		t.Fatal("second table should fail")
	}
	var first, second int
	if err := db.QueryRow("SELECT storage_version FROM tables_meta WHERE id=41").Scan(&first); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow("SELECT storage_version FROM tables_meta WHERE id=42").Scan(&second); err != nil {
		t.Fatal(err)
	}
	if first != 1 || second != 0 {
		t.Fatalf("wrong resume markers %d %d", first, second)
	}
	if _, err := db.Exec("DROP TRIGGER t_42_identity_insert"); err != nil {
		t.Fatal(err)
	}
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal("resume", err)
	}
	if err := db.QueryRow("SELECT count(*) FROM tables_meta WHERE storage_version=1 AND legacy_storage=1").Scan(&first); err != nil || first != 2 {
		t.Fatalf("resume incomplete: %d %v", first, err)
	}
}

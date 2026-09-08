package main

import (
	"database/sql"
	"encoding/json"
	"strings"
	"testing"
)

func TestLegacyRawTextDefaultsUpgradeAndLoaders(t *testing.T) {
	ctx := newTestCtx(t)
	db := ctx.AppDB()
	app := &App{}
	seedLegacyTable(t, ctx, false)
	if err := app.upgradeAll(ctx); err != nil {
		t.Fatal(err)
	} // Already committed work must survive retry.
	for _, q := range []string{
		`INSERT INTO tables_meta(id,project_id,scope,name,physical_name,row_count) VALUES(42,'test-proj','project','raw_defaults','t_42',1)`,
		`INSERT INTO columns_meta(table_id,name,type,nullable,default_value,position) VALUES(42,'kind','text',0,'standard',0),(42,'currency','text',0,'EUR',1)`,
		`CREATE TABLE t_42(id INTEGER PRIMARY KEY,created_at TEXT DEFAULT CURRENT_TIMESTAMP,updated_at TEXT DEFAULT CURRENT_TIMESTAMP,kind TEXT NOT NULL DEFAULT 'standard',currency TEXT NOT NULL DEFAULT 'EUR')`,
		`CREATE UNIQUE INDEX ux_t_42_kind ON t_42(kind)`,
		`INSERT INTO t_42(id,kind,currency) VALUES(37,'existing','GBP')`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal("resume with legacy text defaults", err)
	}
	check := func(cols []Column) {
		t.Helper()
		if len(cols) != 2 || cols[0].Default != "standard" || cols[1].Default != "EUR" {
			t.Fatalf("defaults changed: %+v", cols)
		}
	}
	cols, err := loadColumns(db, 42)
	if err != nil {
		t.Fatal(err)
	}
	check(cols)
	tables, err := loadTables(db, "test-proj")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if table.ID == 42 {
			check(table.Columns)
		}
	}
	table, err := app.loadTableSchema(ctx, "test-proj", "raw_defaults")
	if err != nil {
		t.Fatal(err)
	}
	check(table.Columns)
	var kind, currency string
	if err := db.QueryRow(`SELECT kind,currency FROM t_42 WHERE id=37`).Scan(&kind, &currency); err != nil || kind != "existing" || currency != "GBP" {
		t.Fatal("existing row changed", kind, currency, err)
	}
	inserted := mustCall(t, app, ctx, "rows_insert", map[string]any{"table": "raw_defaults", "rows": []any{map[string]any{}}})
	id := inserted["ids"].([]int64)[0]
	if id <= 37 {
		t.Fatal("identity reused", id)
	}
	if err := db.QueryRow(`SELECT kind,currency FROM t_42 WHERE id=?`, id).Scan(&kind, &currency); err != nil || kind != "standard" || currency != "EUR" {
		t.Fatal("defaults not applied", kind, currency, err)
	}
	// Legacy metadata, rows, indexes and the already-migrated first table stay intact.
	var raw string
	if err := db.QueryRow(`SELECT default_value FROM columns_meta WHERE table_id=42 AND name='kind'`).Scan(&raw); err != nil || raw != "standard" {
		t.Fatal("metadata rewritten", raw, err)
	}
	if err := (&App{}).OnMount(ctx); err != nil {
		t.Fatal("second startup", err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM tables_meta WHERE storage_version=1`).Scan(&count); err != nil || count != 2 {
		t.Fatal(count, err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='index' AND name='ux_t_42_kind'`).Scan(&count); err != nil || count != 1 {
		t.Fatal("index lost", count, err)
	}
}

func TestColumnDefaultsKeepJSONTypesAndRejectMalformedNonText(t *testing.T) {
	for _, tc := range []struct {
		name, typ, raw string
		want           any
		fail           bool
	}{
		{"raw text", "text", "standard", "standard", false},
		{"quoted text", "text", `"EUR"`, "EUR", false},
		{"empty string", "text", `""`, "", false},
		{"no default", "text", "", nil, false},
		{"null", "text", "null", nil, false},
		{"number", "number", "9007199254740993", json.Number("9007199254740993"), false},
		{"bool", "bool", "true", true, false},
		{"bad number", "number", "oops", nil, true},
		{"bad bool", "bool", "oops", nil, true},
		{"bad json", "json", "oops", nil, true},
		{"bad datetime", "datetime", "oops", nil, true},
		{"bad file id", "file_id", "oops", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := newTestCtx(t)
			db := ctx.AppDB()
			if _, err := db.Exec(`INSERT INTO tables_meta(id,project_id,name,physical_name) VALUES(90,'test-proj','defaults','t_90')`); err != nil {
				t.Fatal(err)
			}
			if _, err := db.Exec(`INSERT INTO columns_meta(table_id,name,type,default_value,position) VALUES(90,'value',?,?,0)`, tc.typ, tc.raw); err != nil {
				t.Fatal(err)
			}
			cols, err := loadColumns(db, 90)
			if tc.fail {
				if err == nil || !strings.Contains(err.Error(), "value") {
					t.Fatal("missing contextual error", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if cols[0].Default != tc.want {
				t.Fatalf("got %#v want %#v", cols[0].Default, tc.want)
			}
		})
	}
}

func TestSQLNullDefaultRemainsAbsent(t *testing.T) {
	ctx := newTestCtx(t)
	if _, err := ctx.AppDB().Exec(`INSERT INTO tables_meta(id,project_id,name,physical_name) VALUES(91,'test-proj','null_default','t_91'); INSERT INTO columns_meta(table_id,name,type,default_value,position) VALUES(91,'value','text',NULL,0)`); err != nil {
		t.Fatal(err)
	}
	var raw sql.NullString
	if err := ctx.AppDB().QueryRow(`SELECT default_value FROM columns_meta WHERE table_id=91`).Scan(&raw); err != nil || raw.Valid {
		t.Fatal(raw, err)
	}
	cols, err := loadColumns(ctx.AppDB(), 91)
	if err != nil || cols[0].Default != nil {
		t.Fatal(cols, err)
	}
}

package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"modernc.org/sqlite"
)

const executionMigration = "005_execution_identity.sql"

// 1.8.0's SDK committed individual statements without their migration receipt.
// Keep 005 out of the SDK SQL directory: an unconditional ADD COLUMN there
// prevents OnMount from ever recovering a partially applied upgrade.
// All repairs and the original receipt commit in one transaction. Existing
// instance/artifact identities MUST survive retries and successful upgrades.
func migrateExecutionIdentity(ctx context.Context, db *sql.DB, progress func(int, int)) error {
	for {
		err := migrateExecutionIdentityOnce(ctx, db, progress)
		var sqliteErr *sqlite.Error
		if !errors.As(err, &sqliteErr) || sqliteErr.Code()&255 != 5 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type executionColumn struct {
	table, name, kind string
	notNull           int
	defaultSQL        string
}

var executionColumns = []executionColumn{
	{"functions", "instance_key", "TEXT", 1, "''"},
	{"functions", "deployment_revision", "INTEGER", 1, "0"},
	{"functions", "access_json", "TEXT", 0, ""},
	{"function_versions", "artifact_key", "TEXT", 1, "''"},
	{"function_versions", "deployment_revision", "INTEGER", 1, "0"},
	{"function_versions", "package_lock", "TEXT", 0, ""},
	{"function_invocations", "version_id", "INTEGER", 0, ""},
	{"function_invocations", "config_hash", "TEXT", 0, ""},
	{"function_invocations", "truncated", "INTEGER", 1, "0"},
	{"function_invocations", "build_ms", "INTEGER", 1, "0"},
	{"function_invocations", "queue_ms", "INTEGER", 1, "0"},
	{"function_invocations", "cold_start_ms", "INTEGER", 1, "0"},
	{"function_invocations", "execution_ms", "INTEGER", 1, "0"},
}

func migrateExecutionIdentityOnce(ctx context.Context, db *sql.DB, progress func(int, int)) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS _migrations(filename TEXT PRIMARY KEY, applied_at DATETIME DEFAULT CURRENT_TIMESTAMP)`); err != nil {
		return err
	}
	// Acquire the SQLite write reservation before inspecting schema/receipt.
	// A second startup must observe the first startup's completed transaction.
	if _, err = tx.ExecContext(ctx, "UPDATE _migrations SET filename=filename WHERE 0"); err != nil {
		return err
	}
	var seen string
	err = tx.QueryRowContext(ctx, "SELECT filename FROM _migrations WHERE filename=?", executionMigration).Scan(&seen)
	if err == nil {
		return nil
	}
	if err != sql.ErrNoRows {
		return err
	}
	total := len(executionColumns) + 3
	step := func(n int) {
		if progress != nil {
			progress(n, total)
		}
	}
	step(0)
	for i, col := range executionColumns {
		var kind string
		var notNull int
		var def sql.NullString
		err = tx.QueryRowContext(ctx, `SELECT type, "notnull", dflt_value FROM pragma_table_info(?) WHERE name=?`, col.table, col.name).Scan(&kind, &notNull, &def)
		switch {
		case err == sql.ErrNoRows:
			// Identifiers and definitions are fixed literals, never caller input.
			query := "ALTER TABLE " + col.table + " ADD COLUMN " + col.name + " " + col.kind
			if col.notNull != 0 {
				query += " NOT NULL"
			}
			if col.defaultSQL != "" {
				query += " DEFAULT " + col.defaultSQL
			}
			if _, err = tx.ExecContext(ctx, query); err != nil {
				return fmt.Errorf("%s add %s: %w", executionMigration, col.name, err)
			}
		case err != nil:
			return err
		case !strings.EqualFold(kind, col.kind) || notNull != col.notNull || def.String != col.defaultSQL:
			return fmt.Errorf("%s: incompatible existing column %s.%s", executionMigration, col.table, col.name)
		}
		step(i + 1)
	}
	// These backfills also recover an interruption between ADD COLUMN and UPDATE.
	// Never regenerate committed identities or overwrite a version's artifact key.
	if _, err = tx.ExecContext(ctx, `UPDATE functions SET instance_key=lower(hex(randomblob(16))) WHERE instance_key=''`); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE function_versions SET artifact_key=(SELECT instance_key FROM functions WHERE id=function_id) WHERE artifact_key=''`); err != nil {
		return err
	}
	step(len(executionColumns) + 1)
	if _, err = tx.ExecContext(ctx, `CREATE INDEX IF NOT EXISTS ix_inv_fn_id ON function_invocations(project_id,function_id,id DESC)`); err != nil {
		return fmt.Errorf("%s index: %w", executionMigration, err)
	}
	// IF NOT EXISTS alone would accept a conflicting index with the same name.
	rows, err := tx.QueryContext(ctx, `SELECT name, desc FROM pragma_index_xinfo('ix_inv_fn_id') WHERE key=1 ORDER BY seqno`)
	if err != nil {
		return err
	}
	var cols []string
	for rows.Next() {
		var name string
		var desc int
		if err = rows.Scan(&name, &desc); err != nil {
			rows.Close()
			return err
		}
		cols = append(cols, fmt.Sprintf("%s:%d", name, desc))
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	var table string
	var unique, partial int
	if err = tx.QueryRowContext(ctx, `SELECT tbl_name FROM sqlite_master WHERE type='index' AND name='ix_inv_fn_id'`).Scan(&table); err != nil {
		return err
	}
	if err = tx.QueryRowContext(ctx, `SELECT "unique", partial FROM pragma_index_list('function_invocations') WHERE name='ix_inv_fn_id'`).Scan(&unique, &partial); err != nil {
		return err
	}
	if table != "function_invocations" || unique != 0 || partial != 0 || strings.Join(cols, ",") != "project_id:0,function_id:0,id:1" {
		return fmt.Errorf("%s: incompatible ix_inv_fn_id index", executionMigration)
	}
	step(len(executionColumns) + 2)
	if _, err = tx.ExecContext(ctx, "INSERT INTO _migrations(filename) VALUES(?)", executionMigration); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	step(total)
	return nil
}

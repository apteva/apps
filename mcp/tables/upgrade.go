package main

import (
	"context"
	"database/sql"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) upgradeAll(ctx *sdk.AppCtx) error {
	parent, cancel := context.WithTimeout(context.Background(), time.Duration(cfgInt64Range(ctx, "migration_timeout_ms", 300000, 1, 3600000))*time.Millisecond)
	defer cancel()
	scoped := ctx.WithProject(ctx.CurrentProject())
	activeContexts.Store(scoped, parent)
	defer activeContexts.Delete(scoped)
	if err := a.schemaMu.acquire(parent, true); err != nil {
		return err
	}
	defer a.schemaMu.Unlock()

	rows, err := ctx.AppReadDB().QueryContext(parent, "SELECT project_id,name FROM tables_meta WHERE project_id<>'' AND (storage_version<>1 OR row_count IS NULL)")
	if err != nil {
		return err
	}
	pending := []schemaCacheKey{}
	for rows.Next() {
		var key schemaCacheKey
		if err := rows.Scan(&key.projectID, &key.tableName); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, key)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, key := range pending {
		table, err := a.loadTableSchema(scoped, key.projectID, key.tableName)
		if err != nil {
			return fmt.Errorf("upgrade table %s: %w", key.tableName, err)
		}
		if _, err := currentRowCount(scoped, table); err != nil {
			return err
		}
	}

	return nil
}

func initializeCountTx(tx *writeTx, t *Table) (int64, error) {
	if count, ok := tx.counts[t.ID]; ok {
		return count, nil
	}
	// Acquire SQLite's writer before reading; COUNT and publication share a snapshot.
	if _, err := tx.Exec("UPDATE tables_meta SET row_count=row_count WHERE id=?", t.ID); err != nil {
		return 0, err
	}
	var count sql.NullInt64
	if err := tx.QueryRow("SELECT row_count FROM tables_meta WHERE id=?", t.ID).Scan(&count); err != nil {
		return 0, err
	}
	if count.Valid {
		tx.counts[t.ID] = count.Int64
		return count.Int64, nil
	}
	if err := tx.QueryRow("SELECT COUNT(*) FROM " + quote(t.PhysicalName)).Scan(&count.Int64); err != nil {
		return 0, err
	}
	_, err := tx.Exec("UPDATE tables_meta SET row_count=? WHERE id=?", count.Int64, t.ID)
	if err == nil {
		tx.counts[t.ID] = count.Int64
	}
	return count.Int64, err
}

func normalizeTimestamp(v any) (any, error) {
	if v == nil {
		return nil, nil
	}
	s := fmt.Sprint(v)
	if b, ok := v.([]byte); ok {
		s = string(b)
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(timestampLayout), nil
		}
	}
	return nil, errf("cannot migrate invalid datetime %q", s)
}

// Upgrades are atomic, resumable, and retain row IDs and SQLite index definitions.
// New tables already use version 1. Called only on cache misses and at mount.
func upgradeTable(ctx *sdk.AppCtx, t *Table) error {
	var version int
	if err := ctx.AppReadDB().QueryRowContext(requestContext(ctx), "SELECT storage_version FROM tables_meta WHERE id=?", t.ID).Scan(&version); err != nil {
		return err
	}
	if version == 1 {
		return nil
	}
	if version != 0 {
		return errf("unsupported table storage version %d", version)
	}
	tx, err := beginWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE tables_meta SET storage_version=storage_version WHERE id=?", t.ID); err != nil {
		return err
	}
	if err = tx.QueryRow("SELECT storage_version FROM tables_meta WHERE id=?", t.ID).Scan(&version); err != nil {
		return err
	}
	if version == 1 {
		return tx.Commit()
	}
	indexes, err := tx.Query("SELECT sql FROM sqlite_master WHERE type='index' AND tbl_name=? AND sql IS NOT NULL", t.PhysicalName)
	if err != nil {
		return err
	}
	definitions := []string{}
	for indexes.Next() {
		var ddl string
		if err = indexes.Scan(&ddl); err != nil {
			indexes.Close()
			return err
		}
		definitions = append(definitions, ddl)
	}
	err = indexes.Err()
	indexes.Close()
	if err != nil {
		return err
	}
	temp := t.PhysicalName + "_upgrade"
	ddl, err := buildCreateTableSQL(temp, t.Columns)
	if err != nil {
		return err
	}
	if _, err = tx.Exec(ddl); err != nil {
		return err
	}
	cols := []string{"id", "created_at", "updated_at"}
	for _, col := range t.Columns {
		cols = append(cols, col.Name)
	}
	quoted := make([]string, len(cols))
	for i, c := range cols {
		quoted[i] = quote(c)
	}
	source, err := tx.Query("SELECT " + strings.Join(quoted, ",") + " FROM " + quote(t.PhysicalName))
	if err != nil {
		return err
	}
	insert, err := tx.Prepare("INSERT INTO " + quote(temp) + " (" + strings.Join(quoted, ",") + ") VALUES (" + strings.TrimSuffix(strings.Repeat("?,", len(cols)), ",") + ")")
	if err != nil {
		source.Close()
		return err
	}
	defer insert.Close()
	for source.Next() {
		values := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range values {
			ptrs[i] = &values[i]
		}
		if err = source.Scan(ptrs...); err != nil {
			source.Close()
			return err
		}
		for i := 1; i < len(values); i++ {
			if i < 3 || t.Columns[i-3].Type == "datetime" {
				values[i], err = normalizeTimestamp(values[i])
				if err != nil {
					source.Close()
					return err
				}
			}
		}
		if _, err = insert.ExecContext(tx.ctx, values...); err != nil {
			source.Close()
			return err
		}
	}
	err = source.Err()
	source.Close()
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DROP TABLE " + quote(t.PhysicalName)); err != nil {
		return err
	}
	if _, err = tx.Exec("ALTER TABLE " + quote(temp) + " RENAME TO " + quote(t.PhysicalName)); err != nil {
		return err
	}
	for _, ddl := range definitions {
		if _, err = tx.Exec(ddl); err != nil {
			return err
		}
	}
	if _, err = initializeCountTx(tx, t); err != nil {
		return err
	}
	if err = reconcileIndexes(tx, t); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE tables_meta SET storage_version=1 WHERE id=?", t.ID); err != nil {
		return err
	}
	return tx.Commit()
}

// Record historical managed indexes even if their key has never been upserted
// since upgrade. Introspection uses bound arguments, not caller SQL.
func reconcileIndexes(tx *writeTx, t *Table) error {
	rows, err := tx.Query("SELECT name FROM sqlite_master WHERE type='index' AND tbl_name=? AND name LIKE 'ux_%'", t.PhysicalName)
	if err != nil {
		return err
	}
	names := []string{}
	for rows.Next() {
		var n string
		if err = rows.Scan(&n); err != nil {
			rows.Close()
			return err
		}
		names = append(names, n)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, name := range names {
		var count int
		if err = tx.QueryRow("SELECT COUNT(*) FROM indexes_meta WHERE physical_name=?", name).Scan(&count); err != nil {
			return err
		}
		if count > 0 {
			continue
		}
		info, err := tx.Query("SELECT name,desc FROM pragma_index_xinfo(?) WHERE key=1 ORDER BY seqno", name)
		if err != nil {
			return err
		}
		columns := []IndexColumn{}
		for info.Next() {
			var col sql.NullString
			var desc int
			if err = info.Scan(&col, &desc); err != nil {
				info.Close()
				return err
			}
			if !col.Valid {
				info.Close()
				return errf("expression index cannot be registered")
			}
			direction := "asc"
			if desc != 0 {
				direction = "desc"
			}
			columns = append(columns, IndexColumn{Col: col.String, Order: direction})
		}
		err = info.Err()
		info.Close()
		if err != nil {
			return err
		}
		logical := managedIndexName(columns)
		// Historical renamed indexes may have stale physical hashes. Metadata names
		// are reconciled from their actual columns, never inferred from the hash.
		var existing int
		if err = tx.QueryRow("SELECT COUNT(*) FROM indexes_meta WHERE table_id=? AND name=?", t.ID, logical).Scan(&existing); err != nil {
			return err
		}
		if existing > 0 {
			logical = "managed_legacy_" + digest(name)
		}
		result, err := tx.Exec("INSERT INTO indexes_meta(table_id,name,physical_name,unique_index,managed) VALUES(?,?,?,1,1)", t.ID, logical, name)
		if err != nil {
			return err
		}
		id, err := result.LastInsertId()
		if err != nil {
			return err
		}
		if err = insertIndexColumns(tx, id, columns); err != nil {
			return err
		}
	}
	return nil
}

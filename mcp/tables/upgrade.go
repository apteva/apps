package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"modernc.org/sqlite"
	"time"
)

func (a *App) upgradeAll(ctx *sdk.AppCtx) error {
	parent, cancel := context.WithTimeout(ctx.StartupContext(), time.Duration(cfgInt64Range(ctx, "migration_timeout_ms", 300000, 1, 3600000))*time.Millisecond)
	defer cancel()
	scoped := ctx.WithProject(ctx.CurrentProject())
	activeContexts.Store(scoped, parent)
	defer activeContexts.Delete(scoped)
	if err := a.schemaMu.acquire(parent, true); err != nil {
		return err
	}
	defer a.schemaMu.Unlock()

	rows, err := ctx.AppReadDB().QueryContext(parent, "SELECT project_id,name FROM tables_meta WHERE project_id<>'' AND (storage_version<>1 OR row_count IS NULL) ORDER BY id")
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
	ctx.ReportStartupProgress("tables", 0, int64(len(pending)))
	for i, key := range pending {
		var table *Table
		var err error
		for {
			table, err = a.loadTableSchema(scoped, key.projectID, key.tableName)
			var busy *sqlite.Error
			if !errors.As(err, &busy) || busy.Code()&255 != 5 {
				break
			}
			ctx.ReportStartupProgress("waiting_for_writer", int64(i), int64(len(pending)))
			select {
			case <-parent.Done():
				return parent.Err()
			case <-time.After(25 * time.Millisecond):
			}
		}
		if err != nil {
			return fmt.Errorf("upgrade table %s: %w", key.tableName, err)
		}
		if _, err := currentRowCount(scoped, table); err != nil {
			return err
		}
		ctx.ReportStartupProgress("tables", int64(i+1), int64(len(pending)))
		ctx.Logger().Info("migration progress", "completed", i+1, "total", len(pending))
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
	if err := ctx.AppReadDB().QueryRowContext(requestContext(ctx), "SELECT storage_version,legacy_storage FROM tables_meta WHERE id=?", t.ID).Scan(&version, &t.LegacyStorage); err != nil {
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
	if err = tx.QueryRow("SELECT storage_version,legacy_storage FROM tables_meta WHERE id=?", t.ID).Scan(&version, &t.LegacyStorage); err != nil {
		return err
	}
	if version == 1 {
		return tx.Commit()
	}
	// SQLite adds this constant-default column without copying existing rows.
	// Preserve timestamp bytes and index roots so a 0.1.14 fallback remains usable.
	for _, c := range t.Columns {
		if c.Name == "_revision" {
			return errf("legacy user column _revision conflicts with reserved revision column")
		}
	}
	if _, err = tx.Exec("ALTER TABLE " + quote(t.PhysicalName) + " ADD COLUMN _revision INTEGER NOT NULL DEFAULT 1"); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO row_identity(table_id,last_id) SELECT ?,COALESCE(MAX(id),0) FROM "+quote(t.PhysicalName), t.ID); err != nil {
		return err
	}
	// Old writers omit revision and identity bookkeeping. SQL-only triggers remain
	// executable by the old binary and keep the new binary's invariants current.
	if _, err = tx.Exec(fmt.Sprintf("CREATE TRIGGER %s AFTER UPDATE ON %s WHEN NEW._revision=OLD._revision BEGIN UPDATE %s SET _revision=OLD._revision+1 WHERE id=NEW.id; END", quote(t.PhysicalName+"_revision_update"), quote(t.PhysicalName), quote(t.PhysicalName))); err != nil {
		return err
	}
	if _, err = tx.Exec(fmt.Sprintf("CREATE TRIGGER %s AFTER INSERT ON %s BEGIN UPDATE row_identity SET last_id=MAX(last_id,NEW.id) WHERE table_id=%d; END", quote(t.PhysicalName+"_identity_insert"), quote(t.PhysicalName), t.ID)); err != nil {
		return err
	}
	if _, err = initializeCountTx(tx, t); err != nil {
		return err
	}
	if err = reconcileIndexes(tx, t); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE tables_meta SET storage_version=1,legacy_storage=1 WHERE id=?", t.ID); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	t.LegacyStorage = true
	return nil
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

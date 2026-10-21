package sdk

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"modernc.org/sqlite"
	"regexp"
	"strings"
	"time"
)

// A migration and its receipt commit together, including legacy table-rebuild
// scripts with their own transaction wrappers. Trigger BEGIN/END bodies survive.
func applyMigration(db *sql.DB, filename, body string) error {
	return applyMigrationContext(context.Background(), db, filename, body)
}

func applyMigrationContext(ctx context.Context, db *sql.DB, filename, body string) error {
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	var foreignKeys int
	if err = conn.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return err
	}
	disableFK := false
	lines := strings.Split(body, "\n")
	inTrigger := false
	triggerStart := regexp.MustCompile(`(?i)^CREATE\s+(?:(?:TEMP|TEMPORARY)\s+)?TRIGGER\b`)
	for i, line := range lines {
		stmt := strings.ToUpper(strings.TrimSpace(strings.SplitN(line, "--", 2)[0]))
		if triggerStart.MatchString(stmt) {
			inTrigger = true
		}
		if inTrigger {
			if stmt == "END;" {
				inTrigger = false
			}
			continue
		}
		switch stmt {
		case "BEGIN;", "BEGIN TRANSACTION;", "BEGIN IMMEDIATE;", "BEGIN IMMEDIATE TRANSACTION;", "COMMIT;", "END TRANSACTION;":
			lines[i] = ""
		case "PRAGMA FOREIGN_KEYS = OFF;", "PRAGMA FOREIGN_KEYS=OFF;", "PRAGMA FOREIGN_KEYS = 0;", "PRAGMA FOREIGN_KEYS=0;":
			disableFK = true
			lines[i] = ""
		case "PRAGMA FOREIGN_KEYS = ON;", "PRAGMA FOREIGN_KEYS=ON;", "PRAGMA FOREIGN_KEYS = 1;", "PRAGMA FOREIGN_KEYS=1;":
			lines[i] = ""
		}
	}
	if disableFK {
		if _, err = conn.ExecContext(ctx, "PRAGMA foreign_keys=OFF"); err != nil {
			return err
		}
		defer conn.ExecContext(context.Background(), fmt.Sprintf("PRAGMA foreign_keys=%d", foreignKeys))
	}
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, strings.Join(lines, "\n")); err != nil {
		return fmt.Errorf("migration %s: %w", filename, err)
	}
	if disableFK {
		rows, e := tx.QueryContext(ctx, "PRAGMA foreign_key_check")
		if e != nil {
			return e
		}
		bad := rows.Next()
		e = rows.Err()
		rows.Close()
		if e != nil {
			return e
		}
		if bad {
			return fmt.Errorf("migration %s violates foreign keys", filename)
		}
	}
	if _, err = tx.ExecContext(ctx, "INSERT INTO _migrations(filename) VALUES (?)", filename); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", filename, err)
	}
	return nil
}

// A short busy wait makes SQLite cancellation responsive. Retry only atomic
// startup units; never replay an arbitrary application write after partial work.
func retryStartupBusy(ctx context.Context, operation func() error) error {
	if ctx.Done() == nil {
		return operation()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := operation()
		var sqliteErr *sqlite.Error
		if err == nil || !errors.As(err, &sqliteErr) || (sqliteErr.Code()&255) != 5 {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(25 * time.Millisecond):
		}
	}
}

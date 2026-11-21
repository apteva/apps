package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
)

// RemapScopes rewrites the scope keys in the control registry under root (a
// closed engine directory, as restored from a snapshot). Database records are
// stored under database ids, never under scopes, so this is all a restore to
// a new project or new caller installs has to change. remap returns the new
// scope and true for a scope that moves; others stay.
//
// It runs in one transaction that also records restoreID in the registry's
// settings, so applying the same restore again changes nothing: the app
// database that records the SDK's restore marker is a different file, and a
// crash between the two commits must not move scopes twice. It returns how
// many distinct scopes moved.
func RemapScopes(ctx context.Context, root, restoreID string, remap func(scope string) (string, bool)) (int, error) {
	if restoreID == "" {
		return 0, Invalid("restore id required")
	}
	path := filepath.Join(root, "control.db")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return 0, nil
	}
	db, err := openSQL(path)
	if err != nil {
		return 0, err
	}
	defer db.Close()
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	marker := "restore:" + restoreID
	var seen int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM settings WHERE name=?`, marker).Scan(&seen); err != nil {
		return 0, err
	}
	if seen > 0 {
		return 0, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT scope FROM databases UNION SELECT scope FROM app_access`)
	if err != nil {
		return 0, err
	}
	moves := map[string]string{}
	for rows.Next() {
		var scope string
		if err := rows.Scan(&scope); err != nil {
			rows.Close()
			return 0, err
		}
		if next, ok := remap(scope); ok && next != scope {
			moves[scope] = next
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	// Two passes through placeholders, so a scope that is both moved and the
	// target of another move is never rewritten twice.
	i := 0
	placeholders := map[string]string{}
	for old, next := range moves {
		i++
		tmp := fmt.Sprintf("\x00restore-%d", i)
		placeholders[tmp] = next
		for _, table := range []string{"databases", "app_access"} {
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET scope=? WHERE scope=?`, tmp, old); err != nil {
				return 0, fmt.Errorf("remap scope in %s: %w", table, err)
			}
		}
	}
	for tmp, next := range placeholders {
		for _, table := range []string{"databases", "app_access"} {
			if _, err := tx.ExecContext(ctx, `UPDATE `+table+` SET scope=? WHERE scope=?`, next, tmp); err != nil {
				return 0, fmt.Errorf("remap scope in %s: %w", table, err)
			}
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO settings(name,value) VALUES(?,?)`, marker, fmt.Sprint(len(moves))); err != nil {
		return 0, fmt.Errorf("record restore: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(moves), nil
}

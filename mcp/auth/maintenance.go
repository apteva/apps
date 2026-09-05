package main

import (
	"context"
	"database/sql"
	sdk "github.com/apteva/app-sdk"
	"time"
)

// Preserve spent refresh credentials until their family's absolute expiry for
// replay detection. Retention never removes a still-valid security credential.
func cleanupAuth(db *sql.DB, auditDays int, now time.Time) error {
	if auditDays < 7 || auditDays > 3650 {
		auditDays = 90
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	cutoff := rfc3339(now.Add(-24 * time.Hour))
	for _, q := range []struct {
		sql string
		arg any
	}{
		{`DELETE FROM auth_session_families WHERE expires_at<?`, cutoff},
		{`DELETE FROM sessions WHERE family_id IS NULL AND expires_at<?`, cutoff},
		{`DELETE FROM verification_tokens WHERE expires_at<?`, cutoff},
		{`DELETE FROM auth_rate_limits WHERE expires_at<?`, now.Unix()},
		{`DELETE FROM auth_recovery_jobs WHERE created_at<?`, now.Add(-7 * 24 * time.Hour).Unix()},
		{`DELETE FROM audit_log WHERE occurred_at<?`, rfc3339(now.Add(-time.Duration(auditDays) * 24 * time.Hour))},
	} {
		if _, err = tx.Exec(q.sql, q.arg); err != nil {
			return err
		}
	}
	return tx.Commit()
}
func (a *App) maintenance(_ context.Context, ctx *sdk.AppCtx) error {
	if err := cleanupAuth(ctx.AppDB(), cfgInt(ctx, "audit_retention_days", 90), time.Now()); err != nil {
		return err
	}
	return reconcileBrowserOrigins(ctx, envProject())
}

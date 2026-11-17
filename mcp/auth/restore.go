package main

import (
	"context"
	"database/sql"

	sdk "github.com/apteva/app-sdk"
)

// OnRestore runs before Auth serves data restored from a snapshot. The SDK
// has already moved every project_id; this rewrites the stored URLs that
// embed the old public base URL (client redirect URIs and origins, pending
// recovery links) and the acting app install recorded in the audit log.
// Client origins are re-registered with the platform at mount, so CORS
// follows the rewritten values.
func (a *App) OnRestore(ctx context.Context, _ *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) error {
	if tx == nil {
		return nil
	}
	if _, err := sdk.RewriteOriginColumns(ctx, tx, rc,
		"clients.redirect_uris", "clients.allowed_origins", "auth_recovery_jobs.continue_url"); err != nil {
		return err
	}
	_, err := sdk.RemapInstallIDColumns(ctx, tx, rc, "audit_log.source_install_id")
	return err
}

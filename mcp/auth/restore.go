package main

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// OnRestore runs before Auth serves data restored from a snapshot. The SDK
// has already moved every project_id; organization, user and client ids are
// copied unchanged. This hook:
//
//   - re-seals signing keys: a sealed private key is bound to its project as
//     associated data, so after the move it would no longer open and the
//     first mount would rotate every organization's key and revoke all
//     sessions;
//   - rewrites stored URLs that embed the old public base URL (client
//     redirect URIs and origins, pending recovery links, avatars); client
//     origins are re-registered with the platform at mount, so CORS follows;
//   - maps the acting app install recorded in the audit log.
func (a *App) OnRestore(ctx context.Context, app *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) error {
	if tx == nil {
		return nil
	}
	resealed, err := resealRestoredSigningKeys(ctx, app, tx, rc)
	if err != nil {
		return err
	}
	if _, err := sdk.RewriteOriginColumns(ctx, tx, rc,
		"clients.redirect_uris", "clients.allowed_origins", "auth_recovery_jobs.continue_url", "users.avatar_url"); err != nil {
		return err
	}
	if _, err := sdk.RemapInstallIDColumns(ctx, tx, rc, "audit_log.source_install_id"); err != nil {
		return err
	}
	if app != nil && resealed > 0 {
		app.Logger().Info("auth: re-sealed restored signing keys", "count", resealed)
	}
	return nil
}

// resealRestoredSigningKeys re-encrypts sealed private keys that DefaultRestore
// moved from the old project to the new one. A key that does not open under
// the old project (already sealed for the new one, or a lost KEK) is left as
// it is; mount handles an unreadable active key as it always has.
func resealRestoredSigningKeys(ctx context.Context, app *sdk.AppCtx, tx *sql.Tx, rc sdk.RestoreContext) (int, error) {
	if rc.OldProjectID == "" || rc.OldProjectID == rc.NewProjectID {
		return 0, nil
	}
	type sealed struct {
		id, oid     int64
		kid, stored string
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, COALESCE(organization_id, 0), kid, private_pem FROM signing_keys
		WHERE project_id = ? AND private_pem LIKE 'enc:v1:%'`, rc.NewProjectID)
	if err != nil {
		return 0, fmt.Errorf("read signing keys: %w", err)
	}
	var keys []sealed
	for rows.Next() {
		var k sealed
		if err := rows.Scan(&k.id, &k.oid, &k.kid, &k.stored); err != nil {
			rows.Close()
			return 0, err
		}
		keys = append(keys, k)
	}
	if err := rows.Close(); err != nil {
		return 0, err
	}
	if len(keys) == 0 {
		return 0, nil
	}
	// Same KEK resolution as OnMount: without AUTH_SIGNING_KEK or a data
	// directory there is no KEK to open the keys with.
	if strings.TrimSpace(os.Getenv("AUTH_SIGNING_KEK")) == "" && (app == nil || strings.TrimSpace(app.DataDir()) == "") {
		return 0, nil
	}
	aead, err := loadSigningKEK(app)
	if err != nil {
		return 0, fmt.Errorf("signing-key encryption: %w", err)
	}
	n := 0
	for _, k := range keys {
		plain, err := openSigningKeyWith(aead, rc.OldProjectID, k.oid, k.kid, k.stored)
		if err != nil {
			continue
		}
		next, err := sealSigningKeyWith(aead, rc.NewProjectID, k.oid, k.kid, plain)
		if err != nil {
			return n, err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE signing_keys SET private_pem = ? WHERE id = ?`, next, k.id); err != nil {
			return n, fmt.Errorf("re-seal signing key %s: %w", k.kid, err)
		}
		n++
	}
	return n, nil
}

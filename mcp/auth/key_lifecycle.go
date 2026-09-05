package main

import (
	"database/sql"
	"net/http"
	"time"
)

// Graceful rotation drains existing access tokens for at most their maximum
// permitted TTL. Emergency rotation removes every old key and online session.
func rotateSigningKey(db *sql.DB, pid string, oid int64, emergency bool) error {
	tx, err := beginAuthTx(db, pid, oid)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := time.Now().UTC()
	until := now.Add(24 * time.Hour)
	if emergency {
		until = now
	}
	if _, err = tx.Exec(`UPDATE signing_keys SET retired_at=COALESCE(retired_at,?),verify_until=? WHERE project_id=? AND organization_id=? AND (retired_at IS NULL OR ?)`, rfc3339(now), rfc3339(until), pid, oid, emergency); err != nil {
		return err
	}
	if _, err = tx.Exec(`UPDATE signing_keys SET private_pem='' WHERE project_id=? AND organization_id=? AND retired_at IS NOT NULL`, pid, oid); err != nil {
		return err
	}
	if err = ensureSigningKey(tx, pid, oid); err != nil {
		return err
	}
	if emergency {
		if _, err = tx.Exec(`UPDATE auth_session_families SET revoked_at=? WHERE project_id=? AND organization_id=?`, rfc3339(now), pid, oid); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE sessions SET revoked_at=? WHERE project_id=? AND organization_id=?`, rfc3339(now), pid, oid); err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE users SET authorization_version=authorization_version+1 WHERE project_id=? AND organization_id=?`, pid, oid); err != nil {
			return err
		}
	}
	dbAudit(tx, pid, oid, nil, "", "signing_key_rotated", "", "admin", map[string]any{"emergency": emergency})
	return tx.Commit()
}
func (a *App) handleAdminRotateSigningKey(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	var body struct {
		Emergency bool `json:"emergency"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, 400, "invalid JSON")
		return
	}
	if err := rotateSigningKey(getAppCtx(r).AppDB(), pid, org.ID, body.Emergency); err != nil {
		httpErr(w, 500, "rotation failed")
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}
func (a *App) handleAdminClientsUpdate(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	var args map[string]any
	if err = decodeRequest(w, r, &args); err != nil || args == nil {
		httpErr(w, 400, "invalid JSON")
		return
	}
	args["_project_id"] = pid
	args["client_id"] = r.PathValue("client_id")
	out, err := a.toolClientsUpdate(getAppCtx(r), args)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	httpJSON(w, out)
}

package main

import (
	"database/sql"
	"errors"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// maxAccessTokenTTL bounds how long a signed access token outlives a
// revocation for apps that only check the JWT signature. Longer configured
// lifetimes (install policy, org overrides, per-client values) are clamped at
// issue time; refresh keeps sessions alive across that window.
const maxAccessTokenTTL = 3600

type containedFamily struct {
	ID         string `json:"id"`
	ClientID   string `json:"client_id"`
	CreatedAt  string `json:"created_at"`
	LastSeenAt string `json:"last_seen_at"`
	ExpiresAt  string `json:"expires_at"`
}

// containUser is the single incident-response action for a user: it lists what
// is live, then in one transaction optionally disables the account, revokes
// every session family and refresh session, burns outstanding recovery and
// verification links, drops queued recovery mail, and bumps the user's
// authorization_version so session-checking apps reject existing access
// tokens immediately. Credentials Auth cannot revoke are reported with how
// long they can remain usable.
func containUser(ctx *sdk.AppCtx, pid string, org *Organization, uid int64, reason string, disable bool, src auditSource) (map[string]any, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return nil, errors.New("reason required")
	}
	if len(reason) > 500 {
		return nil, errors.New("reason too long")
	}
	fail := func(step string, err error) (map[string]any, error) {
		dbAudit(ctx.AppDB(), pid, org.ID, &uid, "", "user_containment_failed", src,
			map[string]any{"reason": reason, "step": step, "disable": disable})
		return map[string]any{"ok": false, "failed_step": step, "changes_applied": false}, err
	}
	tx, err := beginAuthTx(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return fail("begin", err)
	}
	defer tx.Rollback()
	user, err := dbGetUserByID(tx, pid, org.ID, uid)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("user not found")
	}
	if err != nil {
		return fail("load_user", err)
	}
	now := rfc3339(time.Now())
	families, err := listLiveFamilies(tx, pid, org.ID, uid, now)
	if err != nil {
		return fail("list_session_families", err)
	}
	counts := map[string]int64{}
	for key, q := range map[string]string{
		"sessions":            `SELECT COUNT(*) FROM sessions WHERE project_id=? AND organization_id=? AND user_id=? AND revoked_at IS NULL AND expires_at>?`,
		"verification_tokens": `SELECT COUNT(*) FROM verification_tokens WHERE project_id=? AND organization_id=? AND user_id=? AND used_at IS NULL AND expires_at>?`,
	} {
		var n int64
		if err = tx.QueryRow(q, pid, org.ID, uid, now).Scan(&n); err != nil {
			return fail("count_"+key, err)
		}
		counts[key] = n
	}
	statusBefore := user.Status
	if disable && user.Status != "disabled" {
		if _, err = tx.Exec(`UPDATE users SET status='disabled', updated_at=? WHERE project_id=? AND organization_id=? AND id=?`, now, pid, org.ID, uid); err != nil {
			return fail("disable_user", err)
		}
	}
	if _, err = revokeUserState(tx, pid, org.ID, uid); err != nil {
		return fail("revoke_sessions", err)
	}
	res, err := tx.Exec(`DELETE FROM auth_recovery_jobs WHERE project_id=? AND organization_id=? AND user_id=?`, pid, org.ID, uid)
	if err != nil {
		return fail("drop_recovery_jobs", err)
	}
	jobs, _ := res.RowsAffected()
	var version int64
	if err = tx.QueryRow(`SELECT authorization_version FROM users WHERE project_id=? AND organization_id=? AND id=?`, pid, org.ID, uid).Scan(&version); err != nil {
		return fail("read_authorization_version", err)
	}
	statusAfter := statusBefore
	if disable {
		statusAfter = "disabled"
	}
	revoked := map[string]any{
		"session_families":    len(families),
		"sessions":            counts["sessions"],
		"verification_tokens": counts["verification_tokens"],
		"recovery_jobs":       jobs,
	}
	dbAudit(tx, pid, org.ID, &uid, "", "user_contained", src, map[string]any{
		"reason": reason, "disable": disable, "status_before": statusBefore, "status_after": statusAfter,
		"revoked": revoked, "authorization_version": version,
	})
	if err = tx.Commit(); err != nil {
		return fail("commit", err)
	}
	return map[string]any{
		"ok":                    true,
		"user_id":               uid,
		"organization_id":       org.ID,
		"status_before":         statusBefore,
		"status":                statusAfter,
		"contained_at":          now,
		"reason":                reason,
		"actor":                 map[string]any{"type": src.ActorType, "id": src.ActorID, "source_app": src.SourceApp},
		"session_families":      families,
		"revoked":               revoked,
		"authorization_version": version,
		"outstanding": map[string]any{
			"access_tokens": map[string]any{
				"max_remaining_seconds": maxAccessTokenTTL,
				"note":                  "Signed access tokens stay valid until they expire for apps that only check the signature. Apps that call auth_verify_session or /me reject them now.",
			},
			"platform_delegated_tokens": map[string]any{
				"revocable_by_auth":     false,
				"max_remaining_seconds": int(delegatedMaxTTL / time.Second),
				"note":                  "Minted by the platform for at most 60 seconds; no new ones can be minted because every session family is revoked.",
			},
		},
	}, nil
}

func listLiveFamilies(db DBTX, pid string, oid, uid int64, now string) ([]containedFamily, error) {
	rows, err := db.Query(`SELECT id, client_id, created_at, last_seen_at, expires_at FROM auth_session_families
		WHERE project_id=? AND organization_id=? AND user_id=? AND revoked_at IS NULL AND expires_at>? ORDER BY created_at`, pid, oid, uid, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []containedFamily{}
	for rows.Next() {
		var f containedFamily
		if err := rows.Scan(&f.ID, &f.ClientID, &f.CreatedAt, &f.LastSeenAt, &f.ExpiresAt); err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

func (a *App) toolUsersContain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	org, err := orgFromArgs(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	uid, ok := intReq(args, "user_id")
	if !ok {
		return nil, errors.New("user_id required")
	}
	reason, _ := args["reason"].(string)
	out, err := containUser(ctx, pid, org, uid, reason, boolArg(args, "disable", true), auditFromArgs(args))
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (a *App) handleAdminUsersContain(w http.ResponseWriter, r *http.Request) {
	org, pid, ok := adminOrgInner(w, r, true)
	if !ok {
		return
	}
	uid, err := pathInt64(r, "id")
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Reason  string `json:"reason"`
		Disable *bool  `json:"disable"`
	}
	if err := decodeRequest(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	disable := body.Disable == nil || *body.Disable
	out, err := containUser(getAppCtx(r), pid, org, uid, body.Reason, disable, auditFromRequest(r))
	switch {
	case err == nil:
		httpJSON(w, out)
	case err.Error() == "user not found":
		httpErr(w, http.StatusNotFound, err.Error())
	case out == nil:
		httpErr(w, http.StatusBadRequest, err.Error())
	default:
		getAppCtx(r).Logger().Error("user containment failed", "step", out["failed_step"])
		httpStatus(w, http.StatusInternalServerError, out)
	}
}

package main

import (
	"database/sql"
	"errors"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func inAuthTx(db DBTX, pid string, oid int64, fn func(*sql.Tx) error) error {
	if tx, ok := db.(*sql.Tx); ok {
		return fn(tx)
	}
	conn, ok := db.(*sql.DB)
	if !ok {
		return errors.New("transaction requires database")
	}
	tx, err := beginAuthTx(conn, pid, oid)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err = fn(tx); err != nil {
		return err
	}
	return tx.Commit()
}

func mintSession(ctx *sdk.AppCtx, pid string, org *Organization, user *User, client *Client, r *http.Request) (tokenPair, error) {
	tx, err := beginAuthTx(ctx.AppDB(), pid, org.ID)
	if err != nil {
		return tokenPair{}, err
	}
	defer tx.Rollback()
	current, err := dbGetUserByID(tx, pid, org.ID, user.ID)
	if err != nil {
		return tokenPair{}, err
	}
	if current.AuthorizationVersion != user.AuthorizationVersion {
		return tokenPair{}, errors.New("credentials_changed: log in again")
	}
	pair, err := mintSessionTx(ctx, tx, pid, org.ID, user.ID, client.ClientID, r, "", time.Time{}, "")
	if err != nil {
		return tokenPair{}, err
	}
	if err = tx.Commit(); err != nil {
		return tokenPair{}, err
	}
	return pair, nil
}

// A family is the logical session; rotating credentials never extends its
// absolute lifetime. Validation and signing use one serialized DB snapshot.
func mintSessionTx(ctx *sdk.AppCtx, tx *sql.Tx, pid string, oid, uid int64, cid string, r *http.Request, family string, absolute time.Time, stableRefresh string) (tokenPair, error) {
	org, err := dbGetOrgByID(tx, pid, oid)
	if err != nil {
		return tokenPair{}, err
	}
	user, err := dbGetUserByID(tx, pid, oid, uid)
	if err != nil {
		return tokenPair{}, err
	}
	client, err := dbGetClientByClientID(tx, pid, cid)
	if err != nil {
		return tokenPair{}, err
	}
	if err = sessionEligibility(ctx, org, user, client); err != nil {
		return tokenPair{}, err
	}
	p, err := effectivePolicy(ctx, org)
	if err != nil {
		return tokenPair{}, err
	}
	accessSeconds := p.AccessSeconds
	refreshSeconds := p.RefreshDays * 86400
	if client.AccessTokenTTLSeconds > 0 {
		accessSeconds = client.AccessTokenTTLSeconds
	}
	if client.RefreshTokenTTLSeconds > 0 {
		refreshSeconds = client.RefreshTokenTTLSeconds
	}
	if accessSeconds < 60 || accessSeconds > 86400 || refreshSeconds < 60 || refreshSeconds > 90*86400 {
		return tokenPair{}, errors.New("invalid client token lifetime")
	}
	now := time.Now().UTC()
	if absolute.IsZero() {
		absolute = now.Add(time.Duration(refreshSeconds) * time.Second)
	}
	if !absolute.After(now) {
		return tokenPair{}, errors.New("invalid_grant")
	}
	accessExpiry := now.Add(time.Duration(accessSeconds) * time.Second)
	if accessExpiry.After(absolute) {
		accessExpiry = absolute
	}
	if family == "" {
		family, err = randSlug(24)
		if err != nil {
			return tokenPair{}, err
		}
		_, err = tx.Exec(`INSERT INTO auth_session_families(id,project_id,organization_id,user_id,client_id,created_at,last_seen_at,expires_at) VALUES(?,?,?,?,?,?,?,?)`, family, pid, oid, uid, cid, rfc3339(now), rfc3339(now), rfc3339(absolute))
		if err != nil {
			return tokenPair{}, err
		}
	}
	kid, priv, err := dbActiveSigningKey(tx, pid, oid)
	if err != nil {
		return tokenPair{}, err
	}
	authorization, err := dbAuthorizationContext(tx, pid, org, uid)
	if err != nil {
		return tokenPair{}, err
	}
	if len(authorization.Roles) > 64 || len(authorization.Permissions) > 256 {
		return tokenPair{}, errors.New("authorization context exceeds token limits")
	}
	issuer := orgBaseURL(ctx, r, org)
	if issuer == "" {
		return tokenPair{}, errors.New("public Auth URL is not configured")
	}
	aud := client.JWTAudience
	if aud == "" {
		aud = cid
	}
	claims := jwtClaims{Iss: issuer, Sub: uintToStr(uid), Aud: aud, Azp: cid, Iat: now.Unix(), Exp: accessExpiry.Unix(), Email: user.Email, EVer: user.EmailVerifiedAt != "", Extra: map[string]any{
		"sid": family, "token_use": "access", "org": org.Slug, "kind": userKindOrDefault(user.Kind), "user_id": authorization.UserID, "organization_id": authorization.OrganizationID, "organization_slug": org.Slug, "roles": authorization.Roles, "permissions": authorization.Permissions, "authorization_version": authorization.AuthorizationVersion,
	}}
	access, err := jwtSign(priv, kid, claims)
	if err != nil {
		return tokenPair{}, err
	}
	if len(access) > 12000 {
		return tokenPair{}, errors.New("authorization token too large")
	}
	refresh := stableRefresh
	if refresh == "" {
		refresh, err = randSlug(32)
		if err != nil {
			return tokenPair{}, err
		}
		_, err = tx.Exec(`INSERT INTO sessions(project_id,organization_id,user_id,client_id,refresh_token_hash,user_agent,ip,expires_at,family_id) VALUES(?,?,?,?,?,?,?,?,?)`, pid, oid, uid, cid, hashToken(refresh), requestUA(r), requestIP(r), rfc3339(absolute), family)
		if err != nil {
			return tokenPair{}, err
		}
	}
	_, err = tx.Exec(`UPDATE auth_session_families SET last_seen_at=? WHERE id=?`, rfc3339(now), family)
	if err != nil {
		return tokenPair{}, err
	}
	return tokenPair{access: access, refresh: refresh, expiresIn: int(accessExpiry.Sub(now).Seconds()), authorization: authorization}, nil
}

func refreshSession(ctx *sdk.AppCtx, pid string, c *Client, raw, hint string, r *http.Request) (tokenPair, *Organization, *User, error) {
	// Lookup only routes to the tenant; all authorization happens again in tx.
	var oid int64
	if err := ctx.AppDB().QueryRow(`SELECT organization_id FROM sessions WHERE project_id=? AND refresh_token_hash=?`, pid, hashToken(raw)).Scan(&oid); err != nil {
		return tokenPair{}, nil, nil, errors.New("invalid_grant")
	}
	tx, err := beginAuthTx(ctx.AppDB(), pid, oid)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	defer tx.Rollback()
	var id, uid int64
	var cid, family, expiry string
	var revoked sql.NullString
	err = tx.QueryRow(`SELECT id,user_id,client_id,IFNULL(family_id,''),expires_at,revoked_at FROM sessions WHERE project_id=? AND organization_id=? AND refresh_token_hash=?`, pid, oid, hashToken(raw)).Scan(&id, &uid, &cid, &family, &expiry, &revoked)
	if err != nil || cid != c.ClientID || family == "" {
		return tokenPair{}, nil, nil, errors.New("invalid_grant")
	}
	org, err := dbGetOrgByID(tx, pid, oid)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	if hint != "" && hint != org.Slug {
		return tokenPair{}, nil, nil, errors.New("invalid_grant")
	}
	var active int
	err = tx.QueryRow(`SELECT COUNT(*) FROM auth_session_families WHERE id=? AND revoked_at IS NULL AND expires_at>?`, family, rfc3339(time.Now())).Scan(&active)
	if err != nil || active != 1 {
		return tokenPair{}, nil, nil, errors.New("invalid_grant")
	}
	if revoked.Valid {
		if err := revokeFamily(tx, pid, family); err != nil {
			return tokenPair{}, nil, nil, err
		}
		if err := tx.Commit(); err != nil {
			return tokenPair{}, nil, nil, err
		}
		return tokenPair{}, nil, nil, errors.New("refresh_token_reuse: session revoked")
	}
	currentClient, err := dbGetClientByClientID(tx, pid, cid)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	if !hasGrant(currentClient, "refresh_token") {
		return tokenPair{}, nil, nil, errors.New("client_not_allowed")
	}
	absolute, err := time.Parse(time.RFC3339, expiry)
	if err != nil {
		return tokenPair{}, nil, nil, errors.New("invalid_grant")
	}
	stable := raw
	if currentClient.RefreshRotation {
		res, err := tx.Exec(`UPDATE sessions SET revoked_at=? WHERE id=? AND revoked_at IS NULL`, rfc3339(time.Now()), id)
		if err != nil {
			return tokenPair{}, nil, nil, err
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			return tokenPair{}, nil, nil, errors.New("invalid_grant")
		}
		stable = ""
	}
	pair, err := mintSessionTx(ctx, tx, pid, oid, uid, cid, r, family, absolute, stable)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	_, err = tx.Exec(`UPDATE sessions SET last_seen_at=? WHERE id=?`, rfc3339(time.Now()), id)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	user, err := dbGetUserByID(tx, pid, oid, uid)
	if err != nil {
		return tokenPair{}, nil, nil, err
	}
	if err = tx.Commit(); err != nil {
		return tokenPair{}, nil, nil, err
	}
	return pair, org, user, nil
}
func hasGrant(c *Client, g string) bool {
	for _, v := range c.AllowedGrantTypes {
		if v == g {
			return true
		}
	}
	return false
}
func revokeFamily(db DBTX, pid, family string) error {
	if conn, ok := db.(*sql.DB); ok {
		var oid int64
		if err := conn.QueryRow(`SELECT organization_id FROM auth_session_families WHERE project_id=? AND id=?`, pid, family).Scan(&oid); err != nil {
			return err
		}
		return inAuthTx(conn, pid, oid, func(tx *sql.Tx) error { return revokeFamily(tx, pid, family) })
	}

	if _, err := db.Exec(`UPDATE auth_session_families SET revoked_at=COALESCE(revoked_at,?) WHERE project_id=? AND id=?`, rfc3339(time.Now()), pid, family); err != nil {
		return err
	}
	_, err := db.Exec(`UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE project_id=? AND family_id=?`, rfc3339(time.Now()), pid, family)
	return err
}
func revokeUserState(tx *sql.Tx, pid string, oid, uid int64) (int64, error) {
	res, err := tx.Exec(`UPDATE auth_session_families SET revoked_at=? WHERE project_id=? AND organization_id=? AND user_id=? AND revoked_at IS NULL`, rfc3339(time.Now()), pid, oid, uid)
	if err != nil {
		return 0, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE sessions SET revoked_at=COALESCE(revoked_at,?) WHERE project_id=? AND organization_id=? AND user_id=?`, rfc3339(time.Now()), pid, oid, uid); err != nil {
		return 0, err
	}
	if _, err = tx.Exec(`UPDATE verification_tokens SET used_at=COALESCE(used_at,?) WHERE project_id=? AND organization_id=? AND user_id=?`, rfc3339(time.Now()), pid, oid, uid); err != nil {
		return 0, err
	}
	if err = incrementUserAuthorizationVersion(tx, pid, oid, uid); err != nil {
		return 0, err
	}
	return n, nil
}

func requestUA(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.UserAgent()
}
func requestIP(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.RemoteAddr
}

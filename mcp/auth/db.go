package main

// db.go — data access. Pure SQL; no business logic, no HTTP, no JWT.
// Functions return domain types or sql.ErrNoRows; callers translate.
//
// Convention: every function that touches user/session/client/etc.
// data takes (db, projectID, orgID, …). projectID is the platform-level
// partition; orgID is the row-level Organization partition added in
// v0.4.0. Both leading predicates appear on every read/write WHERE
// clause — a bare `WHERE project_id = ?` is the cross-org-leak smell.
//
// For project-wide reads (Overview stats, the org-list endpoint), pass
// orgID = 0 to opt out of org scoping. Writes always require orgID > 0
// and the call site enforces that.

import (
	"crypto/ed25519"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"
)

// ─── Organizations ───────────────────────────────────────────────────

func dbCreateOrg(db DBTX, pid, slug, name, color string) (int64, error) {
	if conn, ok := db.(*sql.DB); ok {
		tx, err := conn.Begin()
		if err != nil {
			return 0, err
		}
		defer tx.Rollback()
		id, err := dbCreateOrgRow(tx, pid, slug, name, color)
		if err != nil {
			return 0, err
		}
		if err = ensureSigningKey(tx, pid, id); err != nil {
			return 0, err
		}
		return id, tx.Commit()
	}
	id, err := dbCreateOrgRow(db, pid, slug, name, color)
	if err != nil {
		return 0, err
	}
	return id, ensureSigningKey(db, pid, id)
}
func dbCreateOrgRow(db DBTX, projectID, slug, name, color string) (int64, error) {
	if color == "" {
		color = "#94a3b8"
	}
	res, err := db.Exec(
		`INSERT INTO organizations(project_id, slug, name, color) VALUES(?,?,?,?)`,
		projectID, slug, name, color)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetOrgByID(db DBTX, projectID string, id int64) (*Organization, error) {
	row := db.QueryRow(`
		SELECT id, slug, name, IFNULL(color,''), status, IFNULL(policy_overrides,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM organizations WHERE project_id = ? AND id = ?`,
		projectID, id)
	return scanOrg(projectID, row)
}

func dbGetOrgBySlug(db DBTX, projectID, slug string) (*Organization, error) {
	row := db.QueryRow(`
		SELECT id, slug, name, IFNULL(color,''), status, IFNULL(policy_overrides,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM organizations WHERE project_id = ? AND slug = ?`,
		projectID, slug)
	return scanOrg(projectID, row)
}

func scanOrg(projectID string, row *sql.Row) (*Organization, error) {
	var o Organization
	if err := row.Scan(&o.ID, &o.Slug, &o.Name, &o.Color, &o.Status, &o.PolicyOverrides,
		&o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.ProjectID = projectID
	return &o, nil
}

func dbListOrgs(db DBTX, projectID string, includeArchived bool) ([]Organization, error) {
	q := `SELECT id, slug, name, IFNULL(color,''), status, IFNULL(policy_overrides,''),
	             IFNULL(created_at,''), IFNULL(updated_at,'')
	      FROM organizations WHERE project_id = ?`
	if !includeArchived {
		q += " AND status = 'active'"
	}
	q += " ORDER BY (slug = 'default') DESC, created_at ASC"
	rows, err := db.Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Organization
	for rows.Next() {
		var o Organization
		if err := rows.Scan(&o.ID, &o.Slug, &o.Name, &o.Color, &o.Status, &o.PolicyOverrides,
			&o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		o.ProjectID = projectID
		out = append(out, o)
	}
	return out, rows.Err()
}

func dbUpdateOrg(db DBTX, projectID string, id int64, name, color *string, policyOverrides *string) error {
	if err := validatePolicyJSON(policyOverrides); err != nil {
		return err
	}
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	if name != nil {
		sets = append(sets, "name = ?")
		args = append(args, *name)
	}
	if color != nil {
		sets = append(sets, "color = ?")
		args = append(args, *color)
	}
	if policyOverrides != nil {
		sets = append(sets, "policy_overrides = ?")
		if *policyOverrides == "" {
			args = append(args, sql.NullString{})
		} else {
			args = append(args, *policyOverrides)
		}
	}
	if len(sets) == 1 {
		return nil
	}
	args = append(args, projectID, id)
	_, err := db.Exec(
		`UPDATE organizations SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND id = ?`,
		args...)
	return err
}

func dbArchiveOrg(db DBTX, projectID string, id int64) error {
	res, err := db.Exec(
		`UPDATE organizations SET status = 'archived', updated_at = ? WHERE project_id = ? AND id = ? AND status = 'active'`,
		time.Now().UTC().Format(time.RFC3339), projectID, id)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("org not found or already archived")
	}
	return nil
}

// dbDefaultOrgID — lookup helper used during the v0.3.x → v0.4.x
// transition window. Legacy MCP calls and the deprecated /.well-known/*
// paths fall through to the default org of the calling project.
// Returns 0 if no default exists (shouldn't happen post-migration).
func dbDefaultOrgID(db DBTX, projectID string) int64 {
	var id int64
	_ = db.QueryRow(
		`SELECT id FROM organizations WHERE project_id = ? AND slug = 'default'`,
		projectID).Scan(&id)
	return id
}

// ─── Users ───────────────────────────────────────────────────────────

func dbGetUserByID(db DBTX, projectID string, orgID, id int64) (*User, error) {
	row := db.QueryRow(`
		SELECT id, IFNULL(organization_id,0), email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''),
		       COALESCE(metadata_json, '{}'), status, IFNULL(kind,'account'), password_hash IS NOT NULL,
		       authorization_version,
		       IFNULL(last_login_at,''), IFNULL(locked_until,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM users WHERE project_id = ? AND organization_id = ? AND id = ?`,
		projectID, orgID, id)
	return scanUser(db, projectID, row)
}

func dbGetUserByEmail(db DBTX, projectID string, orgID int64, email string) (*User, error) {
	row := db.QueryRow(`
		SELECT id, IFNULL(organization_id,0), email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''),
		       COALESCE(metadata_json, '{}'), status, IFNULL(kind,'account'), password_hash IS NOT NULL,
		       authorization_version,
		       IFNULL(last_login_at,''), IFNULL(locked_until,''),
		       IFNULL(created_at,''), IFNULL(updated_at,'')
		FROM users WHERE project_id = ? AND organization_id = ? AND email = ?`,
		projectID, orgID, strings.ToLower(strings.TrimSpace(email)))
	return scanUser(db, projectID, row)
}

func scanUser(db DBTX, projectID string, row *sql.Row) (*User, error) {
	var u User
	var hasPw int
	var metadata string
	if err := row.Scan(&u.ID, &u.OrganizationID, &u.Email, &u.EmailVerifiedAt, &u.DisplayName, &u.AvatarURL,
		&metadata, &u.Status, &u.Kind, &hasPw, &u.AuthorizationVersion,
		&u.LastLoginAt, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt); err != nil {
		return nil, err
	}
	if strings.TrimSpace(metadata) == "" {
		metadata = "{}"
	}
	u.Metadata = json.RawMessage(metadata)
	u.HasPassword = hasPw == 1
	normalizeUserKind(&u)
	u.ProjectID = projectID
	mfa, err := dbUserHasConfirmedMFA(db, u.ID)
	if err != nil {
		return nil, err
	}
	u.MFAEnabled = mfa
	return &u, nil
}

func dbUserHasConfirmedMFA(db DBTX, userID int64) (bool, error) {
	var n int
	err := db.QueryRow(
		`SELECT COUNT(*) FROM mfa_factors WHERE user_id = ? AND confirmed_at IS NOT NULL`,
		userID).Scan(&n)
	return n > 0, err
}

func dbCreateUser(db DBTX, projectID string, orgID int64, email, passwordHash, displayName string, emailVerified bool, metadataJSON string) (int64, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	verifiedAt := sql.NullString{}
	if emailVerified {
		verifiedAt = sql.NullString{Valid: true, String: now}
	}
	pw := sql.NullString{}
	if passwordHash != "" {
		pw = sql.NullString{Valid: true, String: passwordHash}
	}
	if strings.TrimSpace(metadataJSON) == "" {
		metadataJSON = "{}"
	}
	res, err := db.Exec(`
		INSERT INTO users(project_id, organization_id, email, password_hash, display_name, email_verified_at, metadata_json, status, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?, ?, 'active', ?, ?)`,
		projectID, orgID, strings.ToLower(strings.TrimSpace(email)), pw, displayName, verifiedAt, metadataJSON, now, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbGetUserPasswordHash(db DBTX, projectID string, orgID, userID int64) (string, error) {
	var h sql.NullString
	err := db.QueryRow(
		`SELECT password_hash FROM users WHERE project_id = ? AND organization_id = ? AND id = ?`,
		projectID, orgID, userID).Scan(&h)
	if err != nil {
		return "", err
	}
	if !h.Valid {
		return "", nil
	}
	return h.String, nil
}

func dbSetUserPassword(db DBTX, projectID string, orgID, userID int64, passwordHash string) error {
	_, err := db.Exec(
		`UPDATE users SET password_hash = ?, updated_at = ? WHERE project_id = ? AND organization_id = ? AND id = ?`,
		passwordHash, time.Now().UTC().Format(time.RFC3339), projectID, orgID, userID)
	return err
}

func dbMarkLoginSuccess(db DBTX, projectID string, orgID, userID int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := db.Exec(
		`UPDATE users SET last_login_at = ?, failed_login_count = 0, locked_until = NULL, updated_at = ? WHERE project_id = ? AND organization_id = ? AND id = ?`,
		now, now, projectID, orgID, userID)
	return err
}

func dbMarkLoginFailure(db DBTX, projectID string, orgID, userID int64, lockUntil time.Time) error {
	var until sql.NullString
	if !lockUntil.IsZero() {
		until = sql.NullString{Valid: true, String: lockUntil.UTC().Format(time.RFC3339)}
	}
	_, err := db.Exec(
		`UPDATE users
		   SET failed_login_count = failed_login_count + 1,
		       locked_until = COALESCE(?, locked_until),
		       updated_at = ?
		 WHERE project_id = ? AND organization_id = ? AND id = ?`,
		until, time.Now().UTC().Format(time.RFC3339), projectID, orgID, userID)
	return err
}

func dbSetUserStatus(db DBTX, pid string, oid, uid int64, status string) error {
	if status != "active" && status != "disabled" {
		return errors.New("invalid status")
	}
	return inAuthTx(db, pid, oid, func(tx *sql.Tx) error {
		var old string
		if err := tx.QueryRow(`SELECT status FROM users WHERE project_id=? AND organization_id=? AND id=?`, pid, oid, uid).Scan(&old); err != nil {
			return err
		}
		if old == status {
			return nil
		}
		if _, err := revokeUserState(tx, pid, oid, uid); err != nil {
			return err
		}
		_, err := tx.Exec(`UPDATE users SET status=?,updated_at=? WHERE project_id=? AND organization_id=? AND id=?`, status, rfc3339(time.Now()), pid, oid, uid)
		return err
	})
}

func dbUpdateUserProfile(db DBTX, projectID string, orgID, userID int64,
	displayName *string, markEmailVerified *bool, metadataJSON *string) error {
	sets := []string{"updated_at = ?"}
	args := []any{time.Now().UTC().Format(time.RFC3339)}
	if displayName != nil {
		sets = append(sets, "display_name = ?")
		args = append(args, *displayName)
	}
	if markEmailVerified != nil {
		sets = append(sets, "email_verified_at = ?")
		if *markEmailVerified {
			args = append(args, time.Now().UTC().Format(time.RFC3339))
		} else {
			args = append(args, sql.NullString{})
		}
	}
	if metadataJSON != nil {
		sets = append(sets, "metadata_json = ?")
		args = append(args, *metadataJSON)
	}
	if len(sets) == 1 {
		return nil
	}
	args = append(args, projectID, orgID, userID)
	_, err := db.Exec(
		`UPDATE users SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND organization_id = ? AND id = ?`,
		args...)
	return err
}

// dbSearchUsers — q is substring on email + display_name; status and
// createdAfter are optional filters. orgID = 0 = project-wide
// roll-up (Overview); orgID > 0 = scoped.
func dbSearchUsers(db DBTX, pid string, oid int64, q, status, after string, limit int) ([]User, error) {
	return dbSearchUsersPage(db, pid, oid, q, status, after, limit, nil, 0)
}
func dbSearchUsersPage(db DBTX, projectID string, orgID int64, q, status, createdAfter string, limit int, mfa *bool, before int64) ([]User, error) {
	args := []any{projectID}
	conds := []string{"project_id = ?"}
	if orgID > 0 {
		conds = append(conds, "organization_id = ?")
		args = append(args, orgID)
	}
	if q != "" {
		conds = append(conds, "(email LIKE ? OR display_name LIKE ?)")
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like)
	}
	if status != "" {
		conds = append(conds, "status = ?")
		args = append(args, status)
	}
	if createdAfter != "" {
		conds = append(conds, "julianday(created_at) > julianday(?)")
		args = append(args, createdAfter)
	}
	if limit < 1 || limit > 501 {
		return nil, errors.New("limit out of range")
	}
	if createdAfter != "" {
		if _, err := time.Parse(time.RFC3339, createdAfter); err != nil {
			return nil, errors.New("invalid created_after timestamp")
		}
	}
	if mfa != nil {
		conds = append(conds, "EXISTS(SELECT 1 FROM mfa_factors f WHERE f.user_id=users.id AND f.confirmed_at IS NOT NULL) = ?")
		args = append(args, *mfa)
	}
	if before > 0 {
		conds = append(conds, "id < ?")
		args = append(args, before)
	}
	args = append(args, limit)
	q1 := "SELECT id, IFNULL(organization_id,0), email, IFNULL(email_verified_at,''), IFNULL(display_name,''), IFNULL(avatar_url,''), COALESCE(metadata_json, '{}'), status, IFNULL(kind,'account'), password_hash IS NOT NULL, authorization_version, IFNULL(last_login_at,''), IFNULL(locked_until,''), IFNULL(created_at,''), IFNULL(updated_at,''), EXISTS(SELECT 1 FROM mfa_factors f WHERE f.user_id=users.id AND f.confirmed_at IS NOT NULL) FROM users WHERE " + strings.Join(conds, " AND ") + " ORDER BY id DESC LIMIT ?"
	rows, err := db.Query(q1, args...)
	if err != nil {
		return nil, err
	}
	// Drain into a slice before issuing the per-row MFA query — the
	// outer Rows holds a connection open until we Close() it, and on
	// SQLite the inner QueryRow can wait on the same connection and
	// deadlock under a small pool. Drain first, then loop.
	var out []User
	for rows.Next() {
		var u User
		var hasPw int
		var metadata string
		if err := rows.Scan(&u.ID, &u.OrganizationID, &u.Email, &u.EmailVerifiedAt, &u.DisplayName, &u.AvatarURL,
			&metadata, &u.Status, &u.Kind, &hasPw, &u.AuthorizationVersion,
			&u.LastLoginAt, &u.LockedUntil, &u.CreatedAt, &u.UpdatedAt, &u.MFAEnabled); err != nil {
			rows.Close()
			return nil, err
		}
		if strings.TrimSpace(metadata) == "" {
			metadata = "{}"
		}
		u.Metadata = json.RawMessage(metadata)
		u.HasPassword = hasPw == 1
		normalizeUserKind(&u)
		u.ProjectID = projectID
		out = append(out, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return out, nil
}

// ─── Sessions ────────────────────────────────────────────────────────

func dbCreateSession(db DBTX, projectID string, orgID, userID int64, clientID, refreshHash, ua, ip string, expiresAt time.Time) (int64, error) {
	res, err := db.Exec(
		`INSERT INTO sessions(project_id, organization_id, user_id, client_id, refresh_token_hash, user_agent, ip, expires_at)
		 VALUES(?,?,?,?,?,?,?,?)`,
		projectID, orgID, userID, clientID, refreshHash, ua, ip, expiresAt.UTC().Format(time.RFC3339))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dbFindActiveSessionByRefresh — sessions are looked up by refresh-token
// hash, which has its own UNIQUE index, so we don't need org scoping
// in the query — but we still return the org_id so callers can verify
// the session belongs to the org they expected (defense-in-depth
// against a token from one org being presented to another's refresh).
func dbFindActiveSessionByRefresh(db DBTX, projectID, refreshHash string) (*Session, error) {
	row := db.QueryRow(`
		SELECT id, IFNULL(organization_id,0), user_id, client_id, IFNULL(user_agent,''), IFNULL(ip,''),
		       IFNULL(created_at,''), IFNULL(last_seen_at,''), IFNULL(expires_at,''), IFNULL(revoked_at,'')
		FROM sessions
		WHERE project_id = ? AND refresh_token_hash = ?
		  AND revoked_at IS NULL
		  AND expires_at > ?`,
		projectID, refreshHash, time.Now().UTC().Format(time.RFC3339))
	var s Session
	if err := row.Scan(&s.ID, &s.OrganizationID, &s.UserID, &s.ClientID, &s.UserAgent, &s.IP,
		&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func dbRevokeSession(db DBTX, projectID string, orgID, id int64) error {
	_, err := db.Exec(
		`UPDATE sessions SET revoked_at = ? WHERE project_id = ? AND organization_id = ? AND id = ? AND revoked_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, orgID, id)
	return err
}

func dbRevokeAllUserSessions(db DBTX, pid string, oid, uid int64) (int64, error) {
	var n int64
	err := inAuthTx(db, pid, oid, func(tx *sql.Tx) error { var err error; n, err = revokeUserState(tx, pid, oid, uid); return err })
	return n, err
}

func dbListUserSessions(db DBTX, projectID string, orgID, userID int64) ([]Session, error) {
	rows, err := db.Query(`
		SELECT id, IFNULL(organization_id,0), user_id, client_id, IFNULL(user_agent,''), IFNULL(ip,''),
		       IFNULL(created_at,''), IFNULL(last_seen_at,''), IFNULL(expires_at,''), IFNULL(revoked_at,'')
		FROM sessions
		WHERE project_id = ? AND organization_id = ? AND user_id = ?
		ORDER BY (revoked_at IS NULL AND expires_at > strftime('%Y-%m-%dT%H:%M:%SZ','now')) DESC, id DESC LIMIT 200`,
		projectID, orgID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.OrganizationID, &s.UserID, &s.ClientID, &s.UserAgent, &s.IP,
			&s.CreatedAt, &s.LastSeenAt, &s.ExpiresAt, &s.RevokedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ─── Clients ─────────────────────────────────────────────────────────

// dbCreateClient — orgID = 0 creates a **multi-organization** client.
// Multi-org clients are usable across every org in the project; the
// SaaS frontend must send organization_slug (or _id) on every public
// call. orgID > 0 creates a single-org client (v0.4.0 default).
func dbCreateClient(db DBTX, projectID string, orgID int64, c Client, secretHash string) (int64, error) {
	redirects, _ := json.Marshal(c.RedirectURIs)
	origins, _ := json.Marshal(c.AllowedOrigins)
	grants, _ := json.Marshal(c.AllowedGrantTypes)
	requirePKCE := 0
	if c.RequirePKCE {
		requirePKCE = 1
	}
	requireMFA := 0
	if c.RequireMFA {
		requireMFA = 1
	}
	rotation := 1
	if !c.RefreshRotation {
		rotation = 0
	}
	// orgID = 0 → NULL column; otherwise the FK.
	var orgArg any
	if orgID > 0 {
		orgArg = orgID
	}
	res, err := db.Exec(`
		INSERT INTO clients(project_id, organization_id, client_id, client_secret_hash, name, type,
			redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method,
			require_pkce, require_mfa, jwt_audience,
			access_token_ttl_seconds, refresh_token_ttl_seconds, refresh_rotation)
		VALUES(?,?,?,?,?,?, ?,?,?,?, ?,?,?, ?,?,?)`,
		projectID, orgArg, c.ClientID, nullStr(secretHash), c.Name, c.Type,
		string(redirects), string(origins), string(grants), c.TokenEndpointAuthMethod,
		requirePKCE, requireMFA, nullStr(c.JWTAudience),
		nullInt(c.AccessTokenTTLSeconds), nullInt(c.RefreshTokenTTLSeconds), rotation)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// dbGetClientByClientID — client_id is globally unique (UNIQUE index
// on the column alone), so org-scoping isn't needed for the lookup.
// We return the row's organization_id so the caller scopes everything
// downstream to that org.
func dbGetClientByClientID(db DBTX, projectID, clientID string) (*Client, error) {
	row := db.QueryRow(`
		SELECT id, IFNULL(organization_id,0), client_id, name, type,
		       redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method,
		       require_pkce, require_mfa, IFNULL(jwt_audience,''),
		       IFNULL(access_token_ttl_seconds, 0), IFNULL(refresh_token_ttl_seconds, 0), refresh_rotation,
		       IFNULL(disabled_at,''), IFNULL(created_at,'')
		FROM clients WHERE project_id = ? AND client_id = ?`,
		projectID, clientID)
	return scanClient(row)
}

func dbSetClientAllowedOrigins(db DBTX, projectID, clientID string, origins []string) error {
	encoded, err := json.Marshal(origins)
	if err != nil {
		return err
	}
	res, err := db.Exec(`UPDATE clients SET allowed_origins = ? WHERE project_id = ? AND client_id = ?`, string(encoded), projectID, clientID)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected != 1 {
		return sql.ErrNoRows
	}
	return nil
}

func scanClient(row *sql.Row) (*Client, error) {
	var c Client
	var redirects, origins, grants string
	var requirePKCE, requireMFA, rotation int
	if err := row.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.Type,
		&redirects, &origins, &grants, &c.TokenEndpointAuthMethod,
		&requirePKCE, &requireMFA, &c.JWTAudience,
		&c.AccessTokenTTLSeconds, &c.RefreshTokenTTLSeconds, &rotation,
		&c.DisabledAt, &c.CreatedAt); err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(redirects), &c.RedirectURIs)
	_ = json.Unmarshal([]byte(origins), &c.AllowedOrigins)
	_ = json.Unmarshal([]byte(grants), &c.AllowedGrantTypes)
	c.RequirePKCE = requirePKCE == 1
	c.RequireMFA = requireMFA == 1
	c.RefreshRotation = rotation == 1
	return &c, nil
}

func dbVerifyClientSecret(db DBTX, projectID, clientID, secret string) (bool, error) {
	var h sql.NullString
	if err := db.QueryRow(
		`SELECT client_secret_hash FROM clients WHERE project_id = ? AND client_id = ? AND disabled_at IS NULL`,
		projectID, clientID).Scan(&h); err != nil {
		return false, err
	}
	if !h.Valid {
		return false, nil
	}
	return subtle.ConstantTimeCompare([]byte(h.String), []byte(hashToken(secret))) == 1, nil
}

// dbListClients — orgID = 0 = project-wide (every org's clients).
// orgID > 0 = scoped to that org.
func dbListClients(db DBTX, projectID string, orgID int64, includeDisabled bool) ([]Client, error) {
	conds := []string{"project_id = ?"}
	args := []any{projectID}
	if orgID > 0 {
		// Org-scoped read includes multi-org clients (organization_id
		// IS NULL) too — they're usable from any org so they should
		// surface in every org's client list. The flat project-wide
		// view (orgID = 0) lists everything regardless.
		conds = append(conds, "(organization_id = ? OR organization_id IS NULL)")
		args = append(args, orgID)
	}
	if !includeDisabled {
		conds = append(conds, "disabled_at IS NULL")
	}
	q := `SELECT id, IFNULL(organization_id,0), client_id, name, type, redirect_uris, allowed_origins, allowed_grant_types, token_endpoint_auth_method, require_pkce, require_mfa, IFNULL(jwt_audience,''), IFNULL(access_token_ttl_seconds,0), IFNULL(refresh_token_ttl_seconds,0), refresh_rotation, IFNULL(disabled_at,''), IFNULL(created_at,'') FROM clients WHERE ` + strings.Join(conds, " AND ") + ` ORDER BY created_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Client
	for rows.Next() {
		var c Client
		var redirects, origins, grants string
		var requirePKCE, requireMFA, rotation int
		if err := rows.Scan(&c.ID, &c.OrganizationID, &c.ClientID, &c.Name, &c.Type,
			&redirects, &origins, &grants, &c.TokenEndpointAuthMethod,
			&requirePKCE, &requireMFA, &c.JWTAudience,
			&c.AccessTokenTTLSeconds, &c.RefreshTokenTTLSeconds, &rotation,
			&c.DisabledAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(redirects), &c.RedirectURIs)
		_ = json.Unmarshal([]byte(origins), &c.AllowedOrigins)
		_ = json.Unmarshal([]byte(grants), &c.AllowedGrantTypes)
		c.RequirePKCE = requirePKCE == 1
		c.RequireMFA = requireMFA == 1
		c.RefreshRotation = rotation == 1
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbUpdateClientSecret(db DBTX, projectID, clientID, newHash string) error {
	res, err := db.Exec(
		`UPDATE clients SET client_secret_hash = ? WHERE project_id = ? AND client_id = ?`,
		newHash, projectID, clientID)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return errors.New("client not found")
	}
	return nil
}

func dbDisableClient(db DBTX, projectID, clientID string) error {
	_, err := db.Exec(
		`UPDATE clients SET disabled_at = ? WHERE project_id = ? AND client_id = ? AND disabled_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, clientID)
	return err
}

// ─── Signing keys ────────────────────────────────────────────────────
//
// Per-org keys. Each org has its own active EdDSA keypair; a compromise
// of org-A's private key burns only org-A's tokens. JWKS publishes the
// keys for one org at a time (the discovery URL is org-prefixed too).

func dbActiveSigningKey(db DBTX, projectID string, orgID int64) (kid string, priv ed25519.PrivateKey, err error) {
	var privPEM string
	err = db.QueryRow(`
		SELECT kid, private_pem FROM signing_keys
		WHERE project_id = ? AND organization_id = ? AND retired_at IS NULL
		ORDER BY created_at DESC LIMIT 1`,
		projectID, orgID).Scan(&kid, &privPEM)
	if err != nil {
		return "", nil, err
	}
	priv, err = parseEd25519Private(privPEM)
	return
}

func dbAllSigningKeys(db DBTX, projectID string, orgID int64) (map[string]ed25519.PublicKey, error) {
	rows, err := db.Query(
		`SELECT kid, public_pem FROM signing_keys WHERE project_id = ? AND organization_id = ? AND (retired_at IS NULL OR verify_until>?)`,
		projectID, orgID, rfc3339(time.Now()))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]ed25519.PublicKey{}
	for rows.Next() {
		var kid, pubPEM string
		if err := rows.Scan(&kid, &pubPEM); err != nil {
			return nil, err
		}
		pub, err := parseEd25519Public(pubPEM)
		if err != nil {
			return nil, fmt.Errorf("kid %s: %w", kid, err)
		}
		out[kid] = pub
	}
	return out, rows.Err()
}

// ─── Verification tokens ─────────────────────────────────────────────

func dbInsertVerificationToken(db DBTX, projectID string, orgID, userID int64, kind, tokenHash, meta string, expiresAt time.Time) error {
	_, err := db.Exec(`
		INSERT INTO verification_tokens(project_id, organization_id, user_id, token_hash, kind, meta, expires_at)
		VALUES(?,?,?,?,?,?,?)`,
		projectID, orgID, userID, tokenHash, kind, nullStr(meta), expiresAt.UTC().Format(time.RFC3339))
	return err
}

// dbConsumeVerificationToken validates the tenant, flow kind, and issuing
// client before marking a token used. Keeping validation and consumption in
// one transaction prevents a token presented to the wrong community client
// from being burned and makes concurrent redemption single-use.
func dbConsumeVerificationToken(db *sql.DB, pid, hash string, oid int64, kind, cid string) (int64, string, error) {
	tx, err := beginAuthTx(db, pid, oid)
	if err != nil {
		return 0, "", err
	}
	defer tx.Rollback()
	uid, meta, err := consumeVerificationTokenTx(tx, pid, hash, oid, kind, cid)
	if err != nil {
		return 0, "", err
	}
	return uid, meta, tx.Commit()
}
func consumeVerificationTokenTx(tx *sql.Tx, projectID, tokenHash string, expectedOrgID int64, expectedKind, expectedClientID string) (userID int64, meta string, err error) {
	var expiresAt, used sql.NullString
	var metaNS sql.NullString
	var oid sql.NullInt64
	var kind string
	err = tx.QueryRow(`
		SELECT user_id, organization_id, kind, meta, expires_at, used_at
		FROM verification_tokens
		WHERE project_id = ? AND token_hash = ?`,
		projectID, tokenHash).Scan(&userID, &oid, &kind, &metaNS, &expiresAt, &used)
	if err != nil {
		return 0, "", err
	}
	if !oid.Valid || oid.Int64 != expectedOrgID || kind != expectedKind {
		return 0, "", errors.New("token does not belong to this client")
	}
	if metaNS.Valid && strings.TrimSpace(metaNS.String) != "" && expectedClientID != "" {
		var opts recoveryLinkOptions
		if json.Unmarshal([]byte(metaNS.String), &opts) != nil || (opts.ClientID != "" && opts.ClientID != expectedClientID) {
			return 0, "", errors.New("token does not belong to this client")
		}
	}
	if used.Valid && used.String != "" {
		return 0, "", errors.New("token already used")
	}
	if !expiresAt.Valid {
		return 0, "", errors.New("token expired")
	}
	if t, e := time.Parse(time.RFC3339, expiresAt.String); e != nil || !t.After(time.Now()) {
		return 0, "", errors.New("token expired")
	}
	result, err := tx.Exec(
		`UPDATE verification_tokens SET used_at = ? WHERE project_id = ? AND token_hash = ? AND used_at IS NULL`,
		time.Now().UTC().Format(time.RFC3339), projectID, tokenHash)
	if err != nil {
		return 0, "", err
	}
	if changed, err := result.RowsAffected(); err != nil || changed != 1 {
		return 0, "", errors.New("token already used")
	}
	if metaNS.Valid {
		meta = metaNS.String
	}
	return userID, meta, nil
}

func dbMarkEmailVerified(db DBTX, projectID string, orgID, userID int64) error {
	_, err := db.Exec(
		`UPDATE users SET email_verified_at = COALESCE(email_verified_at, ?), updated_at = ? WHERE project_id = ? AND organization_id = ? AND id = ?`,
		time.Now().UTC().Format(time.RFC3339), time.Now().UTC().Format(time.RFC3339), projectID, orgID, userID)
	return err
}

// ─── Audit log ───────────────────────────────────────────────────────

// dbAudit — orgID may be 0 for genuinely project-wide events (install
// start, org create); user-scoped events should pass the user's org.
func dbAudit(db DBTX, projectID string, orgID int64, userID *int64, clientID, event, ip, ua string, metadata map[string]any) {
	var meta sql.NullString
	if len(metadata) > 0 {
		if b, err := json.Marshal(metadata); err == nil {
			meta = sql.NullString{Valid: true, String: string(b)}
		}
	}
	var uid sql.NullInt64
	if userID != nil {
		uid = sql.NullInt64{Valid: true, Int64: *userID}
	}
	var oid any
	if orgID > 0 {
		oid = orgID
	}
	_, err := db.Exec(`
		INSERT INTO audit_log(project_id, organization_id, user_id, client_id, event, ip, user_agent, metadata)
		VALUES(?,?,?,?,?,?,?,?)`,
		projectID, oid, uid, nullStr(clientID), event, nullStr(ip), nullStr(ua), meta)
	if err != nil {
		log.Printf("auth audit write failed: event=%q project=%q", event, projectID)
	}
}

// dbAuditSearch — orgID = 0 = project-wide.
func dbAuditSearch(db DBTX, projectID string, orgID, userID int64, event, since, until string, limit int) ([]AuditEvent, error) {
	for _, v := range []string{since, until} {
		if v != "" {
			if _, err := time.Parse(time.RFC3339, v); err != nil {
				return nil, errors.New("invalid audit timestamp")
			}
		}
	}
	args := []any{projectID}
	conds := []string{"project_id = ?"}
	if orgID > 0 {
		conds = append(conds, "organization_id = ?")
		args = append(args, orgID)
	}
	if userID > 0 {
		conds = append(conds, "user_id = ?")
		args = append(args, userID)
	}
	if event != "" {
		conds = append(conds, "event = ?")
		args = append(args, event)
	}
	if since != "" {
		conds = append(conds, "julianday(occurred_at) >= julianday(?)")
		args = append(args, since)
	}
	if until != "" {
		conds = append(conds, "julianday(occurred_at) <= julianday(?)")
		args = append(args, until)
	}
	args = append(args, limit)
	q := "SELECT id, organization_id, user_id, IFNULL(client_id,''), event, IFNULL(ip,''), IFNULL(user_agent,''), IFNULL(metadata,''), occurred_at FROM audit_log WHERE " + strings.Join(conds, " AND ") + " ORDER BY occurred_at DESC LIMIT ?"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AuditEvent
	for rows.Next() {
		var a AuditEvent
		var uid, oid sql.NullInt64
		if err := rows.Scan(&a.ID, &oid, &uid, &a.ClientID, &a.Event, &a.IP, &a.UserAgent, &a.Metadata, &a.OccurredAt); err != nil {
			return nil, err
		}
		if uid.Valid {
			v := uid.Int64
			a.UserID = &v
		}
		if oid.Valid {
			v := oid.Int64
			a.OrganizationID = &v
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ─── Stats ───────────────────────────────────────────────────────────

type Stats struct {
	Active    int `json:"active"`
	Disabled  int `json:"disabled"`
	Locked    int `json:"locked"`
	Signups7d int `json:"signups_7d"`
	Logins24h int `json:"logins_24h"`
}

// dbStats — orgID = 0 = project-wide rollup (Overview tab); orgID > 0
// = single-org card. Either way the SaaS-facing semantics are the
// same: user counts and login activity over the rolling windows.
func dbStats(db DBTX, projectID string, orgID int64) (Stats, error) {
	var s Stats
	var queryErr error
	weekAgo := time.Now().Add(-7 * 24 * time.Hour).UTC().Format(time.RFC3339)
	dayAgo := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	now := time.Now().UTC().Format(time.RFC3339)
	scope := "project_id = ?"
	scopeArgs := []any{projectID}
	if orgID > 0 {
		scope += " AND organization_id = ?"
		scopeArgs = append(scopeArgs, orgID)
	}
	q := func(extra string, extraArgs ...any) int {
		if queryErr != nil {
			return 0
		}
		var n int
		args := append([]any{}, scopeArgs...)
		args = append(args, extraArgs...)
		queryErr = db.QueryRow("SELECT COUNT(*) FROM users WHERE "+scope+extra, args...).Scan(&n)
		return n
	}
	s.Active = q(" AND status = 'active'")
	s.Disabled = q(" AND status = 'disabled'")
	s.Locked = q(" AND locked_until IS NOT NULL AND locked_until > ?", now)
	s.Signups7d = q(" AND created_at > ?", weekAgo)
	if queryErr == nil {
		var n int
		args := append([]any{}, scopeArgs...)
		args = append(args, dayAgo)
		queryErr = db.QueryRow("SELECT COUNT(*) FROM audit_log WHERE "+scope+" AND event IN ('login','identity_login') AND occurred_at > ?", args...).Scan(&n)
		s.Logins24h = n
	}
	return s, queryErr
}

// ─── tiny helpers ────────────────────────────────────────────────────

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt(n int) any {
	if n <= 0 {
		return nil
	}
	return n
}
